package forker_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/forker"
)

// instrumentedMerger is a tool.ForkMerger test double that flags concurrent
// entry: it increments inFlight on entry, sleeps, and records the max observed
// concurrency. With a serializing wrapper, maxConcurrent must never exceed 1.
type instrumentedMerger struct {
	inFlight      atomic.Int32
	maxConcurrent atomic.Int32
	calls         atomic.Int32
	sleep         time.Duration
	ret           error
}

func (m *instrumentedMerger) Merge(_ context.Context, _ string, _ tool.Workspace) error {
	m.calls.Add(1)
	n := m.inFlight.Add(1)
	// Track the high-water mark of concurrent entries.
	for {
		prev := m.maxConcurrent.Load()
		if n <= prev || m.maxConcurrent.CompareAndSwap(prev, n) {
			break
		}
	}
	time.Sleep(m.sleep)
	m.inFlight.Add(-1)
	return m.ret
}

// TestSerializingMergerSerializes runs many concurrent Merge calls through the
// serializing wrapper over an instrumented inner that would observe overlap if any
// occurred, and asserts the inner never sees more than one concurrent call. Run
// under -race to also catch any data race in the decorator itself.
func TestSerializingMergerSerializes(t *testing.T) {
	t.Parallel()
	inner := &instrumentedMerger{sleep: 2 * time.Millisecond}
	sm := forker.NewSerializingMerger(inner)

	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if err := sm.Merge(context.Background(), "/fork", nil); err != nil {
				t.Errorf("Merge returned unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := inner.calls.Load(); got != goroutines {
		t.Fatalf("inner Merge called %d times, want %d", got, goroutines)
	}
	if got := inner.maxConcurrent.Load(); got > 1 {
		t.Fatalf("serializing merger allowed %d concurrent inner Merge calls; want at most 1", got)
	}
}

// TestSerializingMergerForwardsResult asserts the wrapper forwards the inner's
// return value verbatim — both the nil (success) and the error path.
func TestSerializingMergerForwardsResult(t *testing.T) {
	t.Parallel()

	// Success path.
	if err := forker.NewSerializingMerger(&instrumentedMerger{}).Merge(context.Background(), "/fork", nil); err != nil {
		t.Fatalf("Merge over a nil-returning inner = %v, want nil", err)
	}

	// Error path: the exact error instance must be returned.
	sentinel := errors.New("merge conflict")
	err := forker.NewSerializingMerger(&instrumentedMerger{ret: sentinel}).Merge(context.Background(), "/fork", nil)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Merge over an erroring inner = %v, want %v", err, sentinel)
	}
}

// Compile-time assertion the decorator satisfies the seam (mirrors the package's
// own _ tool.ForkMerger assertion; cheap belt-and-braces for the test build).
var _ tool.ForkMerger = (*forker.SerializingMerger)(nil)

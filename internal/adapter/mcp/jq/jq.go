// Package jq is a sandboxed wrapper around github.com/itchyny/gojq that
// evaluates a jq filter against a JSON input and returns the JSON-stringified
// result. It exists to narrow a remote tool's JSON result before it enters
// model context, so a large response does not blow the context budget or get
// truncated into an unparseable blob.
//
// Sandboxing: the [gojq] query is constructed via [gojq.Parse] and run with
// [gojq.Query.RunWithContext] WITHOUT [gojq.WithModuleLoader],
// [gojq.WithInputIter], or [gojq.WithEnvironLoader]. The query therefore
// cannot read files, the environment, or stdin. A context deadline bounds
// pathological compute (e.g. `while(1; .+1)`); a context without a deadline is
// capped at [DefaultTimeout].
package jq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/itchyny/gojq"
)

// MaxInputBytes is the cap on the size of the input passed to [Run]. It
// matches session.MaxToolResultBytes (20 MiB): if a remote tool's JSON result
// is larger than this, filtering it in memory is unsafe, and the caller should
// narrow the remote call rather than rely on jq.
const MaxInputBytes = 20 << 20

// MaxOutputBytes is the cap on the JSON-encoded size of the filtered result.
// The filtered subset a caller actually needs should be small; a jq filter
// that produces more than ~100 KiB is too broad and is rejected so it does not
// simply move the context-budget problem from the input to the output.
const MaxOutputBytes = 100_000

// DefaultTimeout is applied when the context passed to [Run] carries no
// deadline, so a pathological filter cannot run unbounded.
const DefaultTimeout = 5 * time.Second

// MaxHeapGrowthBytes bounds the heap a single [Run] may cause to grow before it
// is cancelled (CWE-770/400). The output cap ([MaxOutputBytes]) only fires
// AFTER each yielded value is materialized, so a filter that builds one huge
// value (e.g. `[range(1e8)]`) allocates it in full inside gojq before the cap is
// ever checked; without a memory bound, only [DefaultTimeout] limits it, and at
// gojq's allocation rate that is ~1 GiB before the deadline — enough to OOM a
// memory-constrained pod. gojq honours context cancellation during value
// construction, so [Run] watches heap growth and cancels the run once it crosses
// this budget. 256 MiB sits an order of magnitude above the [MaxInputBytes]
// (20 MiB) working set a legitimate filter needs while staying below a typical
// pod limit; a filter that needs more should narrow the remote call instead.
const MaxHeapGrowthBytes = 256 << 20

// maxHeapGrowth and heapSampleInterval mirror the tuning constants as package
// vars so tests can lower them (a real 256 MiB / multi-second exercise is too
// heavy for CI); production always uses the [MaxHeapGrowthBytes] default.
var (
	maxHeapGrowth      uint64 = MaxHeapGrowthBytes
	heapSampleInterval        = 50 * time.Millisecond
)

// Validate rejects an invalid jq expression before a remote operation begins.
func Validate(filter string) error {
	query, err := gojq.Parse(filter)
	if err != nil {
		return fmt.Errorf("jq parse error: %w", err)
	}
	_, err = gojq.Compile(query)
	return err
}

// Run evaluates a jq filter against a JSON input and returns the
// JSON-stringified result.
//
// Output shape: a jq filter can yield multiple values (e.g. `.items[]` yields
// one value per element). [Run] collects all yielded values and returns:
//   - exactly one value: that value, JSON-encoded (so a string result is
//     returned as a quoted JSON string, e.g. `"hello"`),
//   - more than one value: a JSON array wrapping every value, JSON-encoded,
//   - zero values: the literal `null`.
//
// The returned string is always JSON-shaped: every yielded value is passed
// through [json.Marshal], so a single gojq string output is quoted rather than
// returned raw. Callers parsing the result with [json.Unmarshal] get a
// consistent shape.
//
// Bounds:
//   - input <= [MaxInputBytes] (loud error if exceeded — don't load huge input),
//   - output <= [MaxOutputBytes] (loud error if the filtered result is huge),
//   - ctx should carry a deadline; if none, [DefaultTimeout] is applied.
//
// All error messages are loud and self-describing (they are surfaced to a
// model/operator), wrapping the underlying cause where useful.
func Run(ctx context.Context, filter string, input []byte) (string, error) {
	if len(input) > MaxInputBytes {
		return "", fmt.Errorf("jq input exceeded the %d-byte cap (%d bytes); narrow the call rather than filtering in memory", MaxInputBytes, len(input))
	}

	// Bound pathological compute when the caller supplied no deadline.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	query, err := gojq.Parse(filter)
	if err != nil {
		return "", fmt.Errorf("jq parse error: %w", err)
	}

	// gojq takes the input as an `any`, not raw bytes, so unmarshal first. A
	// non-JSON input fails loudly here rather than inside the iterator.
	var inputAny any
	if err := json.Unmarshal(input, &inputAny); err != nil {
		return "", fmt.Errorf("input is not valid JSON: %w", err)
	}

	// Memory watchdog: bound the heap a memory-amplifying filter can allocate.
	// gojq honours cancellation of runCtx during value construction, so once heap
	// growth crosses maxHeapGrowth the watchdog cancels the run and the next
	// iter.Next() yields a context error, which we map to an explicit
	// memory-budget error (distinct from the timeout path). The watchdog stops
	// when Run returns (close(stop)) or runCtx is cancelled. Run BLOCKS on done
	// so the goroutine never outlives Run — it reads the package vars
	// maxHeapGrowth/heapSampleInterval, and tests mutate those under t.Cleanup,
	// so a still-running watchdog after Run returns is a data race under -race.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var overBudget atomic.Bool
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchHeap(runCtx, cancel, &overBudget, stop)
	}()
	defer func() {
		close(stop)
		<-done
	}()

	iter := query.RunWithContext(runCtx, inputAny)

	var outputs []any
	accumulated := 0
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, isErr := v.(error); isErr {
			if overBudget.Load() {
				return "", fmt.Errorf("jq filter exceeded the %d-byte memory budget (filter too broad?)", maxHeapGrowth)
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return "", fmt.Errorf("jq filter timed out (possible pathological input): %w", err)
			}
			return "", fmt.Errorf("jq filter failed: %w", err)
		}

		enc, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("jq output could not be JSON-encoded: %w", err)
		}
		accumulated += len(enc)
		if accumulated > MaxOutputBytes {
			return "", fmt.Errorf("jq output exceeded the %d-byte cap (filter too broad?)", MaxOutputBytes)
		}
		outputs = append(outputs, v)
	}

	if overBudget.Load() {
		return "", fmt.Errorf("jq filter exceeded the %d-byte memory budget (filter too broad?)", maxHeapGrowth)
	}

	switch len(outputs) {
	case 0:
		return "null", nil
	case 1:
		b, err := json.Marshal(outputs[0])
		if err != nil {
			return "", fmt.Errorf("jq output could not be JSON-encoded: %w", err)
		}
		return string(b), nil
	default:
		b, err := json.Marshal(outputs)
		if err != nil {
			return "", fmt.Errorf("jq output could not be JSON-encoded: %w", err)
		}
		return string(b), nil
	}
}

// watchHeap samples process heap growth over the baseline captured at start and
// cancels the run (setting over) once growth crosses maxHeapGrowth. It exits when
// stop is closed (Run returned) or ctx is cancelled. runtime.ReadMemStats is a
// brief stop-the-world, but only long-running filters are sampled repeatedly —
// legitimate filters finish before the first tick, so the steady-state cost is
// zero. The heap signal is process-global, so a concurrent Run's allocations can
// trip this one early; that is fail-safe (a bounded memory-budget error the model
// can recover from by narrowing the filter) and reflects real aggregate pressure.
func watchHeap(ctx context.Context, cancel context.CancelFunc, over *atomic.Bool, stop <-chan struct{}) {
	var base runtime.MemStats
	runtime.ReadMemStats(&base)

	t := time.NewTicker(heapSampleInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			// Guard the unsigned subtraction: GC can drop HeapAlloc below the
			// baseline, which would wrap to a huge value and false-trip.
			if m.HeapAlloc > base.HeapAlloc && m.HeapAlloc-base.HeapAlloc > maxHeapGrowth {
				over.Store(true)
				cancel()
				return
			}
		}
	}
}

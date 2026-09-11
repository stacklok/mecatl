package osfs_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// --- osfs CommandRunner.RunStreaming tests (real shell, under t.TempDir) ---

// newStreamer returns the runner narrowed to the OPTIONAL tool.CommandStreamer
// capability, failing the test if the concrete runner declines it (osfs must
// always implement it — the background-Shell capture rides this seam).
func newStreamer(t *testing.T, dir string) tool.CommandStreamer {
	t.Helper()
	s, ok := newRunner(t, dir).(tool.CommandStreamer)
	if !ok {
		t.Fatal("osfs CommandRunner does not implement tool.CommandStreamer")
	}
	return s
}

// syncBuffer is a bytes.Buffer that is safe for the concurrent writes the two
// pipe-copy goroutines (stdout + stderr) issue into the single streaming sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestRunStreamingInterleaved pins the core contract: stdout AND stderr both
// land in the ONE caller-owned writer, interleaved in the order the OS delivers
// them (the parenthesized pipeline serializes the writes so the expected order
// is deterministic), and the exit code is 0.
func TestRunStreamingInterleaved(t *testing.T) {
	ctx := context.Background()
	s := newStreamer(t, t.TempDir())

	var out syncBuffer
	code, err := s.RunStreaming(ctx, "(echo out-1; echo err-1 >&2; echo out-2; echo err-2 >&2)", &out)
	if err != nil {
		t.Fatalf("RunStreaming: %v", err)
	}
	if code != 0 {
		t.Errorf("exitCode = %d want 0", code)
	}
	got := out.String()
	want := "out-1\nerr-1\nout-2\nerr-2\n"
	if got != want {
		t.Errorf("interleaved output = %q want %q (ordering as the OS delivers)", got, want)
	}
}

// TestRunStreamingNoRunnerCap proves the runner does NOT apply Run's
// maxCommandOutput head cap on the streaming path: emit well over 1 MiB and the
// caller's writer must hold ALL of it (the caller's own bounding — here none —
// is the only cap). A head-capped capture would keep the FIRST bytes and lose
// the tail, which is exactly what a background command's "recent output" ring
// cannot tolerate.
func TestRunStreamingNoRunnerCap(t *testing.T) {
	ctx := context.Background()
	s := newStreamer(t, t.TempDir())

	// 4096 iterations x ~513 bytes ≈ 2 MiB > maxCommandOutput (1 MiB).
	var out syncBuffer
	code, err := s.RunStreaming(ctx, "i=0; while [ $i -lt 4096 ]; do printf '%0512d\\n' $i; i=$((i+1)); done", &out)
	if err != nil {
		t.Fatalf("RunStreaming: %v", err)
	}
	if code != 0 {
		t.Errorf("exitCode = %d want 0", code)
	}
	if n := len(out.String()); n <= 1<<20 {
		t.Errorf("streamed output = %d bytes; want > 1 MiB (runner must not head-cap the stream)", n)
	}
}

func TestRunStreamingExitCode(t *testing.T) {
	ctx := context.Background()
	s := newStreamer(t, t.TempDir())

	var out syncBuffer
	code, err := s.RunStreaming(ctx, "echo partial; exit 3", &out)
	if err != nil {
		t.Fatalf("RunStreaming returned harness error for a non-zero exit: %v", err)
	}
	if code != 3 {
		t.Errorf("exitCode = %d want 3", code)
	}
	// The output written before the non-zero exit still reaches the sink.
	if strings.TrimSpace(out.String()) != "partial" {
		t.Errorf("streamed output = %q want %q", strings.TrimSpace(out.String()), "partial")
	}
}

func TestRunStreamingCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	s := newStreamer(t, t.TempDir())

	var out syncBuffer
	_, err := s.RunStreaming(ctx, "echo hi", &out)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunStreaming with cancelled ctx err = %v want context.Canceled", err)
	}
}

func TestRunStreamingTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	s := newStreamer(t, t.TempDir())

	var out syncBuffer
	_, err := s.RunStreaming(ctx, "sleep 5", &out)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunStreaming with expired deadline err = %v want context.DeadlineExceeded", err)
	}
}

// TestRunStreamingCancelWithoutDeadline tests cancellation after streaming begins
// with a deadline-free caller context. The sleeper is launched before readiness
// is signaled to eliminate the child-creation race; cancellation must still
// unwind the retained shell promptly.
func TestRunStreamingCancelWithoutDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := newStreamer(t, t.TempDir())

	var out syncBuffer
	done := make(chan error, 1)
	go func() {
		_, err := s.RunStreaming(ctx, "sleep 30 & echo before; wait; echo after", &out)
		done <- err
	}()

	// Wait for the first line to stream through, then cancel.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), "before") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "before") {
		t.Fatal("first line never streamed; command did not start")
	}
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunStreaming after cancel err = %v want context.Canceled", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("RunStreaming blocked %v after cancel; the process-group kill must unwind promptly", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunStreaming did not return after cancel")
	}
}

// TestRunStreamingWaitDelayUnblocksGrandchildPipeWait mirrors the Run oracle
// (TestCommandRunnerWaitDelayUnblocksGrandchildPipeWait) on the streaming path:
// a backgrounded grandchild holding the inherited fds must not park the stream
// past the WaitDelay; what streamed before the shell exited stands.
func TestRunStreamingWaitDelayUnblocksGrandchildPipeWait(t *testing.T) {
	r, err := osfs.NewCommandRunnerShell(t.TempDir(), "/bin/sh", osfs.WithCommandWaitDelay(200*time.Millisecond))
	if err != nil {
		t.Fatalf("NewCommandRunnerShell: %v", err)
	}
	s, ok := r.(tool.CommandStreamer)
	if !ok {
		t.Fatal("osfs CommandRunner does not implement tool.CommandStreamer")
	}

	var out syncBuffer
	start := time.Now()
	code, err := s.RunStreaming(context.Background(), "sleep 5 & echo started", &out)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("a WaitDelay expiry on a successful command must be a success, got err = %v", err)
	}
	if code != 0 || !strings.Contains(out.String(), "started") {
		t.Fatalf("streamed output must survive the WaitDelay close, got exit=%d out=%q", code, out.String())
	}
	if elapsed > 3*time.Second {
		t.Fatalf("RunStreaming blocked %v on the grandchild's inherited pipe; WaitDelay must bound it", elapsed)
	}
}

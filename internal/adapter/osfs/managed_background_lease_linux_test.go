//go:build linux

package osfs_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/managedtemp"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

type leasePathWriter struct {
	mu       sync.Mutex
	contents strings.Builder
	ready    chan struct{}
	once     sync.Once
}

func newLeasePathWriter() *leasePathWriter { return &leasePathWriter{ready: make(chan struct{})} }

func (w *leasePathWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	_, err := w.contents.Write(p)
	w.mu.Unlock()
	w.once.Do(func() { close(w.ready) })
	return len(p), err
}

func (w *leasePathWriter) path(t *testing.T) string {
	t.Helper()
	select {
	case <-w.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("background command did not publish its managed temporary path")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.TrimSpace(w.contents.String())
}

func managedJobStreamer(t *testing.T) tool.CommandTemporaryScopeStreamer {
	t.Helper()
	ns, err := managedtemp.Open(filepath.Join(t.TempDir(), "managed"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ns.Close() })
	workspace, err := ns.OpenWorkspace("osfs", "background-job-lease", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	runner, err := osfs.NewCommandRunnerShell(t.TempDir(), "/bin/sh", osfs.WithManagedTemporaryWorkspace(workspace))
	if err != nil {
		t.Fatal(err)
	}
	streamer, ok := runner.(tool.CommandTemporaryScopeStreamer)
	if !ok {
		t.Fatal("managed command runner does not implement temporary-scope streaming")
	}
	return streamer
}

func assertLeaseLockHeld(t *testing.T, leasePath string) {
	t.Helper()
	lock, err := os.OpenFile(filepath.Join(leasePath, "lease.lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open lease lock: %v", err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("reaper lock attempt = %v, want EWOULDBLOCK while job is live", err)
	}
}

// TestADR_0281_BackgroundJobLeaseLifecycle pins that the streaming path used by
// Bash background jobs allocates a job lease, retains it until its process group
// has joined, and applies the foreground terminal cleanup rule.
func TestADR_0281_BackgroundJobLeaseLifecycle(t *testing.T) {
	streamer := managedJobStreamer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := newLeasePathWriter()
	done := make(chan error, 1)
	go func() {
		_, err := streamer.RunStreamingWithTemporaryScope(ctx, "printf '%s\\n' \"$TMPDIR\"; sleep 30", tool.TemporaryScopeManaged, out)
		done <- err
	}()

	tempPath := out.path(t)
	leasePath := filepath.Dir(tempPath)
	if base := filepath.Base(leasePath); !strings.HasPrefix(base, "job-") || len(strings.TrimPrefix(base, "job-")) != 32 || filepath.Base(tempPath) != "tmp" {
		t.Fatalf("background temporary path = %q, want job-<128-bit-id>/tmp", tempPath)
	}
	if _, err := os.Stat(leasePath); err != nil {
		t.Fatalf("live background job lease %q: %v", leasePath, err)
	}
	assertLeaseLockHeld(t, leasePath)

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("background streaming cancellation = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("background streaming command did not join after cancellation")
	}
	if _, err := os.Stat(leasePath); !os.IsNotExist(err) {
		t.Fatalf("terminal background job lease remains at %q: %v", leasePath, err)
	}
}

// TestADR_0281_ActiveLeaseLockDefeatsReaper pins the per-job exclusion a
// deterministic reaper relies on: concurrent deadline-free managed jobs have
// distinct allocations and its non-blocking lock attempt cannot select either.
func TestADR_0281_ActiveLeaseLockDefeatsReaper(t *testing.T) {
	streamer := managedJobStreamer(t)
	type job struct {
		cancel context.CancelFunc
		out    *leasePathWriter
		done   chan error
	}
	jobs := make([]job, 2)
	for i := range jobs {
		ctx, cancel := context.WithCancel(context.Background()) // no caller timeout
		jobs[i] = job{cancel: cancel, out: newLeasePathWriter(), done: make(chan error, 1)}
		go func(j *job) {
			_, err := streamer.RunStreamingWithTemporaryScope(ctx, "printf '%s\\n' \"$TMPDIR\"; sleep 30", tool.TemporaryScopeManaged, j.out)
			j.done <- err
		}(&jobs[i])
	}

	paths := []string{filepath.Dir(jobs[0].out.path(t)), filepath.Dir(jobs[1].out.path(t))}
	if paths[0] == paths[1] {
		t.Fatalf("concurrent background jobs share lease %q", paths[0])
	}
	for _, path := range paths {
		assertLeaseLockHeld(t, path)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("a reaper must retain lock-contended live lease %q: %v", path, err)
		}
	}
	for _, job := range jobs {
		job.cancel()
		select {
		case err := <-job.done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("background streaming cancellation = %v, want context.Canceled", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("background streaming command did not join after cancellation")
		}
	}
}

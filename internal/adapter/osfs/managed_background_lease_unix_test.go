//go:build linux || darwin

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
	lines    int
}

func newLeasePathWriter() *leasePathWriter { return &leasePathWriter{ready: make(chan struct{})} }

func (w *leasePathWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	_, err := w.contents.Write(p)
	ready := strings.Count(w.contents.String(), "\n") >= max(1, w.lines)
	w.mu.Unlock()
	if ready {
		w.once.Do(func() { close(w.ready) })
	}
	return len(p), err
}

func TestLeasePathWriterWaitsForCompleteEnvironment(t *testing.T) {
	w := newLeasePathWriter()
	w.lines = 2
	for _, chunk := range []string{"/managed/tmp", "\n", "/managed"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
		select {
		case <-w.ready:
			t.Fatal("writer published partial environment")
		default:
		}
	}
	if _, err := w.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	if got := w.path(t); got != "/managed/tmp\n/managed" {
		t.Fatalf("environment = %q", got)
	}
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

// TestManagedTemp_BackgroundJobLeaseLifecycle pins that the streaming path used by
// Shell background jobs allocates a job lease, retains it until its process group
// has joined, and applies the foreground terminal cleanup rule.
func TestManagedTemp_BackgroundJobLeaseLifecycle(t *testing.T) {
	streamer := managedJobStreamer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := newLeasePathWriter()
	out.lines = 2
	done := make(chan error, 1)
	go func() {
		_, err := streamer.RunStreamingWithTemporaryScope(ctx, "printf '%s\\n%s\\n' \"$TMPDIR\" \"$MECATL_TEST_TEMP_LEASE\"; exec sleep 30", tool.TemporaryScopeManaged, out)
		done <- err
	}()

	lines := strings.Split(out.path(t), "\n")
	if len(lines) != 2 {
		t.Fatalf("background managed environment = %q, want TMPDIR and lease marker", lines)
	}
	tempPath := lines[0]
	leasePath := filepath.Dir(tempPath)
	if lines[1] != leasePath {
		t.Fatalf("MECATL_TEST_TEMP_LEASE = %q, want %q", lines[1], leasePath)
	}
	if base := filepath.Base(leasePath); !strings.HasPrefix(base, "job-") || len(strings.TrimPrefix(base, "job-")) != 16 || strings.ContainsAny(strings.TrimPrefix(base, "job-"), "+/=") || filepath.Base(tempPath) != "tmp" {
		t.Fatalf("background temporary path = %q, want job-<16-char-raw-url-base64-id>/tmp", tempPath)
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

// TestManagedTemp_ActiveLeaseLockDefeatsReaper pins the per-job exclusion a
// deterministic reaper relies on: concurrent deadline-free managed jobs have
// distinct allocations and its non-blocking lock attempt cannot select either.
func TestManagedTemp_ActiveLeaseLockDefeatsReaper(t *testing.T) {
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

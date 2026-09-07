package filewatch

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatcherCoalescesBurst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "value")
	mustWrite(t, path, "initial")

	// debounce/maxDebounce/the tail-wait below are deliberately generous (well
	// beyond sibling tests in this file): the loop below round-trips through a
	// real write + real fsnotify delivery + goroutine scheduling 8 times, and
	// under -race on a loaded CI runner that round-trip can occasionally take
	// tens of ms. A tight debounce here does not test coalescing more
	// strictly -- it just makes an inter-write scheduling delay look like
	// real quiescence, correctly (by design) splitting the burst into two
	// callbacks and failing the test on a false positive.
	changes := make(chan struct{}, 4)
	w, armed := newTestWatcher(t, []string{path}, 150*time.Millisecond, 3*time.Second, func() { changes <- struct{}{} })
	defer w.Close()

	for i := 0; i < 8; i++ {
		mustWrite(t, path, "changed")
		awaitChange(t, armed)
	}
	awaitChange(t, changes)
	select {
	case <-changes:
		t.Fatal("burst produced more than one callback")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestWatcherBoundsRepeatedEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "value")
	mustWrite(t, path, "initial")

	changes := make(chan struct{}, 8)
	w, _ := newTestWatcher(t, []string{path, path}, 60*time.Millisecond, 150*time.Millisecond, func() { changes <- struct{}{} })
	defer w.Close()

	started := time.Now()
	stopWrites := make(chan struct{})
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = os.WriteFile(path, []byte("again"), 0o600)
			case <-stopWrites:
				return
			}
		}
	}()
	awaitChange(t, changes)
	close(stopWrites)
	if elapsed := time.Since(started); elapsed > 350*time.Millisecond {
		t.Fatalf("continuous events were not bounded: callback after %v", elapsed)
	}
}

func TestWatcherSeesProjectedSymlinkSwap(t *testing.T) {
	dir := t.TempDir()
	projectVersion(t, dir, "..v1", "one")
	if err := os.Symlink("..v1", filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..data", "value"), filepath.Join(dir, "value")); err != nil {
		t.Fatal(err)
	}

	got := make(chan string, 2)
	path := filepath.Join(dir, "value")
	w, _ := newTestWatcher(t, []string{path}, 30*time.Millisecond, 120*time.Millisecond, func() {
		body, err := os.ReadFile(path)
		if err == nil {
			got <- string(body)
		}
	})
	defer w.Close()

	projectVersion(t, dir, "..v2", "two")
	if err := os.Symlink("..v2", filepath.Join(dir, "..data.next")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "..data.next"), filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-got:
		if value != "two" {
			t.Fatalf("callback read %q, want projected replacement", value)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for projected-volume replacement")
	}
}

func TestWatcherCloseCancelsArmedCallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "value")
	mustWrite(t, path, "initial")
	var calls atomic.Int32
	armed := make(chan struct{}, 1)
	w, err := newWatcher([]string{path}, 200*time.Millisecond, 400*time.Millisecond, func() { calls.Add(1) }, func(err error) { t.Errorf("watch error: %v", err) }, armed)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, "changed")
	awaitChange(t, armed)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	if got := calls.Load(); got != 0 {
		t.Fatalf("callback count after shutdown = %d, want 0", got)
	}
}

func TestWatcherCloseJoinsInFlightCallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "value")
	mustWrite(t, path, "initial")
	started := make(chan struct{})
	release := make(chan struct{})
	w, armed := newTestWatcher(t, []string{path}, 10*time.Millisecond, 20*time.Millisecond, func() {
		close(started)
		<-release
	})
	mustWrite(t, path, "changed")
	awaitChange(t, armed)
	awaitChange(t, started)
	closed := make(chan struct{})
	go func() {
		_ = w.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned while callback was still running")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	awaitChange(t, closed)
}

// newTestWatcher constructs a watcher through the unexported newWatcher entry
// point so tests can synchronise on the armed channel (the same per-arm signal
// TestWatcherCloseCancelsArmedCallback uses) instead of racing real wall-clock
// timing against the watcher goroutine's startup.
func newTestWatcher(t *testing.T, paths []string, debounce, maxDebounce time.Duration, onChange func()) (*Watcher, <-chan struct{}) {
	t.Helper()
	armed := make(chan struct{}, 1)
	w, err := newWatcher(paths, debounce, maxDebounce, onChange, func(err error) { t.Errorf("watch error: %v", err) }, armed)
	if err != nil {
		t.Fatal(err)
	}
	return w, armed
}

func awaitChange(t *testing.T, changes <-chan struct{}) {
	t.Helper()
	select {
	case <-changes:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callback")
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func projectVersion(t *testing.T, root, version, body string) {
	t.Helper()
	dir := filepath.Join(root, version)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "value"), body)
}

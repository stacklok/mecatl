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

	changes := make(chan struct{}, 4)
	w := newTestWatcher(t, []string{path}, 50*time.Millisecond, 200*time.Millisecond, func() { changes <- struct{}{} })
	defer w.Close()

	for i := 0; i < 8; i++ {
		mustWrite(t, path, "changed")
	}
	awaitChange(t, changes)
	select {
	case <-changes:
		t.Fatal("burst produced more than one callback")
	case <-time.After(90 * time.Millisecond):
	}
}

func TestWatcherBoundsRepeatedEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "value")
	mustWrite(t, path, "initial")

	changes := make(chan struct{}, 8)
	w := newTestWatcher(t, []string{path, path}, 60*time.Millisecond, 150*time.Millisecond, func() { changes <- struct{}{} })
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
	w := newTestWatcher(t, []string{path}, 30*time.Millisecond, 120*time.Millisecond, func() {
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
	w := newTestWatcher(t, []string{path}, 10*time.Millisecond, 20*time.Millisecond, func() {
		close(started)
		<-release
	})
	mustWrite(t, path, "changed")
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

func newTestWatcher(t *testing.T, paths []string, debounce, maxDebounce time.Duration, onChange func()) *Watcher {
	t.Helper()
	w, err := New(paths, debounce, maxDebounce, onChange, func(err error) { t.Errorf("watch error: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	return w
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

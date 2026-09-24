package filewatch

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestDebouncerCoalescesQueuedEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const debounce = 150 * time.Millisecond
		events := make(chan fsnotify.Event, 1)
		d := &debouncer{debounce: debounce, maxDebounce: time.Second}
		defer d.stop()

		d.arm(time.Now())
		time.Sleep(debounce / 2)
		d.arm(time.Now())
		events <- fsnotify.Event{}
		time.Sleep(debounce / 2)
		select {
		case <-d.timerC:
			t.Fatal("debouncer fired before a later event's quiet interval elapsed")
		default:
		}
		time.Sleep(debounce / 2)
		select {
		case <-d.timerC:
		default:
			t.Fatal("debouncer did not fire at the first quiet interval")
		}
		if open, ready := d.fire(events); !open || ready {
			t.Fatalf("fire with a queued event = (open=%t, ready=%t), want (true, false)", open, ready)
		}

		time.Sleep(debounce - time.Nanosecond)
		select {
		case <-d.timerC:
			t.Fatal("debouncer fired before the quiet interval elapsed")
		default:
		}
		time.Sleep(time.Nanosecond)
		select {
		case <-d.timerC:
		default:
			t.Fatal("queued event did not rearm the quiet interval")
		}
		if open, ready := d.fire(events); !open || !ready {
			t.Fatalf("fire after the quiet interval = (open=%t, ready=%t), want (true, true)", open, ready)
		}

		d.arm(time.Now())
		time.Sleep(debounce)
		select {
		case <-d.timerC:
		default:
			t.Fatal("new burst did not arm a timer")
		}
		if open, ready := d.fire(events); !open || !ready {
			t.Fatalf("fire for a new burst = (open=%t, ready=%t), want (true, true)", open, ready)
		}
	})
}

func TestDebouncerCappedBurstGrantsOneGrace(t *testing.T) {
	for _, lateEvent := range []bool{false, true} {
		name := "quiet"
		if lateEvent {
			name = "late_event"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const debounce = 100 * time.Millisecond
				events := make(chan fsnotify.Event, 1)
				d := &debouncer{debounce: debounce, maxDebounce: 2 * debounce}
				defer d.stop()

				d.arm(time.Now())
				for range 2 {
					time.Sleep(3 * debounce / 4)
					d.arm(time.Now())
				}
				time.Sleep(debounce/2 - time.Nanosecond)
				select {
				case <-d.timerC:
					t.Fatal("timer fired before the cap")
				default:
				}
				time.Sleep(time.Nanosecond)
				select {
				case <-d.timerC:
				default:
					t.Fatal("timer did not fire at the cap")
				}
				if open, ready := d.fire(events); !open || ready {
					t.Fatalf("first capped fire = (open=%t, ready=%t), want (true, false)", open, ready)
				}

				time.Sleep(debounce / 2)
				if lateEvent {
					// A kernel event arriving during grace can still be queued
					// when the timer is selected by the watcher loop.
					events <- fsnotify.Event{}
				}
				time.Sleep(debounce/2 - time.Nanosecond)
				select {
				case <-d.timerC:
					t.Fatal("grace expired before one debounce interval")
				default:
				}
				time.Sleep(time.Nanosecond)
				select {
				case <-d.timerC:
				default:
					t.Fatal("grace exceeded one debounce interval")
				}
				if lateEvent {
					if open, ready := d.fire(events); !open || ready {
						t.Fatalf("fire with late event = (open=%t, ready=%t), want (true, false)", open, ready)
					}
					// Rearming past the cap is immediate, not another grace.
					select {
					case <-d.timerC:
					default:
						t.Fatal("late event did not rearm at the elapsed cap")
					}
				}
				if open, ready := d.fire(events); !open || !ready {
					t.Fatalf("fire after grace = (open=%t, ready=%t), want (true, true); no second grace", open, ready)
				}
				if d.timerC != nil || !d.firstAt.IsZero() || d.capped || d.graced {
					t.Fatal("completed burst did not reset debounce state")
				}

				time.Sleep(debounce)
				d.arm(time.Now())
				time.Sleep(debounce - time.Nanosecond)
				select {
				case <-d.timerC:
					t.Fatal("new burst reused the previous cap")
				default:
				}
				time.Sleep(time.Nanosecond)
				select {
				case <-d.timerC:
				default:
					t.Fatal("new burst did not expire after debounce")
				}
				if open, ready := d.fire(events); !open || !ready {
					t.Fatalf("new burst fire = (open=%t, ready=%t), want (true, true)", open, ready)
				}
			})
		})
	}
}

func TestWatcherDeliversChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "value")
	mustWrite(t, path, "initial")

	changes := make(chan struct{}, 1)
	w, armed := newTestWatcher(t, []string{path}, 30*time.Millisecond, time.Second, func() { changes <- struct{}{} })
	defer w.Close()

	mustWrite(t, path, "changed")
	awaitChange(t, armed)
	awaitChange(t, changes)
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

package filewatch

import (
	"os"
	"path/filepath"
	"sync"
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
	w, armed := newTestWatcher(t, []string{path}, 30*time.Millisecond, time.Second, func() {
		select {
		case changes <- struct{}{}:
		default:
		}
	})
	defer w.Close()

	mustWrite(t, path, "changed")
	awaitChange(t, armed)
	awaitChange(t, changes)
}

func TestWatcherBoundsRepeatedEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			debounce    = 100 * time.Millisecond
			maxDebounce = 3 * debounce
		)
		changes := make(chan struct{}, 1)
		w, events := newSyntheticWatcher(t, debounce, maxDebounce, func() {
			select {
			case changes <- struct{}{}:
			default:
			}
		})
		defer w.Close()
		stopEvents := make(chan struct{})
		var stopOnce sync.Once
		stop := func() { stopOnce.Do(func() { close(stopEvents) }) }
		defer stop()

		events <- fsnotify.Event{}
		go func() {
			for {
				select {
				case <-time.After(debounce / 2):
				case <-stopEvents:
					return
				}
				select {
				case events <- fsnotify.Event{}:
				case <-stopEvents:
					return
				}
			}
		}()
		synctest.Wait()

		time.Sleep(maxDebounce - time.Nanosecond)
		synctest.Wait()
		assertNoChange(t, changes, "watcher fired while continuous events were below the cap")
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		time.Sleep(debounce)
		synctest.Wait()
		select {
		case <-changes:
		default:
			t.Fatal("continuous events postponed the callback beyond maxDebounce plus one grace interval")
		}

		stop()
		synctest.Wait()
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	})
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
			select {
			case got <- string(body):
			default:
			}
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
	synctest.Test(t, func(t *testing.T) {
		const debounce = 200 * time.Millisecond
		changes := make(chan struct{}, 1)
		w, events := newSyntheticWatcher(t, debounce, 2*debounce, func() { changes <- struct{}{} })
		defer w.Close()

		events <- fsnotify.Event{}
		synctest.Wait()
		assertNoChange(t, changes, "callback fired before Close")
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * debounce)
		synctest.Wait()
		assertNoChange(t, changes, "armed callback fired after Close")
	})
}

func TestWatcherCloseJoinsInFlightCallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const debounce = 100 * time.Millisecond
		started := make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		releaseCallback := func() { releaseOnce.Do(func() { close(release) }) }

		w, events := newSyntheticWatcher(t, debounce, 2*debounce, func() {
			close(started)
			<-release
		})
		defer w.Close()
		// Release is deferred after Close so it runs first if an assertion fails.
		defer releaseCallback()

		events <- fsnotify.Event{}
		time.Sleep(debounce)
		synctest.Wait()
		select {
		case <-started:
		default:
			t.Fatal("callback did not start")
		}

		closed := make(chan struct{})
		go func() {
			_ = w.Close()
			close(closed)
		}()
		synctest.Wait()
		assertNoChange(t, closed, "Close returned while callback was still running")

		releaseCallback()
		synctest.Wait()
		select {
		case <-closed:
		default:
			t.Fatal("Close did not return after callback completed")
		}
	})
}

func newSyntheticWatcher(t *testing.T, debounce, maxDebounce time.Duration, onChange func()) (*Watcher, chan<- fsnotify.Event) {
	t.Helper()
	events := make(chan fsnotify.Event, 1)
	errs := make(chan error)
	w := &Watcher{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		w.run(events, errs, debounce, maxDebounce, onChange, func(err error) { t.Errorf("watch error: %v", err) }, nil)
	}()
	return w, events
}

func assertNoChange(t *testing.T, changes <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-changes:
		t.Fatal(message)
	default:
	}
}

// newTestWatcher constructs a real watcher through the unexported newWatcher
// entry point so integration tests can synchronise on each timer arm instead of
// racing wall-clock timing against the watcher goroutine's startup.
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

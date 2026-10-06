// Package filewatch coalesces filesystem notifications for mounted configuration files.
package filewatch

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher watches the parent directories of a set of files. Watching directories,
// rather than resolved file targets, preserves notifications when Kubernetes swaps
// a projected volume's ..data symlink.
type Watcher struct {
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// New starts a watcher. Events are delayed until debounce has elapsed without a
// new event, but a continuous burst is delivered no later than maxDebounce after
// its first event -- with one exception: when the maxDebounce cap is what ends a
// burst and nothing is yet queued, the watcher grants one further debounce-length
// grace so an fsnotify event still in flight from the kernel to the delivery
// goroutine can merge into the same burst instead of starting a new one (issue
// #885). The effective worst-case delivery bound is therefore maxDebounce+debounce,
// granted at most once per burst. onError receives watcher errors; it must not
// assume an error is fatal. Paths are used only to select and deduplicate their
// parent directories.
func New(paths []string, debounce, maxDebounce time.Duration, onChange func(), onError func(error)) (*Watcher, error) {
	return newWatcher(paths, debounce, maxDebounce, onChange, onError, nil)
}

func newWatcher(paths []string, debounce, maxDebounce time.Duration, onChange func(), onError func(error), armed chan<- struct{}) (*Watcher, error) {
	if len(paths) == 0 {
		return nil, errors.New("filewatch: at least one path is required")
	}
	if debounce <= 0 || maxDebounce < debounce {
		return nil, errors.New("filewatch: debounce must be positive and no greater than maxDebounce")
	}
	if onChange == nil {
		return nil, errors.New("filewatch: onChange is required")
	}

	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("filewatch: create watcher: %w", err)
	}
	dirs := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if path == "" {
			_ = fw.Close()
			return nil, errors.New("filewatch: paths must not be empty")
		}
		dir, err := filepath.Abs(filepath.Dir(filepath.Clean(path)))
		if err != nil {
			_ = fw.Close()
			return nil, fmt.Errorf("filewatch: resolve parent directory: %w", err)
		}
		dirs[dir] = struct{}{}
	}
	for dir := range dirs {
		if err := fw.Add(dir); err != nil {
			_ = fw.Close()
			return nil, fmt.Errorf("filewatch: watch parent directory %q: %w", dir, err)
		}
	}

	w := &Watcher{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		defer func() { _ = fw.Close() }()
		w.run(fw.Events, fw.Errors, debounce, maxDebounce, onChange, onError, armed)
	}()
	return w, nil
}

// debouncer tracks the state of one in-flight coalesced burst of filesystem
// events on behalf of Watcher.run, and decides when a burst is ready to fire.
type debouncer struct {
	debounce, maxDebounce time.Duration
	timer                 *time.Timer
	timerC                <-chan time.Time
	firstAt               time.Time
	capped                bool // true once arm has clamped fireAt to the maxDebounce deadline
	graced                bool // true once the current burst has already used its one straggler grace
}

// arm (re)arms the debounce timer for an event observed at now.
func (d *debouncer) arm(now time.Time) {
	if d.firstAt.IsZero() {
		d.firstAt = now
	}
	fireAt := now.Add(d.debounce)
	deadline := d.firstAt.Add(d.maxDebounce)
	d.capped = fireAt.After(deadline)
	if d.capped {
		fireAt = deadline
	}
	wait := max(time.Until(fireAt), 0)
	if d.timer == nil {
		d.timer = time.NewTimer(wait)
	} else {
		d.stop()
		d.timer.Reset(wait)
	}
	d.timerC = d.timer.C
}

// stop cancels a pending timer, draining its channel if it already fired. It
// is safe to call on a nil or already-expired timer.
func (d *debouncer) stop() {
	if d.timer != nil && !d.timer.Stop() {
		select {
		case <-d.timer.C:
		default:
		}
	}
}

// fire handles the debounce timer becoming ready. fsnotify may already have
// queued further events by the time the timer fires; drainEvents collects
// them so select's random choice between two ready cases can never split one
// burst. It reports whether the watcher's event channel is still open, and
// whether the caller should invoke onChange now.
//
// If the maxDebounce cap is what triggered this fire and nothing was queued,
// fire grants one bounded extra debounce interval instead of firing
// immediately: an event from this burst may still be in flight from the
// kernel to fsnotify's delivery goroutine (issue #885, observed under -race
// on a loaded CI runner), and without the grace it would land moments later,
// start a new firstAt, and produce a spurious second callback. The grace is
// granted at most once per burst (see New's doc comment for the bound).
func (d *debouncer) fire(events <-chan fsnotify.Event) (open, ready bool) {
	drained, open := drainEvents(events)
	if !open {
		return false, false
	}
	switch {
	case drained:
		d.arm(time.Now())
		return true, false
	case d.capped && !d.graced:
		d.graced = true
		d.timer.Reset(d.debounce)
		d.timerC = d.timer.C
		return true, false
	default:
		d.timerC = nil
		d.firstAt = time.Time{}
		d.capped = false
		d.graced = false
		return true, true
	}
}

func (w *Watcher) run(events <-chan fsnotify.Event, errs <-chan error, debounce, maxDebounce time.Duration, onChange func(), onError func(error), armed chan<- struct{}) {
	d := &debouncer{debounce: debounce, maxDebounce: maxDebounce}
	defer d.stop()

	for {
		select {
		case <-w.stop:
			return
		case _, ok := <-events:
			if !ok {
				return
			}
			d.arm(time.Now())
			signalArmed(armed)
		case <-d.timerC:
			open, ready := d.fire(events)
			if !open {
				return
			}
			if ready {
				onChange()
			}
		case err, ok := <-errs:
			if !ok {
				return
			}
			if onError != nil {
				onError(err)
			}
		}
	}
}

// signalArmed notifies a test-only observer that the debounce timer has just
// been (re)armed. The send is best-effort: a full or nil channel is never a
// reason to block the watcher goroutine.
func signalArmed(armed chan<- struct{}) {
	if armed == nil {
		return
	}
	select {
	case armed <- struct{}{}:
	default:
	}
}

func drainEvents(events <-chan fsnotify.Event) (drained, open bool) {
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return false, false
			}
			drained = true
		default:
			return drained, true
		}
	}
}

// Close stops the watcher, cancels any pending debounce, and joins its goroutine.
// It is safe to call repeatedly.
func (w *Watcher) Close() error {
	if w == nil {
		return nil
	}
	w.once.Do(func() { close(w.stop) })
	<-w.done
	return nil
}

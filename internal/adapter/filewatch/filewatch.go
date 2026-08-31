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
	watcher *fsnotify.Watcher
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

// New starts a watcher. Events are delayed until debounce has elapsed without a
// new event, but a continuous burst is delivered no later than maxDebounce after
// its first event. onError receives watcher errors; it must not assume an error is
// fatal. Paths are used only to select and deduplicate their parent directories.
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

	w := &Watcher{watcher: fw, stop: make(chan struct{}), done: make(chan struct{})}
	go w.run(debounce, maxDebounce, onChange, onError, armed)
	return w, nil
}

func (w *Watcher) run(debounce, maxDebounce time.Duration, onChange func(), onError func(error), armed chan<- struct{}) {
	defer close(w.done)
	defer func() { _ = w.watcher.Close() }()

	var (
		timer   *time.Timer
		timerC  <-chan time.Time
		firstAt time.Time
	)
	stopTimer := func() {
		if timer != nil && !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	defer stopTimer()
	arm := func(now time.Time) {
		if firstAt.IsZero() {
			firstAt = now
		}
		fireAt := now.Add(debounce)
		deadline := firstAt.Add(maxDebounce)
		if fireAt.After(deadline) {
			fireAt = deadline
		}
		wait := time.Until(fireAt)
		if wait < 0 {
			wait = 0
		}
		if timer == nil {
			timer = time.NewTimer(wait)
		} else {
			stopTimer()
			timer.Reset(wait)
		}
		timerC = timer.C
		if armed != nil {
			select {
			case armed <- struct{}{}:
			default:
			}
		}
	}

	for {
		select {
		case <-w.stop:
			return
		case _, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			arm(time.Now())
		case <-timerC:
			// fsnotify may already have queued events when the debounce timer
			// becomes ready. Drain them before committing a callback: select's
			// random choice between two ready cases must not split one burst.
			drained, open := drainEvents(w.watcher.Events)
			if !open {
				return
			}
			if drained {
				arm(time.Now())
			} else {
				timerC = nil
				firstAt = time.Time{}
				onChange()
			}
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			if onError != nil {
				onError(err)
			}
		}
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

package redisstore

import (
	"context"
	"fmt"
	"sync"

	"github.com/stacklok/mecatl/engine/port"
)

// followerRegistry owns process-local admission and cancellation for every
// Follow iterator. Admission and close share one mutex, so a racing iterator is
// either rejected or present in the exact cancellation snapshot.
type followerRegistry struct {
	mu       sync.Mutex
	limit    int
	closed   bool
	nextID   uint64
	active   map[uint64]context.CancelFunc
	allDone  chan struct{}
	doneOnce sync.Once
}

func newFollowerRegistry(limit int) *followerRegistry {
	return &followerRegistry{limit: limit, active: make(map[uint64]context.CancelFunc), allDone: make(chan struct{})}
}

func (r *followerRegistry) admit(parent context.Context) (context.Context, func(), error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, nil, errStoreClosed
	}
	if len(r.active) >= r.limit {
		r.mu.Unlock()
		return nil, nil, fmt.Errorf("redisstore: %w", port.ErrEventFollowCapacity)
	}
	ctx, cancel := context.WithCancel(parent)
	id := r.nextID
	r.nextID++
	r.active[id] = cancel
	r.mu.Unlock()

	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			cancel()
			r.mu.Lock()
			delete(r.active, id)
			if r.closed && len(r.active) == 0 {
				r.doneOnce.Do(func() { close(r.allDone) })
			}
			r.mu.Unlock()
		})
	}, nil
}

func (r *followerRegistry) beginClose() <-chan struct{} {
	r.mu.Lock()
	r.closed = true
	for _, cancel := range r.active {
		cancel()
	}
	if len(r.active) == 0 {
		r.doneOnce.Do(func() { close(r.allDone) })
	}
	done := r.allDone
	r.mu.Unlock()
	return done
}

func (r *followerRegistry) activeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.active)
}

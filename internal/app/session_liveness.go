package app

import (
	"sync"

	"github.com/stacklok/mecatl/engine/session"
)

// sessionLiveness is the Build-owned process-wide registry for engine-driven
// child sessions. Top-level runs remain owned by server.Service.runs.
type sessionLiveness struct {
	mu     sync.RWMutex
	active map[session.SessionID]uint64
}

func newSessionLiveness() *sessionLiveness {
	return &sessionLiveness{active: make(map[session.SessionID]uint64)}
}

func (r *sessionLiveness) Register(id session.SessionID) func() {
	r.mu.Lock()
	r.active[id]++
	r.mu.Unlock()
	return sync.OnceFunc(func() {
		r.mu.Lock()
		if r.active[id] <= 1 {
			delete(r.active, id)
		} else {
			r.active[id]--
		}
		r.mu.Unlock()
	})
}

func (r *sessionLiveness) IsLive(id session.SessionID) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.active[id] > 0
}

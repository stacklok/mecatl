package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// SessionMutationCapability is the process-local proof that this Service may
// start a durable mutation for a session. It is deliberately not a backend
// fencing token: admission and invalidation are atomic locally, while a backend
// call admitted before invalidation may finish.
type SessionMutationCapability struct {
	mu       sync.RWMutex
	disabled bool
	states   map[session.SessionID]bool
}

// NewSessionMutationCapability constructs the local mutation gate. When leasing
// is not configured, enabled is false and the gate is a byte-identical pass-through.
func NewSessionMutationCapability(enabled bool) *SessionMutationCapability {
	return &SessionMutationCapability{disabled: !enabled, states: make(map[session.SessionID]bool)}
}

func (c *SessionMutationCapability) grant(id session.SessionID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.disabled {
		c.states[id] = true
	}
}

func (c *SessionMutationCapability) invalidate(id session.SessionID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.disabled {
		c.states[id] = false
	}
}

func (c *SessionMutationCapability) disable() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disabled = true
	clear(c.states)
}

func (c *SessionMutationCapability) allows(id session.SessionID) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	valid, tracked := c.states[id]
	// Untracked identities retain their existing path. Service tracks a main
	// session when its lease is acquired, while independently leased delegation
	// children continue through their own liveness owner. A declared loss leaves
	// an explicit false tombstone, which is the state this gate denies.
	return c.disabled || !tracked || valid
}

// GuardStore gates engine-owned snapshot saves at operation admission. Loads are
// read-only and pass through. Creation remains on Service's unwrapped store path.
func (c *SessionMutationCapability) GuardStore(next port.SessionStore) port.SessionStore {
	if next == nil {
		return nil
	}
	return capabilityStore{capability: c, next: next}
}

type capabilityStore struct {
	capability *SessionMutationCapability
	next       port.SessionStore
}

func (s capabilityStore) Save(ctx context.Context, sess *session.Session) error {
	if !s.capability.allows(sess.ID) {
		return fmt.Errorf("%w: %q", ErrSessionLeasedElsewhere, sess.ID)
	}
	return s.next.Save(ctx, sess)
}

func (s capabilityStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	return s.next.Load(ctx, id)
}

// GuardToolCallRecorder drops audit-sidecar mutations that begin after local
// lease capability invalidation. The recorder port has no error return.
func (c *SessionMutationCapability) GuardToolCallRecorder(next port.ToolCallRecorder) port.ToolCallRecorder {
	if next == nil {
		return nil
	}
	return capabilityToolCallRecorder{capability: c, next: next}
}

type capabilityToolCallRecorder struct {
	capability *SessionMutationCapability
	next       port.ToolCallRecorder
}

func (r capabilityToolCallRecorder) ToolCall(id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration) {
	if r.capability.allows(id) {
		r.next.ToolCall(id, call, result, queued, took)
	}
}

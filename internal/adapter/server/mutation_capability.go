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

// Grant marks id as owned by this process.
func (c *SessionMutationCapability) Grant(id session.SessionID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.disabled {
		c.states[id] = true
	}
}

// Invalidate denies new mutations for id after ownership loss.
func (c *SessionMutationCapability) Invalidate(id session.SessionID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.disabled {
		c.states[id] = false
	}
}

// Remove forgets a normally released capability. It is safe only after every
// owner using the capability has settled.
func (c *SessionMutationCapability) Remove(id session.SessionID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.disabled {
		delete(c.states, id)
	}
}

// Disable permanently turns the capability into the no-lease pass-through mode.
// It is used when an optional lease backend reports ErrLeaseUnsupported.
func (c *SessionMutationCapability) Disable() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disabled = true
	clear(c.states)
}

func (c *SessionMutationCapability) allows(id session.SessionID) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	valid, tracked := c.states[id]
	// With leasing configured, only an exact live Grant is authority. Creation
	// uses the unwrapped store before an owner can exist; every later write must
	// carry a tracked hold. Explicit false entries retain denial during unwind.
	return c.disabled || tracked && valid
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

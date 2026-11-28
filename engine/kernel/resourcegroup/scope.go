package resourcegroup

import (
	"context"
	"time"

	engineid "github.com/stacklok/mecatl/engine/id"
	"github.com/stacklok/mecatl/engine/kernel/history"
	groupid "github.com/stacklok/mecatl/engine/kernel/resourcegroup/id"
)

// Scope is the controlled capability and lifetime boundary freshly supplied to
// each construction/instance, not a reusable per-ID object. Zero is unusable.
// Its intended Context is not borrowed from a load/create waiter and is not a
// cleanup context: cleanup may need to continue after instance cancellation.
// No operational capability is currently implemented.
type Scope struct {
	id    engineid.ID
	group *Group
}

// GroupID returns enclosing identity, or zero for a nil/zero Scope.
func (s *Scope) GroupID() groupid.GroupID { return s.Group().GroupID() }

// Group returns the enclosing controlled Group, or nil for a nil/zero Scope.
// It exposes no Manager or unfenced storage authority.
func (s *Scope) Group() *Group {
	if s == nil {
		return nil
	}
	return s.group
}

// ResourceID returns the instance's generic identity, or zero for nil/zero Scope.
func (s *Scope) ResourceID() engineid.ID {
	if s == nil {
		return engineid.ID{}
	}
	return s.id
}

// History proposes the private, bounded History for this exact resource under
// group fencing, never an unrestricted backend. New submissions are rejected once
// this instance retires; already issued appends can still be accepted before group
// release/takeover. Local cancellation cannot retract accepted writes. Passive
// bounded reads remain available. Currently it returns nil,
// ErrNotImplemented, including on nil/zero receivers.
func (*Scope) History() (*history.History, error) { return nil, ErrNotImplemented }

// Context proposes the instance lifetime context, canceled synchronously on local
// retirement or group loss. It is not a waiter or cleanup context, and cannot be
// revived by late renewal or constructor results. Cleanup must use another context.
// Currently it returns nil, including on nil/zero receivers; there is no lifetime.
func (*Scope) Context() context.Context { return nil }

// Draining proposes a stable channel signaling entry into graceful draining.
// It does not signal completion or prove authority. Currently it returns nil.
func (*Scope) Draining() <-chan struct{} { return nil }

// ShutdownBudget proposes the inherited, non-resetting budget once shutdown has
// begun. Currently it returns zero Budget, false, including on nil/zero receivers.
func (*Scope) ShutdownBudget() (Budget, bool) { return Budget{}, false }

// Admit proposes an atomic authority/lifecycle check, returning Active or
// Draining while admission is allowed. Draining still permits counted resources
// inheriting the same deadlines; emergency or sealed admission rejects work.
// It checks live conservative local evidence, not storage health or external
// effects; required history writes must independently pass generation fencing.
// Resources coordinate their own work/references and terminality under local locks;
// this check neither counts work nor reserves future authority. The observation
// is not a reusable authority grant. Currently it returns
// ModeUnknown, ErrNotImplemented, including on nil/zero receivers.
func (*Scope) Admit() (Mode, error) { return ModeUnknown, ErrNotImplemented }

// Mode is a closed in-memory admission vocabulary, distinct from shutdown phase.
// Zero and unknown values never authorize work.
type Mode uint8

const (
	// ModeUnknown denotes no admission decision.
	ModeUnknown Mode = iota
	// ModeActive denotes ordinary admission under provable group authority.
	ModeActive
	// ModeDraining denotes admission under the inherited graceful drain budget.
	ModeDraining
)

// Budget contains fixed deadlines in the Manager's Clock domain. New resources
// inherit it without resetting either deadline. Zero is unavailable, not unlimited.
type Budget struct {
	// GraceUntil ends cooperative draining; authority trouble may end it earlier.
	GraceUntil time.Time
	// FinishBy bounds all shutdown coordination, not every goroutine or effect.
	FinishBy time.Time
}

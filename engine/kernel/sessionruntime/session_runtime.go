package sessionruntime

import (
	"context"
	"errors"

	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
	groupid "github.com/stacklok/mecatl/engine/kernel/sessiongroup/id"
)

// ErrNotImplemented identifies the nonoperational execution skeleton. It never
// means that input was accepted, work ran, or cancellation reached an executor.
var ErrNotImplemented = errors.New("session runtime is not implemented")

// SessionRuntime is the live execution owner of one Session under a hosting
// group. Zero is unusable; do not copy it after use. No usable runtime can yet
// be constructed. Identity fields express correlation, not execution authority.
//
// The intended implementation privately owns and serializes one passive Session
// and its pending submission/execution obligations. Callers get immutable
// snapshots and controlled commands, never a mutable aggregate or raw storage.
// Different member runtimes may progress concurrently. A member-local failure
// may quiesce this runtime without retiring its siblings; group loss retires all.
type SessionRuntime struct {
	groupID   groupid.GroupID
	sessionID sessionid.SessionID
}

// GroupID returns the hosting group's stable identity, or zero for a zero/nil
// runtime. It does not assert that the runtime is still resident or authorized.
func (r *SessionRuntime) GroupID() groupid.GroupID {
	if r == nil {
		return groupid.GroupID{}
	}
	return r.groupID
}

// SessionID returns the Session identity, which also keys its canonical History,
// or zero for a zero/nil runtime. Reloading must not change this identity.
func (r *SessionRuntime) SessionID() sessionid.SessionID {
	if r == nil {
		return sessionid.SessionID{}
	}
	return r.sessionID
}

// Snapshot proposes capturing accepted state through the runtime's serialized
// Session access. A returned snapshot retains immutable backing and may be read
// concurrently without pinning runtime residency. Capture can wait behind a
// submission; already captured snapshots need no further runtime access.
// Currently it always returns a zero snapshot, ErrNotImplemented.
func (*SessionRuntime) Snapshot(context.Context) (session.Snapshot, error) {
	return session.Snapshot{}, ErrNotImplemented
}

// Run proposes submitting input and driving one execution sequence. It reuses
// the Session payload: input's authoring ordinals must be zero; allocation and
// accepted identity belong to the runtime/aggregate, not the caller. Input must
// stay stable during the call; retained input will require bounded detachment.
// At most one sequence may be active; no queue/steering policy is selected here.
//
// Provisional: the first interaction consumer must refine accepted identity,
// progress, and uncertainty reporting before implementation. Stopping a caller's
// wait must not imply rejected acceptance, cancel execution, or discard pending
// state; explicit Cancel is separate. Neither an accepted commitment nor Run's
// context grants ownership, permits automatic re-invocation, or bypasses final
// execution admission. A bare error is not the intended complete outcome report.
// Currently it always returns ErrNotImplemented and neither accepts nor runs input.
func (*SessionRuntime) Run(context.Context, session.UserInput) error {
	return ErrNotImplemented
}

// Cancel proposes explicitly stopping the currently active execution sequence,
// distinct from canceling an observer's wait or releasing group ownership.
// It must close further execution admission and signal admitted work, not claim
// rollback, rejected storage acceptance, or cessation of external effects.
// Run correlation and acknowledgment/reporting need refinement with Run before
// implementation; this signature is not a chosen steering or queue protocol.
// Currently it always returns ErrNotImplemented and sends no cancellation signal.
func (*SessionRuntime) Cancel(context.Context) error {
	return ErrNotImplemented
}

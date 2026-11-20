package lease

import (
	"context"
	"errors"

	"github.com/stacklok/mecatl/engine/kernel/history"
	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
	groupid "github.com/stacklok/mecatl/engine/kernel/sessiongroup/id"
)

// Hold couples one immutable grant incarnation to its group-scoped storage. The
// successful acquirer owns cleanup and one group lifetime, not multiple mutable
// group handles. Renew and Release are caller-serialized, never under a local
// admission lock across I/O. Other methods, backend operations, and notifications
// may run concurrently with maintenance; implementations synchronize authority
// transitions with append acceptance. No public numeric epoch is required.
type Hold interface {
	// GroupID returns the nonzero group identity requested by the Attempt.
	GroupID() groupid.GroupID

	// CheckMember validates a nonzero Session identity and permanent group assignment.
	// Nil confirms membership; ErrNotMember means definitively not assigned;
	// invalid IDs match history.ErrInvalid. Infrastructure failure is not
	// absence. This is not a write fence or a prerequisite callers must repeat
	// before each backend operation.
	CheckMember(context.Context, sessionid.SessionID) error

	// Backend always returns the same nonnil group/incarnation-bound storage.
	// Every operation enforces membership using the member's SessionID.ResourceID()
	// as its history key. Non-SES resource kinds and zero IDs match history.ErrInvalid;
	// well-formed nonmembers match ErrNotMember. Retirement does not drop these
	// checks. Reads remain passive and bounded after retirement and confer no admission.
	// Appends atomically enforce current incarnation, authoritative unexpired
	// validity, expected head, and publication: no takeover or expiry may win
	// between the authoritative acceptance check and publication. Already
	// accepted writes remain accepted even if acknowledgment follows loss.
	// Nonmembers match ErrNotMember; invalid IDs match history.ErrInvalid.
	// Ownership-fence rejection matches ErrReleased, ErrExpired, or
	// ErrSuperseded when observed, otherwise ErrValidityUnknown. The Backend
	// zero-result and unknown-acceptance error contract remains unchanged.
	// No raw unfenced storage capability may escape through this backend.
	Backend() history.Backend

	// Renew preserves this incarnation and returns conservative current evidence.
	// Error returns zero Validity. Infrastructure failure alone neither closes
	// Done nor invalidates earlier evidence before its deadline. Observed loss
	// matches ErrReleased, ErrExpired, or ErrSuperseded and is terminal; inability
	// to prove validity is instead ErrValidityUnknown. A late reply cannot
	// revive local retirement.
	Renew(context.Context) (Validity, error)

	// Release is idempotent and exact-incarnation scoped. Stale calls cannot
	// alter a successor. Nil proves this incarnation no longer holds authority;
	// error or timeout does not prove cleanup. It cannot undo accepted writes
	// or promise cessation of already-started external effects.
	Release(context.Context) error

	// Done returns a stable nonnil channel closed once terminal release, expiry,
	// or supersession is observed. Notification may lag authority loss; the owner
	// must independently enforce Validity. Infrastructure failure alone must
	// not close it. Closure does not prove external work has ceased.
	Done() <-chan struct{}

	// Err is nil before Done closes, then a stable safe terminal cause matching
	// ErrReleased, ErrExpired, or ErrSuperseded through errors.Is.
	Err() error
}

var (
	// ErrReleased reports observed terminal release of this incarnation.
	ErrReleased = errors.New("lease released")
	// ErrExpired reports observed authoritative expiry, not merely an inability
	// to prove local validity. This incarnation can never be renewed or reminted.
	ErrExpired = errors.New("lease expired")
	// ErrSuperseded reports observed takeover by a different incarnation.
	// The old incarnation is terminal even if its previous deadline was later.
	ErrSuperseded = errors.New("lease superseded")
	// ErrValidityUnknown means current validity cannot be established. It is
	// not observed expiry or takeover and must not alone close Hold.Done.
	// Existing evidence remains usable only until its conservative deadline.
	ErrValidityUnknown = errors.New("lease validity unknown")
	// ErrNotMember means a valid Session identity is definitively not assigned to
	// this group. It does not describe an infrastructure or identity error.
	ErrNotMember = errors.New("session is not a group member")
)

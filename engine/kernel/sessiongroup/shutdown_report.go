package sessiongroup

import (
	"errors"

	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
	groupid "github.com/stacklok/mecatl/engine/kernel/sessiongroup/id"
)

// ShutdownRelease describes what bounded shutdown established about lease release.
// It is a closed in-memory result, not persisted state. Unknown values are
// treated as ShutdownReleaseUnknown.
type ShutdownRelease uint8

const (
	// ShutdownReleaseUnknown is the zero value and is not emitted by a completed
	// shutdown. It fail-closes interpretation of an incomplete report.
	ShutdownReleaseUnknown ShutdownRelease = iota
	// ShutdownReleaseNotHeld means shutdown had no acquired hold to release.
	ShutdownReleaseNotHeld
	// ShutdownReleaseReleased proves the group's acquired hold was released.
	ShutdownReleaseReleased
	// ShutdownReleaseUncertain means release was attempted but did not establish
	// whether the provider still recognizes the hold.
	ShutdownReleaseUncertain
	// ShutdownReleaseUnattempted means the shutdown bound or a serialized provider
	// operation prevented a release attempt.
	ShutdownReleaseUnattempted
)

// ShutdownObligationKind identifies the outstanding shutdown responsibility. It
// is a closed in-memory classification, not a persisted label. Unknown values
// are treated as ShutdownObligationUnknown.
type ShutdownObligationKind uint8

const (
	// ShutdownObligationUnknown is the zero value and fail-closes interpretation
	// of an incomplete report.
	ShutdownObligationUnknown ShutdownObligationKind = iota
	// ShutdownObligationProvider identifies an unresolved provider operation.
	ShutdownObligationProvider
	// ShutdownObligationWork identifies local work that did not drain.
	ShutdownObligationWork
	// ShutdownObligationMember identifies private member shutdown state that did
	// not resolve.
	ShutdownObligationMember
)

// ShutdownObligationCause explains why an obligation remains without exposing a
// provider, work, or payload error. It is safe to return or present directly;
// it intentionally does not unwrap a private cause. Unknown values produce the
// fixed unknown text.
type ShutdownObligationCause uint8

const (
	// ShutdownCauseUnknown is the zero value and fail-closes interpretation of an
	// incomplete report.
	ShutdownCauseUnknown ShutdownObligationCause = iota
	// ShutdownCauseDeadline means the whole-shutdown bound elapsed first.
	ShutdownCauseDeadline
	// ShutdownCauseOperationBlocked means a serialized operation did not finish
	// after cancellation, so shutdown could not overlap a replacement operation.
	ShutdownCauseOperationBlocked
	// ShutdownCauseStillActive means locally tracked work or member state remained
	// active when bounded shutdown ended.
	ShutdownCauseStillActive
)

// Error returns a fixed safe description of the unresolved cause. It never
// includes provider or workload error text.
func (c ShutdownObligationCause) Error() string {
	switch c {
	case ShutdownCauseDeadline:
		return "session group shutdown deadline elapsed"
	case ShutdownCauseOperationBlocked:
		return "session group shutdown operation blocked"
	case ShutdownCauseStillActive:
		return "session group shutdown obligation remains active"
	default:
		return "session group shutdown cause unknown"
	}
}

// ShutdownObligation correlates one unresolved shutdown responsibility with a
// group and, where applicable, one member Session. Provider obligations have a
// zero Member because they apply to the group hold; work and member obligations
// carry the affected nonzero SessionID. Cause is a fixed safe classification.
type ShutdownObligation struct {
	// Kind identifies the unresolved provider, work, or member responsibility.
	Kind ShutdownObligationKind
	// Member identifies the affected Session. It is zero only for a
	// group-scoped provider obligation.
	Member sessionid.SessionID
	// Cause explains the unresolved state without exposing private error text.
	Cause ShutdownObligationCause
}

// ErrNotComplete means no final shutdown report is available yet. It says
// nothing about current ownership or whether an individual effect has stopped.
var ErrNotComplete = errors.New("session group shutdown is not complete")

// ShutdownReport is the compact, typed result of one bounded group shutdown. GroupID
// preserves group correlation; it is nonzero in a report returned for an opened
// group. Unresolved is an independently owned slice containing all known unresolved
// obligations, ordered by the shutdown implementation's deterministic member order.
// Its size is bounded by admitted member/work state, not a separate reporting cap;
// reporting never truncates unresolved obligations. Reports are immutable by
// convention after return and safe for concurrent readers when callers do not
// mutate Unresolved.
type ShutdownReport struct {
	// GroupID identifies the group whose shutdown was observed. Its zero value
	// denotes an incomplete or manually constructed report.
	GroupID groupid.GroupID
	// Release records the known release outcome. Uncertain and unattempted
	// states never claim that provider authority ceased.
	Release ShutdownRelease
	// Unresolved holds bounded detail for provider, work, or member obligations.
	Unresolved []ShutdownObligation
}

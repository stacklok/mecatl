package sessiongroup

import (
	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
	groupid "github.com/stacklok/mecatl/engine/kernel/sessiongroup/id"
)

// CloseRelease describes what bounded closing established about lease release.
// It is a closed in-memory result, not persisted state. Unknown values are
// treated as CloseReleaseUnknown.
type CloseRelease uint8

const (
	// CloseReleaseUnknown is the zero value and is not emitted by a completed
	// close. It fail-closes interpretation of an incomplete report.
	CloseReleaseUnknown CloseRelease = iota
	// CloseReleaseNotHeld means close had no acquired hold to release.
	CloseReleaseNotHeld
	// CloseReleaseReleased proves the group's acquired hold was released.
	CloseReleaseReleased
	// CloseReleaseUncertain means release was attempted but did not establish
	// whether the provider still recognizes the hold.
	CloseReleaseUncertain
	// CloseReleaseUnattempted means the close bound or a serialized provider
	// operation prevented a release attempt.
	CloseReleaseUnattempted
)

// CloseObligationKind identifies the outstanding shutdown responsibility. It
// is a closed in-memory classification, not a persisted label. Unknown values
// are treated as CloseObligationUnknown.
type CloseObligationKind uint8

const (
	// CloseObligationUnknown is the zero value and fail-closes interpretation
	// of an incomplete report.
	CloseObligationUnknown CloseObligationKind = iota
	// CloseObligationProvider identifies an unresolved provider operation.
	CloseObligationProvider
	// CloseObligationWork identifies local work that did not drain.
	CloseObligationWork
	// CloseObligationMember identifies private member shutdown state that did
	// not resolve.
	CloseObligationMember
)

// CloseObligationCause explains why an obligation remains without exposing a
// provider, work, or payload error. It is safe to return or present directly;
// it intentionally does not unwrap a private cause. Unknown values produce the
// fixed unknown text.
type CloseObligationCause uint8

const (
	// CloseCauseUnknown is the zero value and fail-closes interpretation of an
	// incomplete report.
	CloseCauseUnknown CloseObligationCause = iota
	// CloseCauseDeadline means the whole-close bound elapsed first.
	CloseCauseDeadline
	// CloseCauseOperationBlocked means a serialized operation did not finish
	// after cancellation, so close could not overlap a replacement operation.
	CloseCauseOperationBlocked
	// CloseCauseStillActive means locally tracked work or member state remained
	// active when bounded closing ended.
	CloseCauseStillActive
)

// Error returns a fixed safe description of the unresolved cause. It never
// includes provider or workload error text.
func (c CloseObligationCause) Error() string {
	switch c {
	case CloseCauseDeadline:
		return "session group close deadline elapsed"
	case CloseCauseOperationBlocked:
		return "session group close operation blocked"
	case CloseCauseStillActive:
		return "session group close obligation remains active"
	default:
		return "session group close cause unknown"
	}
}

// CloseObligation correlates one unresolved closing responsibility with a
// group and, where applicable, one member Session. Provider obligations have a
// zero Member because they apply to the group hold; work and member obligations
// carry the affected nonzero SessionID. Cause is a fixed safe classification.
type CloseObligation struct {
	// Kind identifies the unresolved provider, work, or member responsibility.
	Kind CloseObligationKind
	// Member identifies the affected Session. It is zero only for a
	// group-scoped provider obligation.
	Member sessionid.SessionID
	// Cause explains the unresolved state without exposing private error text.
	Cause CloseObligationCause
}

// CloseReport is the compact, typed result of one bounded group close. GroupID
// preserves group correlation; it is nonzero in a report returned for an opened
// group. Unresolved is an independently owned slice containing all known unresolved
// obligations, ordered by the closing implementation's deterministic member order.
// Its size is bounded by admitted member/work state, not a separate reporting cap;
// reporting never truncates unresolved obligations. Reports are immutable by
// convention after return and safe for concurrent readers when callers do not
// mutate Unresolved.
type CloseReport struct {
	// GroupID identifies the group whose close was observed. Its zero value
	// denotes an incomplete or manually constructed report.
	GroupID groupid.GroupID
	// Release records the known release outcome. Uncertain and unattempted
	// states never claim that provider authority ceased.
	Release CloseRelease
	// Unresolved holds bounded detail for provider, work, or member obligations.
	Unresolved []CloseObligation
}

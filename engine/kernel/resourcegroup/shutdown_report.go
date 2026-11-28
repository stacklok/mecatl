package resourcegroup

import (
	"errors"

	engineid "github.com/stacklok/mecatl/engine/id"
	groupid "github.com/stacklok/mecatl/engine/kernel/resourcegroup/id"
)

// ShutdownRelease is a closed in-memory release result, not persisted state.
// Unknown values are interpreted as ShutdownReleaseUnknown.
type ShutdownRelease uint8

const (
	// ShutdownReleaseUnknown denotes incomplete or unavailable accounting.
	ShutdownReleaseUnknown ShutdownRelease = iota
	// ShutdownReleaseNotHeld means no acquired generation was held for release.
	ShutdownReleaseNotHeld
	// ShutdownReleaseReleased means release of the acquired generation was proved.
	ShutdownReleaseReleased
	// ShutdownReleaseUncertain means attempted release left acceptance unknown.
	ShutdownReleaseUncertain
	// ShutdownReleaseUnattempted means the bound or a serialized outstanding
	// storage operation prevented release from being attempted.
	ShutdownReleaseUnattempted
)

// ShutdownObligationKind is a closed diagnostic responsibility classification.
// Unknown values are interpreted as ShutdownObligationUnknown.
type ShutdownObligationKind uint8

const (
	// ShutdownObligationUnknown denotes unavailable classification.
	ShutdownObligationUnknown ShutdownObligationKind = iota
	// ShutdownObligationProvider identifies an unresolved storage operation.
	ShutdownObligationProvider
	// ShutdownObligationWork identifies local work that did not drain.
	ShutdownObligationWork
	// ShutdownObligationMember identifies unresolved resource completion state.
	ShutdownObligationMember
)

// ShutdownObligationCause is a closed safe diagnostic cause. It never wraps or
// discloses private factory, storage, or payload errors.
type ShutdownObligationCause uint8

const (
	// ShutdownCauseUnknown denotes unavailable classification.
	ShutdownCauseUnknown ShutdownObligationCause = iota
	// ShutdownCauseDeadline means the whole-shutdown bound elapsed first.
	ShutdownCauseDeadline
	// ShutdownCauseOperationBlocked means cancellation did not finish a serialized
	// operation, precluding a concurrent replacement operation.
	ShutdownCauseOperationBlocked
	// ShutdownCauseStillActive means tracked work or instance state remained active.
	ShutdownCauseStillActive
)

// Error returns fixed safe text, including for unknown values, without unwrapping.
func (c ShutdownObligationCause) Error() string {
	switch c {
	case ShutdownCauseDeadline:
		return "resource group shutdown deadline elapsed"
	case ShutdownCauseOperationBlocked:
		return "resource group shutdown operation blocked"
	case ShutdownCauseStillActive:
		return "resource group shutdown obligation remains active"
	default:
		return "resource group shutdown cause unknown"
	}
}

// ShutdownObligation correlates one unresolved responsibility with a resource
// where applicable. It is diagnostic accounting, not separate lifecycle machinery.
type ShutdownObligation struct {
	// Kind identifies the unresolved storage, work, or resource responsibility.
	Kind ShutdownObligationKind
	// Member is the affected generic resource ID, or zero for group-scoped storage.
	Member engineid.ID
	// Cause is a fixed safe classification, never private error text.
	Cause ShutdownObligationCause
}

// ErrNotComplete means no final shutdown report is available. It says nothing
// about authority or whether individual effects have stopped.
var ErrNotComplete = errors.New("resource group shutdown is not complete")

// ShutdownReport accounts for one bounded Group shutdown, including an opening
// that never claimed authority. It does not prove that all goroutines or effects
// stopped. Unresolved is detached, ordered deterministically by implementation,
// and includes every known outstanding obligation without a separate reporting
// cap or silent truncation. Callers own the slice; concurrent reads require no
// concurrent mutation. Current operational methods never produce a report.
type ShutdownReport struct {
	// GroupID correlates the Group, even for an opening that never acquired authority.
	GroupID groupid.GroupID
	// Release records the established release outcome, not inferred cessation.
	Release ShutdownRelease
	// Unresolved includes all known unresolved storage, work, and resource obligations.
	Unresolved []ShutdownObligation
}

// ManagerShutdownReport is detached final accounting for all resident and opening
// Groups, including openings never claimed. There is no undisclosed truncation.
// Each returned report owns its outer and nested slices; concurrent readers must
// not mutate them. Bounded completion does not end outstanding dependency use.
type ManagerShutdownReport struct {
	// Groups contains every coordinated Group's final accounting, in an
	// implementation-defined deterministic order.
	Groups []ShutdownReport
}

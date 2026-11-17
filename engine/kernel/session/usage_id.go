package session

import (
	"errors"

	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

// UsageID identifies one accounting occurrence within a history, not a model
// invocation, content identity, or verified charge. It is comparable and owns no
// mutable storage. Zero is invalid; allocation order is not a public contract.
type UsageID struct {
	// historyID is the enclosing history, independent of any correlated run.
	historyID historyid.HistoryID
	// usageOrdinal distinguishes this private shape from other occurrence IDs.
	usageOrdinal uint64
}

// HistoryID returns the enclosing history identity, or zero for zero.
func (v UsageID) HistoryID() historyid.HistoryID { return v.historyID }

// HistoryRelative returns the positive history-relative component, or zero for
// zero. The component alone establishes neither allocation nor membership.
func (v UsageID) HistoryRelative() uint64 { return v.usageOrdinal }

// NewUsageID reconstructs a structurally valid scoped identity. It does not
// allocate usage, prove membership or authority, or imply an allocation order.
// Failure returns the zero identity.
func NewUsageID(historyID historyid.HistoryID, component uint64) (UsageID, error) {
	id := UsageID{historyID: historyID, usageOrdinal: component}
	if err := id.Validate(); err != nil {
		return UsageID{}, err
	}
	return id, nil
}

// Validate checks structural validity without mutation or history lookup.
func (v UsageID) Validate() error {
	if _, err := historyid.ParseHistoryID(v.historyID.String()); err != nil {
		return errors.New("invalid usage history ID")
	}
	if v.usageOrdinal == 0 {
		return errors.New("usage ordinal must be positive")
	}
	return nil
}

// Equal reports whether both history-scoped occurrences are identical.
func (v UsageID) Equal(other UsageID) bool { return v == other }

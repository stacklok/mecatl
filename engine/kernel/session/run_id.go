package session

import (
	"errors"

	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

// RunID identifies a run occurrence, not a successful execution or content CID.
// It is comparable and contains no mutable storage. Zero is invalid. Obtain IDs
// from session allocation or inspection; their allocation order is not a public contract.
type RunID struct {
	// historyID is the history in which the run occurs.
	historyID historyid.HistoryID
	// ordinal is the one-based run ordinal, independently of input ordinals.
	ordinal uint64
}

// HistoryID returns the enclosing history identity, or zero for zero.
func (v RunID) HistoryID() historyid.HistoryID { return v.historyID }

// validateOrdinal checks the local occurrence ordinal.
func (v RunID) validateOrdinal() error {
	if v.ordinal == 0 {
		return errors.New("run ordinal must be positive")
	}
	return nil
}

// Validate checks local identity validity without mutation or history lookup.
// It does not establish acceptance or monotonic allocation.
func (v RunID) Validate() error {
	if _, err := historyid.ParseHistoryID(v.historyID.String()); err != nil {
		return errors.New("invalid run history ID")
	}
	return v.validateOrdinal()
}

// Equal reports whether both history-scoped occurrences are identical.
func (v RunID) Equal(other RunID) bool { return v == other }

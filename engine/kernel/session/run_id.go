package session

import (
	"errors"
	"math"

	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

// RunID identifies a run occurrence, not a successful execution or content CID.
// It is comparable and contains no mutable storage. Zero is invalid.
type RunID struct {
	// HistoryID is the history in which the run occurs.
	HistoryID historyid.HistoryID
	// Ordinal is the one-based run ordinal, independently of input ordinals.
	Ordinal uint64
}

// validateOrdinal checks the local occurrence ordinal.
func (v RunID) validateOrdinal() error {
	if v.Ordinal == 0 {
		return errors.New("run ordinal must be positive")
	}
	return nil
}

// Validate checks local identity validity without mutation or history lookup.
// It does not establish acceptance or monotonic allocation.
func (v RunID) Validate() error {
	if _, err := historyid.ParseHistoryID(v.HistoryID.String()); err != nil {
		return errors.New("invalid run history ID")
	}
	return v.validateOrdinal()
}

// Clone returns v, which contains no mutable storage.
func (v RunID) Clone() RunID { return v }

// Equal reports whether both history-scoped occurrences are identical.
func (v RunID) Equal(other RunID) bool { return v == other }

// Next returns the next ordinal in the same history, or zero on invalid input or
// exhaustion. It neither allocates nor reserves an accepted occurrence.
func (v RunID) Next() (RunID, error) {
	if err := v.Validate(); err != nil {
		return RunID{}, err
	}
	if v.Ordinal == math.MaxUint64 {
		return RunID{}, ErrOrdinalExhausted
	}
	return RunID{HistoryID: v.HistoryID, Ordinal: v.Ordinal + 1}, nil
}

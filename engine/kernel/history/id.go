// Package history defines the public vocabulary of committed kernel history.
// It does not implement storage or execute models and tools.
package history

import historyid "github.com/stacklok/mecatl/engine/kernel/history/id"

// HistoryID identifies one linear, append-only history, independently of the
// state reconstructed from it. It aliases the codec-independent identity value
// so callers can use either package without conversion.
type HistoryID = historyid.HistoryID

// NewHistoryID generates a history identity, panicking if secure randomness is
// unavailable. The store must enforce create-only semantics and must not reuse
// an existing identity, including after deletion.
func NewHistoryID() HistoryID {
	return historyid.NewHistoryID()
}

// ParseHistoryID validates a full history ID without normalization.
func ParseHistoryID(s string) (HistoryID, error) {
	return historyid.ParseHistoryID(s)
}

// Package history defines the public vocabulary of committed kernel history.
// It does not implement storage or execute models and tools.
package history

import (
	"errors"

	"github.com/stacklok/mecatl/engine/id"
)

// HistoryID identifies one linear, append-only history, independently of the
// state reconstructed from it. IDs are server-assigned and never reassigned.
// Its zero value is unset, not valid. HistoryIDs are comparable map keys.
type HistoryID struct {
	value id.ID
}

// NewHistoryID generates a history identity, panicking if secure randomness is
// unavailable. The store must enforce create-only semantics and must not reuse
// an existing identity, including after deletion.
func NewHistoryID() HistoryID {
	return HistoryID{value: id.New(id.History)}
}

// ParseHistoryID validates a full history ID without normalization.
func ParseHistoryID(s string) (HistoryID, error) {
	v, err := id.Parse(s)
	if err != nil {
		return HistoryID{}, err
	}
	if v.Kind() != id.History {
		return HistoryID{}, errors.New("expected history ID")
	}
	return HistoryID{value: v}, nil
}

// String returns the full ID, or an empty string for an unset HistoryID.
func (v HistoryID) String() string { return v.value.String() }

// Package id defines history identity values independently of history formats
// and storage. Content-addressed commit identifiers are a separate contract.
package id

import (
	"errors"

	resourceid "github.com/stacklok/mecatl/engine/id"
)

// HistoryID identifies one linear, append-only history, independently of the
// state reconstructed from it. IDs are server-assigned and never reassigned.
// Its zero value is unset, not valid. HistoryIDs are comparable map keys.
type HistoryID struct {
	value resourceid.ID
}

// NewHistoryID generates a history identity, panicking if secure randomness is
// unavailable. The store must enforce create-only semantics and must not reuse
// an existing identity, including after deletion.
func NewHistoryID() HistoryID {
	return HistoryID{value: resourceid.New(resourceid.History)}
}

// ParseHistoryID validates a full history ID without normalization.
func ParseHistoryID(s string) (HistoryID, error) {
	v, err := resourceid.Parse(s)
	if err != nil {
		return HistoryID{}, err
	}
	if v.Kind() != resourceid.History {
		return HistoryID{}, errors.New("expected history ID")
	}
	return HistoryID{value: v}, nil
}

// String returns the full ID, or an empty string for an unset HistoryID.
func (v HistoryID) String() string { return v.value.String() }

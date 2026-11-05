//go:generate go -C ../../../.. run ./internal/cborgen/cmd/cborgen -dir engine/kernel/history/id -types HistoryID -out zz_cbor_generated.go

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
// CBOR is the underlying resource ID text, with no additional wrapping.
//
//cborgen:validate validateCBOR
type HistoryID struct {
	value resourceid.ID `cbor:",value"` //nolint:revive // cborgen's explicit transparent representation, not a standard CBOR tag option.
}

// validateCBOR checks only the history-specific kind after the generated child
// traversal has validated the resource ID.
func (v HistoryID) validateCBOR() error {
	if v.value.Kind() != resourceid.History {
		return errors.New("expected history ID")
	}
	return nil
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

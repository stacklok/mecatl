package groupstorage

import (
	"errors"

	resourceid "github.com/stacklok/mecatl/engine/id"
)

const acquisitionKind resourceid.Kind = "ACQ"

// AcquisitionID is an opaque, comparable identity for one logical claim request.
// Its zero value is unset and invalid. Repeating Claim with the current
// AcquisitionID reconciles a lost response; it never creates a new generation.
// It has no ordering or arithmetic semantics and is not a persisted DTO.
type AcquisitionID struct {
	value resourceid.ID
}

// NewAcquisitionID generates a unique claim identity. It reserves no authority
// and panics only when secure randomness is unavailable.
func NewAcquisitionID() AcquisitionID {
	return AcquisitionID{value: resourceid.New(acquisitionKind)}
}

// ParseAcquisitionID validates exact ACQ-kind text without normalization.
// Failure returns the zero identity.
func ParseAcquisitionID(s string) (AcquisitionID, error) {
	value, err := resourceid.Parse(s)
	if err != nil {
		return AcquisitionID{}, err
	}
	if value.Kind() != acquisitionKind {
		return AcquisitionID{}, errors.New("expected acquisition ID")
	}
	return AcquisitionID{value: value}, nil
}

// ResourceID returns the underlying generic identity, or zero when unset.
func (v AcquisitionID) ResourceID() resourceid.ID { return v.value }

// String returns the full identity, or an empty string when unset.
func (v AcquisitionID) String() string { return v.value.String() }

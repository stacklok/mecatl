package groupstorage

import (
	"errors"

	resourceid "github.com/stacklok/mecatl/engine/id"
)

const generationKind resourceid.Kind = "GEN"

// Generation is an opaque, comparable incarnation of group authority. Its zero
// value is unset and invalid. A generation identifies neither time nor ordering;
// callers must not derive successors or authority from its representation. It is
// not a persisted DTO.
type Generation struct {
	value resourceid.ID
}

// NewGeneration generates a unique authority incarnation. It reserves no
// authority and panics only when secure randomness is unavailable.
func NewGeneration() Generation {
	return Generation{value: resourceid.New(generationKind)}
}

// ParseGeneration validates exact GEN-kind text without normalization. Failure
// returns the zero generation.
func ParseGeneration(s string) (Generation, error) {
	value, err := resourceid.Parse(s)
	if err != nil {
		return Generation{}, err
	}
	if value.Kind() != generationKind {
		return Generation{}, errors.New("expected generation ID")
	}
	return Generation{value: value}, nil
}

// ResourceID returns the underlying generic identity, or zero when unset.
func (v Generation) ResourceID() resourceid.ID { return v.value }

// String returns the full identity, or an empty string when unset.
func (v Generation) String() string { return v.value.String() }

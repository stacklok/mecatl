package format

import "errors"

// UserInput is one authored submission. Zero ordinals are invalid. Assignment
// shares Parts and children; Clone detaches them. Nil and empty Parts are equal.
//
//dtogen:validate Validate
type UserInput struct {
	// RunID is the required one-based owning run ordinal within the enclosing
	// history. Conversion restores history scope from the envelope; the aggregate
	// checks active-run membership, not this payload's local validator.
	RunID uint64 `cbor:"r"`
	// ID is the required one-based user-submission ordinal within RunID. Local
	// validation checks positivity; the aggregate checks contiguous allocation.
	ID uint64 `cbor:"i"`
	// Parts preserves exact submission order, including duplicates. Nil and empty
	// are equivalent valid empty submissions; codec traversal validates children.
	Parts []ContentPart `cbor:"p"`
}

// Validate checks local ordinals, not aggregate admission or resource policy.
func (v UserInput) Validate() error {
	if v.RunID == 0 || v.ID == 0 {
		return errors.New("input requires positive run and input ordinals")
	}
	return nil
}

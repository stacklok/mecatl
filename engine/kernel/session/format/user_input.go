package format

import "errors"

// UserInput is one authored submission. Zero ordinals are invalid. Assignment
// shares Parts and children; Clone detaches them. Nil and empty Parts are equal.
//
//cborgen:validate Validate
type UserInput struct {
	// RunID is the nonzero owning run ordinal within the history.
	RunID uint64 `cbor:"r"`
	// ID is the nonzero input ordinal within the owning run.
	ID uint64 `cbor:"i"`
	// Parts preserves exact content order.
	Parts []ContentPart `cbor:"p"`
}

// Validate checks local ordinals, not aggregate admission or resource policy.
func (v UserInput) Validate() error {
	if v.RunID == 0 || v.ID == 0 {
		return errors.New("input requires positive run and input ordinals")
	}
	return nil
}

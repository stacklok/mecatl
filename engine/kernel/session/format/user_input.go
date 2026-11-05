package format

import "errors"

// UserInput preserves ordered authored message boundaries. Zero ordinals are
// invalid. Assignment shares Messages and its children; Clone detaches them.
// Nil and empty messages are equivalent, but differ from one empty message.
//
//cborgen:validate Validate
type UserInput struct {
	// RunID is the nonzero owning run ordinal within the history.
	RunID uint64 `cbor:"r"`
	// ID is the nonzero input ordinal within the owning run.
	ID uint64 `cbor:"i"`
	// Messages retains exact content order and supplied message boundaries.
	Messages []AuthoredMessage `cbor:"m"`
}

// Validate checks both local ordinals, without mutation or resource policy.
func (v UserInput) Validate() error {
	if v.RunID == 0 {
		return errors.New("owning run ordinal must be positive")
	}
	if v.ID == 0 {
		return errors.New("input ordinal must be positive")
	}
	return nil
}

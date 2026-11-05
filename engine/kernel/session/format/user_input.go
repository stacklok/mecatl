package format

import "errors"

// UserInput preserves one input's exact opaque bytes. Zero is invalid. Assignment
// shares Content; Clone detaches it. Nil and empty content are equivalent.
//
//cborgen:validate Validate
type UserInput struct {
	// RunID is the nonzero owning run ordinal within the history.
	RunID uint64 `cbor:"r"`
	// ID is the nonzero input ordinal within the owning run.
	ID uint64 `cbor:"i"`
	// Content is uninterpreted, including invalid UTF-8 and empty content.
	Content []byte `cbor:"c"`
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

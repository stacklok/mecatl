package format

import "errors"

// RunStarted names a new run. Input-free runs are valid. Zero is invalid.
//
//dtogen:validate Validate
type RunStarted struct {
	// ID is the required one-based run ordinal within the enclosing history.
	// Conversion restores the history identity from the envelope. Local validation
	// checks positivity; the aggregate checks contiguous allocation and admission.
	ID uint64 `cbor:"r"`
}

// Validate checks the local ordinal without mutation or resource policy.
func (v RunStarted) Validate() error {
	if v.ID == 0 {
		return errors.New("run ordinal must be positive")
	}
	return nil
}

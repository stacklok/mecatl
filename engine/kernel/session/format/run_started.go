package format

import "errors"

// RunStarted names a new run. Input-free runs are valid. Zero is invalid.
//
//cborgen:validate Validate
type RunStarted struct {
	// ID is the nonzero run ordinal within the owning history.
	ID uint64 `cbor:"r"`
}

// Validate checks the local ordinal without mutation or resource policy.
func (v RunStarted) Validate() error {
	if v.ID == 0 {
		return errors.New("run ordinal must be positive")
	}
	return nil
}

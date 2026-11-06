package format

import "errors"

// RunInterrupted explicitly closes a run without claiming success or cancellation
// of external work. Zero is invalid. It contains no mutable storage.
//
//cborgen:validate Validate
type RunInterrupted struct {
	// ID is the required one-based ordinal of the interrupted run within the
	// enclosing history, not a new occurrence. Conversion restores history scope
	// from the envelope. Local validation checks positivity; the aggregate checks
	// that this names the active run.
	ID uint64 `cbor:"r"`
}

// Validate checks only the local ordinal, not whether the run is active.
func (v RunInterrupted) Validate() error {
	if v.ID == 0 {
		return errors.New("run ordinal must be positive")
	}
	return nil
}

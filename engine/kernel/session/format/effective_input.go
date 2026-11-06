package format

import "errors"

// EffectiveInput is one ordered incremental contribution owned by a run, not a
// request or replacement. Nil and empty Inputs are equal; duplicates are retained.
// Assignment shares nested storage; Clone detaches it.
//
//cborgen:validate Validate
type EffectiveInput struct {
	// RunID is the required one-based owning run ordinal within the enclosing
	// history. Conversion restores history scope from the envelope. Validate checks
	// positivity only; the aggregate checks active-run admission and references.
	RunID uint64 `cbor:"r"`
	// Inputs preserves contribution order, boundaries and duplicates. Nil and empty
	// are equivalent valid empty increments, not clearing or replacement operations.
	// Codec traversal validates children; the aggregate checks that user references
	// name earlier submissions (including earlier same-batch facts) in the same
	// active run and history.
	Inputs []InputItem `cbor:"i"`
}

// Validate checks the local run ordinal, not membership or ordering.
func (v EffectiveInput) Validate() error {
	if v.RunID == 0 {
		return errors.New("effective input requires a positive run ordinal")
	}
	return nil
}

package format

import "errors"

// EffectiveInput is one ordered incremental contribution owned by a run, not a
// request or replacement. Nil and empty Inputs are equal; duplicates are retained.
// Assignment shares nested storage; Clone detaches it.
//
//cborgen:validate Validate
type EffectiveInput struct {
	RunID  uint64      `cbor:"r"`
	Inputs []InputItem `cbor:"i"`
}

// Validate checks the local run ordinal, not membership or ordering.
func (v EffectiveInput) Validate() error {
	if v.RunID == 0 {
		return errors.New("effective input requires a positive run ordinal")
	}
	return nil
}

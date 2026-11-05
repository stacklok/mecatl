package format

import "errors"

// EffectiveInput retains one complete ordered prepared revision for an earlier
// authored input. Zero ordinals are invalid. Nil and empty Prompts are equivalent
// explicit empty revisions. Assignment shares Prompts; Clone detaches them.
//
//cborgen:validate Validate
type EffectiveInput struct {
	RunID   uint64           `cbor:"r"`
	InputID uint64           `cbor:"i"`
	Prompts []PreparedPrompt `cbor:"p"`
}

// Validate checks local source ordinals, not membership, ordering, or message bounds.
func (v EffectiveInput) Validate() error {
	if v.RunID == 0 || v.InputID == 0 {
		return errors.New("effective input requires positive run and input ordinals")
	}
	return nil
}

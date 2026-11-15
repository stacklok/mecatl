package format

// UserContextPrefix is a complete session-scoped ordered prepared user-context
// prefix replacement, independent of root Instructions. Latest replacement wins
// across runs; nil and empty Blocks explicitly clear it. Assignment shares Blocks
// and children; Clone detaches them. History scope comes from the envelope.
//
//dtogen:include
type UserContextPrefix struct {
	// Blocks preserves prepared contribution order and boundaries, including
	// duplicates and empty blocks. Nil and empty explicitly clear the prefix.
	// Child provenance is required even for an empty block and grants no authority.
	Blocks []InstructionBlock `cbor:"b"`
}

// Validate checks all prepared blocks and their provenance without operational limits.
func (v UserContextPrefix) Validate() error {
	for _, block := range v.Blocks {
		if err := block.Validate(); err != nil {
			return err
		}
	}
	return nil
}

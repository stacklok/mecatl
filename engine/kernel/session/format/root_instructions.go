package format

// Validate checks all ordered blocks and key uniqueness without operational limits.
func (v RootInstructions) Validate() error {
	for _, block := range v.Blocks {
		if err := block.Validate(); err != nil {
			return err
		}
	}
	return v.validateLocal()
}

// validateLocal checks key uniqueness after bounded child traversal.
func (v RootInstructions) validateLocal() error { return validateInstructionKeys(v.Blocks) }

// RootInstructions is the complete session-scoped replacement for the harness
// root operating contract. It is selected by intended placement and authority,
// not a producer, file, or provider message role. Nil and empty Blocks are
// equivalent explicit empty replacements; a single empty block is distinct and
// still needs a key and provenance. Assignment shares Blocks and children;
// Clone detaches them. Session scope comes from the root history envelope.
//
//dtogen:validate validateLocal
//dtogen:include
type RootInstructions struct {
	// Blocks preserves instruction contribution order and boundaries, including
	// duplicate contents and empty blocks, but never duplicate keys within this
	// whole replacement. Slice order, not keys, determines order; this is not a
	// patch API. Nil and empty explicitly clear root instructions.
	// Codec traversal validates children; Session identity is omitted here and
	// restored from the root history envelope at the domain conversion boundary.
	Blocks []InstructionBlock `cbor:"b"`
}

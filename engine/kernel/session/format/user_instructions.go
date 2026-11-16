package format

// UserInstructions is the complete session-scoped replacement for prepared
// user-level model-input standing guidance, including prepared SOUL.md/persona and
// applicable AGENTS.md or CLAUDE.md guidance. It is selected by intended
// placement and authority, not a producer, file, or provider message role.
// Dynamically triggered guidance enters chronological EffectiveInput with
// attribution rather than retroactively replacing this standing value. Latest
// replacement wins across runs; nil and empty Blocks explicitly clear it.
// Assignment shares Blocks and children; Clone detaches them. History scope
// comes from the envelope.
//
//dtogen:validate validateLocal
//dtogen:include
type UserInstructions struct {
	// Blocks preserves prepared contribution order and boundaries, including
	// duplicate contents and empty blocks, but never duplicate keys within this
	// whole replacement. Slice order, not keys, determines order; this is not a
	// patch API. Nil and empty explicitly clear the instructions.
	// Child keys and provenance are required even for empty blocks and grant no
	// authority.
	Blocks []InstructionBlock `cbor:"b"`
}

// Validate checks all prepared blocks, provenance, and key uniqueness without
// operational limits.
func (v UserInstructions) Validate() error {
	for _, block := range v.Blocks {
		if err := block.Validate(); err != nil {
			return err
		}
	}
	return v.validateLocal()
}

// validateLocal checks key uniqueness after bounded child traversal.
func (v UserInstructions) validateLocal() error { return validateInstructionKeys(v.Blocks) }

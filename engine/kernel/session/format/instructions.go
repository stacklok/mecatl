package format

// InstructionBlock retains one ordered instruction contribution and its declared
// origin. Its purpose is instructions by construction, not inferred from a file.
// Assignment shares nested storage; Clone detaches it.
type InstructionBlock struct {
	// Parts preserves exact instruction order, including duplicates. Nil and empty
	// are equivalent valid empty contributions; codec traversal validates children.
	Parts []ContentPart `cbor:"c"`
	// Source is required provenance even for an empty block; it does not confer
	// authority. Codec traversal validates the source and its children.
	Source ContentSource `cbor:"s"`
}

// Instructions is a complete session-scoped ordered root-instruction replacement.
// Nil and empty Blocks are equivalent explicit empty replacements; a single empty
// block is distinct and still needs provenance. Assignment shares Blocks and
// children; Clone detaches them. History scope comes from the enclosing envelope.
type Instructions struct {
	// Blocks preserves instruction contribution order and boundaries, including
	// duplicates and empty blocks. Nil and empty explicitly clear root instructions.
	// Codec traversal validates children; history identity is omitted here and
	// restored from the enclosing envelope at the domain conversion boundary.
	Blocks []InstructionBlock `cbor:"b"`
}

package format

// InstructionBlock retains one ordered instruction contribution and its declared
// origin. Its purpose is instructions by construction, not inferred from a file.
// Assignment shares nested storage; Clone detaches it.
type InstructionBlock struct {
	Content []ContentPart `cbor:"c"`
	Source  ContentSource `cbor:"s"`
}

// Instructions is a complete session-scoped ordered root-instruction replacement.
// Nil and empty Blocks are equivalent explicit empty replacements; a single empty
// block is distinct and still needs provenance. Assignment shares Blocks and
// children; Clone detaches them. History scope comes from the enclosing envelope.
type Instructions struct {
	Blocks []InstructionBlock `cbor:"b"`
}

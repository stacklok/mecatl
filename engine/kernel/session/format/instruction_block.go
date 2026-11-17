package format

import (
	"errors"
	"unicode"
	"unicode/utf8"
)

// InstructionBlock retains one ordered prepared contribution and its declared
// provenance. Its containing RootInstructions or UserInstructions determines
// the intended instruction layer; neither the key nor provenance grants authority.
// Content meaning is not inferred from a file. Assignment shares nested storage;
// Clone detaches it. Zero is invalid.
//
//dtogen:validate validateLocal
type InstructionBlock struct {
	// Key is required even for empty contributions: 1..256 UTF-8 bytes, valid
	// UTF-8, with no unicode.IsSpace or unicode.IsControl rune. Exact bytes are
	// preserved without trimming, normalization, or case folding. The vocabulary
	// is open, with no reserved prefixes; examples include harness/core,
	// operator/persona, and workspace/root-guidance. Producers conventionally keep
	// keys stable across source or text changes. Keys are bookkeeping only: they
	// confer no trust, authority, precedence, or refresh behavior and are omitted
	// from logical request content and its CID. The enclosing whole replacement
	// checks uniqueness; there is no cross-collection, history, or lifetime registry.
	Key string `cbor:"k" cborlimit:"bytes=256"`
	// Parts preserves exact instruction order, including duplicates. Nil and empty
	// are equivalent valid empty contributions; codec traversal validates children.
	Parts []ContentPart `cbor:"c"`
	// Source is required provenance even for an empty block; it does not confer
	// authority. Codec traversal validates the source and its children.
	Source ContentSource `cbor:"s"`
}

// Validate checks key, instruction content, and required provenance without
// operational limits. CheckCBOR performs bounded child traversal first.
func (v InstructionBlock) Validate() error {
	for _, part := range v.Parts {
		if err := part.Validate(); err != nil {
			return err
		}
	}
	if err := v.Source.Validate(); err != nil {
		return err
	}
	return v.validateLocal()
}

// validateLocal checks the immediate key without traversing child payloads.
func (v InstructionBlock) validateLocal() error {
	if len(v.Key) == 0 || len(v.Key) > 256 || !utf8.ValidString(v.Key) {
		return errors.New("instruction block key requires 1..256 valid UTF-8 bytes")
	}
	for _, r := range v.Key {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return errors.New("instruction block key must not contain whitespace or control runes")
		}
	}
	return nil
}

// validateInstructionKeys checks whole-replacement uniqueness after child
// validation, so bounded codec traversal precedes allocation of the key set.
func validateInstructionKeys(blocks []InstructionBlock) error {
	seen := make(map[string]struct{}, len(blocks))
	for _, block := range blocks {
		if _, ok := seen[block.Key]; ok {
			return errors.New("duplicate instruction block key")
		}
		seen[block.Key] = struct{}{}
	}
	return nil
}

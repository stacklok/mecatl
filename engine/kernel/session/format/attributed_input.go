package format

import (
	"errors"
	"unicode/utf8"
)

// Closed purposes declare intent independently of actor, provider role or trust.
const (
	// PromptInstructions declares supplemental instruction content.
	PromptInstructions = "instructions"
	// PromptContext declares supplemental contextual data.
	PromptContext = "context"
)

// AttributedInput retains content, a nonempty UTF-8 logical-origin label, purpose
// and provenance. Actor is not a provider role, authentication or trust claim.
// Assignment shares Parts and Source storage; Clone detaches them.
//
//cborgen:validate Validate
type AttributedInput struct {
	// Parts preserves exact contribution order, including duplicates. Nil and empty
	// are equivalent valid empty contributions; codec traversal validates children.
	Parts []ContentPart `cbor:"c"`
	// Actor is a required nonempty exact UTF-8 logical-origin label. It is open,
	// not an enum, provider role, authenticated identity or trust claim.
	Actor string `cbor:"a"`
	// Purpose is required: PromptInstructions or PromptContext, independent of Actor.
	Purpose string `cbor:"p"`
	// Source is required provenance, even for empty Parts. Codec traversal validates
	// it separately; local Validate checks only Actor and Purpose.
	Source ContentSource `cbor:"s"`
}

// Validate checks actor and purpose locally, not child validity or authority.
func (v AttributedInput) Validate() error {
	if v.Actor == "" || !utf8.ValidString(v.Actor) {
		return errors.New("actor must be nonempty UTF-8")
	}
	if v.Purpose != PromptInstructions && v.Purpose != PromptContext {
		return errors.New("unknown input purpose")
	}
	return nil
}

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

// InputItem selects a whole user submission or inline attributed content. UserInputID
// is a positive ordinal in the enclosing EffectiveInput's run. Assignment shares
// pointers and children; Clone detaches them.
//
//cborgen:validate Validate
type InputItem struct {
	// UserInputID optionally references a whole submission by its required positive
	// ordinal. The enclosing EffectiveInput supplies run scope, and its history
	// envelope supplies history scope; conversion restores both. Inline must be nil.
	// Validate checks positivity/exclusivity, not earlier same-run membership.
	UserInputID *uint64 `cbor:"u,omitempty"`
	// Inline optionally retains attributed content, including an empty contribution.
	// UserInputID must be nil. Codec traversal validates a present child; neither
	// branch may be absent together, and both may not be present together.
	Inline *AttributedInput `cbor:"i,omitempty"`
}

// Validate checks variant exclusivity and the local reference, not membership.
func (v InputItem) Validate() error {
	if (v.UserInputID == nil) == (v.Inline == nil) {
		return errors.New("input item requires exactly one origin")
	}
	if v.UserInputID != nil && *v.UserInputID == 0 {
		return errors.New("input reference must be positive")
	}
	return nil
}

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

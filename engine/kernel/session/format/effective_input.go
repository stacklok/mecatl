package format

import (
	"errors"
	"unicode/utf8"
)

// Actor is an open, nonempty UTF-8 logical-origin label, not authentication or trust.
type Actor string

// PromptPurpose is the closed semantic intent, independent of origin or trust.
type PromptPurpose string

// Closed purposes declare intent independently of actor, provider role or trust.
const (
	// PromptInstructions declares supplemental instruction content.
	PromptInstructions PromptPurpose = "instructions"
	// PromptContext declares supplemental contextual data.
	PromptContext PromptPurpose = "context"
)

// InputItem selects a whole user submission or inline attributed content. UserInputID
// is a positive ordinal in the enclosing EffectiveInput's run. Assignment shares
// pointers and children; Clone detaches them. The component is interpreted in the
// explicit destination run when authored, without proof of its extraction origin.
//
//dtogen:validate validateLocal
type InputItem struct {
	// UserInputID optionally references a whole submission by its required positive
	// ordinal. The enclosing EffectiveInput supplies run scope, and its history
	// envelope supplies history scope; conversion restores both. Inline must be nil.
	// Validate checks positivity/exclusivity, not earlier same-run membership.
	UserInputID *uint64 `cbor:"u,omitempty" view:"manual"`
	// Inline optionally retains attributed content, including an empty contribution.
	// UserInputID must be nil. Codec traversal validates a present child; neither
	// branch may be absent together, and both may not be present together.
	Inline *AttributedInput `cbor:"i,omitempty"`
}

// Validate checks variant exclusivity, the relative reference and inline content.
// Membership is interpreted only within the explicit destination run.
func (v InputItem) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	if v.Inline != nil {
		return v.Inline.Validate()
	}
	return nil
}

// validateLocal checks the union after bounded codec traversal validates children.
func (v InputItem) validateLocal() error {
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
//dtogen:validate validateLocal
type AttributedInput struct {
	// Parts preserves exact contribution order, including duplicates. Nil and empty
	// are equivalent valid empty contributions; codec traversal validates children.
	Parts []ContentPart `cbor:"c"`
	// Actor is a required nonempty exact UTF-8 logical-origin label. It is open,
	// not an enum, provider role, authenticated identity or trust claim.
	Actor Actor `cbor:"a"`
	// Purpose is required: PromptInstructions or PromptContext, independent of Actor.
	Purpose PromptPurpose `cbor:"p"`
	// Source is required provenance, even for empty Parts. Codec traversal validates
	// it separately; public Validate also checks all children.
	Source ContentSource `cbor:"s"`
}

// Validate checks actor, closed purpose, ordered content and provenance.
func (v AttributedInput) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	for _, part := range v.Parts {
		if err := part.Validate(); err != nil {
			return err
		}
	}
	return v.Source.Validate()
}

// validateLocal checks labels after bounded codec traversal validates children.
func (v AttributedInput) validateLocal() error {
	if v.Actor == "" || !utf8.ValidString(string(v.Actor)) {
		return errors.New("actor must be nonempty UTF-8")
	}
	if v.Purpose != PromptInstructions && v.Purpose != PromptContext {
		return errors.New("unknown input purpose")
	}
	return nil
}

// EffectiveInput is one ordered incremental contribution owned by a run, not a
// request or replacement. Nil and empty Inputs are equal; duplicates are retained.
// Assignment shares nested storage; Clone detaches it. Session authoring supplies
// the destination separately and requires RunID zero; persisted validation does not.
//
//dtogen:validate validateLocal
type EffectiveInput struct {
	// RunID is the required one-based owning run ordinal within the enclosing
	// history. Conversion restores history scope from the envelope. Validate checks
	// positivity only; the aggregate checks active-run admission and references.
	RunID uint64 `cbor:"r" view:"manual"`
	// Inputs preserves contribution order, boundaries and duplicates. Nil and empty
	// are equivalent valid empty increments, not clearing or replacement operations.
	// Codec traversal validates children; the aggregate checks that user references
	// name earlier submissions (including earlier same-batch facts) in the same
	// active run and history.
	Inputs []InputItem `cbor:"i" view:"manual"`
}

// Validate checks persisted run identity and all contribution items, not membership.
func (v EffectiveInput) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	for _, input := range v.Inputs {
		if err := input.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// validateLocal checks run identity after bounded codec traversal validates children.
func (v EffectiveInput) validateLocal() error {
	if v.RunID == 0 {
		return errors.New("effective input requires a positive run ordinal")
	}
	return nil
}

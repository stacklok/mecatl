package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// Actor is a nonempty UTF-8 logical-origin label, preserved exactly. It is not
// a provider role, identity registry, authentication, permission or trust claim.
type Actor string

// PromptPurpose is semantic intent, independent of origin, provider role or trust.
type PromptPurpose string

const (
	// PromptInstructions declares supplemental instruction content.
	PromptInstructions PromptPurpose = "instructions"
	// PromptContext declares supplemental contextual data.
	PromptContext PromptPurpose = "context"
)

// AttributedInput retains one inline contribution and its declared origin.
// Empty Parts is valid and contributes no content. Assignment shares Parts and
// Source storage; Clone detaches it. Item and actor boundaries are significant.
type AttributedInput struct {
	Parts   []ContentPart
	Actor   Actor
	Purpose PromptPurpose
	Source  ContentSource
}

// Validate checks actor, purpose, content and provenance without resource limits.
func (v AttributedInput) Validate() error {
	if err := (sessionformat.AttributedInput{Actor: string(v.Actor), Purpose: string(v.Purpose)}).Validate(); err != nil {
		return err
	}
	if err := validateParts(v.Parts); err != nil {
		return err
	}
	return v.Source.Validate()
}

// Clone returns independently owned parts and provenance.
func (v AttributedInput) Clone() AttributedInput {
	return attributedInputFromFormat(v.toFormat().Clone())
}

// Equal compares exact fields, treating nil and empty slices alike.
func (v AttributedInput) Equal(other AttributedInput) bool {
	return v.toFormat().Equal(other.toFormat())
}
func (v AttributedInput) toFormat() sessionformat.AttributedInput {
	return sessionformat.AttributedInput{Parts: partsToFormat(v.Parts), Actor: string(v.Actor), Purpose: string(v.Purpose), Source: v.Source.toFormat()}
}
func attributedInputFromFormat(v sessionformat.AttributedInput) AttributedInput {
	return AttributedInput{Parts: partsFromFormat(v.Parts), Actor: Actor(v.Actor), Purpose: PromptPurpose(v.Purpose), Source: contentSourceFromFormat(v.Source)}
}
func (v AttributedInput) checkSize(b *valueBudget) {
	checkPartsSize(v.Parts, b)
	b.text(string(v.Actor))
	b.text(string(v.Purpose))
	v.Source.checkSize(b)
}

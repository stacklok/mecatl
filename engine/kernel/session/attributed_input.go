package session

import (
	"iter"

	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// Actor is a nonempty UTF-8 logical-origin label, preserved exactly. It is not
// a provider role, identity registry, authentication, permission or trust claim.
type Actor string

// PromptPurpose is semantic intent, independent of origin, provider role or trust.
type PromptPurpose string

const (
	// PromptInstructions declares supplemental instruction content.
	PromptInstructions PromptPurpose = sessionformat.PromptInstructions
	// PromptContext declares supplemental contextual data.
	PromptContext PromptPurpose = sessionformat.PromptContext
)

// AttributedInput retains one inline contribution and its declared origin.
// Empty Parts is valid and contributes no content. Assignment shares Parts and
// Source storage; Clone detaches it. Item and actor boundaries are significant.
type AttributedInput struct {
	// Parts is the ordered contributed content; assignment borrows nested storage.
	Parts []ContentPart
	// Actor is the required declared logical origin.
	Actor Actor
	// Purpose is the required declared semantic intent.
	Purpose PromptPurpose
	// Source is the required provenance for this contribution.
	Source ContentSource
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
	return sessionformat.AttributedInput{Parts: v.Parts, Actor: string(v.Actor), Purpose: string(v.Purpose), Source: v.Source.toFormat()}
}
func attributedInputFromFormat(v sessionformat.AttributedInput) AttributedInput {
	return AttributedInput{Parts: v.Parts, Actor: Actor(v.Actor), Purpose: PromptPurpose(v.Purpose), Source: contentSourceFromFormat(v.Source)}
}

// AttributedInputView retains immutable backing storage. Capture and navigation
// cost O(1); zero has no parts, empty labels and a zero source.
type AttributedInputView struct {
	value *sessionformat.AttributedInput
}

// PartCount returns the number of ordered parts, or zero for zero.
func (v AttributedInputView) PartCount() int {
	if v.value == nil {
		return 0
	}
	return len(v.value.Parts)
}

// Parts returns a repeatable sequence of immutable parts in contribution order.
// Zero yields nothing. The sequence stays pinned to this view's accepted storage.
func (v AttributedInputView) Parts() iter.Seq[ContentPartView] {
	return func(yield func(ContentPartView) bool) {
		for i := 0; i < v.PartCount(); i++ {
			if !yield(v.PartAt(i)) {
				return
			}
		}
	}
}

// PartAt returns an immutable part. It panics outside [0, PartCount()).
func (v AttributedInputView) PartAt(i int) ContentPartView {
	return ContentPartView{value: &v.value.Parts[i]}
}

// Actor returns the exact origin label, or empty for zero.
func (v AttributedInputView) Actor() Actor {
	if v.value == nil {
		return ""
	}
	return Actor(v.value.Actor)
}

// Purpose returns the declared intent, or empty for zero.
func (v AttributedInputView) Purpose() PromptPurpose {
	if v.value == nil {
		return ""
	}
	return PromptPurpose(v.value.Purpose)
}

// Source returns the required provenance, or a zero view for zero.
func (v AttributedInputView) Source() ContentSourceView {
	if v.value == nil {
		return ContentSourceView{}
	}
	return ContentSourceView{value: &v.value.Source}
}

func (v AttributedInput) checkSize(b *valueBudget) {
	checkPartsSize(v.Parts, b)
	b.text(string(v.Actor))
	b.text(string(v.Purpose))
	v.Source.checkSize(b)
}

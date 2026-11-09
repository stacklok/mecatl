package session

import (
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// Actor is an open logical-origin label, not authentication or trust.
type Actor = sessionformat.Actor

// PromptPurpose is closed semantic intent, independent of origin or trust.
type PromptPurpose = sessionformat.PromptPurpose

const (
	// PromptInstructions declares supplemental instruction content.
	PromptInstructions PromptPurpose = sessionformat.PromptInstructions
	// PromptContext declares supplemental contextual data.
	PromptContext PromptPurpose = sessionformat.PromptContext
)

// AttributedInput retains ordered content, declared origin, purpose and provenance.
// Assignment shares nested storage; Clone detaches it. Zero is invalid.
type AttributedInput = sessionformat.AttributedInput

// AttributedInputView retains immutable backing storage. Capture and navigation
// cost O(1); zero has no parts, empty labels and a zero source.
//
//dtogen:include
type AttributedInputView struct {
	value *sessionformat.AttributedInput
}

func checkAttributedInputSize(v AttributedInput, b *valueBudget) {
	checkPartsSize(v.Parts, b)
	b.text(string(v.Actor))
	b.text(string(v.Purpose))
	checkContentSourceSize(v.Source, b)
}

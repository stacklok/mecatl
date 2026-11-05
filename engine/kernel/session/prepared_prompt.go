package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// PromptPurpose is semantic intent, independent of origin, provider role or trust.
type PromptPurpose string

const (
	// PromptInstructions declares supplemental instruction content.
	PromptInstructions PromptPurpose = "instructions"
	// PromptContext declares supplemental contextual data.
	PromptContext PromptPurpose = "context"
)

// PreparedPrompt selects exactly one origin by nonnil pointer presence.
// AuthoredMessage indexes a whole unchanged source message. Assignment shares
// pointers and nested content; Clone detaches them. Zero is invalid.
type PreparedPrompt struct {
	AuthoredMessage *uint64
	Harness         *HarnessPrompt
}

// HarnessPrompt retains rewritten or supplemental content and declared provenance,
// never an exact-user claim. Assignment shares nested storage; Clone detaches it.
type HarnessPrompt struct {
	Purpose PromptPurpose
	Content Content
	Source  ContentSource
}

// Validate checks origin and nested content, not source-message membership/bounds.
func (v PreparedPrompt) Validate() error {
	if err := v.toFormat().Validate(); err != nil {
		return err
	}
	if v.Harness != nil {
		return v.Harness.Validate()
	}
	return nil
}

// Clone returns a deeply detached prompt.
func (v PreparedPrompt) Clone() PreparedPrompt { return preparedPromptFromFormat(v.toFormat().Clone()) }

// Equal compares exact values and pointer presence.
func (v PreparedPrompt) Equal(other PreparedPrompt) bool { return v.toFormat().Equal(other.toFormat()) }
func (v PreparedPrompt) toFormat() sessionformat.PreparedPrompt {
	out := sessionformat.PreparedPrompt{AuthoredMessage: v.AuthoredMessage}
	if v.Harness != nil {
		h := v.Harness.toFormat()
		out.Harness = &h
	}
	return out
}
func preparedPromptFromFormat(v sessionformat.PreparedPrompt) PreparedPrompt {
	out := PreparedPrompt{AuthoredMessage: v.AuthoredMessage}
	if v.Harness != nil {
		h := harnessPromptFromFormat(*v.Harness)
		out.Harness = &h
	}
	return out
}
func (v PreparedPrompt) checkSize(b *valueBudget) {
	if v.AuthoredMessage != nil {
		b.pointer()
	}
	if v.Harness != nil {
		b.pointer()
		b.text(string(v.Harness.Purpose))
		v.Harness.Content.checkSize(b)
		v.Harness.Source.checkSize(b)
	}
}

// Validate checks purpose, content, and provenance without resource limits.
func (v HarnessPrompt) Validate() error {
	if err := v.toFormat().Validate(); err != nil {
		return err
	}
	if err := v.Content.Validate(); err != nil {
		return err
	}
	return v.Source.Validate()
}

// Clone detaches content and source pointers.
func (v HarnessPrompt) Clone() HarnessPrompt { return harnessPromptFromFormat(v.toFormat().Clone()) }

// Equal compares exact retained facts.
func (v HarnessPrompt) Equal(other HarnessPrompt) bool { return v.toFormat().Equal(other.toFormat()) }
func (v HarnessPrompt) toFormat() sessionformat.HarnessPrompt {
	return sessionformat.HarnessPrompt{Purpose: string(v.Purpose), Content: v.Content.toFormat(), Source: v.Source.toFormat()}
}
func harnessPromptFromFormat(v sessionformat.HarnessPrompt) HarnessPrompt {
	return HarnessPrompt{Purpose: PromptPurpose(v.Purpose), Content: contentFromFormat(v.Content), Source: contentSourceFromFormat(v.Source)}
}

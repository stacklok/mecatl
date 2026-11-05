package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// InputItem selects exactly one whole user submission or inline contribution.
// References inherit original user authorship without an actor override.
// Assignment shares pointers and children; Clone detaches them. Zero is invalid.
type InputItem struct {
	UserInput *InputID
	Inline    *AttributedInput
}

// Validate checks variant and full reference identity, not aggregate membership.
func (v InputItem) Validate() error {
	if (v.UserInput == nil) == (v.Inline == nil) {
		return codec.ErrInvalidData
	}
	if v.UserInput != nil {
		return v.UserInput.Validate()
	}
	return v.Inline.Validate()
}

// Clone returns a deeply detached item, preserving full reference scope.
func (v InputItem) Clone() InputItem {
	out := v
	if v.UserInput != nil {
		id := *v.UserInput
		out.UserInput = &id
	}
	if v.Inline != nil {
		in := v.Inline.Clone()
		out.Inline = &in
	}
	return out
}

// Equal compares exact fields, reference scope and variant presence.
func (v InputItem) Equal(other InputItem) bool {
	if (v.UserInput == nil) != (other.UserInput == nil) || (v.Inline == nil) != (other.Inline == nil) {
		return false
	}
	if v.UserInput != nil && *v.UserInput != *other.UserInput {
		return false
	}
	return v.Inline == nil || v.Inline.Equal(*other.Inline)
}

// toFormat omits reference scope; the enclosing EffectiveInput must check it.
func (v InputItem) toFormat() sessionformat.InputItem {
	out := sessionformat.InputItem{}
	if v.UserInput != nil {
		out.UserInput = new(v.UserInput.Ordinal)
	}
	if v.Inline != nil {
		in := v.Inline.toFormat()
		out.Inline = &in
	}
	return out
}

// inputItemFromFormat restores full reference scope from the enclosing run.
// Inline bytes and provenance are borrowed; exported callers supply a cloned DTO.
func inputItemFromFormat(run RunID, v sessionformat.InputItem) InputItem {
	out := InputItem{}
	if v.UserInput != nil {
		out.UserInput = &InputID{RunID: run, Ordinal: *v.UserInput}
	}
	if v.Inline != nil {
		in := attributedInputFromFormat(*v.Inline)
		out.Inline = &in
	}
	return out
}
func (v InputItem) checkSize(b *valueBudget) {
	if v.UserInput != nil {
		b.pointer()
	}
	if v.Inline != nil {
		b.pointer()
		v.Inline.checkSize(b)
	}
}

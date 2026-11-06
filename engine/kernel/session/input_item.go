package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// InputItem selects exactly one whole user submission or inline contribution.
// References inherit original user authorship without an actor override.
// Assignment shares pointers and children; Clone detaches them. Zero is invalid.
type InputItem struct {
	// UserInput is the whole-submission reference branch.
	UserInput *UserInputID
	// Inline is the attributed inline-contribution branch.
	Inline *AttributedInput
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

// InputItemView retains immutable backing storage and its enclosing run scope.
// Capture and navigation cost O(1); neither branch is present on zero.
type InputItemView struct {
	run   RunID
	value *sessionformat.InputItem
}

// UserInput returns the whole-submission reference with its full enclosing scope.
// The inline branch and zero return zero,false.
func (v InputItemView) UserInput() (UserInputID, bool) {
	if v.value == nil || v.value.UserInput == nil {
		return UserInputID{}, false
	}
	return UserInputID{RunID: v.run, Ordinal: *v.value.UserInput}, true
}

// Inline returns the inline contribution; the reference branch and zero return zero,false.
func (v InputItemView) Inline() (AttributedInputView, bool) {
	if v.value == nil || v.value.Inline == nil {
		return AttributedInputView{}, false
	}
	return AttributedInputView{value: v.value.Inline}, true
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

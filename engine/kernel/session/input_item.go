package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// InputItem selects exactly one whole user submission or inline contribution.
// References inherit original user authorship without an actor override.
// Assignment shares pointers and children; Clone detaches them. Zero is invalid.
type InputItem struct {
	// UserInputID is the whole-submission reference branch.
	UserInputID *UserInputID
	// Inline is the attributed inline-contribution branch.
	Inline *AttributedInput
}

// Validate checks variant and full reference identity, not aggregate membership.
func (v InputItem) Validate() error {
	if (v.UserInputID == nil) == (v.Inline == nil) {
		return codec.ErrInvalidData
	}
	if v.UserInputID != nil {
		return v.UserInputID.Validate()
	}
	return v.Inline.Validate()
}

// Clone returns a deeply detached item, preserving full reference scope.
func (v InputItem) Clone() InputItem {
	out := v
	if v.UserInputID != nil {
		id := *v.UserInputID
		out.UserInputID = &id
	}
	if v.Inline != nil {
		in := v.Inline.Clone()
		out.Inline = &in
	}
	return out
}

// Equal compares exact fields, reference scope and variant presence.
func (v InputItem) Equal(other InputItem) bool {
	if (v.UserInputID == nil) != (other.UserInputID == nil) || (v.Inline == nil) != (other.Inline == nil) {
		return false
	}
	if v.UserInputID != nil && *v.UserInputID != *other.UserInputID {
		return false
	}
	return v.Inline == nil || v.Inline.Equal(*other.Inline)
}

// toFormat omits reference scope; the enclosing EffectiveInput must check it.
func (v InputItem) toFormat() sessionformat.InputItem {
	out := sessionformat.InputItem{}
	if v.UserInputID != nil {
		out.UserInputID = new(v.UserInputID.ordinal)
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

// UserInputID returns the whole-submission reference with its full enclosing scope.
// The inline branch and zero return zero,false.
func (v InputItemView) UserInputID() (UserInputID, bool) {
	if v.value == nil || v.value.UserInputID == nil {
		return UserInputID{}, false
	}
	return UserInputID{runID: v.run, ordinal: *v.value.UserInputID}, true
}

// Inline returns the inline contribution; the reference branch and zero return zero,false.
func (v InputItemView) Inline() (AttributedInputView, bool) {
	if v.value == nil || v.value.Inline == nil {
		return AttributedInputView{}, false
	}
	return AttributedInputView{value: v.value.Inline}, true
}

func (v InputItem) checkSize(b *valueBudget) {
	if v.UserInputID != nil {
		b.pointer()
	}
	if v.Inline != nil {
		b.pointer()
		v.Inline.checkSize(b)
	}
}

package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// InputItem selects exactly one whole user submission or inline contribution.
// UserInputID is a destination-run-relative component, without origin provenance.
// Assignment shares pointers and children; Clone detaches them. Zero is invalid.
type InputItem = sessionformat.InputItem

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

func checkInputItemSize(v InputItem, b *valueBudget) {
	if v.UserInputID != nil {
		b.pointer()
	}
	if v.Inline != nil {
		b.pointer()
		checkAttributedInputSize(*v.Inline, b)
	}
}

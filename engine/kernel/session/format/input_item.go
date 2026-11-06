package format

import "errors"

// InputItem selects a whole user submission or inline attributed content.
// UserInput is a positive ordinal in the enclosing EffectiveInput's run. Assignment
// shares pointers and children; Clone detaches them.
//
//cborgen:validate Validate
type InputItem struct {
	// UserInput optionally references a whole submission by its required positive
	// ordinal. The enclosing EffectiveInput supplies run scope, and its history
	// envelope supplies history scope; conversion restores both. Inline must be nil.
	// Validate checks positivity/exclusivity, not earlier same-run membership.
	UserInput *uint64 `cbor:"u,omitempty"`
	// Inline optionally retains attributed content, including an empty contribution.
	// UserInput must be nil. Codec traversal validates a present child; neither
	// branch may be absent together, and both may not be present together.
	Inline *AttributedInput `cbor:"i,omitempty"`
}

// Validate checks variant exclusivity and the local reference, not membership.
func (v InputItem) Validate() error {
	if (v.UserInput == nil) == (v.Inline == nil) {
		return errors.New("input item requires exactly one origin")
	}
	if v.UserInput != nil && *v.UserInput == 0 {
		return errors.New("input reference must be positive")
	}
	return nil
}

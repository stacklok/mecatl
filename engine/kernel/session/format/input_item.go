package format

import "errors"

// InputItem selects a whole user submission or inline attributed content.
// UserInput is a positive ordinal in the enclosing batch's run. Assignment
// shares pointers and children; Clone detaches them.
//
//cborgen:validate Validate
type InputItem struct {
	UserInput *uint64          `cbor:"u,omitempty"`
	Inline    *AttributedInput `cbor:"i,omitempty"`
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

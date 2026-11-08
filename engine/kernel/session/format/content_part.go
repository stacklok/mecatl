package format

import (
	"errors"
	"unicode/utf8"
)

// ContentPart requires exactly one text or media branch. A non-nil Text preserves
// empty text; zero is invalid. Assignment shares pointers; Clone detaches them.
//
//dtogen:validate validateLocal
type ContentPart struct {
	// Text is exact UTF-8 text, including explicitly empty text.
	Text *string `cbor:"t,omitempty"`
	// Media is the media branch; codec traversal separately validates the child.
	Media *Media `cbor:"m,omitempty"`
}

// Validate checks branch presence, UTF-8 text and full media validity.
func (v ContentPart) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	if v.Media != nil {
		return v.Media.Validate()
	}
	return nil
}

// validateLocal checks the union after bounded codec traversal validates Media.
func (v ContentPart) validateLocal() error {
	if (v.Text == nil) == (v.Media == nil) {
		return errors.New("content part requires exactly one text or media branch")
	}
	if v.Text != nil && !utf8.ValidString(*v.Text) {
		return errors.New("text must be UTF-8")
	}
	return nil
}

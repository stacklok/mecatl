package format

import (
	"errors"
	"unicode/utf8"
)

// ContentPart requires exactly one text or media branch. A non-nil Text preserves
// empty text; zero is invalid. Assignment shares pointers; Clone detaches them.
//
//cborgen:validate Validate
type ContentPart struct {
	// Text is exact UTF-8 text, including explicitly empty text.
	Text *string `cbor:"t,omitempty"`
	// Media is the media branch; codec traversal separately validates the child.
	Media *Media `cbor:"m,omitempty"`
}

// Validate checks branch presence and text validity, not child validity.
func (v ContentPart) Validate() error {
	if (v.Text == nil) == (v.Media == nil) {
		return errors.New("content part requires exactly one text or media branch")
	}
	if v.Text != nil && !utf8.ValidString(*v.Text) {
		return errors.New("text must be UTF-8")
	}
	return nil
}

package format

import (
	"errors"
	"unicode/utf8"
)

// ContentPart selects text or media by Kind. Empty text is valid; the zero part
// is invalid. Assignment shares Media; Clone detaches it.
//
//cborgen:validate Validate
type ContentPart struct {
	Kind  string `cbor:"k"`
	Text  string `cbor:"t"`
	Media *Media `cbor:"m,omitempty"`
}

// Validate checks the variant and immediate metadata, not child validity.
func (v ContentPart) Validate() error {
	if !utf8.ValidString(v.Text) {
		return errors.New("text must be UTF-8")
	}
	switch v.Kind {
	case "text":
		if v.Media != nil {
			return errors.New("text part must not contain media")
		}
	case "image", "audio", "document":
		if v.Media == nil || v.Text != "" {
			return errors.New("media part requires media and no text")
		}
		if v.Kind != "document" && (v.Media.Filename != "" || v.Media.Title != "") {
			return errors.New("filename and title require a document part")
		}
	default:
		return errors.New("unknown content kind")
	}
	return nil
}

package format

import (
	"errors"
	"unicode/utf8"
)

// Closed content kinds select the text or media branch of ContentPart.
const (
	// ContentText selects exact UTF-8 text, including empty text.
	ContentText = "text"
	// ContentImage selects image media.
	ContentImage = "image"
	// ContentAudio selects audio media.
	ContentAudio = "audio"
	// ContentDocument selects document media, permitting Filename and Title.
	ContentDocument = "document"
)

// ContentPart selects text or media by Kind. Empty text is valid; the zero part
// is invalid. Assignment shares Media; Clone detaches it.
//
//cborgen:validate Validate
type ContentPart struct {
	// Kind is required: ContentText, ContentImage, ContentAudio or ContentDocument.
	Kind string `cbor:"k"`
	// Text is exact UTF-8 text for ContentText, including empty text; otherwise empty.
	Text string `cbor:"t"`
	// Media is required for image, audio and document kinds and forbidden for text.
	// Filename and Title are permitted only for documents. Validate checks this
	// applicability; codec traversal separately validates the Media child.
	Media *Media `cbor:"m,omitempty"`
}

// Validate checks the variant and immediate metadata, not child validity.
func (v ContentPart) Validate() error {
	if !utf8.ValidString(v.Text) {
		return errors.New("text must be UTF-8")
	}
	switch v.Kind {
	case ContentText:
		if v.Media != nil {
			return errors.New("text part must not contain media")
		}
	case ContentImage, ContentAudio, ContentDocument:
		if v.Media == nil || v.Text != "" {
			return errors.New("media part requires media and no text")
		}
		if v.Kind != ContentDocument && (v.Media.Filename != "" || v.Media.Title != "") {
			return errors.New("filename and title require a document part")
		}
	default:
		return errors.New("unknown content kind")
	}
	return nil
}

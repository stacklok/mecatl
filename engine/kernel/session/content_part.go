package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// ContentKind is a closed logical variant, not a provider capability.
type ContentKind string

const (
	// ContentText selects exact UTF-8 text.
	ContentText ContentKind = "text"
	// ContentImage selects image media.
	ContentImage ContentKind = "image"
	// ContentAudio selects audio media.
	ContentAudio ContentKind = "audio"
	// ContentDocument selects document media.
	ContentDocument ContentKind = "document"
)

// ContentPart holds text or media as selected by Kind. Zero is invalid.
// Assignment shares Media and its bytes; Clone detaches them.
type ContentPart struct {
	Kind  ContentKind
	Text  string
	Media *Media
}

// Validate checks the variant, metadata applicability and nested source.
func (v ContentPart) Validate() error {
	if err := v.toFormat().Validate(); err != nil {
		return err
	}
	if v.Media != nil {
		return v.Media.Validate()
	}
	return nil
}

// Clone returns a deeply detached part.
func (v ContentPart) Clone() ContentPart { return contentPartFromFormat(v.toFormat().Clone()) }

// Equal compares exact values and pointer presence.
func (v ContentPart) Equal(other ContentPart) bool { return v.toFormat().Equal(other.toFormat()) }
func (v ContentPart) toFormat() sessionformat.ContentPart {
	out := sessionformat.ContentPart{Kind: string(v.Kind), Text: v.Text}
	if v.Media != nil {
		m := v.Media.toFormat()
		out.Media = &m
	}
	return out
}
func contentPartFromFormat(v sessionformat.ContentPart) ContentPart {
	out := ContentPart{Kind: ContentKind(v.Kind), Text: v.Text}
	if v.Media != nil {
		m := mediaFromFormat(*v.Media)
		out.Media = &m
	}
	return out
}
func (v ContentPart) checkSize(b *valueBudget) {
	b.text(string(v.Kind))
	b.text(v.Text)
	if v.Media != nil {
		b.pointer()
		v.Media.checkSize(b)
	}
}

// validateParts checks ordered content without imposing operational policy.
func validateParts(v []ContentPart) error {
	for _, p := range v {
		if err := p.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// partsToFormat allocates DTO part/media containers but borrows media bytes.
// Record construction must size first, then encode or clone before retention.
func partsToFormat(v []ContentPart) []sessionformat.ContentPart {
	if v == nil {
		return nil
	}
	out := make([]sessionformat.ContentPart, len(v))
	for i := range v {
		out[i] = v[i].toFormat()
	}
	return out
}

// partsFromFormat allocates public containers but borrows media bytes from v.
// Exporting callers supply a cloned DTO so returned storage is independently owned.
func partsFromFormat(v []sessionformat.ContentPart) []ContentPart {
	if v == nil {
		return nil
	}
	out := make([]ContentPart, len(v))
	for i := range v {
		out[i] = contentPartFromFormat(v[i])
	}
	return out
}

// checkPartsSize bounds collections and nested data before conversion allocates.
func checkPartsSize(v []ContentPart, b *valueBudget) {
	b.collection(len(v))
	if b.err != nil {
		return
	}
	for _, p := range v {
		p.checkSize(b)
	}
}

package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// ContentPart holds exactly one text or media branch. Zero is invalid.
// Assignment shares Text, Media and its bytes; Clone detaches them.
type ContentPart struct {
	// Text is exact UTF-8 text; a non-nil pointer preserves explicitly empty text.
	Text *string
	// Media is the media branch; assignment borrows it.
	Media *Media
}

// Validate checks branch presence, UTF-8 text and full media validity.
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
	out := sessionformat.ContentPart{Text: v.Text}
	if v.Media != nil {
		m := v.Media.toFormat()
		out.Media = &m
	}
	return out
}
func contentPartFromFormat(v sessionformat.ContentPart) ContentPart {
	out := ContentPart{Text: v.Text}
	if v.Media != nil {
		m := mediaFromFormat(*v.Media)
		out.Media = &m
	}
	return out
}
func (v ContentPart) checkSize(b *valueBudget) {
	if v.Text != nil {
		b.pointer()
		b.text(*v.Text)
	}
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

// partsToFormat allocates DTO part/media containers but borrows text pointers and
// media bytes.
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

// partsFromFormat allocates public containers but borrows text pointers and media
// bytes from v.
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

// ContentPartView retains immutable backing storage. Capture and navigation cost
// O(1); zero has neither union branch present.
type ContentPartView struct{ value *sessionformat.ContentPart }

// Text returns exact text only for the text branch, including empty text.
func (v ContentPartView) Text() (string, bool) {
	if v.value == nil || v.value.Text == nil {
		return "", false
	}
	return *v.value.Text, true
}

// Media returns media only for the media branch; zero and text return zero,false.
func (v ContentPartView) Media() (MediaView, bool) {
	if v.value != nil && v.value.Media != nil {
		return MediaView{value: v.value.Media}, true
	}
	return MediaView{}, false
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

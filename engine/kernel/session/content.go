package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// Content preserves part order. Nil and empty are equivalent explicit emptiness.
// Assignment shares storage; Clone detaches all nested storage.
type Content []ContentPart

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
type ContentPart struct {
	Kind  ContentKind
	Text  string
	Media *MediaContent
}

// Validate checks all parts without operational resource limits.
func (v Content) Validate() error {
	for _, part := range v {
		if err := part.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Clone returns a deeply detached copy, preserving nil slices.
func (v Content) Clone() Content { return contentFromFormat(v.toFormat().Clone()) }

// Equal compares exact ordered values, treating nil and empty slices alike.
func (v Content) Equal(other Content) bool { return v.toFormat().Equal(other.toFormat()) }

func (v Content) toFormat() sessionformat.Content {
	if v == nil {
		return nil
	}
	out := make(sessionformat.Content, len(v))
	for i := range v {
		out[i] = v[i].toFormat()
	}
	return out
}
func contentFromFormat(v sessionformat.Content) Content {
	if v == nil {
		return nil
	}
	out := make(Content, len(v))
	for i := range v {
		out[i] = contentPartFromFormat(v[i])
	}
	return out
}
func (v Content) checkSize(b *valueBudget) {
	b.collection(len(v))
	if b.err != nil {
		return
	}
	for _, part := range v {
		part.checkSize(b)
	}
}

// Validate checks the variant, metadata applicability, and nested source.
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
		m := mediaContentFromFormat(*v.Media)
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

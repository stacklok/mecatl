package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// MediaContent retains declared metadata and exactly one source. Empty metadata
// is unspecified; Filename and Title are document-only. Assignment shares source
// storage; Clone detaches it. Inline sources require MediaType.
type MediaContent struct {
	Source    MediaSource
	MediaType string
	Format    string
	Filename  string
	Title     string
}

// Validate checks metadata and source, without resolving references.
func (v MediaContent) Validate() error {
	if err := v.toFormat().Validate(); err != nil {
		return err
	}
	return v.Source.Validate()
}

// Clone returns independently owned media.
func (v MediaContent) Clone() MediaContent { return mediaContentFromFormat(v.toFormat().Clone()) }

// Equal compares exact metadata, source, and pointer presence.
func (v MediaContent) Equal(other MediaContent) bool { return v.toFormat().Equal(other.toFormat()) }
func (v MediaContent) toFormat() sessionformat.MediaContent {
	return sessionformat.MediaContent{Source: v.Source.toFormat(), MediaType: v.MediaType, Format: v.Format, Filename: v.Filename, Title: v.Title}
}
func mediaContentFromFormat(v sessionformat.MediaContent) MediaContent {
	return MediaContent{Source: mediaSourceFromFormat(v.Source), MediaType: v.MediaType, Format: v.Format, Filename: v.Filename, Title: v.Title}
}
func (v MediaContent) checkSize(b *valueBudget) {
	b.text(v.MediaType)
	b.text(v.Format)
	b.text(v.Filename)
	b.text(v.Title)
	v.Source.checkSize(b)
}

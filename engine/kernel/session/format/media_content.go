package format

import (
	"errors"
	"unicode/utf8"
)

// MediaContent retains exact declared metadata and a source, never resolved data.
// Empty metadata is unspecified; inline sources require MediaType. Assignment
// shares source pointers and bytes; Clone detaches them.
//
//cborgen:validate Validate
type MediaContent struct {
	Source    MediaSource `cbor:"s"`
	MediaType string      `cbor:"m"`
	Format    string      `cbor:"f"`
	Filename  string      `cbor:"n"`
	Title     string      `cbor:"t"`
}

// Validate checks immediate metadata, without validating the source recursively.
func (v MediaContent) Validate() error {
	if !utf8.ValidString(v.MediaType) || !utf8.ValidString(v.Format) ||
		!utf8.ValidString(v.Filename) || !utf8.ValidString(v.Title) {
		return errors.New("media metadata must be UTF-8")
	}
	if v.Source.Inline != nil && v.MediaType == "" {
		return errors.New("inline media requires a media type")
	}
	return nil
}

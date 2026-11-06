package session

import (
	"bytes"

	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// MediaSourceKind selects a closed storage variant, not an access capability.
type MediaSourceKind string

const (
	// MediaInline retains bytes, including explicitly empty bytes.
	MediaInline MediaSourceKind = sessionformat.MediaInline
	// MediaRemote retains an absolute URI without dereferencing it.
	MediaRemote MediaSourceKind = sessionformat.MediaRemote
	// MediaProvider retains a complete logical provider storage binding.
	MediaProvider MediaSourceKind = sessionformat.MediaProvider
)

// Media retains exact metadata and a tagged source. Filename and Title are
// document-only; inline media requires MediaType. Assignment shares Data;
// Clone detaches it. References grant no access or dereference authority.
type Media struct {
	// MediaType is the MIME type; it is required for inline media.
	MediaType string
	// Format is an optional exact media-format hint.
	Format string
	// Filename is an optional document-only filename.
	Filename string
	// Title is an optional document-only title.
	Title string
	// Source selects the required storage variant.
	Source MediaSourceKind
	// Data holds inline bytes only; assignment borrows the slice.
	Data []byte
	// URI is the required absolute locator for remote media.
	URI string
	// Provider is the provider name for provider-backed media.
	Provider string
	// Scope is the required provider-local storage scope.
	Scope string
	// ReferenceID is the required provider-local media reference.
	ReferenceID string
}

// Validate checks metadata and source syntax without resolving references.
func (v Media) Validate() error { return v.toFormat().Validate() }

// Clone returns independently owned media.
func (v Media) Clone() Media { return mediaFromFormat(v.toFormat().Clone()) }

// Equal compares exact fields, treating nil and empty bytes alike.
func (v Media) Equal(other Media) bool { return v.toFormat().Equal(other.toFormat()) }
func (v Media) toFormat() sessionformat.Media {
	return sessionformat.Media{MediaType: v.MediaType, Format: v.Format, Filename: v.Filename, Title: v.Title, Source: string(v.Source), Data: v.Data, URI: v.URI, Provider: v.Provider, Scope: v.Scope, ReferenceID: v.ReferenceID}
}
func mediaFromFormat(v sessionformat.Media) Media {
	return Media{MediaType: v.MediaType, Format: v.Format, Filename: v.Filename, Title: v.Title, Source: MediaSourceKind(v.Source), Data: v.Data, URI: v.URI, Provider: v.Provider, Scope: v.Scope, ReferenceID: v.ReferenceID}
}

// MediaView retains immutable backing storage, including inline bytes. Capture
// and metadata navigation cost O(1). Zero has empty metadata and no source branch.
type MediaView struct{ value *sessionformat.Media }

// MediaType returns the MIME type, or empty for zero.
func (v MediaView) MediaType() string {
	if v.value == nil {
		return ""
	}
	return v.value.MediaType
}

// Format returns the exact format hint, or empty for zero.
func (v MediaView) Format() string {
	if v.value == nil {
		return ""
	}
	return v.value.Format
}

// Filename returns the document filename, or empty for zero.
func (v MediaView) Filename() string {
	if v.value == nil {
		return ""
	}
	return v.value.Filename
}

// Title returns the document title, or empty for zero.
func (v MediaView) Title() string {
	if v.value == nil {
		return ""
	}
	return v.value.Title
}

// Source returns the selected storage variant, or empty for zero.
func (v MediaView) Source() MediaSourceKind {
	if v.value == nil {
		return ""
	}
	return MediaSourceKind(v.value.Source)
}

// Data returns independently owned bytes only for inline media. Empty inline
// data is present. It copies only this field, never sibling metadata or content.
func (v MediaView) Data() ([]byte, bool) {
	if v.Source() != MediaInline {
		return nil, false
	}
	return bytes.Clone(v.value.Data), true
}

// URI returns the retained URI only for remote media; no access is granted.
func (v MediaView) URI() (string, bool) {
	if v.Source() != MediaRemote {
		return "", false
	}
	return v.value.URI, true
}

// Provider returns the logical provider only for provider-backed media.
func (v MediaView) Provider() (string, bool) {
	if v.Source() != MediaProvider {
		return "", false
	}
	return v.value.Provider, true
}

// Scope returns the logical storage scope only for provider-backed media.
func (v MediaView) Scope() (string, bool) {
	if v.Source() != MediaProvider {
		return "", false
	}
	return v.value.Scope, true
}

// ReferenceID returns the retained reference only for provider-backed media.
func (v MediaView) ReferenceID() (string, bool) {
	if v.Source() != MediaProvider {
		return "", false
	}
	return v.value.ReferenceID, true
}

func (v Media) checkSize(b *valueBudget) {
	b.text(v.MediaType)
	b.text(v.Format)
	b.text(v.Filename)
	b.text(v.Title)
	b.text(string(v.Source))
	b.bytes(len(v.Data))
	b.text(v.URI)
	b.text(v.Provider)
	b.text(v.Scope)
	b.text(v.ReferenceID)
}

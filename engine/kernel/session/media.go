package session

import (
	"bytes"

	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// MediaKind is a closed content category, not a provider capability.
type MediaKind string

const (
	// MediaKindImage selects image media.
	MediaKindImage MediaKind = sessionformat.MediaKindImage
	// MediaKindAudio selects audio media.
	MediaKindAudio MediaKind = sessionformat.MediaKindAudio
	// MediaKindDocument selects document media, permitting Filename and Title.
	MediaKindDocument MediaKind = sessionformat.MediaKindDocument
)

// MediaSourceKind selects a closed storage variant, not an access capability.
type MediaSourceKind string

const (
	// MediaSourceInline retains bytes, including explicitly empty bytes.
	MediaSourceInline MediaSourceKind = sessionformat.MediaSourceInline
	// MediaSourceRemote retains an absolute URI without dereferencing it.
	MediaSourceRemote MediaSourceKind = sessionformat.MediaSourceRemote
	// MediaSourceProvider retains a complete logical provider storage binding.
	MediaSourceProvider MediaSourceKind = sessionformat.MediaSourceProvider
)

// Media retains exact metadata and a tagged source. Filename and Title are
// document-only; inline media requires MIMEType. Assignment shares InlineData;
// Clone detaches it. References grant no access or dereference authority.
type Media struct {
	// Kind selects the required media category.
	Kind MediaKind
	// Source selects the required storage variant.
	Source MediaSourceKind

	// InlineData holds inline bytes only; assignment borrows the slice.
	InlineData []byte

	// RemoteURI is the required absolute locator for remote media.
	RemoteURI string

	// ProviderName is the provider name for provider-backed media.
	ProviderName string
	// ProviderScope is the required provider-local storage scope.
	ProviderScope string
	// ProviderReferenceID is the required provider-local media reference.
	ProviderReferenceID string

	// MIMEType is the MIME type; it is required for inline media.
	MIMEType string
	// FormatHint is an optional exact media-format hint.
	FormatHint string
	// Filename is an optional document-only filename.
	Filename string
	// Title is an optional document-only title.
	Title string
}

// Validate checks kind, metadata and source syntax without resolving references.
func (v Media) Validate() error { return v.toFormat().Validate() }

// Clone returns independently owned media.
func (v Media) Clone() Media { return mediaFromFormat(v.toFormat().Clone()) }

// Equal compares exact fields, treating nil and empty bytes alike.
func (v Media) Equal(other Media) bool { return v.toFormat().Equal(other.toFormat()) }
func (v Media) toFormat() sessionformat.Media {
	return sessionformat.Media{Kind: string(v.Kind), Source: string(v.Source), InlineData: v.InlineData, RemoteURI: v.RemoteURI, ProviderName: v.ProviderName, ProviderScope: v.ProviderScope, ProviderReferenceID: v.ProviderReferenceID, MIMEType: v.MIMEType, FormatHint: v.FormatHint, Filename: v.Filename, Title: v.Title}
}
func mediaFromFormat(v sessionformat.Media) Media {
	return Media{Kind: MediaKind(v.Kind), Source: MediaSourceKind(v.Source), InlineData: v.InlineData, RemoteURI: v.RemoteURI, ProviderName: v.ProviderName, ProviderScope: v.ProviderScope, ProviderReferenceID: v.ProviderReferenceID, MIMEType: v.MIMEType, FormatHint: v.FormatHint, Filename: v.Filename, Title: v.Title}
}

// MediaView retains immutable backing storage, including inline bytes. Capture
// and metadata navigation cost O(1). Zero has empty metadata and no source branch.
type MediaView struct{ value *sessionformat.Media }

// Kind returns the media category, or empty for zero.
func (v MediaView) Kind() MediaKind {
	if v.value == nil {
		return ""
	}
	return MediaKind(v.value.Kind)
}

// MIMEType returns the MIME type, or empty for zero.
func (v MediaView) MIMEType() string {
	if v.value == nil {
		return ""
	}
	return v.value.MIMEType
}

// FormatHint returns the exact format hint, or empty for zero.
func (v MediaView) FormatHint() string {
	if v.value == nil {
		return ""
	}
	return v.value.FormatHint
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

// InlineData returns independently owned bytes only for inline media. Empty inline
// data is present. It copies only this field, never sibling metadata or content.
func (v MediaView) InlineData() ([]byte, bool) {
	if v.Source() != MediaSourceInline {
		return nil, false
	}
	return bytes.Clone(v.value.InlineData), true
}

// RemoteURI returns the retained URI only for remote media; no access is granted.
func (v MediaView) RemoteURI() (string, bool) {
	if v.Source() != MediaSourceRemote {
		return "", false
	}
	return v.value.RemoteURI, true
}

// ProviderName returns the logical provider only for provider-backed media.
func (v MediaView) ProviderName() (string, bool) {
	if v.Source() != MediaSourceProvider {
		return "", false
	}
	return v.value.ProviderName, true
}

// ProviderScope returns the logical storage scope only for provider-backed media.
func (v MediaView) ProviderScope() (string, bool) {
	if v.Source() != MediaSourceProvider {
		return "", false
	}
	return v.value.ProviderScope, true
}

// ProviderReferenceID returns the retained reference only for provider-backed media.
func (v MediaView) ProviderReferenceID() (string, bool) {
	if v.Source() != MediaSourceProvider {
		return "", false
	}
	return v.value.ProviderReferenceID, true
}

func (v Media) checkSize(b *valueBudget) {
	b.text(string(v.Kind))
	b.text(string(v.Source))
	b.bytes(len(v.InlineData))
	b.text(v.RemoteURI)
	b.text(v.ProviderName)
	b.text(v.ProviderScope)
	b.text(v.ProviderReferenceID)
	b.text(v.MIMEType)
	b.text(v.FormatHint)
	b.text(v.Filename)
	b.text(v.Title)
}

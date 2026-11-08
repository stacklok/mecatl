package format

import (
	"errors"
	"net/url"
	"unicode/utf8"
)

// MediaKind is a closed content category, not a provider capability.
type MediaKind string

// Closed media kinds classify content, not provider capabilities.
const (
	// MediaKindImage selects image media.
	MediaKindImage MediaKind = "image"
	// MediaKindAudio selects audio media.
	MediaKindAudio MediaKind = "audio"
	// MediaKindDocument selects document media, permitting Filename and Title.
	MediaKindDocument MediaKind = "document"
)

// MediaSourceKind selects a closed storage variant, not an access capability.
type MediaSourceKind string

// Closed media sources select the retained storage binding, not access rights.
const (
	// MediaSourceInline selects InlineData, including empty bytes, with a required MIME type.
	MediaSourceInline MediaSourceKind = "inline"
	// MediaSourceRemote selects an absolute URI without dereferencing it.
	MediaSourceRemote MediaSourceKind = "remote"
	// MediaSourceProvider selects a complete provider name, scope and reference binding.
	MediaSourceProvider MediaSourceKind = "provider"
)

// Media retains exact UTF-8 metadata and a tagged source. Assignment shares
// InlineData; Clone detaches it. An inline tag preserves presence with empty bytes.
// References grant no access or dereference authority.
//
//dtogen:validate Validate
type Media struct {
	// Kind is required: MediaKindImage, MediaKindAudio or MediaKindDocument.
	Kind MediaKind `cbor:"k"`
	// Source selects the required storage variant and its exclusive fields.
	Source MediaSourceKind `cbor:"s"`

	// InlineData retains inline bytes only; Source, not nilness, records presence.
	InlineData []byte `cbor:"d" view:"manual"`

	// RemoteURI is a required absolute locator for remote media, empty otherwise.
	RemoteURI string `cbor:"u" view:"manual"`

	// ProviderName is the required logical provider for provider media, empty otherwise.
	ProviderName string `cbor:"p" view:"manual"`
	// ProviderScope is the required provider storage namespace, empty otherwise.
	// It is not the session's history or run scope.
	ProviderScope string `cbor:"o" view:"manual"`
	// ProviderReferenceID is required within the provider scope, empty otherwise.
	ProviderReferenceID string `cbor:"r" view:"manual"`

	// MIMEType is required for inline media, optional for remote/provider media.
	MIMEType string `cbor:"m"`
	// FormatHint is an optional exact format hint for any source.
	FormatHint string `cbor:"f"`
	// Filename is an optional document-only filename label.
	Filename string `cbor:"n"`
	// Title is an optional document-only title.
	Title string `cbor:"t"`
}

// Validate checks kind, UTF-8 metadata, applicability and source binding syntax.
func (v Media) Validate() error {
	switch v.Kind {
	case MediaKindImage, MediaKindAudio, MediaKindDocument:
	default:
		return errors.New("unknown media kind")
	}
	if v.Kind != MediaKindDocument && (v.Filename != "" || v.Title != "") {
		return errors.New("filename and title require document media")
	}
	for _, s := range []string{v.MIMEType, v.FormatHint, v.Filename, v.Title, v.RemoteURI, v.ProviderName, v.ProviderScope, v.ProviderReferenceID} {
		if !utf8.ValidString(s) {
			return errors.New("media fields must be UTF-8")
		}
	}
	return v.validateSource()
}

// validateSource enforces the selected source's fields without resolving it.
func (v Media) validateSource() error {
	switch v.Source {
	case MediaSourceInline:
		if v.MIMEType == "" || v.RemoteURI != "" || v.ProviderName != "" || v.ProviderScope != "" || v.ProviderReferenceID != "" {
			return errors.New("invalid inline media fields")
		}
	case MediaSourceRemote:
		if len(v.InlineData) != 0 || v.ProviderName != "" || v.ProviderScope != "" || v.ProviderReferenceID != "" {
			return errors.New("invalid remote media fields")
		}
		u, err := url.Parse(v.RemoteURI)
		if err != nil || !u.IsAbs() {
			return errors.New("media URI must be absolute")
		}
	case MediaSourceProvider:
		if len(v.InlineData) != 0 || v.RemoteURI != "" || v.ProviderName == "" || v.ProviderScope == "" || v.ProviderReferenceID == "" {
			return errors.New("invalid provider media fields")
		}
	default:
		return errors.New("unknown media source")
	}
	return nil
}

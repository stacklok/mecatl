package format

import (
	"errors"
	"net/url"
	"unicode/utf8"
)

// Closed media sources select the retained storage binding, not access rights.
const (
	// MediaInline selects Data, including empty bytes, with a required MIME type.
	MediaInline = "inline"
	// MediaRemote selects an absolute URI without dereferencing it.
	MediaRemote = "remote"
	// MediaProvider selects a complete Provider, Scope and ReferenceID binding.
	MediaProvider = "provider"
)

// Media retains exact UTF-8 metadata and a tagged source. Assignment shares Data;
// Clone detaches it. An inline tag preserves presence even with empty bytes.
// References grant no access or dereference authority.
//
//cborgen:validate Validate
type Media struct {
	// MediaType is the MIME type: required for inline, optional for remote/provider.
	MediaType string `cbor:"m"`
	// Format is an optional exact format hint for any source, not a decoded format.
	Format string `cbor:"f"`
	// Filename is an optional document filename label, forbidden on image/audio parts.
	Filename string `cbor:"n"`
	// Title is an optional document title, forbidden on image/audio parts.
	Title string `cbor:"t"`
	// Source is required: MediaInline, MediaRemote or MediaProvider. Validate
	// enforces source exclusivity; the enclosing ContentPart checks document metadata.
	Source string `cbor:"s"`
	// Data retains inline bytes, including empty bytes. It must be empty for other
	// sources; Source, not byte length or nilness, records inline presence.
	Data []byte `cbor:"d"`
	// URI is a required absolute URI for remote media and empty for other sources.
	// It is retained without resolution and grants no access.
	URI string `cbor:"u"`
	// Provider is the required logical storage provider for provider media and empty
	// otherwise. Together with Scope and ReferenceID it forms a complete binding.
	Provider string `cbor:"p"`
	// Scope is the required provider storage namespace for provider media and empty
	// otherwise; it is not the session's history or run scope.
	Scope string `cbor:"o"`
	// ReferenceID is required within Provider and Scope for provider media and empty
	// otherwise. This retained reference grants no access or dereference authority.
	ReferenceID string `cbor:"r"`
}

// Validate checks exact UTF-8 metadata, source exclusivity and binding syntax.
func (v Media) Validate() error {
	for _, s := range []string{v.MediaType, v.Format, v.Filename, v.Title, v.URI, v.Provider, v.Scope, v.ReferenceID} {
		if !utf8.ValidString(s) {
			return errors.New("media fields must be UTF-8")
		}
	}
	return v.validateSource()
}

// validateSource enforces the selected source's fields without resolving it.
func (v Media) validateSource() error {
	switch v.Source {
	case MediaInline:
		if v.MediaType == "" || v.URI != "" || v.Provider != "" || v.Scope != "" || v.ReferenceID != "" {
			return errors.New("invalid inline media fields")
		}
	case MediaRemote:
		if len(v.Data) != 0 || v.Provider != "" || v.Scope != "" || v.ReferenceID != "" {
			return errors.New("invalid remote media fields")
		}
		u, err := url.Parse(v.URI)
		if err != nil || !u.IsAbs() {
			return errors.New("media URI must be absolute")
		}
	case MediaProvider:
		if len(v.Data) != 0 || v.URI != "" || v.Provider == "" || v.Scope == "" || v.ReferenceID == "" {
			return errors.New("invalid provider media fields")
		}
	default:
		return errors.New("unknown media source")
	}
	return nil
}

package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// MediaSourceKind selects a closed storage variant, not an access capability.
type MediaSourceKind string

const (
	// MediaInline retains bytes, including explicitly empty bytes.
	MediaInline MediaSourceKind = "inline"
	// MediaRemote retains an absolute URI without dereferencing it.
	MediaRemote MediaSourceKind = "remote"
	// MediaProvider retains a complete logical provider storage binding.
	MediaProvider MediaSourceKind = "provider"
)

// Media retains exact metadata and a tagged source. Filename and Title are
// document-only; inline media requires MediaType. Assignment shares Data;
// Clone detaches it. References grant no access or dereference authority.
type Media struct {
	MediaType   string
	Format      string
	Filename    string
	Title       string
	Source      MediaSourceKind
	Data        []byte
	URI         string
	Provider    string
	Scope       string
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

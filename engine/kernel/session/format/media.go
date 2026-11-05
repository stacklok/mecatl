package format

import (
	"errors"
	"net/url"
	"unicode/utf8"
)

// Media retains exact metadata and a tagged source. Assignment shares Data;
// Clone detaches it. An inline tag preserves presence even with empty bytes.
// References grant no access or dereference authority.
//
//cborgen:validate Validate
type Media struct {
	MediaType   string `cbor:"m"`
	Format      string `cbor:"f"`
	Filename    string `cbor:"n"`
	Title       string `cbor:"t"`
	Source      string `cbor:"s"`
	Data        []byte `cbor:"d"`
	URI         string `cbor:"u"`
	Provider    string `cbor:"p"`
	Scope       string `cbor:"o"`
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
	case "inline":
		if v.MediaType == "" || v.URI != "" || v.Provider != "" || v.Scope != "" || v.ReferenceID != "" {
			return errors.New("invalid inline media fields")
		}
	case "remote":
		if len(v.Data) != 0 || v.Provider != "" || v.Scope != "" || v.ReferenceID != "" {
			return errors.New("invalid remote media fields")
		}
		u, err := url.Parse(v.URI)
		if err != nil || !u.IsAbs() {
			return errors.New("media URI must be absolute")
		}
	case "provider":
		if len(v.Data) != 0 || v.URI != "" || v.Provider == "" || v.Scope == "" || v.ReferenceID == "" {
			return errors.New("invalid provider media fields")
		}
	default:
		return errors.New("unknown media source")
	}
	return nil
}

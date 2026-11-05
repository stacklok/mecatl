package format

import (
	"errors"
	"net/url"
	"unicode/utf8"
)

// MediaSource selects exactly one source by pointer presence, including explicitly
// empty inline bytes. Zero is invalid. Assignment shares pointers and bytes;
// Clone detaches them. References grant no dereference or authorization rights.
//
//cborgen:validate Validate
type MediaSource struct {
	Inline   *InlineMedia   `cbor:"i,omitempty"`
	Remote   *RemoteMedia   `cbor:"r,omitempty"`
	Provider *ProviderMedia `cbor:"p,omitempty"`
}

// Validate checks pointer exclusivity, not child validity.
func (v MediaSource) Validate() error {
	n := 0
	if v.Inline != nil {
		n++
	}
	if v.Remote != nil {
		n++
	}
	if v.Provider != nil {
		n++
	}
	if n != 1 {
		return errors.New("media requires exactly one source")
	}
	return nil
}

// InlineMedia retains raw bytes, including explicitly empty bytes. Assignment
// shares Data; Clone detaches it. Nil and empty Data are equivalent.
type InlineMedia struct {
	Data []byte `cbor:"d"`
}

// RemoteMedia retains an exact absolute URI, not permission to fetch it.
//
//cborgen:validate Validate
type RemoteMedia struct {
	URI string `cbor:"u"`
}

// Validate checks UTF-8 and absolute URI syntax without I/O or normalization.
func (v RemoteMedia) Validate() error {
	if !utf8.ValidString(v.URI) {
		return errors.New("media URI must be UTF-8")
	}
	u, err := url.Parse(v.URI)
	if err != nil || !u.IsAbs() {
		return errors.New("media URI must be absolute")
	}
	return nil
}

// ProviderMedia retains an exact reference and caller-declared logical provider
// and storage scope. These strings are neither credentials nor proof of access.
//
//cborgen:validate Validate
type ProviderMedia struct {
	Provider string `cbor:"p"`
	Scope    string `cbor:"s"`
	ID       string `cbor:"i"`
}

// Validate checks nonempty UTF-8 binding fields, without provider semantics.
func (v ProviderMedia) Validate() error {
	if v.Provider == "" || v.Scope == "" || v.ID == "" ||
		!utf8.ValidString(v.Provider) || !utf8.ValidString(v.Scope) || !utf8.ValidString(v.ID) {
		return errors.New("provider media requires nonempty UTF-8 binding fields")
	}
	return nil
}

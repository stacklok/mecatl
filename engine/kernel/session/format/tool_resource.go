package format

import (
	"errors"
	"net/url"
	"unicode/utf8"
)

// ToolResource describes a reference, granting no fetch, dereference or authority.
// Optional labels are independent, nil when absent and preserved when present-empty.
// Assignment shares pointers; Clone detaches them. Zero is invalid.
//
//dtogen:validate validateLocal
type ToolResource struct {
	// URI is a required exact absolute UTF-8 URI, at most 16384 bytes.
	URI string `cbor:"u" cborlimit:"bytes=16384"`
	// Name is an optional UTF-8 name, at most 1024 bytes.
	Name *string `cbor:"n,omitempty" cborlimit:"bytes=1024"`
	// Title is an optional UTF-8 title, at most 1024 bytes.
	Title *string `cbor:"t,omitempty" cborlimit:"bytes=1024"`
	// MIMEType is an optional UTF-8 producer label, at most 1024 bytes;
	// validation does not interpret MIME syntax.
	MIMEType *string `cbor:"m,omitempty" cborlimit:"bytes=1024"`
}

// Validate checks bounded UTF-8 metadata and absolute URI syntax, without I/O.
func (v ToolResource) Validate() error { return v.validateLocal() }

// validateLocal bounds labels before scans and URI parsing.
func (v ToolResource) validateLocal() error {
	if len(v.URI) == 0 || len(v.URI) > 16384 || !utf8.ValidString(v.URI) {
		return errors.New("tool resource requires bounded UTF-8 URI")
	}
	for _, s := range []*string{v.Name, v.Title, v.MIMEType} {
		if s != nil && (len(*s) > 1024 || !utf8.ValidString(*s)) {
			return errors.New("tool resource requires bounded UTF-8 labels")
		}
	}
	u, err := url.Parse(v.URI)
	if err != nil || !u.IsAbs() {
		return errors.New("tool resource URI must be absolute")
	}
	return nil
}

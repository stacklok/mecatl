package format

import (
	"errors"
	"io/fs"
	"unicode/utf8"
)

// ContentSource retains a nonempty explanation and optional contributing file
// label. Description is not identity. Upstream owns trust and framing; this
// value does not certify trust or exact copying. Assignment shares the file
// pointer; Clone detaches it. Zero is invalid.
//
//dtogen:validate validateLocal
type ContentSource struct {
	// Description is a required nonempty exact UTF-8 provenance explanation,
	// checked locally by Validate. It is not identity, a trust assertion, or an
	// exact-copy assertion.
	Description string `cbor:"d"`
	// WorkspaceFile optionally names a contributing workspace-relative file. Nil
	// means no file label; codec traversal validates a present child. It grants no
	// file access and Load never re-reads it.
	WorkspaceFile *WorkspaceFileSource `cbor:"w,omitempty"`
}

// Validate checks provenance and its optional file label, not authenticity.
func (v ContentSource) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	if v.WorkspaceFile != nil {
		return v.WorkspaceFile.Validate()
	}
	return nil
}

// validateLocal checks the explanation after bounded traversal validates children.
func (v ContentSource) validateLocal() error {
	if v.Description == "" || !utf8.ValidString(v.Description) {
		return errors.New("content source requires a nonempty UTF-8 description")
	}
	return nil
}

// WorkspaceFileSource is a slash-separated workspace-relative file label, not a
// global locator or permission to read. Load never re-reads it.
//
//dtogen:validate Validate
type WorkspaceFileSource struct {
	// Path is a required slash-separated file label relative to the originating
	// workspace, with no empty or dot components. Validate checks path syntax only,
	// not existence, workspace identity or permission; no file is read on load.
	Path string `cbor:"p"`
}

// Validate rejects empty, absolute, and dot-component paths without cleaning them.
func (v WorkspaceFileSource) Validate() error {
	if v.Path == "." || !fs.ValidPath(v.Path) {
		return errors.New("workspace file requires a relative file path")
	}
	return nil
}

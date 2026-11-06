package format

import (
	"errors"
	"io/fs"
	"unicode/utf8"
)

// ContentSource retains a nonempty explanation and optional contributing file
// label. It does not certify trust or exact copying. Assignment shares the file
// pointer; Clone detaches it. Zero is invalid.
//
//cborgen:validate Validate
type ContentSource struct {
	// Description is a required nonempty exact UTF-8 explanation of provenance,
	// checked locally by Validate; it is not a trust or exact-copy assertion.
	Description string `cbor:"d"`
	// WorkspaceFile optionally names a contributing file in its originating
	// workspace. Nil means no file label; codec traversal validates a present child.
	// It grants no file access and loading never resolves it.
	WorkspaceFile *WorkspaceFileSource `cbor:"w,omitempty"`
}

// Validate checks the explanation, not child validity or origin authenticity.
func (v ContentSource) Validate() error {
	if v.Description == "" || !utf8.ValidString(v.Description) {
		return errors.New("content source requires a nonempty UTF-8 description")
	}
	return nil
}

// WorkspaceFileSource is a slash-separated file label relative to its originating
// workspace, not a global locator or permission to read. Loading never resolves it.
//
//cborgen:validate Validate
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

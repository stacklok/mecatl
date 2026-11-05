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
	Description   string               `cbor:"d"`
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
	Path string `cbor:"p"`
}

// Validate rejects empty, absolute, and dot-component paths without cleaning them.
func (v WorkspaceFileSource) Validate() error {
	if v.Path == "." || !fs.ValidPath(v.Path) {
		return errors.New("workspace file requires a relative file path")
	}
	return nil
}

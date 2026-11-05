package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// ContentSource retains a nonempty safe explanation and optional contributing
// workspace-file label, not proof of trust or exact copying. Assignment shares
// WorkspaceFile; Clone detaches it. Zero is invalid.
type ContentSource struct {
	Description   string
	WorkspaceFile *WorkspaceFileSource
}

// WorkspaceFileSource labels a file relative to its originating workspace.
// The slash-separated path is not a global locator or permission to read it.
type WorkspaceFileSource struct{ Path string }

// Validate checks the explanation and optional relative file label.
func (v ContentSource) Validate() error {
	if err := v.toFormat().Validate(); err != nil {
		return err
	}
	if v.WorkspaceFile != nil {
		return v.WorkspaceFile.Validate()
	}
	return nil
}

// Clone detaches the optional file pointer.
func (v ContentSource) Clone() ContentSource { return contentSourceFromFormat(v.toFormat().Clone()) }

// Equal compares exact values and pointer presence.
func (v ContentSource) Equal(other ContentSource) bool { return v.toFormat().Equal(other.toFormat()) }
func (v ContentSource) toFormat() sessionformat.ContentSource {
	out := sessionformat.ContentSource{Description: v.Description}
	if v.WorkspaceFile != nil {
		f := sessionformat.WorkspaceFileSource(*v.WorkspaceFile)
		out.WorkspaceFile = &f
	}
	return out
}
func contentSourceFromFormat(v sessionformat.ContentSource) ContentSource {
	out := ContentSource{Description: v.Description}
	if v.WorkspaceFile != nil {
		f := WorkspaceFileSource(*v.WorkspaceFile)
		out.WorkspaceFile = &f
	}
	return out
}
func (v ContentSource) checkSize(b *valueBudget) {
	b.text(v.Description)
	if v.WorkspaceFile != nil {
		b.pointer()
		b.text(v.WorkspaceFile.Path)
	}
}

// Validate checks a nonempty UTF-8 relative path without dot components or I/O.
func (v WorkspaceFileSource) Validate() error { return sessionformat.WorkspaceFileSource(v).Validate() }

// Clone returns the immutable path unchanged.
func (v WorkspaceFileSource) Clone() WorkspaceFileSource { return v }

// Equal compares the exact path.
func (v WorkspaceFileSource) Equal(other WorkspaceFileSource) bool { return v == other }

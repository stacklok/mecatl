package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// ContentSource retains a nonempty safe explanation and optional contributing
// workspace-file label, not proof of trust or exact copying. Assignment shares
// WorkspaceFile; Clone detaches it. Zero is invalid.
type ContentSource struct {
	// Description is the required safe explanation of the contribution's origin.
	Description string
	// WorkspaceFile is an optional originating workspace-file label; assignment borrows it.
	WorkspaceFile *WorkspaceFileSource
}

// WorkspaceFileSource labels a file relative to its originating workspace.
// The slash-separated path is not a global locator or permission to read it.
type WorkspaceFileSource struct {
	// Path is the required slash-separated path relative to the originating workspace.
	Path string
}

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

// ContentSourceView retains immutable backing storage. Capture and navigation
// cost O(1); zero has an empty description and no workspace-file label.
type ContentSourceView struct{ value *sessionformat.ContentSource }

// Description returns the exact explanation, or empty for zero.
func (v ContentSourceView) Description() string {
	if v.value == nil {
		return ""
	}
	return v.value.Description
}

// WorkspaceFile returns the optional file label, or zero,false when absent.
func (v ContentSourceView) WorkspaceFile() (WorkspaceFileSourceView, bool) {
	if v.value == nil || v.value.WorkspaceFile == nil {
		return WorkspaceFileSourceView{}, false
	}
	return WorkspaceFileSourceView{value: v.value.WorkspaceFile}, true
}

// WorkspaceFileSourceView retains an immutable file label's backing storage.
// Copying it costs O(1); zero has an empty path.
type WorkspaceFileSourceView struct {
	value *sessionformat.WorkspaceFileSource
}

// Path returns the exact workspace-relative label, or empty for zero.
func (v WorkspaceFileSourceView) Path() string {
	if v.value == nil {
		return ""
	}
	return v.value.Path
}

// Validate checks a nonempty UTF-8 relative path without dot components or I/O.
func (v WorkspaceFileSource) Validate() error { return sessionformat.WorkspaceFileSource(v).Validate() }

// Clone returns the immutable path unchanged.
func (v WorkspaceFileSource) Clone() WorkspaceFileSource { return v }

// Equal compares the exact path.
func (v WorkspaceFileSource) Equal(other WorkspaceFileSource) bool { return v == other }

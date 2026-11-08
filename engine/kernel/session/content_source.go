package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// ContentSource retains provenance and an optional file label, not trust or read authority.
// Assignment shares the optional child; Clone detaches it. Zero is invalid.
type ContentSource = sessionformat.ContentSource

// WorkspaceFileSource labels a file relative to its originating workspace.
// The path is not a global locator or permission to read it.
type WorkspaceFileSource = sessionformat.WorkspaceFileSource

func checkContentSourceSize(v ContentSource, b *valueBudget) {
	b.text(v.Description)
	if v.WorkspaceFile != nil {
		b.pointer()
		b.text(v.WorkspaceFile.Path)
	}
}

// ContentSourceView retains immutable backing storage. Capture and navigation
// cost O(1); zero has an empty description and no workspace-file label.
type ContentSourceView struct{ value *sessionformat.ContentSource }

// WorkspaceFileSourceView retains an immutable file label's backing storage.
// Copying it costs O(1); zero has an empty path.
type WorkspaceFileSourceView struct {
	value *sessionformat.WorkspaceFileSource
}

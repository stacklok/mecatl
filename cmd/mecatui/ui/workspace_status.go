package ui

import (
	"context"
	"time"
)

// WorkspaceSnapshotFunc samples the exact captured session once. A nil collaborator
// means local inspection is unavailable, not that the workspace is clean.
// On error Directory may be resolved, but Git and its totals must not be used.
// Implementations return display-safe errors; paths remain raw until presentation.
type WorkspaceSnapshotFunc func(context.Context, string) (WorkspaceSnapshot, error)

// WorkspaceSnapshot is observational and non-atomic. With no error, nil Git means
// a confirmed non-repository. CapturedAt is the completion time, including failure.
type WorkspaceSnapshot struct {
	Directory  string
	Git        *WorkspaceGitSnapshot
	CapturedAt time.Time
}

// WorkspaceGitSnapshot describes only the repository's own index/working tree.
// Submodule working trees are not inspected; staged gitlinks may appear in Files.
type WorkspaceGitSnapshot struct {
	Root       string
	Branch     string // Empty when detached.
	Head       string // Full object ID; empty for an unborn branch.
	Unborn     bool
	Detached   bool
	Upstream   string
	Divergence *WorkspaceGitDivergence // Nil means unknown, not zero; local refs only.
	Files      []WorkspaceGitFile
	Counts     WorkspaceGitCounts
}

// WorkspaceGitDivergence is the local branch's distance from its upstream.
type WorkspaceGitDivergence struct {
	Ahead  int
	Behind int
}

// WorkspaceGitFile describes a repository-relative, byte-exact Git name (possibly invalid UTF-8).
// OriginalPath is the source of a rename/copy; Path is the destination.
type WorkspaceGitFile struct {
	Path         string
	OriginalPath string
	Index        byte // Porcelain v2 status, including '.' for unchanged.
	Worktree     byte
	Conflicted   bool
	Untracked    bool
}

// WorkspaceGitCounts totals the files in each status category. Staged and Unstaged
// overlap; conflicted rows count only in Conflicted.
type WorkspaceGitCounts struct {
	Staged     int
	Unstaged   int
	Untracked  int
	Conflicted int
}

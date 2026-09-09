package tools

import (
	"github.com/stacklok/mecatl/engine/adapter/fstools"
	search "github.com/stacklok/mecatl/engine/adapter/search"
	"github.com/stacklok/mecatl/engine/tool"
)

// alias.go re-exports the filesystem tool bodies that graduated into the
// importable engine module (engine/adapter/fstools, issue #269) so every existing
// caller — internal/app, cmd/mecademo, internal/adapter/server — keeps compiling
// against the tools package unchanged. The bodies and their correctness/security
// invariants live once, in fstools; these are thin type/func/const aliases, not
// re-implementations, so there is no second copy to drift.

// The filesystem tool types. Read/Grep/Glob are read-only; Edit/Write mutate.
type (
	// ReadTool reads a file and records the read for Edit's read-before-edit
	// invariant. See fstools.ReadTool.
	ReadTool = fstools.ReadTool
	// ListDirTool lists immediate directory children. See fstools.ListDirTool.
	ListDirTool = fstools.ListDirTool
	// EditTool replaces an exact substring, enforcing the three Edit invariants.
	// See fstools.EditTool.
	EditTool = fstools.EditTool
	// WriteTool creates or overwrites a file (read-before-overwrite on existing
	// paths). See fstools.WriteTool.
	WriteTool = fstools.WriteTool
	// CopyTool copies a regular file without overwriting. See fstools.CopyTool.
	CopyTool = fstools.CopyTool
	// MoveTool moves a file or directory without overwriting. See fstools.MoveTool.
	MoveTool = fstools.MoveTool
	// RemoveTool removes one file or empty directory. See fstools.RemoveTool.
	RemoveTool = fstools.RemoveTool
	// GrepTool searches file contents for a regular expression. See
	// fstools.GrepTool.
	GrepTool = fstools.GrepTool
	// GlobTool lists files matching a glob pattern. See fstools.GlobTool.
	GlobTool = fstools.GlobTool
	// ShellTool runs a shell command via an injected tool.CommandRunner. See
	// fstools.ShellTool.
	ShellTool = fstools.ShellTool
)

// ShellToolName is the catalog name of the Shell tool — the single authority for
// the name Shell registers under (see fstools.ShellToolName). Callers probe the
// catalog for bash enablement by this constant rather than a literal.
const ShellToolName = fstools.ShellToolName

// NewShellTool constructs the Shell tool (a thin wrapper over
// fstools.NewShellTool). The runner is read off the tool.Environment at Execute
// time (issue #462), so the constructor takes no runner. The composition root
// registers the returned tool ONLY when a runner is available for the
// namespace. It is a function, not a re-exported var, so no other package can
// reassign the constructor.
func NewShellTool() tool.Tool { return fstools.NewShellTool() }

// WebSearchTool graduated into the importable engine module
// (engine/adapter/search, issue #363). The body and its correctness/security
// invariants live once, in engine/adapter/search; this is a thin type/func alias,
// not a re-implementation, so there is no second copy to drift.
type WebSearchTool = search.WebSearchTool

// NewWebSearchTool constructs the WebSearch tool bound to provider (a thin wrapper
// over search.NewWebSearchTool). It is a function, not a re-exported var, so no
// other package can reassign the constructor.
func NewWebSearchTool(provider tool.SearchProvider) search.WebSearchTool {
	return search.NewWebSearchTool(provider)
}

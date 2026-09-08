// Package nofs implements tool.Workspace for a session that has NO filesystem
// at all (the "no-fs" session profile). It is the HONEST empty workspace, not an
// in-memory one: memfs would accept writes into a tree nobody can ever read back
// outside the process — silently losing data the model believes it saved —
// whereas nofs tells the truth on every operation: nothing exists (reads fail
// with fs.ErrNotExist, searches return empty) and nothing can be created (writes
// fail loudly with ErrNoFilesystem). Nothing exists that can be lost.
//
// It is a reference adapter in the importable engine tree (stdlib + engine/tool
// only — see the engine-adapter-nofs depguard rule), constructed by the server
// adapter as the per-session workspace OVERRIDE for a no-FS session so the
// osfs workspace factory is never consulted for it.
package nofs

import (
	"context"
	"errors"
	"io/fs"

	"github.com/stacklok/mecatl/engine/tool"
)

// ErrNoFilesystem is returned by CreateFile/ReplaceFile: a no-FS session has no
// filesystem to create or replace files in. The message is model-readable (a
// tool surfaces it verbatim). The legacy unconditional Write method is removed;
// the agent-facing tools use the version-bearing CreateFile/ReplaceFile.
var ErrNoFilesystem = errors.New("no filesystem in this session: the no-fs session profile has no workspace to create or replace files in")

// Workspace is the no-filesystem tool.Workspace. The zero value is ready to use;
// it is stateless and safe for concurrent use.
type Workspace struct{}

// New returns the no-filesystem Workspace.
func New() Workspace { return Workspace{} }

// Compile-time assertion that Workspace satisfies the frozen seam.
var _ tool.Workspace = Workspace{}

// Root returns "" — a no-FS session has no session root.
func (Workspace) Root() string { return "" }

// Read reports that the file does not exist: there is no filesystem, so no path
// ever resolves. The error wraps fs.ErrNotExist so callers' errors.Is checks
// behave exactly as on an absent file.
func (Workspace) Read(_ context.Context, path string) ([]byte, error) {
	return nil, &fs.PathError{Op: "read", Path: path, Err: fs.ErrNotExist}
}

// ReadVersion reports the same as Read: no file exists, so there is no content
// and no version. The error wraps fs.ErrNotExist.
func (Workspace) ReadVersion(_ context.Context, path string) ([]byte, tool.FileVersion, error) {
	return nil, tool.FileVersion{}, &fs.PathError{Op: "read", Path: path, Err: fs.ErrNotExist}
}

// Stat reports that the file does not exist, wrapping fs.ErrNotExist (see Read).
func (Workspace) Stat(_ context.Context, path string) (tool.FileInfo, error) {
	return tool.FileInfo{}, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
}

// CreateFile fails loudly with ErrNoFilesystem: nothing can be created in a
// no-FS session.
func (Workspace) CreateFile(context.Context, string, []byte) (tool.FileVersion, error) {
	return tool.FileVersion{}, ErrNoFilesystem
}

// ReplaceFile fails loudly with ErrNoFilesystem: nothing can be replaced in a
// no-FS session.
func (Workspace) ReplaceFile(context.Context, string, tool.FileVersion, []byte) (tool.FileVersion, error) {
	return tool.FileVersion{}, ErrNoFilesystem
}

// Glob returns no matches: there is nothing to match against.
func (Workspace) Glob(context.Context, string) ([]string, error) { return nil, nil }

// Grep returns no matches: there is nothing to search.
func (Workspace) Grep(context.Context, string, string) ([]tool.GrepMatch, error) {
	return nil, nil
}

package tool

import (
	"context"
	"io/fs"

	"github.com/stacklok/mecatl/engine/session"
)

// stubWorkspace is a minimal in-memory Workspace for tool-package tests that
// need an Environment but never touch the filesystem (e.g. ToolSearch). It
// reports nothing exists, so instruction discovery cleanly finds nothing.
type stubWorkspace struct{}

func (stubWorkspace) Root() string { return "/" }
func (stubWorkspace) Read(context.Context, string) ([]byte, error) {
	return nil, fs.ErrNotExist
}
func (stubWorkspace) ReadVersion(context.Context, string) ([]byte, FileVersion, error) {
	return nil, FileVersion{}, fs.ErrNotExist
}
func (stubWorkspace) CreateFile(context.Context, string, []byte) (FileVersion, error) {
	return FileVersion{}, fs.ErrPermission
}
func (stubWorkspace) ReplaceFile(context.Context, string, FileVersion, []byte) (FileVersion, error) {
	return FileVersion{}, fs.ErrPermission
}
func (stubWorkspace) Stat(context.Context, string) (FileInfo, error) {
	return FileInfo{}, fs.ErrNotExist
}
func (stubWorkspace) Glob(context.Context, string) ([]string, error) { return nil, nil }
func (stubWorkspace) Grep(context.Context, string, string) ([]GrepMatch, error) {
	return nil, nil
}
func (stubWorkspace) RecordRead(string, FileVersion)             {}
func (stubWorkspace) RecordedVersion(string) (FileVersion, bool) { return FileVersion{}, false }

// testEnv builds a shell-less Environment over a stubWorkspace for tool-package
// tests that need an Environment but never run tools against a real FS.
func testEnv() Environment {
	env, err := NewEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "test"}, stubWorkspace{}, nil)
	if err != nil {
		panic(err)
	}
	return env
}

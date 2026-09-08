package nofs_test

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestNoFSWorkspaceContract pins the honest-empty contract of the no-FS
// Workspace: every read fails as fs.ErrNotExist (the standard absent-file
// shape), every search is empty, every write is a loud, model-readable refusal,
// and the root is "". The contract is what makes the no-fs profile HONEST —
// nothing exists that can be lost (the explicit not-memfs decision).
func TestNoFSWorkspaceContract(t *testing.T) {
	ctx := context.Background()
	ws := nofs.New()

	if got := ws.Root(); got != "" {
		t.Errorf("Root() = %q, want \"\" (a no-FS session has no session root)", got)
	}

	if _, err := ws.Read(ctx, "any/file.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Read error = %v, want errors.Is(_, fs.ErrNotExist)", err)
	}
	if _, err := ws.Stat(ctx, "any/file.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat error = %v, want errors.Is(_, fs.ErrNotExist)", err)
	}

	if matches, err := ws.Glob(ctx, "**/*.go"); err != nil || len(matches) != 0 {
		t.Errorf("Glob = (%v, %v), want empty with no error", matches, err)
	}
	if matches, err := ws.Grep(ctx, "anything", ""); err != nil || len(matches) != 0 {
		t.Errorf("Grep = (%v, %v), want empty with no error", matches, err)
	}

	// The unconditional Write seam was removed from the no-FS workspace (the
	// agent-facing tools use CreateFile/ReplaceFile); CreateFile is the
	// model-reachable create and must fail loudly.
	if _, err := ws.CreateFile(ctx, "new.txt", []byte("data")); !errors.Is(err, nofs.ErrNoFilesystem) {
		t.Errorf("CreateFile error = %v, want ErrNoFilesystem", err)
	}
	if err := nofs.ErrNoFilesystem; err.Error() == "" {
		t.Error("ErrNoFilesystem must be a non-empty, model-readable refusal")
	}

	// Version-bearing reads and explicit mutations are inert too.
	if _, _, err := ws.ReadVersion(ctx, "a.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadVersion error = %v, want errors.Is(_, fs.ErrNotExist)", err)
	}
	if _, err := ws.CreateFile(ctx, "a.txt", []byte("data")); !errors.Is(err, nofs.ErrNoFilesystem) {
		t.Errorf("CreateFile error = %v, want ErrNoFilesystem", err)
	}
	if _, err := ws.ReplaceFile(ctx, "a.txt", tool.FileVersion{}, []byte("data")); !errors.Is(err, nofs.ErrNoFilesystem) {
		t.Errorf("ReplaceFile error = %v, want ErrNoFilesystem", err)
	}
}

// TestNoFSPathErrorCarriesPath pins that the not-exist errors carry the asked
// path (a *fs.PathError), so a tool's error body names the file the model asked
// for instead of a bare "file does not exist".
func TestNoFSPathErrorCarriesPath(t *testing.T) {
	_, err := nofs.New().Read(context.Background(), "docs/missing.md")
	var pe *fs.PathError
	if !errors.As(err, &pe) {
		t.Fatalf("Read error = %T, want *fs.PathError", err)
	}
	if pe.Path != "docs/missing.md" {
		t.Errorf("PathError.Path = %q, want the asked path", pe.Path)
	}
}

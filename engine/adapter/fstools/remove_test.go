package fstools

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestRemoveRequiresPath(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	res := exec(t, RemoveTool{}, call(t, "Remove", map[string]any{"path": ""}), ws)
	if !res.IsError {
		t.Fatal("Remove with an empty path must be a tool error")
	}
}

// TestRemoveDoesNotRequireReadFirst proves Remove is a namespace operation
// with NO read-before-mutate invariant (unlike Edit/existing-file Write): an
// un-Read file may still be removed.
func TestRemoveDoesNotRequireReadFirst(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "a.txt", "content")
	res := exec(t, RemoveTool{}, call(t, "Remove", map[string]any{"path": "a.txt"}), ws)
	if res.IsError {
		t.Fatalf("Remove of an un-Read file should succeed (no read-before-mutate invariant), got: %s", res.Content)
	}
	if _, err := ws.Read(context.Background(), "a.txt"); err == nil {
		t.Fatal("file still present after Remove")
	}
}

// TestRemoveDoesNotConsultReadLedger proves Remove neither reads nor writes
// the read ledger: a prior Read's recorded version is untouched by Remove
// (there is nothing left to compare it against, and Remove must not silently
// clear it out for an unrelated later path).
func TestRemoveDoesNotConsultReadLedger(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "keep.txt", "kept")
	seed(t, ws, "gone.txt", "removed")
	exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "keep.txt"}), ws)
	ledger := ledgerFor(ws)
	before, ok, err := ledger.RecordedVersion(context.Background(), tool.LedgerKey(ws.Root(), "keep.txt"))
	if err != nil || !ok {
		t.Fatalf("precondition: keep.txt should have a recorded read, ok=%v err=%v", ok, err)
	}

	res := exec(t, RemoveTool{}, call(t, "Remove", map[string]any{"path": "gone.txt"}), ws)
	if res.IsError {
		t.Fatalf("Remove errored: %s", res.Content)
	}

	after, ok, err := ledger.RecordedVersion(context.Background(), tool.LedgerKey(ws.Root(), "keep.txt"))
	if err != nil || !ok {
		t.Fatalf("Remove of an unrelated path disturbed keep.txt's ledger entry: ok=%v err=%v", ok, err)
	}
	if !after.Equal(before) {
		t.Fatal("Remove of an unrelated path changed keep.txt's recorded version")
	}
}

// TestRemoveIsNeverRecursive pins the model-visible refusal: a non-empty
// directory is NOT removed, and its content survives byte-unchanged.
func TestRemoveIsNeverRecursive(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "sub/keep.txt", "still here")

	res := exec(t, RemoveTool{}, call(t, "Remove", map[string]any{"path": "sub"}), ws)
	if !res.IsError {
		t.Fatal("Remove of a non-empty directory must be a tool error (never recursive)")
	}
	if !strings.Contains(res.Content, "not empty") {
		t.Errorf("Remove non-empty-directory error should say so, got: %q", res.Content)
	}
	data, err := ws.Read(context.Background(), "sub/keep.txt")
	if err != nil || string(data) != "still here" {
		t.Fatalf("Remove of a refused non-empty directory disturbed its content: data=%q err=%v", data, err)
	}
}

func TestRemoveMissingPath(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	res := exec(t, RemoveTool{}, call(t, "Remove", map[string]any{"path": "nope.txt"}), ws)
	if !res.IsError {
		t.Fatal("Remove of a missing path must be a tool error")
	}
	if !strings.Contains(res.Content, "does not exist") {
		t.Errorf("Remove missing-path error should say so, got: %q", res.Content)
	}
}

func TestRemoveCapabilityMissingIsModelVisible(t *testing.T) {
	base := memfs.NewWorkspace("/")
	ws := noNamespaceWorkspace{Workspace: base}
	res := exec(t, RemoveTool{}, call(t, "Remove", map[string]any{"path": "a.txt"}), ws)
	if !res.IsError {
		t.Fatal("Remove against a namespace-less workspace must be a tool error")
	}
	if !strings.Contains(res.Content, "does not support") {
		t.Errorf("Remove capability-missing error should be legible, got: %q", res.Content)
	}
}

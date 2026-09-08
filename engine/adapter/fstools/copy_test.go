package fstools

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestCopyRequiresSourceAndDestination(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	for _, args := range []map[string]any{
		{"source": "", "destination": "b.txt"},
		{"source": "a.txt", "destination": ""},
	} {
		res := exec(t, CopyTool{}, call(t, "Copy", args), ws)
		if !res.IsError {
			t.Fatalf("Copy with args %v must be a tool error", args)
		}
	}
}

// TestCopyDoesNotRequireReadFirst proves Copy needs no prior Read of the
// source (unlike Edit's read-before-edit invariant), and the source survives
// untouched (Copy is not a move).
func TestCopyDoesNotRequireReadFirst(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "a.txt", "payload")
	res := exec(t, CopyTool{}, call(t, "Copy", map[string]any{"source": "a.txt", "destination": "b.txt"}), ws)
	if res.IsError {
		t.Fatalf("Copy of an un-Read source should succeed, got: %s", res.Content)
	}
	if data, err := ws.Read(context.Background(), "a.txt"); err != nil || string(data) != "payload" {
		t.Fatalf("source after Copy: data=%q err=%v, want unchanged", data, err)
	}
	if data, err := ws.Read(context.Background(), "b.txt"); err != nil || string(data) != "payload" {
		t.Fatalf("destination after Copy: data=%q err=%v, want payload/nil", data, err)
	}
}

// TestCopyNeverOverwritesDestination pins the model-visible no-clobber
// guarantee: an existing destination is refused and left byte-unchanged.
func TestCopyNeverOverwritesDestination(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "a.txt", "source content")
	seed(t, ws, "b.txt", "destination content")

	res := exec(t, CopyTool{}, call(t, "Copy", map[string]any{"source": "a.txt", "destination": "b.txt"}), ws)
	if !res.IsError {
		t.Fatal("Copy onto an existing destination must be a tool error (never overwrite)")
	}
	if !strings.Contains(res.Content, "already exists") {
		t.Errorf("Copy existing-destination error should say so, got: %q", res.Content)
	}
	if data, err := ws.Read(context.Background(), "b.txt"); err != nil || string(data) != "destination content" {
		t.Fatalf("destination after refused Copy: data=%q err=%v, want unchanged", data, err)
	}
}

func TestCopyMissingSource(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	res := exec(t, CopyTool{}, call(t, "Copy", map[string]any{"source": "nope.txt", "destination": "dest.txt"}), ws)
	if !res.IsError {
		t.Fatal("Copy of a missing source must be a tool error")
	}
	if !strings.Contains(res.Content, "does not exist") {
		t.Errorf("Copy missing-source error should say so, got: %q", res.Content)
	}
}

func TestCopyDirectoryIsRefused(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "sub/file.txt", "x")
	res := exec(t, CopyTool{}, call(t, "Copy", map[string]any{"source": "sub", "destination": "sub2"}), ws)
	if !res.IsError {
		t.Fatal("Copy of a directory must be a tool error (regular files only)")
	}
}

// TestCopyDoesNotConsultReadLedger proves a successful Copy does not disturb
// an unrelated file's recorded read, and does not itself record a read for
// the new destination (Edit on the copy still needs its own Read first).
func TestCopyDoesNotConsultReadLedger(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "keep.txt", "kept")
	seed(t, ws, "a.txt", "copied")
	exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "keep.txt"}), ws)
	ledger := ledgerFor(ws)
	before, ok, err := ledger.RecordedVersion(context.Background(), tool.LedgerKey(ws.Root(), "keep.txt"))
	if err != nil || !ok {
		t.Fatalf("precondition: keep.txt should have a recorded read, ok=%v err=%v", ok, err)
	}

	res := exec(t, CopyTool{}, call(t, "Copy", map[string]any{"source": "a.txt", "destination": "b.txt"}), ws)
	if res.IsError {
		t.Fatalf("Copy errored: %s", res.Content)
	}

	after, ok, err := ledger.RecordedVersion(context.Background(), tool.LedgerKey(ws.Root(), "keep.txt"))
	if err != nil || !ok || !after.Equal(before) {
		t.Fatalf("Copy disturbed keep.txt's ledger entry: ok=%v err=%v equal=%v", ok, err, after.Equal(before))
	}
	if _, ok, err := ledger.RecordedVersion(context.Background(), tool.LedgerKey(ws.Root(), "b.txt")); err != nil {
		t.Fatalf("RecordedVersion: %v", err)
	} else if ok {
		t.Fatal("Copy recorded a read-ledger entry for the new destination; it must not")
	}
	editRes := exec(t, EditTool{}, call(t, "Edit", map[string]any{
		"path": "b.txt", "old_string": "copied", "new_string": "changed",
	}), ws)
	if !editRes.IsError {
		t.Fatal("Edit succeeded on the copy destination without a prior Read — Copy must not satisfy read-before-edit")
	}
}

func TestCopyCapabilityMissingIsModelVisible(t *testing.T) {
	base := memfs.NewWorkspace("/")
	ws := noNamespaceWorkspace{Workspace: base}
	res := exec(t, CopyTool{}, call(t, "Copy", map[string]any{"source": "a.txt", "destination": "b.txt"}), ws)
	if !res.IsError {
		t.Fatal("Copy against a namespace-less workspace must be a tool error")
	}
	if !strings.Contains(res.Content, "does not support") {
		t.Errorf("Copy capability-missing error should be legible, got: %q", res.Content)
	}
}

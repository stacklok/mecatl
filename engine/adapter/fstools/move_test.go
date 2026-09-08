package fstools

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestMoveRequiresSourceAndDestination(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	for _, args := range []map[string]any{
		{"source": "", "destination": "b.txt"},
		{"source": "a.txt", "destination": ""},
	} {
		res := exec(t, MoveTool{}, call(t, "Move", args), ws)
		if !res.IsError {
			t.Fatalf("Move with args %v must be a tool error", args)
		}
	}
}

// TestMoveDoesNotRequireReadFirst proves Move needs no prior Read of the
// source (unlike Edit's read-before-edit invariant).
func TestMoveDoesNotRequireReadFirst(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "a.txt", "payload")
	res := exec(t, MoveTool{}, call(t, "Move", map[string]any{"source": "a.txt", "destination": "b.txt"}), ws)
	if res.IsError {
		t.Fatalf("Move of an un-Read source should succeed, got: %s", res.Content)
	}
	data, err := ws.Read(context.Background(), "b.txt")
	if err != nil || string(data) != "payload" {
		t.Fatalf("destination after Move: data=%q err=%v, want payload/nil", data, err)
	}
}

// TestMoveNeverOverwritesDestination pins the model-visible no-clobber
// guarantee: an existing destination is refused, and BOTH the source and the
// destination survive byte-unchanged.
func TestMoveNeverOverwritesDestination(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "a.txt", "source content")
	seed(t, ws, "b.txt", "destination content")

	res := exec(t, MoveTool{}, call(t, "Move", map[string]any{"source": "a.txt", "destination": "b.txt"}), ws)
	if !res.IsError {
		t.Fatal("Move onto an existing destination must be a tool error (never overwrite)")
	}
	if !strings.Contains(res.Content, "already exists") {
		t.Errorf("Move existing-destination error should say so, got: %q", res.Content)
	}
	if data, err := ws.Read(context.Background(), "a.txt"); err != nil || string(data) != "source content" {
		t.Fatalf("source after refused Move: data=%q err=%v, want unchanged", data, err)
	}
	if data, err := ws.Read(context.Background(), "b.txt"); err != nil || string(data) != "destination content" {
		t.Fatalf("destination after refused Move: data=%q err=%v, want unchanged", data, err)
	}
}

func TestMoveMissingSource(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	res := exec(t, MoveTool{}, call(t, "Move", map[string]any{"source": "nope.txt", "destination": "dest.txt"}), ws)
	if !res.IsError {
		t.Fatal("Move of a missing source must be a tool error")
	}
	if !strings.Contains(res.Content, "does not exist") {
		t.Errorf("Move missing-source error should say so, got: %q", res.Content)
	}
}

// TestMoveDoesNotConsultReadLedger proves a successful Move does not disturb
// an unrelated file's recorded read.
func TestMoveDoesNotConsultReadLedger(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "keep.txt", "kept")
	seed(t, ws, "a.txt", "moved")
	exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "keep.txt"}), ws)
	ledger := ledgerFor(ws)
	before, ok, err := ledger.RecordedVersion(context.Background(), tool.LedgerKey(ws.Root(), "keep.txt"))
	if err != nil || !ok {
		t.Fatalf("precondition: keep.txt should have a recorded read, ok=%v err=%v", ok, err)
	}

	res := exec(t, MoveTool{}, call(t, "Move", map[string]any{"source": "a.txt", "destination": "b.txt"}), ws)
	if res.IsError {
		t.Fatalf("Move errored: %s", res.Content)
	}

	after, ok, err := ledger.RecordedVersion(context.Background(), tool.LedgerKey(ws.Root(), "keep.txt"))
	if err != nil || !ok || !after.Equal(before) {
		t.Fatalf("Move of an unrelated path disturbed keep.txt's ledger entry: ok=%v err=%v equal=%v", ok, err, after.Equal(before))
	}
}

func TestMoveCapabilityMissingIsModelVisible(t *testing.T) {
	base := memfs.NewWorkspace("/")
	ws := noNamespaceWorkspace{Workspace: base}
	res := exec(t, MoveTool{}, call(t, "Move", map[string]any{"source": "a.txt", "destination": "b.txt"}), ws)
	if !res.IsError {
		t.Fatal("Move against a namespace-less workspace must be a tool error")
	}
	if !strings.Contains(res.Content, "does not support") {
		t.Errorf("Move capability-missing error should be legible, got: %q", res.Content)
	}
}

package fstools

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// noNamespaceWorkspace wraps a tool.Workspace WITHOUT implementing
// tool.WorkspaceNamespace, so the four namespace tools' capability-missing
// branch is exercised against a workspace that genuinely lacks the extension
// (e.g. ACP), independent of any concrete adapter's own refusal wording.
type noNamespaceWorkspace struct {
	tool.Workspace
}

func TestListDirRequiresPath(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	res := exec(t, ListDirTool{}, call(t, "ListDir", map[string]any{"path": ""}), ws)
	if !res.IsError {
		t.Fatal("ListDir with an empty path must be a tool error")
	}
}

func TestListDirListsImmediateChildren(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "a.txt", "x")
	seed(t, ws, "b.txt", "y")
	seed(t, ws, "sub/nested.txt", "z")

	res := exec(t, ListDirTool{}, call(t, "ListDir", map[string]any{"path": "."}), ws)
	if res.IsError {
		t.Fatalf("ListDir errored: %s", res.Content)
	}
	if !strings.Contains(res.Content, "a.txt") || !strings.Contains(res.Content, "b.txt") {
		t.Errorf("ListDir output missing files: %q", res.Content)
	}
	if !strings.Contains(res.Content, "sub/") {
		t.Errorf("ListDir output must mark the directory entry with a trailing slash: %q", res.Content)
	}
}

func TestListDirEmptyDirectory(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	res := exec(t, ListDirTool{}, call(t, "ListDir", map[string]any{"path": "."}), ws)
	if res.IsError {
		t.Fatalf("ListDir on an empty workspace errored: %s", res.Content)
	}
	if !strings.Contains(res.Content, "empty") {
		t.Errorf("ListDir on an empty directory should say so, got: %q", res.Content)
	}
}

func TestListDirMissingDirectory(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	res := exec(t, ListDirTool{}, call(t, "ListDir", map[string]any{"path": "nope"}), ws)
	if !res.IsError {
		t.Fatal("ListDir of a missing directory must be a tool error")
	}
}

func TestListDirRejectsNonDirectoryAndMalformedArgs(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "file.txt", "content")

	res := exec(t, ListDirTool{}, call(t, "ListDir", map[string]any{"path": "file.txt"}), ws)
	if !res.IsError {
		t.Fatal("ListDir of a regular file must be a tool error")
	}
	malformed := session.NewToolCall("bad-list", "ListDir", []byte(`{"path":`))
	res = exec(t, ListDirTool{}, malformed, ws)
	if !res.IsError || !strings.Contains(res.Content, "invalid arguments") {
		t.Fatalf("ListDir malformed args result = %+v, want model-visible invalid-arguments error", res)
	}
}

// TestListDirDoesNotConsultReadLedger proves ListDir is a pure namespace read:
// it neither requires a prior Read (unlike Edit's read-before-edit invariant)
// nor records one for a later Edit/Write to consume.
func TestListDirDoesNotConsultReadLedger(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "sub/file.txt", "content")
	ledger := ledgerFor(ws)

	// No prior Read of anything under "sub" — ListDir must still succeed.
	res := exec(t, ListDirTool{}, call(t, "ListDir", map[string]any{"path": "sub"}), ws)
	if res.IsError {
		t.Fatalf("ListDir without a prior Read errored: %s", res.Content)
	}

	// ListDir must not have recorded a read for the listed file: Edit on the
	// unread file still requires its own Read first.
	if _, ok, err := ledger.RecordedVersion(context.Background(), tool.LedgerKey(ws.Root(), "sub/file.txt")); err != nil {
		t.Fatalf("RecordedVersion: %v", err)
	} else if ok {
		t.Fatal("ListDir recorded a read-ledger entry for a listed file; it must not participate in the read ledger")
	}
	editRes := exec(t, EditTool{}, call(t, "Edit", map[string]any{
		"path": "sub/file.txt", "old_string": "content", "new_string": "changed",
	}), ws)
	if !editRes.IsError {
		t.Fatal("Edit succeeded on a file ListDir (not Read) surfaced — ListDir must not satisfy read-before-edit")
	}
}

// TestListDirCapabilityMissingIsModelVisible proves the tool surfaces a clear,
// recoverable error (not a harness crash) when the workspace lacks the
// tool.WorkspaceNamespace extension.
func TestListDirCapabilityMissingIsModelVisible(t *testing.T) {
	base := memfs.NewWorkspace("/")
	ws := noNamespaceWorkspace{Workspace: base}
	res := exec(t, ListDirTool{}, call(t, "ListDir", map[string]any{"path": "."}), ws)
	if !res.IsError {
		t.Fatal("ListDir against a namespace-less workspace must be a tool error")
	}
	if !strings.Contains(res.Content, "does not support") {
		t.Errorf("ListDir capability-missing error should be legible, got: %q", res.Content)
	}
}

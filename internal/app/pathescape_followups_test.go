package app

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/fstools"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// pathescape_followups_test.go pins the Wave-1 panel-review follow-up ACs
// (docs/acceptance/path-escape-posture.md, "Wave-1 panel-review follow-ups"):
// AC-W2-F1 (the Edit-ledger pseudo-fs asymmetry) and AC-W2-F2
// (vetRelaxedParent shares Canonicalize — the structural half of that pin
// lives in internal/adapter/osfs, this file pins the observable ledger half).

// TestPathEscapePosture_EditLedgerPseudoFSGuarded pins AC-W2-F1: the read-ledger
// — RecordRead and RecordedVersion, the two tool-body ledger operations — route
// through the SAME pseudo-fs guard escapeWorkspace.Read/Stat/Write consult, so a
// pseudo-fs path (/proc, /sys, /dev) can never enter the ledger. The escape
// policy already hard-denies pseudo-fs for Edit before dispatch; this wrapper
// override is the defense-in-depth at the tool-body boundary.
//
// The guard is fail-safe: a guarded RecordedVersion answers (zero, false)
// (never read), so an Edit relying on a pseudo-fs ledger entry can never
// validate; a guarded RecordRead is a no-op (the version is never stored).
//
// The load-bearing half is the CHECK: a RecordRead that recorded nothing only
// ever WEAKENS Edit's gate (the edit fails read-before-edit), never bypasses it
// — so the guarded-record assertion below is the honest-ledger half, while the
// planted-entry half proves the check consults the guard itself (the
// mutation-verify: dropping the RecordedVersion override turns that half red).
func TestPathEscapePosture_EditLedgerPseudoFSGuarded(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	clf, err := newEscapeClassifier(root)
	if err != nil {
		t.Fatalf("newEscapeClassifier: %v", err)
	}
	base, err := osfs.NewWorkspace(root, osfs.WithRelaxedReads(), osfs.WithRelaxedWrites())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	ws := newEscapeWorkspace(base, clf)

	ledger := memledger.New()
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: root}, ws, ledger, nil)
	ctx := context.Background()
	const procPath = "/proc/version"
	readResult, err := (fstools.ReadTool{}).Execute(ctx, session.ToolCall{ID: "read", Name: "Read", Args: []byte(`{"path":"/proc/version"}`)}, env)
	if err != nil || !readResult.IsError || !strings.Contains(readResult.Content, "pseudo-filesystem") {
		t.Fatalf("Read(%q) = (%+v, %v), want pseudo-filesystem refusal", procPath, readResult, err)
	}
	key := tool.LedgerKey(ws.Root(), procPath)
	if _, ok, err := ledger.RecordedVersion(ctx, key); err != nil || ok {
		t.Fatalf("ledger after refused Read = (ok=%v, err=%v), want absent", ok, err)
	}

	// Planted evidence cannot bypass the content workspace's pseudo-filesystem guard.
	if err := ledger.RecordRead(ctx, key, tool.NewFileVersion("caller-token")); err != nil {
		t.Fatal(err)
	}
	editResult, err := (fstools.EditTool{}).Execute(ctx, session.ToolCall{ID: "edit", Name: "Edit", Args: []byte(`{"path":"/proc/version","old_string":"x","new_string":"y"}`)}, env)
	if err != nil || !editResult.IsError || !strings.Contains(editResult.Content, "pseudo-filesystem") {
		t.Fatalf("Edit(%q) = (%+v, %v), want pseudo-filesystem refusal", procPath, editResult, err)
	}

	if _, err := ws.CreateFile(ctx, "in-root.txt", []byte("v1")); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	readResult, err = (fstools.ReadTool{}).Execute(ctx, session.ToolCall{ID: "read2", Name: "Read", Args: []byte(`{"path":"in-root.txt"}`)}, env)
	if err != nil || readResult.IsError {
		t.Fatalf("ordinary Read = (%+v, %v)", readResult, err)
	}
	if _, ok, err := ledger.RecordedVersion(ctx, tool.LedgerKey(ws.Root(), "in-root.txt")); err != nil || !ok {
		t.Fatalf("ordinary read evidence = (ok=%v, err=%v), want present", ok, err)
	}
}

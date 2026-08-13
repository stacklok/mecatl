package app

import (
	"context"
	"strings"
	"testing"

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

	// /proc/version exists on every Linux host running these tests; its bytes
	// are readable via a plain os.ReadFile, so an unguarded ReadVersion WOULD
	// mint a version for it — the guard must be the thing that stops it, not a
	// missing file.
	const procPath = "/proc/version"
	if _, err := ws.Read(context.Background(), procPath); err == nil || !strings.Contains(err.Error(), "pseudo-filesystem") {
		t.Fatalf("escapeWorkspace.Read(%q) = %v, want the pseudo-fs refusal (sanity: the read guard is armed)", procPath, err)
	}

	// RecordRead must NOT silently store a pseudo-fs entry in the ledger: the
	// record is guarded, so the entry stays unrecorded.
	ws.RecordRead(procPath, tool.NewFileVersion("caller-token"))
	if _, ok := ws.RecordedVersion(procPath); ok {
		t.Fatalf("RecordedVersion(%q) reported ok=true after a guarded RecordRead", procPath)
	}

	// Even with a PLANTED ledger entry (as if the record half regressed to the
	// inner workspace), the check half must not validate a pseudo-fs read:
	// RecordedVersion consults the guard itself, so the planted entry can never
	// satisfy an Edit's read-before-edit check.
	ews, isEscape := ws.(*escapeWorkspace)
	if !isEscape {
		t.Fatalf("newEscapeWorkspace returned %T, want *escapeWorkspace", ws)
	}
	ews.Workspace.RecordRead(procPath, tool.NewFileVersion("caller-token"))
	if _, ok := ws.RecordedVersion(procPath); ok {
		t.Fatalf("RecordedVersion(%q) reported ok=true with a planted entry", procPath)
	}

	// The guard never touches an ordinary in-root path: the ledger behaves
	// exactly as the unwrapped workspace for the ordinary case.
	if _, err := ws.CreateFile(context.Background(), "in-root.txt", []byte("v1")); err != nil {
		t.Fatalf("CreateFile(in-root.txt): %v", err)
	}
	if _, ver, err := ws.ReadVersion(context.Background(), "in-root.txt"); err != nil {
		t.Fatalf("ReadVersion(in-root.txt): %v — the ledger guard must leave ordinary paths untouched", err)
	} else {
		ws.RecordRead("in-root.txt", ver)
		if got, ok := ws.RecordedVersion("in-root.txt"); !ok || !got.Equal(ver) {
			t.Fatalf("in-root RecordedVersion did not return the recorded version (ok=%v)", ok)
		}
	}
}

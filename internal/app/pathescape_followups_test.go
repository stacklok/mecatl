package app

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// pathescape_followups_test.go pins the Wave-1 panel-review follow-up ACs
// (docs/acceptance/path-escape-posture.md, "Wave-1 panel-review follow-ups"):
// AC-W2-F1 (the Edit-ledger pseudo-fs asymmetry) and AC-W2-F2
// (vetRelaxedParent shares Canonicalize — the structural half of that pin
// lives in internal/adapter/osfs, this file pins the observable ledger half).

// TestPathEscapePosture_EditLedgerPseudoFSGuarded pins AC-W2-F1: the Edit
// read-ledger — RecordRead and WasReadUnchanged, the two tool-body reads that
// re-read a file's content for the read-before-edit fingerprint — routes
// through the SAME pseudo-fs guard escapeWorkspace.Read/Stat/Write consult, so
// an Edit-ledger read of a pseudo-fs path (/proc, /sys, /dev) can never bypass
// the guard. The escape policy already hard-denies pseudo-fs for Edit before
// dispatch; this wrapper override is the defense-in-depth at the tool-body
// boundary, uniform across every read site (the asymmetry the Wave-1 panel
// flagged: osfs.Workspace.fingerprint reads via the inner w.fs.Read, and the
// ledger must not be the one read site the wrapper forgets).
//
// The guard is fail-safe: a guarded WasReadUnchanged answers false (never
// read), so an Edit relying on a pseudo-fs ledger entry can never validate;
// a guarded RecordRead is a no-op (the fingerprint is never stored).
//
// The load-bearing half is the CHECK: a RecordRead that recorded nothing only
// ever WEAKENS Edit's gate (the edit fails read-before-edit), never bypasses
// it — so the guarded-record assertion below is the honest-ledger half, while
// the planted-entry half proves the check consults the guard itself (the
// mutation-verify: dropping the WasReadUnchanged override turns that half
// red).
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
	// are readable via a plain os.ReadFile, so an unguarded ledger read WOULD
	// fingerprint it — the guard must be the thing that stops it, not a
	// missing file.
	const procPath = "/proc/version"
	if _, err := ws.Read(context.Background(), procPath); err == nil || !strings.Contains(err.Error(), "pseudo-filesystem") {
		t.Fatalf("escapeWorkspace.Read(%q) = %v, want the pseudo-fs refusal (sanity: the read guard is armed)", procPath, err)
	}

	// RecordRead must NOT silently fingerprint the pseudo-fs file into the
	// ledger: the fingerprint read is guarded, so the entry stays unrecorded.
	ws.RecordRead(procPath, "caller-token")
	ok, err := ws.WasReadUnchanged(context.Background(), procPath)
	if err != nil {
		t.Fatalf("WasReadUnchanged(%q): %v — a guarded ledger read reports never-read, not an error", procPath, err)
	}
	if ok {
		t.Fatalf("WasReadUnchanged(%q) = true after a guarded RecordRead — the Edit ledger fingerprinted a pseudo-fs path (the guard was bypassed)", procPath)
	}

	// Even with a PLANTED ledger entry (as if the record half regressed to the
	// inner workspace), the check half must not validate a pseudo-fs read:
	// WasReadUnchanged consults the guard itself, so the planted entry can
	// never satisfy an Edit's read-before-edit-and-unchanged check.
	ews, isEscape := ws.(*escapeWorkspace)
	if !isEscape {
		t.Fatalf("newEscapeWorkspace returned %T, want *escapeWorkspace", ws)
	}
	ews.Workspace.RecordRead(procPath, "caller-token")
	ok, err = ws.WasReadUnchanged(context.Background(), procPath)
	if err != nil {
		t.Fatalf("WasReadUnchanged(%q) with a planted entry: %v", procPath, err)
	}
	if ok {
		t.Fatalf("WasReadUnchanged(%q) = true with a planted entry — the ledger check half bypassed the pseudo-fs guard", procPath)
	}

	// The guard never touches an ordinary in-root path: the ledger behaves
	// exactly as the unwrapped workspace for the ordinary case.
	if err := ws.Write(context.Background(), "in-root.txt", []byte("v1")); err != nil {
		t.Fatalf("Write(in-root.txt): %v", err)
	}
	ws.RecordRead("in-root.txt", "")
	ok, err = ws.WasReadUnchanged(context.Background(), "in-root.txt")
	if err != nil || !ok {
		t.Fatalf("in-root WasReadUnchanged = %v, %v — the ledger guard must leave ordinary paths untouched", ok, err)
	}
}

package tool

import (
	"path/filepath"
	"testing"
)

// TestLedgerKey is the ONE comprehensive table test for the I/O-free lexical
// ledger-key normalization (ADR 0103). It pins every convergence class the
// osfs/ACP adapters previously duplicated in their own local ledgerKey tests:
// relative vs absolute-in-root cross-form matching in BOTH directions,
// `..`-carrying lexical alias convergence, out-of-root absolute keying by the
// cleaned absolute form, the empty-path sentinel, and that a relative path
// climbing above the root keys by its cleaned form (the ledger is a lookup,
// not a confinement gate — confinement is the Workspace's job at use time).
// Physical symlink aliases are deliberately NOT covered here: they may
// conservatively produce distinct entries (a safe false-negative that forces
// another Read), so they are a use-time property, not a lexical one.
func TestLedgerKey(t *testing.T) {
	// Build a stable, canonical-absolute root and an outside dir ONCE, so the
	// expected values for the out-of-root cases are comparable across cases.
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	outside := filepath.Join(base, "outside")
	// toSlash normalizes expected values for cross-OS portability; LedgerKey
	// itself uses filepath.Clean/ToSlash, so the expected forms are built with
	// the same primitives.
	rootSlash := filepath.ToSlash(root)
	outsideSlash := filepath.ToSlash(outside)

	cases := []struct {
		name string
		root string
		path string
		want string
	}{
		// Empty path is the "." sentinel (mirrors osfs.resolvePath).
		{"empty-ish path is dot", rootSlash, ".", "."},

		// Relative path keys by its cleaned slash form.
		{"relative clean", rootSlash, "a/b.txt", "a/b.txt"},
		{"relative with .. collapses", rootSlash, "a/../b.txt", "b.txt"},
		{"relative trailing slash stripped", rootSlash, "dir/", "dir"},

		// Absolute in-root <root>/<rel> converges with the relative <rel> form.
		{"absolute in-root reduces to relative", rootSlash, rootSlash + "/a/b.txt", "a/b.txt"},
		{"absolute in-root with .. collapses to relative", rootSlash, rootSlash + "/deep/../out.txt", "out.txt"},
		{"root itself reduces to dot", rootSlash, rootSlash, "."},

		// Cross-form convergence is symmetric: record relative, lookup absolute,
		// and vice versa, all share the SAME key.
		{"relative and absolute-in-root share key", rootSlash, "dir/file.txt", "dir/file.txt"},
		{"absolute-in-root and relative share key", rootSlash, rootSlash + "/dir/file.txt", "dir/file.txt"},

		// Out-of-root absolute keys by its cleaned absolute form (so a
		// `..`-carrying lexical alias matches in both directions).
		{"out-of-root absolute keys by cleaned abs", rootSlash, outsideSlash + "/x.txt", outsideSlash + "/x.txt"},
		{"out-of-root abs alias converges with cleaned form", rootSlash, outsideSlash + "/deep/../x.txt", outsideSlash + "/x.txt"},

		// A relative path climbing above the root keys by its cleaned form;
		// the ledger does NOT confine (it is a lookup). This documents the
		// contract so a future tightening does not silently change it.
		{"relative climbing above root keys by cleaned form", rootSlash, "../sibling.txt", "../sibling.txt"},

		// A relative root (memfs passes a logical root like "/"): an absolute
		// path under "/" reduces to its cleaned slash tail.
		{"absolute under root / reduces to relative", "/", "/a/b.txt", "a/b.txt"},
		{"relative under root / stays relative", "/", "a/b.txt", "a/b.txt"},

		// An absolute path with a relative root keys by its cleaned absolute
		// form (defensive: filepath.Rel errors, so the abs branch is used).
		{"absolute path with relative root keys by cleaned abs", "relroot", outsideSlash + "/x.txt", outsideSlash + "/x.txt"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LedgerKey(tc.root, tc.path)
			if got != tc.want {
				t.Fatalf("LedgerKey(%q, %q) = %q, want %q", tc.root, tc.path, got, tc.want)
			}
		})
	}
}

// TestLedgerKeyCrossFormConverges pins the symmetric convergence property
// directly: record-by-X / lookup-by-Y must return the same key for ordinary
// in-root forms, in both directions, and an out-of-root absolute must NOT
// collide with an in-root relative sharing a tail.
func TestLedgerKeyCrossFormConverges(t *testing.T) {
	base := t.TempDir()
	root := filepath.ToSlash(filepath.Join(base, "ws"))
	outside := filepath.ToSlash(filepath.Join(base, "outside"))

	rel := "dir/file.txt"
	abs := root + "/dir/file.txt"
	absDirty := root + "/deep/../dir/file.txt"

	if LedgerKey(root, rel) != LedgerKey(root, abs) {
		t.Fatalf("relative and absolute-in-root did not converge: %q vs %q", LedgerKey(root, rel), LedgerKey(root, abs))
	}
	if LedgerKey(root, rel) != LedgerKey(root, absDirty) {
		t.Fatalf("relative and dirty-absolute-in-root did not converge: %q vs %q", LedgerKey(root, rel), LedgerKey(root, absDirty))
	}
	// An out-of-root absolute and its `..`-carrying alias converge (both key by
	// the cleaned absolute form).
	outsideAbs := outside + "/x.txt"
	outsideAlias := outside + "/deep/../x.txt"
	if LedgerKey(root, outsideAbs) != LedgerKey(root, outsideAlias) {
		t.Fatalf("out-of-root absolute and its alias did not converge: %q vs %q", LedgerKey(root, outsideAbs), LedgerKey(root, outsideAlias))
	}
	// An out-of-root absolute does NOT collide with an in-root relative that
	// happens to share a tail (they are different files).
	if LedgerKey(root, outsideAbs) == LedgerKey(root, "x.txt") {
		t.Fatalf("out-of-root absolute collided with in-root relative %q", "x.txt")
	}
}

package osfs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// osfs_vetrelaxedparent_test.go pins the Wave-1 panel-review follow-up
// AC-W2-F2 (docs/acceptance/path-escape-posture.md): vetRelaxedParent — the
// containment vet the relaxed read/write roots run on the verbatim parent
// dir — delegates each component to the already-extracted Canonicalize rather
// than hand-rolling a THIRD ancestor-resolution algorithm. These cases pin
// the behavioural contract: every canonical component must remain at or below
// its canonical predecessor.

func TestPathEscapePosture_VetRelaxedParentSharesCanonicalize(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures require a POSIX filesystem")
	}
	base := t.TempDir()
	realdir := filepath.Join(base, "real")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{realdir, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", d, err)
		}
	}
	// A symlinked component inside the verbatim path whose target escapes it.
	if err := os.Symlink(outside, filepath.Join(realdir, "link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	for _, tc := range []struct {
		label  string
		parent string
		want   bool
	}{
		{"existing real dir", realdir, true},
		{"existing real dir with a dirty form", filepath.Join(realdir, ".", "..", "real"), true},
		{"not-yet-existing tail under a real dir", filepath.Join(realdir, "no-such-dir", "deeper"), true},
		{"the fs root itself", string(filepath.Separator), true},
		{"a symlinked component escaping the verbatim prefix", filepath.Join(realdir, "link"), false},
		{"a symlinked component escaping deeper", filepath.Join(realdir, "link", "sub"), false},
	} {
		if got := vetRelaxedParent(tc.parent); got != tc.want {
			t.Errorf("%s: vetRelaxedParent(%q) = %v, want %v", tc.label, tc.parent, got, tc.want)
		}
	}
}

func TestPathEscapePosture_VetRelaxedParentAcceptsDarwinSystemAlias(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS system-symlink regression")
	}

	lexical := t.TempDir()
	canonical, err := Canonicalize("", lexical)
	if err != nil {
		t.Fatalf("Canonicalize(%q): %v", lexical, err)
	}
	if canonical == filepath.Clean(lexical) {
		t.Skipf("temporary directory %q does not traverse a system symlink", lexical)
	}

	for _, parent := range []string{
		lexical,
		canonical,
		filepath.Join(lexical, "not-yet-existing", "tail"),
		filepath.Join(canonical, "not-yet-existing", "tail"),
	} {
		if !vetRelaxedParent(parent) {
			t.Errorf("vetRelaxedParent(%q) = false; lexical and canonical system paths must agree", parent)
		}
	}
}

package osfs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// osfs_vetrelaxedparent_test.go pins the Wave-1 panel-review follow-up
// AC-W2-F2 (docs/acceptance/path-escape-posture.md): vetRelaxedParent — the
// containment vet the relaxed read/write roots run on the verbatim parent
// dir — delegates to the already-extracted Canonicalize rather than
// hand-rolling a THIRD ancestor walk, so a future semantic change to the
// canonicalization algorithm cannot drift the containment check. These
// cases pin the behavioural contract the delegation must preserve:
// vet == "Canonicalize(parent) stays under the verbatim cleaned prefix".

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
		// The delegation contract: vet must AGREE with the Canonicalize-based
		// containment check it now shares — canonicalize the parent, compare
		// against the verbatim cleaned prefix (the prefix of the verbatim path
		// that survives canonicalization; a resolved ancestor jumping above it
		// means a symlinked component escaped the verbatim path).
		canon, err := Canonicalize("", tc.parent)
		cleaned := filepath.Clean(tc.parent)
		shared := err == nil && (canon == cleaned ||
			(len(canon) < len(cleaned) && strings.HasPrefix(cleaned, canon+string(filepath.Separator))))
		if got := vetRelaxedParent(tc.parent); got != shared {
			t.Errorf("%s: vetRelaxedParent(%q) = %v, but the shared Canonicalize containment check = %v — the two must never disagree", tc.label, tc.parent, got, shared)
		}
	}
}

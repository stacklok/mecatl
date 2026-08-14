package osfs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// osfs_canonicalize_test.go covers the I/O-light canonicalization helpers the
// path-escape-posture composition classifier consumes. They are the same
// algorithms resolveInRoot and os.Root's lexical gate run, exported without
// opening an *os.Root so the classifier cannot disagree with the tool body.
func TestCanonicalize_MatchesResolveInRoot(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures require a POSIX filesystem")
	}
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "b.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "insymlink")); err != nil {
		t.Fatal(err)
	}
	canonRoot, err := ResolveRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		label     string
		base      string
		path      string
		wantUnder bool
	}{
		{"absolute in-root", "", filepath.Join(canonRoot, "a.txt"), true},
		{"relative in-root against base", canonRoot, "a.txt", true},
		{"absolute outside", "", filepath.Join(outside, "b.txt"), false},
		{"not-yet-existing in-root tail", canonRoot, "newdir/f.txt", true},
		{"in-root symlink escaping", canonRoot, "insymlink/b.txt", false},
	} {
		canon, err := Canonicalize(tc.base, tc.path)
		if err != nil {
			t.Fatalf("%s: Canonicalize(%q): %v", tc.label, tc.path, err)
		}
		under := canon == canonRoot || strings.HasPrefix(canon, canonRoot+string(filepath.Separator))
		if under != tc.wantUnder {
			t.Fatalf("%s: Canonicalize(%q) = %q, under-root=%v, want %v", tc.label, tc.path, canon, under, tc.wantUnder)
		}
	}
}

func TestLocalizeInRoot_LexicalGate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		path string
		ok   bool
	}{
		{"a.txt", true},
		{"sub/dir/f.txt", true},
		{".", true},
		{"sub/../a.txt", true},
		{"../escape.txt", false},
		{"../../deep/escape", false},
		{"/abs/path", false},
	} {
		rel, ok := LocalizeInRoot(tc.path)
		if ok != tc.ok {
			t.Fatalf("LocalizeInRoot(%q) ok=%v (rel=%q), want %v", tc.path, ok, rel, tc.ok)
		}
	}
}

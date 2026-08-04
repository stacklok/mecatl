package osfs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// osfs_canonicalize_test.go covers the I/O-light canonicalization helpers the
// path-escape-posture composition classifier consumes (Canonicalize,
// LocalizeInRoot, MatchReadRoot): they are the SAME algorithms resolveInRoot,
// os.Root's lexical gate, and allowedReadRoot run, exported WITHOUT opening an
// *os.Root, so the classifier can never disagree with the tool body.

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
		// The exact resolveInRoot containment comparison.
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
		{"sub/../a.txt", true},       // lexically stays in-root
		{"../escape.txt", false},     // climbs out: os.Root would refuse
		{"../../deep/escape", false}, // climbs out
		{"/abs/path", false},         // absolute: not a relative operand
	} {
		rel, ok := LocalizeInRoot(tc.path)
		if ok != tc.ok {
			t.Fatalf("LocalizeInRoot(%q) ok=%v (rel=%q), want %v", tc.path, ok, rel, tc.ok)
		}
	}
}

func TestMatchReadRoot_Lexical(t *testing.T) {
	t.Parallel()
	// The root must NOT traverse a top-level system symlink: MatchReadRoot
	// normalizes the PATH's top-level alias (macOS /var -> /private/var, Fedora
	// /srv -> /var/srv) but compares against the root VERBATIM — production
	// roots are canonical by construction (WithReadRoots canonicalizes at
	// construction; issue #356). A lexical root under an aliased top-level
	// component would mismatch its own paths on such hosts.
	root := filepath.Join(t.TempDir(), "skills", "demo")
	for _, tc := range []struct {
		path string
		ok   bool
	}{
		{root, true},
		{filepath.Join(root, "SKILL.md"), true},
		{filepath.Join(root, "..", "demo", "SKILL.md"), true}, // cleaned lexical containment
		{root + "2/SKILL.md", false},                          // prefix-but-not-contained
		{filepath.Dir(root), false},                           // parent of the root
		{"/etc/passwd", false},
		{"demo/SKILL.md", false}, // relative: never matches
		{filepath.Join(filepath.Dir(root), "..", "etc", "passwd"), false},
	} {
		if got := MatchReadRoot(tc.path, []string{root}); got != tc.ok {
			t.Fatalf("MatchReadRoot(%q) = %v, want %v", tc.path, got, tc.ok)
		}
	}
	if MatchReadRoot(filepath.Join(root, "x"), nil) {
		t.Fatal("no roots configured: MatchReadRoot must be false")
	}
}

// TestMatchReadRoot_TopLevelAlias pins the issue-#356 contract: when a
// top-level component is a system symlink (Fedora /srv -> /var/srv, macOS
// /var -> /private/var), a path written through the ALIAS matches a root
// stored in CANONICAL form (the production shape — read roots are
// canonicalized at construction), and a root written through the alias does
// NOT match canonical paths (roots must be canonical; the matcher does not
// normalize them). Runs only on hosts with a top-level alias in /'s
// resolution; skips where the probed directories are already canonical.
func TestMatchReadRoot_TopLevelAlias(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures require a POSIX filesystem")
	}
	// The host-dependency that broke #356 lives at the TOP of the tree (/srv,
	// /var, /home), so probe known candidates for a top-level alias. The
	// matcher needs no fixture on disk — the alias resolution itself is the
	// behavior under test — so use a path that need not exist.
	var lexicalBase, canonicalBase string
	for _, candidate := range []string{"/srv", "/var", "/home", "/private/var"} {
		info, err := os.Lstat(candidate)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil || resolved == filepath.Clean(candidate) {
			continue
		}
		lexicalBase = filepath.Join(candidate, "mecatl-test-alias")
		canonicalBase = filepath.Join(resolved, "mecatl-test-alias")
		break
	}
	if lexicalBase == "" {
		t.Skip("no top-level system alias found on this host")
	}

	canonicalRoot := filepath.Join(canonicalBase, "skills", "demo")
	lexicalRoot := filepath.Join(lexicalBase, "skills", "demo")

	// Production shape: canonical root, alias-written path -> match.
	if !MatchReadRoot(lexicalRoot, []string{canonicalRoot}) {
		t.Errorf("MatchReadRoot(alias path %q, canonical root %q) = false, want true", lexicalRoot, canonicalRoot)
	}
	if !MatchReadRoot(filepath.Join(lexicalRoot, "SKILL.md"), []string{canonicalRoot}) {
		t.Errorf("MatchReadRoot(alias path file, canonical root) = false, want true")
	}
	// The #356 asymmetry: a LEXICAL (alias-written) root must not silently
	// match canonical paths — roots are canonical by construction.
	if MatchReadRoot(canonicalRoot, []string{lexicalRoot}) {
		t.Errorf("MatchReadRoot(canonical path %q, lexical root %q) = true, want false (roots must be canonical)", canonicalRoot, lexicalRoot)
	}
}

func TestMatchReadRoot_DarwinSystemAlias(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS system-symlink regression")
	}
	lexicalBase := t.TempDir()
	canonicalBase, err := filepath.EvalSymlinks(lexicalBase)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", lexicalBase, err)
	}
	if canonicalBase == filepath.Clean(lexicalBase) {
		t.Skipf("temporary directory %q does not traverse a system symlink", lexicalBase)
	}

	lexicalRoot := filepath.Join(lexicalBase, "skills")
	canonicalRoot := filepath.Join(canonicalBase, "skills")
	for _, path := range []string{
		lexicalRoot,
		filepath.Join(lexicalRoot, "SKILL.md"),
		canonicalRoot,
		filepath.Join(canonicalRoot, "SKILL.md"),
	} {
		if !MatchReadRoot(path, []string{canonicalRoot}) {
			t.Errorf("MatchReadRoot(%q, canonical root %q) = false", path, canonicalRoot)
		}
	}
}

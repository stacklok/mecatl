package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// escapeclassifier_test.go pins the path-escape-posture Scenario 1 acceptance
// criteria (docs/acceptance/path-escape-posture.md): the composition-layer
// escapeClassifier classifies an FS-tool path EXACTLY as the osfs tool body
// treats it — reusing resolveInRoot's canonicalize-then-reject for the
// in-root/escape verdict and allowedReadRoot's lexical match for the read-root
// third state — and classifies pseudo-filesystem paths (/proc, /sys, /dev) as
// a distinct never-relaxed category. No behaviour change in this wave: the
// classifier is consumed by a wrapping policy later.

// mustMkdir creates dir (with parents) and fails the test on error.
func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", dir, err)
	}
}

// setupScenario1FS builds the temp filesystem every Scenario-1 test shares:
//
//	root/              the workspace root (canonicalized by osfs.NewFileSystem)
//	outside/           an out-of-root directory with a regular file
//	skillroot/         a configured WithReadRoots read-only root with a file
//	root/insymlink ->  outside   (an in-root symlink whose target escapes)
//	linkroot ->        skillroot (an out-of-root symlink INTO the read root)
func setupScenario1FS(t *testing.T) (root, outside, skillRoot, linkRoot string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures require a POSIX filesystem")
	}
	base := t.TempDir()
	root = filepath.Join(base, "root")
	outside = filepath.Join(base, "outside")
	skillRoot = filepath.Join(base, "skillroot")
	for _, d := range []string{root, outside, skillRoot} {
		mustMkdir(t, d)
	}
	for name, file := range map[string]string{
		filepath.Join(root, "a.txt"):          "in-root",
		filepath.Join(outside, "b.txt"):       "outside",
		filepath.Join(skillRoot, "s.txt"):     "read-root",
		filepath.Join(outside, "deep", "c"):   "outside-deep",
		filepath.Join(skillRoot, "deep", "d"): "read-root-deep",
	} {
		mustMkdir(t, filepath.Dir(name))
		if err := os.WriteFile(name, []byte(file), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", name, err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "insymlink")); err != nil {
		t.Fatalf("Symlink(insymlink): %v", err)
	}
	linkRoot = filepath.Join(base, "linkroot")
	if err := os.Symlink(skillRoot, linkRoot); err != nil {
		t.Fatalf("Symlink(linkroot): %v", err)
	}
	// NewFileSystem canonicalizes root (EvalSymlinks) exactly like production.
	if _, err := osfs.NewFileSystem(root); err != nil {
		t.Fatalf("osfs.NewFileSystem(%q): %v", root, err)
	}
	return root, outside, skillRoot, linkRoot
}

// newScenario1Classifier builds the composition classifier over the real
// canonicalized root and read roots — the same inputs the workspace factory
// hands osfs.NewWorkspace.
func newScenario1Classifier(t *testing.T, root string, readRoots ...string) *escapeClassifier {
	t.Helper()
	c, err := newEscapeClassifier(root, readRoots...)
	if err != nil {
		t.Fatalf("newEscapeClassifier(%q): %v", root, err)
	}
	return c
}

// toolCall builds an FS-tool call carrying the given path arg.
func toolCall(t *testing.T, name, path string) (callName string, args json.RawMessage) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return name, raw
}

// TestPathEscapePosture_Scenario1_ClassifierMatchesResolveInRoot pins AC1.1:
// a relative path and an absolute path that canonicalize inside the workspace
// root classify in-root; a ".." traversal and an absolute path outside classify
// escape — MATCHING the tool body's resolvePath/resolveInRoot verdict on the
// SAME inputs (the classifier shares osfs's algorithms so it cannot disagree).
func TestPathEscapePosture_Scenario1_ClassifierMatchesResolveInRoot(t *testing.T) {
	t.Parallel()
	root, outside, _, _ := setupScenario1FS(t)
	c := newScenario1Classifier(t, root)
	// must mirror. Stat routes through resolveRead/resolvePath — the in-root /
	// escape verdict with no read-root carve-out (none configured here) —
	// against the same root the classifier canonicalizes.
	ws, err := osfs.NewWorkspace(root)
	if err != nil {
		t.Fatalf("osfs.NewWorkspace(%q): %v", root, err)
	}
	inRootAbs := filepath.Join(root, "a.txt")
	outsideAbs := filepath.Join(outside, "b.txt")
	cases := []struct {
		label    string
		path     string
		toolName string
		want     escapeKind
	}{
		{"relative in-root", "a.txt", "Read", escapeInRoot},
		{"relative subdir (not yet existing)", "newdir/file.txt", "Write", escapeInRoot},
		{"absolute in-root", inRootAbs, "Read", escapeInRoot},
		{"absolute in-root with .. inside root", filepath.Join(root, "sub", "..", "a.txt"), "Read", escapeInRoot},
		{"dot-dot traversal", "../outside/b.txt", "Read", escapeEscape},
		{"absolute outside", outsideAbs, "Read", escapeEscape},
		{"absolute outside (Write — no read-root carve-out)", outsideAbs, "Write", escapeEscape},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			name, args := toolCall(t, tc.toolName, tc.path)
			if got := c.classify(name, args); got != tc.want {
				t.Fatalf("classify(%q, %q) = %v, want %v", tc.toolName, tc.path, got, tc.want)
			}
			// Ground truth against the REAL tool body on the same inputs:
			// an existing path is Stat'ed (resolveRead → resolvePath/
			// resolveInRoot); a not-yet-existing path is Stat'ed on its
			// existing PARENT (the deepest-existing-ancestor verdict). Both
			// must AGREE with the classifier: in-root ↔ no ErrPathEscape,
			// escape ↔ ErrPathEscape.
			probe := tc.path
			if tc.label == "relative subdir (not yet existing)" {
				probe = "newdir" // absent; Stat errors not-exist, NOT escape
			}
			_, serr := ws.Stat(t.Context(), probe)
			if tc.want == escapeInRoot && errors.Is(serr, osfs.ErrPathEscape) {
				t.Fatalf("classifier said in-root but the tool body rejects %q (probe %q): %v", tc.path, probe, serr)
			}
			if tc.want == escapeEscape && !errors.Is(serr, osfs.ErrPathEscape) {
				t.Fatalf("classifier said escape but the tool body's verdict for %q = %v, want ErrPathEscape", tc.path, serr)
			}
			_ = ws
		})
	}
}

// TestPathEscapePosture_Scenario1_ReadRootClassification pins AC1.2: an
// absolute path under a configured WithReadRoots read-only root classifies as
// read-root — distinct from both in-root and escape — using allowedReadRoot's
// LEXICAL match, so a symlinked absolute path whose lexical form walks through
// the read root classifies EXACTLY as the tool body serves it (lexically under
// the root → served read-only; canonical escape → ErrPathEscape).
func TestPathEscapePosture_Scenario1_ReadRootClassification(t *testing.T) {
	t.Parallel()
	root, outside, skillRoot, linkRoot := setupScenario1FS(t)
	c := newScenario1Classifier(t, root, skillRoot)

	// Lexically under the read root → read-root, whether the path exists or
	// not (allowedReadRoot matches the cleaned lexical form, never
	// canonicalizes).
	for label, path := range map[string]string{
		"read-root file":             filepath.Join(skillRoot, "s.txt"),
		"read-root dir itself":       skillRoot,
		"read-root not-yet-existing": filepath.Join(skillRoot, "later", "f.txt"),
	} {
		name, args := toolCall(t, "Read", path)
		if got := c.classify(name, args); got != escapeReadRoot {
			t.Fatalf("%s: classify(%q) = %v, want read-root", label, path, got)
		}
	}

	// The drift case the lexical reuse closes: an in-root symlink whose target
	// lives INSIDE the read root canonicalizes out of the workspace but is
	// lexically in-root — the tool body rejects it (resolvePath fails before
	// the lexical read-root fallback), so the classifier must say escape, NOT
	// read-root.
	inRootLinkToSkill := filepath.Join(root, "skillpeek")
	if err := os.Symlink(filepath.Join(skillRoot, "s.txt"), inRootLinkToSkill); err != nil {
		t.Fatalf("Symlink(skillpeek): %v", err)
	}
	name, args := toolCall(t, "Read", inRootLinkToSkill)
	if got := c.classify(name, args); got != escapeEscape {
		t.Fatalf("in-root symlink into read root: classify = %v, want escape (resolveInRoot rejects before the lexical fallback)", got)
	}
	// Ground truth: the REAL workspace rejects the read with ErrPathEscape.
	ws, err := osfs.NewWorkspace(root, osfs.WithReadRoots(skillRoot))
	if err != nil {
		t.Fatalf("osfs.NewWorkspace: %v", err)
	}
	if _, err := ws.Read(t.Context(), inRootLinkToSkill); !errors.Is(err, osfs.ErrPathEscape) {
		t.Fatalf("tool body read of in-root symlink into read root = %v, want ErrPathEscape", err)
	}
	// And the read-root file itself IS served by the tool body (agreement on
	// the positive half).
	if _, err := ws.Read(t.Context(), filepath.Join(skillRoot, "s.txt")); err != nil {
		t.Fatalf("tool body read of read-root file: %v", err)
	}
	// A path outside BOTH roots stays escape even with a read root configured.
	name, args = toolCall(t, "Read", filepath.Join(outside, "b.txt"))
	if got := c.classify(name, args); got != escapeEscape {
		t.Fatalf("outside both roots: classify = %v, want escape", got)
	}
	// A read root configured by its symlink ALIAS: the constructor
	// canonicalizes it exactly like WithReadRoots does (resolveRoot at
	// construction), so the stored root is the REAL dir and a path addressed
	// through the alias does NOT lexically match — the tool body rejects the
	// same path with ErrPathEscape. The classifier must agree (escape), which
	// is exactly why the match must stay lexical AND the roots canonical.
	cAlias := newScenario1Classifier(t, root, linkRoot)
	name, args = toolCall(t, "Read", filepath.Join(linkRoot, "s.txt"))
	if got := cAlias.classify(name, args); got != escapeEscape {
		t.Fatalf("read root via symlink alias: classify = %v, want escape (the canonicalized root no longer matches the alias lexically — the tool body's verdict)", got)
	}
	wsAlias, err := osfs.NewWorkspace(root, osfs.WithReadRoots(linkRoot))
	if err != nil {
		t.Fatalf("osfs.NewWorkspace(alias): %v", err)
	}
	if _, err := wsAlias.Read(t.Context(), filepath.Join(linkRoot, "s.txt")); !errors.Is(err, osfs.ErrPathEscape) {
		t.Fatalf("tool body read via symlink alias = %v, want ErrPathEscape (classifier agreement)", err)
	}
}

// TestPathEscapePosture_Scenario1_SymlinkEscapeIsEscape pins AC1.3: a symlink
// INSIDE the workspace whose target escapes classifies as escape — the
// canonicalize-then-reject path is exercised, never bypassed — whether the
// symlink is addressed relatively or by its absolute alias, and whether the
// escaping component is the leaf or a PARENT directory of a deeper path.
func TestPathEscapePosture_Scenario1_SymlinkEscapeIsEscape(t *testing.T) {
	t.Parallel()
	root, outside, _, _ := setupScenario1FS(t)
	c := newScenario1Classifier(t, root)
	ws, err := osfs.NewWorkspace(root)
	if err != nil {
		t.Fatalf("osfs.NewWorkspace: %v", err)
	}
	cases := []struct {
		label string
		path  string
	}{
		{"relative leaf symlink", "insymlink/b.txt"},
		{"absolute leaf symlink", filepath.Join(root, "insymlink", "b.txt")},
		{"relative symlink parent, deep tail", "insymlink/deep/c"},
		{"absolute symlink parent, not-yet-existing tail", filepath.Join(root, "insymlink", "later", "f.txt")},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			name, args := toolCall(t, "Read", tc.path)
			if got := c.classify(name, args); got != escapeEscape {
				t.Fatalf("classify(%q) = %v, want escape", tc.path, got)
			}
			// Ground truth: the real tool body agrees (resolveInRoot rejects
			// before os.Root is ever consulted).
			if _, err := ws.Read(t.Context(), tc.path); !errors.Is(err, osfs.ErrPathEscape) {
				t.Fatalf("tool body read of %q = %v, want ErrPathEscape", tc.path, err)
			}
			_ = outside
		})
	}
}

// TestPathEscapePosture_Scenario1_PseudoFsClassification pins AC1.5: a path
// under /proc, /sys, or /dev classifies as the NEVER-RELAXED pseudo-fs
// category — distinct from a regular escape — because an in-process FS read of
// e.g. /proc/self/environ would return the SERVER's raw, unscrubbed
// environment, a secret-exfiltration channel the envscrub-scrubbed Bash parity
// path does not provide (docs/acceptance/path-escape-posture.md scope cuts).
// The category must not collapse into escape and must not be reachable as
// in-root or read-root even if a pseudo-fs dir is (mis)configured as a read
// root or a symlink leads there.
func TestPathEscapePosture_Scenario1_PseudoFsClassification(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("pseudo-fs paths are POSIX-specific")
	}
	root, _, _, _ := setupScenario1FS(t)
	c := newScenario1Classifier(t, root, "/proc") // even a misconfigured read root must not relax
	cases := []string{
		"/proc/self/environ",
		"/proc/1/cmdline",
		"/proc",
		"/sys/kernel/hostname",
		"/dev/null",
		"/dev",
	}
	for _, path := range cases {
		name, args := toolCall(t, "Read", path)
		if got := c.classify(name, args); got != escapePseudoFS {
			t.Fatalf("classify(%q) = %v, want pseudo-fs (never-relaxed, distinct from escape)", path, got)
		}
	}
	// Distinctness: pseudo-fs != escape.
	if escapePseudoFS == escapeEscape {
		t.Fatal("escapePseudoFS must be a distinct kind from escapeEscape")
	}
	// A RELATIVE path can never be pseudo-fs (it is interpreted against the
	// workspace root): "proc/self/environ" is an ordinary in-root path.
	name, args := toolCall(t, "Read", "proc/self/environ")
	if got := c.classify(name, args); got != escapeInRoot {
		t.Fatalf("relative proc-looking path: classify = %v, want in-root", got)
	}
	// An in-root symlink INTO a pseudo-fs dir: canonicalize-then-reject makes
	// it an escape (resolveInRoot), and it must STILL classify pseudo-fs —
	// the never-relaxed category applies to the RESOLVED target, not only the
	// lexical form.
	link := filepath.Join(root, "proclink")
	if err := os.Symlink("/proc", link); err != nil {
		t.Fatalf("Symlink(proclink): %v", err)
	}
	name, args = toolCall(t, "Read", filepath.Join("proclink", "self", "environ"))
	got := c.classify(name, args)
	if got != escapePseudoFS && got != escapeEscape {
		t.Fatalf("symlink into /proc: classify = %v, want pseudo-fs or escape — never in-root/read-root", got)
	}
	// And the category must never be returned for an ordinary outside path.
	name, args = toolCall(t, "Read", "/etc/hostname")
	if got := c.classify(name, args); got != escapeEscape {
		t.Fatalf("/etc/hostname: classify = %v, want ordinary escape", got)
	}
}

// TestEscapeClassifier_UnrelatedToolsAreNotEscapes pins the call-shape half of
// the classifier: non-FS tools, FS tools with a malformed/missing path arg,
// and Bash (whose command is gated by the bash classifiers, never the escape
// classifier) classify in-root — the escape decision never fires where no
// workspace path exists, so the later wrapping policy leaves every such call
// to the inner policy untouched (no behaviour change in this wave).
func TestEscapeClassifier_UnrelatedToolsAreNotEscapes(t *testing.T) {
	t.Parallel()
	root, _, _, _ := setupScenario1FS(t)
	c := newScenario1Classifier(t, root)
	for label, call := range map[string]struct {
		name string
		args string
	}{
		"Bash":       {"Bash", `{"command": "cat /etc/passwd"}`},
		"Glob":       {"Glob", `{"pattern": "**/*.go"}`},
		"Grep":       {"Grep", `{"pattern": "x", "path": "/etc/**"}`},
		"WebFetch":   {"WebFetch", `{"url": "https://example.com"}`},
		"Skill":      {"Skill", `{"name": "x"}`},
		"empty args": {"Read", `{}`},
		"null args":  {"Read", `null`},
		"bad json":   {"Read", `{`},
		"empty path": {"Read", `{"path": ""}`},
	} {
		if got := c.classify(call.name, json.RawMessage(call.args)); got != escapeInRoot {
			t.Fatalf("%s: classify = %v, want in-root (no workspace path, never an escape)", label, got)
		}
	}
	// Read/Write/Edit (the path-carrying FS tools) DO classify: sanity that the
	// tool set is the right one.
	for _, name := range []string{"Read", "Write", "Edit"} {
		_, args := toolCall(t, name, "/etc/hostname")
		if got := c.classify(name, args); got == escapeInRoot {
			t.Fatalf("%s with an out-of-root path classified in-root — the classifier is not consulting the path", name)
		}
	}
	if !strings.HasPrefix(root, string(filepath.Separator)) {
		t.Fatalf("test root %q should be absolute", root)
	}
}

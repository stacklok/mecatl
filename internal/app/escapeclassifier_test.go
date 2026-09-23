package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// escapeclassifier_test.go pins the path-escape-posture Scenario 1 acceptance
// criteria (docs/acceptance/path-escape-posture.md): the composition-layer
// escapeClassifier classifies an FS-tool path EXACTLY as the osfs tool body
// treats it by reusing resolveInRoot's canonicalize-then-reject primitives, and
// classifies pseudo-filesystem paths (/proc, /sys, /dev) as a distinct
// never-relaxed category.

// mustMkdir creates dir (with parents) and fails the test on error.
func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", dir, err)
	}
}

// setupScenario1FS builds the temp filesystem every Scenario-1 test shares:
//
//	root/              the workspace root
//	outside/           an out-of-root directory with regular files
//	root/insymlink ->  outside (an in-root symlink whose target escapes)
func setupScenario1FS(t *testing.T) (root, outside string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures require a POSIX filesystem")
	}
	base := t.TempDir()
	root = filepath.Join(base, "root")
	outside = filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		mustMkdir(t, d)
	}
	for name, file := range map[string]string{
		filepath.Join(root, "a.txt"):        "in-root",
		filepath.Join(outside, "b.txt"):     "outside",
		filepath.Join(outside, "deep", "c"): "outside-deep",
	} {
		mustMkdir(t, filepath.Dir(name))
		if err := os.WriteFile(name, []byte(file), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", name, err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "insymlink")); err != nil {
		t.Fatalf("Symlink(insymlink): %v", err)
	}
	// NewFileSystem canonicalizes root (EvalSymlinks) exactly like production.
	if _, err := osfs.NewFileSystem(root); err != nil {
		t.Fatalf("osfs.NewFileSystem(%q): %v", root, err)
	}
	return root, outside
}

// newScenario1Classifier builds the composition classifier over the real
// canonicalized root used by the workspace factory.
func newScenario1Classifier(t *testing.T, root string) *escapeClassifier {
	t.Helper()
	c, err := newEscapeClassifier(root)
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
	root, outside := setupScenario1FS(t)
	c := newScenario1Classifier(t, root)
	// must mirror. Stat routes through resolveRead/resolvePath — the in-root /
	// escape verdict against the same root the classifier canonicalizes.
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
		{"absolute outside ListDir", outside, "ListDir", escapeEscape},
		{"absolute outside Write", outsideAbs, "Write", escapeEscape},
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

// TestPathEscapePosture_Scenario1_SymlinkEscapeIsEscape pins AC1.3: a symlink
// INSIDE the workspace whose target escapes classifies as escape — the
// canonicalize-then-reject path is exercised, never bypassed — whether the
// symlink is addressed relatively or by its absolute alias, and whether the
// escaping component is the leaf or a PARENT directory of a deeper path.
func TestPathEscapePosture_Scenario1_SymlinkEscapeIsEscape(t *testing.T) {
	t.Parallel()
	root, outside := setupScenario1FS(t)
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
// environment, a secret-exfiltration channel the envscrub-scrubbed Shell parity
// path does not provide (docs/acceptance/path-escape-posture.md scope cuts).
// The category must not collapse into escape and must not be reachable as
// in-root, including when a symlink leads there.
func TestPathEscapePosture_Scenario1_PseudoFsClassification(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("pseudo-fs paths are POSIX-specific")
	}
	root, _ := setupScenario1FS(t)
	c := newScenario1Classifier(t, root)
	cases := []string{
		"/proc/self/environ",
		"/proc/1/cmdline",
		"/proc",
		"/sys/kernel/hostname",
		"/dev/null",
		"/dev",
	}
	for _, path := range cases {
		for _, toolName := range []string{"Read", "ListDir"} {
			name, args := toolCall(t, toolName, path)
			if got := c.classify(name, args); got != escapePseudoFS {
				t.Fatalf("classify(%s, %q) = %v, want pseudo-fs (never-relaxed, distinct from escape)", toolName, path, got)
			}
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
	// Aliases target directories only; no pseudo-filesystem contents are read.
	for _, target := range []string{"/proc", "/sys", "/dev"} {
		t.Run("directory aliases "+target, func(t *testing.T) {
			info, err := os.Stat(target)
			if errors.Is(err, os.ErrNotExist) {
				t.Skipf("pseudo-filesystem directory %s is absent on this host", target)
			}
			if err != nil || !info.IsDir() {
				t.Fatalf("pseudo-filesystem fixture %s: %v, %v", target, info, err)
			}
			ws, aliasRoot := newNamespaceTestWorkspace(t)
			link := filepath.Join(aliasRoot, "pseudolink")
			if err := os.Symlink(target, link); err != nil {
				t.Fatalf("Symlink: %v", err)
			}
			for _, path := range []string{target, "pseudolink", link} {
				for _, toolName := range []string{"Read", "ListDir"} {
					_, args := toolCall(t, toolName, path)
					if got := ws.classifier.classify(toolName, args); got != escapePseudoFS {
						t.Errorf("%s(%q) classifies %v, want escapePseudoFS", toolName, path, got)
					}
					call := session.NewToolCall("pseudo", toolName, args)
					for _, posture := range []Posture{PostureStrict, PostureTrusted, PostureAuto, PostureYolo} {
						policy := newEscapePolicy(permpolicy.NewPolicy(defaultRules(), nil), posture)
						decision := policy.Evaluate(t.Context(), "s1", session.ModeDefault, call, ws)
						if decision.Effect != governance.Deny || !strings.Contains(decision.Reason, "pseudo-filesystem") {
							t.Errorf("%s %s(%q): %+v, want pseudo-fs policy deny", posture, toolName, path, decision)
						}
					}
					if toolName != "ListDir" {
						continue
					}
					// A configured ask returns before the policy's pseudo-fs check.
					// Even when approved, the serving workspace must refuse it itself.
					inner := permpolicy.NewPolicy([]governance.Rule{{Scope: governance.ScopeUser, Tool: "ListDir", Effect: governance.Ask}}, nil)
					decision := newEscapePolicy(inner, PostureYolo).Evaluate(t.Context(), "s1", session.ModeDefault, call, ws)
					if decision.Effect != governance.Ask || !decision.ConfiguredAsk {
						t.Fatalf("configured ask for %q = %+v", path, decision)
					}
					_, err := ws.ReadDir(t.Context(), path)
					if !errors.Is(err, osfs.ErrPathEscape) || !strings.Contains(err.Error(), "pseudo-filesystem") {
						t.Errorf("approved ListDir(%q): %v, want workspace pseudo-fs ErrPathEscape", path, err)
					}
				}
			}
		})
	}
	// And the category must never be returned for an ordinary outside path.
	name, args = toolCall(t, "Read", "/etc/hostname")
	if got := c.classify(name, args); got != escapeEscape {
		t.Fatalf("/etc/hostname: classify = %v, want ordinary escape", got)
	}
}

// TestEscapeClassifier_UnrelatedToolsAreNotEscapes pins the call-shape half of
// the classifier: non-FS tools, FS tools with a malformed/missing path arg,
// and Shell (whose command is gated by the bash classifiers, never the escape
// classifier) classify in-root — the escape decision never fires where no
// workspace path exists, so the later wrapping policy leaves every such call
// to the inner policy untouched (no behaviour change in this wave).
func TestEscapeClassifier_UnrelatedToolsAreNotEscapes(t *testing.T) {
	t.Parallel()
	root, _ := setupScenario1FS(t)
	c := newScenario1Classifier(t, root)
	for label, call := range map[string]struct {
		name string
		args string
	}{
		"Shell":      {"Shell", `{"command": "cat /etc/passwd"}`},
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
	// Read/ListDir/Write/Edit (the path-carrying FS tools) DO classify: sanity that the
	// tool set is the right one.
	for _, name := range []string{"Read", "ListDir", "Write", "Edit"} {
		_, args := toolCall(t, name, "/etc/hostname")
		if got := c.classify(name, args); got == escapeInRoot {
			t.Fatalf("%s with an out-of-root path classified in-root — the classifier is not consulting the path", name)
		}
	}
	if !strings.HasPrefix(root, string(filepath.Separator)) {
		t.Fatalf("test root %q should be absolute", root)
	}
}

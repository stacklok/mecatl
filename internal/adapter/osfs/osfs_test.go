package osfs_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/fsconformance"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/gitenv"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// TestConformance runs the shared Workspace conformance table against osfs.
func TestConformance(t *testing.T) {
	fsconformance.Run(t, func(t *testing.T) tool.Workspace {
		ws, err := osfs.NewWorkspace(t.TempDir())
		if err != nil {
			t.Fatalf("NewWorkspace: %v", err)
		}
		return ws
	})
}

// TestNamespaceConformance runs the shared WorkspaceNamespace conformance
// table (ReadDir/Remove/Rename/CopyFile) against osfs.
func TestNamespaceConformance(t *testing.T) {
	fsconformance.RunNamespace(t, func(t *testing.T) tool.Workspace {
		ws, err := osfs.NewWorkspace(t.TempDir())
		if err != nil {
			t.Fatalf("NewWorkspace: %v", err)
		}
		return ws
	})
}

// TestEmptyPhysicalDirectorySurvivesLastFileRemoval pins osfs's real-directory
// contract, the point where it diverges from memfs/redisstore's derived
// directories (see the tool.WorkspaceNamespace doc-comment): osfs directories
// are real physical inodes, so once a directory's last file is removed the
// now-empty directory itself still exists — ReadDir reports it empty, and a
// second Remove of it succeeds (it is a genuinely empty directory, not an
// absent one).
func TestEmptyPhysicalDirectorySurvivesLastFileRemoval(t *testing.T) {
	ctx := context.Background()
	ws, err := osfs.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if _, err := ws.CreateFile(ctx, "sub/only.txt", []byte("x")); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if err := ws.Remove(ctx, "sub/only.txt"); err != nil {
		t.Fatalf("Remove(sub/only.txt): %v", err)
	}
	entries, err := ws.ReadDir(ctx, "sub")
	if err != nil {
		t.Fatalf("ReadDir(now-empty physical dir) = %v, want the directory to still exist", err)
	}
	if len(entries) != 0 {
		t.Fatalf("ReadDir(now-empty physical dir) = %v, want empty", entries)
	}
	if err := ws.Remove(ctx, "sub"); err != nil {
		t.Fatalf("Remove(now-empty physical dir) = %v, want success", err)
	}
}

// --- osfs CommandRunner tests (real shell, under t.TempDir) ---

func newRunner(t *testing.T, dir string) tool.CommandRunner {
	t.Helper()
	r, err := osfs.NewCommandRunner(dir)
	if err != nil {
		t.Fatalf("NewCommandRunner: %v", err)
	}
	return r
}

func TestCommandRunnerRun(t *testing.T) {
	ctx := context.Background()
	r := newRunner(t, t.TempDir())

	res, err := r.Run(ctx, "echo hi && echo err >&2")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "hi" {
		t.Errorf("Stdout = %q want hi", res.Stdout)
	}
	if strings.TrimSpace(res.Stderr) != "err" {
		t.Errorf("Stderr = %q want err", res.Stderr)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d want 0", res.ExitCode)
	}
}

func TestCommandRunnerExitCode(t *testing.T) {
	ctx := context.Background()
	r := newRunner(t, t.TempDir())
	res, err := r.Run(ctx, "exit 3")
	if err != nil {
		t.Fatalf("Run returned harness error for non-zero exit: %v", err)
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d want 3", res.ExitCode)
	}
}

func TestCommandRunnerWorkingDir(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "marker.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed marker: %v", err)
	}
	r := newRunner(t, root)
	res, err := r.Run(ctx, "ls")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Stdout, "marker.txt") {
		t.Errorf("ls output %q does not contain marker.txt (wrong cwd?)", res.Stdout)
	}
}

// TestCommandRunnerBoundNamespace proves the runner is BOUND to a single
// namespace root at construction (issue #462): a runner minted against a
// directory runs its commands there, and a SEPARATE runner minted against a
// different directory runs its commands THERE — never the two crossing via a
// per-call workdir. This is the fork-isolation property the Environment seam
// relies on: a forked child gets its OWN runner bound to the child namespace,
// so its Bash observes the child tree (not the parent's), and a relative write
// lands in the runner's bound root.
func TestCommandRunnerBoundNamespace(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "base.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed base marker: %v", err)
	}
	// A SEPARATE directory, not under base.
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "other.txt"), []byte("y"), 0o644); err != nil {
		t.Fatalf("seed other marker: %v", err)
	}

	// A runner bound to base runs in base.
	baseR := newRunner(t, base)
	res, err := baseR.Run(ctx, "ls")
	if err != nil {
		t.Fatalf("base runner Run: %v", err)
	}
	if !strings.Contains(res.Stdout, "base.txt") || strings.Contains(res.Stdout, "other.txt") {
		t.Errorf("base runner ls = %q; want base.txt (not other.txt)", res.Stdout)
	}

	// A relative write through the base runner lands in base, never leaking
	// into other — the fork-isolation property: each runner's writes stay in
	// its bound namespace.
	if _, err := baseR.Run(ctx, "echo hi > written.txt"); err != nil {
		t.Fatalf("base runner write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "written.txt")); err != nil {
		t.Errorf("relative write did not land in the bound root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "written.txt")); !os.IsNotExist(err) {
		t.Errorf("relative write leaked into the other namespace")
	}

	// A SEPARATE runner bound to other runs in other (the fork child case).
	otherR := newRunner(t, other)
	res, err = otherR.Run(ctx, "ls")
	if err != nil {
		t.Fatalf("other runner Run: %v", err)
	}
	if !strings.Contains(res.Stdout, "other.txt") || strings.Contains(res.Stdout, "base.txt") {
		t.Errorf("other runner ls = %q; want other.txt (not base.txt) — bound namespace not honored", res.Stdout)
	}
}

func TestCommandRunnerCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	r := newRunner(t, t.TempDir())
	_, err := r.Run(ctx, "echo hi")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run with cancelled ctx err = %v want context.Canceled", err)
	}
}

func TestCommandRunnerTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r := newRunner(t, t.TempDir())
	_, err := r.Run(ctx, "sleep 5")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run with expired deadline err = %v want context.DeadlineExceeded", err)
	}
}

// TestCommandRunnerWaitDelayUnblocksGrandchildPipeWait pins the A7 hardening: a
// GRANDCHILD that inherits the output pipes (`sleep 5 &`) used to park cmd.Wait
// on the pipe-copy goroutines until the grandchild exited (~5s here; up to the
// 30s default ctx timeout in general) even though the shell itself exited
// immediately. With cmd.WaitDelay set, Run returns once the delay elapses after
// the shell's exit — as a SUCCESS carrying the output captured so far (the
// exec.ErrWaitDelay sentinel is not a command failure).
func TestCommandRunnerWaitDelayUnblocksGrandchildPipeWait(t *testing.T) {
	r, err := osfs.NewCommandRunnerShell(t.TempDir(), "/bin/sh", osfs.WithCommandWaitDelay(200*time.Millisecond))
	if err != nil {
		t.Fatalf("NewCommandRunnerShell: %v", err)
	}
	start := time.Now()
	res, err := r.Run(context.Background(), "sleep 5 & echo started")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("a WaitDelay expiry on a successful command must be a success, got err = %v", err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "started") {
		t.Fatalf("captured output must survive the WaitDelay close, got exit=%d stdout=%q", res.ExitCode, res.Stdout)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("Run blocked %v on the grandchild's inherited pipe; WaitDelay must bound it", elapsed)
	}
}

func TestCommandRunnerEmptyShellRejected(t *testing.T) {
	if _, err := osfs.NewCommandRunnerShell(t.TempDir(), ""); err == nil {
		t.Fatal("NewCommandRunnerShell with empty shell = nil err, want error")
	}
}

// TestCommandRunnerWithCommandEnvList asserts the WithCommandEnvList option sets the
// COMPLETE process environment for every Run (it REPLACES os.Environ() rather than
// appending). This is the seam the composition root uses to hand the runner a fully
// scrubbed-and-neutralised environment: because it replaces, it can REMOVE an
// inherited dangerous variable (an append-only option could not). It checks that an
// injected var is present, that a var NOT in the list is absent (proving replacement,
// not augmentation), and that an inherited dangerous var is gone.
func TestCommandRunnerWithCommandEnvList(t *testing.T) {
	t.Setenv("GIT_EXTERNAL_DIFF", "/bin/evil") // inherited danger that must NOT survive
	r, err := osfs.NewCommandRunnerShell(t.TempDir(), "/bin/sh", osfs.WithCommandEnvList([]string{
		"GIT_PAGER=cat",
		"PATH=/usr/bin:/bin",
	}))
	if err != nil {
		t.Fatalf("NewCommandRunnerShell: %v", err)
	}
	ctx := context.Background()

	res, err := r.Run(ctx, "printf '%s' \"$GIT_PAGER\"")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "cat" {
		t.Errorf("GIT_PAGER = %q, want cat (injected env)", res.Stdout)
	}

	// GIT_EXTERNAL_DIFF was inherited but is NOT in the supplied list: replacement
	// semantics mean it must be GONE (an append-only env could not have removed it).
	res, err = r.Run(ctx, "printf '%s' \"$GIT_EXTERNAL_DIFF\"")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "" {
		t.Errorf("GIT_EXTERNAL_DIFF = %q, want empty (replaced env drops inherited danger)", res.Stdout)
	}

	// A var present in the list IS set; a var NOT present is unset (replacement).
	res, err = r.Run(ctx, "printf '%s' \"$PATH\"")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "/usr/bin:/bin" {
		t.Errorf("PATH = %q, want /usr/bin:/bin (replacement env, exact list)", res.Stdout)
	}
}

// TestSandboxedRunnerConfigOverride proves the env-injected git config (the kind
// buildSandboxedCommandRunner hands the runner via gitenv.Scrub) beats a repo-local
// .git/config setting — the actual threat, not merely "the env var is set". A temp
// git repo's repo-local config points a config-driven execution hook at a marker
// command that writes a sentinel; running git through the sandboxed runner must NOT
// fire the marker because the env-injected config takes precedence over .git/config.
//
// It uses TWO repo-local vectors:
//   - core.fsmonitor — a program git runs on `git status` UNCONDITIONALLY (no TTY
//     needed), giving a deterministic config→exec proof; the scrub injects
//     core.fsmonitor=false.
//   - core.pager via `git --paginate log` — the reviewer's named vector; the scrub
//     injects core.pager=cat (and GIT_PAGER=cat).
//
// A control sub-test first confirms the fsmonitor marker DOES fire under an
// unhardened environment, so the negative assertion proves neutralization rather
// than a no-op.
func TestSandboxedRunnerConfigOverride(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run("add", "f.txt")
	run("commit", "-q", "-m", "first")

	// Repo-local config sets a config-driven execution vector that writes a sentinel.
	fsmonSentinel := filepath.Join(t.TempDir(), "fsmonitor-fired")
	run("config", "core.fsmonitor", "touch "+fsmonSentinel+"; echo")
	pagerSentinel := filepath.Join(t.TempDir(), "pager-fired")
	run("config", "core.pager", "touch "+pagerSentinel)

	// Control: under an UNHARDENED env the fsmonitor program fires on `git status`,
	// proving the vector is genuinely reachable in this repo/git version.
	t.Run("control_fires_unhardened", func(t *testing.T) {
		ctrlMarker := filepath.Join(t.TempDir(), "ctrl-fired")
		run("config", "core.fsmonitor", "touch "+ctrlMarker+"; echo")
		t.Cleanup(func() { run("config", "core.fsmonitor", "touch "+fsmonSentinel+"; echo") })
		cmd := exec.Command("git", "-C", repo, "status")
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
		_ = cmd.Run()
		if _, err := os.Stat(ctrlMarker); err != nil {
			t.Skipf("fsmonitor did not fire even unhardened (%v); vector not exercisable here", err)
		}
	})

	// Build the runner with the SAME neutralizing env composition uses.
	r, err := osfs.NewCommandRunnerShell(repo, "/bin/sh", osfs.WithCommandEnvList(gitenv.Scrub(os.Environ())))
	if err != nil {
		t.Fatalf("NewCommandRunnerShell: %v", err)
	}

	// fsmonitor: env-injected core.fsmonitor=false must override .git/config.
	if _, err := r.Run(context.Background(), "git status"); err != nil {
		t.Fatalf("Run status: %v", err)
	}
	if _, statErr := os.Stat(fsmonSentinel); statErr == nil {
		t.Fatalf("core.fsmonitor program FIRED (sentinel %s) — env-injected core.fsmonitor=false did not override repo .git/config", fsmonSentinel)
	}

	// pager: env-injected core.pager=cat (+ GIT_PAGER=cat) must override .git/config.
	if _, err := r.Run(context.Background(), "git --paginate log"); err != nil {
		t.Fatalf("Run log: %v", err)
	}
	if _, statErr := os.Stat(pagerSentinel); statErr == nil {
		t.Fatalf("core.pager marker FIRED (sentinel %s) — env-injected core.pager=cat did not override repo .git/config", pagerSentinel)
	}
}

func TestGrep(t *testing.T) {
	ctx := context.Background()
	ws, _ := osfs.NewWorkspace(t.TempDir())
	files := map[string]string{
		"a.go":     "package main\nfunc Foo() {}\n",
		"b.go":     "package main\nfunc Bar() {}\n",
		"notes.md": "Foo is documented here\n",
	}
	for p, c := range files {
		if err := ws.Write(ctx, p, []byte(c)); err != nil {
			t.Fatalf("Write %s: %v", p, err)
		}
	}

	// Across all files.
	all, err := ws.Grep(ctx, "Foo", "")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("Grep Foo all = %d matches (%v) want 2", len(all), all)
	}

	// Restricted by glob.
	goOnly, err := ws.Grep(ctx, "func", "*.go")
	if err != nil {
		t.Fatalf("Grep glob: %v", err)
	}
	if len(goOnly) != 2 {
		t.Errorf("Grep func *.go = %d matches want 2", len(goOnly))
	}
	for _, m := range goOnly {
		if !strings.HasSuffix(m.Path, ".go") {
			t.Errorf("match outside glob: %q", m.Path)
		}
		if m.Line != 2 {
			t.Errorf("match line = %d want 2", m.Line)
		}
	}
}

func TestGrepStopsAfterTruncationThreshold(t *testing.T) {
	ctx := context.Background()
	ws, err := osfs.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if err := ws.Write(ctx, "a-many.txt", []byte(strings.Repeat("needle\n", 300))); err != nil {
		t.Fatalf("Write a-many.txt: %v", err)
	}
	if err := ws.Write(ctx, "z-unrelated.txt", []byte("needle after threshold\n")); err != nil {
		t.Fatalf("Write z-unrelated.txt: %v", err)
	}

	matches, err := ws.Grep(ctx, "needle", "")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(matches) != 201 {
		t.Fatalf("Grep returned %d matches, want 201 for the truncation sentinel", len(matches))
	}
	if matches[len(matches)-1].Path != "a-many.txt" {
		t.Errorf("Grep scanned beyond the first file after reaching the match threshold: last match = %#v", matches[len(matches)-1])
	}
}

func TestGrepSearchSafetyBudget(t *testing.T) {
	tests := []struct {
		name     string
		pathGlob string
	}{
		{name: "unscoped", pathGlob: ""},
		{name: "recursive glob", pathGlob: "**"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			ws, err := osfs.NewWorkspace(root)
			if err != nil {
				t.Fatalf("NewWorkspace: %v", err)
			}
			large := filepath.Join(root, "large.txt")
			if err := os.WriteFile(large, nil, 0o600); err != nil {
				t.Fatalf("create large file: %v", err)
			}
			if err := os.Truncate(large, 64<<20+1); err != nil {
				t.Fatalf("make large sparse file: %v", err)
			}

			_, err = ws.Grep(ctx, "needle", test.pathGlob)
			if err == nil || !strings.Contains(err.Error(), "grep search exceeds the workspace safety budget; narrow the path") {
				t.Fatalf("Grep budget error = %v, want actionable safety-budget error", err)
			}
		})
	}
}

func TestGrepSkipsDirectoriesAndHonorsCancellation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	ws, err := osfs.NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory.txt"), 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := ws.Write(ctx, "match.txt", []byte("needle\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	matches, err := ws.Grep(ctx, "needle", "*")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(matches) != 1 || matches[0].Path != "match.txt" {
		t.Errorf("Grep included a non-regular file: %#v", matches)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ws.Grep(cancelled, "needle", ""); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled Grep error = %v, want context.Canceled", err)
	}
}

// TestGlobGlobstarRecurses asserts the "**" globstar crosses directory
// separators, finding files at every depth. The old filepath.Glob delegation
// could only match within a single path segment, so a recursive "**/*.go" found
// only the lucky single-segment depth (it matched depth-2 "a/mid.go" but missed
// depth-1 "top.go" and depth-3 "a/b/deep.go").
func TestGlobGlobstarRecurses(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	ws, err := osfs.NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	// Files at depths 1, 2 and 3.
	want := []string{
		"top.go",
		"a/mid.go",
		"a/b/deep.go",
	}
	other := []string{
		"a/notes.md",
		"a/b/data.txt",
	}
	for _, p := range append(append([]string{}, want...), other...) {
		if err := ws.Write(ctx, p, []byte("package x\n")); err != nil {
			t.Fatalf("Write %s: %v", p, err)
		}
	}

	got, err := ws.Glob(ctx, "**/*.go")
	if err != nil {
		t.Fatalf("Glob(**/*.go): %v", err)
	}
	gotSet := map[string]bool{}
	for _, m := range got {
		gotSet[m] = true
	}
	for _, w := range want {
		if !gotSet[w] {
			t.Errorf("Glob(**/*.go) = %v, missing %q", got, w)
		}
	}
	// The deepest file is the canary the old single-segment behavior would miss.
	if !gotSet["a/b/deep.go"] {
		t.Errorf("Glob(**/*.go) did not recurse to depth 3; got %v", got)
	}
	// Non-.go files must not appear.
	for _, o := range other {
		if gotSet[o] {
			t.Errorf("Glob(**/*.go) surfaced non-Go file %q", o)
		}
	}
}

// TestGrepGlobstarPathGlob proves Grep is fixed transitively: its pathGlob
// funnels through FileSystem.Glob, so a "**" pattern now selects files at any
// depth.
func TestGrepGlobstarPathGlob(t *testing.T) {
	ctx := context.Background()
	ws, _ := osfs.NewWorkspace(t.TempDir())
	files := map[string]string{
		"top.go":      "package main\nfunc Foo() {}\n",
		"a/mid.go":    "package a\nfunc Foo() {}\n",
		"a/b/deep.go": "package b\nfunc Foo() {}\n",
		"a/b/skip.md": "Foo in markdown\n",
	}
	for p, c := range files {
		if err := ws.Write(ctx, p, []byte(c)); err != nil {
			t.Fatalf("Write %s: %v", p, err)
		}
	}

	hits, err := ws.Grep(ctx, "func Foo", "**/*.go")
	if err != nil {
		t.Fatalf("Grep(**/*.go): %v", err)
	}
	if len(hits) != 3 {
		t.Errorf("Grep(func Foo, **/*.go) = %d hits (%v), want 3", len(hits), hits)
	}
	var sawDeep bool
	for _, h := range hits {
		if h.Path == "a/b/deep.go" {
			sawDeep = true
		}
		if strings.HasSuffix(h.Path, ".md") {
			t.Errorf("Grep(**/*.go) matched non-Go file %q", h.Path)
		}
	}
	if !sawDeep {
		t.Errorf("Grep(**/*.go) did not reach depth-3 file; got %v", hits)
	}
}

func TestGrepInvalidPattern(t *testing.T) {
	ctx := context.Background()
	ws, _ := osfs.NewWorkspace(t.TempDir())
	if _, err := ws.Grep(ctx, "(", ""); err == nil {
		t.Fatal("Grep with invalid regex = nil err, want error")
	}
}

func TestRootIsAbsolute(t *testing.T) {
	root := t.TempDir()
	ws, _ := osfs.NewWorkspace(root)
	if !filepath.IsAbs(ws.Root()) {
		t.Errorf("Root() = %q is not absolute", ws.Root())
	}
}

func TestWriteCreatesParents(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	ws, _ := osfs.NewWorkspace(root)
	if err := ws.Write(ctx, "deep/nested/path/f.txt", []byte("x")); err != nil {
		t.Fatalf("Write nested: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "deep", "nested", "path", "f.txt")); err != nil {
		t.Fatalf("expected nested file on disk: %v", err)
	}
}

// TestGlobDoesNotLeakThroughSymlink asserts Glob never returns paths reachable
// only by traversing a symlink out of the workspace — neither a leaf symlink nor
// a symlinked intermediate directory component. The latter is the filename
// enumeration leak: filepath.Glob follows an in-root directory symlink to an
// out-of-root target, and the match looks like an ordinary in-root regular file.
func TestGlobDoesNotLeakThroughSymlink(t *testing.T) {
	ctx := context.Background()

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o600); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	root := t.TempDir()
	ws, err := osfs.NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	// An in-root regular file Glob must still find.
	if err := ws.Write(ctx, "real.txt", []byte("ok")); err != nil {
		t.Fatalf("Write real.txt: %v", err)
	}
	// (a) symlinked intermediate directory component: link/ -> outside/.
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatalf("symlink dir: %v", err)
	}
	// (b) leaf symlink to an out-of-root file.
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "leaf.txt")); err != nil {
		t.Fatalf("symlink leaf: %v", err)
	}

	// The "**" globstar must also refuse to cross an escaping symlink: a
	// recursive walk is exactly the pattern an attacker would use to enumerate
	// out-of-root files, so it is the security gate on the doublestar change.
	for _, pattern := range []string{"link/*", "*", "*.txt", "**/*", "**/*.txt"} {
		got, err := ws.Glob(ctx, pattern)
		if err != nil {
			t.Fatalf("Glob(%q): %v", pattern, err)
		}
		for _, m := range got {
			if strings.HasPrefix(m, "link/") || m == "leaf.txt" {
				t.Errorf("Glob(%q) leaked out-of-root match %q", pattern, m)
			}
		}
	}

	// Sanity: the genuine in-root file is still matched.
	got, err := ws.Glob(ctx, "*.txt")
	if err != nil {
		t.Fatalf("Glob(*.txt): %v", err)
	}
	var sawReal bool
	for _, m := range got {
		if m == "real.txt" {
			sawReal = true
		}
	}
	if !sawReal {
		t.Errorf("Glob(*.txt) = %v, expected to contain real.txt", got)
	}
}

// TestGlobRejectsDotDotPattern pins the confinement against literal ".." in the
// pattern itself: a "../"-bearing glob must never climb out of the root. The
// os.Root.FS() walk refuses such traversal, so these patterns return no matches
// and no error. Stand-in target names are used; nothing sensitive is referenced.
func TestGlobRejectsDotDotPattern(t *testing.T) {
	ctx := context.Background()

	// An out-of-root sibling file the patterns would reach if confinement broke.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "target.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	root := t.TempDir()
	ws, err := osfs.NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if err := ws.Write(ctx, "in.txt", []byte("ok")); err != nil {
		t.Fatalf("Write in.txt: %v", err)
	}

	for _, pattern := range []string{"../*", "../*.txt", "**/../**", "a/../../target.txt", "../../*"} {
		got, gerr := ws.Glob(ctx, pattern)
		if gerr != nil {
			t.Errorf("Glob(%q) error = %v, want nil", pattern, gerr)
		}
		if len(got) != 0 {
			t.Errorf("Glob(%q) = %v, want no matches (confinement breach)", pattern, got)
		}
	}
}

// TestGlobBadPattern asserts a malformed pattern surfaces as an error (mirroring
// TestGrepInvalidPattern), rather than being silently swallowed.
func TestGlobBadPattern(t *testing.T) {
	ctx := context.Background()
	ws, _ := osfs.NewWorkspace(t.TempDir())
	if _, err := ws.Glob(ctx, "["); err == nil {
		t.Fatal("Glob with malformed pattern = nil err, want error")
	}
}

// TestGlobPatternNormalization asserts the leading-"/"/"./" leniency: "/**/*.go"
// and "./**/*.go" return the same non-empty set as "**/*.go", and ""/"." return
// (nil, nil).
func TestGlobPatternNormalization(t *testing.T) {
	ctx := context.Background()
	ws, _ := osfs.NewWorkspace(t.TempDir())
	for _, p := range []string{"top.go", "a/b/deep.go"} {
		if err := ws.Write(ctx, p, []byte("z")); err != nil {
			t.Fatalf("Write %s: %v", p, err)
		}
	}

	base, err := ws.Glob(ctx, "**/*.go")
	if err != nil {
		t.Fatalf("Glob(**/*.go): %v", err)
	}
	if len(base) == 0 {
		t.Fatal("Glob(**/*.go) returned no matches; fixture broken")
	}
	for _, equiv := range []string{"/**/*.go", "./**/*.go"} {
		got, gerr := ws.Glob(ctx, equiv)
		if gerr != nil {
			t.Fatalf("Glob(%q): %v", equiv, gerr)
		}
		if !slices.Equal(got, base) {
			t.Errorf("Glob(%q) = %v, want same as Glob(**/*.go) = %v", equiv, got, base)
		}
	}

	for _, empty := range []string{"", "."} {
		got, gerr := ws.Glob(ctx, empty)
		if gerr != nil {
			t.Errorf("Glob(%q) error = %v, want nil", empty, gerr)
		}
		if got != nil {
			t.Errorf("Glob(%q) = %v, want nil (match-nothing)", empty, got)
		}
	}
}

// TestGlobZeroSegmentGlobstar pins the doublestar semantic that "a/**/b.go"
// matches with ZERO intermediate segments ("a/b.go") as well as one or more
// ("a/x/b.go") — the behavior that distinguishes "**" from a single "*".
func TestGlobZeroSegmentGlobstar(t *testing.T) {
	ctx := context.Background()
	ws, _ := osfs.NewWorkspace(t.TempDir())
	for _, p := range []string{"a/b.go", "a/x/b.go"} {
		if err := ws.Write(ctx, p, []byte("z")); err != nil {
			t.Fatalf("Write %s: %v", p, err)
		}
	}

	got, err := ws.Glob(ctx, "a/**/b.go")
	if err != nil {
		t.Fatalf("Glob(a/**/b.go): %v", err)
	}
	set := map[string]bool{}
	for _, m := range got {
		set[m] = true
	}
	if !set["a/b.go"] {
		t.Errorf("Glob(a/**/b.go) = %v, missing zero-segment a/b.go", got)
	}
	if !set["a/x/b.go"] {
		t.Errorf("Glob(a/**/b.go) = %v, missing one-segment a/x/b.go", got)
	}
}

// TestGlobDropsInRootLeafSymlink pins the "drop ALL leaf symlinks" contract: a
// symlink wholly inside the root (link.txt -> a.txt) is still dropped from Glob
// results, even though it does not escape. This catches a future refactor that
// narrows the drop to only escaping links.
func TestGlobDropsInRootLeafSymlink(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	ws, err := osfs.NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if err := ws.Write(ctx, "a.txt", []byte("ok")); err != nil {
		t.Fatalf("Write a.txt: %v", err)
	}
	// An in-root symlink to an in-root target (does not escape).
	if err := os.Symlink("a.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("symlink in-root leaf: %v", err)
	}

	got, err := ws.Glob(ctx, "*")
	if err != nil {
		t.Fatalf("Glob(*): %v", err)
	}
	var sawReal, sawLink bool
	for _, m := range got {
		switch m {
		case "a.txt":
			sawReal = true
		case "link.txt":
			sawLink = true
		}
	}
	if !sawReal {
		t.Errorf("Glob(*) = %v, missing real file a.txt", got)
	}
	if sawLink {
		t.Errorf("Glob(*) = %v, surfaced in-root leaf symlink link.txt (must drop all leaf symlinks)", got)
	}
}

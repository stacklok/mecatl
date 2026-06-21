package forker_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/forker"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// osfsWorkspace adapts osfs.NewWorkspace to the forker's constructor signature.
func osfsWorkspace(root string) (tool.Workspace, error) {
	return osfs.NewWorkspace(root)
}

// TestForkGitWorktree exercises the git-repo path: forking a repo yields a real
// worktree directory derived from the base, and cleanup removes it. Skipped when
// git is unavailable.
func TestForkGitWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	initGitRepo(t, base)
	writeFile(t, filepath.Join(base, "tracked.txt"), "from base\n")
	gitCommit(t, base)

	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}

	f := forker.New(osfsWorkspace)
	child, cleanup, _, err := f.Fork(context.Background(), baseWS, "idea-a")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}

	// The child root must be a distinct directory from the base, and a worktree
	// (carries a .git file/dir pointing back at the repo).
	if child.Root() == base {
		t.Fatalf("child root must differ from base; both are %q", base)
	}
	if _, err := os.Stat(filepath.Join(child.Root(), ".git")); err != nil {
		t.Fatalf("child is not a git worktree (.git missing): %v", err)
	}
	// The committed file is present in the worktree.
	if _, err := os.Stat(filepath.Join(child.Root(), "tracked.txt")); err != nil {
		t.Fatalf("worktree missing committed file: %v", err)
	}

	// A write in the child does NOT touch the base.
	if err := child.Write(context.Background(), "child-only.txt", []byte("x")); err != nil {
		t.Fatalf("child write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "child-only.txt")); !os.IsNotExist(err) {
		t.Fatalf("child write leaked into base (err=%v)", err)
	}

	childRoot := child.Root()
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(childRoot); !os.IsNotExist(err) {
		t.Fatalf("cleanup did not remove worktree dir %q (err=%v)", childRoot, err)
	}
}

// TestForkNeutralizesRepoHooks proves the forker's OWN git invocations run with a
// git-neutralizing environment: `git worktree add` would otherwise FIRE the base
// repo's post-checkout hook at fork time (before any sandboxed member runner exists).
// A base repo with a post-checkout hook that writes a sentinel must NOT create that
// sentinel when Fork runs. Skipped when git is unavailable.
func TestForkNeutralizesRepoHooks(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	initGitRepo(t, base)
	writeFile(t, filepath.Join(base, "tracked.txt"), "from base\n")
	gitCommit(t, base)

	// Install a post-checkout hook that writes an innocuous sentinel file. git
	// worktree add runs a checkout in the new worktree and fires this hook UNLESS
	// core.hooksPath is neutralised.
	sentinel := filepath.Join(t.TempDir(), "hook-fired")
	hookDir := filepath.Join(base, ".git", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	hook := "#!/bin/sh\ntouch " + sentinel + "\n"
	if err := os.WriteFile(filepath.Join(hookDir, "post-checkout"), []byte(hook), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}
	f := forker.New(osfsWorkspace)
	child, cleanup, _, err := f.Fork(context.Background(), baseWS, "ro-member")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	defer func() { _ = cleanup() }()

	// Sanity: we did take the worktree path (so the hook was genuinely reachable).
	if _, err := os.Stat(filepath.Join(child.Root(), ".git")); err != nil {
		t.Fatalf("child is not a git worktree (.git missing): %v", err)
	}
	if _, statErr := os.Stat(sentinel); statErr == nil {
		t.Fatalf("post-checkout hook FIRED (sentinel %s created) at fork time — forker git env is not neutralized", sentinel)
	}
}

// TestForkCopyFallback exercises the non-git path: forking a plain directory
// yields an isolated recursive copy; writes in the child do not affect the base,
// and cleanup removes the copy.
func TestForkCopyFallback(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "a.txt"), "alpha\n")
	writeFile(t, filepath.Join(base, "sub", "b.txt"), "beta\n")

	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}

	f := forker.New(osfsWorkspace)
	child, cleanup, _, err := f.Fork(context.Background(), baseWS, "copy idea!")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if child.Root() == base {
		t.Fatalf("child root must differ from base")
	}

	// The copy carries the base files.
	got, err := child.Read(context.Background(), "a.txt")
	if err != nil || string(got) != "alpha\n" {
		t.Fatalf("child a.txt = %q, err=%v", got, err)
	}
	got, err = child.Read(context.Background(), "sub/b.txt")
	if err != nil || string(got) != "beta\n" {
		t.Fatalf("child sub/b.txt = %q, err=%v", got, err)
	}

	// Mutating the child must not affect the base.
	if err := child.Write(context.Background(), "a.txt", []byte("CHANGED")); err != nil {
		t.Fatalf("child write: %v", err)
	}
	baseData, err := os.ReadFile(filepath.Join(base, "a.txt"))
	if err != nil {
		t.Fatalf("read base a.txt: %v", err)
	}
	if string(baseData) != "alpha\n" {
		t.Fatalf("base a.txt mutated by child: %q", baseData)
	}

	childRoot := child.Root()
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(childRoot); !os.IsNotExist(err) {
		t.Fatalf("cleanup did not remove copy dir %q (err=%v)", childRoot, err)
	}
}

// TestForkConcurrentCopiesAreDistinct forks the same base several times in
// parallel and asserts every child gets a distinct, isolated root.
func TestForkConcurrentCopiesAreDistinct(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "f.txt"), "x")

	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}
	f := forker.New(osfsWorkspace)

	const n = 5
	roots := make([]string, n)
	cleanups := make([]func() error, n)
	errs := make([]error, n)
	done := make(chan int, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			child, cleanup, _, err := f.Fork(context.Background(), baseWS, "x")
			if err != nil {
				errs[i] = err
			} else {
				roots[i] = child.Root()
				cleanups[i] = cleanup
			}
			done <- i
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}
	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("fork %d: %v", i, errs[i])
		}
		if seen[roots[i]] {
			t.Fatalf("duplicate child root %q", roots[i])
		}
		seen[roots[i]] = true
		if cleanups[i] != nil {
			_ = cleanups[i]()
		}
	}
}

// TestForkForceCopyIsFullyIsolatedRepo proves the WithForceCopy mode: forking a
// git-repo base yields a SELF-CONTAINED repository (its own .git: HEAD/refs/objects)
// so a branch that runs git commit / writes a ref / writes a file INSIDE the fork
// does NOT touch the base repo's .git or working tree. This is the mutating-fork
// isolation guarantee. Skipped when git is unavailable.
func TestForkForceCopyIsFullyIsolatedRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	initGitRepo(t, base)
	writeFile(t, filepath.Join(base, "tracked.txt"), "from base\n")
	gitCommit(t, base)

	// Snapshot the base repo's pre-fork state: HEAD commit, ref listing, object dir.
	baseHeadBefore := gitOutput(t, base, "rev-parse", "HEAD")
	baseRefsBefore := gitOutput(t, base, "show-ref")

	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}

	f := forker.New(osfsWorkspace, forker.WithForceCopy())
	child, cleanup, _, err := f.Fork(context.Background(), baseWS, "mutating-branch")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })

	if child.Root() == base {
		t.Fatalf("child root must differ from base; both are %q", base)
	}
	// Force-copy of a repo carries .git as a real directory (NOT a worktree gitfile),
	// so the fork is an independent repo with its own object DB/refs.
	gitInfo, err := os.Stat(filepath.Join(child.Root(), ".git"))
	if err != nil {
		t.Fatalf("force-copy fork missing .git: %v", err)
	}
	if !gitInfo.IsDir() {
		t.Fatalf("force-copy fork .git must be a directory (own object DB), got a file (worktree gitlink)")
	}

	// Mutate INSIDE the fork in every way a branch's Bash/git could: a new commit, a
	// directly-written ref, and a new working-tree file.
	writeFile(t, filepath.Join(child.Root(), "branch-only.txt"), "made in fork\n")
	runGit(t, child.Root(), "add", "-A")
	runGit(t, child.Root(), "commit", "-m", "fork commit")
	runGit(t, child.Root(), "update-ref", "refs/heads/sneaky", "HEAD")

	// The base repo's .git MUST be unchanged: same HEAD, same refs.
	if got := gitOutput(t, base, "rev-parse", "HEAD"); got != baseHeadBefore {
		t.Errorf("base HEAD changed after fork commit: before=%q after=%q", baseHeadBefore, got)
	}
	if got := gitOutput(t, base, "show-ref"); got != baseRefsBefore {
		t.Errorf("base refs changed after fork ref write:\nbefore=%q\nafter=%q", baseRefsBefore, got)
	}
	if _, err := os.Stat(filepath.Join(base, "refs", "heads", "sneaky")); err == nil {
		t.Errorf("fork's update-ref leaked into base .git (refs/heads/sneaky present)")
	}
	// The base working tree MUST be unchanged: the branch-only file did not appear.
	if _, err := os.Stat(filepath.Join(base, "branch-only.txt")); !os.IsNotExist(err) {
		t.Errorf("fork's working-tree file leaked into base (err=%v)", err)
	}
}

// TestForkWorktreeSharesObjectDB is the CONTRAST: the DEFAULT (auto) path uses a
// git worktree for a repo base, which SHARES the object DB and refs. A commit made
// inside the worktree fork is visible in the BASE repo's object store — exactly the
// isolation gap WithForceCopy closes. This documents why the default is unsafe for
// mutating callers and that the base-unchanged assertion above would FAIL on this
// path. Skipped when git is unavailable.
func TestForkWorktreeSharesObjectDB(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	initGitRepo(t, base)
	writeFile(t, filepath.Join(base, "tracked.txt"), "from base\n")
	gitCommit(t, base)

	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}

	// Default forker (no WithForceCopy) → worktree path for a repo.
	f := forker.New(osfsWorkspace)
	child, cleanup, _, err := f.Fork(context.Background(), baseWS, "wt")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })

	// A worktree's .git is a FILE (gitlink), not a directory — proof the object DB
	// is shared with the base repo, not copied.
	gitInfo, err := os.Stat(filepath.Join(child.Root(), ".git"))
	if err != nil {
		t.Fatalf("worktree fork missing .git: %v", err)
	}
	if gitInfo.IsDir() {
		t.Fatalf("expected worktree .git to be a gitlink file (shared object DB), got a directory")
	}

	// Commit inside the worktree; the commit object lands in the SHARED object DB.
	writeFile(t, filepath.Join(child.Root(), "wt-only.txt"), "x\n")
	runGit(t, child.Root(), "add", "-A")
	runGit(t, child.Root(), "commit", "-m", "worktree commit")
	forkHead := gitOutput(t, child.Root(), "rev-parse", "HEAD")

	// The base repo can resolve the worktree's commit object — they share .git.
	// (This is the leak WithForceCopy prevents.)
	if got := gitOutput(t, base, "cat-file", "-t", forkHead); got != "commit" {
		t.Fatalf("expected worktree commit %s to be visible in base object DB, got type %q", forkHead, got)
	}
}

// TestNewNilConstructorPanics asserts the composition-root contract.
func TestNewNilConstructorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("New(nil) did not panic")
		}
	}()
	_ = forker.New(nil)
}

// --- helpers ---------------------------------------------------------------

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
}

func gitCommit(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "init")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// gitOutput runs a git subcommand in dir and returns its trimmed stdout, failing
// the test on a non-zero exit.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// TestMergerAppliesForkDiffToParent asserts that Merger.Merge applies a fork's
// working-tree changes (tracked modifications + untracked new files) back into
// the parent workspace — the auto-merge fast path. Skipped when git is
// unavailable.
func TestMergerAppliesForkDiffToParent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	initGitRepo(t, base)
	writeFile(t, filepath.Join(base, "existing.txt"), "from base\n")
	gitCommit(t, base)

	// Fork the base so we have an isolated child to edit in.
	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}
	f := forker.New(osfsWorkspace)
	child, cleanup, _, err := f.Fork(context.Background(), baseWS, "impl")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	defer func() { _ = cleanup() }()

	// Edit a tracked file and add an untracked file in the fork (the child's
	// "implementation").
	childRoot := child.Root()
	if err := os.WriteFile(filepath.Join(childRoot, "existing.txt"), []byte("from fork\n"), 0o644); err != nil {
		t.Fatalf("edit tracked in fork: %v", err)
	}
	if err := os.WriteFile(filepath.Join(childRoot, "new.txt"), []byte("new from fork\n"), 0o644); err != nil {
		t.Fatalf("write untracked in fork: %v", err)
	}

	// The parent must still have the original content before the merge.
	if got, _ := os.ReadFile(filepath.Join(base, "existing.txt")); string(got) != "from base\n" {
		t.Fatalf("parent existing.txt mutated before merge: %q", got)
	}

	// Merge the fork's diff back into the parent.
	m := forker.NewMerger()
	if err := m.Merge(context.Background(), childRoot, baseWS); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	// The parent now has BOTH the tracked edit and the untracked new file.
	got, err := os.ReadFile(filepath.Join(base, "existing.txt"))
	if err != nil {
		t.Fatalf("parent existing.txt missing after merge: %v", err)
	}
	if string(got) != "from fork\n" {
		t.Fatalf("parent existing.txt not merged: got %q, want %q", got, "from fork\n")
	}
	gotNew, err := os.ReadFile(filepath.Join(base, "new.txt"))
	if err != nil {
		t.Fatalf("parent new.txt missing after merge: %v", err)
	}
	if string(gotNew) != "new from fork\n" {
		t.Fatalf("parent new.txt not merged: got %q", gotNew)
	}
}

// TestMergerCleanForkIsNoOp asserts that a fork with NO working-tree changes
// short-circuits — Merge returns nil without touching the parent.
func TestMergerCleanForkIsNoOp(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	initGitRepo(t, base)
	writeFile(t, filepath.Join(base, "tracked.txt"), "from base\n")
	gitCommit(t, base)

	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}
	f := forker.New(osfsWorkspace)
	child, cleanup, _, err := f.Fork(context.Background(), baseWS, "clean")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	defer func() { _ = cleanup() }()

	// No edits in the fork — it's a clean checkout of HEAD.
	m := forker.NewMerger()
	if err := m.Merge(context.Background(), child.Root(), baseWS); err != nil {
		t.Fatalf("Merge of a clean fork must be a no-op, got: %v", err)
	}
}

// TestMergerConflictSurfacesError asserts that a merge conflict (the parent has
// diverging edits to the same file) returns a non-nil error naming the fork
// path, and does NOT force the apply.
func TestMergerConflictSurfacesError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	initGitRepo(t, base)
	writeFile(t, filepath.Join(base, "shared.txt"), "from base\n")
	gitCommit(t, base)

	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}
	f := forker.New(osfsWorkspace)
	child, cleanup, _, err := f.Fork(context.Background(), baseWS, "conflict")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	defer func() { _ = cleanup() }()

	// Diverging edit: the fork changes line 1 one way, the parent changes it
	// another way — `git apply` will reject the patch.
	if err := os.WriteFile(filepath.Join(child.Root(), "shared.txt"), []byte("from fork\n"), 0o644); err != nil {
		t.Fatalf("edit in fork: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "shared.txt"), []byte("from parent\n"), 0o644); err != nil {
		t.Fatalf("edit in parent: %v", err)
	}

	m := forker.NewMerger()
	err = m.Merge(context.Background(), child.Root(), baseWS)
	if err == nil {
		t.Fatal("Merge of a conflicting fork must return an error, got nil")
	}
	if !strings.Contains(err.Error(), "preserved at") {
		t.Fatalf("conflict error must name the preserved fork path for manual resolution, got: %v", err)
	}
	// ATOMIC-OR-NOTHING: the failed merge must leave the parent tree EXACTLY as it
	// was. git apply is itself atomic (it validates all hunks before writing any) and
	// the `git apply --check` dry-run is defense-in-depth on top; either way a
	// conflicting patch never partially writes the parent. The parent's own edit
	// ("from parent") must survive verbatim. The cross-file atomicity property — a
	// patch where one file applies cleanly and another conflicts must write NEITHER —
	// is locked explicitly by TestMergerMultiFileConflictLeavesCleanFileUntouched.
	got, rerr := os.ReadFile(filepath.Join(base, "shared.txt"))
	if rerr != nil {
		t.Fatalf("read parent shared.txt after failed merge: %v", rerr)
	}
	if string(got) != "from parent\n" {
		t.Fatalf("a conflicting merge must leave the parent tree untouched; want %q, got %q", "from parent\n", string(got))
	}
}

// TestMergerMultiFileConflictLeavesCleanFileUntouched locks the CROSS-FILE
// atomic-or-nothing property the single-file conflict test cannot show: a merge
// whose patch touches TWO files — one that would apply cleanly, one that
// conflicts with a divergent parent edit — must write NEITHER (no partial
// apply), and surface the conflict as an error naming the preserved fork. This
// is the QA-panel must-add: it proves the conflict path leaves a clean sibling
// file untouched, which the single-file scenario (where there is no clean
// sibling) structurally cannot. The property holds via git apply's own
// all-or-nothing hunk validation (the --check pre-pass is defense-in-depth).
func TestMergerMultiFileConflictLeavesCleanFileUntouched(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	initGitRepo(t, base)
	writeFile(t, filepath.Join(base, "clean.txt"), "from base clean\n")
	writeFile(t, filepath.Join(base, "shared.txt"), "from base shared\n")
	gitCommit(t, base)

	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}
	f := forker.New(osfsWorkspace)
	child, cleanup, _, err := f.Fork(context.Background(), baseWS, "multiconflict")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	defer func() { _ = cleanup() }()

	// The fork edits BOTH files. clean.txt is untouched in the parent (its hunk
	// would apply); shared.txt is edited divergently in the parent (its hunk
	// conflicts), so `git apply` rejects the WHOLE patch.
	if err := os.WriteFile(filepath.Join(child.Root(), "clean.txt"), []byte("from fork clean\n"), 0o644); err != nil {
		t.Fatalf("edit clean.txt in fork: %v", err)
	}
	if err := os.WriteFile(filepath.Join(child.Root(), "shared.txt"), []byte("from fork shared\n"), 0o644); err != nil {
		t.Fatalf("edit shared.txt in fork: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "shared.txt"), []byte("from parent shared\n"), 0o644); err != nil {
		t.Fatalf("divergent edit shared.txt in parent: %v", err)
	}

	m := forker.NewMerger()
	if err := m.Merge(context.Background(), child.Root(), baseWS); err == nil {
		t.Fatal("a multi-file merge with a conflicting file must return an error, got nil")
	}

	// THE PROPERTY: clean.txt — which would have applied cleanly on its own — must
	// NOT have been written, because the whole patch was rejected atomically. A
	// partial apply would leave it as "from fork clean".
	gotClean, err := os.ReadFile(filepath.Join(base, "clean.txt"))
	if err != nil {
		t.Fatalf("read parent clean.txt after failed merge: %v", err)
	}
	if string(gotClean) != "from base clean\n" {
		t.Fatalf("PARTIAL APPLY: clean.txt was written despite the conflicting sibling; want %q, got %q", "from base clean\n", string(gotClean))
	}
	// And the conflicting file keeps the parent's divergent edit.
	gotShared, err := os.ReadFile(filepath.Join(base, "shared.txt"))
	if err != nil {
		t.Fatalf("read parent shared.txt after failed merge: %v", err)
	}
	if string(gotShared) != "from parent shared\n" {
		t.Fatalf("a conflicting merge must leave the parent's shared.txt untouched; want %q, got %q", "from parent shared\n", string(gotShared))
	}
}

// TestMergerRefusesGitattributesPatch asserts the SECURITY mitigation: the merge
// refuses to apply a patch that touches `.gitattributes` — an untrusted branch must
// not silently land attribute changes that repoint the parent's git filter/diff
// drivers (a filter.<drv>.smudge in the parent's config would fire on
// attacker-controlled blob content at merge time). The merge returns an error
// naming the refusal; the parent tree is untouched.
//
// Uses WithForceCopy (the real Parallel-branch topology: the fork gets its OWN
// `.git` the branch can write to), and STAGES the .gitattributes so `git diff HEAD`
// carries it as a tracked change (a committed change would move HEAD and yield an
// empty diff; staged is the realistic pre-commit state).
func TestMergerRefusesGitattributesPatch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	initGitRepo(t, base)
	writeFile(t, filepath.Join(base, "existing.txt"), "from base\n")
	gitCommit(t, base)

	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}
	// Force-copy: the fork has its own .git the branch can write to.
	f := forker.New(osfsWorkspace, forker.WithForceCopy())
	child, cleanup, _, err := f.Fork(context.Background(), baseWS, "attrs")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	defer func() { _ = cleanup() }()

	// In the fork: add a .gitattributes (repointing a filter) + STAGE it so
	// `git diff HEAD` carries it, plus a staged tracked edit so the patch has both.
	childRoot := child.Root()
	if err := os.WriteFile(filepath.Join(childRoot, ".gitattributes"), []byte("*.txt filter=evil\n"), 0o644); err != nil {
		t.Fatalf("write .gitattributes in fork: %v", err)
	}
	if err := os.WriteFile(filepath.Join(childRoot, "existing.txt"), []byte("from fork\n"), 0o644); err != nil {
		t.Fatalf("edit tracked in fork: %v", err)
	}
	runGit(t, childRoot, "add", ".gitattributes", "existing.txt")

	// The parent must be untouched before the merge (sanity).
	if _, err := os.Stat(filepath.Join(base, ".gitattributes")); !os.IsNotExist(err) {
		t.Fatalf("parent already has a .gitattributes before merge (err=%v)", err)
	}

	m := forker.NewMerger()
	err = m.Merge(context.Background(), childRoot, baseWS)
	if err == nil {
		t.Fatal("Merge of a .gitattributes-touching fork must be refused, got nil")
	}
	if !strings.Contains(err.Error(), ".gitattributes") {
		t.Fatalf("refusal error must name .gitattributes, got: %v", err)
	}
	// The parent must NOT have the .gitattributes (the merge refused before apply).
	if _, err := os.Stat(filepath.Join(base, ".gitattributes")); !os.IsNotExist(err) {
		t.Fatalf("the refused merge leaked .gitattributes into the parent (err=%v)", err)
	}
	// And the tracked edit must NOT have landed either (the whole patch was refused).
	got, readErr := os.ReadFile(filepath.Join(base, "existing.txt"))
	if readErr != nil {
		t.Fatalf("parent existing.txt missing: %v", readErr)
	}
	if string(got) != "from base\n" {
		t.Fatalf("the refused merge leaked the tracked edit into the parent: got %q", got)
	}
}

// TestMergerRefusesUntrackedGitattributes is the FIX B guard: the step-(3) untracked
// copy loop must ALSO refuse a NEW (untracked) `.gitattributes`. The step-(2) patch
// screen only covers TRACKED changes, so a child that CREATES a .gitattributes (here
// in a subdir, exercising the "/.gitattributes" suffix branch) would otherwise bypass
// the filter/diff-driver-repointing defense. The merge returns an error naming the
// refusal; the file must NOT appear in the parent.
func TestMergerRefusesUntrackedGitattributes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	initGitRepo(t, base)
	writeFile(t, filepath.Join(base, "existing.txt"), "from base\n")
	gitCommit(t, base)

	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}
	f := forker.New(osfsWorkspace, forker.WithForceCopy())
	child, cleanup, _, err := f.Fork(context.Background(), baseWS, "untracked-attrs")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	defer func() { _ = cleanup() }()

	// In the fork: create a NEW, UNTRACKED .gitattributes in a subdir (never staged,
	// so it is NOT in `git diff HEAD` — it rides the untracked copy loop, step 3).
	childRoot := child.Root()
	if err := os.MkdirAll(filepath.Join(childRoot, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub in fork: %v", err)
	}
	if err := os.WriteFile(filepath.Join(childRoot, "sub", ".gitattributes"), []byte("*.txt filter=evil\n"), 0o644); err != nil {
		t.Fatalf("write untracked sub/.gitattributes in fork: %v", err)
	}

	m := forker.NewMerger()
	err = m.Merge(context.Background(), childRoot, baseWS)
	if err == nil {
		t.Fatal("Merge of a fork with an untracked .gitattributes must be refused, got nil")
	}
	if !strings.Contains(err.Error(), ".gitattributes") {
		t.Fatalf("refusal error must name .gitattributes, got: %v", err)
	}
	// The parent must NOT have gained the untracked .gitattributes.
	if _, err := os.Stat(filepath.Join(base, "sub", ".gitattributes")); !os.IsNotExist(err) {
		t.Fatalf("the refused merge leaked sub/.gitattributes into the parent (err=%v)", err)
	}
}

// TestMergerTextconvDoesNotFire asserts the SECURITY mitigation: the merge's
// `git diff` runs with --no-textconv, so an attacker-installed
// `diff.<drv>.textconv` in the fork's .git/config does NOT execute at merge time
// (and its stdout does NOT masquerade as the merged content). Without --no-textconv
// the textconv command would fire and the sentinel file would appear; with it, the
// diff is emitted from raw blob content and no textconv runs.
//
// Uses WithForceCopy (the real Parallel-branch topology: the fork has its own
// `.git` the branch can arm a textconv driver in).
func TestMergerTextconvDoesNotFire(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	initGitRepo(t, base)
	writeFile(t, filepath.Join(base, "tracked.txt"), "from base\n")
	gitCommit(t, base)

	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}
	f := forker.New(osfsWorkspace, forker.WithForceCopy())
	child, cleanup, _, err := f.Fork(context.Background(), baseWS, "textconv")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	defer func() { _ = cleanup() }()

	// Arm an attacker textconv in the FORK's .git/config + a tracked edit so the
	// fork is dirty. The textconv command writes a sentinel file if it fires.
	sentinel := filepath.Join(t.TempDir(), "TEXCONV_FIRED.txt")
	childRoot := child.Root()
	runGit(t, childRoot, "config", "diff.evil.textconv",
		"/bin/sh -c \"echo FIRED > "+sentinel+"\"")
	// Put the attribute in the fork's .git/info/attributes (NOT carried by the
	// patch, so the .gitattributes refusal doesn't block this test) — the textconv
	// fires on `git diff` for files the attribute selects, regardless of how the
	// attribute was set. The force-copy fork has a real .git dir.
	infoAttrs := filepath.Join(childRoot, ".git", "info", "attributes")
	if err := os.MkdirAll(filepath.Dir(infoAttrs), 0o755); err != nil {
		t.Fatalf("mkdir .git/info: %v", err)
	}
	if err := os.WriteFile(infoAttrs, []byte("tracked.txt diff=evil\n"), 0o644); err != nil {
		t.Fatalf("write .git/info/attributes: %v", err)
	}
	if err := os.WriteFile(filepath.Join(childRoot, "tracked.txt"), []byte("from fork\n"), 0o644); err != nil {
		t.Fatalf("edit tracked in fork: %v", err)
	}

	m := forker.NewMerger()
	if err := m.Merge(context.Background(), childRoot, baseWS); err != nil {
		t.Fatalf("Merge (textconv test) failed: %v", err)
	}
	// The sentinel must NOT exist — the textconv did NOT fire under --no-textconv.
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("the attacker textconv FIRED during the merge's git diff (sentinel %q exists) — --no-textconv is not suppressing diff.*.textconv", sentinel)
	}
	// And the merged content must be the real edit, not the textconv's stdout.
	got, readErr := os.ReadFile(filepath.Join(base, "tracked.txt"))
	if readErr != nil {
		t.Fatalf("parent tracked.txt missing after merge: %v", readErr)
	}
	if string(got) != "from fork\n" {
		t.Fatalf("merged content is the textconv stdout, not the real edit: got %q", got)
	}
}

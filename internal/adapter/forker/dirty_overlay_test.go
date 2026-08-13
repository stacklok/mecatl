package forker_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/forker"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// requireGit skips the test when git is unavailable, matching the package style.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// dirtyOverlayBase initialises a git repo with one committed tracked file
// ("tracked.txt" = "v1\n") and returns the repo dir.
func dirtyOverlayBase(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	initGitRepo(t, base)
	writeFile(t, filepath.Join(base, "tracked.txt"), "v1\n")
	gitCommit(t, base)
	return base
}

// forkDirty forks base with the dirty-overlay forker and returns the child root +
// cleanup. The forker uses WithTempBase(base) so the worktree shares base's
// filesystem (and, more importantly, is created where the test expects).
func forkDirty(t *testing.T, base, label string) (string, func() error) {
	t.Helper()
	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}
	f := forker.New(osfsWorkspace, forker.WithDirtyOverlay())
	child, cleanup, advisory, err := forkWorkspace(f, context.Background(), baseWS, label)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	// Every test routed through forkDirty exercises a SUCCESSFUL overlay (or a clean
	// tree), so the degraded-fork advisory must be empty — only TestForkDirtyOverlayFailSoft
	// (which forks directly) drives the degraded path. This pins item 1d's "empty on
	// success/clean" across the whole success suite.
	if advisory != "" {
		t.Fatalf("Fork returned a non-empty advisory %q; want empty on a successful overlay / clean tree", advisory)
	}
	return child.Root(), cleanup
}

// TestForkDirtyOverlayTrackedModification: a modified (uncommitted) tracked file in
// the parent is visible in the child worktree.
func TestForkDirtyOverlayTrackedModification(t *testing.T) {
	requireGit(t)
	base := dirtyOverlayBase(t)
	writeFile(t, filepath.Join(base, "tracked.txt"), "v2-DIRTY\n")

	childRoot, cleanup := forkDirty(t, base, "ro-mod")
	t.Cleanup(func() { _ = cleanup() })

	got, err := os.ReadFile(filepath.Join(childRoot, "tracked.txt"))
	if err != nil {
		t.Fatalf("read child tracked.txt: %v", err)
	}
	if string(got) != "v2-DIRTY\n" {
		t.Fatalf("child tracked.txt = %q, want the parent's uncommitted %q", got, "v2-DIRTY\n")
	}
}

// TestForkDirtyOverlayStagedChange: a staged-but-uncommitted change is visible
// (diff HEAD spans both the index and the working tree).
func TestForkDirtyOverlayStagedChange(t *testing.T) {
	requireGit(t)
	base := dirtyOverlayBase(t)
	writeFile(t, filepath.Join(base, "staged.txt"), "staged-content\n")
	runGit(t, base, "add", "staged.txt")

	childRoot, cleanup := forkDirty(t, base, "ro-staged")
	t.Cleanup(func() { _ = cleanup() })

	got, err := os.ReadFile(filepath.Join(childRoot, "staged.txt"))
	if err != nil {
		t.Fatalf("read child staged.txt: %v", err)
	}
	if string(got) != "staged-content\n" {
		t.Fatalf("child staged.txt = %q, want %q", got, "staged-content\n")
	}
}

// TestForkDirtyOverlayUntrackedFile: an untracked, non-ignored file appears in the
// child via the ls-files --others copy step.
func TestForkDirtyOverlayUntrackedFile(t *testing.T) {
	requireGit(t)
	base := dirtyOverlayBase(t)
	writeFile(t, filepath.Join(base, "new", "untracked.txt"), "brand new\n")

	childRoot, cleanup := forkDirty(t, base, "ro-untracked")
	t.Cleanup(func() { _ = cleanup() })

	got, err := os.ReadFile(filepath.Join(childRoot, "new", "untracked.txt"))
	if err != nil {
		t.Fatalf("read child untracked file: %v", err)
	}
	if string(got) != "brand new\n" {
		t.Fatalf("child untracked file = %q, want %q", got, "brand new\n")
	}
}

// TestForkDirtyOverlayRespectsGitignore: a file matched by a committed .gitignore is
// untracked-but-IGNORED, so ls-files --others --exclude-standard omits it and it must
// NOT appear in the child.
func TestForkDirtyOverlayRespectsGitignore(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	initGitRepo(t, base)
	writeFile(t, filepath.Join(base, "tracked.txt"), "v1\n")
	writeFile(t, filepath.Join(base, ".gitignore"), "*.log\n")
	gitCommit(t, base)

	// An ignored file (matches *.log) — untracked AND ignored.
	writeFile(t, filepath.Join(base, "debug.log"), "secret noise\n")

	childRoot, cleanup := forkDirty(t, base, "ro-ignored")
	t.Cleanup(func() { _ = cleanup() })

	if _, err := os.Stat(filepath.Join(childRoot, "debug.log")); !os.IsNotExist(err) {
		t.Fatalf("ignored file debug.log leaked into child (err=%v); overlay must respect .gitignore", err)
	}
}

// TestForkDirtyOverlayDeletedFile: an uncommitted deletion of a tracked file is
// reflected — the file is gone in the child (diff HEAD carries the deletion hunk).
func TestForkDirtyOverlayDeletedFile(t *testing.T) {
	requireGit(t)
	base := dirtyOverlayBase(t)
	if err := os.Remove(filepath.Join(base, "tracked.txt")); err != nil {
		t.Fatalf("remove tracked.txt in parent: %v", err)
	}

	childRoot, cleanup := forkDirty(t, base, "ro-deleted")
	t.Cleanup(func() { _ = cleanup() })

	if _, err := os.Stat(filepath.Join(childRoot, "tracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted file still present in child (err=%v); overlay must reproduce the deletion", err)
	}
}

// TestForkDirtyOverlayBinaryFile: a modified binary tracked file's new bytes are
// present in the child — proves the --binary flag round-trips binary content.
func TestForkDirtyOverlayBinaryFile(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	initGitRepo(t, base)
	orig := []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0x00, 0x10}
	if err := os.WriteFile(filepath.Join(base, "blob.bin"), orig, 0o644); err != nil {
		t.Fatalf("write blob.bin: %v", err)
	}
	gitCommit(t, base)

	modified := []byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x42, 0x99, 0x7f}
	if err := os.WriteFile(filepath.Join(base, "blob.bin"), modified, 0o644); err != nil {
		t.Fatalf("modify blob.bin: %v", err)
	}

	childRoot, cleanup := forkDirty(t, base, "ro-binary")
	t.Cleanup(func() { _ = cleanup() })

	got, err := os.ReadFile(filepath.Join(childRoot, "blob.bin"))
	if err != nil {
		t.Fatalf("read child blob.bin: %v", err)
	}
	if !bytes.Equal(got, modified) {
		t.Fatalf("child blob.bin = %v, want the modified bytes %v (--binary should round-trip)", got, modified)
	}
}

// TestForkDirtyOverlaySkipsUntrackedSymlink: an untracked symlink is NOT copied into
// the child (mirrors copyTree's symlink discipline — a symlink could point outside
// the base and break isolation).
func TestForkDirtyOverlaySkipsUntrackedSymlink(t *testing.T) {
	requireGit(t)
	base := dirtyOverlayBase(t)
	// Untracked symlink pointing at the committed file.
	link := filepath.Join(base, "link-to-tracked")
	if err := os.Symlink("tracked.txt", link); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	childRoot, cleanup := forkDirty(t, base, "ro-symlink")
	t.Cleanup(func() { _ = cleanup() })

	if _, err := os.Lstat(filepath.Join(childRoot, "link-to-tracked")); !os.IsNotExist(err) {
		t.Fatalf("untracked symlink was copied into child (err=%v); overlay must skip symlinks", err)
	}
}

// TestForkDirtyOverlayCleanTreeNoOp: on a clean repo the child equals HEAD, its status
// is clean, and — the load-bearing assertion — the overlay runs EXACTLY the one cheap
// `git status --porcelain` probe and NO diff/apply/ls-files. The probe count is captured
// by wrapping the overlay's git runner (CountOverlayGitForTest), so a regression that
// removed the short-circuit (running diff+apply+ls-files on a clean tree) is caught: the
// count would jump from 1 to 4. This genuinely pins the cheap-path guarantee item 2 asks
// for, instead of merely describing it.
func TestForkDirtyOverlayCleanTreeNoOp(t *testing.T) {
	requireGit(t)
	base := dirtyOverlayBase(t) // committed, then left clean

	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}
	f := forker.New(osfsWorkspace, forker.WithDirtyOverlay())

	var (
		childRoot string
		cleanup   func() error
		advisory  string
		ferr      error
	)
	overlayCalls := forker.CountOverlayGitForTest(func() {
		var child interface{ Root() string }
		var c func() error
		child, c, advisory, ferr = forkWorkspace(f, context.Background(), baseWS, "ro-clean")
		if ferr == nil {
			childRoot = child.Root()
			cleanup = c
		}
	})
	if ferr != nil {
		t.Fatalf("Fork: %v", ferr)
	}
	t.Cleanup(func() { _ = cleanup() })

	// Cheap-path short-circuit: exactly ONE overlay git call (the status probe), nothing
	// more. A regression that dropped the clean-tree short-circuit would run diff + apply
	// + ls-files and this count would be 4.
	if len(overlayCalls) != 1 {
		t.Fatalf("clean-tree overlay ran %d git command(s) %v, want exactly 1 (the status --porcelain probe)", len(overlayCalls), overlayCalls)
	}
	if len(overlayCalls[0]) < 1 || overlayCalls[0][0] != "status" {
		t.Fatalf("the single overlay git call was %v, want a `status` probe", overlayCalls[0])
	}
	// A clean tree degrades nothing — no advisory.
	if advisory != "" {
		t.Fatalf("clean tree returned a non-empty advisory %q; want empty", advisory)
	}

	// The committed file is present and unchanged.
	got, err := os.ReadFile(filepath.Join(childRoot, "tracked.txt"))
	if err != nil {
		t.Fatalf("read child tracked.txt: %v", err)
	}
	if string(got) != "v1\n" {
		t.Fatalf("child tracked.txt = %q, want the clean HEAD %q", got, "v1\n")
	}
	// The child worktree's own status is clean — no overlay artifacts.
	if out := gitOutput(t, childRoot, "status", "--porcelain"); out != "" {
		t.Fatalf("child worktree not clean after no-op overlay: %q", out)
	}
}

// TestForkDirtyOverlayIsolation: after the overlay, writes/edits in the child do NOT
// change the parent working tree or the parent index. The parent's porcelain status
// and file contents are identical before and after the child mutates.
func TestForkDirtyOverlayIsolation(t *testing.T) {
	requireGit(t)
	base := dirtyOverlayBase(t)
	writeFile(t, filepath.Join(base, "tracked.txt"), "v2-DIRTY\n")
	writeFile(t, filepath.Join(base, "untracked.txt"), "u1\n")

	parentStatusBefore := gitOutput(t, base, "status", "--porcelain")
	parentTrackedBefore, _ := os.ReadFile(filepath.Join(base, "tracked.txt"))

	childRoot, cleanup := forkDirty(t, base, "ro-iso")
	t.Cleanup(func() { _ = cleanup() })

	// Mutate everything in the child: edit the overlaid tracked file, the untracked
	// file, and add a new file.
	writeFile(t, filepath.Join(childRoot, "tracked.txt"), "CHILD-EDIT\n")
	writeFile(t, filepath.Join(childRoot, "untracked.txt"), "CHILD-EDIT\n")
	writeFile(t, filepath.Join(childRoot, "child-new.txt"), "child only\n")

	// Parent working tree + index unchanged.
	if got := gitOutput(t, base, "status", "--porcelain"); got != parentStatusBefore {
		t.Fatalf("parent status changed after child mutation:\nbefore=%q\nafter=%q", parentStatusBefore, got)
	}
	parentTrackedAfter, _ := os.ReadFile(filepath.Join(base, "tracked.txt"))
	if !bytes.Equal(parentTrackedBefore, parentTrackedAfter) {
		t.Fatalf("parent tracked.txt mutated by child: before=%q after=%q", parentTrackedBefore, parentTrackedAfter)
	}
	if _, err := os.Stat(filepath.Join(base, "child-new.txt")); !os.IsNotExist(err) {
		t.Fatalf("child's new file leaked into parent (err=%v)", err)
	}
}

// TestForkDirtyOverlayFailSoft (ADVERSARIAL): induce a `git apply` failure by wrapping
// the runGit seam so that AFTER `git worktree add` succeeds, a conflicting version of
// the modified file is written into the child dir. The dirty-overlay's `git apply`
// then fails to apply the parent's diff HEAD; the overlay must fail-soft — reset the
// worktree to pristine HEAD and let Fork SUCCEED with a usable clean worktree.
func TestForkDirtyOverlayFailSoft(t *testing.T) {
	requireGit(t)
	base := dirtyOverlayBase(t)
	// Parent has a tracked modification, so an overlay WILL try to apply a patch.
	writeFile(t, filepath.Join(base, "tracked.txt"), "v2-DIRTY\n")

	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}
	f := forker.New(osfsWorkspace, forker.WithDirtyOverlay())
	realRunGit := f.RunGitForTest()
	f.SetRunGitForTest(func(ctx context.Context, dir string, args ...string) error {
		err := realRunGit(ctx, dir, args...)
		// After the worktree is created, write a CONFLICTING file into the child so the
		// subsequent `git apply HEAD-diff` fails (the patch's HEAD context no longer
		// matches the working file). worktree add's last positional arg is the childDir.
		if err == nil && len(args) >= 2 && args[0] == "worktree" && args[1] == "add" {
			childDir := args[len(args)-2] // ... "--detach" <childDir> "HEAD"
			_ = os.WriteFile(filepath.Join(childDir, "tracked.txt"), []byte("CONFLICT-DIFFERENT\n"), 0o644)
		}
		return err
	})

	child, cleanup, advisory, ferr := forkWorkspace(f, context.Background(), baseWS, "ro-failsoft")
	if ferr != nil {
		t.Fatalf("Fork must succeed despite overlay failure, got: %v", ferr)
	}
	t.Cleanup(func() { _ = cleanup() })

	// The degraded path returns a NON-EMPTY advisory: the tree was dirty but the
	// overlay failed, so the child sees committed HEAD only and must be told.
	if advisory == "" {
		t.Fatal("degraded overlay (dirty tree + failed apply) returned an EMPTY advisory; the child must be told it sees committed HEAD only")
	}
	if !strings.Contains(advisory, "committed HEAD") {
		t.Errorf("degraded-fork advisory does not explain the degradation; got: %q", advisory)
	}

	// Fail-soft floor: the worktree is reset to pristine HEAD (the conflicting file is
	// gone, HEAD content restored, status clean). The overlay did not propagate.
	got, rerr := os.ReadFile(filepath.Join(child.Root(), "tracked.txt"))
	if rerr != nil {
		t.Fatalf("read child tracked.txt: %v", rerr)
	}
	if string(got) != "v1\n" {
		t.Fatalf("child tracked.txt = %q, want pristine HEAD %q after fail-soft reset", got, "v1\n")
	}
	if out := gitOutput(t, child.Root(), "status", "--porcelain"); out != "" {
		t.Fatalf("child not reset to clean after overlay failure: %q", out)
	}
}

// TestForkDirtyOverlayUsesScrubbedEnv: install a malicious post-checkout hook AND a
// core.pager sentinel in the parent, make the tree dirty, then Fork with overlay. The
// NEW runGitCapture invocations (status/diff/apply/ls-files) must carry the scrubbed,
// git-neutralizing env — no sentinel fires. Modelled on TestForkNeutralizesRepoHooks.
func TestForkDirtyOverlayUsesScrubbedEnv(t *testing.T) {
	requireGit(t)
	base := dirtyOverlayBase(t)
	writeFile(t, filepath.Join(base, "tracked.txt"), "v2-DIRTY\n")

	// post-checkout hook sentinel (fires on worktree add's checkout unless neutralised).
	hookSentinel := filepath.Join(t.TempDir(), "hook-fired")
	hookDir := filepath.Join(base, ".git", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hookDir, "post-checkout"),
		[]byte("#!/bin/sh\ntouch "+hookSentinel+"\n"), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	// core.pager sentinel: git would spawn this pager for status/diff/log unless
	// core.pager is forced to cat by the neutralizing env.
	pagerSentinel := filepath.Join(t.TempDir(), "pager-fired")
	runGit(t, base, "config", "core.pager", "touch "+pagerSentinel+"; cat")

	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}
	f := forker.New(osfsWorkspace, forker.WithDirtyOverlay())
	child, cleanup, _, ferr := forkWorkspace(f, context.Background(), baseWS, "ro-scrub")
	if ferr != nil {
		t.Fatalf("Fork: %v", ferr)
	}
	t.Cleanup(func() { _ = cleanup() })

	// Sanity: the overlay actually ran (the dirty file is present).
	got, _ := os.ReadFile(filepath.Join(child.Root(), "tracked.txt"))
	if string(got) != "v2-DIRTY\n" {
		t.Fatalf("overlay did not apply the dirty change (got %q); test premise broken", got)
	}
	if _, statErr := os.Stat(hookSentinel); statErr == nil {
		t.Fatalf("post-checkout hook FIRED — overlay git env is not neutralized")
	}
	if _, statErr := os.Stat(pagerSentinel); statErr == nil {
		t.Fatalf("core.pager FIRED during overlay — overlay git env is not neutralized")
	}
}

// TestForkDirtyOverlayConcurrent: fork the same dirty repo N times in parallel with
// the overlay; each child independently shows the dirty state and the parent is
// untouched (read-parallel safety).
func TestForkDirtyOverlayConcurrent(t *testing.T) {
	requireGit(t)
	base := dirtyOverlayBase(t)
	writeFile(t, filepath.Join(base, "tracked.txt"), "v2-DIRTY\n")
	writeFile(t, filepath.Join(base, "untracked.txt"), "u-new\n")

	parentStatusBefore := gitOutput(t, base, "status", "--porcelain")

	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}
	f := forker.New(osfsWorkspace, forker.WithDirtyOverlay())

	const n = 5
	var wg sync.WaitGroup
	roots := make([]string, n)
	cleanups := make([]func() error, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			child, cleanup, _, ferr := forkWorkspace(f, context.Background(), baseWS, "conc")
			if ferr != nil {
				errs[i] = ferr
				return
			}
			roots[i] = child.Root()
			cleanups[i] = cleanup
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if cleanups[i] != nil {
			defer func(cl func() error) { _ = cl() }(cleanups[i])
		}
	}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("fork %d: %v", i, errs[i])
		}
		gotTracked, _ := os.ReadFile(filepath.Join(roots[i], "tracked.txt"))
		if string(gotTracked) != "v2-DIRTY\n" {
			t.Fatalf("fork %d child tracked.txt = %q, want %q", i, gotTracked, "v2-DIRTY\n")
		}
		gotUntracked, _ := os.ReadFile(filepath.Join(roots[i], "untracked.txt"))
		if string(gotUntracked) != "u-new\n" {
			t.Fatalf("fork %d child untracked.txt = %q, want %q", i, gotUntracked, "u-new\n")
		}
	}
	if got := gitOutput(t, base, "status", "--porcelain"); got != parentStatusBefore {
		t.Fatalf("parent status changed after concurrent forks:\nbefore=%q\nafter=%q", parentStatusBefore, got)
	}
}

// TestForkForceCopyWithDirtyOverlayIsInert pins the "force-copy wins by call site"
// guarantee: New(ws, WithForceCopy(), WithDirtyOverlay()) over a DIRTY repo takes the
// COPY path (not the worktree+overlay path), so the dirty state arrives via copyTree
// ONCE — not copyTree PLUS an overlay re-apply. Proof: the child sees the dirty state,
// its `.git` is a real DIRECTORY (copy path; a worktree would be a gitlink FILE), the
// overlay runs ZERO git commands (CountOverlayGitForTest == 0), and no advisory.
func TestForkForceCopyWithDirtyOverlayIsInert(t *testing.T) {
	requireGit(t)
	base := dirtyOverlayBase(t)
	writeFile(t, filepath.Join(base, "tracked.txt"), "v2-DIRTY\n")
	writeFile(t, filepath.Join(base, "untracked.txt"), "u-new\n")

	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}
	// Both options set: force-copy must win, the overlay must never run.
	f := forker.New(osfsWorkspace, forker.WithForceCopy(), forker.WithDirtyOverlay())

	var (
		childRoot string
		cleanup   func() error
		advisory  string
		ferr      error
	)
	overlayCalls := forker.CountOverlayGitForTest(func() {
		var child interface{ Root() string }
		var c func() error
		child, c, advisory, ferr = forkWorkspace(f, context.Background(), baseWS, "fc-overlay")
		if ferr == nil {
			childRoot = child.Root()
			cleanup = c
		}
	})
	if ferr != nil {
		t.Fatalf("Fork: %v", ferr)
	}
	t.Cleanup(func() { _ = cleanup() })

	// The overlay never ran: force-copy took the copy path, which never calls the
	// overlay's git runner at all.
	if len(overlayCalls) != 0 {
		t.Fatalf("force-copy + WithDirtyOverlay ran %d overlay git command(s) %v, want 0 (force-copy wins, overlay never runs)", len(overlayCalls), overlayCalls)
	}
	if advisory != "" {
		t.Fatalf("force-copy path returned a non-empty advisory %q; want empty", advisory)
	}

	// .git is a real DIRECTORY (a self-contained copied repo), proving the copy path —
	// a worktree would leave a gitlink FILE.
	gitInfo, statErr := os.Stat(filepath.Join(childRoot, ".git"))
	if statErr != nil {
		t.Fatalf("force-copy fork missing .git: %v", statErr)
	}
	if !gitInfo.IsDir() {
		t.Fatalf("force-copy fork .git must be a directory (copy path), got a gitlink file (worktree)")
	}

	// The dirty state arrived via copyTree exactly once: the modified tracked file and
	// the untracked file are both present with their dirty content.
	gotTracked, _ := os.ReadFile(filepath.Join(childRoot, "tracked.txt"))
	if string(gotTracked) != "v2-DIRTY\n" {
		t.Fatalf("child tracked.txt = %q, want the dirty %q (carried by copyTree)", gotTracked, "v2-DIRTY\n")
	}
	gotUntracked, _ := os.ReadFile(filepath.Join(childRoot, "untracked.txt"))
	if string(gotUntracked) != "u-new\n" {
		t.Fatalf("child untracked.txt = %q, want %q", gotUntracked, "u-new\n")
	}
}

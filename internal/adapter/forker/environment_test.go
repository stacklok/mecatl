package forker_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/forker"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// forkWorkspace keeps the existing adapter behavior tests focused on the child
// filesystem while adapting them to the EnvironmentForker API. Dedicated
// environment tests assert the child ref and bound runner.
func forkWorkspace(f *forker.Forker, ctx context.Context, base tool.Workspace, label string) (tool.Workspace, func() error, string, error) { //nolint:revive // context.Context is not first to mirror the forker.Fork signature this wraps
	baseEnv := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: base.Root()}, base, memledger.New(), nil)
	child, cleanup, advisory, err := f.Fork(ctx, baseEnv, label)
	if err != nil {
		return nil, cleanup, advisory, err
	}
	return child.Workspace(), cleanup, advisory, nil
}

func mergeWorkspaces(m tool.EnvironmentMerger, ctx context.Context, child, parent tool.Workspace) error { //nolint:revive // context.Context is not first to mirror the merger.Merge signature this wraps
	childEnv := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: child.Root()}, child, memledger.New(), nil)
	parentEnv := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: parent.Root()}, parent, memledger.New(), nil)
	return m.Merge(ctx, childEnv, parentEnv)
}

func mergeRoot(m tool.EnvironmentMerger, ctx context.Context, childRoot string, parent tool.Workspace) error { //nolint:revive // context.Context is not first to mirror the mergeWorkspaces helper shape
	child, err := osfs.NewWorkspace(childRoot)
	if err != nil {
		return err
	}
	return mergeWorkspaces(m, ctx, child, parent)
}

func TestPersistentReadLedgers_ForkPreservesContentBackendAndFreshLedger(t *testing.T) {
	root := t.TempDir()
	ws, err := osfs.NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	parentLedger := memledger.New()
	parent := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: root}, ws, parentLedger, nil)
	f := forker.New(func(childRoot string) (tool.Workspace, error) { return osfs.NewWorkspace(childRoot) }, forker.WithForceCopy())

	child, cleanup, _, err := f.Fork(context.Background(), parent, "fresh-ledger")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })
	if child.Workspace().Root() == parent.Workspace().Root() {
		t.Fatal("fork child must retain its isolated content backend")
	}
	if child.ReadLedger() == nil || child.ReadLedger() == parent.ReadLedger() {
		t.Fatal("fork child must receive a fresh non-parent ledger")
	}
}

// TestForkReturnsEnvironmentWithBoundRunner proves the EnvironmentForker returns
// a COMPLETE child Environment whose Workspace AND command runner share the
// child namespace (issue #462): a relative-path shell write through the child's
// runner lands in the CHILD fork, never the parent base.
func TestForkReturnsEnvironmentWithBoundRunner(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "seed.txt"), []byte("base"), 0o644); err != nil {
		t.Fatalf("seed base: %v", err)
	}
	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base ws: %v", err)
	}
	baseEnv := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: base}, baseWS, memledger.New(), nil)

	// WithRunner wires a bound runner for each child directory. The runner is
	// bound to the child root so its cwd follows the fork.
	f := forker.New(func(root string) (tool.Workspace, error) {
		return osfs.NewWorkspace(root)
	},
		forker.WithRunner(func(childRoot string) tool.CommandRunner {
			r, rerr := osfs.NewCommandRunnerShell(childRoot, "/bin/sh")
			if rerr != nil {
				t.Fatalf("runner for %s: %v", childRoot, rerr)
			}
			return r
		}))

	ctx := context.Background()
	child, cleanup, _, ferr := f.Fork(ctx, baseEnv, "t")
	if ferr != nil {
		t.Fatalf("Fork: %v", ferr)
	}
	defer cleanup()

	// The child Environment carries the child namespace ref and a non-nil runner.
	if child.CommandRunner() == nil {
		t.Fatal("child Environment must have a bound runner")
	}
	childRoot := child.Workspace().Root()
	// A relative-path write through the child's runner lands in the child fork.
	if _, err := child.CommandRunner().Run(ctx, "echo forked > child.txt"); err != nil {
		t.Fatalf("child runner Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(childRoot, "child.txt")); err != nil {
		t.Errorf("child write did not land in the child fork: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "child.txt")); !os.IsNotExist(err) {
		t.Errorf("child write leaked into the parent base (runner not bound to child)")
	}
}

// TestForkFallbackCopyAffinity proves the worktree-failure→copy fallback binds
// Workspace, Ref, AND runner to the ACTUAL copy root (issue #462 phase-2 finding
// #1): when a git worktree fails and the forker falls back to a recursive copy,
// the copy lives in a FRESH directory distinct from the one Fork initially
// reserved. The child Environment must derive its ref ID and bound runner root
// from ws.Root() — the copy root — never from the discarded reservation. This
// test forces the fallback by injecting a runGit that always fails the worktree
// add, then asserts: Ref.ID == Workspace.Root, Ref.Kind == local, a relative
// Bash write lands in the copy root, cleanup removes it, and no stray extra
// child directory remains in the temp base.
func TestForkFallbackCopyAffinity(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "seed.txt"), []byte("base"), 0o644); err != nil {
		t.Fatalf("seed base: %v", err)
	}
	// Initialise a git repo at base so Fork attempts the worktree path (which the
	// injected runGit will then force to fail, exercising the copy fallback).
	if err := forker.GitForTest(context.Background(), base, "init"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := forker.GitForTest(context.Background(), base, "config", "user.email", "test@mecatl.dev"); err != nil {
		t.Fatalf("git config email: %v", err)
	}
	if err := forker.GitForTest(context.Background(), base, "config", "user.name", "Test"); err != nil {
		t.Fatalf("git config name: %v", err)
	}
	if err := forker.GitForTest(context.Background(), base, "add", "-A"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := forker.GitForTest(context.Background(), base, "commit", "-m", "seed"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base ws: %v", err)
	}
	baseEnv := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: base}, baseWS, memledger.New(), nil)

	// tmpBase scopes the child directories so the test can enumerate them and
	// assert no stray reservation remains after the fallback.
	tmpBase := t.TempDir()
	var runnerRoots []string
	f := forker.New(
		func(root string) (tool.Workspace, error) {
			return osfs.NewWorkspace(root)
		},
		forker.WithTempBase(tmpBase),
		forker.WithRunner(func(childRoot string) tool.CommandRunner {
			runnerRoots = append(runnerRoots, childRoot)
			r, rerr := osfs.NewCommandRunnerShell(childRoot, "/bin/sh")
			if rerr != nil {
				t.Fatalf("runner for %s: %v", childRoot, rerr)
			}
			return r
		}),
	)
	// Force the worktree-add to fail so Fork falls back to a copy. The fallback
	// mints a fresh directory (distinct from the initial reservation).
	f.SetRunGitForTest(func(ctx context.Context, dir string, args ...string) error {
		if len(args) > 0 && args[0] == "worktree" {
			return errors.New("forced worktree failure")
		}
		return forker.GitForTest(ctx, dir, args...)
	})

	ctx := context.Background()
	child, cleanup, _, ferr := f.Fork(ctx, baseEnv, "fallback")
	if ferr != nil {
		t.Fatalf("Fork: %v", ferr)
	}
	defer cleanup()

	// Ref.ID must equal the Workspace root (the copy root), and Kind is local.
	ref := child.Ref()
	childRoot := child.Workspace().Root()
	if ref.ID != childRoot {
		t.Errorf("Ref.ID = %q, want Workspace.Root() %q (ref must derive from the copy root, not the discarded reservation)", ref.ID, childRoot)
	}
	if ref.Kind != session.EnvKindLocal {
		t.Errorf("Ref.Kind = %q, want %q", ref.Kind, session.EnvKindLocal)
	}
	// The runner must have been bound to the SAME copy root.
	if len(runnerRoots) != 1 {
		t.Fatalf("expected exactly 1 runner bound, got %d", len(runnerRoots))
	}
	if runnerRoots[0] != childRoot {
		t.Errorf("runner bound to %q, want copy root %q", runnerRoots[0], childRoot)
	}
	// A relative Bash write through the child runner lands in the copy root.
	if _, err := child.CommandRunner().Run(ctx, "echo forked > child.txt"); err != nil {
		t.Fatalf("child runner Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(childRoot, "child.txt")); err != nil {
		t.Errorf("child write did not land in the copy root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "child.txt")); !os.IsNotExist(err) {
		t.Errorf("child write leaked into the parent base")
	}
	// Cleanup removes the copy root.
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(childRoot); !os.IsNotExist(err) {
		t.Errorf("cleanup did not remove the copy root %q", childRoot)
	}
	// No stray extra child directory remains in the temp base: the discarded
	// reservation was removed, and only the (now-removed) copy existed.
	entries, err := os.ReadDir(tmpBase)
	if err != nil {
		t.Fatalf("read tmpBase: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected no stray child directories in tmpBase, got %v", names)
	}
}

// TestForkWithoutRunnerIsShellLess proves a forker constructed without WithRunner
// mints shell-less children (the child's Bash surfaces ErrNoShell honestly).
func TestForkWithoutRunnerIsShellLess(t *testing.T) {
	base := t.TempDir()
	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base ws: %v", err)
	}
	baseEnv := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: base}, baseWS, memledger.New(), nil)
	f := forker.New(func(root string) (tool.Workspace, error) {
		return osfs.NewWorkspace(root)
	})

	child, cleanup, _, ferr := f.Fork(context.Background(), baseEnv, "t")
	if ferr != nil {
		t.Fatalf("Fork: %v", ferr)
	}
	defer cleanup()
	if child.CommandRunner() != nil {
		t.Fatal("a forker without WithRunner must mint shell-less children")
	}
}

// TestMergerRejectsNilWorkspace proves the EnvironmentMerger rejects a zero/nil
// child OR parent Workspace with a normal error, never a nil-pointer panic. A
// zero-value tool.Environment{} has a nil Workspace; calling .Root() on it
// would panic, so the merger must guard at the boundary (issue #462 review).
func TestMergerRejectsNilWorkspace(t *testing.T) {
	m := forker.NewMerger()
	ctx := context.Background()

	// Nil child Workspace (zero-value Environment).
	var zeroChild tool.Environment
	ws, err := osfs.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("parent ws: %v", err)
	}
	parentEnv := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: ws.Root()}, ws, memledger.New(), nil)
	if err := m.Merge(ctx, zeroChild, parentEnv); err == nil {
		t.Fatal("Merge with nil child Workspace must return an error, not nil")
	}

	// Nil parent Workspace (zero-value Environment).
	var zeroParent tool.Environment
	childEnv := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: ws.Root()}, ws, memledger.New(), nil)
	if err := m.Merge(ctx, childEnv, zeroParent); err == nil {
		t.Fatal("Merge with nil parent Workspace must return an error, not nil")
	}

	// Both nil — must not panic.
	if err := m.Merge(ctx, zeroChild, zeroParent); err == nil {
		t.Fatal("Merge with both Workspaces nil must return an error, not nil")
	}
}

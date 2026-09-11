package remoteenv_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/remoteenv"
)

// TestRunnerRejectsTraversalAndAbsolutePaths (issue #462 phase-3 finding #4)
// proves the runner's path normalization is ALIGNED with the Workspace's
// cleanPath: a `cat`/`write` carrying a ".." traversal or an absolute path is
// rejected (exit 2), never silently normalized to a namespace-escaping key. The
// runner protocol addresses the SAME namespace the file API does, so its paths
// obey the SAME escape discipline — a path that escapes the namespace root is
// never silently normalized away.
func TestRunnerRejectsTraversalAndAbsolutePaths(t *testing.T) {
	ctx := context.Background()
	b := remoteenv.NewBackend()
	env, err := b.NewEnvironment("main")
	if err != nil {
		t.Fatalf("NewEnvironment: %v", err)
	}
	ws, runner := env.Workspace(), env.CommandRunner()
	mustCreate(t, ws, "seed.txt", "x")

	for _, tc := range []struct {
		name string
		cmd  string
	}{
		{"cat dotdot", "cat ../escape.txt"},
		{"cat nested dotdot", "cat a/../../escape.txt"},
		{"write dotdot", "write ../escape.txt pwned"},
		{"write nested dotdot", "write a/../../escape.txt pwned"},
		{"write deep dotdot", "write a/b/../../../escape.txt pwned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := runner.Run(ctx, tc.cmd)
			if err != nil {
				t.Fatalf("runner.Run(%q): %v", tc.cmd, err)
			}
			if res.ExitCode == 0 {
				t.Fatalf("runner.Run(%q) succeeded (exit 0); want a non-zero exit (escape rejected)", tc.cmd)
			}
			if !strings.Contains(res.Stderr, "escapes namespace root") {
				t.Fatalf("runner.Run(%q) stderr = %q; want one naming an escape", tc.cmd, res.Stderr)
			}
		})
	}

	// A relative path that stays inside the namespace is accepted and reads the
	// seed (the alignment does NOT break the legitimate protocol). A leading
	// slash is stripped by the documented protocol, so `cat /seed.txt` reads the
	// same key `cat seed.txt` does (the runner has no real filesystem; the path
	// addresses the namespace, not the OS root).
	res, err := runner.Run(ctx, "cat seed.txt")
	if err != nil {
		t.Fatalf("runner cat seed.txt: %v", err)
	}
	if res.ExitCode != 0 || res.Stdout != "x" {
		t.Fatalf("runner cat seed.txt = %+v; want exit 0 stdout %q", res, "x")
	}
	res, err = runner.Run(ctx, "cat /seed.txt")
	if err != nil {
		t.Fatalf("runner cat /seed.txt: %v", err)
	}
	if res.ExitCode != 0 || res.Stdout != "x" {
		t.Fatalf("runner cat /seed.txt = %+v; want exit 0 stdout %q (leading slash is stripped)", res, "x")
	}
}

// mustCreate writes a new file via CreateFile, failing the test on error.
func mustCreate(t *testing.T, ws tool.Workspace, path string, content string) tool.FileVersion {
	t.Helper()
	v, err := ws.CreateFile(context.Background(), path, []byte(content))
	if err != nil {
		t.Fatalf("CreateFile %q: %v", path, err)
	}
	return v
}

// mustRead reads a file, failing the test on error or a content mismatch.
func mustRead(t *testing.T, ws tool.Workspace, path, want string) {
	t.Helper()
	got, err := ws.Read(context.Background(), path)
	if err != nil {
		t.Fatalf("Read %q: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("Read %q = %q, want %q", path, got, want)
	}
}

// TestFileAPIAndRunnerObserveSameNamespace proves the file API write/read and
// the fake Shell runner observe the SAME namespace in BOTH directions: a file
// written via CreateFile is visible to `cat`, and a file written via `write`
// (the runner protocol) is visible to Read.
func TestFileAPIAndRunnerObserveSameNamespace(t *testing.T) {
	b := remoteenv.NewBackend()
	env, err := b.NewEnvironment("main")
	if err != nil {
		t.Fatalf("NewEnvironment: %v", err)
	}
	ws, runner := env.Workspace(), env.CommandRunner()
	if runner == nil {
		t.Fatal("runner is nil")
	}

	// File API -> runner: write via CreateFile, read via `cat`.
	mustCreate(t, ws, "from-api.txt", "hello from api")
	res, err := runner.Run(context.Background(), "cat from-api.txt")
	if err != nil {
		t.Fatalf("runner cat: %v", err)
	}
	if res.Stdout != "hello from api" {
		t.Fatalf("runner cat stdout = %q, want %q", res.Stdout, "hello from api")
	}

	// Runner -> file API: write via `write`, read via Read.
	if _, err := runner.Run(context.Background(), "write from-runner.txt hello from runner"); err != nil {
		t.Fatalf("runner write: %v", err)
	}
	mustRead(t, ws, "from-runner.txt", "hello from runner")
}

// TestTwoHandlesSameIDStaleVersionConflict proves two handles to the same
// namespace produce a stale-version conflict: handle A reads a version, handle
// B mutates the file, then A's ReplaceFile with its stale version must mismatch.
func TestTwoHandlesSameIDStaleVersionConflict(t *testing.T) {
	b := remoteenv.NewBackend()
	env1, err := b.NewEnvironment("main")
	if err != nil {
		t.Fatalf("NewEnvironment: %v", err)
	}
	ref := env1.Ref()

	// Resolve a SECOND handle to the same namespace.
	env2, err := b.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	ws1, ws2 := env1.Workspace(), env2.Workspace()

	// Seed a file and read its version through handle 1.
	mustCreate(t, ws1, "doc.txt", "v1")
	_, v1, err := ws1.ReadVersion(context.Background(), "doc.txt")
	if err != nil {
		t.Fatalf("ReadVersion: %v", err)
	}

	// Handle 2 replaces the file (new version).
	if _, err := ws2.ReplaceFile(context.Background(), "doc.txt", v1, []byte("v2-by-2")); err != nil {
		t.Fatalf("handle2 ReplaceFile: %v", err)
	}

	// Handle 1's ReplaceFile with its stale v1 must mismatch.
	_, err = ws1.ReplaceFile(context.Background(), "doc.txt", v1, []byte("v3-by-1"))
	var mm *tool.VersionMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("handle1 stale ReplaceFile: want *tool.VersionMismatchError, got %v", err)
	}

	// Handle 1 re-reads and succeeds against the current version.
	_, v2, err := ws1.ReadVersion(context.Background(), "doc.txt")
	if err != nil {
		t.Fatalf("ReadVersion after mismatch: %v", err)
	}
	if _, err := ws1.ReplaceFile(context.Background(), "doc.txt", v2, []byte("v3-by-1")); err != nil {
		t.Fatalf("handle1 ReplaceFile after re-read: %v", err)
	}
	mustRead(t, ws1, "doc.txt", "v3-by-1")
}

// TestForkChildMatchingRefAndIsolated proves the fork returns a complete child
// Environment with matching Ref Kind, a non-nil Workspace+runner bound to the
// child namespace, and isolation: writes through the child do not affect the
// parent, and the child's Shell observes its own namespace.
func TestForkChildMatchingRefAndIsolated(t *testing.T) {
	b := remoteenv.NewBackend()
	parent, err := b.NewEnvironment("main")
	if err != nil {
		t.Fatalf("NewEnvironment: %v", err)
	}
	mustCreate(t, parent.Workspace(), "shared.txt", "parent-seed")

	f := remoteenv.NewForker(b)
	child, cleanup, advisory, err := f.Fork(context.Background(), parent, "branch-1")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	defer func() { _ = cleanup() }()
	if advisory != "" {
		t.Fatalf("fork advisory = %q, want empty", advisory)
	}
	if child.Ref().Kind != remoteenv.Kind {
		t.Fatalf("child ref Kind = %q, want %q", child.Ref().Kind, remoteenv.Kind)
	}
	if child.Ref().ID == parent.Ref().ID {
		t.Fatal("child ref ID must differ from parent")
	}
	if child.Workspace() == nil {
		t.Fatal("child workspace is nil")
	}
	if child.CommandRunner() == nil {
		t.Fatal("child runner is nil")
	}

	// Child sees the parent's seed (copied).
	mustRead(t, child.Workspace(), "shared.txt", "parent-seed")

	// Child writes; parent must NOT see it.
	mustCreate(t, child.Workspace(), "child-only.txt", "child")
	mustRead(t, child.Workspace(), "child-only.txt", "child")
	if _, err := parent.Workspace().Read(context.Background(), "child-only.txt"); err == nil {
		t.Fatal("parent must NOT see child-only.txt (isolation broken)")
	}

	// Child's Shell observes its own namespace: `cat` the child-only file.
	res, err := child.CommandRunner().Run(context.Background(), "cat child-only.txt")
	if err != nil {
		t.Fatalf("child cat: %v", err)
	}
	if res.Stdout != "child" {
		t.Fatalf("child cat stdout = %q, want %q", res.Stdout, "child")
	}
}

// TestMergeAppliesChildChanges proves the merger applies the child's new and
// modified files to the parent by ref; a clean merge lands them.
func TestMergeAppliesChildChanges(t *testing.T) {
	b := remoteenv.NewBackend()
	parent, err := b.NewEnvironment("main")
	if err != nil {
		t.Fatalf("NewEnvironment: %v", err)
	}
	mustCreate(t, parent.Workspace(), "keep.txt", "parent")

	f := remoteenv.NewForker(b)
	child, cleanup, _, err := f.Fork(context.Background(), parent, "b")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	defer func() { _ = cleanup() }()

	// Child adds a new file and modifies the shared one.
	mustCreate(t, child.Workspace(), "added.txt", "from-child")
	if _, v, err := child.Workspace().ReadVersion(context.Background(), "keep.txt"); err != nil {
		t.Fatalf("ReadVersion keep.txt: %v", err)
	} else {
		if _, err := child.Workspace().ReplaceFile(context.Background(), "keep.txt", v, []byte("modified-by-child")); err != nil {
			t.Fatalf("ReplaceFile keep.txt: %v", err)
		}
	}

	m := remoteenv.NewMerger(b)
	if err := m.Merge(context.Background(), child, parent); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	mustRead(t, parent.Workspace(), "added.txt", "from-child")
	mustRead(t, parent.Workspace(), "keep.txt", "modified-by-child")
}

// TestMergeConflictPreservesChild proves a conflicting merge returns an error
// naming the conflict and leaves the child intact (recoverable). The parent
// keeps its own content for the conflicting path.
func TestMergeConflictPreservesChild(t *testing.T) {
	b := remoteenv.NewBackend()
	parent, err := b.NewEnvironment("main")
	if err != nil {
		t.Fatalf("NewEnvironment: %v", err)
	}
	mustCreate(t, parent.Workspace(), "both.txt", "parent-version")

	f := remoteenv.NewForker(b)
	child, cleanup, _, err := f.Fork(context.Background(), parent, "b")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	defer func() { _ = cleanup() }()

	// Parent mutates its copy AFTER the fork.
	if _, v, err := parent.Workspace().ReadVersion(context.Background(), "both.txt"); err != nil {
		t.Fatalf("parent ReadVersion: %v", err)
	} else {
		if _, err := parent.Workspace().ReplaceFile(context.Background(), "both.txt", v, []byte("parent-mutated")); err != nil {
			t.Fatalf("parent ReplaceFile: %v", err)
		}
	}
	// Child mutates its copy too (now divergent).
	if _, v, err := child.Workspace().ReadVersion(context.Background(), "both.txt"); err != nil {
		t.Fatalf("child ReadVersion: %v", err)
	} else {
		if _, err := child.Workspace().ReplaceFile(context.Background(), "both.txt", v, []byte("child-mutated")); err != nil {
			t.Fatalf("child ReplaceFile: %v", err)
		}
	}

	m := remoteenv.NewMerger(b)
	err = m.Merge(context.Background(), child, parent)
	if err == nil {
		t.Fatal("Merge: want conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("Merge error = %q, want one naming a conflict", err.Error())
	}
	// Parent keeps its own mutated content.
	mustRead(t, parent.Workspace(), "both.txt", "parent-mutated")
	// Child is intact (recoverable).
	mustRead(t, child.Workspace(), "both.txt", "child-mutated")
}

// TestSimulatedRestartReattachesSameState proves the restart-reattachment
// contract: after a simulated restart (the live Environment is dropped), a
// fresh Backend populated with the SAME namespace state resolves the persisted
// ref back to the SAME content. (The fake's Backend is the durable stand-in for
// a remote backend's persistent state; a real transport would survive the
// process. Here we prove the RESOLUTION path reattaches to the same state.)
func TestSimulatedRestartReattachesSameState(t *testing.T) {
	ctx := context.Background()
	b1 := remoteenv.NewBackend()
	env, err := b1.NewEnvironment("main")
	if err != nil {
		t.Fatalf("NewEnvironment: %v", err)
	}
	ref := env.Ref()
	mustCreate(t, env.Workspace(), "persisted.txt", "survives-restart")

	// Simulate a restart: drop the live Environment. A fresh Service/Resolver
	// would be built against a backend whose state persisted. The fake's
	// Backend is in-memory, so we construct a NEW Backend and re-seed it with
	// the same namespace state (the durable-stand-in for a remote backend's
	// persistence). This proves the RESOLUTION path resolves the persisted ref
	// to the same backend state.
	b2 := remoteenv.NewBackend()
	// Re-seed: copy the namespace's file map into the new backend under the
	// same id (a real transport reattaches without this; the fake simulates it).
	if err := b2.RehydrateForTest(ref.ID, env.Workspace()); err != nil {
		t.Fatalf("RehydrateForTest: %v", err)
	}

	reattached, err := b2.Resolve(ctx, ref)
	if err != nil {
		t.Fatalf("Resolve after restart: %v", err)
	}
	if reattached.Ref() != ref {
		t.Fatalf("reattached ref = %+v, want %+v", reattached.Ref(), ref)
	}
	mustRead(t, reattached.Workspace(), "persisted.txt", "survives-restart")

	// The reattached Environment's runner also observes the same state.
	res, err := reattached.CommandRunner().Run(ctx, "cat persisted.txt")
	if err != nil {
		t.Fatalf("reattached cat: %v", err)
	}
	if res.Stdout != "survives-restart" {
		t.Fatalf("reattached cat = %q, want %q", res.Stdout, "survives-restart")
	}
}

// TestRehydrateForTestRejectsNilSource (issue #462 phase-3 finding #5) proves
// RehydrateForTest rejects a nil source with ErrNilSource rather than panicking
// on a nil-dereference inside Glob/Read.
func TestRehydrateForTestRejectsNilSource(t *testing.T) {
	b := remoteenv.NewBackend()
	err := b.RehydrateForTest("ns-1", nil)
	if !errors.Is(err, remoteenv.ErrNilSource) {
		t.Fatalf("RehydrateForTest(nil) = %v, want ErrNilSource", err)
	}
}

// TestRehydrateForTestRefusesClobber (issue #462 phase-3 finding #5) proves
// RehydrateForTest refuses to overwrite an EXISTING namespace, returning
// ErrNamespaceExists rather than silently clobbering the live state. A second
// rehydrate over the same id would otherwise mask the live namespace's state
// and hide a real bug.
func TestRehydrateForTestRefusesClobber(t *testing.T) {
	ctx := context.Background()
	b1 := remoteenv.NewBackend()
	env, err := b1.NewEnvironment("main")
	if err != nil {
		t.Fatalf("NewEnvironment: %v", err)
	}
	ref := env.Ref()
	mustCreate(t, env.Workspace(), "a.txt", "original")

	b2 := remoteenv.NewBackend()
	if err := b2.RehydrateForTest(ref.ID, env.Workspace()); err != nil {
		t.Fatalf("first RehydrateForTest: %v", err)
	}
	// A second rehydrate over the SAME id must refuse to clobber.
	err = b2.RehydrateForTest(ref.ID, env.Workspace())
	if !errors.Is(err, remoteenv.ErrNamespaceExists) {
		t.Fatalf("second RehydrateForTest = %v, want ErrNamespaceExists", err)
	}
	// The original rehydrated state is intact (not clobbered).
	reattached, err := b2.Resolve(ctx, ref)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	mustRead(t, reattached.Workspace(), "a.txt", "original")
}

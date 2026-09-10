package redisstore_test

import (
	"context"
	"errors"
	"io/fs"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/adapter/fsconformance"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
)

func TestRedisWorkspaceConformance(t *testing.T) {
	fsconformance.Run(t, func(t *testing.T) tool.Workspace {
		mr := miniredis.RunT(t)
		st, err := redisstore.New(mr.Addr())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		ws, err := st.CreateWorkspace(t.Context(), "conformance")
		if err != nil {
			t.Fatal(err)
		}
		return ws
	})
}

// TestRedisWorkspaceNamespaceConformance runs the shared WorkspaceNamespace
// conformance table (ReadDir/Remove/Rename/CopyFile) against the Redis-backed
// Workspace over a fresh miniredis instance per case.
func TestRedisWorkspaceNamespaceConformance(t *testing.T) {
	fsconformance.RunNamespace(t, func(t *testing.T) tool.Workspace {
		mr := miniredis.RunT(t)
		st, err := redisstore.New(mr.Addr())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		ws, err := st.CreateWorkspace(t.Context(), "ns-conformance")
		if err != nil {
			t.Fatal(err)
		}
		return ws
	})
}

func TestRedisWorkspacePersistsAndCASesAcrossStores(t *testing.T) {
	mr := miniredis.RunT(t)
	first, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	ctx := context.Background()
	a, err := first.CreateWorkspace(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.OpenWorkspace(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	version, err := a.CreateFile(ctx, "shared.txt", []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _, err := b.ReadVersion(ctx, "shared.txt"); err != nil || string(got) != "one" {
		t.Fatalf("cross-store read = %q, %v", got, err)
	}

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, ws := range []tool.Workspace{a, b} {
		wg.Add(1)
		go func(ws tool.Workspace) {
			defer wg.Done()
			_, replaceErr := ws.ReplaceFile(ctx, "shared.txt", version, []byte("next"))
			results <- replaceErr
		}(ws)
	}
	wg.Wait()
	close(results)
	var success, mismatch int
	for result := range results {
		if result == nil {
			success++
			continue
		}
		var stale *tool.VersionMismatchError
		if errors.As(result, &stale) {
			mismatch++
			continue
		}
		t.Fatalf("unexpected replace result: %v", result)
	}
	if success != 1 || mismatch != 1 {
		t.Fatalf("replace outcomes success=%d mismatch=%d", success, mismatch)
	}
}

// TestRedisWorkspaceDerivedDirectoryVanishesOnceEmpty pins redisstore's
// derived-directory contract (the tool.WorkspaceNamespace doc-comment): like
// memfs, a Redis-backed directory has no record of its own, so once its last
// file is removed the directory is indistinguishable from one that never
// existed — ReadDir and Remove both report fs.ErrNotExist. osfs diverges here
// (its physical, now-empty directory survives); each adapter pins its own
// version of this case.
func TestRedisWorkspaceDerivedDirectoryVanishesOnceEmpty(t *testing.T) {
	mr := miniredis.RunT(t)
	st, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	ws, err := st.CreateWorkspace(ctx, "vanish")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.CreateFile(ctx, "sub/only.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := ws.Remove(ctx, "sub/only.txt"); err != nil {
		t.Fatalf("Remove(sub/only.txt): %v", err)
	}
	if _, err := ws.ReadDir(ctx, "sub"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadDir(now-empty derived dir) err = %v, want errors.Is(_, fs.ErrNotExist)", err)
	}
	if err := ws.Remove(ctx, "sub"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Remove(now-empty derived dir) err = %v, want errors.Is(_, fs.ErrNotExist)", err)
	}
}

// TestRedisWorkspaceNamespaceOpsPersistAcrossStores proves the namespace
// operations (Rename/Remove/CopyFile) are visible through a SECOND Store
// handle opened over the same Redis backend — the same cross-store contract
// TestRedisWorkspacePersistsAndCASesAcrossStores pins for content operations,
// extended to the namespace family.
func TestRedisWorkspaceNamespaceOpsPersistAcrossStores(t *testing.T) {
	mr := miniredis.RunT(t)
	first, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	ctx := t.Context()
	a, err := first.CreateWorkspace(ctx, "cross-ns")
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.OpenWorkspace(ctx, "cross-ns")
	if err != nil {
		t.Fatal(err)
	}

	// A rename issued through handle a is visible through handle b.
	if _, err := a.CreateFile(ctx, "old.txt", []byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err := a.Rename(ctx, "old.txt", "new.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if data, err := b.Read(ctx, "new.txt"); err != nil || string(data) != "payload" {
		t.Fatalf("cross-store read after Rename: data=%q err=%v", data, err)
	}

	// A copy issued through handle b is visible through handle a.
	if _, err := b.CopyFile(ctx, "new.txt", "dup.txt"); err != nil {
		t.Fatalf("CopyFile: %v", err)
	}
	if data, err := a.Read(ctx, "dup.txt"); err != nil || string(data) != "payload" {
		t.Fatalf("cross-store read after CopyFile: data=%q err=%v", data, err)
	}

	// A remove issued through handle a is visible through handle b.
	if err := a.Remove(ctx, "dup.txt"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := b.Read(ctx, "dup.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("cross-store read after Remove err = %v, want errors.Is(_, fs.ErrNotExist)", err)
	}
}

func TestRedisWorkspaceScopesAndMissingNamespace(t *testing.T) {
	mr := miniredis.RunT(t)
	st, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	a, _ := st.CreateWorkspace(ctx, "a")
	b, _ := st.CreateWorkspace(ctx, "b")
	if _, err := a.CreateFile(ctx, "private.txt", []byte("secret")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Read(ctx, "private.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("other scope read error = %v, want not exist", err)
	}
	if _, err := st.OpenWorkspace(ctx, "missing"); err == nil {
		t.Fatal("OpenWorkspace(missing) succeeded")
	}
}

// TestRedisWorkspaceAuthorityResourcePathRootDot pins that AuthorityResourcePath
// maps "." (the documented ListDir/ReadDir root spelling, ADR-consistent with
// cleanWorkspaceDir) to the workspace root instead of rejecting it as an
// escape — the bug that denied a root ListDir authority check before
// execution — while continuing to reject every other invalid spelling
// (empty, absolute, NUL-containing, and ".."-traversing paths) exactly as
// cleanWorkspacePath already does.
func TestRedisWorkspaceAuthorityResourcePathRootDot(t *testing.T) {
	mr := miniredis.RunT(t)
	st, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	ws, err := st.CreateWorkspace(ctx, "authority-root")
	if err != nil {
		t.Fatal(err)
	}
	resolver, ok := tool.Workspace(ws).(tool.AuthorityResourceResolver)
	if !ok {
		t.Fatal("redisstore.Workspace does not implement tool.AuthorityResourceResolver")
	}

	target, workspace, err := resolver.AuthorityResourcePath(".")
	if err != nil {
		t.Fatalf("AuthorityResourcePath(\".\") err = %v, want nil", err)
	}
	if target != "/workspace" || workspace != "/workspace" {
		t.Fatalf("AuthorityResourcePath(\".\") = (%q, %q), want (/workspace, /workspace)", target, workspace)
	}

	for _, p := range []string{"", "/abs", "has\x00nul", "../escape", "a/../../escape"} {
		if _, _, err := resolver.AuthorityResourcePath(p); err == nil {
			t.Fatalf("AuthorityResourcePath(%q) succeeded, want an escape error", p)
		}
	}
}

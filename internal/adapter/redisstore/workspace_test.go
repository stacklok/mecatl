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

package memfs_test

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/fsconformance"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestConformance runs the shared Workspace conformance table against memfs.
func TestConformance(t *testing.T) {
	fsconformance.Run(t, func(_ *testing.T) tool.Workspace {
		return memfs.NewWorkspace("/ws")
	})
}

// TestNamespaceConformance runs the shared WorkspaceNamespace conformance
// table (ReadDir/Remove/Rename/CopyFile) against memfs.
func TestNamespaceConformance(t *testing.T) {
	fsconformance.RunNamespace(t, func(_ *testing.T) tool.Workspace {
		return memfs.NewWorkspace("/ws")
	})
}

// --- memfs-specific tests ---

// TestDerivedDirectoryVanishesOnceEmpty pins memfs's derived-directory
// contract (the tool.WorkspaceNamespace doc-comment): a directory has no
// record of its own, so once its last file is removed the directory itself
// is indistinguishable from one that never existed — ReadDir and Remove both
// report fs.ErrNotExist, never success and never ErrDirectoryNotEmpty. osfs
// diverges here (a real, now-empty physical directory survives); each
// adapter pins its own version of this case.
func TestDerivedDirectoryVanishesOnceEmpty(t *testing.T) {
	ctx := context.Background()
	ws := memfs.NewWorkspace("/ws")
	if err := ws.Write(ctx, "sub/only.txt", []byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
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

func TestGrepDeterministic(t *testing.T) {
	ctx := context.Background()
	// Build many fresh workspaces and assert identical Grep ordering each time.
	build := func() *memfs.Workspace {
		ws := memfs.NewWorkspace("/ws")
		files := map[string]string{
			"z.txt": "match here\nno\n",
			"a.txt": "match\n",
			"m.txt": "nope\nmatch again\n",
		}
		for p, c := range files {
			if err := ws.Write(ctx, p, []byte(c)); err != nil {
				t.Fatalf("Write: %v", err)
			}
		}
		return ws
	}

	var first []tool.GrepMatch
	for i := 0; i < 20; i++ {
		got, err := build().Grep(ctx, "match", "")
		if err != nil {
			t.Fatalf("Grep: %v", err)
		}
		if i == 0 {
			first = got
			continue
		}
		if len(got) != len(first) {
			t.Fatalf("run %d: len = %d want %d", i, len(got), len(first))
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("run %d match %d = %+v want %+v", i, j, got[j], first[j])
			}
		}
	}
	// Sanity: ordering is sorted by path.
	if len(first) < 2 || first[0].Path > first[1].Path {
		t.Fatalf("Grep results not sorted by path: %+v", first)
	}
}

func TestGlobDeterministic(t *testing.T) {
	ctx := context.Background()
	var first []string
	for i := 0; i < 20; i++ {
		ws := memfs.NewWorkspace("/ws")
		for _, p := range []string{"c.go", "a.go", "b.go"} {
			_ = ws.Write(ctx, p, []byte("x"))
		}
		got, err := ws.Glob(ctx, "*.go")
		if err != nil {
			t.Fatalf("Glob: %v", err)
		}
		if i == 0 {
			first = got
			continue
		}
		if strings.Join(got, ",") != strings.Join(first, ",") {
			t.Fatalf("run %d glob = %v want %v", i, got, first)
		}
	}
	if strings.Join(first, ",") != "a.go,b.go,c.go" {
		t.Fatalf("Glob order = %v want sorted", first)
	}
}

func TestCommandRunnerNoShell(t *testing.T) {
	ctx := context.Background()
	r := memfs.NewCommandRunner()
	_, err := r.Run(ctx, "echo hi")
	if !errors.Is(err, memfs.ErrNoShell) {
		t.Fatalf("Run err = %v want ErrNoShell", err)
	}
	// ErrNoShell wraps the shared sentinel so either matches.
	if !errors.Is(err, tool.ErrNoShell) {
		t.Fatalf("Run err = %v want to wrap tool.ErrNoShell", err)
	}
}

func TestCommandRunnerCanned(t *testing.T) {
	ctx := context.Background()
	r := memfs.NewCommandRunner()
	r.SetResult(&tool.CommandResult{Stdout: "canned", ExitCode: 7}, nil)
	res, err := r.Run(ctx, "anything")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stdout != "canned" || res.ExitCode != 7 {
		t.Fatalf("Run = %+v want canned/7", res)
	}
}

func TestCommandRunnerCannedError(t *testing.T) {
	ctx := context.Background()
	r := memfs.NewCommandRunner()
	sentinel := errors.New("boom")
	r.SetResult(nil, sentinel)
	if _, err := r.Run(ctx, "x"); !errors.Is(err, sentinel) {
		t.Fatalf("Run err = %v want sentinel", err)
	}
}

func TestCommandRunnerCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := memfs.NewCommandRunner()
	r.SetResult(&tool.CommandResult{Stdout: "x"}, nil)
	if _, err := r.Run(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run cancelled = %v want context.Canceled", err)
	}
}

func TestReadNotExist(t *testing.T) {
	ctx := context.Background()
	ws := memfs.NewWorkspace("/ws")
	if _, err := ws.Read(ctx, "missing.txt"); err == nil {
		t.Fatal("Read missing = nil err, want error")
	}
}

func TestGrepGlobScoped(t *testing.T) {
	ctx := context.Background()
	ws := memfs.NewWorkspace("/ws")
	_ = ws.Write(ctx, "x.go", []byte("target\n"))
	_ = ws.Write(ctx, "x.md", []byte("target\n"))
	got, err := ws.Grep(ctx, "target", "*.go")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(got) != 1 || got[0].Path != "x.go" {
		t.Fatalf("Grep glob-scoped = %+v want only x.go", got)
	}
}

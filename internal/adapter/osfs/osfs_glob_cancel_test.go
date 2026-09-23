package osfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"testing"
	"testing/fstest"
)

func TestGlobCancellationBeforeTraversal(t *testing.T) {
	ws, err := NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := ws.Glob(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("Glob error = %v, want context.Canceled", err)
	}
}

func TestGlobCancellationAtInitialReadDir(t *testing.T) {
	ws, err := NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"a/one.txt", "a/b/two.txt", "a/b/c/three.txt"} {
		if err := ws.Write(context.Background(), path, []byte("content")); err != nil {
			t.Fatal(err)
		}
	}
	ctx := &grepCountingCancelContext{Context: context.Background(), cancelAt: 3}

	if _, err := ws.Glob(ctx, "**/*.never"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Glob error = %v, want cancellation during no-match traversal", err)
	}
}

func TestGlobCancellationFromFinalVisitor(t *testing.T) {
	ws, err := NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.Write(context.Background(), "only.txt", []byte("content")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())

	err = ws.fs.globWalk(ctx, "only.txt", func(string, fs.DirEntry) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("globWalk error = %v, want cancellation raised by final visitor", err)
	}
}

func TestGlobCancellationDuringReadDirDiscardsEntries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	base := &cancelDuringReadDirFS{
		FS:     fstest.MapFS{"match.txt": {Data: []byte("content")}},
		cancel: cancel,
	}

	entries, err := (globWalkFS{ctx: ctx, base: base}).ReadDir(".")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadDir error = %v, want context.Canceled", err)
	}
	if entries != nil {
		t.Fatalf("ReadDir entries = %v, want none after cancellation during ReadDir", entries)
	}
}

func TestGlobCancellationAroundLiteralPrefixStat(t *testing.T) {
	const (
		pattern = "deep/nested/prefix/**"
		prefix  = "deep/nested/prefix"
	)
	contents := fstest.MapFS{
		prefix + "/match.txt": {Data: []byte("content")},
	}

	t.Run("before", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		base := &cancelDuringStatFS{FS: contents, cancelName: prefix}

		_, err := (globWalkFS{ctx: ctx, base: base}).Stat(prefix)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Stat error = %v, want context.Canceled", err)
		}
		if len(base.statNames) != 0 {
			t.Fatalf("Stat calls = %v, want none for pre-cancelled context", base.statNames)
		}
	})

	t.Run("during", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		base := &cancelDuringStatFS{FS: contents, cancel: cancel, cancelName: prefix}

		info, err := (globWalkFS{ctx: ctx, base: base}).Stat(prefix)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Stat error = %v, want context.Canceled", err)
		}
		if info != nil {
			t.Fatalf("Stat info = %v, want nil after cancellation during Stat", info)
		}
	})

	t.Run("doublestar literal prefix", func(t *testing.T) {
		base := &cancelDuringStatFS{FS: contents, cancelName: prefix}
		var matches []string

		err := walkGlob(context.Background(), base, pattern, func(path string, _ fs.DirEntry) error {
			matches = append(matches, path)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(base.statNames, prefix) {
			t.Fatalf("Stat calls = %v, want literal prefix %q", base.statNames, prefix)
		}
		if !slices.Contains(matches, prefix+"/match.txt") {
			t.Fatalf("matches = %v, want nested file", matches)
		}
	})
}

func TestPathGlobStopsTraversalWhenCancellationReachesReadDir(t *testing.T) {
	for _, test := range []struct {
		name string
		call func(context.Context, *Workspace) error
	}{
		{
			name: "Glob",
			call: func(ctx context.Context, ws *Workspace) error {
				_, err := ws.Glob(ctx, "**/*.never")
				return err
			},
		},
		{
			name: "Grep",
			call: func(ctx context.Context, ws *Workspace) error {
				_, err := ws.Grep(ctx, "needle", "**/*.never")
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ws, err := NewWorkspace(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			// Keep many root siblings queued after cancellation. The context cancels at
			// the first recursive ReadDir below the first root child; without production's
			// fail-on-I/O option, doublestar swallows that error and checks every sibling.
			for i := range 32 {
				path := fmt.Sprintf("%02d/child/file.txt", i)
				if err := ws.Write(context.Background(), path, []byte("needle")); err != nil {
					t.Fatal(err)
				}
			}
			const cancelAt = 10
			ctx := &grepCountingCancelContext{Context: context.Background(), cancelAt: cancelAt}

			if err := test.call(ctx, ws); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
			if ctx.checks > cancelAt+4 {
				t.Fatalf("context checks = %d, want at most %d; traversal continued after cancellation", ctx.checks, cancelAt+4)
			}
		})
	}
}

func TestGlobWalkPreservesIgnoredIOErrorSemantics(t *testing.T) {
	base := faultingGlobFS{FS: fstest.MapFS{
		"blocked/secret.txt": {Data: []byte("secret")},
		"visible/ok.txt":     {Data: []byte("ok")},
	}}
	var matches []string
	err := walkGlob(context.Background(), base, "**/*.txt", func(path string, _ fs.DirEntry) error {
		matches = append(matches, path)
		return nil
	})
	if err != nil {
		t.Fatalf("GlobWalk returned ordinary ReadDir error: %v", err)
	}
	if want := []string{"visible/ok.txt"}; !slices.Equal(matches, want) {
		t.Fatalf("matches = %v, want %v", matches, want)
	}

	err = walkGlob(context.Background(), base, "blocked.txt", func(string, fs.DirEntry) error {
		t.Fatal("failed Stat must be treated as a nonmatch")
		return nil
	})
	if err != nil {
		t.Fatalf("GlobWalk returned ordinary Stat error: %v", err)
	}
}

type cancelDuringReadDirFS struct {
	fs.FS
	cancel context.CancelFunc
}

func (f *cancelDuringReadDirFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(f.FS, name)
	f.cancel()
	return entries, err
}

type cancelDuringStatFS struct {
	fs.FS
	cancel     context.CancelFunc
	cancelName string
	statNames  []string
}

func (f *cancelDuringStatFS) Stat(name string) (fs.FileInfo, error) {
	f.statNames = append(f.statNames, name)
	info, err := fs.Stat(f.FS, name)
	if f.cancel != nil && name == f.cancelName {
		f.cancel()
	}
	return info, err
}

func (f *cancelDuringStatFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return fs.ReadDir(f.FS, name)
}

type faultingGlobFS struct{ fs.FS }

func (f faultingGlobFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name == "blocked" {
		return nil, fs.ErrPermission
	}
	return fs.ReadDir(f.FS, name)
}

func (f faultingGlobFS) Stat(name string) (fs.FileInfo, error) {
	if name == "blocked.txt" {
		return nil, fs.ErrPermission
	}
	return fs.Stat(f.FS, name)
}

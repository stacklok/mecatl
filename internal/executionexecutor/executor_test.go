package executionexecutor

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/executionenv"
)

func TestFileOperationsAreConfinedAndVersioned(t *testing.T) {
	root := t.TempDir()
	x, err := New(root, Limits{MaxFileBytes: 1024, MaxEntries: 100, CommandTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	ctx := context.Background()
	created, err := x.Execute(ctx, executionenv.ExecutorRequest{Operation: executionenv.OpFileCreate, Path: "a/file.txt", Data: []byte("one")})
	if err != nil {
		t.Fatal(err)
	}
	read, err := x.Execute(ctx, executionenv.ExecutorRequest{Operation: executionenv.OpFileRead, Path: "a/file.txt"})
	if err != nil || string(read.Data) != "one" || read.Version != created.Version {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := x.Execute(ctx, executionenv.ExecutorRequest{
		Operation: executionenv.OpFileResolveAuthority, Path: "a/file.txt",
	})
	if err != nil || resolved.AuthorityWorkspace != physical ||
		resolved.AuthorityTarget != filepath.Join(physical, "a/file.txt") {
		t.Fatalf("resolved authority=%+v err=%v", resolved.FileResponse, err)
	}
	_, err = x.Execute(ctx, executionenv.ExecutorRequest{Operation: executionenv.OpFileReplace, Path: "a/file.txt", Data: []byte("two"), Version: "wrong"})
	var pe *executionenv.Error
	if !errors.As(err, &pe) || pe.Code != executionenv.CodeVersionMismatch {
		t.Fatalf("expected version mismatch: %v", err)
	}
	_, err = x.Execute(ctx, executionenv.ExecutorRequest{Operation: executionenv.OpFileRead, Path: "../../etc/passwd"})
	if err == nil {
		t.Fatal("escape accepted")
	}
	outside := filepath.Join(root, "..", "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	_, err = x.Execute(ctx, executionenv.ExecutorRequest{Operation: executionenv.OpFileRead, Path: "escape"})
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("symlink escape not distinctly rejected: %v", err)
	}
	_, err = x.Execute(ctx, executionenv.ExecutorRequest{Operation: executionenv.OpFileResolveAuthority, Path: "escape"})
	var denied *executionenv.Error
	if !errors.As(err, &denied) || denied.Code != executionenv.CodePermissionDenied {
		t.Fatalf("authority resolver accepted symlink escape: %v", err)
	}
}

func TestRenameAndCopyNeverClobber(t *testing.T) {
	x, _ := New(t.TempDir(), Limits{MaxFileBytes: 1024, MaxEntries: 100})
	defer x.Close()
	ctx := context.Background()
	_, _ = x.Execute(ctx, executionenv.ExecutorRequest{Operation: executionenv.OpFileCreate, Path: "a", Data: []byte("a")})
	_, _ = x.Execute(ctx, executionenv.ExecutorRequest{Operation: executionenv.OpFileCreate, Path: "b", Data: []byte("b")})
	for _, op := range []executionenv.Operation{executionenv.OpFileCopy, executionenv.OpFileRename} {
		if _, err := x.Execute(ctx, executionenv.ExecutorRequest{Operation: op, Path: "a", Destination: "b"}); err == nil {
			t.Fatalf("%s clobbered", op)
		} else {
			var pe *executionenv.Error
			if !errors.As(err, &pe) || pe.Code != executionenv.CodeAlreadyExists {
				t.Fatalf("%s wrong error: %v", op, err)
			}
		}
	}
}

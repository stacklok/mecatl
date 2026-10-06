package executionexecutor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/executionenv"
)

func TestFileOperationsNeverTraverseExternalSymlink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	sentinel := filepath.Join(outside, "file")
	if err := os.WriteFile(sentinel, []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	x, err := New(root, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	for _, op := range []executionenv.Operation{executionenv.OpFileRead, executionenv.OpFileResolveAuthority, executionenv.OpFileStat, executionenv.OpFileCreate, executionenv.OpFileReplace, executionenv.OpFileList, executionenv.OpFileRemove, executionenv.OpFileRename, executionenv.OpFileCopy} {
		t.Run(string(op), func(t *testing.T) {
			req := executionenv.ExecutorRequest{Operation: op, Path: "escape/file", Destination: "destination", Version: "version"}
			if op == executionenv.OpFileList {
				req.Path = "escape"
			}
			if op == executionenv.OpFileCreate {
				req.Path = "escape/new"
			}
			if _, err := x.Execute(t.Context(), req); err == nil {
				t.Fatal("external symlink traversal accepted")
			}
			if op == executionenv.OpFileCopy || op == executionenv.OpFileRename {
				if _, err := x.Execute(t.Context(), executionenv.ExecutorRequest{Operation: executionenv.OpFileCreate, Path: string(op), Data: []byte("local")}); err != nil {
					t.Fatal(err)
				}
				req.Path = string(op)
				req.Destination = "escape/new"
				if _, err := x.Execute(t.Context(), req); err == nil {
					t.Fatal("external destination accepted")
				}
			}
		})
	}
	for _, op := range []executionenv.Operation{executionenv.OpFileGlob, executionenv.OpFileGrep} {
		req := executionenv.ExecutorRequest{Operation: op, Pattern: "**/*", Limit: 100}
		if op == executionenv.OpFileGrep {
			req.Path = "**/*"
			req.Pattern = "outside-secret"
		}
		result, err := x.Execute(t.Context(), req)
		if err != nil {
			continue
		} // Honest confinement errors are also safe.
		for _, path := range result.Paths {
			if strings.HasPrefix(path, "escape/") {
				t.Fatalf("glob escaped: %q", path)
			}
		}
		if len(result.Matches) != 0 {
			t.Fatalf("grep leaked external content: %+v", result.Matches)
		}
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "outside-secret" {
		t.Fatalf("external file mutated: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); !os.IsNotExist(err) {
		t.Fatalf("external file created: %v", err)
	}
}

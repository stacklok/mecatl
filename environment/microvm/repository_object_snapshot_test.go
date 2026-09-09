package microvm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/environment/microvm/gitexec"
)

func TestRepositoryObjectSnapshotRetainsPackedGitObjects(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.name", "MicroVM Test"},
		{"config", "user.email", "microvm@example.invalid"},
	} {
		if _, err := gitexec.Run(t.Context(), repository, nil, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("snapshot content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "tracked.txt"}, {"commit", "-qm", "snapshot fixture"}, {"gc", "--prune=now"}} {
		if _, err := gitexec.Run(t.Context(), repository, nil, args...); err != nil {
			t.Fatal(err)
		}
	}

	common := filepath.Join(repository, ".git")
	snapshot, err := snapshotRepositoryObjects(t.Context(), common, root)
	if err != nil {
		t.Fatal(err)
	}
	defer removeObjectSnapshot(snapshot)
	packs, err := filepath.Glob(filepath.Join(snapshot, "pack", "*.pack"))
	if err != nil || len(packs) == 0 {
		t.Fatalf("packed objects missing from snapshot: packs=%v err=%v", packs, err)
	}
	indexes, err := filepath.Glob(filepath.Join(snapshot, "pack", "*.idx"))
	if err != nil || len(indexes) == 0 {
		t.Fatalf("pack indexes missing from snapshot: indexes=%v err=%v", indexes, err)
	}

	original := filepath.Join(common, "objects")
	if err := os.Rename(original, original+".host"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	environment := []string{"GIT_OBJECT_DIRECTORY=" + snapshot}
	if _, err := gitexec.RunWithEnv(t.Context(), repository, nil, environment, "cat-file", "-e", "HEAD^{tree}"); err != nil {
		t.Fatalf("packed snapshot cannot serve Git object reads: %v", err)
	}
	status, err := gitexec.RunWithEnv(t.Context(), repository, nil, environment, "status", "--porcelain")
	if err != nil || strings.TrimSpace(string(status)) != "" {
		t.Fatalf("logical worktree status over snapshot = %q, %v", status, err)
	}
}

func TestRepositoryObjectSnapshotRejectsNestedSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	common := filepath.Join(root, "repository.git")
	if err := os.MkdirAll(filepath.Join(common, "objects", "pack"), 0o700); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(root, "external")
	if err := os.WriteFile(external, []byte("do not copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(common, "objects", "pack", "host.pack")); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotRepositoryObjects(t.Context(), common, root); err == nil {
		t.Fatal("nested object-store symlink was copied")
	}
}

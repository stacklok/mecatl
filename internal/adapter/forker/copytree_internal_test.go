package forker

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestIsTransientGitMaintenanceLock(t *testing.T) {
	if !isTransientGitMaintenanceLock(".git/objects/maintenance.lock") {
		t.Fatal("known Git maintenance lock was not identified")
	}
	if isTransientGitMaintenanceLock(".git/objects/other.lock") {
		t.Fatal("unrelated Git object lock was identified")
	}
	if isTransientGitMaintenanceLock(".git/maintenance.lock") {
		t.Fatal("maintenance lock outside the object database was identified")
	}
}

func TestCopyTreeEntryVanishedWorktreeLock(t *testing.T) {
	for _, rel := range []string{
		".git/worktrees/child/locked",
		".git/worktrees/child/HEAD",
		".git/worktrees/child/index.lock",
		".git/worktrees/locked",
		".git/worktrees/nested/child/locked",
		"nested/.git/worktrees/child/locked",
		"locked",
		"tracked.txt",
	} {
		t.Run(rel, func(t *testing.T) {
			root, dst := t.TempDir(), t.TempDir()
			path := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("lock reason\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(entries) != 1 {
				t.Fatalf("enumerate source: entries=%v, err=%v", entries, err)
			}
			entry := entries[0]
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}

			for _, stage := range []string{"walk", "open"} {
				t.Run(stage, func(t *testing.T) {
					var err error
					if stage == "walk" {
						err = copyTreeEntry(root, dst, path, nil, &fs.PathError{Op: "lstat", Path: path, Err: fs.ErrNotExist})
					} else {
						// WalkDir already enumerated this entry; the source vanishes before open.
						err = copyTreeEntry(root, dst, path, entry, nil)
					}
					if rel == ".git/worktrees/child/locked" {
						if err != nil {
							t.Fatalf("vanished worktree lock: %v", err)
						}
					} else if !errors.Is(err, fs.ErrNotExist) {
						t.Fatalf("missing non-lock error = %v, want ErrNotExist", err)
					}
				})
			}
			if err := copyTreeEntry(root, dst, path, nil, fs.ErrPermission); !errors.Is(err, fs.ErrPermission) {
				t.Fatalf("walk permission error = %v, want ErrPermission", err)
			}
			// A stale regular DirEntry now opens a symlink loop: a deterministic
			// non-ENOENT source-open failure, even when the test runs as root.
			if err := os.Symlink(filepath.Base(path), path); err != nil {
				t.Fatal(err)
			}
			err = copyTreeEntry(root, dst, path, entry, nil)
			var pathErr *fs.PathError
			if !errors.As(err, &pathErr) || pathErr.Op != "open" || pathErr.Path != path || errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("source-open error = %v, want a non-ENOENT source-open failure", err)
			}
		})
	}
}

func TestCopyTreePreservesExistingWorktreeLock(t *testing.T) {
	root, dst := t.TempDir(), t.TempDir()
	rel := filepath.Join(".git", "worktrees", "child", "locked")
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	const reason = "keep this worktree\n"
	if err := os.WriteFile(path, []byte(reason), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(root, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dst, rel))
	if err != nil || string(got) != reason {
		t.Fatalf("copied lock = %q, err=%v; want %q", got, err, reason)
	}

	// A destination ENOENT must not be mistaken for a vanished source lock.
	if err := os.Remove(filepath.Join(dst, rel)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dst, "missing", "target"), filepath.Join(dst, rel)); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(root, dst); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("destination error = %v, want ErrNotExist", err)
	}
}

func TestCopyTreeEntrySkipsOnlyVanishedMaintenanceLock(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".git", "objects", "maintenance.lock")

	if err := copyTreeEntry(root, t.TempDir(), path, nil, fs.ErrNotExist); err != nil {
		t.Fatalf("vanished maintenance lock: %v", err)
	}

	permissionErr := errors.New("permission denied")
	err := copyTreeEntry(root, t.TempDir(), path, nil, permissionErr)
	if !errors.Is(err, permissionErr) {
		t.Fatalf("maintenance lock permission error = %v, want %v", err, permissionErr)
	}
}

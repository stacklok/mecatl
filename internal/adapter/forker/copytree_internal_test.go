package forker

import (
	"errors"
	"io/fs"
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

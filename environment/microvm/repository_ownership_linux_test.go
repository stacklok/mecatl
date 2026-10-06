//go:build linux

package microvm

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRepositoryOwnershipLinuxIsNoOp(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareRepositoryOwnership(t.Context(), root, "."); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.Getxattr(file, "user.containers.override_stat", nil); !errors.Is(err, unix.ENODATA) {
		t.Fatalf("Linux ownership preparation mutated xattrs: %v", err)
	}
}

func TestRepositoryMountsLinuxRetainNamespaceMappingWithoutXattrs(t *testing.T) {
	mounts := repositoryMounts("/logical", "/objects")
	if len(mounts) != 2 {
		t.Fatalf("mount count = %d, want 2", len(mounts))
	}
	for _, mount := range mounts {
		if mount.OverrideUID != 0 || mount.OverrideGID != 0 || mount.StrictOwnershipPreparation {
			t.Fatalf("Linux mount %s mutates ownership policy: %+v", mount.Tag, mount)
		}
	}
	if mounts[0].ReadOnly || !mounts[1].ReadOnly {
		t.Fatalf("mount access changed: %+v", mounts)
	}
}

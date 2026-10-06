//go:build darwin

package microvm

import "testing"

func TestRepositoryMountsDarwinRequireStrictFixedGuestOwnership(t *testing.T) {
	mounts := repositoryMounts("/logical", "/objects")
	if len(mounts) != 2 {
		t.Fatalf("mount count = %d, want 2", len(mounts))
	}
	for _, mount := range mounts {
		if mount.OverrideUID != repositoryGuestOwnershipID || mount.OverrideGID != repositoryGuestOwnershipID || !mount.StrictOwnershipPreparation {
			t.Fatalf("Darwin mount %s ownership policy = %+v", mount.Tag, mount)
		}
	}
	if mounts[0].ReadOnly || !mounts[1].ReadOnly {
		t.Fatalf("mount access changed: %+v", mounts)
	}
}

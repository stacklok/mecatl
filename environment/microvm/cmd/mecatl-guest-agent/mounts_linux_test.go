//go:build linux

package main

import (
	"testing"

	"github.com/stacklok/mecatl/environment/microvm/worktree"
)

func TestRepositoryGuestMountsSharedObjectsReadOnly(t *testing.T) {
	mounts := repositoryGuestMounts()
	if len(mounts) != 2 || mounts[0].path != "/run/mecatl/repositories" || mounts[0].tag != "mecatl-repository-logical" || mounts[0].readOnly ||
		mounts[1].path != worktree.GuestObjectStore || mounts[1].tag != "mecatl-git-objects" || !mounts[1].readOnly {
		t.Fatalf("repository guest mounts = %+v", mounts)
	}
}

func TestADR_0224_GuestGitMetadataHasConfinedMount(t *testing.T) {
	mounts := guestMounts()
	for _, mount := range mounts {
		if mount.tag != "mecatl-git-metadata" {
			continue
		}
		if mount.path != worktree.GuestMetadata || mount.path == worktree.GuestWorkspace+"/.git" {
			t.Fatalf("Git metadata mount = %+v, want separate guest-local mount %s", mount, worktree.GuestMetadata)
		}
		return
	}
	t.Fatal("guest mount plan has no Git metadata mount")
}

//go:build darwin

package microvm

import (
	"context"

	gomicrovm "github.com/stacklok/go-microvm"
	gomicrovmvirtiofs "github.com/stacklok/go-microvm/virtiofs"
)

func prepareRepositoryOwnership(ctx context.Context, root, relative string) error {
	return gomicrovmvirtiofs.PrepareOwnership(ctx, root, relative, repositoryGuestOwnershipID, repositoryGuestOwnershipID)
}

func applyRepositoryMountOwnership(mounts []gomicrovm.VirtioFSMount) []gomicrovm.VirtioFSMount {
	for i := range mounts {
		mounts[i].OverrideUID = repositoryGuestOwnershipID
		mounts[i].OverrideGID = repositoryGuestOwnershipID
		mounts[i].StrictOwnershipPreparation = true
	}
	return mounts
}

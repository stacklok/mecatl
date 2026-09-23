//go:build darwin

package microvm

import (
	"context"

	gomicrovm "github.com/stacklok/go-microvm"
	gomicrovmvirtiofs "github.com/stacklok/go-microvm/virtiofs"

	"github.com/stacklok/mecatl/environment/microvm/guestexec"
)

func prepareRepositoryOwnership(ctx context.Context, root, relative string) error {
	identity := guestexec.DefaultWorkloadIdentity()
	return gomicrovmvirtiofs.PrepareOwnership(ctx, root, relative, identity.UID, identity.GID)
}

func applyRepositoryMountOwnership(mounts []gomicrovm.VirtioFSMount) []gomicrovm.VirtioFSMount {
	identity := guestexec.DefaultWorkloadIdentity()
	for i := range mounts {
		mounts[i].OverrideUID = int(identity.UID)
		mounts[i].OverrideGID = int(identity.GID)
		mounts[i].StrictOwnershipPreparation = true
	}
	return mounts
}

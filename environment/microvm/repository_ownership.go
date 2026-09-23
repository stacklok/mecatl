package microvm

import (
	"context"

	gomicrovm "github.com/stacklok/go-microvm"
)

const repositoryGuestOwnershipID = 65532

type repositoryOwnershipPreparer func(context.Context, string, string) error

func repositoryMounts(logicalRoot, objectSnapshot string) []gomicrovm.VirtioFSMount {
	return applyRepositoryMountOwnership([]gomicrovm.VirtioFSMount{
		{Tag: repositoryMountTag, HostPath: logicalRoot},
		{Tag: repositoryObjectMountTag, HostPath: objectSnapshot, ReadOnly: true},
	})
}

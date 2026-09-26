//go:build !darwin

package microvm

import (
	"context"

	gomicrovm "github.com/stacklok/go-microvm"
)

func prepareRepositoryOwnership(context.Context, string, string) error { return nil }

func applyRepositoryMountOwnership(mounts []gomicrovm.VirtioFSMount) []gomicrovm.VirtioFSMount {
	return mounts
}

package microvm_test

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/environment/microvm"
)

func TestProductionLibkrunBackendRequiresRecoverableLaunchOwnership(t *testing.T) {
	backend, err := microvm.NewLibkrunBackend(t.TempDir(), nil)
	if backend != nil || !errors.Is(err, microvm.ErrLaunchOwnershipUnsupported) {
		t.Fatalf("NewLibkrunBackend without launch ownership = (%v, %v), want (nil, ErrLaunchOwnershipUnsupported)", backend, err)
	}

	runtime, err := microvm.NewRepositoryRuntime(microvm.RepositoryRuntimeConfig{
		Backend:      &microvm.LibkrunBackend{},
		Network:      microvm.NewNetworkController(nil, nil),
		UnixEndpoint: true,
	})
	if runtime != nil || !errors.Is(err, microvm.ErrLaunchOwnershipUnsupported) {
		t.Fatalf("NewRepositoryRuntime with unowned production backend = (%v, %v), want (nil, ErrLaunchOwnershipUnsupported)", runtime, err)
	}
}

package microvm

import (
	"errors"
	"fmt"

	"github.com/stacklok/mecatl/environment/microvm/worktree"
)

// RepositoryComposition is the production repository-scoped microvmd slice.
// Attachments connects session and isolated-delegation callers to the shared
// authenticated runtime and logical worktree manager.
type RepositoryComposition struct {
	Runtime     *RepositoryRuntime
	Registry    *RepositoryVMRegistry
	Logical     *RepositoryLogicalManager
	Attachments *RepositoryAttachmentManager
	// RestartHealthError is set when durable repository state predates this
	// composition and its in-process network backend therefore cannot be reattached.
	RestartHealthError error
}

// NewRepositoryComposition wires the singleton lifecycle and logical routing to
// the concrete hypervisor/guest adapters.
func NewRepositoryComposition(stateRoot string, cfg RepositoryRuntimeConfig) (*RepositoryComposition, error) {
	runtime, err := NewRepositoryRuntime(cfg)
	if err != nil {
		return nil, err
	}
	endpointRoots := []string(nil)
	if cfg.EndpointRoot != "" {
		endpointRoots = append(endpointRoots, cfg.EndpointRoot)
	}
	registry, err := OpenRepositoryVMRegistry(stateRoot, runtime, endpointRoots...)
	if err != nil {
		return nil, err
	}
	registry.artifactPolicy = cfg.ArtifactPolicy
	registry.artifactRequests = cloneArtifactRequests(cfg.Artifacts)
	registry.guestEgress = cloneGuestEgressPolicy(cfg.GuestEgress)
	logical, err := NewRepositoryLogicalManager(registry, worktree.New(), runtime)
	if err != nil {
		return nil, err
	}
	if logical == nil {
		return nil, errors.New("repository microvmd composition is incomplete")
	}
	attachments, err := newRepositoryAttachmentManager(logical)
	if err != nil {
		return nil, fmt.Errorf("load repository attachment inventory: %w", err)
	}
	return &RepositoryComposition{
		Runtime: runtime, Registry: registry, Logical: logical,
		Attachments: attachments,
	}, nil
}

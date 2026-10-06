package server

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
)

// ACPEnvironmentOverlay decorates an already-authorized placement environment
// with editor-buffer semantics. The returned environment must preserve the exact
// placement identity.
type ACPEnvironmentOverlay func(session.SessionID, tool.Environment) (tool.Environment, error)

// CreateACPSession binds the composition-configured default placement before it
// consults the ACP cwd assertion. cwd can only confirm that trusted binding; it
// is never used to construct or select an environment.
func (s *Service) CreateACPSession(ctx context.Context, cwd string, mode session.PermissionMode, limits session.Limits, specs []mcp.ServerConfig, overlay ACPEnvironmentOverlay) (*session.Session, error) {
	binding, err := s.BindPlacement(ctx, DefaultPlacement(), PlacementOperationCreate)
	if err != nil {
		return nil, err
	}
	if err := assertACPPlacementCWD(cwd, binding); err != nil {
		rollbackUnpublishedPlacement(s, &binding)
		return nil, err
	}
	id := s.cfg.NewID()
	if overlay != nil {
		binding.Environment, err = overlay(id, binding.Environment)
		if err != nil {
			rollbackUnpublishedPlacement(s, &binding)
			return nil, fmt.Errorf("%w: editor filesystem overlay unavailable", ErrFailedPrecondition)
		}
		if err := validatePlacementBinding(binding); err != nil {
			rollbackUnpublishedPlacement(s, &binding)
			return nil, err
		}
	}
	created, err := s.createSession(ctx, mode, limits, ProviderSelector{}, specs, ProfileDefault, createSessionOpts{
		id: id, idSet: true, placement: &binding, placementEnvironmentOverride: overlay != nil,
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// LoadACPSession owner-authorizes the snapshot, reattaches its exact persisted
// EnvironmentRef, and only then checks cwd. It never binds a current default or
// constructs an environment from cwd.
func (s *Service) LoadACPSession(ctx context.Context, id session.SessionID, cwd string, specs []mcp.ServerConfig, overlay ACPEnvironmentOverlay) (*session.Session, error) {
	persisted, err := s.cfg.Store.Load(ctx, id)
	if err != nil || persisted == nil || persisted.ID != id || s.authorizeSession(ctx, persisted) != nil {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	if !persisted.EnvironmentRef.Valid() {
		return nil, fmt.Errorf("%w: session has no exact placement", ErrFailedPrecondition)
	}
	binding, err := s.sessionPlacement(ctx, persisted)
	if err != nil {
		return nil, err
	}
	if err := assertACPPlacementCWD(cwd, binding); err != nil {
		discardPlacementBinding(binding)
		return nil, err
	}
	if overlay != nil {
		binding.Environment, err = overlay(id, binding.Environment)
		if err != nil {
			discardPlacementBinding(binding)
			return nil, fmt.Errorf("%w: editor filesystem overlay unavailable", ErrFailedPrecondition)
		}
		binding.Ref = persisted.EnvironmentRef
		if err := validatePlacementBinding(binding); err != nil {
			discardPlacementBinding(binding)
			return nil, err
		}
	}
	sess, err := s.LoadSessionWithMCP(ctx, id, specs)
	if err != nil {
		discardPlacementBinding(binding)
		return nil, err
	}
	if sess.EnvironmentRef != persisted.EnvironmentRef {
		discardPlacementBinding(binding)
		return nil, fmt.Errorf("%w: session placement changed while loading", ErrFailedPrecondition)
	}
	if overlay != nil {
		s.updateSessionEnvironment(id, binding.Environment)
	}
	return sess, nil
}

func assertACPPlacementCWD(cwd string, binding PlacementBinding) error {
	governanceRoot, rootErr := PlacementGovernanceRoot(binding)
	resolved, err := filepath.EvalSymlinks(cwd)
	if rootErr != nil || err != nil || governanceRoot == "" || !filepath.IsAbs(cwd) || filepath.Clean(resolved) != filepath.Clean(governanceRoot) {
		return fmt.Errorf("%w: cwd does not match the configured session placement", ErrInvalidArgument)
	}
	return nil
}

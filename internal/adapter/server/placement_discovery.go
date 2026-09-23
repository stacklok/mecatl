package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// ScopedWorktree is the additive path-free discovery projection used by the
// server-owned placement cutover. Selector is ephemeral authority; the other
// fields are display-only.
type ScopedWorktree struct {
	Selector string
	Label    string
	Branch   string
	Revision string
	Bare     bool
}

func (s *Service) ownedSessionBinding(ctx context.Context, id session.SessionID) (*session.Session, PlacementBinding, error) {
	sess, err := s.cfg.Store.Load(ctx, id)
	if err != nil {
		if errors.Is(err, port.ErrSessionNotFound) {
			return nil, PlacementBinding{}, fmt.Errorf("%w: %q", ErrNotFound, id)
		}
		s.logDiscoveryError(ctx, "load session", err)
		return nil, PlacementBinding{}, fmt.Errorf("%w: discovery backend failed", ErrInternal)
	}
	if sess == nil || sess.ID != id || s.authorizeSession(ctx, sess) != nil {
		return nil, PlacementBinding{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	if sess.EnvironmentRef.Kind == session.EnvKindNoFS {
		return sess, PlacementBinding{}, nil
	}
	binding, err := s.ReattachPlacement(ctx, sess.EnvironmentRef)
	if err != nil {
		return nil, PlacementBinding{}, err
	}
	return sess, binding, nil
}

// ownedSessionEnvironment is the long-lived environment path used by team
// creation. The team supervisor adopts the environment capability; discovery
// uses ownedSessionBinding directly so it can release request-scoped provider
// resources after the read completes.
func (s *Service) ownedSessionEnvironment(ctx context.Context, id session.SessionID) (*session.Session, tool.Environment, error) {
	sess, binding, err := s.ownedSessionBinding(ctx, id)
	if err != nil {
		return nil, tool.Environment{}, err
	}
	return sess, binding.Environment, nil
}

// ListCommandsForSession owner-authorizes and exactly reattaches before command
// discovery. A no-FS source returns empty without touching either provider.
func (s *Service) ListCommandsForSession(ctx context.Context, id session.SessionID) ([]Command, error) {
	sess, binding, err := s.ownedSessionBinding(ctx, id)
	if err != nil {
		return nil, err
	}
	if binding.Close != nil {
		defer func() { _ = binding.Close() }()
	}
	if sess.EnvironmentRef.Kind == session.EnvKindNoFS || s.cfg.Commands == nil {
		return nil, nil
	}
	commands, err := s.cfg.Commands.List(ctx, binding.Environment.Workspace())
	if err != nil {
		s.logDiscoveryError(ctx, "list commands", err)
		return nil, fmt.Errorf("%w: command discovery failed", ErrInternal)
	}
	return commands, nil
}

// ListWorktreesForSession owner-authorizes and exactly reattaches before
// enumeration, then issues caller/source-scoped selectors without retaining
// them. A no-FS source is an empty result and invokes no lister.
func (s *Service) ListWorktreesForSession(ctx context.Context, id session.SessionID) ([]ScopedWorktree, error) {
	sess, binding, err := s.ownedSessionBinding(ctx, id)
	if err != nil {
		return nil, err
	}
	if binding.Close != nil {
		defer func() { _ = binding.Close() }()
	}
	if sess.EnvironmentRef.Kind == session.EnvKindNoFS {
		return nil, nil
	}
	provider, ok := s.cfg.PlacementProvider.(PlacementDiscoverer)
	if !ok {
		return nil, fmt.Errorf("%w: worktree discovery is unavailable", ErrPlacementUnavailable)
	}
	current, err := provider.ListWorktrees(ctx, PlacementDiscoveryRequest{
		Source: id, SourceRef: sess.EnvironmentRef,
		Principal: session.PrincipalFromContext(ctx), Scope: s.cfg.PlacementScope,
	})
	if err != nil {
		err = sanitizePlacementProviderError(err)
		s.logPlacementProviderError(ctx, "discover", err)
		return nil, err
	}
	for i := range current {
		if !safePlacementText(current[i].Selector, maxPlacementIdentityRunes) || strings.ContainsAny(current[i].Selector, `/\\`) {
			return nil, ErrInvalidPlacementBinding
		}
		current[i].Label = sanitizePlacementDisplay(current[i].Label, maxPlacementNameRunes)
		current[i].Branch = sanitizePlacementDisplay(current[i].Branch, maxPlacementNameRunes)
		current[i].Revision = sanitizePlacementDisplay(current[i].Revision, maxPlacementIdentityRunes)
	}
	return current, nil
}

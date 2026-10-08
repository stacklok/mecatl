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

func (s *Service) ownedSession(ctx context.Context, id session.SessionID) (*session.Session, error) {
	sess, err := s.cfg.Store.Load(ctx, id)
	if err != nil {
		if errors.Is(err, port.ErrSessionNotFound) {
			return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
		}
		s.logDiscoveryError(ctx, "load session", err)
		return nil, fmt.Errorf("%w: discovery backend failed", ErrInternal)
	}
	if sess == nil || sess.ID != id || s.authorizeSession(ctx, sess) != nil {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	return sess, nil
}

func (s *Service) ownedSessionBinding(ctx context.Context, id session.SessionID) (*session.Session, PlacementBinding, error) {
	sess, err := s.ownedSession(ctx, id)
	if err != nil {
		return nil, PlacementBinding{}, err
	}
	if sess.EnvironmentRef.Kind == session.EnvKindNoFS {
		return sess, PlacementBinding{}, nil
	}
	binding, err := s.ReattachPlacementForBinding(ctx, sess.EnvironmentRef, sess.ID)
	if err != nil {
		return nil, PlacementBinding{}, err
	}
	return sess, binding, nil
}

// ownedSessionEnvironment is the long-lived environment path used by team
// creation. The team supervisor adopts the environment capability; discovery
// uses ownedSessionBinding directly so it can release request-scoped provider
// resources after the read completes.
func (s *Service) ownedSessionEnvironment(ctx context.Context, id session.SessionID) (*session.Session, tool.Environment, func(), error) {
	sess, binding, err := s.ownedSessionBinding(ctx, id)
	if err != nil {
		return nil, tool.Environment{}, nil, err
	}
	release := func() {}
	if binding.Close != nil {
		release = func() { _ = binding.Close() }
	}
	return sess, binding.Environment, release, nil
}

// ListCommandsForSession owner-authorizes the session and borrows its independent
// command source binding without reattaching execution.
func (s *Service) ListCommandsForSession(ctx context.Context, id session.SessionID) ([]Command, error) {
	sess, err := s.ownedSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.cfg.Commands == nil {
		return nil, nil
	}
	var binding CommandSourceBinding
	var release func()
	if resolver, ok := s.cfg.Commands.(executionWorkspaceCommandSourceResolver); ok {
		binding, release, err = resolver.BorrowWithExecutionWorkspace(ctx, sess.ID, sess.Owner.Clone(), sess.Profile, s.executionWorkspaceAcquirer(sess.Owner, sess.EnvironmentRef))
	} else {
		binding, release, err = s.cfg.Commands.Borrow(ctx, sess.ID, sess.Owner.Clone(), sess.Profile)
	}
	if err != nil {
		s.logDiscoveryError(ctx, "bind command sources", err)
		return nil, fmt.Errorf("%w: command source binding failed", ErrInternal)
	}
	defer release()
	commands, err := binding.List(ctx)
	if err != nil {
		s.logDiscoveryError(ctx, "list commands", err)
		return nil, fmt.Errorf("%w: command discovery failed", ErrInternal)
	}
	out := make([]Command, 0, len(commands))
	for _, command := range commands {
		out = append(out, Command{Name: command.Name, Description: command.Description})
	}
	return out, nil
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

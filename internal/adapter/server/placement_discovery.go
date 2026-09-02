package server

import (
	"context"
	"errors"
	"fmt"

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

func (s *Service) ownedSessionEnvironment(ctx context.Context, id session.SessionID) (*session.Session, tool.Environment, error) {
	sess, err := s.cfg.Store.Load(ctx, id)
	if err != nil {
		if errors.Is(err, port.ErrSessionNotFound) {
			return nil, tool.Environment{}, fmt.Errorf("%w: %q", ErrNotFound, id)
		}
		return nil, tool.Environment{}, fmt.Errorf("%w: load session: %v", ErrInternal, err)
	}
	if sess == nil || sess.ID != id || s.authorizeSession(ctx, sess) != nil {
		return nil, tool.Environment{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	if sess.EnvironmentRef.Kind == session.EnvKindNoFS {
		return sess, tool.Environment{}, nil
	}
	binding, err := s.ReattachPlacement(ctx, sess.EnvironmentRef)
	if err != nil {
		return nil, tool.Environment{}, err
	}
	return sess, binding.Environment, nil
}

// ListCommandsForSession owner-authorizes and exactly reattaches before command
// discovery. A no-FS source returns empty without touching either provider.
func (s *Service) ListCommandsForSession(ctx context.Context, id session.SessionID) ([]Command, error) {
	sess, env, err := s.ownedSessionEnvironment(ctx, id)
	if err != nil {
		return nil, err
	}
	if sess.EnvironmentRef.Kind == session.EnvKindNoFS || s.cfg.Commands == nil {
		return nil, nil
	}
	commands, err := s.cfg.Commands.List(ctx, env.Workspace().Root())
	if err != nil {
		return nil, fmt.Errorf("%w: list commands: %v", ErrInternal, err)
	}
	return commands, nil
}

// ListWorktreesForSession owner-authorizes and exactly reattaches before
// enumeration, then issues caller/source-scoped selectors without retaining
// them. A no-FS source is an empty result and invokes no lister.
func (s *Service) ListWorktreesForSession(ctx context.Context, id session.SessionID) ([]ScopedWorktree, error) {
	sess, env, err := s.ownedSessionEnvironment(ctx, id)
	if err != nil {
		return nil, err
	}
	if sess.EnvironmentRef.Kind == session.EnvKindNoFS || s.cfg.Worktrees == nil {
		return nil, nil
	}
	if s.worktreeSelectors == nil {
		return nil, fmt.Errorf("%w: worktree selectors are unavailable", ErrFailedPrecondition)
	}
	current, err := s.cfg.Worktrees.List(ctx, env.Workspace().Root())
	if err != nil {
		return nil, fmt.Errorf("%w: list worktrees: %v", ErrInternal, err)
	}
	principal := session.PrincipalFromContext(ctx)
	out := make([]ScopedWorktree, 0, len(current))
	for _, choice := range current {
		label := choice.Branch
		if label == "" {
			label = "Detached worktree"
		}
		out = append(out, ScopedWorktree{
			Selector: s.worktreeSelectors.Issue(principal, id, choice),
			Label:    label, Branch: choice.Branch, Revision: choice.Head, Bare: choice.Bare,
		})
	}
	return out, nil
}

func (s *Service) matchCurrentWorktree(ctx context.Context, source session.SessionID, selector string, env tool.Environment) (Worktree, error) {
	if s.worktreeSelectors == nil || s.cfg.Worktrees == nil {
		return Worktree{}, ErrPlacementNotFound
	}
	current, err := s.cfg.Worktrees.List(ctx, env.Workspace().Root())
	if err != nil {
		return Worktree{}, fmt.Errorf("%w: list worktrees: %v", ErrInternal, err)
	}
	return s.worktreeSelectors.Match(selector, session.PrincipalFromContext(ctx), source, current)
}

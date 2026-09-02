package server

import (
	"context"
	"fmt"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// SuccessorPlacement selects exact inheritance when empty or one freshly
// matched source-scoped worktree when Selector is present.
type SuccessorPlacement struct {
	Selector string
}

// ForkSuccessorRequest is the additive internal successor shape used before
// the public contract cutover.
type ForkSuccessorRequest struct {
	Source          session.SessionID
	Placement       SuccessorPlacement
	Title           string
	ProviderID      string
	ModelID         string
	ReasoningEffort string
}

// ClearSessionSuccessor creates a distinct empty-history peer while preserving
// source ownership, labels, limits, mode, and exact placement unless a current
// source-scoped selector is supplied.
func (s *Service) ClearSessionSuccessor(ctx context.Context, source session.SessionID, placement SuccessorPlacement) (session.SessionID, error) {
	return s.createPlacedSuccessor(ctx, ForkSuccessorRequest{Source: source, Placement: placement}, false)
}

// ForkSessionSuccessor creates a history-carrying peer on inherited exact
// placement or a freshly matched source-scoped worktree.
func (s *Service) ForkSessionSuccessor(ctx context.Context, req ForkSuccessorRequest) (session.SessionID, error) {
	return s.createPlacedSuccessor(ctx, req, true)
}

func (s *Service) createPlacedSuccessor(ctx context.Context, req ForkSuccessorRequest, copyHistory bool) (session.SessionID, error) {
	absent, err := s.managementOwnershipPreflight(ctx, req.Source, false)
	if err != nil {
		return "", err
	}
	if absent {
		return "", fmt.Errorf("%w: %q", ErrNotFound, req.Source)
	}
	unlock := s.runEntryMu.lock(req.Source)
	defer unlock()
	if _, absent, err = s.managementTarget(ctx, req.Source, false); err != nil || absent {
		return "", err
	}
	release, err := s.acquireMutationLease(ctx, req.Source)
	if err != nil {
		return "", err
	}
	defer release()
	source, absent, err := s.managementTarget(ctx, req.Source, false)
	if err != nil || absent {
		return "", err
	}
	binding, err := s.successorPlacement(ctx, source, req.Placement)
	if err != nil {
		return "", err
	}

	created := session.New(s.cfg.NewID(), source.Mode, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: binding.Environment.Workspace().Root(), Revision: inTreeEnvironmentRevision}, source.Limits, s.cfg.Now())
	selector, err := successorProviderSelector(source, req)
	if err != nil {
		return "", err
	}
	authority, bound := source.BoundAuthority()
	if !bound {
		authority = session.Authority{}
	}
	if err := setSessionLabels(created, selector, profileForSession(source), source.Owner, authority); err != nil {
		return "", err
	}
	created.EnvironmentRef = binding.Ref
	if copyHistory {
		if err := created.SeedHistory(session.ForkSnapshot(source.Conversation)); err != nil {
			return "", fmt.Errorf("server: seed fork history: %w", err)
		}
		if req.Title == "" {
			created.Title, created.TitleProvenance = source.Title, source.TitleProvenance
		}
	}
	if req.Title != "" {
		if err := created.RenameTitle(req.Title); err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidArgument, err)
		}
	}
	// Publish only after every source, placement, history, and label validation
	// has succeeded. Source state and binding are never modified.
	if err := s.persistNewSession(ctx, created); err != nil {
		return "", fmt.Errorf("server: persist successor: %w", err)
	}
	s.mu.Lock()
	s.sessionEnvironments[created.ID] = binding.Environment
	s.mu.Unlock()
	return created.ID, nil
}

func successorProviderSelector(source *session.Session, req ForkSuccessorRequest) (ProviderSelector, error) {
	selector := ProviderSelector{ProviderID: source.ProviderID, ModelID: source.ModelID, ReasoningEffort: source.ReasoningEffort}
	if req.ProviderID != "" {
		selector.ProviderID = req.ProviderID
	}
	if req.ModelID != "" {
		if req.ProviderID == "" {
			return ProviderSelector{}, fmt.Errorf("%w: model_id requires provider_id", ErrInvalidArgument)
		}
		selector.ModelID = req.ModelID
	}
	if req.ReasoningEffort != "" {
		selector.ReasoningEffort = req.ReasoningEffort
	}
	return selector, nil
}

func (s *Service) successorPlacement(ctx context.Context, source *session.Session, requested SuccessorPlacement) (PlacementBinding, error) {
	binding, err := s.ReattachPlacement(ctx, source.EnvironmentRef)
	if err != nil {
		return PlacementBinding{}, err
	}
	if requested.Selector == "" {
		return binding, nil
	}
	if source.EnvironmentRef.Kind == session.EnvKindNoFS {
		return PlacementBinding{}, ErrPlacementNotFound
	}
	choice, err := s.matchCurrentWorktree(ctx, source.ID, requested.Selector, binding.Environment)
	if err != nil {
		return PlacementBinding{}, err
	}
	if choice.Path == "" || choice.Head == "" {
		return PlacementBinding{}, ErrPlacementNotFound
	}
	ws := s.cfg.Workspaces(choice.Path)
	if ws == nil {
		return PlacementBinding{}, ErrPlacementUnavailable
	}
	var runner tool.CommandRunner
	if s.cfg.CommandRunnerFactory != nil {
		runner = s.cfg.CommandRunnerFactory(ws.Root())
	}
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: choice.Path, Revision: choice.Head}
	env, err := tool.NewEnvironment(ref, ws, runner)
	if err != nil {
		return PlacementBinding{}, ErrPlacementUnavailable
	}
	selected := PlacementBinding{Environment: env, Ref: ref}
	if err := validatePlacementBinding(selected); err != nil {
		return PlacementBinding{}, err
	}
	return selected, nil
}

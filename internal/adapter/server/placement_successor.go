package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// SuccessorPlacement selects exact inheritance when empty or one freshly
// matched source-scoped worktree when Selector is present.
type SuccessorPlacement struct {
	Selector        string
	SelectorPresent bool
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

//nolint:gocyclo // Successor creation keeps validation, exact placement, engine setup, and publication atomic.
func (s *Service) createPlacedSuccessor(ctx context.Context, req ForkSuccessorRequest, copyHistory bool) (session.SessionID, error) {
	if req.Placement.SelectorPresent && req.Placement.Selector == "" {
		return "", fmt.Errorf("%w: worktree_selector must not be empty when present", ErrInvalidArgument)
	}
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
	mutationCtx, stopMutation, stillHeld := s.mutationLeaseContext(ctx, req.Source)
	defer stopMutation()
	source, absent, err := s.managementTarget(mutationCtx, req.Source, false)
	if err != nil || absent {
		return "", err
	}
	selector, err := successorProviderSelector(source, req)
	if err != nil {
		return "", err
	}
	binding, err := s.successorPlacement(mutationCtx, source, req.Placement)
	if err != nil {
		return "", err
	}
	if binding.Close != nil {
		defer func() { _ = binding.Close() }()
	}

	created := session.New(s.cfg.NewID(), source.Mode, binding.Ref, source.Limits, s.cfg.Now())
	created.Placement = canonicalPlacementMetadata(binding)
	authority, bound := source.BoundAuthority()
	if !bound {
		authority = session.Authority{}
	}
	if err := setSessionLabels(created, selector, profileForSession(source), source.Owner, authority); err != nil {
		return "", err
	}
	created.EnvironmentRef = binding.Ref
	if copyHistory {
		if err := created.SeedHistory(s.providerCarryoverSnapshot(source, selector.ProviderID)); err != nil {
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
	profile := profileForSession(created)
	var builtEngine *sessionEngine
	if s.sessionNeedsPerFactory(selector, nil, profile, binding.Environment.Workspace().Root()) {
		if s.cfg.SessionEngine == nil {
			return "", fmt.Errorf("%w: per-session engine not supported (no session-engine factory configured)", ErrInvalidArgument)
		}
		builtEngine, err = s.buildAndRegisterSessionEngine(mutationCtx, created, selector, profile, created.Mode, false)
		if err != nil {
			return "", err
		}
	}
	cleanupEngine := func() {
		if builtEngine == nil {
			return
		}
		s.mu.Lock()
		delete(s.sessionEngines, created.ID)
		s.mu.Unlock()
		if builtEngine.close != nil {
			_ = builtEngine.close()
		}
	}
	// The lease context covers provider binding and engine construction. Recheck
	// ownership immediately before the only publication point.
	//
	// TODO(ADR 0291 follow-up): SessionStore has no lease-token CAS Save. A lease
	// can therefore be lost after stillHeld and before/while Save publishes. Context
	// cancellation is advisory because supported stores may already be committing;
	// closing this residual window requires a new token-fenced store seam.
	if !stillHeld() {
		cleanupEngine()
		return "", ErrSessionLeasedElsewhere
	}
	if err := s.persistNewSession(mutationCtx, created); err != nil {
		cleanupEngine()
		if errors.Is(err, port.ErrSessionAlreadyExists) {
			return "", err
		}
		s.logDiscoveryError(ctx, "persist successor placement", err)
		return "", fmt.Errorf("%w: placement storage failed", ErrInternal)
	}
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
	if requested.Selector == "" {
		return s.ReattachPlacement(ctx, source.EnvironmentRef)
	}
	if source.EnvironmentRef.Kind == session.EnvKindNoFS {
		return PlacementBinding{}, ErrPlacementNotFound
	}
	return s.BindPlacement(ctx, SelectWorktree(source.ID, source.EnvironmentRef, requested.Selector), PlacementOperationSuccessor)
}

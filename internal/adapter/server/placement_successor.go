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

// ClearSessionSuccessor cancels any exact source lifecycle, waits for its relay
// to deregister, then creates a distinct empty-history peer while preserving
// source ownership, labels, limits, mode, and exact placement unless a current
// source-scoped selector is supplied.
func (s *Service) ClearSessionSuccessor(ctx context.Context, source session.SessionID, placement SuccessorPlacement) (session.SessionID, error) {
	req := ForkSuccessorRequest{Source: source, Placement: placement}
	if err := validateSuccessorRequest(req); err != nil {
		return "", err
	}
	absent, err := s.managementOwnershipPreflight(ctx, source, false)
	if err != nil {
		return "", err
	}
	if absent {
		return "", fmt.Errorf("%w: %q", ErrNotFound, source)
	}

	unlock := s.runEntryMu.lock(source)
	defer unlock()
	lockedSource, absent, err := s.managementSession(ctx, source, false)
	if err != nil || absent {
		return "", err
	}
	// Validate an explicit worktree placement while the source is still untouched.
	// In particular, a stale, foreign, or unavailable selector must not cancel an
	// active run or durable approval. Successor creation revalidates after
	// settlement under its mutation lease; this preflight is only the
	// non-destructive gate. Exact inherited placement stays on the lease-protected
	// path below because reattachment may itself wait for lease loss/cancellation.
	if placement.Selector != "" {
		preflight, placementErr := s.successorPlacement(ctx, lockedSource, placement)
		if placementErr != nil {
			return "", placementErr
		}
		if preflight.Close != nil {
			_ = preflight.Close()
		}
	}
	if err := s.cancelAndAwaitClearSource(ctx, source); err != nil {
		return "", err
	}
	successor, err := s.createPlacedSuccessorLocked(ctx, req, false, true)
	if err != nil {
		return "", err
	}
	if s.cfg.SessionCleared != nil {
		s.cfg.SessionCleared(source)
	}
	return successor, nil
}

// cancelAndAwaitClearSource captures and cancels the exact lifecycle registered
// while runEntryMu excludes replacement admission. Settlement is awaited without
// s.mu so the relay can call FinishRun and close the captured signal.
func (s *Service) cancelAndAwaitClearSource(ctx context.Context, id session.SessionID) error {
	s.mu.Lock()
	// This is Clear's irreversible cancellation boundary. Retire every request
	// that captured the source generation before this point, including callers
	// already queued on runEntryMu. A request received later snapshots the new
	// generation and may deliberately address the old id again.
	s.runEntryGenerations[id]++
	st := s.runs[id]
	if st == nil {
		s.mu.Unlock()
		return nil
	}
	settled := st.settled
	st.cancelling = true
	s.mu.Unlock()

	s.cancelRegisteredRunState(id, st, nil, false)
	select {
	case <-settled:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ForkSessionSuccessor creates a history-carrying peer on inherited exact
// placement or a freshly matched source-scoped worktree.
func (s *Service) ForkSessionSuccessor(ctx context.Context, req ForkSuccessorRequest) (session.SessionID, error) {
	return s.createPlacedSuccessor(ctx, req, true)
}

func validateSuccessorRequest(req ForkSuccessorRequest) error {
	if req.Placement.SelectorPresent && req.Placement.Selector == "" {
		return fmt.Errorf("%w: worktree_selector must not be empty when present", ErrInvalidArgument)
	}
	return nil
}

//nolint:gocyclo // Successor creation keeps validation, exact placement, engine setup, and publication atomic.
func (s *Service) createPlacedSuccessor(ctx context.Context, req ForkSuccessorRequest, copyHistory bool) (session.SessionID, error) {
	if err := validateSuccessorRequest(req); err != nil {
		return "", err
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
	return s.createPlacedSuccessorLocked(ctx, req, copyHistory, false)
}

//nolint:gocyclo // Successor creation keeps validation, exact placement, engine setup, and publication atomic.
func (s *Service) createPlacedSuccessorLocked(ctx context.Context, req ForkSuccessorRequest, copyHistory, clearSource bool) (session.SessionID, error) {
	release, err := s.acquireMutationLease(ctx, req.Source)
	if err != nil {
		return "", err
	}
	defer release()
	mutationCtx, stopMutation, stillHeld := s.mutationLeaseContext(ctx, req.Source)
	defer stopMutation()
	var (
		source *session.Session
		absent bool
	)
	if clearSource {
		source, absent, err = s.managementSession(mutationCtx, req.Source, false)
	} else {
		source, absent, err = s.managementTarget(mutationCtx, req.Source, false)
	}
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
	// Durable awaiting sessions have no registered relay for the preflight to
	// settle. Cancel them only after placement has been revalidated under the
	// mutation lease, so a failed successor cannot consume the pending ask.
	if clearSource && (source.State == session.StateRunning || source.State == session.StateAwaiting) {
		if err := source.Cancel(); err != nil {
			return "", fmt.Errorf("server: cancel clear source: %w", err)
		}
		if err := s.saveSession(mutationCtx, source); err != nil {
			return "", fmt.Errorf("server: persist cancelled clear source: %w", err)
		}
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
	var (
		builtEngine     *sessionEngine
		broker          *localBrokerAttachment
		brokerCommitted bool
	)
	// A successor is a new broker incarnation, never a reattachment of the
	// source. Build it before the engine so the exact tools belonging to this
	// id are the ones the factory receives. The binding is stamped on the
	// successor before its first durable publication; broker commit happens only
	// after that publication succeeds.
	if s.cfg.MCPBroker != nil {
		broker, err = s.openBrokerAttachment(mutationCtx, created.ID, "", false)
		if err != nil {
			return "", err
		}
		defer s.finalizeBrokerAttachment(broker, &brokerCommitted)
		created.ExternalBinding = broker.attachment.Binding()
	}
	if s.cfg.MCPBroker != nil || s.sessionNeedsPerFactory(selector, nil, profile, binding.Environment.Workspace().Root()) {
		if s.cfg.MCPBroker == nil && s.cfg.SessionEngine == nil {
			return "", fmt.Errorf("%w: per-session engine not supported (no session-engine factory configured)", ErrInvalidArgument)
		}
		if broker != nil {
			builtEngine, err = s.buildAndRegisterSessionEngineWithBrokerTools(mutationCtx, created, selector, profile, created.Mode, false, brokerTools(broker), true)
		} else {
			builtEngine, err = s.buildAndRegisterSessionEngine(mutationCtx, created, selector, profile, created.Mode, false)
		}
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
	var forkObjectsPending, forkPublished bool
	if copyHistory {
		references, hasPDFPrompt := successorPDFReferences(created.Conversation.Messages)
		if hasPDFPrompt && (s.cfg.PDFArtifacts == nil || !s.sessionCapabilitiesFor(created).PDF) {
			cleanupEngine()
			return "", fmt.Errorf("%w: selected model does not accept inherited PDF input", ErrInvalidArgument)
		}
		if len(references) != 0 {
			if s.cfg.PDFArtifacts == nil {
				cleanupEngine()
				return "", ErrPDFArtifactsUnavailable
			}
			forkObjectsPending = true
			defer func() {
				if forkPublished {
					return
				}
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(mutationCtx), engineCloseTimeout)
				defer cancel()
				if discardErr := s.cfg.PDFArtifacts.DiscardUnpublished(cleanupCtx, created.ID); discardErr != nil {
					s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "discard unpublished PDF fork failed",
						"session", string(created.ID))
				}
			}()
			rewritten, copyErr := s.cfg.PDFArtifacts.CopyFork(mutationCtx, source.ID, created.ID, created.Conversation.Messages)
			if copyErr != nil {
				cleanupEngine()
				return "", copyErr
			}
			if err := created.SeedHistory(rewritten); err != nil {
				cleanupEngine()
				return "", fmt.Errorf("server: seed copied fork history: %w", err)
			}
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
	forkPublished = true
	if forkObjectsPending {
		references, _ := successorPDFReferences(created.Conversation.Messages)
		// The snapshot is authoritative if a marker update fails. Reconciliation
		// repairs ready records and clears prepublication cleanup intent.
		if err := s.cfg.PDFArtifacts.CommitPrompt(mutationCtx, created.ID, references); err != nil {
			s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "commit PDF fork references failed",
				"session", string(created.ID))
		}
	}
	if broker != nil {
		commitCtx, cancelCommit := context.WithTimeout(context.WithoutCancel(mutationCtx), engineCloseTimeout)
		commitErr := s.commitBrokerAttachment(commitCtx, created.ID, broker)
		cancelCommit()
		if commitErr != nil {
			cleanupEngine()
			return "", fmt.Errorf("%w: %v", ErrInternal, commitErr)
		}
		brokerCommitted = true
	}
	return created.ID, nil
}

func successorPDFReferences(history []session.Message) ([]string, bool) {
	seen := make(map[string]struct{})
	var ids []string
	var hasPrompt bool
	add := func(id string) {
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	for _, message := range history {
		for _, part := range message.Parts {
			if part.Kind == session.MediaPDF && part.BlockKind == "" {
				hasPrompt = true
				add(part.ArtifactID)
			}
		}
		if message.ToolResult != nil {
			for _, part := range message.ToolResult.Parts {
				if part.BlockKind == session.BlockPDFArtifact {
					add(part.ArtifactID)
				}
			}
		}
	}
	return ids, hasPrompt
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

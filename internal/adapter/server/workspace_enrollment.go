package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// WorkspaceEnrollmentProjection is the presentation-safe state returned to a client.
type WorkspaceEnrollmentProjection struct {
	Ref    brokercontract.WorkspaceEnrollmentRef
	Status brokercontract.WorkspaceEnrollmentStatus
	URL    string
}

// ConnectWorkspaceServices begins or observes the one pre-prompt enrollment bundle.
func (s *Service) ConnectWorkspaceServices(ctx context.Context, id session.SessionID) (WorkspaceEnrollmentProjection, error) {
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	if err := s.acquireLease(ctx, id); err != nil {
		return WorkspaceEnrollmentProjection{}, err
	}
	sess, enroller, release, recovery, err := s.recoveringWorkspaceEnrollmentTarget(ctx, id)
	if err != nil {
		if sess != nil && errors.Is(err, brokercontract.ErrStateUnavailable) {
			return s.settleTerminalWorkspaceEnrollment(ctx, sess, brokercontract.WorkspaceEnrollmentFailed)
		}
		return WorkspaceEnrollmentProjection{}, err
	}
	defer func() { release() }()

	pending, exists := sess.PendingWorkspaceEnrollment()
	if recovery.recovered && !exists {
		// Replacement recovery already saved and committed fresh B2 authority;
		// publish the engine from that same recovered catalogue.
		provider := ProviderSelector{ProviderID: sess.ProviderID, ModelID: sess.ModelID, ReasoningEffort: sess.ReasoningEffort}
		providerTools, ok := enroller.(interface{ Tools() []tool.Tool })
		if !ok {
			return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: recovered broker catalogue is unavailable", ErrFailedPrecondition)
		}
		release()
		release = func() {}
		if _, err := s.buildAndRegisterSessionEngineWithBrokerTools(ctx, sess, provider, profileForSession(sess), sess.Mode, false, providerTools.Tools(), true); err != nil {
			return WorkspaceEnrollmentProjection{}, err
		}
		projection := WorkspaceEnrollmentProjection{Status: brokercontract.WorkspaceEnrollmentConnected}
		if recovery.completedPending != nil {
			projection.Ref = enrollmentRef(*recovery.completedPending)
		}
		return projection, nil
	}
	if !exists {
		presentation, beginErr := enroller.BeginWorkspaceEnrollment(ctx)
		if errors.Is(beginErr, brokercontract.ErrBrokerIncarnationLost) {
			local, rebindErr := s.rebindBrokerAttachment(ctx, sess, s.brokerAttachmentGenerationFor(sess.ID), true)
			if rebindErr != nil {
				return WorkspaceEnrollmentProjection{}, rebindErr
			}
			enroller, exists = local.attachment.(brokercontract.WorkspaceEnrollmentHandle)
			if !exists {
				return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: workspace services are not configured", ErrFailedPrecondition)
			}
			presentation, beginErr = enroller.BeginWorkspaceEnrollment(ctx)
		}
		return s.recordNewWorkspaceEnrollment(ctx, sess, enroller, presentation, beginErr)
	}

	expectedRef := enrollmentRef(pending)
	if custody, ok := sess.BrokerCredentialCustody(); ok {
		if err := s.commitPendingCustody(ctx, sess, custody); err != nil {
			if errors.Is(err, errBrokerCustodyGuardMismatch) {
				if invalidateErr := s.invalidateBrokerCustody(ctx, sess, false); invalidateErr != nil {
					return WorkspaceEnrollmentProjection{}, invalidateErr
				}
			}
			return WorkspaceEnrollmentProjection{}, err
		}
	}
	result, err := enroller.ObserveWorkspaceEnrollment(ctx, expectedRef)
	if errors.Is(err, brokercontract.ErrBrokerIncarnationLost) {
		local, rebindErr := s.rebindBrokerAttachment(ctx, sess, s.brokerAttachmentGenerationFor(sess.ID), true)
		if rebindErr != nil {
			return WorkspaceEnrollmentProjection{}, rebindErr
		}
		freshEnroller, ok := local.attachment.(brokercontract.WorkspaceEnrollmentHandle)
		if !ok {
			return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: workspace services are not configured", ErrFailedPrecondition)
		}
		presentation, beginErr := freshEnroller.BeginWorkspaceEnrollment(ctx)
		return s.recordNewWorkspaceEnrollment(ctx, sess, freshEnroller, presentation, beginErr)
	}
	if err != nil || !result.Valid() || !sameWorkspaceEnrollmentRef(result.Ref, expectedRef) {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: observe workspace enrollment", ErrFailedPrecondition)
	}
	if result.Status != brokercontract.WorkspaceEnrollmentConnected {
		return s.recordObservedWorkspaceEnrollment(ctx, sess, pending, result)
	}

	if err := s.stageInitialCustody(ctx, sess, enroller, pending); err != nil {
		return WorkspaceEnrollmentProjection{}, err
	}
	// The authenticated result is the single snapshot for both executable wrappers
	// and durable authority. Never re-read SessionHandle.Tools during this rebuild: a
	// remote session handle may advance between observation and engine construction.
	// Read the names from the catalogue's own frozen ToolNames(), never by
	// re-calling Spec() per tool: the catalogue is the one authoritative source.
	exactTools := result.Catalogue.Tools()
	toolNames := result.Catalogue.ToolNames()
	release()
	release = func() {}
	sel := ProviderSelector{ProviderID: sess.ProviderID, ModelID: sess.ModelID, ReasoningEffort: sess.ReasoningEffort}
	// Build first, then persist durable completion, then register. A build failure
	// leaves the pending enrollment in place so the same Connect retries it; engine
	// registration is always the final step.
	persistCompletion := func() error {
		if err := sess.CompleteWorkspaceEnrollment(pending, toolNames); err != nil {
			return fmt.Errorf("%w: complete workspace enrollment", ErrFailedPrecondition)
		}
		if err := s.saveSession(ctx, sess); err != nil {
			loaded, loadErr := s.cfg.Store.Load(ctx, sess.ID)
			if loadErr == nil && loaded != nil && loaded.ExternalBinding == sess.ExternalBinding {
				_, pendingStill := loaded.PendingWorkspaceEnrollment()
				_, custodyStill := loaded.BrokerCredentialCustody()
				if !pendingStill && custodyStill {
					// The final write landed despite the reported error. The
					// persisted completed authority is authoritative; continue to
					// engine publication, never publish an unsaved catalogue.
					return nil
				}
				if pendingStill && custodyStill {
					return fmt.Errorf("%w: persist workspace enrollment completion (pending custody remains)", ErrInternal)
				}
			}
			return fmt.Errorf("%w: persist workspace enrollment completion", ErrInternal)
		}
		return nil
	}
	if _, err := s.buildPersistAndRegisterSessionEngine(ctx, sess, sel, profileForSession(sess), sess.Mode, true, exactTools, true, persistCompletion); err != nil {
		return WorkspaceEnrollmentProjection{}, err
	}
	return WorkspaceEnrollmentProjection{Ref: result.Ref, Status: result.Status}, nil
}

func (s *Service) recordNewWorkspaceEnrollment(ctx context.Context, sess *session.Session, enroller brokercontract.WorkspaceEnrollmentHandle, presentation brokercontract.WorkspaceEnrollmentPresentation, beginErr error) (WorkspaceEnrollmentProjection, error) {
	if beginErr != nil || !presentation.Valid() {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: begin workspace enrollment", ErrFailedPrecondition)
	}
	pending := pendingEnrollment(presentation.Ref)
	if err := sess.BeginWorkspaceEnrollment(pending); err != nil {
		cancelWorkspaceEnrollmentDetached(ctx, enroller, presentation.Ref)
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: record workspace enrollment", ErrFailedPrecondition)
	}
	if err := s.saveSession(ctx, sess); err != nil {
		cancelWorkspaceEnrollmentDetached(ctx, enroller, presentation.Ref)
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: persist workspace enrollment", ErrInternal)
	}
	return WorkspaceEnrollmentProjection{Ref: presentation.Ref, Status: brokercontract.WorkspaceEnrollmentPending, URL: presentation.URL}, nil
}

func (s *Service) recordObservedWorkspaceEnrollment(ctx context.Context, sess *session.Session, pending session.PendingWorkspaceEnrollment, result brokercontract.WorkspaceEnrollmentResult) (WorkspaceEnrollmentProjection, error) {
	if result.Status != brokercontract.WorkspaceEnrollmentPending {
		if err := sess.AbortWorkspaceEnrollment(pending.ID); err != nil {
			return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: clear terminal workspace enrollment", ErrFailedPrecondition)
		}
		if err := s.saveSession(ctx, sess); err != nil {
			return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: persist terminal workspace enrollment", ErrInternal)
		}
	}
	return WorkspaceEnrollmentProjection{Ref: result.Ref, Status: result.Status}, nil
}

// RetryWorkspaceEnrollment cancels one exact bundle before beginning a replacement.
func (s *Service) RetryWorkspaceEnrollment(ctx context.Context, id session.SessionID, enrollmentID session.WorkspaceEnrollmentID) (WorkspaceEnrollmentProjection, error) {
	if _, err := s.cancelWorkspaceEnrollment(ctx, id, enrollmentID); err != nil {
		return WorkspaceEnrollmentProjection{}, err
	}
	return s.ConnectWorkspaceServices(ctx, id)
}

// CancelWorkspaceEnrollment cancels one exact bundle and clears its prompt gate.
func (s *Service) CancelWorkspaceEnrollment(ctx context.Context, id session.SessionID, enrollmentID session.WorkspaceEnrollmentID) (WorkspaceEnrollmentProjection, error) {
	return s.cancelWorkspaceEnrollment(ctx, id, enrollmentID)
}

func (s *Service) cancelWorkspaceEnrollment(ctx context.Context, id session.SessionID, enrollmentID session.WorkspaceEnrollmentID) (WorkspaceEnrollmentProjection, error) {
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	if err := s.acquireLease(ctx, id); err != nil {
		return WorkspaceEnrollmentProjection{}, err
	}
	sess, enroller, release, err := s.workspaceEnrollmentTarget(ctx, id)
	if err != nil {
		if sess != nil && errors.Is(err, brokercontract.ErrStateUnavailable) {
			pending, ok := sess.PendingWorkspaceEnrollment()
			if !ok || pending.ID != enrollmentID {
				return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: stale workspace enrollment", ErrFailedPrecondition)
			}
			return s.settleTerminalWorkspaceEnrollment(ctx, sess, brokercontract.WorkspaceEnrollmentFailed)
		}
		return WorkspaceEnrollmentProjection{}, err
	}
	defer func() { release() }()
	pending, ok := sess.PendingWorkspaceEnrollment()
	if !ok || pending.ID != enrollmentID {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: stale workspace enrollment", ErrFailedPrecondition)
	}
	expectedRef := enrollmentRef(pending)
	if _, hasCustody := sess.BrokerCredentialCustody(); hasCustody {
		if err := s.invalidateBrokerCustody(ctx, sess, false); err != nil {
			return WorkspaceEnrollmentProjection{}, err
		}
		return s.settleTerminalWorkspaceEnrollment(ctx, sess, brokercontract.WorkspaceEnrollmentCancelled)
	}
	result, err := enroller.CancelWorkspaceEnrollment(ctx, expectedRef)
	if err != nil {
		if errors.Is(err, brokercontract.ErrAuthorizationNotFound) {
			// The broker's transaction is already gone (a prior terminal Observe
			// already tore it down): there is nothing left to cancel, but the
			// aggregate's own pending record must still be cleared, or this
			// session stays wedged behind a cancel that can never succeed on the
			// broker side again.
			if err := s.invalidateBrokerCustody(ctx, sess, false); err != nil {
				return WorkspaceEnrollmentProjection{}, err
			}
			return s.settleTerminalWorkspaceEnrollment(ctx, sess, brokercontract.WorkspaceEnrollmentCancelled)
		}
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: cancel workspace enrollment", ErrFailedPrecondition)
	}
	if !result.Valid() || !sameWorkspaceEnrollmentRef(result.Ref, expectedRef) {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: cancel workspace enrollment", ErrFailedPrecondition)
	}
	if err := sess.AbortWorkspaceEnrollment(enrollmentID); err != nil {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: clear workspace enrollment", ErrFailedPrecondition)
	}
	if err := s.saveSession(ctx, sess); err != nil {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: persist workspace enrollment cancellation", ErrInternal)
	}
	return WorkspaceEnrollmentProjection{Ref: result.Ref, Status: result.Status}, nil
}

// settleTerminalWorkspaceEnrollment clears the aggregate's pending record for
// any terminal outcome the broker can no longer confirm (its own transaction
// is already gone) or that the caller has already determined — never leaving
// PendingWorkspaceEnrollment set with nothing left able to resolve it.
func (s *Service) settleTerminalWorkspaceEnrollment(ctx context.Context, sess *session.Session, status brokercontract.WorkspaceEnrollmentStatus) (WorkspaceEnrollmentProjection, error) {
	pending, ok := sess.PendingWorkspaceEnrollment()
	if !ok {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: workspace enrollment is not pending", ErrFailedPrecondition)
	}
	if _, hasCustody := sess.BrokerCredentialCustody(); hasCustody {
		if err := s.invalidateBrokerCustody(ctx, sess, false); err != nil {
			return WorkspaceEnrollmentProjection{}, err
		}
	}
	if err := sess.AbortWorkspaceEnrollment(pending.ID); err != nil {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: clear terminal workspace enrollment", ErrFailedPrecondition)
	}
	if err := s.saveSession(ctx, sess); err != nil {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: persist terminal workspace enrollment", ErrInternal)
	}
	return WorkspaceEnrollmentProjection{Ref: enrollmentRef(pending), Status: status}, nil
}

// enrollmentRecovery reports whether workspaceEnrollmentTarget performed
// replacement recovery, and which pending enrollment that recovery completed.
type enrollmentRecovery struct {
	recovered        bool
	completedPending *session.PendingWorkspaceEnrollment
}

func (s *Service) workspaceEnrollmentTarget(ctx context.Context, id session.SessionID) (*session.Session, brokercontract.WorkspaceEnrollmentHandle, func(), error) {
	sess, enroller, release, _, err := s.recoveringWorkspaceEnrollmentTarget(ctx, id)
	return sess, enroller, release, err
}

func (s *Service) recoveringWorkspaceEnrollmentTarget(ctx context.Context, id session.SessionID) (*session.Session, brokercontract.WorkspaceEnrollmentHandle, func(), enrollmentRecovery, error) {
	var recovery enrollmentRecovery
	sess, err := s.cfg.Store.Load(ctx, id)
	if err != nil || sess == nil || sess.ID != id || s.authorizeSession(ctx, sess) != nil {
		return nil, nil, nil, recovery, ErrNotFound
	}
	if !s.brokerConfigured() || sess.State != session.StateIdle || sess.Conversation == nil || len(sess.Conversation.Messages) != 0 {
		return nil, nil, nil, recovery, fmt.Errorf("%w: workspace enrollment must precede the first prompt", ErrFailedPrecondition)
	}
	if _, live := s.LookupRun(id); live {
		return nil, nil, nil, recovery, fmt.Errorf("%w: session has an active run", ErrFailedPrecondition)
	}
	brokerUnlock := s.brokerMu.lock(id)
	if custody, hasCustody := sess.BrokerCredentialCustody(); hasCustody {
		now := time.Now()
		if s.cfg.Now != nil {
			now = s.cfg.Now()
		}
		if !custody.ExpiresAt().After(now) {
			if err := s.invalidateBrokerCustody(ctx, sess, false); err != nil {
				brokerUnlock()
				return nil, nil, nil, recovery, err
			}
		}
	}
	_, attemptedGeneration := s.brokerSnapshot()
	local, err := s.openBrokerAttachment(ctx, id, sess.ExternalBinding, true)
	if err != nil {
		if !errors.Is(err, ErrBrokerBindingMismatch) && !errors.Is(err, brokercontract.ErrBrokerIncarnationLost) {
			brokerUnlock()
			return sess, nil, nil, recovery, err
		}
		// This seam is pre-prompt by construction, so a lost incarnation costs the
		// session nothing durable: adopt the live one rather than strand it behind
		// a binding no restarted process can ever match.
		_, hasCustody := sess.BrokerCredentialCustody()
		if hasCustody {
			if !errors.Is(err, brokercontract.ErrBrokerIncarnationLost) {
				brokerUnlock()
				return sess, nil, nil, recovery, err
			}
			if pending, hasPending := sess.PendingWorkspaceEnrollment(); hasPending {
				recovery.completedPending = &pending
			}
			if local, err = s.recoverBrokerAttachment(ctx, sess, attemptedGeneration); err != nil {
				brokerUnlock()
				return sess, nil, nil, enrollmentRecovery{}, err
			}
			recovery.recovered = true
		} else {
			failedGeneration := s.brokerAttachmentGenerationFor(id)
			if failedGeneration == 0 {
				failedGeneration = attemptedGeneration
			}
			if local, err = s.rebindBrokerAttachment(ctx, sess, failedGeneration, errors.Is(err, brokercontract.ErrBrokerIncarnationLost)); err != nil {
				brokerUnlock()
				return sess, nil, nil, recovery, err
			}
		}
	}
	enroller, ok := local.attachment.(brokercontract.WorkspaceEnrollmentHandle)
	if !ok {
		brokerUnlock()
		return nil, nil, nil, recovery, fmt.Errorf("%w: workspace services are not configured", ErrFailedPrecondition)
	}
	return sess, enroller, brokerUnlock, recovery, nil
}

func (s *Service) stageInitialCustody(ctx context.Context, sess *session.Session, enroller brokercontract.WorkspaceEnrollmentHandle, pending session.PendingWorkspaceEnrollment) error {
	// Custody is required exactly when the broker offers continuity on this
	// attachment. A broker without encrypted custody, or an older broker, keeps
	// the legacy path; once offered, every missing input fails closed rather than
	// completing an enrollment that could never be recovered.
	advertiser, advertises := enroller.(brokercontract.CredentialContinuityAdvertiser)
	if !advertises || !advertiser.CredentialContinuity() {
		return nil
	}
	if _, exists := sess.BrokerCredentialCustody(); exists {
		return nil
	}
	stager, ok := enroller.(brokercontract.CredentialCustodyStager)
	if !ok || s.cfg.BrokerWorkloadIdentity == nil || sess.Owner == nil {
		return fmt.Errorf("%w: credential continuity is offered but custody cannot be staged", ErrFailedPrecondition)
	}
	ownerPartition, err := brokercontract.ContinuityPrincipalPartition(brokercontract.ContinuityPartitionOwner, sess.Owner)
	if err != nil {
		return fmt.Errorf("%w: derive broker owner partition", ErrFailedPrecondition)
	}
	workloadPartition, err := brokercontract.ContinuityPrincipalPartition(brokercontract.ContinuityPartitionWorkload, s.cfg.BrokerWorkloadIdentity)
	if err != nil {
		return fmt.Errorf("%w: derive broker workload partition", ErrFailedPrecondition)
	}
	now := time.Now()
	if s.cfg.Now != nil {
		now = s.cfg.Now()
	}
	deadline := now.Add(brokercontract.ContinuityAttemptTTL)
	if pending.ExpiresAt.Before(deadline) {
		deadline = pending.ExpiresAt
	}
	staged, err := stager.StageCredentialCustody(ctx, "stage-"+string(pending.ID), brokercontract.ContinuityGuard{
		SessionID: sess.ID, SessionIncarnation: sess.Incarnation(), OwnerPartition: ownerPartition, WorkloadPartition: workloadPartition,
	}, enrollmentRef(pending), deadline)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("%w: stage credential custody", ErrFailedPrecondition)
	}
	custody, err := session.NewBrokerCredentialCustody(staged.RecoveryReference, sess.Incarnation(), ownerPartition, workloadPartition, staged.ProfileDigest, staged.Providers, staged.ExpiresAt)
	if err != nil {
		return fmt.Errorf("%w: construct broker credential custody", ErrFailedPrecondition)
	}
	if err := sess.InstallBrokerCredentialCustody(pending, custody, now); err != nil {
		s.tombstoneStagedCustodyDetached(ctx, sess.ID, custody)
		return fmt.Errorf("%w: install broker credential custody", ErrFailedPrecondition)
	}
	if err := s.saveSession(ctx, sess); err != nil {
		// A store error does not say whether the write landed. Reload: continue
		// only if this exact custody is durable, otherwise retire the staged row.
		if !s.custodyPersisted(ctx, sess.ID, custody) {
			s.tombstoneStagedCustodyDetached(ctx, sess.ID, custody)
			return fmt.Errorf("%w: persist broker credential custody", ErrInternal)
		}
	}
	if err := s.commitPendingCustody(ctx, sess, custody); err != nil {
		if errors.Is(err, errBrokerCustodyGuardMismatch) {
			if invalidateErr := s.invalidateBrokerCustody(ctx, sess, false); invalidateErr != nil {
				return invalidateErr
			}
		}
		return err
	}
	return nil
}

// custodyPersisted reports whether the stored session holds exactly custody.
func (s *Service) custodyPersisted(ctx context.Context, id session.SessionID, custody session.BrokerCredentialCustody) bool {
	loaded, err := s.cfg.Store.Load(ctx, id)
	if err != nil || loaded == nil {
		return false
	}
	stored, ok := loaded.BrokerCredentialCustody()
	return ok && stored.RecoveryReference() == custody.RecoveryReference() && stored.SessionIncarnation() == custody.SessionIncarnation()
}

// tombstoneStagedCustodyDetached retires a staged custody row whose host save
// failed. It is best effort: an unretired staged row is never resolvable (Load
// never promotes staged custody) and expires at its native expiry.
func (s *Service) tombstoneStagedCustodyDetached(ctx context.Context, id session.SessionID, custody session.BrokerCredentialCustody) {
	service, ok := s.brokerService().(brokercontract.CredentialContinuityService)
	if !ok {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), engineCloseTimeout)
	defer cancel()
	now := time.Now()
	if s.cfg.Now != nil {
		now = s.cfg.Now()
	}
	deadline := now.Add(brokercontract.ContinuityAttemptTTL)
	if custody.ExpiresAt().Before(deadline) {
		deadline = custody.ExpiresAt()
	}
	_ = service.TombstoneCredentialCustody(cleanupCtx, brokercontract.CustodyAssertion{
		Guard: brokercontract.ContinuityGuard{
			SessionID: id, SessionIncarnation: custody.SessionIncarnation(),
			OwnerPartition: custody.OwnerPartition(), WorkloadPartition: custody.WorkloadPartition(),
			ProfileDigest: custody.ProfileDigest(), Providers: custody.Providers(),
		}, RecoveryReference: custody.RecoveryReference(), AttemptDeadline: deadline,
	})
}

func (s *Service) recoverBrokerAttachment(ctx context.Context, sess *session.Session, failedGeneration uint64) (*localBrokerAttachment, error) {
	custody, ok := sess.BrokerCredentialCustody()
	if !ok {
		return nil, fmt.Errorf("%w: broker credential custody is absent", ErrFailedPrecondition)
	}
	if s.cfg.BrokerWorkloadIdentity == nil {
		// Unknown identity (not configured or token unreadable) is not a
		// rotation: fail closed without destroying recoverable custody.
		return nil, fmt.Errorf("%w: broker recovery identity is unavailable", ErrFailedPrecondition)
	}
	if sess.Owner == nil {
		if err := s.invalidateBrokerCustody(ctx, sess, false); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: broker recovery owner is unavailable", ErrFailedPrecondition)
	}
	owner, err := brokercontract.ContinuityPrincipalPartition(brokercontract.ContinuityPartitionOwner, sess.Owner)
	if err != nil {
		if invalidateErr := s.invalidateBrokerCustody(ctx, sess, false); invalidateErr != nil {
			return nil, invalidateErr
		}
		return nil, fmt.Errorf("%w: derive broker owner partition", ErrFailedPrecondition)
	}
	workload, err := brokercontract.ContinuityPrincipalPartition(brokercontract.ContinuityPartitionWorkload, s.cfg.BrokerWorkloadIdentity)
	if err != nil {
		if invalidateErr := s.invalidateBrokerCustody(ctx, sess, false); invalidateErr != nil {
			return nil, invalidateErr
		}
		return nil, fmt.Errorf("%w: derive broker workload partition", ErrFailedPrecondition)
	}
	storedOwner, storedWorkload := custody.OwnerPartition(), custody.WorkloadPartition()
	if subtle.ConstantTimeCompare(owner[:], storedOwner[:]) != 1 || subtle.ConstantTimeCompare(workload[:], storedWorkload[:]) != 1 {
		if err := s.invalidateBrokerCustody(ctx, sess, false); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: broker recovery principal changed", ErrFailedPrecondition)
	}

	baseService, baseGeneration := s.brokerSnapshot()
	recoveryService := baseService
	var replacement brokercontract.Service
	var replacementClose func() error
	if baseGeneration == failedGeneration && s.cfg.MCPBrokerFactory != nil {
		// Candidate construction may dial. It is deliberately outside the
		// replacement mutex; publication below is the generation CAS.
		fresh, closeFresh, factoryErr := s.cfg.MCPBrokerFactory(ctx)
		if factoryErr != nil {
			return nil, fmt.Errorf("%w: replace MCP broker client", ErrInternal)
		}
		if fresh == nil || closeFresh == nil {
			if closeFresh != nil {
				_ = closeFresh()
			}
			return nil, fmt.Errorf("%w: incomplete replacement MCP broker client", ErrInternal)
		}
		replacement, replacementClose, recoveryService = fresh, closeFresh, fresh
	}

	service, ok := recoveryService.(brokercontract.CredentialContinuityService)
	if !ok {
		return nil, fmt.Errorf("%w: credential continuity is unavailable", ErrFailedPrecondition)
	}
	now := time.Now()
	if s.cfg.Now != nil {
		now = s.cfg.Now()
	}
	deadline := now.Add(brokercontract.ContinuityAttemptTTL)
	if custody.ExpiresAt().Before(deadline) {
		deadline = custody.ExpiresAt()
	}
	attemptKey := recoveryAttemptKey(sess, custody)
	s.brokerRecoveryMu.Lock()
	attempt, exists := s.brokerRecovery[attemptKey]
	if !exists || !attempt.deadline.After(now) {
		capacity := s.cfg.MaxSessionEngines
		if capacity <= 0 {
			capacity = 64
		}
		if !exists && len(s.brokerRecovery) >= capacity {
			s.brokerRecoveryMu.Unlock()
			if replacementClose != nil {
				_ = replacementClose()
			}
			return nil, fmt.Errorf("%w: broker recovery capacity reached", ErrUnavailable)
		}
		attempt = brokerRecoveryAttempt{requestID: fmt.Sprintf("recover-%s-%d", sess.ID, now.UnixNano()), deadline: deadline}
		if s.brokerRecovery == nil {
			s.brokerRecovery = make(map[brokerRecoveryAttemptKey]brokerRecoveryAttempt)
		}
		s.brokerRecovery[attemptKey] = attempt
	}
	s.brokerRecoveryMu.Unlock()
	deadline = attempt.deadline
	assertion := brokercontract.CustodyAssertion{Guard: brokercontract.ContinuityGuard{
		SessionID: sess.ID, SessionIncarnation: custody.SessionIncarnation(),
		OwnerPartition: storedOwner, WorkloadPartition: storedWorkload,
		ProfileDigest: custody.ProfileDigest(), Providers: custody.Providers(),
	}, RecoveryReference: custody.RecoveryReference(), AttemptDeadline: deadline}
	if _, hasPending := sess.PendingWorkspaceEnrollment(); hasPending {
		if err := s.commitPendingCustody(ctx, sess, custody); err != nil {
			if errors.Is(err, errBrokerCustodyGuardMismatch) {
				if invalidateErr := s.invalidateBrokerCustody(ctx, sess, false); invalidateErr != nil {
					return nil, invalidateErr
				}
			}
			return nil, err
		}
	}
	pending, hasPending := sess.PendingWorkspaceEnrollment()
	recovered, err := service.RecoverCredentialAttachment(ctx, assertion, attempt.requestID)
	if err != nil || recovered.Attachment == nil || recovered.Attachment.Binding() == "" {
		if err == nil {
			err = brokercontract.ErrContinuityUnavailable
		}
		if errors.Is(err, brokercontract.ErrContinuityProfileChanged) {
			if invalidateErr := s.invalidateBrokerCustody(ctx, sess, false); invalidateErr != nil {
				return nil, invalidateErr
			}
		}
		if replacementClose != nil {
			_ = replacementClose()
		}
		return nil, fmt.Errorf("%w: recover broker credential attachment", ErrFailedPrecondition)
	}
	_, recoveredGeneration := s.brokerSnapshot()
	local := &localBrokerAttachment{attachment: recovered.Attachment, generation: recoveredGeneration, owned: true}
	tools := brokerTools(local)
	names := make([]string, 0, len(tools))
	for _, candidate := range tools {
		if candidate == nil {
			s.rollbackBrokerAttachment(ctx, local)
			return nil, fmt.Errorf("%w: recovered broker catalogue contains nil tool", ErrFailedPrecondition)
		}
		names = append(names, candidate.Spec().Name)
	}
	if !session.ValidWorkspaceEnrollmentToolNames(names) {
		s.rollbackBrokerAttachment(ctx, local)
		if replacementClose != nil {
			_ = replacementClose()
		}
		return nil, fmt.Errorf("%w: recovered broker catalogue is invalid", ErrFailedPrecondition)
	}
	if replacement != nil {
		adopted := false
		var winnerGeneration uint64
		s.brokerReplacementMu.Lock()
		if s.brokerClosed {
			s.brokerReplacementMu.Unlock()
			s.rollbackBrokerAttachment(ctx, local)
			_ = replacementClose()
			return nil, fmt.Errorf("%w: MCP broker is closed", ErrUnavailable)
		}
		_, currentGeneration := s.brokerSnapshot()
		if currentGeneration == baseGeneration {
			s.brokerGenerationMu.Lock()
			oldClose := s.brokerFactoryClose
			s.brokerCurrent, s.brokerFactoryClose = replacement, replacementClose
			s.brokerGeneration++
			local.generation = s.brokerGeneration
			if oldClose != nil {
				s.brokerRetiredCloses[baseGeneration] = oldClose
			}
			s.brokerGenerationMu.Unlock()
			adopted = true
		} else {
			winnerGeneration = currentGeneration
		}
		s.brokerReplacementMu.Unlock()
		if adopted {
			s.releaseRetiredBrokerClient(baseGeneration)
		}
		if !adopted {
			// Another recovery won publication. Its client owns the generation;
			// retire only this unadvertised candidate, then replay the identical
			// request through the winner so the returned handle is winner-bound.
			s.rollbackBrokerAttachment(ctx, local)
			_ = replacementClose()
			return s.recoverBrokerAttachment(ctx, sess, winnerGeneration+1)
		}
	}
	pending, hasPending = sess.PendingWorkspaceEnrollment()
	if hasPending {
		if err := sess.CompleteWorkspaceEnrollmentWithBinding(pending, local.attachment.Binding(), names); err != nil {
			s.rollbackBrokerAttachment(ctx, local)
			return nil, fmt.Errorf("%w: complete recovered workspace enrollment", ErrFailedPrecondition)
		}
	} else if err := sess.AdoptRecoveredBrokerCatalogue(custody.RecoveryReference(), sess.ExternalBinding, local.attachment.Binding(), names); err != nil {
		s.rollbackBrokerAttachment(ctx, local)
		return nil, fmt.Errorf("%w: adopt recovered broker catalogue", ErrFailedPrecondition)
	}
	if err := s.saveSession(ctx, sess); err != nil {
		loaded, loadErr := s.cfg.Store.Load(ctx, sess.ID)
		persisted := loadErr == nil && loaded != nil && loaded.ExternalBinding == local.attachment.Binding()
		if hasPending {
			persisted = persisted && func() bool { _, ok := loaded.PendingWorkspaceEnrollment(); return !ok }()
		}
		if !persisted {
			s.rollbackBrokerAttachment(ctx, local)
			return nil, fmt.Errorf("%w: persist recovered broker authority", ErrInternal)
		}
	}
	// After the candidate is durable, ownership has transferred even if Commit
	// reports an ambiguous error; never Abort that provisional state.
	if err := s.commitBrokerAttachment(ctx, sess.ID, local); err != nil {
		local.owned = false
		return nil, fmt.Errorf("%w: commit recovered broker attachment: %v", ErrInternal, err)
	}
	s.brokerRecoveryMu.Lock()
	delete(s.brokerRecovery, attemptKey)
	s.brokerRecoveryMu.Unlock()
	return local, nil
}

// invalidateBrokerCustody removes host authority durably before any broker-side
// cleanup. It always uses the stored custody guard and captured B2 binding: current
// owner/workload configuration is not authority to revoke a prior enrollment.
func (s *Service) invalidateBrokerCustody(ctx context.Context, sess *session.Session, durableAuthorityGone bool) error {
	custody, ok := sess.BrokerCredentialCustody()
	if !ok {
		return nil
	}
	binding := sess.ExternalBinding
	if !durableAuthorityGone {
		if _, err := sess.ClearBrokerCredentialCustody(custody.RecoveryReference()); err != nil {
			return fmt.Errorf("%w: clear broker custody", ErrFailedPrecondition)
		}
		if err := s.saveSession(ctx, sess); err != nil {
			loaded, loadErr := s.cfg.Store.Load(ctx, sess.ID)
			if loadErr != nil || loaded == nil {
				if restoreErr := sess.RestoreBrokerCredentialCustody(custody); restoreErr != nil {
					return fmt.Errorf("%w: restore broker custody after failed invalidation", ErrInternal)
				}
				return fmt.Errorf("%w: persist broker custody invalidation", ErrInternal)
			}
			if _, stillCustody := loaded.BrokerCredentialCustody(); stillCustody {
				if restoreErr := sess.RestoreBrokerCredentialCustody(custody); restoreErr != nil {
					return fmt.Errorf("%w: restore broker custody after failed invalidation", ErrInternal)
				}
				return fmt.Errorf("%w: persist broker custody invalidation", ErrInternal)
			}
		}
	}

	// Nothing local may retain the retired authority after the durable revocation.
	s.discardBrokerRecoveryAttempts(sess.ID)
	s.closeSessionLocal(sess.ID)
	service, continuity := s.brokerService().(brokercontract.CredentialContinuityService)
	if continuity {
		now := time.Now()
		if s.cfg.Now != nil {
			now = s.cfg.Now()
		}
		deadline := now.Add(brokercontract.ContinuityAttemptTTL)
		if custody.ExpiresAt().Before(deadline) {
			deadline = custody.ExpiresAt()
		}
		assertion := brokercontract.CustodyAssertion{Guard: brokercontract.ContinuityGuard{
			SessionID: sess.ID, SessionIncarnation: custody.SessionIncarnation(), OwnerPartition: custody.OwnerPartition(), WorkloadPartition: custody.WorkloadPartition(), ProfileDigest: custody.ProfileDigest(), Providers: custody.Providers(),
		}, RecoveryReference: custody.RecoveryReference(), AttemptDeadline: deadline}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), engineCloseTimeout)
		err := service.TombstoneCredentialCustody(cleanupCtx, assertion)
		cancel()
		if err != nil {
			s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "broker custody tombstone failed", "operation", "tombstone")
		}
	} else {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "broker custody tombstone unavailable", "operation", "tombstone")
	}
	if err := s.deleteBrokerSessionLocked(ctx, sess.ID, binding); err != nil {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "broker custody logical session cleanup failed", "operation", "delete")
	}
	return nil
}

var errBrokerCustodyGuardMismatch = errors.New("broker custody guard mismatch")

func (s *Service) commitPendingCustody(ctx context.Context, sess *session.Session, custody session.BrokerCredentialCustody) error {
	if sess == nil || s.cfg.BrokerWorkloadIdentity == nil {
		// An unconfigured or unreadable workload identity is unknown, not
		// rotated: fail closed but keep custody, which a correctly configured
		// host can still commit.
		return fmt.Errorf("%w: broker workload identity is unavailable", ErrFailedPrecondition)
	}
	ownerPartition, err := brokercontract.ContinuityPrincipalPartition(brokercontract.ContinuityPartitionOwner, sess.Owner)
	storedOwner := custody.OwnerPartition()
	if err != nil || subtle.ConstantTimeCompare(ownerPartition[:], storedOwner[:]) != 1 {
		return fmt.Errorf("%w: %w", ErrFailedPrecondition, errBrokerCustodyGuardMismatch)
	}
	workloadPartition, err := brokercontract.ContinuityPrincipalPartition(brokercontract.ContinuityPartitionWorkload, s.cfg.BrokerWorkloadIdentity)
	storedWorkload := custody.WorkloadPartition()
	if err != nil || subtle.ConstantTimeCompare(workloadPartition[:], storedWorkload[:]) != 1 {
		return fmt.Errorf("%w: %w", ErrFailedPrecondition, errBrokerCustodyGuardMismatch)
	}
	service, ok := s.brokerService().(brokercontract.CredentialContinuityService)
	if !ok {
		return fmt.Errorf("%w: credential continuity is unavailable", ErrFailedPrecondition)
	}
	now := time.Now()
	if s.cfg.Now != nil {
		now = s.cfg.Now()
	}
	deadline := now.Add(brokercontract.ContinuityAttemptTTL)
	if custody.ExpiresAt().Before(deadline) {
		deadline = custody.ExpiresAt()
	}
	assertion := brokercontract.CustodyAssertion{
		Guard: brokercontract.ContinuityGuard{
			SessionID: sess.ID, SessionIncarnation: custody.SessionIncarnation(),
			OwnerPartition: custody.OwnerPartition(), WorkloadPartition: custody.WorkloadPartition(),
			ProfileDigest: custody.ProfileDigest(), Providers: custody.Providers(),
		}, RecoveryReference: custody.RecoveryReference(), AttemptDeadline: deadline,
	}
	if err := service.CommitCredentialCustody(ctx, assertion); err != nil {
		if errors.Is(err, brokercontract.ErrContinuityProfileChanged) {
			return fmt.Errorf("%w: %w", ErrFailedPrecondition, errBrokerCustodyGuardMismatch)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("%w: commit credential custody", ErrFailedPrecondition)
	}
	return nil
}

func cancelWorkspaceEnrollmentDetached(ctx context.Context, enroller brokercontract.WorkspaceEnrollmentHandle, ref brokercontract.WorkspaceEnrollmentRef) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), engineCloseTimeout)
	defer cancel()
	_, _ = enroller.CancelWorkspaceEnrollment(cleanupCtx, ref)
}

func sameWorkspaceEnrollmentRef(left, right brokercontract.WorkspaceEnrollmentRef) bool {
	return left.ID == right.ID &&
		left.RequiredServices == right.RequiredServices &&
		left.ExpiresAt.Equal(right.ExpiresAt)
}

func pendingEnrollment(ref brokercontract.WorkspaceEnrollmentRef) session.PendingWorkspaceEnrollment {
	return session.PendingWorkspaceEnrollment{ID: ref.ID, RequiredServices: ref.RequiredServices, ExpiresAt: ref.ExpiresAt}
}

func enrollmentRef(pending session.PendingWorkspaceEnrollment) brokercontract.WorkspaceEnrollmentRef {
	return brokercontract.WorkspaceEnrollmentRef{ID: pending.ID, RequiredServices: pending.RequiredServices, ExpiresAt: pending.ExpiresAt}
}

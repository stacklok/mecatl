package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/stacklok/mecatl/engine/session"
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

	sess, enroller, release, err := s.workspaceEnrollmentTarget(ctx, id)
	if err != nil {
		if sess != nil && errors.Is(err, brokercontract.ErrStateUnavailable) {
			return s.settleTerminalWorkspaceEnrollment(ctx, sess, brokercontract.WorkspaceEnrollmentFailed)
		}
		return WorkspaceEnrollmentProjection{}, err
	}
	defer func() { release() }()

	pending, exists := sess.PendingWorkspaceEnrollment()
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
	result, err := enroller.CancelWorkspaceEnrollment(ctx, expectedRef)
	if err != nil {
		if errors.Is(err, brokercontract.ErrAuthorizationNotFound) {
			// The broker's transaction is already gone (a prior terminal Observe
			// already tore it down): there is nothing left to cancel, but the
			// aggregate's own pending record must still be cleared, or this
			// session stays wedged behind a cancel that can never succeed on the
			// broker side again.
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
	if err := sess.AbortWorkspaceEnrollment(pending.ID); err != nil {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: clear terminal workspace enrollment", ErrFailedPrecondition)
	}
	if err := s.saveSession(ctx, sess); err != nil {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: persist terminal workspace enrollment", ErrInternal)
	}
	return WorkspaceEnrollmentProjection{Ref: enrollmentRef(pending), Status: status}, nil
}

func (s *Service) workspaceEnrollmentTarget(ctx context.Context, id session.SessionID) (*session.Session, brokercontract.WorkspaceEnrollmentHandle, func(), error) {
	sess, err := s.cfg.Store.Load(ctx, id)
	if err != nil || sess == nil || sess.ID != id || s.authorizeSession(ctx, sess) != nil {
		return nil, nil, nil, ErrNotFound
	}
	if !s.brokerConfigured() || sess.State != session.StateIdle || sess.Conversation == nil || len(sess.Conversation.Messages) != 0 {
		return nil, nil, nil, fmt.Errorf("%w: workspace enrollment must precede the first prompt", ErrFailedPrecondition)
	}
	if _, live := s.LookupRun(id); live {
		return nil, nil, nil, fmt.Errorf("%w: session has an active run", ErrFailedPrecondition)
	}
	brokerUnlock := s.brokerMu.lock(id)
	_, attemptedGeneration := s.brokerSnapshot()
	local, err := s.openBrokerAttachment(ctx, id, sess.ExternalBinding, true)
	if err != nil {
		if !errors.Is(err, ErrBrokerBindingMismatch) && !errors.Is(err, brokercontract.ErrBrokerIncarnationLost) {
			brokerUnlock()
			return sess, nil, nil, err
		}
		// This seam is pre-prompt by construction, so a lost incarnation costs the
		// session nothing durable: adopt the live one rather than strand it behind
		// a binding no restarted process can ever match.
		failedGeneration := s.brokerAttachmentGenerationFor(id)
		if failedGeneration == 0 {
			failedGeneration = attemptedGeneration
		}
		if local, err = s.rebindBrokerAttachment(ctx, sess, failedGeneration, errors.Is(err, brokercontract.ErrBrokerIncarnationLost)); err != nil {
			brokerUnlock()
			return sess, nil, nil, err
		}
	}
	enroller, ok := local.attachment.(brokercontract.WorkspaceEnrollmentHandle)
	if !ok {
		brokerUnlock()
		return nil, nil, nil, fmt.Errorf("%w: workspace services are not configured", ErrFailedPrecondition)
	}
	return sess, enroller, brokerUnlock, nil
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

func (s *Service) commitPendingCustody(ctx context.Context, sess *session.Session, custody session.BrokerCredentialCustody) error {
	if sess == nil || s.cfg.BrokerWorkloadIdentity == nil {
		return fmt.Errorf("%w: credential continuity is unavailable", ErrFailedPrecondition)
	}
	ownerPartition, err := brokercontract.ContinuityPrincipalPartition(brokercontract.ContinuityPartitionOwner, sess.Owner)
	storedOwner := custody.OwnerPartition()
	if err != nil || subtle.ConstantTimeCompare(ownerPartition[:], storedOwner[:]) != 1 {
		return fmt.Errorf("%w: broker owner partition changed", ErrFailedPrecondition)
	}
	workloadPartition, err := brokercontract.ContinuityPrincipalPartition(brokercontract.ContinuityPartitionWorkload, s.cfg.BrokerWorkloadIdentity)
	storedWorkload := custody.WorkloadPartition()
	if err != nil || subtle.ConstantTimeCompare(workloadPartition[:], storedWorkload[:]) != 1 {
		return fmt.Errorf("%w: broker workload partition changed", ErrFailedPrecondition)
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

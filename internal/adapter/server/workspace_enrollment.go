package server

import (
	"context"
	"errors"
	"fmt"

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
			return s.settleUnavailableWorkspaceEnrollment(ctx, sess)
		}
		return WorkspaceEnrollmentProjection{}, err
	}
	defer func() { release() }()

	pending, exists := sess.PendingWorkspaceEnrollment()
	if !exists {
		presentation, beginErr := enroller.BeginWorkspaceEnrollment(ctx)
		if beginErr != nil || !presentation.Valid() {
			return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: begin workspace enrollment", ErrFailedPrecondition)
		}
		pending = pendingEnrollment(presentation.Ref)
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

	expectedRef := enrollmentRef(pending)
	result, err := enroller.ObserveWorkspaceEnrollment(ctx, expectedRef)
	if err != nil || !result.Valid() || !sameWorkspaceEnrollmentRef(result.Ref, expectedRef) {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: observe workspace enrollment", ErrFailedPrecondition)
	}
	if result.Status != brokercontract.WorkspaceEnrollmentConnected {
		return WorkspaceEnrollmentProjection{Ref: result.Ref, Status: result.Status}, nil
	}

	// The authenticated result is the single snapshot for both executable wrappers
	// and durable authority. Never re-read Attachment.Tools during this rebuild: a
	// remote attachment may advance between observation and engine construction.
	exactTools := result.Catalogue.Tools()
	toolNames := make([]string, len(exactTools))
	for i, candidate := range exactTools {
		toolNames[i] = candidate.Spec().Name
	}
	release()
	release = func() {}
	sel := ProviderSelector{ProviderID: sess.ProviderID, ModelID: sess.ModelID, ReasoningEffort: sess.ReasoningEffort}
	if _, err := s.buildAndRegisterSessionEngineWithBrokerTools(ctx, sess, sel, profileForSession(sess), sess.Mode, true, exactTools, true); err != nil {
		return WorkspaceEnrollmentProjection{}, err
	}
	if err := sess.CompleteWorkspaceEnrollment(pending, toolNames); err != nil {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: complete workspace enrollment", ErrFailedPrecondition)
	}
	if err := s.saveSession(ctx, sess); err != nil {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: persist workspace enrollment completion", ErrInternal)
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
			return s.settleUnavailableWorkspaceEnrollment(ctx, sess)
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
	if err != nil || !result.Valid() || !sameWorkspaceEnrollmentRef(result.Ref, expectedRef) {
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

func (s *Service) settleUnavailableWorkspaceEnrollment(ctx context.Context, sess *session.Session) (WorkspaceEnrollmentProjection, error) {
	pending, ok := sess.PendingWorkspaceEnrollment()
	if !ok {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: workspace enrollment is not pending", ErrFailedPrecondition)
	}
	if err := sess.AbortWorkspaceEnrollment(pending.ID); err != nil {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: clear unavailable workspace enrollment", ErrFailedPrecondition)
	}
	if err := s.saveSession(ctx, sess); err != nil {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: persist unavailable workspace enrollment", ErrInternal)
	}
	return WorkspaceEnrollmentProjection{Ref: enrollmentRef(pending), Status: brokercontract.WorkspaceEnrollmentFailed}, nil
}

func (s *Service) workspaceEnrollmentTarget(ctx context.Context, id session.SessionID) (*session.Session, brokercontract.WorkspaceEnrollmentAttachment, func(), error) {
	sess, err := s.cfg.Store.Load(ctx, id)
	if err != nil || sess == nil || sess.ID != id || s.authorizeSession(ctx, sess) != nil {
		return nil, nil, nil, ErrNotFound
	}
	if s.cfg.MCPBroker == nil || sess.State != session.StateIdle || sess.Conversation == nil || len(sess.Conversation.Messages) != 0 {
		return nil, nil, nil, fmt.Errorf("%w: workspace enrollment must precede the first prompt", ErrFailedPrecondition)
	}
	if _, live := s.LookupRun(id); live {
		return nil, nil, nil, fmt.Errorf("%w: session has an active run", ErrFailedPrecondition)
	}
	brokerUnlock := s.brokerMu.lock(id)
	local, err := s.openBrokerAttachment(ctx, id, sess.ExternalBinding, true)
	if err != nil {
		if !errors.Is(err, ErrBrokerBindingMismatch) {
			brokerUnlock()
			return sess, nil, nil, err
		}
		// This seam is pre-prompt by construction, so a lost incarnation costs the
		// session nothing durable: adopt the live one rather than strand it behind
		// a binding no restarted process can ever match.
		if local, err = s.rebindBrokerAttachment(ctx, sess); err != nil {
			brokerUnlock()
			return sess, nil, nil, err
		}
	}
	enroller, ok := local.attachment.(brokercontract.WorkspaceEnrollmentAttachment)
	if !ok {
		brokerUnlock()
		return nil, nil, nil, fmt.Errorf("%w: workspace services are not configured", ErrFailedPrecondition)
	}
	return sess, enroller, brokerUnlock, nil
}

func cancelWorkspaceEnrollmentDetached(ctx context.Context, enroller brokercontract.WorkspaceEnrollmentAttachment, ref brokercontract.WorkspaceEnrollmentRef) {
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

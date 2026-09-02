package server

import (
	"context"
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

	sess, enroller, release, err := s.workspaceEnrollmentTarget(ctx, id)
	if err != nil {
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
			_, _ = enroller.CancelWorkspaceEnrollment(context.WithoutCancel(ctx), presentation.Ref)
			return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: record workspace enrollment", ErrFailedPrecondition)
		}
		if err := s.cfg.Store.Save(ctx, sess); err != nil {
			_, _ = enroller.CancelWorkspaceEnrollment(context.WithoutCancel(ctx), presentation.Ref)
			return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: persist workspace enrollment", ErrInternal)
		}
		return WorkspaceEnrollmentProjection{Ref: presentation.Ref, Status: brokercontract.WorkspaceEnrollmentPending, URL: presentation.URL}, nil
	}

	result, err := enroller.ObserveWorkspaceEnrollment(ctx, enrollmentRef(pending))
	if err != nil || !result.Valid() {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: observe workspace enrollment", ErrFailedPrecondition)
	}
	if result.Status != brokercontract.WorkspaceEnrollmentConnected {
		return WorkspaceEnrollmentProjection{Ref: result.Ref, Status: result.Status}, nil
	}

	// The attachment has already replaced its catalogue atomically. Rebuild the
	// engine while the aggregate still blocks prompts, then publish the exact tool
	// authority and clear the pending gate in one aggregate transition.
	release()
	release = func() {}
	sel := ProviderSelector{ProviderID: sess.ProviderID, ModelID: sess.ModelID, ReasoningEffort: sess.ReasoningEffort}
	if _, err := s.buildAndRegisterSessionEngine(ctx, sess, sel, profileForSession(sess), sess.Mode, true); err != nil {
		return WorkspaceEnrollmentProjection{}, err
	}
	if err := sess.CompleteWorkspaceEnrollment(pending, result.Catalogue.ToolNames()); err != nil {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: complete workspace enrollment", ErrFailedPrecondition)
	}
	if err := s.cfg.Store.Save(ctx, sess); err != nil {
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
	sess, enroller, release, err := s.workspaceEnrollmentTarget(ctx, id)
	if err != nil {
		return WorkspaceEnrollmentProjection{}, err
	}
	defer func() { release() }()
	pending, ok := sess.PendingWorkspaceEnrollment()
	if !ok || pending.ID != enrollmentID {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: stale workspace enrollment", ErrFailedPrecondition)
	}
	result, err := enroller.CancelWorkspaceEnrollment(ctx, enrollmentRef(pending))
	if err != nil || !result.Valid() {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: cancel workspace enrollment", ErrFailedPrecondition)
	}
	if err := sess.AbortWorkspaceEnrollment(enrollmentID); err != nil {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: clear workspace enrollment", ErrFailedPrecondition)
	}
	if err := s.cfg.Store.Save(ctx, sess); err != nil {
		return WorkspaceEnrollmentProjection{}, fmt.Errorf("%w: persist workspace enrollment cancellation", ErrInternal)
	}
	return WorkspaceEnrollmentProjection{Ref: result.Ref, Status: result.Status}, nil
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
		brokerUnlock()
		return nil, nil, nil, err
	}
	enroller, ok := local.attachment.(brokercontract.WorkspaceEnrollmentAttachment)
	if !ok {
		brokerUnlock()
		return nil, nil, nil, fmt.Errorf("%w: workspace services are not configured", ErrFailedPrecondition)
	}
	return sess, enroller, brokerUnlock, nil
}

func pendingEnrollment(ref brokercontract.WorkspaceEnrollmentRef) session.PendingWorkspaceEnrollment {
	return session.PendingWorkspaceEnrollment{ID: ref.ID, RequiredServices: ref.RequiredServices, ExpiresAt: ref.ExpiresAt}
}

func enrollmentRef(pending session.PendingWorkspaceEnrollment) brokercontract.WorkspaceEnrollmentRef {
	return brokercontract.WorkspaceEnrollmentRef{ID: pending.ID, RequiredServices: pending.RequiredServices, ExpiresAt: pending.ExpiresAt}
}

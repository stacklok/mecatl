package server

import (
	"context"
	"fmt"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// WorkspaceEnrollmentProjection is the presentation-safe state returned to a client.
type WorkspaceEnrollmentProjection struct {
	Ref    brokercontract.WorkspaceEnrollmentRef
	Status brokercontract.WorkspaceEnrollmentStatus
	URL    string
}

func (s *Service) ConnectWorkspaceServices(ctx context.Context, id session.SessionID) (WorkspaceEnrollmentProjection, error) {
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	if err := s.acquireLease(ctx, id); err != nil {
		return WorkspaceEnrollmentProjection{}, err
	}
	return s.connectWorkspaceServicesLocked(ctx, id)
}

func (s *Service) connectWorkspaceServicesLocked(ctx context.Context, id session.SessionID) (WorkspaceEnrollmentProjection, error) {
	if _, err := s.GetSession(ctx, id); err != nil {
		return WorkspaceEnrollmentProjection{}, err
	}
	if s.cfg.SessionBroker == nil {
		return WorkspaceEnrollmentProjection{}, ErrFailedPrecondition
	}
	return s.connectSessionBrokerLocked(ctx, id)
}

func (s *Service) RetryWorkspaceEnrollment(ctx context.Context, id session.SessionID, enrollmentID session.WorkspaceEnrollmentID) (WorkspaceEnrollmentProjection, error) {
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	if err := s.acquireLease(ctx, id); err != nil {
		return WorkspaceEnrollmentProjection{}, err
	}
	if _, err := s.cancelWorkspaceEnrollmentLocked(ctx, id, enrollmentID); err != nil {
		return WorkspaceEnrollmentProjection{}, err
	}
	return s.connectWorkspaceServicesLocked(ctx, id)
}

func (s *Service) CancelWorkspaceEnrollment(ctx context.Context, id session.SessionID, enrollmentID session.WorkspaceEnrollmentID) (WorkspaceEnrollmentProjection, error) {
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	if err := s.acquireLease(ctx, id); err != nil {
		return WorkspaceEnrollmentProjection{}, err
	}
	return s.cancelWorkspaceEnrollmentLocked(ctx, id, enrollmentID)
}

func (s *Service) cancelWorkspaceEnrollmentLocked(ctx context.Context, id session.SessionID, enrollmentID session.WorkspaceEnrollmentID) (WorkspaceEnrollmentProjection, error) {
	if _, err := s.GetSession(ctx, id); err != nil {
		return WorkspaceEnrollmentProjection{}, err
	}
	if s.cfg.SessionBroker == nil {
		return WorkspaceEnrollmentProjection{}, ErrFailedPrecondition
	}
	return s.cancelSessionBrokerEnrollmentLocked(ctx, id, enrollmentID)
}

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

func (s *Service) withdrawBrokerEngine(id session.SessionID) {
	s.mu.Lock()
	engine, ok := s.sessionEngines[id]
	delete(s.sessionEngines, id)
	s.mu.Unlock()
	if ok && engine.close != nil {
		if err := engine.close(); err != nil {
			s.cfg.Diagnostics.Log(context.Background(), port.LevelWarn, "broker session engine withdrawal failed")
		}
	}
}

func enrollmentRef(pending session.PendingWorkspaceEnrollment) brokercontract.WorkspaceEnrollmentRef {
	return brokercontract.WorkspaceEnrollmentRef{ID: pending.ID, RequiredServices: pending.RequiredServices, ExpiresAt: pending.ExpiresAt}
}

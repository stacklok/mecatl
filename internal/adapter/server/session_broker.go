package server

import (
	"context"
	"errors"
	"slices"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

// These wrappers add only the host's durable dispatch fence. The original tool
// retains preflight, frozen descriptors and exact-byte resume semantics.
type durableSessionTool struct {
	tool.Tool
	ref       c.SessionRef
	catalogue c.CatalogueRef
}

func (*durableSessionTool) DispatchSerialTool() {}
func (t *durableSessionTool) BrokerInvocationRefs() (session.BrokerSessionRef, session.BrokerCatalogueRef) {
	return session.BrokerSessionRef(t.ref), session.BrokerCatalogueRef(t.catalogue)
}

func (t *durableSessionTool) BrokerInvocationDisposition(err error) session.BrokerAttemptDisposition {
	if b, ok := t.Tool.(interface {
		BrokerInvocationDisposition(error) session.BrokerAttemptDisposition
	}); ok {
		return b.BrokerInvocationDisposition(err)
	}
	return session.BrokerAttemptUnknown
}

type durableSessionAuthTool struct {
	*durableSessionTool
	requester tool.AuthorizationRequester
}

func (t *durableSessionAuthTool) RequestAuthorization(ctx context.Context, call session.ToolCall) (session.ExternalAuthorization, bool, error) {
	return t.requester.RequestAuthorization(ctx, call)
}
func (t *durableSessionAuthTool) AbortAuthorization(ctx context.Context, a session.ExternalAuthorization) error {
	return t.requester.AbortAuthorization(ctx, a)
}
func durableSessionTools(ref c.SessionRef, cat c.Catalogue, tools []tool.Tool) []tool.Tool {
	out := make([]tool.Tool, 0, len(tools))
	for _, t := range tools {
		base := &durableSessionTool{Tool: t, ref: ref, catalogue: cat.Ref()}
		if a, ok := t.(tool.AuthorizationRequester); ok {
			out = append(out, &durableSessionAuthTool{base, a})
		} else {
			out = append(out, base)
		}
	}
	return out
}

func effectiveSessionBrokerTools(sess *session.Session, ref c.SessionRef, cat c.Catalogue, tools []tool.Tool) []tool.Tool {
	access, _ := sess.BrokerAccess()
	eligible := make([]tool.Tool, 0, len(tools))
	for _, t := range tools {
		if slices.Contains(access.BrokerTools, t.Spec().Name) {
			eligible = append(eligible, t)
		}
	}
	return durableSessionTools(ref, cat, eligible)
}

func (s *Service) adoptSessionBrokerCatalogue(ctx context.Context, sess *session.Session, cat c.Catalogue) error {
	a, ok := sess.BrokerAccess()
	if !ok || a.Withdrawn || cat == nil || !cat.Valid() || (a.Connection != "" && a.Connection != cat.Connection()) {
		return ErrFailedPrecondition
	}
	candidate, err := brokerAuthorityCandidate(sess)
	if err != nil {
		return err
	}
	if err := candidate.AdoptBrokerCatalogue(a.Session, session.BrokerCatalogueRef(cat.Ref()), session.BrokerConnectionRef(cat.Connection()), a.ExpiresAt, cat.ToolNames()); err != nil {
		return err
	}
	if err := s.saveSession(ctx, candidate); err != nil {
		s.withdrawBrokerEngine(sess.ID)
		return errors.Join(errBrokerAuthoritySave, err)
	}
	// Preserve the parked run's aggregate and conversation identity after saving.
	return sess.AdoptBrokerCatalogue(a.Session, session.BrokerCatalogueRef(cat.Ref()), session.BrokerConnectionRef(cat.Connection()), a.ExpiresAt, cat.ToolNames())
}

var errBrokerAuthoritySave = errors.New("broker authority save failed; explicitly reload the session")

func brokerAuthorityCandidate(sess *session.Session) (*session.Session, error) {
	snapshot, err := sessnap.Of(sess)
	if err != nil {
		return nil, err
	}
	return snapshot.Restore()
}

func (s *Service) restoreSessionBrokerTools(ctx context.Context, sess *session.Session) ([]tool.Tool, error) {
	a, ok := sess.BrokerAccess()
	if !ok {
		return nil, nil
	}
	if a.Withdrawn {
		return nil, nil
	}
	ref := c.SessionRef(a.Session)
	if a.Current != nil {
		return nil, ErrFailedPrecondition
	}
	snapshot, err := s.cfg.SessionBroker.OpenSession(ctx, &ref)
	if err != nil {
		return nil, err
	}
	if snapshot.Ref != ref || !snapshot.ExpiresAt.Equal(a.ExpiresAt) {
		return nil, ErrFailedPrecondition
	}
	if err := s.adoptSessionBrokerCatalogue(ctx, sess, snapshot.Catalogue); err != nil {
		return nil, err
	}
	return effectiveSessionBrokerTools(sess, ref, snapshot.Catalogue, snapshot.Catalogue.Tools()), nil
}

func (s *Service) sessionBrokerEnrollmentTarget(ctx context.Context, id session.SessionID) (*session.Session, session.BrokerAccess, error) {
	sess, err := s.GetSession(ctx, id)
	if err != nil {
		return nil, session.BrokerAccess{}, err
	}
	if sess.State == session.StateCompleted {
		if err := sess.Reopen(); err != nil {
			return nil, session.BrokerAccess{}, err
		}
	}
	if sess.State != session.StateIdle || sess.Conversation == nil || s.IsLive(id) {
		return nil, session.BrokerAccess{}, ErrFailedPrecondition
	}
	a, ok := sess.BrokerAccess()
	if !ok {
		return nil, session.BrokerAccess{}, ErrFailedPrecondition
	}
	return sess, a, nil
}

func (s *Service) connectSessionBrokerLocked(ctx context.Context, id session.SessionID) (WorkspaceEnrollmentProjection, error) {
	sess, a, err := s.sessionBrokerEnrollmentTarget(ctx, id)
	if err != nil {
		return WorkspaceEnrollmentProjection{}, err
	}
	ref := c.SessionRef(a.Session)
	pending, exists := sess.PendingWorkspaceEnrollment()
	if a.Withdrawn && exists {
		if _, err := s.cfg.SessionBroker.CancelEnrollment(ctx, ref, c.EnrollmentRef(pending.ID)); err != nil {
			return WorkspaceEnrollmentProjection{}, err
		}
		candidate, err := brokerAuthorityCandidate(sess)
		if err != nil {
			return WorkspaceEnrollmentProjection{}, err
		}
		if err := candidate.AbortWorkspaceEnrollment(pending.ID); err != nil {
			return WorkspaceEnrollmentProjection{}, err
		}
		if err := s.saveSession(ctx, candidate); err != nil {
			return WorkspaceEnrollmentProjection{}, errors.Join(errBrokerAuthoritySave, err)
		}
		if err := sess.AbortWorkspaceEnrollment(pending.ID); err != nil {
			return WorkspaceEnrollmentProjection{}, err
		}
		exists = false
	}
	if a.Withdrawn && !exists {
		// Only explicit Connect can reopen withdrawal. Finish exact old-revision
		// cleanup, then save the empty replacement revision before starting Begin.
		result := c.AlreadyDisconnected
		if a.Connection != "" {
			result, err = s.cfg.SessionBroker.DisconnectTools(ctx, ref, c.ConnectionRef(a.Connection))
		}
		if err != nil {
			return WorkspaceEnrollmentProjection{}, err
		}
		if result != c.Disconnected && result != c.AlreadyDisconnected {
			return WorkspaceEnrollmentProjection{}, ErrFailedPrecondition
		}
		snapshot, err := s.cfg.SessionBroker.OpenSession(ctx, &ref)
		if err != nil {
			return WorkspaceEnrollmentProjection{}, err
		}
		if snapshot.Ref != ref || !snapshot.ExpiresAt.Equal(a.ExpiresAt) || snapshot.Catalogue == nil || !snapshot.Catalogue.Valid() || snapshot.Catalogue.Connection() != a.Connection || len(snapshot.Catalogue.ToolNames()) != 0 {
			return WorkspaceEnrollmentProjection{}, ErrFailedPrecondition
		}
		a.Catalogue = session.BrokerCatalogueRef(snapshot.Catalogue.Ref())
		a.Withdrawn = false // Explicit Connect durably opens only the empty intent.
		candidate, err := brokerAuthorityCandidate(sess)
		if err != nil {
			return WorkspaceEnrollmentProjection{}, err
		}
		if err := candidate.RestoreBrokerAccess(a); err != nil {
			return WorkspaceEnrollmentProjection{}, err
		}
		if err := s.saveSession(ctx, candidate); err != nil {
			s.withdrawBrokerEngine(id)
			return WorkspaceEnrollmentProjection{}, errors.Join(errBrokerAuthoritySave, err)
		}
		if err := sess.RestoreBrokerAccess(a); err != nil {
			return WorkspaceEnrollmentProjection{}, err
		}
	}
	if exists {
		flow, err := s.cfg.SessionBroker.ObserveEnrollment(ctx, ref, c.EnrollmentRef(pending.ID))
		if err != nil {
			return WorkspaceEnrollmentProjection{}, err
		}
		switch flow.Kind {
		case c.FlowCompleted:
			return s.publishSessionBrokerEnrollment(ctx, sess, flow.Catalogue)
		case c.FlowPending:
			begin, err := s.cfg.SessionBroker.BeginEnrollment(ctx, ref)
			if err != nil {
				return WorkspaceEnrollmentProjection{}, err
			}
			if begin.Kind != c.EnrollmentStartedKind || begin.Started.Ref != c.EnrollmentRef(pending.ID) {
				return WorkspaceEnrollmentProjection{}, ErrFailedPrecondition
			}
			return WorkspaceEnrollmentProjection{Ref: enrollmentRef(pending), Status: c.WorkspaceEnrollmentPending, URL: begin.Started.Prompt.URL}, nil
		case c.FlowCancelled, c.FlowExpired, c.FlowFailed:
			status := c.WorkspaceEnrollmentFailed
			if flow.Kind == c.FlowCancelled {
				status = c.WorkspaceEnrollmentCancelled
			}
			if flow.Kind == c.FlowExpired {
				status = c.WorkspaceEnrollmentExpired
			}
			return s.settleTerminalWorkspaceEnrollment(ctx, sess, status)
		default:
			return WorkspaceEnrollmentProjection{}, ErrFailedPrecondition
		}
	}
	begin, err := s.cfg.SessionBroker.BeginEnrollment(ctx, ref)
	if err != nil {
		return WorkspaceEnrollmentProjection{}, err
	}
	switch begin.Kind {
	case c.EnrollmentCompletedKind:
		return s.publishSessionBrokerEnrollment(ctx, sess, begin.Catalogue)
	case c.EnrollmentAlreadyConnected:
		snapshot, err := s.cfg.SessionBroker.OpenSession(ctx, &ref)
		if err != nil {
			return WorkspaceEnrollmentProjection{}, err
		}
		return s.publishSessionBrokerEnrollment(ctx, sess, snapshot.Catalogue)
	case c.EnrollmentStartedKind:
		pending := session.PendingWorkspaceEnrollment{ID: session.WorkspaceEnrollmentID(begin.Started.Ref), RequiredServices: 1, ExpiresAt: begin.Started.Prompt.ExpiresAt}
		if err := sess.BeginWorkspaceEnrollment(pending); err != nil {
			return WorkspaceEnrollmentProjection{}, err
		}
		if err := s.saveSession(ctx, sess); err != nil {
			return WorkspaceEnrollmentProjection{}, err
		}
		return WorkspaceEnrollmentProjection{Ref: enrollmentRef(pending), Status: c.WorkspaceEnrollmentPending, URL: begin.Started.Prompt.URL}, nil
	default:
		return WorkspaceEnrollmentProjection{}, ErrFailedPrecondition
	}
}

func (s *Service) publishSessionBrokerEnrollment(ctx context.Context, sess *session.Session, cat c.Catalogue) (WorkspaceEnrollmentProjection, error) {
	a, ok := sess.BrokerAccess()
	if !ok || cat == nil || !cat.Valid() || cat.Connection() == "" {
		return WorkspaceEnrollmentProjection{}, ErrFailedPrecondition
	}
	candidate, err := brokerAuthorityCandidate(sess)
	if err != nil {
		return WorkspaceEnrollmentProjection{}, err
	}
	var ref c.WorkspaceEnrollmentRef
	pending, hasPending := candidate.PendingWorkspaceEnrollment()
	if hasPending {
		ref = enrollmentRef(pending)
	} else {
		// Synchronous enrollment has no browser flow reference; the exact returned
		// catalogue identifies its completion within this unpublished candidate.
		pending = session.PendingWorkspaceEnrollment{ID: session.WorkspaceEnrollmentID(cat.Ref()), RequiredServices: 1, ExpiresAt: a.ExpiresAt}
		if err := candidate.BeginWorkspaceEnrollment(pending); err != nil {
			return WorkspaceEnrollmentProjection{}, err
		}
	}
	if err := candidate.CompleteWorkspaceEnrollmentWithBrokerCatalogue(pending, a.Session, session.BrokerCatalogueRef(cat.Ref()), session.BrokerConnectionRef(cat.Connection()), a.ExpiresAt, cat.ToolNames()); err != nil {
		return WorkspaceEnrollmentProjection{}, err
	}
	tools := effectiveSessionBrokerTools(candidate, c.SessionRef(a.Session), cat, cat.Tools())
	sel := ProviderSelector{ProviderID: sess.ProviderID, ModelID: sess.ModelID, ReasoningEffort: sess.ReasoningEffort}
	_, err = s.buildPersistAndRegisterSessionEngine(ctx, candidate, sel, profileForSession(candidate), candidate.Mode, true, tools, true, func() error {
		if err := s.saveSession(ctx, candidate); err != nil {
			s.withdrawBrokerEngine(sess.ID)
			return errors.Join(errBrokerAuthoritySave, err)
		}
		return nil
	})
	if err != nil {
		return WorkspaceEnrollmentProjection{}, err
	}
	return WorkspaceEnrollmentProjection{Ref: ref, Status: c.WorkspaceEnrollmentConnected}, nil
}

func (s *Service) cancelSessionBrokerEnrollmentLocked(ctx context.Context, id session.SessionID, enrollmentID session.WorkspaceEnrollmentID) (WorkspaceEnrollmentProjection, error) {
	sess, a, err := s.sessionBrokerEnrollmentTarget(ctx, id)
	if err != nil {
		return WorkspaceEnrollmentProjection{}, err
	}
	pending, ok := sess.PendingWorkspaceEnrollment()
	if !ok || pending.ID != enrollmentID {
		return WorkspaceEnrollmentProjection{}, ErrFailedPrecondition
	}
	if _, err := s.cfg.SessionBroker.CancelEnrollment(ctx, c.SessionRef(a.Session), c.EnrollmentRef(enrollmentID)); err != nil {
		return WorkspaceEnrollmentProjection{}, err
	}
	return s.settleTerminalWorkspaceEnrollment(ctx, sess, c.WorkspaceEnrollmentCancelled)
}

// DisconnectWorkspaceServices withdraws local authority durably before the exact
// remote withdrawal. It never publishes a replacement on a cleanup failure.
func (s *Service) DisconnectWorkspaceServices(ctx context.Context, id session.SessionID) error {
	if s.cfg.SessionBroker == nil {
		return ErrFailedPrecondition
	}
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	if err := s.acquireLease(ctx, id); err != nil {
		return err
	}
	sess, a, err := s.sessionBrokerEnrollmentTarget(ctx, id)
	if err != nil {
		return err
	}
	candidate, err := brokerAuthorityCandidate(sess)
	if err != nil {
		return err
	}
	if err := candidate.WithdrawBrokerAccess(); err != nil {
		return err
	}
	if err := s.saveSession(ctx, candidate); err != nil {
		s.withdrawBrokerEngine(id)
		return errors.Join(errBrokerAuthoritySave, err)
	}
	s.withdrawBrokerEngine(id)
	if pending, ok := candidate.PendingWorkspaceEnrollment(); ok {
		if _, err := s.cfg.SessionBroker.CancelEnrollment(ctx, c.SessionRef(a.Session), c.EnrollmentRef(pending.ID)); err != nil {
			return err
		}
		if err := candidate.AbortWorkspaceEnrollment(pending.ID); err != nil {
			return err
		}
		if err := s.saveSession(ctx, candidate); err != nil {
			return errors.Join(errBrokerAuthoritySave, err)
		}
	}
	if a.Connection == "" {
		return nil
	}
	result, err := s.cfg.SessionBroker.DisconnectTools(ctx, c.SessionRef(a.Session), c.ConnectionRef(a.Connection))
	if err != nil {
		return err
	}
	if result == c.ConnectionChanged {
		return ErrFailedPrecondition
	}
	return nil
}

// sessionAuthorizationControl reuses the donor host's claim/continuation
// machinery, but carries no native custody, handles or process identity.
type sessionAuthorizationControl struct {
	host    *Service
	ref     c.SessionRef
	sess    *session.Session
	attempt session.BrokerAttempt
}

func (a *sessionAuthorizationControl) valid(auth session.ExternalAuthorization) bool {
	return auth.Binding == session.AuthorizationBinding(a.ref) && a.attempt.Valid()
}
func (a *sessionAuthorizationControl) PresentAuthorization(ctx context.Context, auth session.ExternalAuthorization) (string, error) {
	if !a.valid(auth) {
		return "", ErrFailedPrecondition
	}
	p, err := a.host.cfg.SessionBroker.BeginAuthorization(ctx, a.ref, c.AuthorizationRef(auth.ID))
	return p.URL, err
}
func (a *sessionAuthorizationControl) AuthorizationStatus(ctx context.Context, auth session.ExternalAuthorization) (session.AuthorizationStatus, error) {
	if !a.valid(auth) {
		return "", ErrFailedPrecondition
	}
	f, err := a.host.cfg.SessionBroker.ObserveAuthorization(ctx, a.ref, c.AuthorizationRef(auth.ID))
	if err != nil {
		return "", err
	}
	switch f.Kind {
	case c.FlowPending:
		return session.AuthorizationPending, nil
	case c.FlowCompleted:
		return session.AuthorizationGranted, nil
	case c.FlowCancelled:
		return session.AuthorizationCancelled, nil
	case c.FlowExpired:
		return session.AuthorizationExpired, nil
	case c.FlowFailed:
		return session.AuthorizationInterrupted, nil
	default:
		return "", ErrFailedPrecondition
	}
}
func (a *sessionAuthorizationControl) CancelAuthorization(ctx context.Context, auth session.ExternalAuthorization) (c.CancelOutcome, error) {
	if !a.valid(auth) {
		return "", ErrFailedPrecondition
	}
	result, err := a.host.cfg.SessionBroker.CancelAuthorization(ctx, a.ref, c.AuthorizationRef(auth.ID), a.attempt)
	if err != nil {
		return "", err
	}
	if result == c.Cancelled {
		return c.CancelCancelled, nil
	}
	return c.CancelAlreadyResolved, nil
}
func (s *Service) sessionBrokerResumeTools(ctx context.Context, sess *session.Session, claimed session.PendingAuthorization) ([]tool.Tool, error) {
	a, ok := sess.BrokerAccess()
	if !ok || a.Withdrawn || a.Current != nil || claimed.Authorization.Binding != session.AuthorizationBinding(a.Session) {
		return nil, ErrFailedPrecondition
	}
	ref := c.SessionRef(a.Session)
	flow, err := s.cfg.SessionBroker.ObserveAuthorization(ctx, ref, c.AuthorizationRef(claimed.Authorization.ID))
	if err != nil {
		return nil, err
	}
	if flow.Kind != c.FlowCompleted {
		return nil, ErrFailedPrecondition
	}
	if err := s.adoptSessionBrokerCatalogue(ctx, sess, flow.Catalogue); err != nil {
		return nil, err
	}
	attempt, err := sess.PrepareBrokerInvocation(a.Session, flow.Catalogue.Ref(), claimed.Call, s.cfg.Now())
	if err != nil {
		return nil, err
	}
	resume, err := s.cfg.SessionBroker.ResumeToolWrapper(ref, flow.Catalogue, claimed.Call.Name, claimed.Call.ID, c.AuthorizationRef(claimed.Authorization.ID), attempt)
	if err != nil {
		return nil, err
	}
	tools := flow.Catalogue.Tools()
	for i, t := range tools {
		if t.Spec().Name == claimed.Call.Name {
			tools[i] = resume
		}
	}
	return effectiveSessionBrokerTools(sess, ref, flow.Catalogue, tools), nil
}

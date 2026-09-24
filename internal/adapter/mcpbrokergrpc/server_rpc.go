package mcpbrokergrpc

import (
	"context"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

// Attach binds an authenticated workload to a logical broker session.
func (s *Server) Attach(ctx context.Context, req *brokerv1.AttachRequest) (*brokerv1.AttachResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.RPCDeadline)
	defer cancel()
	if req.GetSessionId() == "" {
		return nil, invalid("session_id is required")
	}
	if req.GetExpectedBinding() != "" && req.GetBrokerIncarnation() != "" {
		return nil, invalid("expected_binding and broker_incarnation are mutually exclusive")
	}
	if req.GetExpectedBinding() == "" {
		if err := s.checkInstanceID(req.GetBrokerIncarnation(), true); err != nil {
			return nil, err
		}
	}
	logicalID := session.SessionID(req.GetSessionId())
	if !mcpbroker.ValidLogicalSessionID(logicalID) {
		return nil, invalid("session_id is invalid")
	}
	var principal *session.Principal
	var owner *sessionOwner
	var err error
	if req.GetExpectedBinding() != "" {
		principal, owner, err = s.bindExistingSession(ctx, logicalID)
	} else {
		principal, owner, err = s.bindSession(ctx, logicalID)
	}
	if err != nil {
		return nil, err
	}
	attached := false
	defer func() { s.finishSessionBind(logicalID, owner, attached) }()
	var a mcpbroker.SessionHandle
	var outcome mcpbroker.AttachOutcome
	if expected := session.ExternalBinding(req.GetExpectedBinding()); expected != "" {
		attacher, ok := s.service.(mcpbroker.ExpectedBindingAttacher)
		if !ok {
			return nil, continuityUnavailable()
		}
		a, outcome, err = attacher.AttachSessionExpectedBinding(ctx, logicalID, expected)
	} else {
		a, outcome, err = s.service.AttachSession(ctx, logicalID)
	}
	if err != nil {
		return nil, brokerStatus(err)
	}
	if req.GetExpectedBinding() != "" && outcome != mcpbroker.AttachReattached && outcome != mcpbroker.AttachRecoveredProvisional {
		s.discardUnpublishedHandle(a, outcome)
		return nil, invalid("invalid expected-binding attach outcome")
	}
	if req.GetExpectedBinding() == "" && outcome == mcpbroker.AttachRecoveredProvisional {
		s.discardUnpublishedHandle(a, outcome)
		return nil, invalid("invalid attach outcome")
	}
	desc, tools, err := descriptors(a.Tools())
	if err != nil {
		s.discardUnpublishedHandle(a, outcome)
		return nil, invalid(err.Error())
	}
	h, err := newHandle()
	if err != nil {
		s.discardUnpublishedHandle(a, outcome)
		return nil, status.Error(codes.Internal, "mint attachment handle")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		s.discardUnpublishedHandle(a, outcome)
		return nil, reasonStatus(codes.Unavailable, "broker state unavailable", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE, "")
	}
	if len(s.handles) >= s.cfg.MaxHandles {
		s.discardUnpublishedHandle(a, outcome)
		return nil, reasonStatus(codes.ResourceExhausted, "attachment handle capacity reached", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED, "")
	}
	if owner != nil && (s.owners[logicalID] != owner || owner.retiring) {
		s.discardUnpublishedHandle(a, outcome)
		return nil, reasonStatus(codes.Unavailable, "broker session is being retired", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE, "")
	}
	if owner != nil && outcome != mcpbroker.AttachRecoveredProvisional {
		owner.published = true
	}
	_, enrollment := a.(mcpbroker.WorkspaceEnrollmentHandle)
	advertiser, advertises := a.(mcpbroker.CredentialContinuityAdvertiser)
	continuity := advertises && advertiser.CredentialContinuity()
	now := time.Now()
	s.handles[h] = &serverHandle{sessionHandle: a, principal: principal, logicalID: logicalID, owner: owner, binding: string(a.Binding()), tools: tools, expiresAt: now.Add(s.cfg.HandleIdleTimeout), changed: make(chan struct{}), receipts: make(map[session.ToolCallID]*executeReceipt)}
	attached = true
	return &brokerv1.AttachResponse{Binding: string(a.Binding()), Handle: h, Outcome: string(outcome), Tools: desc, BrokerIncarnation: s.instanceID, WorkspaceEnrollment: enrollment, CredentialContinuity: continuity}, nil
}

func (s *Server) discardUnpublishedHandle(handle mcpbroker.SessionHandle, outcome mcpbroker.AttachOutcome) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.CleanupTimeout)
	defer cancel()
	if outcome == mcpbroker.AttachCreated || outcome == mcpbroker.AttachRecoveredProvisional {
		_ = handle.Abort(ctx)
		return
	}
	_, _ = handle.Close(ctx)
}

// Commit commits the provisional state behind one exact handle.
func (s *Server) Commit(ctx context.Context, req *brokerv1.CommitRequest) (*brokerv1.CommitResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.RPCDeadline)
	defer cancel()
	a, release, e := s.get(ctx, req.GetBrokerIncarnation(), req.GetHandle())
	if e != nil {
		return nil, e
	}
	defer release()
	if e = a.sessionHandle.Commit(ctx); e != nil {
		return nil, brokerStatus(e)
	}
	return &brokerv1.CommitResponse{}, nil
}

// Abort aborts and releases one exact handle.
func (s *Server) Abort(ctx context.Context, req *brokerv1.AbortRequest) (*brokerv1.AbortResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.RPCDeadline)
	defer cancel()
	a, _, receipt, e := s.beginLifecycle(ctx, req.GetBrokerIncarnation(), req.GetHandle(), lifecycleAbort)
	if e != nil {
		return nil, e
	}
	if receipt {
		return &brokerv1.AbortResponse{}, nil
	}
	e = a.sessionHandle.Abort(ctx)
	s.finishLifecycle(a, lifecycleAbort, "", e == nil)
	if e != nil {
		return nil, brokerStatus(e)
	}
	return &brokerv1.AbortResponse{}, nil
}

// Close releases one exact handle without deleting logical state.
func (s *Server) Close(ctx context.Context, req *brokerv1.CloseRequest) (*brokerv1.CloseResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.RPCDeadline)
	defer cancel()
	a, out, receipt, e := s.beginLifecycle(ctx, req.GetBrokerIncarnation(), req.GetHandle(), lifecycleClose)
	if e != nil {
		return nil, e
	}
	if receipt {
		return &brokerv1.CloseResponse{Outcome: string(out)}, nil
	}
	out, e = a.sessionHandle.Close(ctx)
	terminal := out == mcpbroker.CloseClosed || out == mcpbroker.CloseAlreadyClosed
	s.finishLifecycle(a, lifecycleClose, out, terminal)
	if e != nil {
		return nil, brokerStatus(e)
	}
	return &brokerv1.CloseResponse{Outcome: string(out)}, nil
}

// Delete deletes only the exact logical state in the addressed broker instance.
func (s *Server) Delete(ctx context.Context, req *brokerv1.DeleteRequest) (*brokerv1.DeleteResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.RPCDeadline)
	defer cancel()
	if req.GetSessionId() == "" {
		return nil, invalid("session_id is required")
	}
	if !mcpbroker.ValidLogicalSessionID(session.SessionID(req.GetSessionId())) {
		return nil, invalid("session_id is invalid")
	}
	if req.GetBinding() == "" {
		return nil, invalid("binding is required")
	}
	if err := s.checkInstanceID(req.GetBrokerIncarnation(), true); err != nil {
		return nil, err
	}
	owner, err := s.beginSessionDelete(ctx, session.SessionID(req.GetSessionId()))
	if err != nil {
		return nil, err
	}
	d, ok := s.service.(mcpbroker.BindingSessionDeleter)
	if !ok {
		s.finishSessionDelete(session.SessionID(req.GetSessionId()), owner, false)
		return nil, status.Error(codes.FailedPrecondition, "binding delete is unsupported")
	}
	out, err := d.DeleteSessionIfBinding(ctx, session.SessionID(req.GetSessionId()), session.ExternalBinding(req.GetBinding()))
	if err != nil {
		s.finishSessionDelete(session.SessionID(req.GetSessionId()), owner, false)
		return nil, brokerStatus(err)
	}
	s.finishSessionDelete(session.SessionID(req.GetSessionId()), owner, out == mcpbroker.DeleteDeleted)
	return &brokerv1.DeleteResponse{Outcome: string(out)}, nil
}

// RequestAuthorization begins authorization for one exact invocation.
func (s *Server) RequestAuthorization(ctx context.Context, req *brokerv1.RequestAuthorizationRequest) (*brokerv1.RequestAuthorizationResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.RPCDeadline)
	defer cancel()
	a, release, e := s.get(ctx, req.GetBrokerIncarnation(), req.GetHandle())
	if e != nil {
		return nil, e
	}
	defer release()
	call, e := callFrom(req.GetName(), req.GetCallId(), req.GetArgs(), req.GetItemId())
	if e != nil {
		return nil, e
	}
	s.mu.Lock()
	target, ok := a.tools[call.Name].(tool.AuthorizationRequester)
	s.mu.Unlock()
	if !ok {
		return nil, invalid("tool is not authorization-capable")
	}
	digest := invocationDigest(call)
	s.mu.Lock()
	receipt := a.receipts[call.ID]
	if receipt != nil && receipt.digest != digest {
		s.mu.Unlock()
		return nil, invalid("call_id was reused with different invocation content")
	}
	if receipt == nil {
		if len(a.receipts) >= s.cfg.MaxReceipts || a.receiptBytes+receiptReservationBytes(call) > s.cfg.MaxReceiptBytes {
			s.mu.Unlock()
			return nil, reasonStatus(codes.ResourceExhausted, "broker receipt capacity reached", brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED, "")
		}
		receipt := &executeReceipt{digest: digest, bytes: receiptReservationBytes(call), done: make(chan struct{})}
		a.receipts[call.ID] = receipt
		a.receiptBytes += receipt.bytes
	}
	s.mu.Unlock()
	auth, required, e := target.RequestAuthorization(ctx, call)
	if e != nil {
		return nil, brokerStatus(e)
	}
	if !required {
		return &brokerv1.RequestAuthorizationResponse{}, nil
	}
	return &brokerv1.RequestAuthorizationResponse{Authorization: authToWire(auth), Required: true}, nil
}

// AbortAuthorization aborts one exact tool authorization.
func (s *Server) AbortAuthorization(ctx context.Context, req *brokerv1.AbortAuthorizationRequest) (*brokerv1.AbortAuthorizationResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.RPCDeadline)
	defer cancel()
	a, release, e := s.get(ctx, req.GetBrokerIncarnation(), req.GetHandle())
	if e != nil {
		return nil, e
	}
	defer release()
	auth, e := authFromWire(req.GetAuthorization())
	if e != nil {
		return nil, e
	}
	s.mu.Lock()
	targets := make([]tool.AuthorizationRequester, 0, len(a.tools))
	for _, target := range a.tools {
		if requester, ok := target.(tool.AuthorizationRequester); ok {
			targets = append(targets, requester)
		}
	}
	s.mu.Unlock()
	for _, requester := range targets {
		if e = requester.AbortAuthorization(ctx, auth); e == nil {
			return &brokerv1.AbortAuthorizationResponse{}, nil
		}
	}
	if e == nil {
		return nil, invalid("tool is not authorization-capable")
	}
	return nil, brokerStatus(e)
}

// PresentAuthorization returns the ephemeral URL for one exact authorization.
func (s *Server) PresentAuthorization(ctx context.Context, req *brokerv1.PresentAuthorizationRequest) (*brokerv1.PresentAuthorizationResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.RPCDeadline)
	defer cancel()
	a, release, err := s.get(ctx, req.GetBrokerIncarnation(), req.GetHandle())
	if err != nil {
		return nil, err
	}
	defer release()
	auth, err := authFromWire(req.GetAuthorization())
	if err != nil {
		return nil, err
	}
	url, err := a.sessionHandle.PresentAuthorization(ctx, auth)
	if err != nil {
		return nil, brokerStatus(err)
	}
	if url == "" || !utf8.ValidString(url) {
		return nil, invalid("invalid presentation")
	}
	return &brokerv1.PresentAuthorizationResponse{Url: url}, nil
}

// AuthorizationStatus observes one exact authorization.
func (s *Server) AuthorizationStatus(ctx context.Context, req *brokerv1.AuthorizationStatusRequest) (*brokerv1.AuthorizationStatusResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.RPCDeadline)
	defer cancel()
	a, release, err := s.get(ctx, req.GetBrokerIncarnation(), req.GetHandle())
	if err != nil {
		return nil, err
	}
	defer release()
	auth, err := authFromWire(req.GetAuthorization())
	if err != nil {
		return nil, err
	}
	out, err := a.sessionHandle.AuthorizationStatus(ctx, auth)
	if err != nil {
		return nil, brokerStatus(err)
	}
	if !validAuthorizationStatus(out) {
		return nil, status.Error(codes.Internal, "invalid authorization status")
	}
	return &brokerv1.AuthorizationStatusResponse{Status: string(out)}, nil
}

// CancelAuthorization cancels one exact authorization.
func (s *Server) CancelAuthorization(ctx context.Context, req *brokerv1.CancelAuthorizationRequest) (*brokerv1.CancelAuthorizationResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.RPCDeadline)
	defer cancel()
	a, release, err := s.get(ctx, req.GetBrokerIncarnation(), req.GetHandle())
	if err != nil {
		return nil, err
	}
	defer release()
	auth, err := authFromWire(req.GetAuthorization())
	if err != nil {
		return nil, err
	}
	out, err := a.sessionHandle.CancelAuthorization(ctx, auth)
	if err != nil {
		return nil, brokerStatus(err)
	}
	if !validCancelOutcome(out) {
		return nil, status.Error(codes.Internal, "invalid cancel outcome")
	}
	return &brokerv1.CancelAuthorizationResponse{Outcome: string(out)}, nil
}

// BeginWorkspaceEnrollment begins a pre-prompt enrollment.
func (s *Server) BeginWorkspaceEnrollment(ctx context.Context, req *brokerv1.BeginWorkspaceEnrollmentRequest) (*brokerv1.BeginWorkspaceEnrollmentResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.RPCDeadline)
	defer cancel()
	a, release, err := s.get(ctx, req.GetBrokerIncarnation(), req.GetHandle())
	if err != nil {
		return nil, err
	}
	defer release()
	enroller, ok := a.sessionHandle.(mcpbroker.WorkspaceEnrollmentHandle)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "workspace enrollment unsupported")
	}
	presentation, err := enroller.BeginWorkspaceEnrollment(ctx)
	if err != nil {
		return nil, brokerStatus(err)
	}
	if !presentation.Valid() {
		return nil, status.Error(codes.Internal, "invalid workspace presentation")
	}
	return &brokerv1.BeginWorkspaceEnrollmentResponse{Ref: workspaceRefToWire(presentation.Ref), Url: presentation.URL}, nil
}

// ObserveWorkspaceEnrollment observes one exact pre-prompt enrollment.
func (s *Server) ObserveWorkspaceEnrollment(ctx context.Context, req *brokerv1.ObserveWorkspaceEnrollmentRequest) (*brokerv1.ObserveWorkspaceEnrollmentResponse, error) {
	result, err := s.workspaceResult(ctx, req.GetBrokerIncarnation(), req.GetHandle(), req.GetRef(), false)
	if err != nil {
		return nil, err
	}
	return &brokerv1.ObserveWorkspaceEnrollmentResponse{Ref: result.ref, Status: result.status, Tools: result.tools}, nil
}

// CancelWorkspaceEnrollment cancels one exact pre-prompt enrollment.
func (s *Server) CancelWorkspaceEnrollment(ctx context.Context, req *brokerv1.CancelWorkspaceEnrollmentRequest) (*brokerv1.CancelWorkspaceEnrollmentResponse, error) {
	result, err := s.workspaceResult(ctx, req.GetBrokerIncarnation(), req.GetHandle(), req.GetRef(), true)
	if err != nil {
		return nil, err
	}
	return &brokerv1.CancelWorkspaceEnrollmentResponse{Ref: result.ref, Status: result.status, Tools: result.tools}, nil
}

type workspaceResultWire struct {
	ref    *brokerv1.WorkspaceRef
	status string
	tools  []*brokerv1.ToolDescriptor
}

func (s *Server) workspaceResult(ctx context.Context, instanceID, handle string, wireRef *brokerv1.WorkspaceRef, cancel bool) (workspaceResultWire, error) {
	ctx, stop := context.WithTimeout(ctx, s.cfg.RPCDeadline)
	defer stop()
	a, release, err := s.get(ctx, instanceID, handle)
	if err != nil {
		return workspaceResultWire{}, err
	}
	defer release()
	enroller, ok := a.sessionHandle.(mcpbroker.WorkspaceEnrollmentHandle)
	if !ok {
		return workspaceResultWire{}, status.Error(codes.FailedPrecondition, "workspace enrollment unsupported")
	}
	ref, err := workspaceRefFromWire(wireRef)
	if err != nil {
		return workspaceResultWire{}, err
	}
	var result mcpbroker.WorkspaceEnrollmentResult
	if cancel {
		result, err = enroller.CancelWorkspaceEnrollment(ctx, ref)
	} else {
		result, err = enroller.ObserveWorkspaceEnrollment(ctx, ref)
	}
	if err != nil {
		return workspaceResultWire{}, brokerStatus(err)
	}
	if !result.Valid() {
		return workspaceResultWire{}, status.Error(codes.Internal, "invalid workspace result")
	}
	var desc []*brokerv1.ToolDescriptor
	if result.Catalogue != nil {
		var tools map[string]tool.Tool
		desc, tools, err = descriptors(result.Catalogue.Tools())
		if err != nil {
			return workspaceResultWire{}, status.Error(codes.Internal, "invalid workspace catalogue")
		}
		// A connected catalogue is the complete frozen set for this enrollment.
		// Publish its descriptors and executable tools together for this handle.
		s.mu.Lock()
		a.tools = tools
		s.mu.Unlock()
	}
	return workspaceResultWire{ref: workspaceRefToWire(result.Ref), status: string(result.Status), tools: desc}, nil
}

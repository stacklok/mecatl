package mcpbrokergrpc

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

// Attach binds an authenticated workload to a logical broker session.
//
//nolint:gocyclo // one ordered admission transaction over binding/rebind/reattach outcomes.
func (s *Server) Attach(ctx context.Context, req *brokerv1.AttachRequest) (*brokerv1.AttachResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.RPCDeadline)
	defer cancel()
	if req.GetSessionId() == "" {
		return nil, invalid("session_id is required")
	}
	if err := s.checkInstanceID(req.GetBrokerIncarnation(), true); err != nil {
		return nil, err
	}
	logicalID := session.SessionID(req.GetSessionId())
	if !mcpbroker.ValidLogicalSessionID(logicalID) {
		return nil, invalid("session_id is invalid")
	}
	var principal *session.Principal
	var owner *sessionOwner
	var err error
	principal, owner, err = s.bindSession(ctx, logicalID)
	if err != nil {
		return nil, err
	}
	attached := false
	defer func() { s.finishSessionBind(logicalID, owner, attached) }()
	var a mcpbroker.Attachment
	var outcome mcpbroker.AttachOutcome
	a, outcome, err = s.service.AttachSession(ctx, logicalID)
	if err != nil {
		return nil, brokerStatus(err)
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
	if owner != nil {
		owner.published = true
	}
	now := time.Now()
	s.handles[h] = &serverHandle{sessionHandle: a, principal: principal, logicalID: logicalID, owner: owner, binding: string(a.Binding()), expiresAt: now.Add(s.cfg.HandleIdleTimeout), changed: make(chan struct{})}
	attached = true
	return &brokerv1.AttachResponse{Binding: string(a.Binding()), Handle: h, Outcome: string(outcome), BrokerIncarnation: s.instanceID}, nil
}

func (s *Server) discardUnpublishedHandle(handle mcpbroker.Attachment, outcome mcpbroker.AttachOutcome) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.CleanupTimeout)
	defer cancel()
	if outcome == mcpbroker.AttachCreated {
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

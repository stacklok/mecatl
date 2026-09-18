package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type recheckAuthorizationStream struct {
	grpc.BidiStreamingServer[mecatlv1.RecheckMcpAuthorizationRequest, mecatlv1.RecheckMcpAuthorizationResponse]
	ctx          context.Context
	requests     []*mecatlv1.RecheckMcpAuthorizationRequest
	requestCh    chan *mecatlv1.RecheckMcpAuthorizationRequest
	approveOnAsk bool
	afterInitial func()
	sendErr      error
	failAt       int
	sendCalls    int
	recvErr      error
	recvErred    chan struct{}
	eofAfterAsk  chan struct{}
	responses    []*mecatlv1.RecheckMcpAuthorizationResponse
}

func (s *recheckAuthorizationStream) Context() context.Context { return s.ctx }
func (s *recheckAuthorizationStream) Recv() (*mecatlv1.RecheckMcpAuthorizationRequest, error) {
	if len(s.requests) != 0 {
		req := s.requests[0]
		s.requests = s.requests[1:]
		if s.afterInitial != nil {
			s.afterInitial()
			s.afterInitial = nil
		}
		return req, nil
	}
	if s.recvErr != nil {
		if s.recvErred != nil {
			close(s.recvErred)
			s.recvErred = nil
		}
		return nil, s.recvErr
	}
	if s.eofAfterAsk != nil {
		<-s.eofAfterAsk
		return nil, io.EOF
	}
	if s.requestCh != nil {
		select {
		case req := <-s.requestCh:
			return req, nil
		case <-s.ctx.Done():
			return nil, s.ctx.Err()
		}
	}
	return nil, io.EOF
}
func (s *recheckAuthorizationStream) Send(response *mecatlv1.RecheckMcpAuthorizationResponse) error {
	s.responses = append(s.responses, response)
	s.sendCalls++
	if s.sendErr != nil && (s.failAt == 0 || s.sendCalls == s.failAt) {
		return s.sendErr
	}
	if s.approveOnAsk && response.GetEvent().GetType() == "permission.ask" {
		s.requestCh <- &mecatlv1.RecheckMcpAuthorizationRequest{Control: &mecatlv1.RecheckMcpAuthorizationRequest_ResumeApproval{ResumeApproval: &mecatlv1.ResumeApproval{AskId: response.GetEvent().GetAsk().GetAskId(), Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ONCE}}}
	}
	if s.eofAfterAsk != nil && response.GetEvent().GetType() == "permission.ask" {
		close(s.eofAfterAsk)
	}
	return nil
}

type cancelAuthorizationStream struct {
	grpc.BidiStreamingServer[mecatlv1.CancelMcpAuthorizationRequest, mecatlv1.CancelMcpAuthorizationResponse]
	ctx          context.Context
	requests     []*mecatlv1.CancelMcpAuthorizationRequest
	afterInitial func()
	sendErr      error
	responses    []*mecatlv1.CancelMcpAuthorizationResponse
}

type rejectCancelledSaveStore struct{ port.SessionStore }

func (s rejectCancelledSaveStore) Save(ctx context.Context, sess *session.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.SessionStore.Save(ctx, sess)
}

func (s *cancelAuthorizationStream) Context() context.Context { return s.ctx }
func (s *cancelAuthorizationStream) Recv() (*mecatlv1.CancelMcpAuthorizationRequest, error) {
	if len(s.requests) == 0 {
		return nil, io.EOF
	}
	req := s.requests[0]
	s.requests = s.requests[1:]
	if s.afterInitial != nil {
		s.afterInitial()
		s.afterInitial = nil
	}
	return req, nil
}
func (s *cancelAuthorizationStream) Send(response *mecatlv1.CancelMcpAuthorizationResponse) error {
	s.responses = append(s.responses, response)
	return s.sendErr
}

func ownedAuthorizationFixture(t *testing.T, authorizationStatus session.AuthorizationStatus) (lifecycleFixture, context.Context, context.Context) {
	t.Helper()
	f := newLifecycleFixture(t, authorizationStatus, nil, time.Now, nil)
	alice := &session.Principal{Issuer: "https://issuer.example", Subject: "alice", GrantType: session.GrantTypeUser}
	bob := &session.Principal{Issuer: alice.Issuer, Subject: "bob", GrantType: session.GrantTypeUser}
	sess, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	sess.Owner = alice
	if err := f.store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	f.svc.cfg.OwnershipEnforced = true
	return f, session.WithPrincipal(t.Context(), alice), session.WithPrincipal(t.Context(), bob)
}

func TestMCPAuthorizationGRPCUsesAuthenticatedOwnerAndAuthoritativeStatus(t *testing.T) {
	f, ownerCtx, foreignCtx := ownedAuthorizationFixture(t, session.AuthorizationPending)
	h := NewHarnessServer(f.svc)
	req := &mecatlv1.GetMcpAuthorizationPresentationRequest{SessionId: "authorization-session", AuthorizationId: f.pending.Authorization.ID}
	response, err := h.GetMcpAuthorizationPresentation(ownerCtx, req)
	if err != nil || response.GetUrl() != f.attach.url {
		t.Fatalf("owner presentation = %q, %v", response.GetUrl(), err)
	}
	if _, err := h.GetMcpAuthorizationPresentation(foreignCtx, req); status.Code(err) != codes.NotFound {
		t.Fatalf("foreign presentation code = %v, want NotFound", status.Code(err))
	}

	stream := &recheckAuthorizationStream{ctx: ownerCtx, requests: []*mecatlv1.RecheckMcpAuthorizationRequest{{SessionId: req.SessionId, AuthorizationId: req.AuthorizationId}}}
	if err := h.RecheckMcpAuthorization(stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.responses) != 1 || stream.responses[0].GetEvent().GetAuthorization().GetStatus() != "pending" {
		t.Fatalf("recheck responses = %+v", stream.responses)
	}
}

func TestMCPAuthorizationGRPCContinuationPermissionApproval(t *testing.T) {
	followup := session.NewToolCall("followup-call", "protected", nil)
	f := newLifecycleFixtureWithTurns(t, session.AuthorizationGranted, nil, time.Now, nil,
		mockllm.ToolCallTurn(followup), mockllm.TextTurn("continued after approval"))
	log := memstore.NewEventLog()
	f.svc.cfg.EventLog = log
	owner := &session.Principal{Issuer: "https://issuer.example", Subject: "alice", GrantType: session.GrantTypeUser}
	sess, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	sess.Owner = owner
	if err := f.store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	f.svc.cfg.OwnershipEnforced = true

	ctx, cancel := context.WithCancel(session.WithPrincipal(t.Context(), owner))
	defer cancel()
	requests := make(chan *mecatlv1.RecheckMcpAuthorizationRequest, 1)
	stream := &recheckAuthorizationStream{
		ctx: ctx, requestCh: requests, approveOnAsk: true,
		requests: []*mecatlv1.RecheckMcpAuthorizationRequest{{SessionId: "authorization-session", AuthorizationId: f.pending.Authorization.ID}},
	}
	if err := NewHarnessServer(f.svc).RecheckMcpAuthorization(stream); err != nil {
		t.Fatal(err)
	}
	var asked, toolResult, terminal bool
	for _, response := range stream.responses {
		ev := response.GetEvent()
		switch {
		case ev.GetType() == "permission.ask" && ev.GetAsk().GetAskId() != "":
			asked = true
		case ev.GetType() == "tool.result" && ev.GetToolResult().GetCallId() == "followup-call":
			toolResult = true
		case ev.GetType() == "result":
			terminal = true
		}
	}
	if !asked || !toolResult || !terminal {
		t.Fatalf("continuation events missing ask/approved result/terminal: asked=%t toolResult=%t terminal=%t", asked, toolResult, terminal)
	}
	assertAuthorizationStatusLoggedOnce(t, log, "authorization-session", session.AuthorizationGranted)
}

func TestMCPAuthorizationGRPCInitialStatusSendFailureDrainsAndFinishesContinuation(t *testing.T) {
	f := newLifecycleFixtureWithTurns(t, session.AuthorizationGranted, nil, time.Now, nil, mockllm.TextTurn("continued"))
	log := memstore.NewEventLog()
	f.svc.cfg.EventLog = log
	sentinel := errors.New("initial authorization status send failed")
	stream := &recheckAuthorizationStream{
		ctx:     t.Context(),
		sendErr: sentinel,
		requests: []*mecatlv1.RecheckMcpAuthorizationRequest{{
			SessionId: "authorization-session", AuthorizationId: f.pending.Authorization.ID,
		}},
	}

	err := NewHarnessServer(f.svc).RecheckMcpAuthorization(stream)
	if !errors.Is(err, sentinel) {
		t.Fatalf("RecheckMcpAuthorization error = %v, want sentinel", err)
	}
	if len(stream.responses) != 1 || stream.responses[0].GetEvent().GetAuthorization().GetStatus() != "granted" {
		t.Fatalf("initial response = %+v, want granted authorization status", stream.responses)
	}
	if _, live := f.svc.LookupRun("authorization-session"); live {
		t.Fatal("initial status send failure left continuation registered")
	}
	assertAuthorizationStatusLoggedOnce(t, log, "authorization-session", session.AuthorizationGranted)
}

func assertAuthorizationStatusLoggedOnce(t *testing.T, log port.EventLog, id session.SessionID, want session.AuthorizationStatus) {
	t.Helper()
	wantType := session.EvAuthorizationResolved
	if want == session.AuthorizationPending {
		wantType = session.EvAuthorizationRequired
	}
	var matches int
	for ev, err := range log.Read(t.Context(), id) {
		if err != nil {
			t.Fatalf("read event log: %v", err)
		}
		if ev.Type == wantType && ev.Authorization != nil && ev.Authorization.Status == want {
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("durable %q authorization statuses = %d, want 1", want, matches)
	}
}

func TestMCPAuthorizationGRPCCancelInitialStatusSendFailureDrainsAndFinishesContinuation(t *testing.T) {
	f, ownerCtx, _ := ownedAuthorizationFixture(t, session.AuthorizationPending)
	sentinel := errors.New("initial cancellation status send failed")
	stream := &cancelAuthorizationStream{
		ctx:     ownerCtx,
		sendErr: sentinel,
		requests: []*mecatlv1.CancelMcpAuthorizationRequest{{
			SessionId: "authorization-session", AuthorizationId: f.pending.Authorization.ID,
		}},
	}

	err := NewHarnessServer(f.svc).CancelMcpAuthorization(stream)
	if !errors.Is(err, sentinel) {
		t.Fatalf("CancelMcpAuthorization error = %v, want sentinel", err)
	}
	if len(stream.responses) != 1 || stream.responses[0].GetEvent().GetAuthorization().GetStatus() != "cancelled" {
		t.Fatalf("initial response = %+v, want cancelled authorization status", stream.responses)
	}
	if _, live := f.svc.LookupRun("authorization-session"); live {
		t.Fatal("initial status send failure left cancellation continuation registered")
	}
}

func TestMCPAuthorizationGRPCLaterEventSendFailureDrainsAndReturnsError(t *testing.T) {
	f := newLifecycleFixtureWithTurns(t, session.AuthorizationGranted, nil, time.Now, nil, mockllm.TextTurn("continued"))
	f.svc.cfg.Store = rejectCancelledSaveStore{SessionStore: f.store}
	sentinel := errors.New("continuation event send failed")
	stream := &recheckAuthorizationStream{
		ctx:     t.Context(),
		sendErr: sentinel,
		failAt:  2,
		requests: []*mecatlv1.RecheckMcpAuthorizationRequest{{
			SessionId: "authorization-session", AuthorizationId: f.pending.Authorization.ID,
		}},
	}

	if err := NewHarnessServer(f.svc).RecheckMcpAuthorization(stream); !errors.Is(err, sentinel) {
		t.Fatalf("RecheckMcpAuthorization error = %v, want sentinel", err)
	}
	if len(stream.responses) < 2 {
		t.Fatalf("sent responses = %d, want initial status and a continuation event", len(stream.responses))
	}
	persisted, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatalf("load continuation: %v", err)
	}
	if persisted.State != session.StateCompleted {
		t.Fatalf("persisted continuation state = %q, want %q", persisted.State, session.StateCompleted)
	}
}

func TestMCPAuthorizationGRPCStatusOnlySendFailureIsReturned(t *testing.T) {
	f, ownerCtx, _ := ownedAuthorizationFixture(t, session.AuthorizationPending)
	log := memstore.NewEventLog()
	f.svc.cfg.EventLog = log
	sentinel := errors.New("status-only authorization send failed")
	stream := &recheckAuthorizationStream{
		ctx:     ownerCtx,
		sendErr: sentinel,
		requests: []*mecatlv1.RecheckMcpAuthorizationRequest{{
			SessionId: "authorization-session", AuthorizationId: f.pending.Authorization.ID,
		}},
	}

	err := NewHarnessServer(f.svc).RecheckMcpAuthorization(stream)
	if !errors.Is(err, sentinel) {
		t.Fatalf("RecheckMcpAuthorization error = %v, want sentinel", err)
	}
	if _, live := f.svc.LookupRun("authorization-session"); live {
		t.Fatal("status-only control registered a continuation")
	}
	assertAuthorizationStatusLoggedOnce(t, log, "authorization-session", session.AuthorizationPending)
}

func TestMCPAuthorizationGRPCControlEOFDrainsContinuationWithoutCancellingIt(t *testing.T) {
	f := newLifecycleFixtureWithTurns(t, session.AuthorizationGranted, nil, time.Now, nil, mockllm.TextTurn("continued"))
	f.svc.cfg.Store = rejectCancelledSaveStore{SessionStore: f.store}
	ctx, cancel := context.WithCancel(t.Context())
	stream := &recheckAuthorizationStream{
		ctx:          ctx,
		afterInitial: cancel,
		requests:     []*mecatlv1.RecheckMcpAuthorizationRequest{{SessionId: "authorization-session", AuthorizationId: f.pending.Authorization.ID}},
	}
	if err := NewHarnessServer(f.svc).RecheckMcpAuthorization(stream); err != nil {
		t.Fatal(err)
	}
	if got := stream.responses[len(stream.responses)-1].GetEvent().GetResult().GetStop(); got != "end_turn" {
		t.Fatalf("terminal stop = %q, want end_turn", got)
	}
	persisted, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatalf("load EOF continuation: %v", err)
	}
	if persisted.State != session.StateCompleted {
		t.Fatalf("persisted EOF continuation state = %q, want %q", persisted.State, session.StateCompleted)
	}
}

func TestMCPAuthorizationGRPCControlEOFCancelsStrandedPermissionContinuation(t *testing.T) {
	followup := session.NewToolCall("followup-call", "protected", nil)
	f := newLifecycleFixtureWithTurns(t, session.AuthorizationGranted, nil, time.Now, nil,
		mockllm.ToolCallTurn(followup), mockllm.TextTurn("must not continue after a stranded ask"))
	stream := &recheckAuthorizationStream{
		ctx:         t.Context(),
		eofAfterAsk: make(chan struct{}),
		requests: []*mecatlv1.RecheckMcpAuthorizationRequest{{
			SessionId: "authorization-session", AuthorizationId: f.pending.Authorization.ID,
		}},
	}

	if err := NewHarnessServer(f.svc).RecheckMcpAuthorization(stream); err != nil {
		t.Fatal(err)
	}
	if got := stream.responses[len(stream.responses)-1].GetEvent().GetResult().GetStop(); got != "cancelled" {
		t.Fatalf("terminal stop = %q, want cancelled", got)
	}
	persisted, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != session.StateCancelled {
		t.Fatalf("persisted EOF permission continuation state = %q, want %q", persisted.State, session.StateCancelled)
	}
}

func TestMCPAuthorizationGRPCControlEOFBeforeAskCancelsWhenContinuationLaterStrands(t *testing.T) {
	followup := session.NewToolCall("followup-call", "protected", nil)
	f := newLifecycleFixtureWithTurns(t, session.AuthorizationGranted, nil, time.Now, nil,
		mockllm.ToolCallTurn(followup), mockllm.TextTurn("must not continue after a stranded ask"))
	recvErred := make(chan struct{})
	releaseWork := make(chan struct{})
	f.attach.tool.hold = func(ctx context.Context) {
		if f.attach.tool.calls.Load() < 2 {
			return
		}
		select {
		case <-releaseWork:
		case <-ctx.Done():
		}
	}
	stream := &recheckAuthorizationStream{
		ctx:       t.Context(),
		recvErr:   io.EOF,
		recvErred: recvErred,
		requests: []*mecatlv1.RecheckMcpAuthorizationRequest{{
			SessionId: "authorization-session", AuthorizationId: f.pending.Authorization.ID,
		}},
	}
	done := make(chan error, 1)
	go func() { done <- NewHarnessServer(f.svc).RecheckMcpAuthorization(stream) }()
	<-recvErred
	// The authorized tool is still held, so EOF is the relay's only ready input.
	// Give that select turn a bounded scheduling window before the later ask exists.
	time.Sleep(25 * time.Millisecond)
	close(releaseWork)

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		if run, live := f.svc.LookupRun("authorization-session"); live {
			f.svc.cancelRegisteredRun("authorization-session", run)
		}
		<-done
		t.Fatal("control EOF observed before the ask left the later permission continuation stranded")
	}
	if got := stream.responses[len(stream.responses)-1].GetEvent().GetResult().GetStop(); got != "cancelled" {
		t.Fatalf("terminal stop = %q, want cancelled", got)
	}
}

func TestMCPAuthorizationGRPCControlEOFDoesNotCancelPlanApprovalContinuation(t *testing.T) {
	planCall := session.NewToolCall("plan-call", "PresentPlan", json.RawMessage(`{"plan":"inspect the change"}`))
	f := newInteractiveLifecycleFixtureWithMode(t, session.AuthorizationGranted, nil, time.Now, nil,
		session.ModePlan, mockllm.ToolCallTurn(planCall))
	f.attach.refreshTools = []tool.Tool{agent.NewPresentPlanTool()}
	stream := &recheckAuthorizationStream{
		ctx:         t.Context(),
		eofAfterAsk: make(chan struct{}),
		requests: []*mecatlv1.RecheckMcpAuthorizationRequest{{
			SessionId: "authorization-session", AuthorizationId: f.pending.Authorization.ID,
		}},
	}
	done := make(chan error, 1)
	go func() { done <- NewHarnessServer(f.svc).RecheckMcpAuthorization(stream) }()
	select {
	case <-stream.eofAfterAsk:
	case err := <-done:
		t.Fatalf("continuation ended before plan approval ask: %v; responses=%+v", err, stream.responses)
	case <-time.After(time.Second):
		t.Fatal("continuation did not reach plan approval ask")
	}

	select {
	case err := <-done:
		t.Fatalf("plan approval continuation ended on control EOF: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	run, live := f.svc.LookupRun("authorization-session")
	if !live {
		t.Fatal("plan approval continuation was not retained for the dedicated plan workflow")
	}
	f.svc.cancelRegisteredRun("authorization-session", run)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A broken control stream is not a cancellation (H-K5). The stream is a one-shot
// RPC on a context deliberately detached from the continuation; cancelling the
// run on a transport fault is what destroyed a follow-up authorization park mid
// browser round trip and left the session unrecoverably `cancelled` (H-K23).
// rejectCancelledSaveStore is the oracle: a cancelled run cannot persist, so a
// completed persisted state proves the run was left alone.
func TestMCPAuthorizationGRPCNonEOFControlErrorDoesNotCancelContinuation(t *testing.T) {
	f := newLifecycleFixtureWithTurns(t, session.AuthorizationGranted, nil, time.Now, nil, mockllm.TextTurn("continued"))
	f.svc.cfg.Store = rejectCancelledSaveStore{SessionStore: f.store}
	sentinel := errors.New("control stream reset by peer")
	recvErred := make(chan struct{})
	var cancelledInFlight atomic.Bool
	// Hold the resumed protected call open until the control error has been
	// raised, so the relay observes the dead stream while real work is in flight
	// — the exact window a follow-up authorization parks in.
	f.attach.tool.hold = func(ctx context.Context) {
		<-recvErred
		select {
		case <-ctx.Done():
			cancelledInFlight.Store(true)
		case <-time.After(250 * time.Millisecond):
		}
	}
	stream := &recheckAuthorizationStream{
		ctx:       t.Context(),
		recvErr:   sentinel,
		recvErred: recvErred,
		requests:  []*mecatlv1.RecheckMcpAuthorizationRequest{{SessionId: "authorization-session", AuthorizationId: f.pending.Authorization.ID}},
	}

	if err := NewHarnessServer(f.svc).RecheckMcpAuthorization(stream); !errors.Is(err, sentinel) {
		t.Fatalf("RecheckMcpAuthorization error = %v, want sentinel", err)
	}
	if cancelledInFlight.Load() {
		t.Fatal("control-stream transport error cancelled the in-flight continuation")
	}
	persisted, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatalf("load continuation: %v", err)
	}
	if persisted.State != session.StateCompleted {
		t.Fatalf("persisted continuation state = %q, want %q", persisted.State, session.StateCompleted)
	}
	if _, live := f.svc.LookupRun("authorization-session"); live {
		t.Fatal("drained continuation left registered")
	}
}

func TestMCPAuthorizationGRPCRejectsRepeatedInitialControl(t *testing.T) {
	followup := session.NewToolCall("followup-call", "protected", nil)
	f := newLifecycleFixtureWithTurns(t, session.AuthorizationGranted, nil, time.Now, nil, mockllm.ToolCallTurn(followup))
	start := func() *mecatlv1.RecheckMcpAuthorizationRequest {
		return &mecatlv1.RecheckMcpAuthorizationRequest{SessionId: "authorization-session", AuthorizationId: f.pending.Authorization.ID}
	}
	stream := &recheckAuthorizationStream{ctx: t.Context(), requests: []*mecatlv1.RecheckMcpAuthorizationRequest{start(), start()}}
	if err := NewHarnessServer(f.svc).RecheckMcpAuthorization(stream); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("repeated initial control code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestMCPAuthorizationGRPCRejectsMalformedIDBeforeLookup(t *testing.T) {
	f, ownerCtx, _ := ownedAuthorizationFixture(t, session.AuthorizationPending)
	h := NewHarnessServer(f.svc)
	for _, malformed := range []string{"bad id", "line\nbreak", string([]byte{0xff}), strings.Repeat("a", 257)} {
		_, err := h.GetMcpAuthorizationPresentation(ownerCtx, &mecatlv1.GetMcpAuthorizationPresentationRequest{SessionId: "authorization-session", AuthorizationId: malformed})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("id %q code = %v, want InvalidArgument", malformed, status.Code(err))
		}
	}
	_, err := h.GetMcpAuthorizationPresentation(ownerCtx, &mecatlv1.GetMcpAuthorizationPresentationRequest{SessionId: "authorization-session", AuthorizationId: "well-formed-unknown"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("unknown code = %v, want NotFound", status.Code(err))
	}
}

func TestMCPAuthorizationGRPCCancelStreamsResolutionAndContinuation(t *testing.T) {
	f, ownerCtx, _ := ownedAuthorizationFixture(t, session.AuthorizationPending)
	ctx, cancel := context.WithCancel(ownerCtx)
	stream := &cancelAuthorizationStream{
		ctx:          ctx,
		afterInitial: cancel,
		requests:     []*mecatlv1.CancelMcpAuthorizationRequest{{SessionId: "authorization-session", AuthorizationId: f.pending.Authorization.ID}},
	}
	err := NewHarnessServer(f.svc).CancelMcpAuthorization(stream)
	if err != nil {
		t.Fatal(err)
	}
	foundCancelled := false
	for _, response := range stream.responses {
		if response.GetEvent().GetAuthorization().GetStatus() == "cancelled" {
			foundCancelled = true
		}
	}
	if !foundCancelled {
		t.Fatalf("cancel responses lack authoritative cancelled status: %+v", stream.responses)
	}
	if got := stream.responses[len(stream.responses)-1].GetEvent().GetType(); got != "result" {
		t.Fatalf("last event = %q, want result", got)
	}
	persisted, err := f.store.Load(ownerCtx, "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != session.StateCompleted {
		t.Fatalf("persisted cancel continuation state = %q, want %q", persisted.State, session.StateCompleted)
	}
}

type nonFlushingResponseWriter struct{ rec *httptest.ResponseRecorder }

func (w nonFlushingResponseWriter) Header() http.Header         { return w.rec.Header() }
func (w nonFlushingResponseWriter) Write(p []byte) (int, error) { return w.rec.Write(p) }
func (w nonFlushingResponseWriter) WriteHeader(code int)        { w.rec.WriteHeader(code) }

type failingSSEWriter struct {
	*httptest.ResponseRecorder
	failAt        int
	writes        int
	err           error
	onWriteHeader func()
	onWrite       func(int)
}

func (w *failingSSEWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, w.err
	}
	n, err := w.ResponseRecorder.Write(p)
	if w.onWrite != nil {
		w.onWrite(w.writes)
	}
	return n, err
}

func (w *failingSSEWriter) WriteHeader(code int) {
	w.ResponseRecorder.WriteHeader(code)
	if w.onWriteHeader != nil {
		w.onWriteHeader()
	}
}

func TestMCPAuthorizationHTTPControlRejectsNonFlusherBeforeContinuation(t *testing.T) {
	f, ownerCtx, _ := ownedAuthorizationFixture(t, session.AuthorizationGranted)
	h := NewHTTPHandler(f.svc)
	rec := httptest.NewRecorder()
	path := "/v1/sessions/authorization-session/mcp-authorizations/" + f.pending.Authorization.ID + "/recheck"
	h.ServeHTTP(nonFlushingResponseWriter{rec: rec}, httptest.NewRequest(http.MethodPost, path, nil).WithContext(ownerCtx))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if _, live := f.svc.LookupRun("authorization-session"); live {
		t.Fatal("non-Flusher request started a continuation run")
	}
	sess, err := f.svc.GetSession(ownerCtx, "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	if sess.State != session.StateAuthorizing {
		t.Fatalf("state = %q, want authorizing", sess.State)
	}
}

func TestMCPAuthorizationHTTPControlCancellationStopsConstruction(t *testing.T) {
	f, ownerCtx, _ := ownedAuthorizationFixture(t, session.AuthorizationGranted)
	requestCtx, cancel := context.WithCancel(ownerCtx)
	f.attach.statusHook = cancel

	path := "/v1/sessions/authorization-session/mcp-authorizations/" + f.pending.Authorization.ID + "/recheck"
	response := httptest.NewRecorder()
	NewHTTPHandler(f.svc).ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil).WithContext(requestCtx))
	if response.Code == http.StatusOK {
		t.Fatalf("status = %d, want cancellation failure", response.Code)
	}
	if _, live := f.svc.LookupRun("authorization-session"); live {
		t.Fatal("cancelled control request registered a continuation run")
	}
	if got := f.attach.tool.calls.Load(); got != 0 {
		t.Fatalf("protected executions = %d, want 0", got)
	}
	sess, err := f.svc.GetSession(ownerCtx, "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	if sess.State != session.StateAuthorizing {
		t.Fatalf("state = %q, want authorizing", sess.State)
	}
}

func TestMCPAuthorizationHTTPControlRequestLossAfterRegistrationCancelsDrainsAndFinishes(t *testing.T) {
	f, ownerCtx, _ := ownedAuthorizationFixture(t, session.AuthorizationGranted)
	ctx, cancel := context.WithCancel(ownerCtx)
	defer cancel()
	writer := &failingSSEWriter{
		ResponseRecorder: httptest.NewRecorder(),
		onWrite: func(writes int) {
			// The status frame is complete. Wait for the registered continuation to
			// finish producing its buffered events, then lose the request before the
			// relay can select its next event.
			if writes != 3 {
				return
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				sess, err := f.svc.GetSession(ownerCtx, "authorization-session")
				if err == nil && sess.State == session.StateCompleted {
					cancel()
					return
				}
				time.Sleep(time.Millisecond)
			}
			t.Fatal("continuation did not become ready before request loss")
		},
	}
	path := "/v1/sessions/authorization-session/mcp-authorizations/" + f.pending.Authorization.ID + "/recheck"

	NewHTTPHandler(f.svc).ServeHTTP(writer, httptest.NewRequest(http.MethodPost, path, nil).WithContext(ctx))

	// The three status-frame writes precede loss. No ready continuation event may
	// be written after it.
	if writer.writes != 3 {
		t.Fatalf("SSE writes = %d, want only the three status-frame writes", writer.writes)
	}
	if _, live := f.svc.LookupRun("authorization-session"); live {
		t.Fatal("request loss left continuation registered")
	}
}

func TestMCPAuthorizationHTTPControlInitialStatusWriteFailureCancelsDrainsAndFinishes(t *testing.T) {
	f, ownerCtx, _ := ownedAuthorizationFixture(t, session.AuthorizationGranted)
	writer := &failingSSEWriter{
		ResponseRecorder: httptest.NewRecorder(),
		failAt:           1,
		err:              errors.New("initial status write failed"),
	}
	path := "/v1/sessions/authorization-session/mcp-authorizations/" + f.pending.Authorization.ID + "/recheck"

	NewHTTPHandler(f.svc).ServeHTTP(writer, httptest.NewRequest(http.MethodPost, path, nil).WithContext(ownerCtx))

	if writer.writes != 1 {
		t.Fatalf("writes after initial status failure = %d, want 1", writer.writes)
	}
	if _, live := f.svc.LookupRun("authorization-session"); live {
		t.Fatal("initial status write failure left continuation registered")
	}
}

func TestMCPAuthorizationHTTPControlContinuationWriteFailureCancelsDrainsAndFinishes(t *testing.T) {
	f, ownerCtx, _ := ownedAuthorizationFixture(t, session.AuthorizationGranted)
	writer := &failingSSEWriter{
		ResponseRecorder: httptest.NewRecorder(),
		// The status frame writes data, JSON, and its terminating newline first.
		failAt: 4,
		err:    errors.New("continuation event write failed"),
	}
	path := "/v1/sessions/authorization-session/mcp-authorizations/" + f.pending.Authorization.ID + "/recheck"

	NewHTTPHandler(f.svc).ServeHTTP(writer, httptest.NewRequest(http.MethodPost, path, nil).WithContext(ownerCtx))

	if writer.writes != 4 {
		t.Fatalf("writes after continuation failure = %d, want 4", writer.writes)
	}
	if _, live := f.svc.LookupRun("authorization-session"); live {
		t.Fatal("continuation write failure left run registered")
	}
}

func TestMCPAuthorizationHTTPPresentationAndSSERecheck(t *testing.T) {
	f, ownerCtx, foreignCtx := ownedAuthorizationFixture(t, session.AuthorizationPending)
	h := NewHTTPHandler(f.svc)
	presentationPath := "/v1/sessions/authorization-session/mcp-authorizations/" + f.pending.Authorization.ID + "/presentation"

	foreign := httptest.NewRecorder()
	h.ServeHTTP(foreign, httptest.NewRequest(http.MethodGet, presentationPath, nil).WithContext(foreignCtx))
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign presentation status = %d, want 404", foreign.Code)
	}
	owner := httptest.NewRecorder()
	h.ServeHTTP(owner, httptest.NewRequest(http.MethodGet, presentationPath, nil).WithContext(ownerCtx))
	if owner.Code != http.StatusOK || !strings.Contains(owner.Body.String(), "https://auth.example/") {
		t.Fatalf("owner presentation = %d %s", owner.Code, owner.Body.String())
	}

	recheck := httptest.NewRecorder()
	path := "/v1/sessions/authorization-session/mcp-authorizations/" + f.pending.Authorization.ID + "/recheck"
	h.ServeHTTP(recheck, httptest.NewRequest(http.MethodPost, path, nil).WithContext(ownerCtx))
	body, err := io.ReadAll(recheck.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	if recheck.Code != http.StatusOK || recheck.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(string(body), `"status":"pending"`) {
		t.Fatalf("recheck SSE = %d %q %s", recheck.Code, recheck.Header().Get("Content-Type"), body)
	}

	escaped := httptest.NewRecorder()
	h.ServeHTTP(escaped, httptest.NewRequest(http.MethodPost, "/v1/sessions/authorization-session/mcp-authorizations/bad%2Fid/recheck", nil).WithContext(ownerCtx))
	if escaped.Code != http.StatusBadRequest && escaped.Code != http.StatusNotFound {
		t.Fatalf("escaped slash status = %d, want 400/404", escaped.Code)
	}
	malformed := httptest.NewRecorder()
	h.ServeHTTP(malformed, httptest.NewRequest(http.MethodGet, "/v1/sessions/authorization-session/mcp-authorizations/bad%20id/presentation", nil).WithContext(ownerCtx))
	if malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed ID status = %d, want 400", malformed.Code)
	}
	unknown := httptest.NewRecorder()
	h.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "/v1/sessions/authorization-session/mcp-authorizations/well-formed-unknown/presentation", nil).WithContext(ownerCtx))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown ID status = %d, want 404", unknown.Code)
	}

	for _, target := range []string{presentationPath, path, "/v1/sessions/authorization-session/mcp-authorizations/" + f.pending.Authorization.ID + "/cancel"} {
		withAssertion := httptest.NewRecorder()
		method := http.MethodPost
		if target == presentationPath {
			method = http.MethodGet
		}
		h.ServeHTTP(withAssertion, httptest.NewRequest(method, target, strings.NewReader(`{"success":true,"code":"client-asserted"}`)).WithContext(ownerCtx))
		if withAssertion.Code != http.StatusBadRequest {
			t.Errorf("body-bearing %s %s status = %d, want 400", method, target, withAssertion.Code)
		}
	}
}

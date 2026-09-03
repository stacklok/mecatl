package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

type recheckAuthorizationStream struct {
	grpc.BidiStreamingServer[mecatlv1.RecheckMcpAuthorizationRequest, mecatlv1.RecheckMcpAuthorizationResponse]
	ctx          context.Context
	requests     []*mecatlv1.RecheckMcpAuthorizationRequest
	requestCh    chan *mecatlv1.RecheckMcpAuthorizationRequest
	approveOnAsk bool
	afterInitial func()
	sendErr      error
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
	if s.sendErr != nil {
		return s.sendErr
	}
	if s.approveOnAsk && response.GetEvent().GetType() == "permission.ask" {
		s.requestCh <- &mecatlv1.RecheckMcpAuthorizationRequest{Control: &mecatlv1.RecheckMcpAuthorizationRequest_ResumeApproval{ResumeApproval: &mecatlv1.ResumeApproval{AskId: response.GetEvent().GetAsk().GetAskId(), Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ONCE}}}
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
}

func TestMCPAuthorizationGRPCInitialStatusSendFailureCancelsAndFinishesContinuation(t *testing.T) {
	f := newLifecycleFixtureWithTurns(t, session.AuthorizationGranted, nil, time.Now, nil, mockllm.TextTurn("continued"))
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
}

func TestMCPAuthorizationGRPCCancelInitialStatusSendFailureCancelsAndFinishesContinuation(t *testing.T) {
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

func TestMCPAuthorizationGRPCStatusOnlySendFailureIsReturned(t *testing.T) {
	f, ownerCtx, _ := ownedAuthorizationFixture(t, session.AuthorizationPending)
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

package server_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func exactPlanActorService(t *testing.T) (*server.Service, *memstore.EventLog) {
	t.Helper()
	store := memstore.New()
	log := memstore.NewEventLog()
	llm := mockllm.New(mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"plan":"one"}`)))
	cat := tool.NewCatalog()
	cat.MustRegister(agent.NewPresentPlanTool())
	eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: permpolicy.NewPolicy(allowRules(), nil), Model: "test-model", Interactive: true, Store: store})
	svc, err := newPlacementTestService(server.Config{Engine: eng, Store: store, EventLog: log, Now: func() time.Time { return time.Unix(0, 0) }, DefaultCapabilities: llm.Capabilities()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc, log
}

func assertExactApprovalActor(t *testing.T, log *memstore.EventLog, id session.SessionID, runID, askID string, want *session.Principal) {
	t.Helper()
	approvals, otherEvents := 0, 0
	for _, ev := range readEventLog(t, log, id) {
		if ev.Type == session.EvApproval && ev.RunID == runID && ev.Approval != nil && ev.Approval.AskID == askID {
			approvals++
			if got := ownerOf(ev.Actor); got != ownerOf(want) || (ev.Actor == nil) != (want == nil) {
				t.Fatalf("exact approval actor = %+v, want %+v", ev.Actor, want)
			}
			continue
		}
		otherEvents++
		if got := ownerOf(ev.Actor); got != *alice {
			t.Fatalf("unrelated %s actor = %+v, want original caller %+v", ev.Type, ev.Actor, *alice)
		}
	}
	if approvals != 1 || otherEvents == 0 {
		t.Fatalf("logged exact approvals = %d, other events = %d; want one approval and original-caller events", approvals, otherEvents)
	}
}

// TestADR_0204_ExactPlanApprovalUsesVerdictCaller proves an accepted exact
// plan verdict is attributed to its own caller, while other run events retain
// the caller who started the run. The direct case also pins absent identity.
func TestADR_0204_ExactPlanApprovalUsesVerdictCaller(t *testing.T) {
	t.Run("verified gRPC verdict caller", func(t *testing.T) {
		svc, log := exactPlanActorService(t)
		sess, err := svc.CreateSession(session.WithPrincipal(t.Context(), alice), session.ModePlan, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		auth := server.NewAuthenticator(server.SecurityConfig{Validator: fakeValidator{ok: map[string]session.Principal{
			"alice-tok": *alice,
			"bob-tok":   *bob,
		}}})
		client, cleanup := dialGRPCSecure(t, svc, auth)
		defer cleanup()
		stream, err := client.Converse(bearerCtx(t.Context(), "alice-tok"))
		if err != nil {
			t.Fatal(err)
		}
		if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(sess.ID), Text: "make a plan", ServerOwnedPlanContinuation: true}}}); err != nil {
			t.Fatal(err)
		}
		var runID, askID string
		for askID == "" {
			response, err := stream.Recv()
			if err != nil {
				t.Fatalf("plan stream closed before ask: %v", err)
			}
			if ev := response.GetEvent(); ev.GetType() == string(session.EvPermissionAsk) {
				runID, askID = ev.GetRunId(), ev.GetAsk().GetAskId()
			}
		}
		if err := stream.CloseSend(); err != nil {
			t.Fatal(err)
		}
		if _, err := client.ResolvePlanAsk(bearerCtx(t.Context(), "bob-tok"), &mecatlv1.ResolvePlanAskRequest{
			SessionId: string(sess.ID), ExpectedRunId: runID, AskId: askID, Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_DENY,
		}); err != nil {
			t.Fatal(err)
		}
		for {
			_, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		assertExactApprovalActor(t, log, sess.ID, runID, askID, bob)
	})

	t.Run("verified HTTP verdict caller", func(t *testing.T) {
		svc, log := exactPlanActorService(t)
		sess, err := svc.CreateSession(session.WithPrincipal(t.Context(), alice), session.ModePlan, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		auth := server.NewAuthenticator(server.SecurityConfig{Validator: fakeValidator{ok: map[string]session.Principal{
			"alice-tok": *alice,
			"bob-tok":   *bob,
		}}})
		h := auth.Middleware(server.NewHTTPHandler(svc))
		promptCtx, cancelPrompt := context.WithCancel(t.Context())
		defer cancelPrompt()
		prompt := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+string(sess.ID)+"/prompt", strings.NewReader(`{"text":"make a plan","server_owned_plan_continuation":true}`)).WithContext(promptCtx)
		prompt.Header.Set("Authorization", "Bearer alice-tok")
		promptDone := make(chan int, 1)
		go func() {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, prompt)
			promptDone <- rec.Code
		}()
		var runID, askID string
		deadline := time.Now().Add(5 * time.Second)
		for askID == "" && time.Now().Before(deadline) {
			for _, ev := range readEventLog(t, log, sess.ID) {
				if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
					runID, askID = ev.RunID, ev.Ask.AskID
					break
				}
			}
			if askID == "" {
				time.Sleep(time.Millisecond)
			}
		}
		if askID == "" {
			t.Fatal("HTTP prompt did not surface a plan ask")
		}
		control := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+string(sess.ID)+"/controls/resolve-plan-ask", strings.NewReader(`{"expected_run_id":"`+runID+`","ask_id":"`+askID+`","verdict":"deny"}`))
		control.Header.Set("Authorization", "Bearer bob-tok")
		controlRec := httptest.NewRecorder()
		h.ServeHTTP(controlRec, control)
		if controlRec.Code != http.StatusOK {
			t.Fatalf("HTTP exact verdict status = %d, want 200: %s", controlRec.Code, controlRec.Body.String())
		}
		select {
		case code := <-promptDone:
			if code != http.StatusOK {
				t.Fatalf("HTTP prompt status = %d, want 200", code)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("HTTP prompt did not finish after deny")
		}
		cancelPrompt()
		assertExactApprovalActor(t, log, sess.ID, runID, askID, bob)
	})

	t.Run("direct verdict with no caller", func(t *testing.T) {
		svc, log := exactPlanActorService(t)
		starter := session.WithPrincipal(t.Context(), alice)
		sess, err := svc.CreateSession(starter, session.ModePlan, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		run, err := svc.StartInteractiveRunContentWithPlanContinuation(starter, sess.ID, "make a plan", nil)
		if err != nil {
			t.Fatal(err)
		}
		recorder := server.NewRunEventRecorder(context.WithoutCancel(starter), svc, sess.ID)
		var askID string
		for ev := range run.Events() {
			recorder.Observe(ev)
			if ev.Type == session.EvPermissionAsk {
				askID = ev.Ask.AskID
				break
			}
		}
		if askID == "" {
			t.Fatal("live run did not park on a plan ask")
		}
		svc.Persist(starter, sess.ID)
		if _, err := svc.ResolvePlanAsk(t.Context(), sess.ID, run.RunID(), askID, session.VerdictDeny); err != nil {
			t.Fatal(err)
		}
		for ev := range run.Events() {
			recorder.Observe(ev)
		}
		recorder.Close()
		svc.FinishRun(sess.ID, run)
		assertExactApprovalActor(t, log, sess.ID, run.RunID(), askID, nil)
	})
}

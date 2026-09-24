package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func exactPlanService(t *testing.T, store *memstore.Store, llm *mockllm.Provider, lease *fakeLease) *server.Service {
	t.Helper()
	cat := tool.NewCatalog()
	cat.MustRegister(agent.NewPresentPlanTool())
	eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: permpolicy.NewPolicy(allowRules(), nil), Model: "test-model", Interactive: true, Store: store})
	cfg := server.Config{Engine: eng, Store: store, LeaseOwner: "exact-plan-test", LeaseTTL: time.Hour, LeaseRenewInterval: 30 * time.Minute, Now: func() time.Time { return time.Unix(0, 0) }}
	if lease != nil {
		cfg.SessionLease = lease
	}
	svc, err := newPlacementTestService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func waitExactPlanProceed(t *testing.T, svc *server.Service, id session.SessionID, oldRunID string, wantMode session.PermissionMode, llm *mockllm.Provider, wantCalls int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := svc.GetSession(t.Context(), id)
		if err == nil && got.State == session.StateCompleted && got.Mode == wantMode && got.RunID() != oldRunID {
			proceeds := 0
			for _, msg := range got.Conversation.Messages {
				if msg.Role == session.RoleUser && strings.Contains(msg.Text, agent.PlanApprovedProceedText) {
					proceeds++
				}
			}
			if proceeds != 1 {
				t.Fatalf("proceed messages = %d, want exactly one", proceeds)
			}
			if calls := llm.Calls(); calls != wantCalls {
				t.Fatalf("model calls = %d, want %d", calls, wantCalls)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("approved plan did not complete exactly one fresh proceed run")
}

// TestADR_0362_LiveAndRestoredExactPlanAsk proves exact authority and
// server-owned continuation on both sides of a restart.
func TestADR_0362_LiveAndRestoredExactPlanAsk(t *testing.T) {
	t.Run("live", func(t *testing.T) {
		llm := mockllm.New(mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"plan":"one"}`)), mockllm.TextTurn("executed"))
		svc := planApprovalService(t, llm, allowRules())
		t.Cleanup(svc.Close)
		sess, err := svc.CreateSession(t.Context(), session.ModePlan, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		run, err := svc.StartRunContent(context.Background(), sess.ID, "make a plan", nil)
		if err != nil {
			t.Fatal(err)
		}
		askID, _ := awaitLivePlanAsk(t, run, "c1", "one")
		svc.Persist(t.Context(), sess.ID)
		if _, err := svc.ResolvePlanAsk(t.Context(), sess.ID, "old-run", askID, session.VerdictAllowOnce); !errors.Is(err, server.ErrStaleRunControl) {
			t.Fatalf("old run = %v", err)
		}
		if _, err := svc.ResolvePlanAsk(t.Context(), sess.ID, run.RunID(), "other-ask", session.VerdictAllowOnce); !errors.Is(err, server.ErrAskNotPending) {
			t.Fatalf("wrong ask = %v", err)
		}
		controlCtx, cancel := context.WithCancel(t.Context())
		ack, err := svc.ResolvePlanAsk(controlCtx, sess.ID, run.RunID(), askID, session.VerdictAllowOnce)
		if err != nil {
			t.Fatal(err)
		}
		cancel() // A lost unary response cannot cancel the accepted continuation.
		if ack.RunID != run.RunID() || ack.AskID != askID {
			t.Fatalf("ack = %+v", ack)
		}
		if _, err := svc.ResolvePlanAsk(t.Context(), sess.ID, run.RunID(), askID, session.VerdictDeny); !errors.Is(err, server.ErrAskNotPending) {
			t.Fatalf("competing verdict = %v", err)
		}
		evs := drainApprovedEvents(t, run.Events())
		if !hasResultWithStop(evs, session.StopPlanApproved) {
			t.Fatalf("live stops = %v", stopReasonsOf(evs))
		}
		svc.FinishRun(sess.ID, run)
		waitExactPlanProceed(t, svc, sess.ID, run.RunID(), session.ModeDefault, llm, 2)
	})
	t.Run("restored", func(t *testing.T) {
		store := memstore.New()
		parked := makePlanControlSession(t, "exact-plan-restored")
		if err := store.Save(t.Context(), parked); err != nil {
			t.Fatal(err)
		}
		llm := mockllm.New(mockllm.TextTurn("executed"))
		svc := exactPlanService(t, store, llm, nil)
		if _, err := svc.ResolvePlanAsk(t.Context(), parked.ID, "old-run", "plan-ask", session.VerdictAllowOnce); !errors.Is(err, server.ErrStaleRunControl) {
			t.Fatalf("old run = %v", err)
		}
		if _, err := svc.ResolvePlanAsk(t.Context(), parked.ID, parked.RunID(), "other-ask", session.VerdictAllowOnce); !errors.Is(err, server.ErrAskNotPending) {
			t.Fatalf("wrong ask = %v", err)
		}
		controlCtx, cancel := context.WithCancel(t.Context())
		ack, err := svc.ResolvePlanAsk(controlCtx, parked.ID, parked.RunID(), "plan-ask", session.VerdictAllowAlways)
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		if ack.RunID != parked.RunID() || ack.AskID != "plan-ask" {
			t.Fatalf("ack = %+v", ack)
		}
		if _, err := svc.ResolvePlanAsk(t.Context(), parked.ID, parked.RunID(), "plan-ask", session.VerdictDeny); !errors.Is(err, server.ErrAskNotPending) && !errors.Is(err, server.ErrStaleRunControl) {
			t.Fatalf("competing verdict = %v", err)
		}
		waitExactPlanProceed(t, svc, parked.ID, parked.RunID(), session.ModeAccept, llm, 1)
	})
}

// TestStudioPlanAsk_Scenario2_DuplicateAndStaleVerdicts guards the negative
// branches: ordinary origin, lease-race replacement, and explicit iteration.
func TestStudioPlanAsk_Scenario2_DuplicateAndStaleVerdicts(t *testing.T) {
	t.Run("ended restored run", func(t *testing.T) {
		store := memstore.New()
		ended := makePlanControlSession(t, "exact-plan-ended")
		if err := ended.Complete(); err != nil {
			t.Fatal(err)
		}
		if err := store.Save(t.Context(), ended); err != nil {
			t.Fatal(err)
		}
		llm := mockllm.New(mockllm.TextTurn("must not run"))
		svc := exactPlanService(t, store, llm, nil)
		if _, err := svc.ResolvePlanAsk(t.Context(), ended.ID, ended.RunID(), "plan-ask", session.VerdictAllowAlways); !errors.Is(err, server.ErrStaleRunControl) {
			t.Fatalf("ended verdict = %v, want stale_run_control", err)
		}
		got, err := store.Load(t.Context(), ended.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != session.StateCompleted || got.Mode != session.ModePlan || got.RunID() != ended.RunID() || llm.Calls() != 0 {
			t.Fatalf("ended verdict mutated run: state=%s mode=%s run=%s calls=%d", got.State, got.Mode, got.RunID(), llm.Calls())
		}
	})
	t.Run("cancelling live run", func(t *testing.T) {
		llm := mockllm.New(mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"plan":"one"}`)), mockllm.TextTurn("must not run"))
		svc := planApprovalService(t, llm, allowRules())
		t.Cleanup(svc.Close)
		sess, err := svc.CreateSession(t.Context(), session.ModePlan, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		run, err := svc.StartRunContent(context.Background(), sess.ID, "make a plan", nil)
		if err != nil {
			t.Fatal(err)
		}
		askID, _ := awaitLivePlanAsk(t, run, "c1", "one")
		if err := svc.Cancel(t.Context(), sess.ID, run.RunID()); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ResolvePlanAsk(t.Context(), sess.ID, run.RunID(), askID, session.VerdictAllowOnce); !errors.Is(err, server.ErrStaleRunControl) {
			t.Fatalf("cancelling verdict = %v, want stale_run_control", err)
		}
		drainApprovedEvents(t, run.Events())
		svc.FinishRun(sess.ID, run)
		got, err := svc.GetSession(t.Context(), sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Mode != session.ModePlan || got.RunID() != run.RunID() || llm.Calls() != 1 {
			t.Fatalf("cancelling verdict mutated run: mode=%s run=%s calls=%d", got.Mode, got.RunID(), llm.Calls())
		}
	})
	t.Run("foreign session is concealed before live run lookup", func(t *testing.T) {
		store := memstore.New()
		llm := mockllm.New(mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"plan":"one"}`)))
		cat := tool.NewCatalog()
		cat.MustRegister(agent.NewPresentPlanTool())
		eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: permpolicy.NewPolicy(allowRules(), nil), Model: "test-model", Interactive: true, Store: store})
		svc, err := newPlacementTestService(server.Config{Engine: eng, Store: store, OwnershipEnforced: true, Now: func() time.Time { return time.Unix(0, 0) }})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(svc.Close)
		ownerCtx := session.WithPrincipal(t.Context(), alice)
		sess, err := svc.CreateSession(ownerCtx, session.ModePlan, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		run, err := svc.StartRunContent(ownerCtx, sess.ID, "make a plan", nil)
		if err != nil {
			t.Fatal(err)
		}
		askID, _ := awaitLivePlanAsk(t, run, "c1", "one")
		if _, err := svc.ResolvePlanAsk(session.WithPrincipal(t.Context(), bob), sess.ID, run.RunID(), askID, session.VerdictAllowOnce); !errors.Is(err, server.ErrNotFound) {
			t.Fatalf("foreign verdict = %v, want concealed not found", err)
		}
		if _, err := svc.ResolvePlanAsk(ownerCtx, sess.ID, run.RunID(), askID, session.VerdictDeny); err != nil {
			t.Fatalf("owner deny: %v", err)
		}
		evs := drainApprovedEvents(t, run.Events())
		if !hasResultWithStop(evs, session.StopPlanIterate) {
			t.Fatalf("owner deny stops = %v", stopReasonsOf(evs))
		}
		svc.FinishRun(sess.ID, run)
	})
	t.Run("competing restored verdicts", func(t *testing.T) {
		store := memstore.New()
		parked := makePlanControlSession(t, "exact-plan-compete")
		if err := store.Save(t.Context(), parked); err != nil {
			t.Fatal(err)
		}
		llm := mockllm.New(mockllm.TextTurn("executed"))
		svc := exactPlanService(t, store, llm, nil)
		type outcome struct {
			verdict session.ApprovalVerdict
			err     error
		}
		outcomes := make(chan outcome, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, verdict := range []session.ApprovalVerdict{session.VerdictAllowOnce, session.VerdictDeny} {
			wg.Add(1)
			go func(v session.ApprovalVerdict) {
				defer wg.Done()
				<-start
				_, err := svc.ResolvePlanAsk(context.Background(), parked.ID, parked.RunID(), "plan-ask", v)
				outcomes <- outcome{verdict: v, err: err}
			}(verdict)
		}
		close(start)
		wg.Wait()
		close(outcomes)
		accepted := 0
		var winning session.ApprovalVerdict
		for result := range outcomes {
			if result.err == nil {
				accepted++
				winning = result.verdict
				continue
			}
			if !errors.Is(result.err, server.ErrAskNotPending) && !errors.Is(result.err, server.ErrStaleRunControl) {
				t.Fatalf("losing verdict error = %v", result.err)
			}
		}
		if accepted != 1 {
			t.Fatalf("accepted verdicts = %d, want one", accepted)
		}
		if winning == session.VerdictAllowOnce {
			waitExactPlanProceed(t, svc, parked.ID, parked.RunID(), session.ModeDefault, llm, 1)
		} else {
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				got, err := svc.GetSession(t.Context(), parked.ID)
				if err == nil && got.State == session.StateCompleted {
					stop, ok := got.RecordedStopReason()
					if !ok || stop != session.StopPlanIterate || got.Mode != session.ModePlan || llm.Calls() != 0 {
						t.Fatalf("deny outcome: stop=%s mode=%s calls=%d", stop, got.Mode, llm.Calls())
					}
					return
				}
				time.Sleep(time.Millisecond)
			}
			t.Fatal("denying winner did not complete")
		}
	})
	t.Run("ordinary origin", func(t *testing.T) {
		store := memstore.New()
		parked, ask := makePersistedControlSession(t, "ordinary-is-not-plan")
		if err := store.Save(t.Context(), parked); err != nil {
			t.Fatal(err)
		}
		svc := exactPlanService(t, store, mockllm.New(mockllm.TextTurn("must not run")), nil)
		if _, err := svc.ResolvePlanAsk(t.Context(), parked.ID, parked.RunID(), ask.AskID, session.VerdictAllowOnce); !errors.Is(err, server.ErrAskNotPending) {
			t.Fatalf("ordinary ask = %v", err)
		}
		got, err := store.Load(t.Context(), parked.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Mode != session.ModeDefault || got.State != session.StateAwaiting {
			t.Fatalf("ordinary ask mutated: mode=%s state=%s", got.Mode, got.State)
		}
	})
	t.Run("lease race", func(t *testing.T) {
		store := memstore.New()
		parked := makePlanControlSession(t, "exact-plan-lease-race")
		if err := store.Save(t.Context(), parked); err != nil {
			t.Fatal(err)
		}
		lease := &fakeLease{}
		lease.acquireHook = func(id session.SessionID, _ string) {
			fresh, err := store.Load(context.Background(), id)
			if err != nil {
				panic(err)
			}
			if _, err := fresh.ResumeWith(); err != nil {
				panic(err)
			}
			if err := fresh.PauseForApproval(session.PendingAsk{AskID: "replacement", Tool: "PresentPlan", Call: "plan-call", PlanOriginated: true}); err != nil {
				panic(err)
			}
			if err := store.Save(context.Background(), fresh); err != nil {
				panic(err)
			}
		}
		svc := exactPlanService(t, store, mockllm.New(mockllm.TextTurn("must not run")), lease)
		if _, err := svc.ResolvePlanAsk(t.Context(), parked.ID, parked.RunID(), "plan-ask", session.VerdictAllowOnce); !errors.Is(err, server.ErrAskNotPending) {
			t.Fatalf("stale pre-lease ask = %v", err)
		}
		got, err := store.Load(t.Context(), parked.ID)
		if err != nil {
			t.Fatal(err)
		}
		pending, ok := got.PendingAsk()
		if !ok || pending.AskID != "replacement" || got.Mode != session.ModePlan {
			t.Fatalf("lease-race snapshot changed: %+v, mode=%s", pending, got.Mode)
		}
	})
	t.Run("deny iterates without continuation", func(t *testing.T) {
		store := memstore.New()
		parked := makePlanControlSession(t, "exact-plan-deny")
		if err := store.Save(t.Context(), parked); err != nil {
			t.Fatal(err)
		}
		llm := mockllm.New(mockllm.TextTurn("must not run"))
		svc := exactPlanService(t, store, llm, nil)
		if _, err := svc.ResolvePlanAsk(t.Context(), parked.ID, parked.RunID(), "plan-ask", session.VerdictDeny); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			got, err := svc.GetSession(t.Context(), parked.ID)
			if err == nil && got.State == session.StateCompleted {
				stop, ok := got.RecordedStopReason()
				if !ok || stop != session.StopPlanIterate || got.Mode != session.ModePlan || got.RunID() != parked.RunID() || llm.Calls() != 0 {
					t.Fatalf("deny: stop=%s mode=%s run=%s model calls=%d", stop, got.Mode, got.RunID(), llm.Calls())
				}
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("deny did not end as StopPlanIterate")
	})
}

func TestExactPlanAsk_TransportParity(t *testing.T) {
	store := memstore.New()
	parked := makePlanControlSession(t, "exact-plan-wire")
	if err := store.Save(t.Context(), parked); err != nil {
		t.Fatal(err)
	}
	svc := exactPlanService(t, store, mockllm.New(mockllm.TextTurn("executed")), nil)
	if !containsString(svc.CompatibilityInfo(t.Context()).GetFeatures(), "exact_plan_ask_control") {
		t.Fatal("daemon did not advertise exact_plan_ask_control")
	}
	client, closeClient := dialGRPC(t, svc)
	defer closeClient()
	staleRequest := &mecatlv1.ResolvePlanAskRequest{SessionId: string(parked.ID), ExpectedRunId: "old-run", AskId: "plan-ask", Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ONCE}
	if _, err := client.ResolvePlanAsk(t.Context(), staleRequest); status.Code(err) != codes.Aborted {
		t.Fatalf("stale gRPC status = %v, want Aborted", err)
	}
	if _, err := client.ResolvePlanAsk(t.Context(), &mecatlv1.ResolvePlanAskRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty gRPC control = %v, want InvalidArgument", err)
	}
	if _, err := client.ResolvePlanAsk(t.Context(), &mecatlv1.ResolvePlanAskRequest{SessionId: string(parked.ID), ExpectedRunId: parked.RunID(), AskId: "plan-ask"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unspecified verdict = %v, want InvalidArgument", err)
	}
	h := server.NewHTTPHandler(svc)
	path := "/v1/sessions/" + string(parked.ID) + "/controls/resolve-plan-ask"
	staleRec := httptest.NewRecorder()
	h.ServeHTTP(staleRec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"expected_run_id":"old-run","ask_id":"plan-ask","verdict":"allow_once"}`)))
	var staleProblem struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(staleRec.Body.Bytes(), &staleProblem); err != nil {
		t.Fatal(err)
	}
	if staleRec.Code != http.StatusConflict || staleProblem.Code != "stale_run_control" {
		t.Fatalf("stale HTTP status=%d code=%q", staleRec.Code, staleProblem.Code)
	}
	for _, body := range []string{
		`{}`,
		`{"expected_run_id":"run","ask_id":"ask","verdict":1}`,
		`{"expected_run_id":"run","ask_id":"ask","verdict":"allow_once","extra":true}`,
		`{"expected_run_id":"run","ask_id":"ask","verdict":"allow_once"} {}`,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q status=%d, want 400", body, rec.Code)
		}
	}
	ack, err := client.ResolvePlanAsk(t.Context(), &mecatlv1.ResolvePlanAskRequest{SessionId: string(parked.ID), ExpectedRunId: parked.RunID(), AskId: "plan-ask", Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_DENY})
	if err != nil {
		t.Fatal(err)
	}
	if ack.GetRunId() != parked.RunID() || ack.GetAskId() != "plan-ask" {
		t.Fatalf("gRPC ack = %+v", ack)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"expected_run_id":"`+parked.RunID()+`","ask_id":"plan-ask","verdict":"allow_once"}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate HTTP status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	httpParked := makePlanControlSession(t, "exact-plan-http")
	if err := store.Save(t.Context(), httpParked); err != nil {
		t.Fatal(err)
	}
	httpPath := "/v1/sessions/" + string(httpParked.ID) + "/controls/resolve-plan-ask"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, httpPath, strings.NewReader(`{"expected_run_id":"`+httpParked.RunID()+`","ask_id":"plan-ask","verdict":"allow_always"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("accepted HTTP status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"run_id":"`+httpParked.RunID()+`"`) || !strings.Contains(rec.Body.String(), `"ask_id":"plan-ask"`) {
		t.Fatalf("HTTP acknowledgement lost exact correlation: %s", rec.Body.String())
	}
}

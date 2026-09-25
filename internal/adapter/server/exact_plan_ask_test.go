package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func exactPlanService(t *testing.T, store *memstore.Store, llm *mockllm.Provider, lease *fakeLease) *server.Service {
	t.Helper()
	cat := tool.NewCatalog()
	cat.MustRegister(agent.NewPresentPlanTool())
	eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: permpolicy.NewPolicy(allowRules(), nil), Model: "test-model", Interactive: true, Store: store})
	cfg := server.Config{Engine: eng, Store: store, EventLog: memstore.NewEventLog(), LeaseOwner: "exact-plan-test", LeaseTTL: time.Hour, LeaseRenewInterval: 30 * time.Minute, Now: func() time.Time { return time.Unix(0, 0) }}
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

// TestADR_0362_ExactPlanControlRequiresDurableEventLog keeps a deployment
// without durable failure recording from accepting a server-owned plan verdict.
func TestADR_0362_ExactPlanControlRequiresDurableEventLog(t *testing.T) {
	store := memstore.New()
	parked := makePlanControlSession(t, "exact-plan-no-event-log")
	if err := store.Save(t.Context(), parked); err != nil {
		t.Fatal(err)
	}
	llm := mockllm.New(mockllm.TextTurn("must not execute"))
	cat := tool.NewCatalog()
	cat.MustRegister(agent.NewPresentPlanTool())
	eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: permpolicy.NewPolicy(allowRules(), nil), Model: "test-model", Interactive: true, Store: store})
	svc, err := newPlacementTestService(server.Config{Engine: eng, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	if containsString(svc.CompatibilityInfo(t.Context()).GetFeatures(), server.FeatureExactPlanAskControl) {
		t.Fatal("exact plan control advertised without durable failure recording")
	}
	if _, err := svc.ResolvePlanAsk(t.Context(), parked.ID, parked.RunID(), "plan-ask", session.VerdictAllowOnce); !errors.Is(err, server.ErrNoEventLog) {
		t.Fatalf("restored exact verdict without EventLog = %v, want ErrNoEventLog", err)
	}
	got, err := svc.GetSession(t.Context(), parked.ID)
	if err != nil {
		t.Fatal(err)
	}
	ask, ok := got.PendingAsk()
	if got.State != session.StateAwaiting || !ok || ask.AskID != "plan-ask" || got.RunID() != parked.RunID() || llm.Calls() != 0 {
		t.Fatalf("refused verdict changed restored ask: state=%s pending=%+v ok=%t run=%q model calls=%d", got.State, ask, ok, got.RunID(), llm.Calls())
	}

	fresh, err := svc.CreateSession(t.Context(), session.ModePlan, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartInteractiveRunContentWithPlanContinuation(t.Context(), fresh.ID, "make a plan", nil); !errors.Is(err, server.ErrNoEventLog) {
		t.Fatalf("opted-in run without EventLog = %v, want ErrNoEventLog", err)
	}
	if llm.Calls() != 0 {
		t.Fatalf("refused opted-in run reached model %d times", llm.Calls())
	}
}

// TestADR_0362_LiveAndRestoredExactPlanAsk proves exact authority and
// server-owned continuation on both sides of a restart.
func TestADR_0362_LiveAndRestoredExactPlanAsk(t *testing.T) {
	t.Run("unopted live run keeps client continuation ownership", func(t *testing.T) {
		llm := mockllm.New(mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"plan":"one"}`)))
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
		if _, err := svc.ResolvePlanAsk(t.Context(), sess.ID, run.RunID(), askID, session.VerdictAllowOnce); !errors.Is(err, server.ErrFailedPrecondition) {
			t.Fatalf("unopted strict verdict = %v, want failed precondition", err)
		}
		if _, err := svc.ApproveRun(t.Context(), sess.ID, askID, session.VerdictDeny, run.RunID()); err != nil {
			t.Fatalf("legacy owner could not resolve untouched ask: %v", err)
		}
		evs := drainApprovedEvents(t, run.Events())
		if !hasResultWithStop(evs, session.StopPlanIterate) {
			t.Fatalf("legacy deny stops = %v", stopReasonsOf(evs))
		}
		svc.FinishRun(sess.ID, run)
	})
	t.Run("live", func(t *testing.T) {
		llm := mockllm.New(mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"plan":"one"}`)), mockllm.TextTurn("executed"))
		svc := planApprovalService(t, llm, allowRules())
		t.Cleanup(svc.Close)
		sess, err := svc.CreateSession(t.Context(), session.ModePlan, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		run, err := svc.StartInteractiveRunContentWithPlanContinuation(context.Background(), sess.ID, "make a plan", nil)
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
		if _, err := svc.ApproveRun(t.Context(), sess.ID, askID, session.VerdictAllowOnce, run.RunID()); !errors.Is(err, server.ErrPlanResolutionRequired) {
			t.Fatalf("in-stream plan verdict on opted-in run = %v, want plan_resolution_required", err)
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
	t.Run("competing live verdicts accept exactly one", func(t *testing.T) {
		llm := mockllm.New(mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"plan":"one"}`)), mockllm.TextTurn("executed"))
		svc := planApprovalService(t, llm, allowRules())
		t.Cleanup(svc.Close)
		sess, err := svc.CreateSession(t.Context(), session.ModePlan, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		run, err := svc.StartInteractiveRunContentWithPlanContinuation(context.Background(), sess.ID, "make a plan", nil)
		if err != nil {
			t.Fatal(err)
		}
		askID, _ := awaitLivePlanAsk(t, run, "c1", "one")
		type outcome struct {
			verdict session.ApprovalVerdict
			err     error
		}
		results := make(chan outcome, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, verdict := range []session.ApprovalVerdict{session.VerdictAllowOnce, session.VerdictDeny} {
			wg.Add(1)
			go func(v session.ApprovalVerdict) {
				defer wg.Done()
				<-start
				_, err := svc.ResolvePlanAsk(context.Background(), sess.ID, run.RunID(), askID, v)
				results <- outcome{verdict: v, err: err}
			}(verdict)
		}
		close(start)
		wg.Wait()
		close(results)
		var accepted int
		var winner session.ApprovalVerdict
		for result := range results {
			if result.err == nil {
				accepted++
				winner = result.verdict
			} else if !errors.Is(result.err, server.ErrAskNotPending) && !errors.Is(result.err, server.ErrStaleRunControl) {
				t.Fatalf("losing live verdict = %v", result.err)
			}
		}
		if accepted != 1 {
			t.Fatalf("accepted live verdicts = %d, want one", accepted)
		}
		evs := drainApprovedEvents(t, run.Events())
		if winner == session.VerdictAllowOnce {
			if !hasResultWithStop(evs, session.StopPlanApproved) {
				t.Fatalf("allow stops = %v", stopReasonsOf(evs))
			}
		} else if !hasResultWithStop(evs, session.StopPlanIterate) {
			t.Fatalf("deny stops = %v", stopReasonsOf(evs))
		}
		svc.FinishRun(sess.ID, run)
		if winner == session.VerdictAllowOnce {
			waitExactPlanProceed(t, svc, sess.ID, run.RunID(), session.ModeDefault, llm, 2)
		} else if llm.Calls() != 1 {
			t.Fatalf("denied plan started a proceed run: model calls=%d", llm.Calls())
		}
	})
	t.Run("surfaced child ask cannot resolve the root plan", func(t *testing.T) {
		svc, childTool := newInteractiveSubagentService(t)
		t.Cleanup(svc.Close)
		sess, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		run, err := svc.StartInteractiveRunContentWithPlanContinuation(context.Background(), sess.ID, "go", nil)
		if err != nil {
			t.Fatal(err)
		}
		var askID string
		for ev := range run.Events() {
			if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
				askID = ev.Ask.AskID
				break
			}
		}
		if askID == "" {
			t.Fatal("child did not surface its ordinary ask")
		}
		if _, err := svc.ResolvePlanAsk(t.Context(), sess.ID, run.RunID(), askID, session.VerdictAllowOnce); !errors.Is(err, server.ErrAskNotPending) {
			t.Fatalf("child plan verdict = %v, want ask_not_pending", err)
		}
		if _, err := svc.ApproveRun(t.Context(), sess.ID, askID, session.VerdictAllowOnce, run.RunID()); err != nil {
			t.Fatalf("ordinary child approval failed after rejected plan verdict: %v", err)
		}
		drainApprovedEvents(t, run.Events())
		svc.FinishRun(sess.ID, run)
		if !childTool.ran() {
			t.Fatal("ordinary in-stream child approval did not execute the child tool")
		}
	})
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
		svc, err := newPlacementTestService(server.Config{Engine: eng, Store: store, EventLog: memstore.NewEventLog(), OwnershipEnforced: true, Now: func() time.Time { return time.Unix(0, 0) }})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(svc.Close)
		ownerCtx := session.WithPrincipal(t.Context(), alice)
		sess, err := svc.CreateSession(ownerCtx, session.ModePlan, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		run, err := svc.StartInteractiveRunContentWithPlanContinuation(ownerCtx, sess.ID, "make a plan", nil)
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

// TestStudioPlanAsk_ContinuationOwnershipAndFailure pins the accepted plan's
// reserved proceed run and its content-safe failure signal.
type blockingPlanResultLog struct {
	serverLog port.EventLog
	seen      chan struct{}
	release   chan struct{}
}

type notifyingPlanAskLog struct {
	port.EventLog
	asks chan session.Event
}

func (l *notifyingPlanAskLog) Append(ctx context.Context, id session.SessionID, ev session.Event) error {
	if err := l.EventLog.Append(ctx, id, ev); err != nil {
		return err
	}
	if ev.Type == session.EvPermissionAsk && ev.Ask != nil && ev.Ask.Origin() == session.AskOriginPlan {
		l.asks <- ev
	}
	return nil
}

func (l *blockingPlanResultLog) Append(ctx context.Context, id session.SessionID, ev session.Event) error {
	if err := l.serverLog.Append(ctx, id, ev); err != nil {
		return err
	}
	if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Stop == session.StopPlanApproved {
		close(l.seen)
		<-l.release
	}
	return nil
}

func (l *blockingPlanResultLog) Read(ctx context.Context, id session.SessionID) iter.Seq2[session.Event, error] {
	return l.serverLog.Read(ctx, id)
}

func TestStudioPlanAsk_ContinuationOwnershipAndFailure(t *testing.T) {
	t.Run("HTTP prompt mirrors the run-start opt-in", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			optIn    bool
			wantCode int
		}{
			{name: "unopted", wantCode: http.StatusPreconditionFailed},
			{name: "opted in", optIn: true, wantCode: http.StatusOK},
		} {
			t.Run(tc.name, func(t *testing.T) {
				store := memstore.New()
				llm := mockllm.New(mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"plan":"one"}`)), mockllm.TextTurn("executed"))
				cat := tool.NewCatalog()
				cat.MustRegister(agent.NewPresentPlanTool())
				eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: permpolicy.NewPolicy(allowRules(), nil), Model: "test-model", Interactive: true, Store: store})
				log := &notifyingPlanAskLog{EventLog: memstore.NewEventLog(), asks: make(chan session.Event, 1)}
				svc, err := newPlacementTestService(server.Config{Engine: eng, Store: store, EventLog: log})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(svc.Close)
				sess, err := svc.CreateSession(t.Context(), session.ModePlan, session.Limits{})
				if err != nil {
					t.Fatal(err)
				}
				h := server.NewHTTPHandler(svc)
				body := `{"text":"make a plan"}`
				if tc.optIn {
					body = `{"text":"make a plan","server_owned_plan_continuation":true}`
				}
				rec := httptest.NewRecorder()
				promptDone := make(chan struct{})
				promptCtx, cancelPrompt := context.WithCancel(t.Context())
				defer cancelPrompt()
				go func() {
					h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/sessions/"+string(sess.ID)+"/prompt", strings.NewReader(body)).WithContext(promptCtx))
					close(promptDone)
				}()
				var ask session.Event
				select {
				case ask = <-log.asks:
				case <-time.After(5 * time.Second):
					t.Fatal("HTTP prompt did not surface a plan ask")
				}
				control := httptest.NewRecorder()
				path := "/v1/sessions/" + string(sess.ID) + "/controls/resolve-plan-ask"
				h.ServeHTTP(control, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"expected_run_id":"`+ask.RunID+`","ask_id":"`+ask.Ask.AskID+`","verdict":"allow_once"}`)))
				if control.Code != tc.wantCode {
					t.Fatalf("HTTP exact verdict status = %d, want %d; body=%s", control.Code, tc.wantCode, control.Body.String())
				}
				if !tc.optIn {
					if _, err := svc.ApproveRun(t.Context(), sess.ID, ask.Ask.AskID, session.VerdictDeny, ask.RunID); err != nil {
						t.Fatalf("legacy owner could not deny untouched ask: %v", err)
					}
				}
				select {
				case <-promptDone:
				case <-time.After(5 * time.Second):
					t.Fatal("HTTP prompt relay did not finish")
				}
				cancelPrompt()
				if tc.optIn {
					waitExactPlanProceed(t, svc, sess.ID, ask.RunID, session.ModeDefault, llm, 2)
				} else if llm.Calls() != 1 {
					t.Fatalf("unopted rejected verdict started a proceed run: model calls=%d", llm.Calls())
				}
			})
		}
	})
	t.Run("accepted continuation has priority over a competing prompt", func(t *testing.T) {
		store := memstore.New()
		llm := mockllm.New(mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"plan":"one"}`)), mockllm.TextTurn("executed"))
		cat := tool.NewCatalog()
		cat.MustRegister(agent.NewPresentPlanTool())
		eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: permpolicy.NewPolicy(allowRules(), nil), Model: "test-model", Interactive: true, Store: store})
		log := &blockingPlanResultLog{serverLog: memstore.NewEventLog(), seen: make(chan struct{}), release: make(chan struct{})}
		svc, err := newPlacementTestService(server.Config{Engine: eng, Store: store, EventLog: log})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(svc.Close)
		t.Cleanup(func() {
			select {
			case <-log.release:
			default:
				close(log.release)
			}
		})
		sess, err := svc.CreateSession(t.Context(), session.ModePlan, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		client, closeClient := dialGRPC(t, svc)
		defer closeClient()
		stream, err := client.Converse(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(sess.ID), Text: "make a plan", ServerOwnedPlanContinuation: true}}}); err != nil {
			t.Fatal(err)
		}
		var planRunID, askID string
		for askID == "" {
			response, err := stream.Recv()
			if err != nil {
				t.Fatalf("plan stream closed before ask: %v", err)
			}
			if ev := response.GetEvent(); ev.GetType() == string(session.EvPermissionAsk) {
				planRunID, askID = ev.GetRunId(), ev.GetAsk().GetAskId()
			}
		}
		if err := stream.CloseSend(); err != nil {
			t.Fatal(err)
		}
		if _, err := client.ResolvePlanAsk(t.Context(), &mecatlv1.ResolvePlanAskRequest{SessionId: string(sess.ID), ExpectedRunId: planRunID, AskId: askID, Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ONCE}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-log.seen:
		case <-time.After(5 * time.Second):
			t.Fatal("approved terminal was not relayed to the event log")
		}
		if _, err := svc.StartRunContent(t.Context(), sess.ID, "competing prompt", nil); !errors.Is(err, server.ErrFailedPrecondition) {
			t.Fatalf("competing prompt = %v, want failed precondition while continuation reserved", err)
		}
		close(log.release)
		for {
			_, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		waitExactPlanProceed(t, svc, sess.ID, planRunID, session.ModeDefault, llm, 2)
	})
	t.Run("grpc opt-in closes the old stream before slow proceed admission", func(t *testing.T) {
		store := memstore.New()
		llm := mockllm.New(mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"plan":"one"}`)), mockllm.TextTurn("executed"))
		cat := tool.NewCatalog()
		cat.MustRegister(agent.NewPresentPlanTool())
		eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: permpolicy.NewPolicy(allowRules(), nil), Model: "test-model", Interactive: true, Store: store})
		entered := make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		var admissions atomic.Int32
		svc, err := newPlacementTestService(server.Config{Engine: eng, Store: store, EventLog: memstore.NewEventLog(), AwaitContextWindow: func(context.Context, string, string) error {
			if admissions.Add(1) == 2 {
				close(entered)
				<-release
			}
			return nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(svc.Close)
		t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
		sess, err := svc.CreateSession(t.Context(), session.ModePlan, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		client, closeClient := dialGRPC(t, svc)
		defer closeClient()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		stream, err := client.Converse(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(sess.ID), Text: "make a plan", ServerOwnedPlanContinuation: true}}}); err != nil {
			t.Fatal(err)
		}
		var planRunID, askID string
		for askID == "" {
			response, err := stream.Recv()
			if err != nil {
				t.Fatalf("plan stream closed before ask: %v", err)
			}
			if ev := response.GetEvent(); ev.GetType() == string(session.EvPermissionAsk) {
				planRunID, askID = ev.GetRunId(), ev.GetAsk().GetAskId()
			}
		}
		if err := stream.CloseSend(); err != nil {
			t.Fatal(err)
		}
		if _, err := client.ResolvePlanAsk(t.Context(), &mecatlv1.ResolvePlanAskRequest{SessionId: string(sess.ID), ExpectedRunId: planRunID, AskId: askID, Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ONCE}); err != nil {
			t.Fatal(err)
		}
		streamDone := make(chan error, 1)
		go func() {
			var approved bool
			for {
				response, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					if !approved {
						streamDone <- errors.New("old stream closed without plan-approved terminal")
					} else {
						streamDone <- nil
					}
					return
				}
				if err != nil {
					streamDone <- err
					return
				}
				if ev := response.GetEvent(); ev.GetType() == string(session.EvResult) && ev.GetResult().GetStop() == string(session.StopPlanApproved) {
					approved = true
				}
			}
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("proceed admission did not reach the controlled window gate")
		}
		select {
		case err := <-streamDone:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("old plan stream stayed open while proceed admission waited")
		}
		releaseOnce.Do(func() { close(release) })
		waitExactPlanProceed(t, svc, sess.ID, planRunID, session.ModeDefault, llm, 2)
		closed := make(chan struct{})
		go func() { svc.Close(); close(closed) }()
		select {
		case <-closed:
		case <-time.After(3 * time.Second):
			t.Fatal("Service.Close retained a completed plan continuation reservation")
		}
	})
	t.Run("known start failure is durable and published without a second terminal", func(t *testing.T) {
		store := memstore.New()
		parked := makePlanControlSession(t, "exact-plan-start-failure")
		if err := store.Save(t.Context(), parked); err != nil {
			t.Fatal(err)
		}
		log := memstore.NewEventLog()
		llm := mockllm.New(mockllm.TextTurn("must not execute"))
		cat := tool.NewCatalog()
		cat.MustRegister(agent.NewPresentPlanTool())
		eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: permpolicy.NewPolicy(allowRules(), nil), Model: "test-model", Interactive: true, Store: store})
		var admissions atomic.Int32
		svc, err := newPlacementTestService(server.Config{Engine: eng, Store: store, EventLog: log, Now: func() time.Time { return time.Unix(0, 0) }, AwaitContextWindow: func(context.Context, string, string) error {
			if admissions.Add(1) == 2 {
				return errors.New("secret failure detail")
			}
			return nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(svc.Close)
		live, unsubscribe, err := svc.Subscribe(t.Context(), parked.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer unsubscribe()
		if _, err := svc.ResolvePlanAsk(t.Context(), parked.ID, parked.RunID(), "plan-ask", session.VerdictAllowOnce); err != nil {
			t.Fatal(err)
		}
		var failure session.Event
		deadline := time.After(5 * time.Second)
		for failure.Type != session.EvPlanContinuationFailed {
			select {
			case failure = <-live:
			case <-deadline:
				t.Fatal("no published continuation failure")
			}
		}
		if failure.RunID != "" || failure.PlanContinuationFailure == nil || failure.PlanContinuationFailure.PlanRunID != parked.RunID() || failure.PlanContinuationFailure.AskID != "plan-ask" || failure.Text != "" {
			t.Fatalf("unsafe or uncorrelated failure event = %+v", failure)
		}
		var failures, terminals int
		for ev, readErr := range log.Read(t.Context(), parked.ID) {
			if readErr != nil {
				t.Fatal(readErr)
			}
			if ev.Type == session.EvPlanContinuationFailed {
				failures++
				if !reflect.DeepEqual(ev, failure) {
					t.Fatalf("published failure differs from durable failure: %+v %+v", ev, failure)
				}
			}
			if ev.Type == session.EvResult {
				terminals++
			}
		}
		if failures != 1 || terminals != 1 || llm.Calls() != 0 {
			t.Fatalf("failures=%d terminals=%d model calls=%d", failures, terminals, llm.Calls())
		}
		closed := make(chan struct{})
		go func() { svc.Close(); close(closed) }()
		select {
		case <-closed:
		case <-time.After(3 * time.Second):
			t.Fatal("Service.Close retained a failed plan continuation reservation")
		}
	})
	t.Run("lost lease leaves the outcome uncertain without publishing failure", func(t *testing.T) {
		store := memstore.New()
		parked := makePlanControlSession(t, "exact-plan-lost-lease")
		if err := store.Save(t.Context(), parked); err != nil {
			t.Fatal(err)
		}
		log := memstore.NewEventLog()
		llm := mockllm.New(mockllm.TextTurn("must not execute"))
		cat := tool.NewCatalog()
		cat.MustRegister(agent.NewPresentPlanTool())
		eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: permpolicy.NewPolicy(allowRules(), nil), Model: "test-model", Interactive: true, Store: store})
		lease := &fakeLease{}
		var triggerLoss atomic.Bool
		lossAttempted := make(chan struct{})
		var lossOnce sync.Once
		lease.renewHook = func(current port.Lease) (port.Lease, error) {
			if triggerLoss.Load() {
				lossOnce.Do(func() { close(lossAttempted) })
				return port.Lease{}, port.ErrLeaseHeld
			}
			return current, nil
		}
		var admissions atomic.Int32
		secondEntered := make(chan struct{})
		svc, err := newPlacementTestService(server.Config{
			Engine: eng, Store: store, EventLog: log, SessionLease: lease,
			LeaseOwner: "exact-plan-lost-lease", LeaseTTL: time.Second, LeaseRenewInterval: 5 * time.Millisecond,
			AwaitContextWindow: func(ctx context.Context, _, _ string) error {
				if admissions.Add(1) == 2 {
					close(secondEntered)
					triggerLoss.Store(true)
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(svc.Close)
		live, unsubscribe, err := svc.Subscribe(t.Context(), parked.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer unsubscribe()
		if _, err := svc.ResolvePlanAsk(t.Context(), parked.ID, parked.RunID(), "plan-ask", session.VerdictAllowOnce); err != nil {
			t.Fatal(err)
		}
		for label, signal := range map[string]<-chan struct{}{"proceed admission": secondEntered, "lease loss": lossAttempted} {
			select {
			case <-signal:
			case <-time.After(5 * time.Second):
				t.Fatalf("timed out waiting for %s", label)
			}
		}
		if !eventually(5*time.Second, func() bool { return lease.releaseCount() == 1 }) {
			t.Fatal("lease loss did not complete its release")
		}
		closed := make(chan struct{})
		go func() { svc.Close(); close(closed) }()
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("continuation reservation survived lost-lease teardown")
		}
		for ev := range live {
			if ev.Type == session.EvPlanContinuationFailed {
				t.Fatal("former lease owner published an unrecorded failure")
			}
		}
		for ev, readErr := range log.Read(t.Context(), parked.ID) {
			if readErr != nil {
				t.Fatal(readErr)
			}
			if ev.Type == session.EvPlanContinuationFailed {
				t.Fatal("former lease owner appended a failure after lease loss")
			}
		}
		if llm.Calls() != 0 {
			t.Fatalf("execution provider starts after lease loss = %d, want zero", llm.Calls())
		}
	})
}

func TestADR_0362_HeadlessAutoApproveUsesExactOwnership(t *testing.T) {
	store := memstore.New()
	llm := mockllm.New(mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"plan":"one"}`)), mockllm.TextTurn("executed"))
	cat := tool.NewCatalog()
	cat.MustRegister(agent.NewPresentPlanTool())
	eng := agent.NewEngine(agent.Deps{
		LLM: llm, Catalog: cat, Policy: permpolicy.NewPolicy(allowRules(), nil),
		Model: "test-model", Interactive: false, PlanModeAutoApprove: true, Store: store,
	})
	svc, err := newPlacementTestService(server.Config{Engine: eng, Store: store, EventLog: memstore.NewEventLog(), PlanModeAutoApprove: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	sess, err := svc.CreateSession(t.Context(), session.ModePlan, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	client, closeClient := dialGRPC(t, svc)
	defer closeClient()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(sess.ID), Text: "make a plan", ServerOwnedPlanContinuation: true}}}); err != nil {
		t.Fatal(err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	var planRunID string
	var approved bool
	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("headless opted-in plan stream did not terminate: %v", err)
		}
		ev := response.GetEvent()
		if ev.GetType() == string(session.EvPermissionAsk) {
			planRunID = ev.GetRunId()
		}
		if ev.GetType() == string(session.EvResult) && ev.GetResult().GetStop() == string(session.StopPlanApproved) {
			approved = true
		}
	}
	if planRunID == "" || !approved {
		t.Fatalf("operator auto-approve did not end the exact plan run: run=%q approved=%t", planRunID, approved)
	}
	waitExactPlanProceed(t, svc, sess.ID, planRunID, session.ModeDefault, llm, 2)
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

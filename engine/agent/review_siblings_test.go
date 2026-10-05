package agent

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type budgetReadTool struct{ executions *atomic.Int32 }

func (budgetReadTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "BudgetRead", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (budgetReadTool) ReadOnly() bool { return true }
func (t budgetReadTool) Execute(_ context.Context, c session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	t.executions.Add(1)
	return session.NewToolResult(c.ID, "done"), nil
}

type siblingBudgetReviewer struct {
	mu      sync.Mutex
	called  []session.ToolCallID
	enforce bool
}

func (r *siblingBudgetReviewer) GuardrailReviewPolicy(_ string, job ReviewJob, failure bool) (bool, bool) {
	return job == ReviewJobAction, r.enforce || !failure
}
func (r *siblingBudgetReviewer) Review(_ context.Context, req ToolReviewRequest, _ ReviewEvidenceSource) (ToolReviewResult, session.AuxiliaryUsage, error) {
	r.mu.Lock()
	r.called = append(r.called, req.EffectiveCall.ID)
	r.mu.Unlock()
	return ToolReviewResult{Assessment: ReviewAcceptable}, session.AuxiliaryUsage{}, nil
}

type closingReviewPreparer struct {
	closed    int
	reviewCtx context.Context
	cancel    context.CancelFunc
}

func (p *closingReviewPreparer) PrepareReviewEvidence(ctx context.Context, _ ReviewEvidencePreparation) (PreparedReviewEvidence, error) {
	if p.reviewCtx != nil {
		p.cancel()
		return PreparedReviewEvidence{}, ctx.Err()
	}
	p.reviewCtx = ctx
	return PreparedReviewEvidence{Complete: true, Close: func() { p.closed++ }}, nil
}

func TestCancelledReadBatchClosesUnassessedSibling(t *testing.T) {
	var executions atomic.Int32
	catalog := tool.NewCatalog()
	catalog.MustRegister(budgetReadTool{executions: &executions})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	policy := terminalFailurePolicy{job: ReviewJobAction}
	preparer := &closingReviewPreparer{cancel: cancel}
	calls := []session.ToolCall{session.NewToolCall("first", "BudgetRead", []byte(`{}`)), session.NewToolCall("second", "BudgetRead", []byte(`{}`))}
	engine := NewEngine(Deps{LLM: mockllm.New(mockllm.ToolCallTurn(calls...)), Catalog: catalog, Policy: policy, ToolReviewer: &siblingBudgetReviewer{}, ReviewEvidencePreparer: preparer})
	env := memEnv("/ws")
	sess := session.New("cancel-siblings", session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
	for range engine.Run(ctx, sess, env, RunRequest{Text: "read"}).Events() {
	}
	if preparer.closed != 1 || preparer.reviewCtx == nil || preparer.reviewCtx.Err() == nil || executions.Load() != 0 {
		t.Fatalf("closed=%d review context=%v executions=%d", preparer.closed, preparer.reviewCtx, executions.Load())
	}
}

type cancellingSiblingPreparer struct {
	entered <-chan struct{}
	cancel  context.CancelFunc
	closed  atomic.Int32
}

func (p *cancellingSiblingPreparer) PrepareReviewEvidence(ctx context.Context, req ReviewEvidencePreparation) (PreparedReviewEvidence, error) {
	if req.Request.EffectiveCall.ID == "second" {
		<-p.entered
		p.cancel()
		return PreparedReviewEvidence{}, ctx.Err()
	}
	return PreparedReviewEvidence{Complete: true, Close: func() { p.closed.Add(1) }}, nil
}

type cancellationCleanupReviewer struct {
	terminalFailureReviewer
	entered chan struct{}
	cleanup chan struct{}
	release <-chan struct{}
}

func (r *cancellationCleanupReviewer) Review(ctx context.Context, _ ToolReviewRequest, _ ReviewEvidenceSource) (ToolReviewResult, session.AuxiliaryUsage, error) {
	r.reviews++
	close(r.entered)
	<-ctx.Done()
	close(r.cleanup)
	<-r.release
	return ToolReviewResult{Assessment: ReviewUnresolved}, testGuardrailUsage(7), ctx.Err()
}

func TestCancelledReadBatchJoinsActiveCheckerBeforeClosingSource(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		release := make(chan struct{})
		defer close(release)
		reviewer := &cancellationCleanupReviewer{terminalFailureReviewer: terminalFailureReviewer{job: ReviewJobAction}, entered: make(chan struct{}), cleanup: make(chan struct{}), release: release}
		preparer := &cancellingSiblingPreparer{entered: reviewer.entered, cancel: cancel}
		var executions atomic.Int32
		catalog := tool.NewCatalog()
		catalog.MustRegister(budgetReadTool{executions: &executions})
		calls := []session.ToolCall{session.NewToolCall("first", "BudgetRead", []byte(`{}`)), session.NewToolCall("second", "BudgetRead", []byte(`{}`))}
		details := &rootContractSink{}
		engine := NewEngine(Deps{LLM: mockllm.New(mockllm.ToolCallTurn(calls...)), Catalog: catalog, Policy: terminalFailurePolicy{job: ReviewJobAction}, ToolReviewer: reviewer, ReviewEvidencePreparer: preparer, ReviewDetails: details, Interactive: true})
		env := memEnv("/ws")
		sess := session.New("cancel-active-sibling", session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
		run := engine.Run(ctx, sess, env, RunRequest{Text: "read"})
		done := make(chan struct{})
		var events []session.Event
		go func() {
			defer close(done)
			for ev := range run.Events() {
				events = append(events, ev)
			}
		}()
		synctest.Wait()
		select {
		case <-reviewer.cleanup:
		default:
			t.Fatal("first checker did not enter cancellation cleanup")
		}
		select {
		case <-done:
			t.Error("run finished before active checker cleanup was released")
		default:
		}
		if got := preparer.closed.Load(); got != 0 {
			t.Errorf("source closed %d times while checker was active", got)
		}
		release <- struct{}{}
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("run did not finish after checker cleanup")
		}
		stopped := false
		for _, ev := range events {
			if ev.Result != nil && ev.Result.Stop == session.StopCancelled {
				stopped = true
			}
			if ev.Type == session.EvPermissionAsk || (ev.Hook != nil && ev.Hook.Guardrail != nil) {
				t.Errorf("cancelled review published %+v", ev)
			}
		}
		if !stopped || preparer.closed.Load() != 1 || reviewer.reviews != 1 || executions.Load() != 0 || reviewer.recorded != nil || details.got.ReviewID != "" {
			t.Fatalf("stopped=%t closed=%d reviews=%d executions=%d failure=%v detail=%+v", stopped, preparer.closed.Load(), reviewer.reviews, executions.Load(), reviewer.recorded, details.got)
		}
		if got := sess.UsageFor(session.UsageKindGuardrail); got.InputTokens != 7 {
			t.Fatalf("cancelled checker usage=%+v, want 7 input tokens exactly once", got)
		}
	})
}

type siblingAskGate struct {
	hook  bool
	asked bool
}

func (g *siblingAskGate) Evaluate(_ context.Context, _ session.SessionID, _ session.PermissionMode, call session.ToolCall, _ tool.WorkspaceReader) port.PermissionResult {
	if !g.hook && call.ID == "second" && !g.asked {
		g.asked = true
		return port.PermissionResult{Decision: governance.PermissionDecision{Effect: governance.Ask, Reason: "second sibling needs approval"}}
	}
	return port.PermissionResult{Decision: governance.PermissionDecision{Effect: governance.Allow}}
}
func (*siblingAskGate) Learn(session.SessionID, session.ToolCall) {}
func (g *siblingAskGate) Run(_ context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	if g.hook && ev.Phase == governance.PhasePreToolUse && ev.CallID == "second" && !g.asked {
		g.asked = true
		return governance.HookOutcome{Block: true, AskApproval: true, Message: "approve second sibling"}, nil
	}
	return governance.HookOutcome{}, nil
}

func TestReadBatchHumanWaitPrecedesAllReviewBudgets(t *testing.T) {
	for _, hook := range []bool{false, true} {
		name := "permission"
		if hook {
			name = "prehook"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var executions atomic.Int32
				catalog := tool.NewCatalog()
				catalog.MustRegister(budgetReadTool{executions: &executions})
				reviewer := &siblingBudgetReviewer{}
				gate := &siblingAskGate{hook: hook}
				calls := []session.ToolCall{session.NewToolCall("first", "BudgetRead", []byte(`{}`)), session.NewToolCall("second", "BudgetRead", []byte(`{}`))}
				engine := NewEngine(Deps{LLM: mockllm.New(mockllm.ToolCallTurn(calls...), mockllm.TextTurn("done")), Catalog: catalog, Policy: gate, Hooks: gate, ToolReviewer: reviewer, Interactive: true})
				env := memEnv("/ws")
				sess := session.New("wait-siblings", session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
				run := engine.Run(context.Background(), sess, env, RunRequest{Text: "read"})
				asks, timeouts := 0, 0
				for ev := range run.Events() {
					if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
						asks++
						<-time.After(91 * time.Second)
						if err := run.Approve(ev.Ask.AskID, session.VerdictAllowOnce); err != nil {
							t.Fatal(err)
						}
					}
					if ev.Hook != nil && ev.Hook.Guardrail != nil && ev.Hook.Guardrail.ReasonCode == string(ReviewFailureTimeout) {
						timeouts++
					}
				}
				if asks != 1 || timeouts != 0 || len(reviewer.called) != 2 || executions.Load() != 2 {
					t.Fatalf("asks=%d timeouts=%d reviewed=%v executed=%d", asks, timeouts, reviewer.called, executions.Load())
				}
			})
		})
	}
}

type observingSiblingPreparer struct {
	deadlineEvidencePreparer
	reviewer                 *siblingBudgetReviewer
	firstCheckedDuringSecond bool
}

func (p *observingSiblingPreparer) PrepareReviewEvidence(ctx context.Context, req ReviewEvidencePreparation) (PreparedReviewEvidence, error) {
	if p.calls == 1 {
		synctest.Wait()
		p.reviewer.mu.Lock()
		p.firstCheckedDuringSecond = len(p.reviewer.called) == 1 && p.reviewer.called[0] == "first"
		p.reviewer.mu.Unlock()
	}
	return p.deadlineEvidencePreparer.PrepareReviewEvidence(ctx, req)
}

func TestReadBatchSiblingReviewBudgetsStartAtOwnPreparation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var executions atomic.Int32
		catalog := tool.NewCatalog()
		catalog.MustRegister(budgetReadTool{executions: &executions})
		reviewer := &siblingBudgetReviewer{enforce: true}
		preparer := &observingSiblingPreparer{deadlineEvidencePreparer: deadlineEvidencePreparer{delay: 50 * time.Second}, reviewer: reviewer}
		calls := []session.ToolCall{session.NewToolCall("first", "BudgetRead", []byte(`{}`)), session.NewToolCall("second", "BudgetRead", []byte(`{}`))}
		engine := NewEngine(Deps{LLM: mockllm.New(mockllm.ToolCallTurn(calls...), mockllm.TextTurn("done")), Catalog: catalog, Policy: terminalFailurePolicy{job: ReviewJobAction}, ToolReviewer: reviewer, ReviewEvidencePreparer: preparer})
		env := memEnv("/ws")
		sess := session.New("siblings", session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
		timeouts := 0
		for ev := range engine.Run(context.Background(), sess, env, RunRequest{Text: "read"}).Events() {
			if ev.Hook != nil && ev.Hook.Guardrail != nil && ev.Hook.Guardrail.ReasonCode == string(ReviewFailureTimeout) {
				timeouts++
			}
		}
		reviewer.mu.Lock()
		defer reviewer.mu.Unlock()
		if len(reviewer.called) != 2 || reviewer.called[0] != "first" || reviewer.called[1] != "second" || timeouts != 0 || executions.Load() != 2 || preparer.calls != 2 || !preparer.firstCheckedDuringSecond {
			t.Fatalf("called=%v timeouts=%d executions=%d prepares=%d first checked during second=%t", reviewer.called, timeouts, executions.Load(), preparer.calls, preparer.firstCheckedDuringSecond)
		}
	})
}

func TestReadBatchSlowOwnPreparationTimesOutUnderEnforcement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var executions atomic.Int32
		catalog := tool.NewCatalog()
		catalog.MustRegister(budgetReadTool{executions: &executions})
		reviewer := &siblingBudgetReviewer{enforce: true}
		preparer := &deadlineEvidencePreparer{delay: 91 * time.Second}
		calls := []session.ToolCall{session.NewToolCall("first", "BudgetRead", []byte(`{}`)), session.NewToolCall("second", "BudgetRead", []byte(`{}`))}
		engine := NewEngine(Deps{LLM: mockllm.New(mockllm.ToolCallTurn(calls...), mockllm.TextTurn("done")), Catalog: catalog, Policy: terminalFailurePolicy{job: ReviewJobAction}, ToolReviewer: reviewer, ReviewEvidencePreparer: preparer})
		env := memEnv("/ws")
		sess := session.New("slow-own-prep", session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
		timeouts := 0
		for ev := range engine.Run(context.Background(), sess, env, RunRequest{Text: "read"}).Events() {
			if ev.Hook != nil && ev.Hook.Guardrail != nil && ev.Hook.Guardrail.ReasonCode == string(ReviewFailureTimeout) {
				timeouts++
			}
		}
		if timeouts != 2 || executions.Load() != 0 || preparer.calls != 2 || len(reviewer.called) != 0 {
			t.Fatalf("timeouts=%d executions=%d prepares=%d reviewed=%v", timeouts, executions.Load(), preparer.calls, reviewer.called)
		}
	})
}

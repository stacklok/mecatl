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
	mu     sync.Mutex
	called []session.ToolCallID
}

func (r *siblingBudgetReviewer) GuardrailReviewPolicy(_ string, job ReviewJob, failure bool) (bool, bool) {
	return job == ReviewJobAction, !failure
}
func (r *siblingBudgetReviewer) Review(ctx context.Context, req ToolReviewRequest, _ ReviewEvidenceSource) (ToolReviewResult, error) {
	r.mu.Lock()
	r.called = append(r.called, req.EffectiveCall.ID)
	r.mu.Unlock()
	return ToolReviewResult{Assessment: ReviewAcceptable}, nil
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

type siblingAskGate struct {
	hook  bool
	asked bool
}

func (g *siblingAskGate) Evaluate(_ context.Context, _ session.SessionID, _ session.PermissionMode, call session.ToolCall, _ tool.WorkspaceReader) governance.PermissionDecision {
	if !g.hook && call.ID == "second" && !g.asked {
		g.asked = true
		return governance.PermissionDecision{Effect: governance.Ask, Reason: "second sibling needs approval"}
	}
	return governance.PermissionDecision{Effect: governance.Allow}
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

func TestReadBatchSiblingReviewBudgetsStartAtOwnPreparation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var executions atomic.Int32
		catalog := tool.NewCatalog()
		catalog.MustRegister(budgetReadTool{executions: &executions})
		reviewer := &siblingBudgetReviewer{}
		preparer := &deadlineEvidencePreparer{delay: 50 * time.Second}
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
		if len(reviewer.called) != 1 || reviewer.called[0] != "second" || timeouts != 1 || executions.Load() != 2 || preparer.calls != 2 {
			t.Fatalf("called=%v timeouts=%d executions=%d prepares=%d", reviewer.called, timeouts, executions.Load(), preparer.calls)
		}
	})
}

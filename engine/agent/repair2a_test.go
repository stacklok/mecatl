package agent_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/localauthority"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type repairPolicy struct {
	mu     sync.Mutex
	effect governance.Effect
}

func (p *repairPolicy) Evaluate(context.Context, session.SessionID, session.PermissionMode, session.ToolCall, tool.WorkspaceReader) governance.PermissionDecision {
	p.mu.Lock()
	defer p.mu.Unlock()
	return governance.PermissionDecision{Effect: p.effect, Reason: "repair policy"}
}
func (*repairPolicy) Learn(session.SessionID, session.ToolCall) {}
func (p *repairPolicy) set(effect governance.Effect)            { p.mu.Lock(); p.effect = effect; p.mu.Unlock() }

type repairReviewer struct {
	mu         sync.Mutex
	calls      int
	assessment agent.ReviewAssessment
}

func (*repairReviewer) GuardrailReviewPolicy(_ string, job agent.ReviewJob, _ bool) (bool, bool) {
	return job == agent.ReviewJobAction, true
}
func (r *repairReviewer) Review(context.Context, agent.ToolReviewRequest, agent.ReviewEvidenceSource) (agent.ToolReviewResult, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return agent.ToolReviewResult{Assessment: r.assessment}, nil
}
func (r *repairReviewer) count() int { r.mu.Lock(); defer r.mu.Unlock(); return r.calls }

type approvalHook struct{ calls int }

func (h *approvalHook) Run(_ context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	if ev.Phase == governance.PhasePreToolUse {
		h.calls++
		return governance.HookOutcome{Block: true, AskApproval: true, Message: "operator guardrail"}, nil
	}
	return governance.HookOutcome{}, nil
}

func TestRepair2AApprovalOccurrencesAreUniqueInBothOrders(t *testing.T) {
	for _, tc := range []struct {
		name         string
		initial      governance.Effect
		beforeSecond func(*repairPolicy)
	}{
		{name: "ordinary_then_contextual", initial: governance.Ask},
		{name: "contextual_then_reauthorization", initial: governance.Allow, beforeSecond: func(p *repairPolicy) { p.set(governance.Ask) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := &repairPolicy{effect: tc.initial}
			reviewer := &repairReviewer{assessment: agent.ReviewProhibited}
			executions := 0
			read := &fakeTool{name: "Read", readOnly: true, exec: func(context.Context, session.ToolCall, tool.Workspace) (session.ToolResult, error) {
				executions++
				return session.NewToolResult("c1", "ok"), nil
			}}
			cat := tool.NewCatalog()
			cat.MustRegister(read)
			eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("c1", "Read", []byte(`{"path":"a"}`))), mockllm.TextTurn("done")), Catalog: cat, Policy: policy, ToolReviewer: reviewer, Interactive: true})
			sess := newSession(t, session.Limits{})
			run := eng.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "read"})
			var ids []string
			for ev := range run.Events() {
				if ev.Type != session.EvPermissionAsk {
					continue
				}
				ids = append(ids, ev.Ask.AskID)
				if len(ids) == 1 && tc.beforeSecond != nil {
					tc.beforeSecond(policy)
				}
				if len(ids) == 2 {
					_ = run.Approve(ev.Ask.AskID, session.VerdictDeny)
				} else {
					_ = run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
				}
			}
			if len(ids) != 2 || ids[0] == ids[1] {
				t.Fatalf("approval ids = %v, want two unique occurrences", ids)
			}
			if sess.Counters.ToolCalls != 1 {
				t.Fatalf("tool counter = %d, want original 1", sess.Counters.ToolCalls)
			}
			if executions != 0 {
				t.Fatalf("executions = %d, want denied contextual action", executions)
			}
		})
	}
}

func TestRepair2AHookApprovalStillRunsContextualReview(t *testing.T) {
	policy := &repairPolicy{effect: governance.Allow}
	reviewer := &repairReviewer{assessment: agent.ReviewProhibited}
	hook := &approvalHook{}
	executions := 0
	read := &fakeTool{name: "Read", readOnly: true, exec: func(context.Context, session.ToolCall, tool.Workspace) (session.ToolResult, error) {
		executions++
		return session.NewToolResult("c1", "unsafe"), nil
	}}
	cat := tool.NewCatalog()
	cat.MustRegister(read)
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("c1", "Read", []byte(`{"path":"a"}`))), mockllm.TextTurn("done")), Catalog: cat, Policy: policy, Hooks: hook, ToolReviewer: reviewer, Interactive: true})
	run := eng.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "read"})
	asks := 0
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk {
			asks++
			verdict := session.VerdictAllowOnce
			if asks == 2 {
				verdict = session.VerdictDeny
			}
			_ = run.Approve(ev.Ask.AskID, verdict)
		}
	}
	if asks != 2 || hook.calls != 1 || reviewer.count() != 1 || executions != 0 {
		t.Fatalf("asks=%d hook=%d reviews=%d executions=%d", asks, hook.calls, reviewer.count(), executions)
	}
}

func TestRepair2ABoundAuthorityDeniesBeforeReviewOrEvidence(t *testing.T) {
	policy := &repairPolicy{effect: governance.Allow}
	reviewer := &repairReviewer{assessment: agent.ReviewAcceptable}
	executions := 0
	grep := &fakeTool{name: "Grep", readOnly: true, exec: func(context.Context, session.ToolCall, tool.Workspace) (session.ToolResult, error) {
		executions++
		return session.NewToolResult("g1", "x"), nil
	}}
	cat := tool.NewCatalog()
	cat.MustRegister(grep)
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("g1", "Grep", []byte(`{"path":"b","pattern":"x"}`))), mockllm.TextTurn("done")), Catalog: cat, Policy: policy, AuthorityEvaluator: localauthority.New(), ToolReviewer: reviewer})
	sess := newSession(t, session.Limits{})
	if err := sess.BindAuthority(session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}, FileSystem: true}, Provenance: "test"}); err != nil {
		t.Fatal(err)
	}
	drain(eng.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "grep"}))
	if reviewer.count() != 0 || executions != 0 {
		t.Fatalf("reviews=%d executions=%d, authority denial must precede both", reviewer.count(), executions)
	}
}

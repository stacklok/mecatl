package agent_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type changingPermissionPolicy struct {
	mu       sync.Mutex
	decision governance.PermissionDecision
}

func (p *changingPermissionPolicy) Evaluate(context.Context, session.SessionID, session.PermissionMode, session.ToolCall, tool.WorkspaceReader) governance.PermissionDecision {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.decision
}

func (*changingPermissionPolicy) Learn(session.SessionID, session.ToolCall) {}

func (p *changingPermissionPolicy) set(decision governance.PermissionDecision) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.decision = decision
}

func newProhibitedActionReviewer() agent.ToolReviewer {
	return toolReviewerFunc(func(context.Context, agent.ToolReviewRequest, agent.ReviewEvidenceSource) (agent.ToolReviewResult, error) {
		return agent.ToolReviewResult{Assessment: agent.ReviewProhibited}, nil
	})
}

func TestContextualActionApprovalReauthorizesNewPermissionAsk(t *testing.T) {
	policy := &changingPermissionPolicy{decision: governance.PermissionDecision{Effect: governance.Allow, Reason: "initial allow"}}
	executions := 0
	act := &fakeTool{name: "Act", exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		executions++
		return session.NewToolResult(call.ID, "ok"), nil
	}}
	cat := tool.NewCatalog()
	cat.MustRegister(act)
	eng := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("call", "Act", []byte(`{"value":"x"}`))), mockllm.TextTurn("done")),
		Catalog: cat, Policy: policy, ToolReviewer: newProhibitedActionReviewer(), Interactive: true,
	})
	run := eng.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "act"})
	guardrailAsks, permissionAsks := 0, 0
	for ev := range run.Events() {
		if ev.Type != session.EvPermissionAsk || ev.Ask == nil {
			continue
		}
		if ev.Ask.Guardrail != nil {
			guardrailAsks++
			policy.set(governance.PermissionDecision{Effect: governance.Ask, Reason: "policy changed"})
		} else {
			permissionAsks++
		}
		run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
	}
	if guardrailAsks != 1 || permissionAsks != 1 || executions != 1 {
		t.Fatalf("guardrail asks=%d permission asks=%d executions=%d", guardrailAsks, permissionAsks, executions)
	}
}

func TestContextualActionPermissionChangeDuringReapprovalStopsWithoutLoop(t *testing.T) {
	policy := &changingPermissionPolicy{decision: governance.PermissionDecision{Effect: governance.Allow}}
	executions := 0
	act := &fakeTool{name: "Act", exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		executions++
		return session.NewToolResult(call.ID, "unexpected"), nil
	}}
	cat := tool.NewCatalog()
	cat.MustRegister(act)
	eng := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("call", "Act", nil)), mockllm.TextTurn("done")),
		Catalog: cat, Policy: policy, ToolReviewer: newProhibitedActionReviewer(), Interactive: true,
	})
	run := eng.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "act"})
	permissionAsks := 0
	for ev := range run.Events() {
		if ev.Type != session.EvPermissionAsk || ev.Ask == nil {
			continue
		}
		if ev.Ask.Guardrail != nil {
			policy.set(governance.PermissionDecision{Effect: governance.Ask, Reason: "new approval required"})
		} else {
			permissionAsks++
			policy.set(governance.PermissionDecision{Effect: governance.Deny, Reason: "changed while waiting"})
		}
		run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
	}
	if permissionAsks != 1 || executions != 0 {
		t.Fatalf("permission asks=%d executions=%d", permissionAsks, executions)
	}
}

func TestContextualActionApprovalHonorsNewPermissionDeny(t *testing.T) {
	policy := &changingPermissionPolicy{decision: governance.PermissionDecision{Effect: governance.Allow}}
	executions := 0
	act := &fakeTool{name: "Act", exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		executions++
		return session.NewToolResult(call.ID, "unexpected"), nil
	}}
	cat := tool.NewCatalog()
	cat.MustRegister(act)
	eng := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("call", "Act", nil)), mockllm.TextTurn("done")),
		Catalog: cat, Policy: policy, ToolReviewer: newProhibitedActionReviewer(), Interactive: true,
	})
	run := eng.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "act"})
	permissionAsks := 0
	for ev := range run.Events() {
		if ev.Type != session.EvPermissionAsk || ev.Ask == nil {
			continue
		}
		if ev.Ask.Guardrail != nil {
			policy.set(governance.PermissionDecision{Effect: governance.Deny, Reason: "new deny"})
		} else {
			permissionAsks++
		}
		run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
	}
	if permissionAsks != 0 || executions != 0 {
		t.Fatalf("permission asks=%d executions=%d", permissionAsks, executions)
	}
}

func TestContextualActionApprovalDoesNotRepeatOriginalPermissionAsk(t *testing.T) {
	policy := &changingPermissionPolicy{decision: governance.PermissionDecision{Effect: governance.Ask, Reason: "approval required"}}
	executions := 0
	act := &fakeTool{name: "Act", exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		executions++
		return session.NewToolResult(call.ID, "ok"), nil
	}}
	cat := tool.NewCatalog()
	cat.MustRegister(act)
	eng := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("call", "Act", nil)), mockllm.TextTurn("done")),
		Catalog: cat, Policy: policy, ToolReviewer: newProhibitedActionReviewer(), Interactive: true,
	})
	run := eng.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "act"})
	guardrailAsks, permissionAsks := 0, 0
	for ev := range run.Events() {
		if ev.Type != session.EvPermissionAsk || ev.Ask == nil {
			continue
		}
		if ev.Ask.Guardrail != nil {
			guardrailAsks++
		} else {
			permissionAsks++
		}
		run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
	}
	if guardrailAsks != 1 || permissionAsks != 1 || executions != 1 {
		t.Fatalf("guardrail asks=%d permission asks=%d executions=%d", guardrailAsks, permissionAsks, executions)
	}
}

type evidencePermissionPolicy struct{}

func (evidencePermissionPolicy) Evaluate(_ context.Context, _ session.SessionID, _ session.PermissionMode, call session.ToolCall, _ tool.WorkspaceReader) governance.PermissionDecision {
	if call.Name == "Read" {
		return governance.PermissionDecision{Effect: governance.Deny, Reason: "explicit Read deny"}
	}
	return governance.PermissionDecision{Effect: governance.Allow}
}
func (evidencePermissionPolicy) Learn(session.SessionID, session.ToolCall) {}

type permissionCheckingEvidencePreparer struct {
	authorizeErr error
}

func (p *permissionCheckingEvidencePreparer) PrepareReviewEvidence(ctx context.Context, prep agent.ReviewEvidencePreparation) (agent.PreparedReviewEvidence, error) {
	p.authorizeErr = prep.Authorize(ctx, session.NewToolCall(prep.Request.EffectiveCall.ID, "Read", []byte(`{"path":"secret.sh"}`)))
	return agent.PreparedReviewEvidence{Complete: p.authorizeErr == nil}, nil
}

func TestContextualEvidenceRequiresOrdinaryReadPermission(t *testing.T) {
	executions := 0
	act := &fakeTool{name: tool.ShellToolName, exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		executions++
		return session.NewToolResult(call.ID, "unexpected"), nil
	}}
	read := &fakeTool{name: "Read", readOnly: true}
	cat := tool.NewCatalog()
	cat.MustRegister(act)
	cat.MustRegister(read)
	preparer := &permissionCheckingEvidencePreparer{}
	eng := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("call", tool.ShellToolName, []byte(`{"command":"./secret.sh"}`))), mockllm.TextTurn("done")),
		Catalog: cat, Policy: evidencePermissionPolicy{}, ToolReviewer: toolReviewerFunc(func(context.Context, agent.ToolReviewRequest, agent.ReviewEvidenceSource) (agent.ToolReviewResult, error) {
			return agent.ToolReviewResult{Assessment: agent.ReviewAcceptable}, nil
		}), ReviewEvidencePreparer: preparer,
	})
	for range eng.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "act"}).Events() {
	}
	if preparer.authorizeErr == nil || executions != 1 {
		t.Fatalf("evidence authorization error=%v executions=%d", preparer.authorizeErr, executions)
	}
}

func TestContextualReadBatchReauthorizationPreservesConcurrency(t *testing.T) {
	policy := &changingPermissionPolicy{decision: governance.PermissionDecision{Effect: governance.Allow}}
	execEntered := make(chan struct{}, 2)
	execRelease := make(chan struct{})
	mk := func(name string) *fakeTool {
		return &fakeTool{name: name, readOnly: true, exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			execEntered <- struct{}{}
			<-execRelease
			return session.NewToolResult(call.ID, "ok"), nil
		}}
	}
	cat := tool.NewCatalog()
	cat.MustRegister(mk("LookupA"))
	cat.MustRegister(mk("LookupB"))
	eng := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(mockllm.ToolCallTurn(
			session.NewToolCall("one", "LookupA", []byte(`{"query":"a"}`)),
			session.NewToolCall("two", "LookupB", []byte(`{"query":"b"}`)),
		), mockllm.TextTurn("done")),
		Catalog: cat, Policy: policy, ToolReviewer: newProhibitedActionReviewer(), Interactive: true,
	})
	run := eng.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "inspect"})
	done := make(chan struct{})
	go func() {
		<-execEntered
		<-execEntered
		close(execRelease)
		close(done)
	}()
	guardrailAsks, permissionAsks := 0, 0
	for ev := range run.Events() {
		if ev.Type != session.EvPermissionAsk || ev.Ask == nil {
			continue
		}
		if ev.Ask.Guardrail != nil {
			guardrailAsks++
			policy.set(governance.PermissionDecision{Effect: governance.Ask, Reason: "batch policy changed"})
		} else {
			permissionAsks++
		}
		run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
	}
	<-done
	if guardrailAsks != 2 || permissionAsks != 2 {
		t.Fatalf("guardrail asks=%d permission asks=%d", guardrailAsks, permissionAsks)
	}
}

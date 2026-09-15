package agent_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
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

func TestContextualActionApprovalReauthorizesDependencyReadsBeforeBackendAccess(t *testing.T) {
	for _, tc := range []struct {
		name          string
		after         governance.Effect
		wantReads     int
		wantExecution int
	}{
		{"deny", governance.Deny, 1, 0},
		{"ask", governance.Ask, 1, 0},
		{"unchanged_allow", governance.Allow, 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			base := memfs.NewWorkspace("/ws")
			if err := base.Write(ctx, "script.sh", []byte("echo safe")); err != nil {
				t.Fatal(err)
			}
			workspace := &countingBoundedWorkspace{Workspace: base}
			env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, workspace, memledger.New(), nil)
			policy := &perToolChangingPolicy{effects: map[string]governance.Effect{"Read": governance.Allow, tool.ShellToolName: governance.Allow}}
			executions := 0
			cat := tool.NewCatalog()
			cat.MustRegister(&fakeTool{name: "Read", readOnly: true})
			cat.MustRegister(&fakeTool{name: tool.ShellToolName, exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				executions++
				return session.NewToolResult(call.ID, "unexpected"), nil
			}})
			eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("shell", tool.ShellToolName, []byte(`{"command":"./script.sh"}`))), mockllm.TextTurn("done")), Catalog: cat, Policy: policy, ToolReviewer: newProhibitedActionReviewer(), Interactive: true})
			run := eng.Run(ctx, newSession(t, session.Limits{}), env, agent.RunRequest{Text: "run"})
			ordinaryAsks := 0
			for ev := range run.Events() {
				if ev.Type != session.EvPermissionAsk || ev.Ask == nil {
					continue
				}
				if ev.Ask.Guardrail != nil {
					policy.set("Read", tc.after)
				} else {
					ordinaryAsks++
				}
				if err := run.ResolveApproval(agent.ApprovalResolution{AskID: ev.Ask.AskID, ReviewID: reviewID(ev.Ask), Kind: reviewKind(ev.Ask), Verdict: session.VerdictAllowOnce}); err != nil {
					t.Fatal(err)
				}
			}
			if workspace.bounded != tc.wantReads || executions != tc.wantExecution || ordinaryAsks != 0 {
				t.Fatalf("bounded reads=%d want=%d executions=%d want=%d ordinary asks=%d", workspace.bounded, tc.wantReads, executions, tc.wantExecution, ordinaryAsks)
			}
		})
	}
}

type countingBoundedWorkspace struct {
	tool.Workspace
	bounded int
}

func (w *countingBoundedWorkspace) ReadVersionBounded(ctx context.Context, path string, maxBytes int64) ([]byte, tool.FileVersion, error) {
	w.bounded++
	return w.Workspace.(tool.BoundedWorkspaceReader).ReadVersionBounded(ctx, path, maxBytes)
}

type perToolChangingPolicy struct {
	mu      sync.Mutex
	effects map[string]governance.Effect
}

func (p *perToolChangingPolicy) Evaluate(_ context.Context, _ session.SessionID, _ session.PermissionMode, call session.ToolCall, _ tool.WorkspaceReader) governance.PermissionDecision {
	p.mu.Lock()
	defer p.mu.Unlock()
	return governance.PermissionDecision{Effect: p.effects[call.Name], Reason: "test policy"}
}
func (*perToolChangingPolicy) Learn(session.SessionID, session.ToolCall) {}
func (p *perToolChangingPolicy) set(name string, effect governance.Effect) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.effects[name] = effect
}

func reviewID(ask *session.PendingAsk) string {
	if ask.Guardrail != nil {
		return ask.Guardrail.ReviewID
	}
	return ""
}
func reviewKind(ask *session.PendingAsk) session.GuardrailApprovalKind {
	if ask.Guardrail != nil {
		return ask.Guardrail.Kind
	}
	return ""
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

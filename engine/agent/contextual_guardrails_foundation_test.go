package agent_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestValidateApprovalResolutionClassifiesInvalidIntent(t *testing.T) {
	ordinary := session.PendingAsk{AskID: "ordinary", Origin: session.ApprovalOriginPermission}
	if err := agent.ValidateApprovalResolution(ordinary, agent.ApprovalResolution{AskID: "other", Verdict: session.VerdictDeny}); !errors.Is(err, agent.ErrApprovalIntentMismatch) {
		t.Fatalf("identity mismatch = %v", err)
	}
	if err := agent.ValidateApprovalResolution(ordinary, agent.ApprovalResolution{AskID: ordinary.AskID, Verdict: session.ApprovalVerdict(99)}); !errors.Is(err, agent.ErrApprovalGrantIneligible) {
		t.Fatalf("unknown ordinary verdict = %v", err)
	}
	invalid := []session.PendingAsk{
		{AskID: "unknown", Origin: session.ApprovalOriginUnknown},
		{AskID: "permission-scope", Origin: session.ApprovalOriginPermission, Guardrail: &session.GuardrailPendingScope{ReviewID: "r", Kind: session.GuardrailApprovalAction}},
		{AskID: "plan-scope", Origin: session.ApprovalOriginPlan, Guardrail: &session.GuardrailPendingScope{ReviewID: "r", Kind: session.GuardrailApprovalAction}},
		{AskID: "future", Origin: session.ApprovalOriginHookGuardrail, Guardrail: &session.GuardrailPendingScope{ReviewID: "r", Kind: "future"}},
	}
	for _, ask := range invalid {
		kind := session.GuardrailApprovalKind("")
		if ask.Guardrail != nil {
			kind = ask.Guardrail.Kind
		}
		err := agent.ValidateApprovalResolution(ask, agent.ApprovalResolution{AskID: ask.AskID, ReviewID: "r", Kind: kind, Verdict: session.VerdictDeny})
		if !errors.Is(err, agent.ErrApprovalUnsupported) {
			t.Errorf("ask %+v error = %v", ask, err)
		}
	}
	for _, kind := range []session.GuardrailApprovalKind{session.GuardrailApprovalAction, session.GuardrailApprovalResultRelease} {
		ask := session.PendingAsk{AskID: string(kind), Origin: session.ApprovalOriginHookGuardrail, Guardrail: &session.GuardrailPendingScope{ReviewID: "r", Kind: kind}}
		if err := agent.ValidateApprovalResolution(ask, agent.ApprovalResolution{AskID: ask.AskID, ReviewID: "r", Kind: kind, Verdict: session.ApprovalVerdict(99)}); !errors.Is(err, agent.ErrApprovalGrantIneligible) {
			t.Errorf("unknown %s verdict = %v", kind, err)
		}
		if err := agent.ValidateApprovalResolution(ask, agent.ApprovalResolution{AskID: ask.AskID, ReviewID: "r", Kind: kind, Verdict: session.VerdictAllowOnce}); err != nil {
			t.Errorf("valid %s approval: %v", kind, err)
		}
	}
}

func TestUnknownApprovalVerdictLeavesLiveOrdinaryAskPending(t *testing.T) {
	policy := permpolicy.NewPolicy(nil, permstore.New())
	var executions atomic.Int64
	recorder := &resultRecorder{}
	write := &fakeTool{name: "Write", exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		executions.Add(1)
		return session.NewToolResult(call.ID, "ok"), nil
	}}
	eng := newEngine(agent.Deps{
		LLM:              mockllm.New(mockllm.ToolCallTurn(toolCall("w-live", "Write", `{}`)), mockllm.TextTurn("done")),
		Catalog:          catalogWith(t, write),
		Policy:           policy,
		ToolCallRecorder: recorder,
	})
	run := eng.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "write"})
	for ev := range run.Events() {
		if ev.Type != session.EvPermissionAsk || ev.Ask == nil {
			continue
		}
		err := run.ResolveApproval(agent.ApprovalResolution{AskID: ev.Ask.AskID, Verdict: session.ApprovalVerdict(99)})
		if !errors.Is(err, agent.ErrApprovalGrantIneligible) {
			t.Fatalf("unknown ResolveApproval verdict = %v", err)
		}
		if err := run.Approve(ev.Ask.AskID, session.ApprovalVerdict(99)); !errors.Is(err, agent.ErrApprovalGrantIneligible) {
			t.Fatalf("unknown Approve verdict = %v", err)
		}
		if err := run.ValidateRemoteApprovalIntent(ev.Ask.AskID, "", "", session.ApprovalVerdict(99)); !errors.Is(err, agent.ErrApprovalGrantIneligible) {
			t.Fatalf("unknown ValidateRemoteApprovalIntent verdict = %v", err)
		}
		recorder.mu.Lock()
		recorded := len(recorder.results)
		recorder.mu.Unlock()
		if executions.Load() != 0 || recorded != 0 {
			t.Fatalf("unknown verdict executed or audited: executions=%d audit=%d", executions.Load(), recorded)
		}
		if err := run.ResolveApproval(agent.ApprovalResolution{AskID: ev.Ask.AskID, Verdict: session.VerdictAllowOnce}); err != nil {
			t.Fatalf("valid follow-up verdict: %v", err)
		}
	}
	if executions.Load() != 1 {
		t.Fatalf("valid follow-up executions=%d, want 1", executions.Load())
	}
	recorder.mu.Lock()
	recorded := len(recorder.results)
	recorder.mu.Unlock()
	if recorded != 1 {
		t.Fatalf("valid follow-up audit=%d, want 1", recorded)
	}
}

func TestUnknownApprovalVerdictLeavesRestoredOrdinaryAskPending(t *testing.T) {
	policy := permpolicy.NewPolicy(nil, permstore.New())
	first := &fakeTool{name: "Write", exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		return session.NewToolResult(call.ID, "unexpected"), nil
	}}
	sess := session.New("unknown-restored", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	askID, restored := driveToAwaiting(t, newEngine(agent.Deps{
		LLM: mockllm.New(mockllm.ToolCallTurn(toolCall("w-restored", "Write", `{}`))), Catalog: catalogWith(t, first), Policy: policy,
	}), sess, agent.MemEnv("/ws"), "write")

	var executions atomic.Int64
	recorder := &resultRecorder{}
	write := &fakeTool{name: "Write", exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		executions.Add(1)
		return session.NewToolResult(call.ID, "ok"), nil
	}}
	eng := newEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: catalogWith(t, write), Policy: policy, ToolCallRecorder: recorder,
	})
	invalidEvents := resumeEvents(eng.ResumeApproval(context.Background(), restored, agent.MemEnv("/ws"), askID, session.ApprovalVerdict(99)))
	if result := lastResult(t, invalidEvents); result.Stop != session.StopError || !strings.Contains(result.Error, agent.ErrApprovalGrantIneligible.Error()) {
		t.Fatalf("invalid resume result = %+v", result)
	}
	pending, ok := restored.PendingAsk()
	if !ok || pending.AskID != askID || restored.State != session.StateAwaiting {
		t.Fatalf("invalid resume consumed pending ask: state=%q pending=%+v ok=%v", restored.State, pending, ok)
	}
	recorder.mu.Lock()
	recorded := len(recorder.results)
	recorder.mu.Unlock()
	if executions.Load() != 0 || recorded != 0 {
		t.Fatalf("invalid resume executed or audited: executions=%d audit=%d", executions.Load(), recorded)
	}
	validEvents := resumeEvents(eng.ResumeApproval(context.Background(), restored, agent.MemEnv("/ws"), askID, session.VerdictAllowOnce))
	if result := lastResult(t, validEvents); result.Stop != session.StopEndTurn {
		t.Fatalf("valid follow-up result = %+v", result)
	}
	recorder.mu.Lock()
	recorded = len(recorder.results)
	recorder.mu.Unlock()
	if executions.Load() != 1 || recorded != 1 {
		t.Fatalf("valid follow-up executions=%d audit=%d, want 1/1", executions.Load(), recorded)
	}
}

func TestADR_0363_ContextualGuardrails_Scenario5_OldClientSafety(t *testing.T) {
	policy := permpolicy.NewPolicy(nil, permstore.New())
	sess := session.New("old-client", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	first := &fakeTool{name: "Write", exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		return session.NewToolResult(in.ID, "unexpected"), nil
	}}
	e1 := newEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(toolCall("w1", "Write", `{"path":"a.go"}`))), Catalog: catalogWith(t, first), Policy: policy})
	askID, restored := driveToAwaiting(t, e1, sess, agent.MemEnv("/ws"), "go")
	snap, err := sessnap.Of(restored)
	if err != nil {
		t.Fatal(err)
	}
	snap.Pending.Origin = session.ApprovalOriginUnknown
	snap.Pending.Guardrail = &session.GuardrailPendingScope{Kind: session.GuardrailApprovalKind("future")}
	legacy, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	var ran atomic.Int64
	second := &fakeTool{name: "Write", exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		ran.Add(1)
		return session.NewToolResult(in.ID, "unexpected"), nil
	}}
	e2 := newEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("after")), Catalog: catalogWith(t, second), Policy: policy})
	events := resumeEvents(e2.ResumeApproval(context.Background(), legacy, agent.MemEnv("/ws"), askID, session.VerdictAllowAlways))
	if ran.Load() != 0 {
		t.Fatal("unknown origin/kind executed the tool")
	}
	if len(events) == 0 || lastResult(t, events).Stop != session.StopEndTurn {
		t.Fatalf("unexpected events: %+v", events)
	}
	if got, _ := resultsFor(events, "w1"); got != 0 {
		t.Fatal("unknown origin/kind produced a successful result")
	}
}

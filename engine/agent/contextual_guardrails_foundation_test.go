package agent_test

import (
	"context"
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

func TestADR_0342_ContextualGuardrails_Scenario5_OldClientSafety(t *testing.T) {
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

package app

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// runYoloGuardedGrep drives one guarded Grep through the production reviewer in a
// yolo session and returns the checker's call count and the guardrail events.
func runYoloGuardedGrep(t *testing.T, yoloAdvisory bool) (int, []session.Event) {
	t.Helper()
	verdict := `{"assessment":"prohibited","concerns":[{"ref":"c1","category":"redirection","rationale":"r","source_ref":""}],"evidence":[],"missing_evidence":[]}`
	checker := mockllm.New(mockllm.TextTurn(verdict), mockllm.TextTurn(verdict), mockllm.TextTurn(verdict), mockllm.TextTurn(verdict))
	cfg := Config{
		UseMock:                true,
		GuardrailsModel:        "checker-model",
		GuardrailsRules:        []GuardrailRule{{Match: "Grep", Phases: []string{"pre", "post"}, Mode: "block"}},
		GuardrailsYoloAdvisory: yoloAdvisory,
	}
	reviewer := buildGuardrailsActionReviewer(cfg, nil, checker, providerMock, nil)
	if reviewer == nil {
		t.Fatal("configured guardrail factory returned nil reviewer")
	}
	catalog := tool.NewCatalog()
	catalog.MustRegister(stubTool{name: "Grep"})
	deps := agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("guarded", "Grep", []byte(`{}`))), mockllm.TextTurn("done")),
		Catalog: catalog, Model: "parent-model",
		Policy: permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil),
	}
	attachGuardrailReviewer(&deps, reviewer, nil)
	sess := session.New("yolo", session.ModeYolo,
		session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"},
		session.Limits{}, time.Unix(1, 0))
	var hooks []session.Event
	for ev := range agent.NewEngine(deps).Run(context.Background(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "go"}).Events() {
		if ev.Hook != nil && ev.Hook.Guardrail != nil {
			hooks = append(hooks, ev)
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.IsError {
			t.Fatalf("yolo tool result was blocked or withheld: %+v", ev.ToolResult)
		}
	}
	return checker.Calls(), hooks
}

func TestGuardrailsYoloSkipsCheckerByDefault(t *testing.T) {
	calls, hooks := runYoloGuardedGrep(t, false)
	if calls != 0 || len(hooks) != 0 {
		t.Fatalf("checker calls=%d guardrail events=%d, want none without guardrails.yoloAdvisory", calls, len(hooks))
	}
}

func TestGuardrailsYoloAdvisoryReviewsInBackground(t *testing.T) {
	calls, hooks := runYoloGuardedGrep(t, true)
	if calls == 0 {
		t.Fatal("guardrails.yoloAdvisory did not reach the checker")
	}
	if len(hooks) != 2 {
		t.Fatalf("guardrail events = %d, want action and inbound advisories", len(hooks))
	}
	for _, ev := range hooks {
		if ev.Hook.Guardrail.Disposition != "pass_advisory" {
			t.Fatalf("disposition = %q, want pass_advisory", ev.Hook.Guardrail.Disposition)
		}
	}
}

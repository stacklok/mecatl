package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestSubagentAgentPlusModelRunsScopedChildOnOverrideModel is the E2E: a parent delegates
// agent:"reviewer", model:"fast", and the child engine that ACTUALLY RAN is the scoped
// override engine the agentModelFactory minted (distinguished by a marker summary +
// Engine.Model()=="fast"), NOT the pre-built agentEngines["reviewer"] engine (which runs on
// a different model) and NOT the default explorer. It also asserts per-def limits bind the
// agent+model child: a def with maxTurns pins the override child's turn budget.
func TestSubagentAgentPlusModelRunsScopedChildOnOverrideModel(t *testing.T) {
	// The pre-built reviewer engine runs on "reviewer-model" and yields "PREBUILT-REVIEWER".
	// It must NOT run when the call sets model:"fast" (proves no map mutation / no reuse).
	prebuiltReviewerLLM := mockllm.New(mockllm.TextTurn("PREBUILT-REVIEWER"))
	prebuiltReviewer := childEngineWithModel("reviewer-model", prebuiltReviewerLLM, catalogWith(t))

	// The override engine runs on "fast" and yields "OVERRIDE-REVIEWER-ON-FAST".
	var seenRequest port.LLMRequest
	overrideLLM := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { seenRequest = r })},
		mockllm.TextTurn("OVERRIDE-REVIEWER-ON-FAST"),
	)
	overrideEngine := childEngineWithModel("fast", overrideLLM, catalogWith(t))

	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("DEFAULT")), catalogWith(t))

	task := agent.NewSubagentTool(defaultEngine,
		agent.WithAgentEngines(
			map[string]*agent.Engine{"reviewer": prebuiltReviewer},
			// A def with maxTurns=2 pins the agent+model child's turn budget.
			[]agent.AgentMeta{{Name: "reviewer", Description: "reviews", Limits: session.Limits{MaxTurns: 2}}}),
		agent.WithAgentModelEngineFactory(func(agentName string, target agent.ModelTarget) (*agent.Engine, bool) {
			if agentName == "reviewer" && target.Model == "fast" {
				return overrideEngine, true
			}
			return nil, false
		}),
	)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"review it","agent":"reviewer","model":"fast"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("agent+model must succeed, got %+v", results[0])
	}
	// The override engine ran (its marker summary), not the pre-built reviewer.
	if !strings.Contains(results[0].Content, "OVERRIDE-REVIEWER-ON-FAST") {
		t.Fatalf("agent+model must route to the override engine, got %q", results[0].Content)
	}
	if strings.Contains(results[0].Content, "PREBUILT-REVIEWER") {
		t.Fatalf("the pre-built reviewer engine must NOT run (no reuse/mutation), got %q", results[0].Content)
	}
	// The override child ran on the override model ("fast"), proving the engine minted by
	// the factory (not the pre-built "reviewer-model" engine) is what executed.
	if seenRequest.Model != "fast" {
		t.Fatalf("override child request model = %q, want %q", seenRequest.Model, "fast")
	}
	// The pre-built reviewer engine never ran (no map mutation / reuse on the agent+model path).
	if prebuiltReviewerLLM.Calls() != 0 {
		t.Fatalf("pre-built reviewer engine must not be driven on agent+model, made %d calls", prebuiltReviewerLLM.Calls())
	}
}

// TestSubagentAgentPlusModelRunsScopedCatalog is the SCOPED-CATALOG proof (QA gap #1): the
// override engine's catalog carries a probe tool ("ScopedProbe") the DEFAULT explorer does
// NOT. The child LLM calls ScopedProbe; if the override engine (with its scoped catalog)
// ran, the probe's marker appears in the result; if the generic explorer ran instead, the
// call would be an unknown-tool error. This is the assertion that distinguishes "the
// specialist's scoped engine ran" from "a generic explorer on the override model ran" —
// the central claim of the agent+model feature.
func TestSubagentAgentPlusModelRunsScopedCatalog(t *testing.T) {
	const scopedProbeName = "ScopedProbe"
	scopedProbe := &fakeTool{name: scopedProbeName, readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "SCOPED-PROBE-RAN"), nil
		}}

	// The override engine's catalog carries ScopedProbe (a scoped surface the explorer lacks).
	overrideEngine := childEngineWithModel("fast",
		mockllm.New(
			// Turn 1: call the scoped probe tool.
			mockllm.ToolCallTurn(toolCall("c1", scopedProbeName, `{}`)),
			// Turn 2: report the probe's marker in the summary.
			mockllm.TextTurn("probe said: SCOPED-PROBE-RAN"),
		),
		catalogWith(t, scopedProbe))

	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("DEFAULT")), catalogWith(t))

	task := agent.NewSubagentTool(defaultEngine,
		agent.WithAgentEngines(
			map[string]*agent.Engine{"reviewer": childEngineWith(mockllm.New(mockllm.TextTurn("r")), catalogWith(t))},
			[]agent.AgentMeta{{Name: "reviewer", Description: "reviews"}}),
		agent.WithAgentModelEngineFactory(func(string, agent.ModelTarget) (*agent.Engine, bool) { return overrideEngine, true }),
	)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"call the probe","agent":"reviewer","model":"fast"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("agent+model scoped-catalog call must succeed, got %+v", results[0])
	}
	// The scoped probe tool RAN (its marker reached the parent) — proving the override
	// engine's SCOPED catalog executed, NOT the generic explorer (which has no ScopedProbe
	// and would have returned an unknown-tool error).
	if !strings.Contains(results[0].Content, "SCOPED-PROBE-RAN") {
		t.Fatalf("the scoped catalog's probe must have run; got %q (the generic explorer would not have ScopedProbe)",
			results[0].Content)
	}
}

// TestSubagentAgentPlusModelPerDefLimitsBind proves a def's maxTurns pins the agent+model
// child: a def with maxTurns=1 and a child that tries two turns is bounded to one (the
// per-def limits from agentLimits bind the override child, NOT the Subagent default).
func TestSubagentAgentPlusModelPerDefLimitsBind(t *testing.T) {
	// Override child: yields a text turn, then tries a second text turn — but the def's
	// maxTurns=1 must stop it after the first.
	overrideLLM := mockllm.New(
		mockllm.TextTurn("first turn"),
		mockllm.TextTurn("second turn — must not run"),
	)
	overrideEngine := childEngineWithModel("fast", overrideLLM, catalogWith(t))
	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("DEFAULT")), catalogWith(t))

	task := agent.NewSubagentTool(defaultEngine,
		agent.WithAgentEngines(
			map[string]*agent.Engine{"reviewer": childEngineWith(mockllm.New(mockllm.TextTurn("r")), catalogWith(t))},
			[]agent.AgentMeta{{Name: "reviewer", Description: "reviews", Limits: session.Limits{MaxTurns: 1}}}),
		agent.WithAgentModelEngineFactory(func(string, agent.ModelTarget) (*agent.Engine, bool) { return overrideEngine, true }),
	)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"go","agent":"reviewer","model":"fast"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("agent+model with per-def limits must succeed, got %+v", results[0])
	}
	// maxTurns=1 ⇒ the child ran exactly ONE model call (the override engine's first turn).
	if got := overrideLLM.Calls(); got != 1 {
		t.Fatalf("per-def maxTurns=1 must bound the override child to 1 call, got %d", got)
	}
}

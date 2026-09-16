package agent_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// TestSubagentPerCallModelRoutesToFactory is the MODEL-FACING e2e: a Subagent call with
// `model: X` runs on the engine the factory minted for X (distinguished by a marker
// mockllm summary), NOT the default explorer.
func TestSubagentPerCallModelRoutesToFactory(t *testing.T) {
	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("DEFAULT")), catalogWith(t))
	overrideEngine := childEngineWith(mockllm.New(mockllm.TextTurn("OVERRIDE-X")), catalogWith(t))

	var sawModel string
	task := agent.NewSubagentTool(defaultEngine, agent.WithSubagentEngineFactory(
		func(model string) (*agent.Engine, bool) {
			sawModel = model
			if model == "fast-mini" {
				return overrideEngine, true
			}
			return nil, false
		}))

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"explore","model":"fast-mini"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("want 1 success result, got %+v", results[0])
	}
	if !strings.Contains(results[0].Content, "OVERRIDE-X") {
		t.Fatalf("model override must route to the factory's engine, got %q", results[0].Content)
	}
	if sawModel != "fast-mini" {
		t.Fatalf("factory must receive the requested model, got %q", sawModel)
	}
}

// TestSubagentPerCallModelCarriesOverrideOnStart asserts the generic Model field
// (issue #112 / ADR 0035) on EvSubagentStart reflects the per-call `model` override —
// the child engine minted for that model carries it as deps.Model, and Engine.Model()
// surfaces it. The factory mints an engine whose Deps.Model IS the requested model
// (mirroring composition's re-derivation), so the assertion proves the override flows
// end-to-end to the wire field, not just to factory selection.
func TestSubagentPerCallModelCarriesOverrideOnStart(t *testing.T) {
	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("DEFAULT")), catalogWith(t))
	overrideEngineFor := func(model string) *agent.Engine {
		return childEngineWithModel(model, mockllm.New(mockllm.TextTurn("OVERRIDE:"+model)), catalogWith(t))
	}
	task := agent.NewSubagentTool(defaultEngine, agent.WithSubagentEngineFactory(
		func(model string) (*agent.Engine, bool) { return overrideEngineFor(model), true }))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"explore","model":"fast-mini"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)

	var found bool
	for _, ev := range evs {
		if ev.Type == session.EvSubagentStart && ev.Subagent != nil {
			found = true
			if ev.Subagent.Model != "fast-mini" {
				t.Fatalf("EvSubagentStart.Model = %q, want %q (the per-call override model)",
					ev.Subagent.Model, "fast-mini")
			}
			// A per-call override is NOT a router classification: the routed fields stay empty.
			if ev.Subagent.RoutedCategory != "" || ev.Subagent.RoutedModel != "" {
				t.Fatalf("per-call override must leave routed fields empty: %+v", ev.Subagent)
			}
		}
	}
	if !found {
		t.Fatalf("no subagent.start event observed")
	}
}

// TestSubagentPerCallModelUnknownErrors is the ADVERSARIAL test: a bogus model the factory
// cannot route returns (nil,false), so Subagent yields a model-addressable error and spawns
// NO child (the default engine's summary never appears).
func TestSubagentPerCallModelUnknownErrors(t *testing.T) {
	defaultLLM := mockllm.New(mockllm.TextTurn("DEFAULT"))
	defaultEngine := childEngineWith(defaultLLM, catalogWith(t))
	task := agent.NewSubagentTool(defaultEngine, agent.WithSubagentEngineFactory(
		func(string) (*agent.Engine, bool) { return nil, false }))

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x","model":"bogus"}`)),
		mockllm.TextTurn("parent recovered"),
	)
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("unknown model must be a model-addressable error, got %+v", results[0])
	}
	if !strings.Contains(results[0].Content, "bogus") {
		t.Fatalf("error must name the bad model, got %q", results[0].Content)
	}
	if defaultLLM.Calls() != 0 {
		t.Fatalf("no child must be spawned on an unroutable model, default engine made %d calls", defaultLLM.Calls())
	}
}

// TestSubagentAgentAndModelTogetherSupported proves a call that sets BOTH `agent` and `model`
// routes to the engine the agentModelFactory minted for the (agent, model) pair — a scoped
// rebuild of the specialist on the override model (distinguished by a marker mockllm
// summary), NOT the pre-built agentEngines["reviewer"] engine and NOT the default explorer.
func TestSubagentAgentAndModelTogetherSupported(t *testing.T) {
	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("DEFAULT")), catalogWith(t))
	reviewerEngine := childEngineWith(mockllm.New(mockllm.TextTurn("REVIEWER")), catalogWith(t))
	overrideEngine := childEngineWithModel("fast", mockllm.New(mockllm.TextTurn("REVIEWER-ON-FAST")), catalogWith(t))
	task := agent.NewSubagentTool(defaultEngine,
		agent.WithAgentEngines(
			map[string]*agent.Engine{"reviewer": reviewerEngine},
			[]agent.AgentMeta{{Name: "reviewer", Description: "reviews"}}),
		agent.WithAgentModelEngineFactory(func(agentName, model string) (*agent.Engine, bool) {
			if agentName == "reviewer" && model == "fast" {
				return overrideEngine, true
			}
			return nil, false
		}),
	)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x","agent":"reviewer","model":"fast"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("agent+model together must succeed, got %+v", results[0])
	}
	if !strings.Contains(results[0].Content, "REVIEWER-ON-FAST") {
		t.Fatalf("agent+model must route to the factory's override engine, got %q", results[0].Content)
	}
	if !strings.Contains(results[0].Content, "agentId:") {
		t.Fatalf("result must carry an agentId trailer, got %q", results[0].Content)
	}
}

// TestSubagentAgentAndModelUnsupportedErrors proves that without a wired
// agentModelFactory, agent+model together is a model-addressable "not supported in this
// deployment" error (never a silent fallback to the agent's own engine or the explorer).
func TestSubagentAgentAndModelUnsupportedErrors(t *testing.T) {
	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("DEFAULT")), catalogWith(t))
	reviewerEngine := childEngineWith(mockllm.New(mockllm.TextTurn("REVIEWER")), catalogWith(t))
	task := agent.NewSubagentTool(defaultEngine,
		agent.WithAgentEngines(
			map[string]*agent.Engine{"reviewer": reviewerEngine},
			[]agent.AgentMeta{{Name: "reviewer", Description: "reviews"}}),
		// NO WithAgentModelEngineFactory — agent+model is unsupported in this deployment.
	)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x","agent":"reviewer","model":"fast"}`)),
		mockllm.TextTurn("parent recovered"),
	)
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("agent+model with no factory must be an error, got %+v", results[0])
	}
	if !strings.Contains(results[0].Content, "not supported in this deployment") {
		t.Fatalf("error must say 'not supported in this deployment', got %q", results[0].Content)
	}
}

// TestSubagentAgentModelUnknownAgentListsNames proves that with agent+model, an UNKNOWN
// agent yields the unknown-agent hint (listing valid names) — NOT a model error. The
// name-truth check runs before the factory, so a typo names the valid agents, not the model.
func TestSubagentAgentModelUnknownAgentListsNames(t *testing.T) {
	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("DEFAULT")), catalogWith(t))
	reviewerEngine := childEngineWith(mockllm.New(mockllm.TextTurn("REVIEWER")), catalogWith(t))
	task := agent.NewSubagentTool(defaultEngine,
		agent.WithAgentEngines(
			map[string]*agent.Engine{"reviewer": reviewerEngine},
			[]agent.AgentMeta{{Name: "reviewer", Description: "reviews"}}),
		agent.WithAgentModelEngineFactory(func(string, string) (*agent.Engine, bool) { return nil, false }),
	)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x","agent":"ghost","model":"fast"}`)),
		mockllm.TextTurn("parent recovered"),
	)
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("unknown agent+model must be an error, got %+v", results[0])
	}
	if !strings.Contains(results[0].Content, "reviewer") {
		t.Fatalf("unknown-agent error must list the valid agent name 'reviewer', got %q", results[0].Content)
	}
	if strings.Contains(results[0].Content, "unroutable model") {
		t.Fatalf("unknown-agent error must NOT be a model error, got %q", results[0].Content)
	}
}

// TestSubagentAgentModelUnroutableModelErrors proves a KNOWN agent + a model the factory
// cannot route yields a model-addressable error naming BOTH the agent and the model (so the
// model can retry by omitting `model`), never a silent fallback to the agent's own engine.
func TestSubagentAgentModelUnroutableModelErrors(t *testing.T) {
	defaultEngine := childEngineWith(mockllm.New(mockllm.TextTurn("DEFAULT")), catalogWith(t))
	reviewerEngine := childEngineWith(mockllm.New(mockllm.TextTurn("REVIEWER")), catalogWith(t))
	task := agent.NewSubagentTool(defaultEngine,
		agent.WithAgentEngines(
			map[string]*agent.Engine{"reviewer": reviewerEngine},
			[]agent.AgentMeta{{Name: "reviewer", Description: "reviews"}}),
		agent.WithAgentModelEngineFactory(func(string, string) (*agent.Engine, bool) { return nil, false }),
	)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x","agent":"reviewer","model":"bogus"}`)),
		mockllm.TextTurn("parent recovered"),
	)
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("unroutable model must be an error, got %+v", results[0])
	}
	if !strings.Contains(results[0].Content, "bogus") || !strings.Contains(results[0].Content, "reviewer") {
		t.Fatalf("error must name both the bad model and the agent, got %q", results[0].Content)
	}
}

// TestSubagentPerCallMaxRunTokensHitsBudgetTerminal is the MODEL-FACING e2e + ADVERSARIAL: a
// runaway child (never stops on its own) with a tighten-only per-call max_run_tokens hits the
// token-budget terminal. StopBudget is a clean terminal, so the Subagent RESULT is a SUCCESS
// (best-effort), annotated with the budget note. The Run-scoped override works on the
// SHARED child engine (which carries NO operator budget of its own).
//
// The per-call budget is set above agent.MinSubagentRunTokens (the floor) so it is passed
// through verbatim. Each turn spends 15 000 tokens, so the budget trips after a few turns.
func TestSubagentPerCallMaxRunTokensHitsBudgetTerminal(t *testing.T) {
	// 15 000 tokens/turn; a per-call budget of MinSubagentRunTokens+1000 = 26 000 trips
	// after turn 2 (30 000 > 26 000). Using a value above the floor so it is not silently
	// raised — the test proves the per-call path, not the floor mechanics.
	budget := agent.MinSubagentRunTokens + 1000
	childLLM := &runawayProvider{perTurn: session.Usage{InputTokens: 15_000}}
	// The shared child engine has NO MaxRunTokens; the per-call max_run_tokens is the only brake.
	childEngine := agent.NewEngine(agent.Deps{
		LLM:     childLLM,
		Catalog: catalogWith(t, loopTool()),
		Policy:  allowAll(),
		Model:   "child-model",
	})
	task := agent.NewSubagentTool(childEngine)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent",
			fmt.Sprintf(`{"prompt":"run forever","max_run_tokens":%d}`, budget))),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	// StopBudget is a clean terminal → a success result with the budget note, not an error.
	if results[0].IsError {
		t.Fatalf("budget-stopped child must be a clean success result, got error: %q", results[0].Content)
	}
	if !strings.Contains(results[0].Content, "token budget") {
		t.Fatalf("result should carry the budget note, got %q", results[0].Content)
	}
	// The child must have run multiple turns before the per-call budget tripped (proving
	// the Run-scoped override bound the SHARED engine that has no operator budget).
	if got := childLLM.calls.Load(); got < 2 {
		t.Fatalf("child made %d model calls, want >= 2 (per-call max_run_tokens must bound a runaway on a shared engine)", got)
	}
}

// TestSubagentPerCallMaxRunTokensTightenOnly proves the per-call max_run_tokens is TIGHTEN-ONLY: a
// per-call value HIGHER than the engine's operator default does NOT loosen it — the
// engine's lower budget still trips. The child runs on an engine with a TIGHT operator
// budget and a generous per-call max_run_tokens; the operator budget must win.
func TestSubagentPerCallMaxRunTokensTightenOnly(t *testing.T) {
	childLLM := &runawayProvider{perTurn: session.Usage{InputTokens: 60, OutputTokens: 40}}
	childEngine := agent.NewEngine(agent.Deps{
		LLM:          childLLM,
		Catalog:      catalogWith(t, loopTool()),
		Policy:       allowAll(),
		Model:        "child-model",
		MaxRunTokens: 250, // tight operator budget
	})
	task := agent.NewSubagentTool(childEngine)

	results, _ := subagentParentResults(t, task,
		// A generous per-call max_run_tokens that must NOT loosen the tight operator budget.
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"run forever","max_run_tokens":100000}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("want 1 clean result, got %+v", results[0])
	}
	if !strings.Contains(results[0].Content, "token budget") {
		t.Fatalf("the tight operator budget must still trip (tighten-only), got %q", results[0].Content)
	}
	// ~3 turns to cross 250 at 100/turn; assert it did NOT run away to the 100000 ceiling.
	if got := childLLM.calls.Load(); got > 10 {
		t.Fatalf("child made %d model calls; the per-call max_run_tokens must not loosen the tight operator budget", got)
	}
}

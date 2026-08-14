package app

import (
	"context"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
)

// TestBuildSubagentEngineFactoryReDerivesForOverrideModel is the CONTAMINATION guard for the
// per-call model override (Iteration 4): the factory must mint the override child engine
// through newChildEngineForProvider (engineDepsForProvider re-derives Compactor/
// TokenCounter/Env.Model/ContextWindow for the OVERRIDE model), NEVER a clone-and-swap of
// an existing engine. We prove re-derivation observably via the engine's ContextWindow():
// an override model with a catalogued 200k window yields a child engine whose window is
// 200k, distinct from the parent default's 128k. A clone-and-swap that reused the parent's
// derived window (or window=0 → 128k) would FAIL this.
func TestBuildSubagentEngineFactoryReDerivesForOverrideModel(t *testing.T) {
	// Parent provider = anthropic; the override model has a catalogued 200k window.
	const overrideModel = catAnthropicModel // catalogued 200k
	prov := mockllm.New()
	reg := regForTest(prov, providerAnthropic, "claude-default")
	cfg := Config{Model: "claude-default"}

	factory := buildSubagentEngineFactory(cfg, reg, prov, providerAnthropic, "claude-default", nil)

	// An empty model is unroutable (ok=false), so Subagent surfaces a model-addressable error.
	if _, ok := factory(""); ok {
		t.Fatal("empty model must be unroutable (ok=false)")
	}

	eng, ok := factory(overrideModel)
	if !ok || eng == nil {
		t.Fatalf("factory(%q) = (%v, %v), want a non-nil engine", overrideModel, eng, ok)
	}
	if got := eng.ContextWindow(); got != catAnthropicCtx {
		t.Fatalf("override child ContextWindow = %d, want the OVERRIDE model's catalogued window %d "+
			"(re-derived via engineDepsForProvider, not the parent default %d)",
			got, catAnthropicCtx, defaultContextWindowTokens)
	}
}

// TestBuildAgentModelEngineFactoryRebuildsDefScopeOnOverrideModel proves the agent+model
// factory rebuilds the def's SCOPED engine on the override model (NOT the generic explorer
// set), Engine.Model() is the override model, and the engine's context window is re-derived
// for the override model (the contamination-safe path). It mirrors
// app.TestBuildSubagentEngineFactoryReDerivesForOverrideModel for the agent+model axis.
//
// The def has a scoped tool allowlist (Read + Grep) and a body prompt; the override engine
// must carry BOTH (the scoped catalog, not the explorer set) AND run on the override model.
func TestBuildAgentModelEngineFactoryRebuildsDefScopeOnOverrideModel(t *testing.T) {
	const overrideModel = catAnthropicModel // catalogued 200k window
	prov := mockllm.New()
	reg := regForTest(prov, providerAnthropic, "claude-default")
	// A def with a SCOPED tool allowlist (Read + Grep — NOT the explorer set) and a body.
	def := agents.AgentDef{Name: "reviewer", Description: "reviews", Tools: []string{"Read", "Grep"}, Body: "You are a reviewer."}
	agentReg := agents.NewRegistry([]agents.AgentDef{def})
	cfg := Config{Model: "claude-default"}

	factory := buildAgentModelEngineFactory(context.Background(), cfg, reg, prov, providerAnthropic, "claude-default",
		agentReg, nil, hookexec.New(nil), nil, nil)

	// An empty agent or model is unroutable.
	if _, ok := factory("", overrideModel); ok {
		t.Fatal("empty agent must be unroutable (ok=false)")
	}
	if _, ok := factory("reviewer", ""); ok {
		t.Fatal("empty model must be unroutable (ok=false)")
	}
	// An unknown agent is unroutable.
	if _, ok := factory("ghost", overrideModel); ok {
		t.Fatal("unknown agent must be unroutable (ok=false)")
	}

	eng, ok := factory("reviewer", overrideModel)
	if !ok || eng == nil {
		t.Fatalf("factory(reviewer, %q) = (%v, %v), want a non-nil engine", overrideModel, eng, ok)
	}
	// Engine.Model() is the override model (re-derived, not the def's startup model).
	if got := eng.Model(); got != overrideModel {
		t.Fatalf("override engine Model() = %q, want the override model %q", got, overrideModel)
	}
	// The override child ContextWindow is the OVERRIDE model's catalogued window (re-derived
	// via engineDepsForProvider, not the parent default).
	if got := eng.ContextWindow(); got != catAnthropicCtx {
		t.Fatalf("override child ContextWindow = %d, want the OVERRIDE model's catalogued window %d "+
			"(re-derived via engineDepsForProvider, not the parent default %d)",
			got, catAnthropicCtx, defaultContextWindowTokens)
	}
	// The def's SCOPED catalog (Read + Grep, NOT the explorer's Glob) is rebuilt by the same
	// scopedToolNamesMode path buildAgentSubagentEngines uses (verified by the scoped-names
	// tests); the engine's catalog is private, so the scoping is proven end-to-end by
	// TestSubagentAgentPlusModelRunsScopedChildOnOverrideModel (the override engine that
	// actually ran is the factory-minted one, not the explorer) rather than peeked here.
}

// TestBuildAgentModelEngineFactoryDeclinesInlineMCP proves the v1 scope limit (Risk-1,
// Option B): a def with an INLINE MCP server (URL set, !IsReference) is declined by the
// factory (nil, false) — the inline manager's live session must outlive a per-call engine,
// and the per-call path has no process-lifetime owner for one. A reference-only def is NOT
// declined (it borrows the process-lifetime mainMgr).
func TestBuildAgentModelEngineFactoryDeclinesInlineMCP(t *testing.T) {
	prov := mockllm.New()
	reg := regForTest(prov, providerAnthropic, "claude-default")
	inlineDef := agents.AgentDef{
		Name:       "inline-reviewer",
		Tools:      []string{"Read"},
		MCPServers: []agents.AgentMCPServer{{Name: "inline", URL: "http://127.0.0.1:1/sse"}},
	}
	refDef := agents.AgentDef{
		Name:       "ref-reviewer",
		Tools:      []string{"Read"},
		MCPServers: []agents.AgentMCPServer{{Name: "main"}}, // reference (no URL)
	}
	agentReg := agents.NewRegistry([]agents.AgentDef{inlineDef, refDef})
	cfg := Config{Model: "claude-default"}

	factory := buildAgentModelEngineFactory(context.Background(), cfg, reg, prov, providerAnthropic, "claude-default",
		agentReg, nil, hookexec.New(nil), nil, nil)

	// Inline MCP server ⇒ declined (the v1 scope limit).
	if eng, ok := factory("inline-reviewer", "claude-default"); ok || eng != nil {
		t.Fatalf("a def with an inline MCP server must be declined (nil, false), got (%v, %v)", eng, ok)
	}
	// Reference-only MCP server ⇒ NOT declined (borrows mainMgr, no new connection).
	eng, ok := factory("ref-reviewer", "claude-default")
	if !ok || eng == nil {
		t.Fatalf("a reference-only MCP def must NOT be declined (it borrows mainMgr), got (%v, %v)", eng, ok)
	}
}

// TestBuildSubagentToolAgentPlusModelWiring is the COMPOSITION-LEVEL e2e (QA gap #2): it
// drives the REAL buildSubagentTool (which wires WithAgentModelEngineFactory internally)
// and executes a Subagent call setting BOTH `agent` and `model`, proving the wiring is
// correct end-to-end — the child runs on the override model ("fast"), not the def's
// startup model ("reviewer-model") and not the parent default. A wiring regression (wrong
// option, missing append, factory not threaded) would pass the direct-factory tests above
// but fail here.
func TestBuildSubagentToolAgentPlusModelWiring(t *testing.T) {
	ctx := context.Background()
	// A one-def registry: "reviewer" with no model pin (inherits parent). buildSubagentTool
	// builds its pre-built engine on the parent model; the agent+model factory rebuilds it
	// on the override model.
	def := agents.AgentDef{Name: "reviewer", Description: "reviews", Tools: []string{"Read"}, Body: "You are a reviewer."}
	agentReg := agents.NewRegistry([]agents.AgentDef{def})

	// The child provider observes the model on its requests; the override must be "fast".
	var seenModel string
	var seenReqMu sync.Mutex
	prov := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
			seenReqMu.Lock()
			seenModel = r.Model
			seenReqMu.Unlock()
		})},
		mockllm.TextTurn("reviewer-on-fast"),
	)
	cfg := Config{Model: "claude-default", Diagnostics: port.NopDiagnostics{}}

	subTool, closeFn := buildSubagentTool(ctx, cfg, regForTest(prov, providerAnthropic, cfg.Model), prov, providerAnthropic, cfg.Model,
		hookexec.New(nil), agentReg, nil, nil, nil, catalogAssets{}, false)
	if closeFn != nil {
		defer func() { _ = closeFn() }()
	}

	res, err := subTool.Execute(ctx,
		session.NewToolCall("c1", "Subagent", []byte(`{"prompt":"review it","agent":"reviewer","model":"fast"}`)),
		memEnvironment("/ws"))
	if err != nil {
		t.Fatalf("Subagent.Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("agent+model through buildSubagentTool must succeed, got error: %q", res.Content)
	}
	// The child ran on the OVERRIDE model "fast" — proving the WithAgentModelEngineFactory
	// wiring (buildSubagentTool → buildAgentModelEngineFactory → override engine) is live.
	seenReqMu.Lock()
	got := seenModel
	seenReqMu.Unlock()
	if got != "fast" {
		t.Fatalf("child ran on model %q, want the override %q (wiring regression?)", got, "fast")
	}
}

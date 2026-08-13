package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// globalMCPFactory builds a sessionEngineFactory over a two-provider registry
// (openai default, openrouter selectable) and threads `globalMgr` as the SHARED
// server-global MCP manager — the seam bug #3 fixed (a selector session used to get
// a fresh core-only catalog and silently drop the server-global MCP tools).
func globalMCPFactory(t *testing.T, globalMgr *mcp.Manager) (server.SessionEngineFactory, *providerRegistry) {
	t.Helper()
	cfg := Config{Model: "default-model"}
	oa := mockllm.New(mockllm.TextTurn("OPENAI-REPLY"))
	or := mockllm.New(mockllm.TextTurn("OPENROUTER-REPLY"))
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerOpenAI:     {id: providerOpenAI, provider: oa, available: true},
			providerOpenRouter: {id: providerOpenRouter, provider: or, available: true},
		},
		defaultID: providerOpenAI,
	}
	store := memstore.New()
	policy := permpolicy.NewPolicy(defaultRules(), nil)
	factory := sessionEngineFactory(cfg, reg, oa, store, policy, hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{globalMgr: globalMgr}, nil)
	return factory, reg
}

// TestSessionEngineFactoryMountsGlobalMCPToolsForSelector is the headline regression
// guard for bug #3: a NON-default selector session (what the mecatui /models picker
// always produces) must still carry the SERVER-GLOBAL MCP tools. Before the fix the
// factory built a fresh core-only catalog and mounted ZERO global MCP tools, so
// selecting a model stripped github/slack/fetch/etc. Here a real in-process global
// manager exposes mcp__globe__echo; the test asserts the selector engine both HAS the
// namespaced tool (catalog accessor) AND can actually DISPATCH it to the echo result.
func TestSessionEngineFactoryMountsGlobalMCPToolsForSelector(t *testing.T) {
	url := newMCPTestServer(t)
	globalMgr := connectMainManager(t, "globe", url)

	const toolName = "mcp__globe__echo"

	// Sanity: the shared manager really exposes the global tool.
	if _, ok := toolNameSet(globalMgr.Tools())[toolName]; !ok {
		t.Fatalf("global manager should expose %s, got %v", toolName, globalMgr.Tools())
	}

	factory, _ := globalMCPFactory(t, globalMgr)

	// A NON-default selector (openrouter) + NO client specs — exactly the picker case.
	res, err := factory(context.Background(), server.ProviderSelector{ProviderID: providerOpenRouter}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory(selector): %v", err)
	}
	eng, closeFn := res.Engine, res.Close
	defer func() { _ = closeFn() }()

	// Catalog-accessor proof: the namespaced global tool is present on the engine.
	if !eng.HasTool(toolName) {
		t.Fatalf("selector engine is missing the server-global MCP tool %s (bug #3: global MCP stripped under model selection)", toolName)
	}

	// Dispatch-level proof: script the model to CALL the global tool and assert it
	// dispatches and returns the echo result (the direct "0 MCP calls" reproduction).
	echoCall := session.NewToolCall("c1", toolName, []byte(`{"text":"hi"}`))
	prov := mockllm.New(mockllm.ToolCallTurn(echoCall), mockllm.TextTurn("done"))
	or2 := mockllm.New(mockllm.TextTurn("unused"))
	reg2 := &providerRegistry{
		entries: map[string]providerEntry{
			providerOpenAI:     {id: providerOpenAI, provider: or2, available: true},
			providerOpenRouter: {id: providerOpenRouter, provider: prov, available: true},
		},
		defaultID: providerOpenAI,
	}
	store := memstore.New()
	policy := permpolicy.NewPolicy(defaultRules(), nil)
	factory2 := sessionEngineFactory(Config{Model: "default-model"}, reg2, or2, store, policy, hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{globalMgr: globalMgr}, nil)
	res2, err := factory2(context.Background(), server.ProviderSelector{ProviderID: providerOpenRouter}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory(dispatch): %v", err)
	}
	eng2, close2 := res2.Engine, res2.Close
	defer func() { _ = close2() }()

	sess := session.New("s1", session.ModeDefault, "/ws", session.Limits{MaxTurns: 5}, time.Now())
	run := eng2.Run(context.Background(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "go", Parts: nil})
	var echoed bool
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.Content == "echo:hi" {
			echoed = true
		}
	}
	if !echoed {
		t.Fatal("selector engine did not dispatch the server-global MCP tool to its echo result (bug #3: 0 global MCP calls)")
	}
}

// TestSessionEngineFactorySelectorNilGlobalMCP is the nil-globalMgr insurance case: a
// NON-default selector with globalMgr==nil and no client specs still builds a usable
// engine carrying the CORE tools, and simply has NO global MCP tool. It guards the
// `globalMgr != nil` branch (no nil-deref, no spurious mount) under a selector.
func TestSessionEngineFactorySelectorNilGlobalMCP(t *testing.T) {
	factory, _ := globalMCPFactory(t, nil)

	res, err := factory(context.Background(), server.ProviderSelector{ProviderID: providerOpenRouter}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory(nil global): %v", err)
	}
	defer func() { _ = res.Close() }()
	if !res.Engine.HasTool("Read") {
		t.Fatal("selector engine missing a CORE tool (Read) — core mount regressed")
	}
	if res.Engine.HasTool("mcp__globe__echo") {
		t.Fatal("selector engine has a global MCP tool with globalMgr==nil — spurious mount")
	}
}

// TestSessionEngineFactorySelectorCloseKeepsGlobalMCP proves per-session close
// ISOLATION: a selector session's Close must tear down ONLY its own connections and
// NEVER the SHARED global manager (doing so would kill MCP for every other session).
// The counting server increments a DELETE counter when the streamable-HTTP client
// terminates its session; after a selector Close the counter must still be 0, and a
// SECOND selector session must still mount the global tool.
func TestSessionEngineFactorySelectorCloseKeepsGlobalMCP(t *testing.T) {
	url, deletes := newMCPTestServerCounting(t)
	globalMgr := connectMainManager(t, "globe", url)

	const toolName = "mcp__globe__echo"
	factory, _ := globalMCPFactory(t, globalMgr)

	// First selector session, then close it.
	res, err := factory(context.Background(), server.ProviderSelector{ProviderID: providerOpenRouter}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory(first): %v", err)
	}
	if !res.Engine.HasTool(toolName) {
		t.Fatalf("first selector session missing %s", toolName)
	}
	if err := res.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}

	// The SHARED global manager must NOT have been torn down by the per-session close.
	if got := atomic.LoadInt32(deletes); got != 0 {
		t.Fatalf("per-session Close terminated the SHARED global MCP session (DELETE count=%d, want 0) — it must never close the global manager", got)
	}

	// A SECOND selector session still has the global tool (proof the manager lives on).
	res2, err := factory(context.Background(), server.ProviderSelector{ProviderID: providerOpenRouter}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory(second): %v", err)
	}
	defer func() { _ = res2.Close() }()
	if !res2.Engine.HasTool(toolName) {
		t.Fatalf("second selector session missing %s — the global manager was wrongly torn down by the first Close", toolName)
	}
	// And the global manager's own Tools() are still non-empty (not drained).
	if len(globalMgr.Tools()) == 0 {
		t.Fatal("global manager has no tools after a per-session Close — it was wrongly closed")
	}
}

// runSelectorSubagentRefAndCheckEcho drives the FACTORY end to end for a selector
// session whose Subagent def `reference: globe` should resolve a "globe" MCP server, and
// reports whether the child actually DISPATCHED mcp__globe__echo successfully.
//
// It exercises the factory's `refMgr := globalMgr; if refMgr==nil { refMgr=mgr }`
// selection (NOT buildAgentSubagentEngines directly), so reverting refMgr→mgr flips the
// global-manager branch RED. The child runs with its Sink OFF, but the Subagent tool
// forwards a REDACTED projection of child tool activity to the parent run stream as
// EvSubagentTool{ToolName, IsError} — the factory-observable discriminator: a
// resolved ref ⇒ a non-error mcp__globe__echo subagent-tool event; an unresolved one
// ⇒ IsError (the child dispatched an "unknown tool").
func runSelectorSubagentRefAndCheckEcho(t *testing.T, globalMgr *mcp.Manager, specs []mcp.ServerConfig) bool {
	t.Helper()
	const toolName = "mcp__globe__echo"
	defs := agents.NewRegistry([]agents.AgentDef{{
		Name:        "ref-task",
		Description: "references the globe server",
		Tools:       []string{"Read"},
		MCPServers:  []agents.AgentMCPServer{{Name: "globe"}}, // reference (no URL)
	}})

	// One shared mockllm cursor backs BOTH the parent session engine and the child
	// (the child inherits the openrouter session provider). Sequence across the shared
	// cursor: parent → Subagent(ref-task); child → mcp__globe__echo; child → "child done";
	// parent → "parent done".
	mk := func() *mockllm.Provider {
		return mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("p1", "Subagent", []byte(`{"prompt":"go","agent":"ref-task"}`))),
			mockllm.ToolCallTurn(session.NewToolCall("c1", toolName, []byte(`{"text":"hi"}`))),
			mockllm.TextTurn("child done"),
			mockllm.TextTurn("parent done"),
		)
	}
	oa := mk()
	or := mk()
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerOpenAI:     {id: providerOpenAI, provider: oa, available: true},
			providerOpenRouter: {id: providerOpenRouter, provider: or, available: true},
		},
		defaultID: providerOpenAI,
	}
	store := memstore.New()
	policy := permpolicy.NewPolicy(defaultRules(), nil)
	factory := sessionEngineFactory(Config{Model: "gpt-5"}, reg, oa, store, policy, hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{globalMgr: globalMgr, agentReg: defs}, nil)

	res, err := factory(context.Background(), server.ProviderSelector{ProviderID: providerOpenRouter}, specs, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = res.Close() }()

	sess := session.New("s1", session.ModeDefault, "/ws", session.Limits{MaxTurns: 8}, time.Now())
	run := res.Engine.Run(context.Background(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "go", Parts: nil})
	var resolved bool
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
		// The child's mcp__globe__echo surfaces as a redacted subagent-tool event.
		if ev.Type == session.EvSubagentTool && ev.Subagent != nil &&
			ev.Subagent.ToolName == toolName && !ev.Subagent.IsError {
			resolved = true
		}
	}
	return resolved
}

// TestSelectorSubagentRefResolvesGlobalMCP proves the Subagent PARITY half of the fix through
// the FACTORY: a selector session's Subagent def `reference: globe` resolves the
// SERVER-GLOBAL servers because the factory threads globalMgr as the
// reference-resolution mainMgr (refMgr). Driven end to end; reverting refMgr→mgr
// makes this case RED (the global ref would no longer resolve).
func TestSelectorSubagentRefResolvesGlobalMCP(t *testing.T) {
	url := newMCPTestServer(t)
	globalMgr := connectMainManager(t, "globe", url)

	if !runSelectorSubagentRefAndCheckEcho(t, globalMgr, nil) {
		t.Fatal("selector session's Subagent `reference: globe` did not resolve the SERVER-GLOBAL MCP tool through the factory (refMgr==globalMgr branch regressed)")
	}
}

// TestSelectorSubagentRefResolvesClientMCPWhenNoGlobal covers the FALLBACK branch the
// global case never exercises: globalMgr==nil, so the factory's refMgr falls back to
// the per-session CLIENT manager (built from the spec list). A Subagent def
// `reference: globe` must then resolve against the client-supplied "globe" server.
// This is the branch that goes RED if `refMgr := globalMgr; if refMgr==nil {
// refMgr=mgr }` is reduced to just `refMgr := globalMgr`.
func TestSelectorSubagentRefResolvesClientMCPWhenNoGlobal(t *testing.T) {
	url := newMCPTestServer(t)

	// No global manager; the "globe" server arrives as a CLIENT spec, so the factory
	// connects a per-session client mgr and refMgr falls back to it.
	if !runSelectorSubagentRefAndCheckEcho(t, nil, []mcp.ServerConfig{{Name: "globe", URL: url}}) {
		t.Fatal("with no global manager, the Subagent `reference: globe` did not resolve against the per-session CLIENT manager (refMgr==nil → mgr fallback regressed)")
	}
}

// TestSelectorClientToolCollisionGlobalWins proves the mcp.Register skip-and-continue
// contract end to end through the factory, BEHAVIORALLY: the global "globe" server
// and the colliding CLIENT "globe" server are deliberately DISTINCT (the global echo
// answers "global:"+text, the client one "client:"+text), the surviving
// mcp__globe__echo is actually EXECUTED, and the result must carry the GLOBAL
// behavior. Two identical echo servers made the old HasTool-only assertion
// tautological (QA mutant M3: mounting client-before-global still passed); now a
// precedence flip changes the observed output and fails loudly. The client specs are
// [globe (collides), other (distinct, ordered AFTER the collider)], so the test also
// still proves the collision does not abort the rest of the client batch.
func TestSelectorClientToolCollisionGlobalWins(t *testing.T) {
	gURL := newMCPTestServerPrefixed(t, "global:")
	globalMgr := connectMainManager(t, "globe", gURL)

	// Two client servers: "globe" (same namespace → mcp__globe__echo collides,
	// behaviorally distinct) FIRST, then a distinct "other" (→ mcp__other__echo)
	// ordered after the collider.
	cURL := newMCPTestServerPrefixed(t, "client:")
	oURL := newMCPTestServer(t)

	// A provider scripted to CALL the surviving mcp__globe__echo so the test
	// observes WHICH server's tool executes, not merely that a name registered.
	echoCall := session.NewToolCall("c1", "mcp__globe__echo", []byte(`{"text":"hi"}`))
	or := mockllm.New(mockllm.ToolCallTurn(echoCall), mockllm.TextTurn("done"))
	oa := mockllm.New(mockllm.TextTurn("unused"))
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerOpenAI:     {id: providerOpenAI, provider: oa, available: true},
			providerOpenRouter: {id: providerOpenRouter, provider: or, available: true},
		},
		defaultID: providerOpenAI,
	}
	store := memstore.New()
	policy := permpolicy.NewPolicy(defaultRules(), nil)
	factory := sessionEngineFactory(Config{Model: "default-model"}, reg, oa, store, policy, hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{globalMgr: globalMgr}, nil)

	res, err := factory(context.Background(), server.ProviderSelector{ProviderID: providerOpenRouter},
		[]mcp.ServerConfig{{Name: "globe", URL: cURL}, {Name: "other", URL: oURL}}, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory(collision): %v", err)
	}
	defer func() { _ = res.Close() }()

	if !res.Engine.HasTool("mcp__globe__echo") {
		t.Fatal("the GLOBAL mcp__globe__echo was lost on a client name collision (global must win — mounted first)")
	}
	if !res.Engine.HasTool("mcp__other__echo") {
		t.Fatal("the client's non-colliding mcp__other__echo was dropped after the collider (abort-on-first-dup regression; Register must skip-and-continue)")
	}

	// Behavioral proof: execute the surviving tool and assert the GLOBAL server
	// answered. "client:hi" here means the client tool shadowed the global one —
	// the precedence inverted even though both names registered.
	sess := session.New("s1", session.ModeDefault, "/ws", session.Limits{MaxTurns: 5}, time.Now())
	run := res.Engine.Run(context.Background(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "go", Parts: nil})
	var got string
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			got = ev.ToolResult.Content
		}
	}
	if got != "global:hi" {
		t.Fatalf("executing the surviving mcp__globe__echo returned %q, want %q — the GLOBAL server's tool must win the collision (client-before-global mount regression)", got, "global:hi")
	}
}

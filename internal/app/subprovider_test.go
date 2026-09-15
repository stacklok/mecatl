package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/adapter/tokenizer"
)

// --- resolveProviderModel precedence matrix ----------------------------------

// TestResolveProviderModelPrecedence drives the §2 three-level precedence table:
// def.Provider > parentProviderID (the call site's inherited provider), with the
// cross-provider model rebasing rule. The registry holds openai (default) +
// openrouter; the parent is openai/parent-model.
func TestResolveProviderModelPrecedence(t *testing.T) {
	reg := twoProviderReg(mockllm.New(), providerOpenAI, "parent-model", mockllm.New(), providerOpenRouter)
	cfg := Config{Model: "parent-model"}
	const (
		parentID    = providerOpenAI
		parentModel = "parent-model"
	)

	cases := []struct {
		name    string
		def     agents.AgentDef
		wantPID string
		wantMdl string
	}{
		{
			name:    "no provider, no model => inherit parent provider + parent model",
			def:     agents.AgentDef{Name: "a"},
			wantPID: parentID, wantMdl: parentModel,
		},
		{
			name:    "no provider, model set => inherit parent provider, def model wins",
			def:     agents.AgentDef{Name: "b", Model: "some-model"},
			wantPID: parentID, wantMdl: "some-model",
		},
		{
			name:    "provider switch, no model => switched provider + ITS builtin default (NOT parent model)",
			def:     agents.AgentDef{Name: "c", Provider: providerOpenRouter},
			wantPID: providerOpenRouter, wantMdl: builtinDefaultModel[providerOpenRouter],
		},
		{
			name:    "provider switch, model set => switched provider + def model verbatim",
			def:     agents.AgentDef{Name: "d", Provider: providerOpenRouter, Model: "anthropic/claude-sonnet-4.5"},
			wantPID: providerOpenRouter, wantMdl: "anthropic/claude-sonnet-4.5",
		},
		{
			name:    "provider pins the SAME as parent => existing resolveModel chain (no rebase)",
			def:     agents.AgentDef{Name: "e", Provider: providerOpenAI},
			wantPID: providerOpenAI, wantMdl: parentModel,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pid, mdl := resolveProviderModel(cfg, reg, tc.def, parentID, parentModel)
			if pid != tc.wantPID || mdl != tc.wantMdl {
				t.Fatalf("resolveProviderModel = (%q,%q), want (%q,%q)", pid, mdl, tc.wantPID, tc.wantMdl)
			}
		})
	}
}

// TestResolveProviderModelSessionInheritance proves the third precedence level is
// realised by the CALL SITE's parentProviderID: a def that pins NO provider
// inherits whatever the caller threads in — so a "selected session" parent yields
// the session provider, NOT the build-time default.
func TestResolveProviderModelSessionInheritance(t *testing.T) {
	reg := twoProviderReg(mockllm.New(), providerOpenAI, "parent-model", mockllm.New(), providerOpenRouter)
	cfg := Config{Model: "parent-model"}

	// Call site supplies the SELECTED session as the parent (openrouter + its model).
	pid, mdl := resolveProviderModel(cfg, reg, agents.AgentDef{Name: "x"}, providerOpenRouter, "session-model")
	if pid != providerOpenRouter || mdl != "session-model" {
		t.Fatalf("def with no provider on a selected session = (%q,%q), want (openrouter, session-model)", pid, mdl)
	}

	// A def pinning a DIFFERENT provider still overrides the session selection.
	pid2, _ := resolveProviderModel(cfg, reg, agents.AgentDef{Name: "y", Provider: providerOpenAI}, providerOpenRouter, "session-model")
	if pid2 != providerOpenAI {
		t.Fatalf("def pinning openai on an openrouter session = %q, want openai (def wins)", pid2)
	}
}

// TestResolveProviderModelFailSafe proves an unknown/unavailable def.Provider is a
// LOUD fallback to the parent provider + a slog.Warn (never fatal, never dropped) —
// the model resolves through the same-provider chain.
func TestResolveProviderModelFailSafe(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelWarn)

	reg := regForTest(mockllm.New(), providerOpenAI, "parent-model")
	cfg := Config{Model: "parent-model", Diagnostics: diag}

	// "anthropic" is not in this single-provider registry => unknown.
	pid, mdl := resolveProviderModel(cfg, reg, agents.AgentDef{Name: "z", Provider: "anthropic"}, providerOpenAI, "parent-model")
	if pid != providerOpenAI {
		t.Fatalf("unknown provider fell to %q, want the parent (openai) fallback", pid)
	}
	if mdl != "parent-model" {
		t.Fatalf("unknown-provider model = %q, want the resolveModel chain (parent-model)", mdl)
	}
	if !strings.Contains(buf.String(), "unknown/unavailable provider") {
		t.Fatalf("expected an unknown-provider slog.Warn, got: %s", buf.String())
	}
}

// --- contamination guard at the CHILD level ----------------------------------

// TestSubproviderChildContextWindow proves a child bound to a SWITCHED provider+model
// is built through engineDepsForProvider — its ContextWindowTokens reflects THAT
// provider+model's catalogued window (1M for openrouter anthropic/claude-sonnet-4.5),
// never the default 128k. The inherited-default child now resolves the PARENT's real
// window via the shared childWindowFor rule (issue #64: a same-model child compacts on
// the parent's actual window, 400k for gpt-5 — NOT the old hardcoded 128k floor).
func TestSubproviderChildContextWindow(t *testing.T) {
	reg := twoProviderReg(mockllm.New(), providerOpenAI, "gpt-5", mockllm.New(), providerOpenRouter)
	cfg := Config{Model: "gpt-5"}

	// (a) provider-switched def => its catalogued window.
	switched := agents.NewRegistry([]agents.AgentDef{
		{Name: "big", Description: "big-context", Provider: providerOpenRouter, Model: "anthropic/claude-sonnet-4.5"},
	})
	engines, _, _ := buildAgentSubagentEngines(context.Background(), cfg, reg.entries[providerOpenAI].provider,
		reg, providerOpenAI, "gpt-5", switched, nil, nil, nil, nil)
	if engines["big"] == nil {
		t.Fatal("provider-switched def engine not built")
	}
	if got := engines["big"].ContextWindow(); got != 1_000_000 {
		t.Fatalf("switched child ContextWindow = %d, want 1000000 (openrouter sonnet-4.5 catalogued)", got)
	}

	// (b) inherited-default def (same provider+model as the parent) => the PARENT's
	// real catalogued window (400k for openai gpt-5), NOT the 128k floor (issue #64).
	const gpt5Ctx = 400_000
	inherit := agents.NewRegistry([]agents.AgentDef{{Name: "plain", Description: "default"}})
	engines2, _, _ := buildAgentSubagentEngines(context.Background(), cfg, reg.entries[providerOpenAI].provider,
		reg, providerOpenAI, "gpt-5", inherit, nil, nil, nil, nil)
	if got := engines2["plain"].ContextWindow(); got != gpt5Ctx {
		t.Fatalf("inherited-default child ContextWindow = %d, want %d (issue #64: same-model child resolves the parent's real window, not the 128k floor)", got, gpt5Ctx)
	}

	// (c) SAME-provider def `model:` => ITS catalogued window (issue-#35 panel fix:
	// childWindowFor keys on the MODEL changing, not only on a provider switch — a
	// same-provider cheap def must compact on the cheap model's window, never the
	// parent default's 128k).
	areg := regForTest(mockllm.New(), providerAnthropic, "claude-default")
	samep := agents.NewRegistry([]agents.AgentDef{
		{Name: "cheap", Description: "same-provider cheap model", Model: catAnthropicModel},
	})
	engines3, _, _ := buildAgentSubagentEngines(context.Background(), Config{Model: "claude-default"},
		areg.entries[providerAnthropic].provider, areg, providerAnthropic, "claude-default", samep, nil, nil, nil, nil)
	if engines3["cheap"] == nil {
		t.Fatal("same-provider def engine not built")
	}
	if got := engines3["cheap"].ContextWindow(); got != catAnthropicCtx {
		t.Fatalf("same-provider def-model child ContextWindow = %d, want the def model's catalogued window %d (not the parent default %d)",
			got, catAnthropicCtx, defaultContextWindowTokens)
	}
}

// TestSubproviderChildCompactorAndCounter closes the stated contamination gap: a
// provider-switched child's Compactor.Model (the design's real contamination vector)
// reflects the CHILD's model, not the parent's, and its TokenCounter is the
// model-keyed tiktoken counter (re-derived, not the parent's). A shallow clone that
// swapped only LLM+window would reuse the parent model here and FAIL.
func TestSubproviderChildCompactorAndCounter(t *testing.T) {
	cfg := Config{Model: "gpt-5", Compaction: "cascade", Tokenizer: "tiktoken"}
	reg := twoProviderReg(mockllm.New(), providerOpenAI, "gpt-5", mockllm.New(), providerOpenRouter)
	const childModel = "anthropic/claude-sonnet-4.5"

	// Build the child deps directly (a provider switch to openrouter + childModel).
	childProvider, _, model, windowFn := resolveChildProvider(cfg, reg,
		agents.AgentDef{Name: "big", Provider: providerOpenRouter, Model: childModel},
		reg.entries[providerOpenAI].provider, providerOpenAI, "gpt-5")
	deps := childEngineDepsForProvider(cfg, "", childProvider, model, windowFn, tool.NewCatalog(), promptConfig(cfg, ""), nil)

	// Compactor.Model is bound BY VALUE to the child's model (the contamination vector).
	cc, ok := deps.Compactor.(agent.CascadeCompactor)
	if !ok {
		t.Fatalf("child Compactor type = %T, want agent.CascadeCompactor (cfg.Compaction=cascade)", deps.Compactor)
	}
	if cc.Model != childModel {
		t.Fatalf("child Compactor.Model = %q, want the CHILD model %q (not the parent gpt-5)", cc.Model, childModel)
	}
	// TokenCounter is the model-keyed tiktoken counter, re-derived for the child (not
	// nil / not the heuristic) — engineDepsForProvider derives it internally against
	// the child model, so a counter built for a DIFFERENT model is impossible here.
	if _, ok := deps.TokenCounter.(*tokenizer.Counter); !ok {
		t.Fatalf("child TokenCounter type = %T, want *tokenizer.Counter (cfg.Tokenizer=tiktoken, model-keyed)", deps.TokenCounter)
	}
	if got := deps.ContextWindow(); got != 1_000_000 {
		t.Fatalf("child ContextWindow() = %d, want 1000000 (childmodel catalogued window)", got)
	}
}

// TestSubproviderChildTelemetryOff guards the telemetry-leak regression: a child
// engine's Deps.Sink and Deps.ToolCallRecorder are NIL even though engineDepsForProvider sets
// them from cfg — restoring byte-identity with the pre-feature child shape, so a
// sub-agent's turns/tool-calls don't double-count against the operator-facing
// histograms. A regression that dropped the nil-restore would fail here.
func TestSubproviderChildTelemetryOff(t *testing.T) {
	injectedDiag := &capturingDiagnostics{}
	cfg := Config{Model: "gpt-5", Sink: fakeSink{}, ToolCallRecorder: &recordingToolLogger{}, Diagnostics: injectedDiag}
	provider := mockllm.New()
	// A provider-switched child (the same path a def-pinned / Half-B session child takes).
	deps := childEngineDepsForProvider(cfg, "member:explorer", provider, "gpt-5", func() int { return defaultContextWindowTokens }, tool.NewCatalog(), promptConfig(cfg, ""), nil)
	if deps.Sink != nil {
		t.Fatalf("child Deps.Sink = %v, want nil (child telemetry off; pre-feature byte-identity)", deps.Sink)
	}
	if deps.ToolCallRecorder != nil {
		t.Fatalf("child Deps.ToolCallRecorder = %v, want nil (child telemetry off)", deps.ToolCallRecorder)
	}
	// Diagnostics is LIVE for children (DISTINCT from telemetry/audit, which stay
	// off above): the child carries the INJECTED diagnostics, not NopDiagnostics,
	// and is tagged with its agent role so interleaved child logs are readable.
	if deps.Diagnostics != injectedDiag {
		t.Fatalf("child Deps.Diagnostics = %T, want the injected diagnostics (child diagnostics live + correlated)", deps.Diagnostics)
	}
	if deps.Role != "member:explorer" {
		t.Fatalf("child Deps.Role = %q, want %q (child diagnostics correlated by agent role)", deps.Role, "member:explorer")
	}
	// Sanity: the parent's shared deps DO carry the Sink (so the test proves the child
	// override, not an absent Sink). baseEngineDeps is the default-provider parent path.
	parent := baseEngineDeps(cfg, regForTest(provider, providerOpenAI, cfg.Model), provider, memstore.New(),
		permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil), hookexec.New(nil), nil, prompt.RootAssembler{})
	if parent.Sink == nil {
		t.Fatal("parent (baseEngineDeps) Sink is nil; the child-off assertion would be vacuous")
	}
}

// TestMaxRunTokensPropagatesToParentAndChild asserts the loop-level token budget
// (Config.MaxRunTokens) is threaded onto the MAIN engine deps via engineDepsForProvider
// AND INHERITED by a child/member engine via childEngineDepsForProvider (the child
// delegates to engineDepsForProvider and never clears it). This is the
// "every delegation path inherits the budget" contract.
func TestMaxRunTokensPropagatesToParentAndChild(t *testing.T) {
	const budget = 250_000
	cfg := Config{Model: "gpt-5", MaxRunTokens: budget}
	provider := mockllm.New()

	parent := engineDepsForProvider(cfg, provider, cfg.Model, func() int { return defaultContextWindowTokens }, nil,
		permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil), hookexec.New(nil), nil, nil)
	if parent.MaxRunTokens != budget {
		t.Fatalf("parent Deps.MaxRunTokens = %d, want %d (engineDepsForProvider must thread the budget)", parent.MaxRunTokens, budget)
	}

	child := childEngineDepsForProvider(cfg, "member:explorer", provider, cfg.Model, func() int { return defaultContextWindowTokens }, tool.NewCatalog(), promptConfig(cfg, ""), nil)
	if child.MaxRunTokens != budget {
		t.Fatalf("child Deps.MaxRunTokens = %d, want %d (children must INHERIT the budget)", child.MaxRunTokens, budget)
	}
}

// TestDefaultConfigPolicyEvaluatesWithoutPanic is the permconfig typed-nil
// regression guard. With a DEFAULT Config (no PermissionsConventional, no
// PermissionConfigs, no AllowAllTools) permconfig.New returns a TYPED-nil
// *permconfig.Resolver; before the Build fix it was stored in the policy's
// RuleResolver interface as a non-nil interface wrapping a nil pointer, so the
// `resolver != nil` guard stayed true and Resolve PANICKED on the first tool
// permission evaluation. This drives a real session whose model issues a Write
// (which hits the ScopeBuiltinDefault Ask floor) and asserts a permission.ask
// verdict arrives — proving the policy evaluates the resolver branch without
// panicking. (The existing permpolicy nil-resolver tests pass an UNTYPED nil and
// cannot reproduce the typed-nil-in-interface panic.)
func TestDefaultConfigPolicyEvaluatesWithoutPanic(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	built, err := buildIsolated(t, ctx, Config{
		Workspace: workspace,
		NoSoul:    true,
		// DEFAULT permission posture: no Conventional, no ExplicitFiles, no AllowAll —
		// so Build takes the resolver branch with a typed-nil *permconfig.Resolver.
		envDetector: fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-x"}),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(
				mockllm.ToolCallTurn(session.NewToolCall("w1", "Write", []byte(`{"path":"note.txt","content":"x"}`))),
				mockllm.TextTurn("done"),
			)
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	svc := built.Service

	sess, err := svc.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(ctx, sess.ID, "save a note")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	// Drain: the run reaches a permission.ask (Write hits the built-in Ask floor)
	// WITHOUT panicking in the resolver. Reaching any terminal/ask event proves the
	// policy evaluated the typed-nil resolver branch safely.
	var sawAsk bool
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk {
			sawAsk = true
			break
		}
	}
	if !sawAsk {
		t.Fatal("expected a permission.ask for Write (built-in Ask floor); none arrived (policy resolver branch must evaluate without panic)")
	}
}

// --- Half A: def-pinned provider routes through the REAL Subagent path -----------

// TestSubproviderHalfADefPinsProvider drives the REAL Subagent path: an agent def
// pinning provider=openrouter routes its child engine to the openrouter-bound mock
// (distinct reply), while a def pinning nothing runs on the default (openai) mock.
func TestSubproviderHalfADefPinsProvider(t *testing.T) {
	oa := mockllm.New(mockllm.TextTurn("REPLY-FROM-openai"))
	or := mockllm.New(mockllm.TextTurn("REPLY-FROM-openrouter"))
	reg := twoProviderReg(oa, providerOpenAI, "gpt-5", or, providerOpenRouter)
	cfg := Config{Model: "gpt-5"}

	defs := agents.NewRegistry([]agents.AgentDef{
		{Name: "pinned", Description: "on openrouter", Provider: providerOpenRouter},
	})
	engines, meta, _ := buildAgentSubagentEngines(context.Background(), cfg, oa, reg, providerOpenAI, "gpt-5", defs, nil, nil, nil, nil)

	if got := runSubagentAgent(t, oa, engines, meta, "pinned"); !strings.Contains(got, "REPLY-FROM-openrouter") {
		t.Fatalf("def pinned to openrouter routed to %q, want it to contain REPLY-FROM-openrouter", got)
	}
}

// TestSubproviderHalfAFullBuildE2E drives the FULL composition (app.Build →
// server.Service) with TWO providers backed by distinct mocks and a real agent def
// on disk pinning provider=openrouter. A DEFAULT session routes a Subagent to that def;
// the sub-agent's reply proves it ran on openrouter while the main session runs on
// the default openai provider. Offline. The session is created with --yolo
// (AllowAllTools) so the routed Subagent is auto-approved without an interactive gate.
func TestSubproviderHalfAFullBuildE2E(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	agentsDir := t.TempDir()
	// A def pinning openrouter, read-only (Read), routable by Subagent(agent="orspec").
	writeFile(t, agentsDir, "orspec.md", "---\nname: orspec\ndescription: runs on openrouter\nprovider: openrouter\ntools: [Read]\n---\nYou run on openrouter.\n")

	built, err := buildIsolated(t, ctx, Config{
		Workspace:     workspace,
		NoSoul:        true,
		AgentsDirs:    []string{agentsDir},
		AllowAllTools: true, // --yolo: auto-approve the routed Subagent (no interactive gate)
		envDetector: fakeEnv(map[string]string{
			"OPENAI_API_KEY":     "sk-x",
			"OPENROUTER_API_KEY": "sk-x",
		}),
		// Strictly offline: refuse the (keyed) openrouter live fetch ⇒ embedded floor.
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
			reply := "REPLY-FROM-" + id
			return mockllm.New(
				mockllm.ToolCallTurn(session.NewToolCall("p1", "Subagent", []byte(`{"prompt":"go","agent":"orspec"}`))),
				mockllm.TextTurn(reply), mockllm.TextTurn(reply), mockllm.TextTurn(reply),
			)
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	svc := built.Service

	sess, err := svc.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var taskResult string
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.Content != "" {
			taskResult = ev.ToolResult.Content
		}
	}
	if !strings.Contains(taskResult, "REPLY-FROM-openrouter") {
		t.Fatalf("Subagent sub-agent (provider: openrouter) reply = %q, want it to contain REPLY-FROM-openrouter", taskResult)
	}
}

// writeFile writes content to dir/name (helper for the on-disk agent def).
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// runSubagentAgent wires a parent engine on parentProvider whose only move is to call
// Subagent(agent=name), runs one parent turn through the REAL Subagent tool over the per-def
// engines, and returns the sub-agent's terminal text.
func runSubagentAgent(t *testing.T, _ *mockllm.Provider, engines map[string]*agent.Engine, meta []agent.AgentMeta, name string) string {
	t.Helper()
	defaultEngine := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Model: "gpt-5"})
	task := newTestSubagentTool(defaultEngine, agent.WithAgentEngines(engines, meta))
	parentCat := tool.NewCatalog()
	parentCat.MustRegister(task)

	parent := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("p1", "Subagent", []byte(`{"prompt":"go","agent":"`+name+`"}`))),
		mockllm.TextTurn("parent done"),
	)
	e := agent.NewEngine(agent.Deps{
		LLM:     parent,
		Catalog: parentCat,
		Policy:  permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil),
		Model:   "gpt-5",
	})
	r := e.Run(context.Background(),
		session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0)),
		memEnvironment("/ws"), agent.RunRequest{Text: "go"})

	var last string
	for ev := range r.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			last = ev.ToolResult.Content
		}
	}
	return last
}

// --- Half B: session-provider propagation to per-session sub-agent tools -----

// twoProviderFactoryWithAgents builds a sessionEngineFactory over a two-provider
// registry (openai default + openrouter) with the supplied agent defs in scope, so
// a SELECTED session gets a per-session Subagent tool wired to its provider as parent.
// Each provider mock is scripted with: a Subagent tool call (the session/parent turn),
// the sub-agent's reply, then a parent-done turn — so a single provider can back
// BOTH the session engine and a child that inherited it (shared mockllm cursor).
func twoProviderFactoryWithAgents(t *testing.T, defs *agents.Registry, subagentName string, diag ...port.Diagnostics) (server.SessionEngineFactory, *providerRegistry) {
	t.Helper()
	mkMock := func(id string) *mockllm.Provider {
		return mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("p1", "Subagent", []byte(`{"prompt":"go","agent":"`+subagentName+`"}`))),
			mockllm.TextTurn("CHILD-FROM-"+id),
			mockllm.TextTurn("parent done"),
		)
	}
	oa := mkMock(providerOpenAI)
	or := mkMock(providerOpenRouter)
	reg := twoProviderReg(oa, providerOpenAI, "gpt-5", or, providerOpenRouter)
	cfg := Config{Model: "gpt-5"}
	if len(diag) > 0 {
		cfg.Diagnostics = diag[0]
	}
	store := memstore.New()
	policy := permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil)
	factory := sessionEngineFactory(cfg, reg, oa, store, policy, hookexec.New(nil), nil, nil, catalogAssets{agentReg: defs}, nil)
	return factory, reg
}

// TestHalfBSelectedSessionHasSubagentTool is the crux regression guard: a session that
// SELECTS a provider gets a per-session catalog WITH the Subagent tool — today (pre-Half
// B) the per-session catalog was core-tools-only and could not spawn sub-agents.
func TestHalfBSelectedSessionHasSubagentTool(t *testing.T) {
	factory, _ := twoProviderFactoryWithAgents(t, agents.NewRegistry(nil), "")
	res, err := factory(context.Background(), server.ProviderSelector{ProviderID: providerOpenRouter}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = res.Close() }()
	if !res.Engine.HasTool("Subagent") {
		t.Fatal("a provider-selected session must now carry the Subagent tool (Half B); it does not")
	}
	// InspectSubagent is registered UNCONDITIONALLY wherever Subagent is — including the
	// per-session (sessionEngineFactory) catalog, the historically-fragile registration
	// site (the MCP-strip regression class).
	if !res.Engine.HasTool("InspectSubagent") {
		t.Fatal("a provider-selected session must carry the InspectSubagent tool alongside Subagent; it does not")
	}
}

// TestHalfBSessionProviderInheritance: a session selecting provider B, routing a
// Subagent to a def that pins NO provider, runs the sub-agent on the SESSION provider
// (B) — not the build-time default (A). A default (zero-selector) session is NOT
// covered by the factory (it uses the shared engine), so this is asserted via the
// selected path only, contrasting openrouter vs the default openai by selecting each.
func TestHalfBSessionProviderInheritance(t *testing.T) {
	defs := agents.NewRegistry([]agents.AgentDef{{Name: "plain", Description: "no provider"}})

	// Select openrouter: the no-provider def inherits openrouter.
	orFactory, _ := twoProviderFactoryWithAgents(t, defs, "plain")
	if got := runFactorySubagentTurn(t, orFactory, server.ProviderSelector{ProviderID: providerOpenRouter}); !strings.Contains(got, "CHILD-FROM-openrouter") {
		t.Fatalf("no-provider def on an openrouter session ran on %q, want it to contain CHILD-FROM-openrouter (inherited session provider)", got)
	}

	// Select openai: the same def inherits openai.
	oaFactory, _ := twoProviderFactoryWithAgents(t, defs, "plain")
	if got := runFactorySubagentTurn(t, oaFactory, server.ProviderSelector{ProviderID: providerOpenAI}); !strings.Contains(got, "CHILD-FROM-openai") {
		t.Fatalf("no-provider def on an openai session ran on %q, want it to contain CHILD-FROM-openai", got)
	}
}

// TestHalfBDefProviderOverridesSession: a session selecting provider B, routing a
// Subagent to a def pinning provider A, runs the sub-agent on A (def wins — highest
// precedence). The session is openrouter; the def pins openai.
func TestHalfBDefProviderOverridesSession(t *testing.T) {
	defs := agents.NewRegistry([]agents.AgentDef{
		{Name: "pinned", Description: "pins openai", Provider: providerOpenAI},
	})
	factory, _ := twoProviderFactoryWithAgents(t, defs, "pinned")
	// Session selects openrouter; the def pins openai => the CHILD must run on openai.
	if got := runFactorySubagentTurn(t, factory, server.ProviderSelector{ProviderID: providerOpenRouter}); !strings.Contains(got, "CHILD-FROM-openai") {
		t.Fatalf("def pinning openai on an openrouter session ran on %q, want it to contain CHILD-FROM-openai (def overrides session)", got)
	}
}

// TestHalfBSelectedSessionUnknownProviderFallsBack drives the fail-safe through the
// REAL selected-session Subagent path: a session selecting provider B routes a Subagent to a
// def naming a BOGUS provider. The child must still RUN — falling back to the SESSION
// provider (B), not the build-time default A and not an error — and the
// unknown-provider warn must fire. This complements the unit-level
// TestResolveProviderModelFailSafe by exercising the whole per-session wiring.
func TestHalfBSelectedSessionUnknownProviderFallsBack(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelWarn)

	defs := agents.NewRegistry([]agents.AgentDef{
		{Name: "bogus", Description: "names an unkeyed provider", Provider: "anthropic"},
	})
	factory, _ := twoProviderFactoryWithAgents(t, defs, "bogus", diag)
	// Session selects openrouter; the def's bogus provider is unknown => the child
	// falls back to the SESSION provider (openrouter), runs, and does NOT error.
	if got := runFactorySubagentTurn(t, factory, server.ProviderSelector{ProviderID: providerOpenRouter}); !strings.Contains(got, "CHILD-FROM-openrouter") {
		t.Fatalf("def with a bogus provider on an openrouter session ran on %q, want it to contain CHILD-FROM-openrouter (fall back to session provider, not error)", got)
	}
	if !strings.Contains(buf.String(), "unknown/unavailable provider") {
		t.Fatalf("expected an unknown-provider slog.Warn, got: %s", buf.String())
	}
}

// TestHalfBSelectedSessionTeardownFoldsSubagentClose proves the per-session Subagent tool's
// inline-MCP teardown is FOLDED into SessionEngineResult.Close: a Subagent def with an
// inline MCP server connects through the per-session path (its mcp__ tool lands in
// the per-session engine catalog), and res.Close() runs the folded close cleanly —
// so CloseSession/Service.Close tear the def's inline manager down with the session.
func TestHalfBSelectedSessionTeardownFoldsSubagentClose(t *testing.T) {
	url := newMCPTestServer(t)

	defs := agents.NewRegistry([]agents.AgentDef{{
		Name:        "inline-task",
		Description: "inline mcp task def",
		Tools:       []string{"Read"},
		MCPServers:  []agents.AgentMCPServer{{Name: "inline", URL: url}},
	}})
	oa := mockllm.New(mockllm.TextTurn("x"))
	or := mockllm.New(mockllm.TextTurn("x"))
	reg := twoProviderReg(oa, providerOpenAI, "gpt-5", or, providerOpenRouter)
	store := memstore.New()
	policy := permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil)
	factory := sessionEngineFactory(Config{Model: "gpt-5"}, reg, oa, store, policy, hookexec.New(nil), nil, nil, catalogAssets{agentReg: defs}, nil)

	res, err := factory(context.Background(), server.ProviderSelector{ProviderID: providerOpenRouter}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	// The per-session catalog carries the Subagent tool (so the inline def engine was
	// built through the per-session path, connecting its inline MCP server).
	if !res.Engine.HasTool("Subagent") {
		t.Fatal("selected session missing the Subagent tool")
	}
	// The folded close runs cleanly (it aggregates the Subagent-def inline MCP manager's
	// Close into the session's teardown). A nil/leaking close would fail here.
	if res.Close == nil {
		t.Fatal("SessionEngineResult.Close is nil; the per-session Subagent close was not folded in")
	}
	if cerr := res.Close(); cerr != nil {
		t.Fatalf("folded per-session close: %v", cerr)
	}
}

// TestHalfBSelectedSessionCapBounded proves a selected session is ONE
// sessionEngines entry regardless of how many per-def Subagent child engines it builds,
// and that the MaxSessionEngines cap still holds (the per-def children are GC'd with
// the map entry; only the inline MCP managers need explicit close, folded above).
func TestHalfBSelectedSessionCapBounded(t *testing.T) {
	// Several defs so the selected session builds several per-def child engines.
	defs := agents.NewRegistry([]agents.AgentDef{
		{Name: "a", Description: "a"},
		{Name: "b", Description: "b", Provider: providerOpenRouter},
		{Name: "c", Description: "c"},
	})
	oa := mockllm.New(mockllm.TextTurn("x"))
	or := mockllm.New(mockllm.TextTurn("x"))
	reg := twoProviderReg(oa, providerOpenAI, "gpt-5", or, providerOpenRouter)

	store := memstore.New()
	policy := permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil)
	factory := sessionEngineFactory(Config{Model: "gpt-5"}, reg, oa, store, policy, hookexec.New(nil), nil, nil, catalogAssets{agentReg: defs}, nil)
	svc, err := newTestServerService(server.Config{
		Engine: noopEngine(),
		Store:  store,

		Now:               func() time.Time { return time.Unix(0, 0) },
		SessionEngine:     factory,
		MaxSessionEngines: 1,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	// Cap is 1: a single selected session fills the one slot regardless of its 3
	// per-def child engines; CloseSession frees it; a second create then succeeds.
	sess, err := svc.CreateSessionWithProvider(context.Background(), session.ModeDefault, defaultLimits(),
		server.ProviderSelector{ProviderID: providerOpenRouter})
	if err != nil {
		t.Fatalf("first selected create (with 3 per-def children): %v", err)
	}
	// A second selected session must hit the cap (proving the first counts as ONE).
	if _, err := svc.CreateSessionWithProvider(context.Background(), session.ModeDefault, defaultLimits(),
		server.ProviderSelector{ProviderID: providerOpenRouter}); err == nil {
		t.Fatal("second selected create should hit MaxSessionEngines=1 (the first counts as ONE entry)")
	}
	svc.CloseSession(sess.ID)
	if _, err := svc.CreateSessionWithProvider(context.Background(), session.ModeDefault, defaultLimits(),
		server.ProviderSelector{ProviderID: providerOpenRouter}); err != nil {
		t.Fatalf("after CloseSession the slot should free: %v", err)
	}
}

// runFactorySubagentTurn builds the per-session engine for sel, drives one turn (the
// session engine's mock issues a Subagent call), and returns the sub-agent's terminal
// tool-result text (so a test can assert WHICH provider the child ran on).
func runFactorySubagentTurn(t *testing.T, factory server.SessionEngineFactory, sel server.ProviderSelector) string {
	t.Helper()
	res, err := factory(context.Background(), sel, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory(%+v): %v", sel, err)
	}
	defer func() { _ = res.Close() }()
	sess := session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 5}, time.Now())
	r := res.Engine.Run(context.Background(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "go", Parts: nil})
	var last string
	for ev := range r.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.Content != "" {
			last = ev.ToolResult.Content
		}
	}
	return last
}

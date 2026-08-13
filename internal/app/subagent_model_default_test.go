package app

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// These tests close issue #35: Config.SubagentModel must reach the DEF-LESS child
// families too — the default Subagent explorer, undefined team members, and
// Parallel BRANCH children — through the ONE resolveModelFor chain (zero def), and
// the window must be re-derived for the override model. The Parallel JUDGE is the
// pinned asymmetry: it stays on the session model.

// observedProvider returns a mockllm provider that records every req.Model it
// receives (the engine's buildRequest sets it from Deps.Model — the same seam
// session_engine_test.go asserts at) plus the scripted turns.
func observedProvider(models *[]string, mu *sync.Mutex, turns ...mockllm.Turn) *mockllm.Provider {
	return mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
		mu.Lock()
		*models = append(*models, r.Model)
		mu.Unlock()
	})}, turns...)
}

// TestDefaultExplorerUsesSubagentModel: the default (def-less) Subagent explorer
// resolves SubagentModel and is built through the contamination-safe per-provider
// path, so Deps.Model, the prompt Env.Model, AND the context window all key on the
// CHEAP model (window re-derived from the catalog, not the parent's 128k default).
func TestDefaultExplorerUsesSubagentModel(t *testing.T) {
	prov := mockllm.New()
	reg := regForTest(prov, providerAnthropic, "claude-default")
	cfg := Config{Model: "claude-default", SubagentModel: catAnthropicModel}

	deps := childExplorerDeps(cfg, reg, prov, providerAnthropic, cfg.Model, nil)
	if deps.Model != catAnthropicModel {
		t.Fatalf("explorer Deps.Model = %q, want the SubagentModel %q", deps.Model, catAnthropicModel)
	}
	if got := deps.PromptConfig.Env.Model; got != catAnthropicModel {
		t.Fatalf("explorer Env.Model = %q, want the SubagentModel %q (prompt keyed on the model the child RUNS on)", got, catAnthropicModel)
	}
	if deps.ContextWindow() != catAnthropicCtx {
		t.Fatalf("explorer ContextWindowTokens = %d, want the OVERRIDE model's catalogued window %d (re-derived, not the parent default %d)",
			deps.ContextWindow(), catAnthropicCtx, defaultContextWindowTokens)
	}
}

// TestDefaultExplorerInheritsParentWhenUnset is the zero-config guard: with NO
// SubagentModel the explorer stays on the parent model with the default window —
// behaviourally identical to the pre-#35 shape at the Deps seam.
func TestDefaultExplorerInheritsParentWhenUnset(t *testing.T) {
	prov := mockllm.New()
	reg := regForTest(prov, providerAnthropic, "claude-default")
	cfg := Config{Model: "claude-default"} // no SubagentModel

	deps := childExplorerDeps(cfg, reg, prov, providerAnthropic, cfg.Model, nil)
	if deps.Model != "claude-default" {
		t.Fatalf("explorer Deps.Model = %q, want the inherited parent %q", deps.Model, "claude-default")
	}
	if got := deps.PromptConfig.Env.Model; got != "claude-default" {
		t.Fatalf("explorer Env.Model = %q, want the inherited parent", got)
	}
	// "claude-default" is a fictional/uncatalogued model, so childWindowFor resolves
	// it via contextWindowFor → 0 → the 128k floor. (A CATALOGUED same-model inherit
	// would now resolve the parent's real window — issue #64, covered by
	// TestSubproviderChildContextWindow case (b).)
	if deps.ContextWindow() != defaultContextWindowTokens {
		t.Fatalf("explorer ContextWindowTokens = %d, want the %d floor (uncatalogued inherit model)",
			deps.ContextWindow(), defaultContextWindowTokens)
	}
	// The explorer Role shape is unchanged: References convention still appended.
	if !strings.Contains(deps.PromptConfig.Role, "References:") {
		t.Fatalf("explorer Role lost the References convention\nRole=%q", deps.PromptConfig.Role)
	}
}

// TestDefaultMemberUsesSubagentModel: an UNDEFINED team member (no AgentType — the
// buildMemberEngine DEFAULT branch) resolves SubagentModel: the member's actual LLM
// request carries the cheap model and its engine window is re-derived for it.
func TestDefaultMemberUsesSubagentModel(t *testing.T) {
	var (
		mu     sync.Mutex
		models []string
	)
	prov := observedProvider(&models, &mu, mockllm.TextTurn("done"))
	cfg := Config{Model: "claude-default", SubagentModel: catAnthropicModel}
	factory := buildMemberEngine(cfg, regForTest(prov, providerAnthropic, cfg.Model), prov, providerAnthropic, cfg.Model,
		hookexec.New(nil), agents.NewRegistry(nil), nil, nil, nil, false, nil, catalogAssets{}, false)

	build := factory(team.New("t"), agent.MemberSpec{Name: "m"}, "")
	if build.Engine == nil {
		t.Fatal("factory returned a nil engine")
	}
	if got := build.Engine.ContextWindow(); got != catAnthropicCtx {
		t.Fatalf("default member ContextWindow = %d, want the override model's catalogued window %d", got, catAnthropicCtx)
	}
	drainEngine(t, build.Engine)
	mu.Lock()
	defer mu.Unlock()
	if len(models) == 0 || models[0] != catAnthropicModel {
		t.Fatalf("default member LLM request models = %v, want the SubagentModel %q", models, catAnthropicModel)
	}
}

// TestDefaultMemberInheritsParentWhenUnset: zero-config — an undefined member's
// request stays on the parent model with the default window.
func TestDefaultMemberInheritsParentWhenUnset(t *testing.T) {
	var (
		mu     sync.Mutex
		models []string
	)
	prov := observedProvider(&models, &mu, mockllm.TextTurn("done"))
	cfg := Config{Model: "claude-default"} // no SubagentModel
	factory := buildMemberEngine(cfg, regForTest(prov, providerAnthropic, cfg.Model), prov, providerAnthropic, cfg.Model,
		hookexec.New(nil), agents.NewRegistry(nil), nil, nil, nil, false, nil, catalogAssets{}, false)

	build := factory(team.New("t"), agent.MemberSpec{Name: "m"}, "")
	// "claude-default" is uncatalogued ⇒ floor (see TestDefaultExplorerInheritsParentWhenUnset).
	if got := build.Engine.ContextWindow(); got != defaultContextWindowTokens {
		t.Fatalf("default member ContextWindow = %d, want the %d floor (uncatalogued inherit model)", got, defaultContextWindowTokens)
	}
	drainEngine(t, build.Engine)
	mu.Lock()
	defer mu.Unlock()
	if len(models) == 0 || models[0] != "claude-default" {
		t.Fatalf("default member LLM request models = %v, want the inherited parent %q", models, "claude-default")
	}
}

// TestMemberEngineRelaysResolvedContextWindow is the composition half of the issue
// #63/#64 team-member wiring (req 6): a team member that pins NO model of its own,
// built on a CATALOGUED parent (provider+model), must resolve the parent model's
// REAL window via childWindowFor and carry it on the member engine's ContextWindow()
// — the exact value the supervisor relays into TeamEvent.ContextWindow (proto field
// 14). Before issue #64 a same-model member short-circuited to 0 ⇒ the 128k floor.
// catAnthropicModel is catalogued at catAnthropicCtx (200,000), proving the resolved
// window — NOT 128k — feeds the relay. (The relay copy itself is proven offline by
// engine/agent's TestSupervisorRelaysMemberContextWindow + the projectTeamEvent test.)
func TestMemberEngineRelaysResolvedContextWindow(t *testing.T) {
	prov := mockllm.New(mockllm.TextTurn("done"))
	cfg := Config{Model: catAnthropicModel} // catalogued parent; no SubagentModel ⇒ member inherits it
	factory := buildMemberEngine(cfg, regForTest(prov, providerAnthropic, cfg.Model), prov, providerAnthropic, cfg.Model,
		hookexec.New(nil), agents.NewRegistry(nil), nil, nil, nil, false, nil, catalogAssets{}, false)

	build := factory(team.New("t"), agent.MemberSpec{Name: "m"}, "")
	if got := build.Engine.ContextWindow(); got != catAnthropicCtx {
		t.Fatalf("inherited-default member ContextWindow = %d, want the parent model's resolved catalog window %d (issue #64: NOT the %d floor)",
			got, catAnthropicCtx, defaultContextWindowTokens)
	}
	drainEngine(t, build.Engine)
}

// TestParallelBranchUsesSubagentModel: a Parallel BRANCH child resolves
// SubagentModel (model + Env.Model + re-derived window), like the explorer.
func TestParallelBranchUsesSubagentModel(t *testing.T) {
	prov := mockllm.New()
	reg := regForTest(prov, providerAnthropic, "claude-default")
	cfg := Config{Model: "claude-default", SubagentModel: catAnthropicModel}

	deps := parallelChildDeps(cfg, reg, prov, providerAnthropic, cfg.Model, nil)
	if deps.Model != catAnthropicModel {
		t.Fatalf("branch Deps.Model = %q, want the SubagentModel %q", deps.Model, catAnthropicModel)
	}
	if got := deps.PromptConfig.Env.Model; got != catAnthropicModel {
		t.Fatalf("branch Env.Model = %q, want the SubagentModel %q", got, catAnthropicModel)
	}
	if deps.ContextWindow() != catAnthropicCtx {
		t.Fatalf("branch ContextWindowTokens = %d, want the override model's catalogued window %d", deps.ContextWindow(), catAnthropicCtx)
	}
}

// TestParallelJudgeStaysOnParentModel pins the DELIBERATE asymmetry: the Parallel
// JUDGE never consults SubagentModel — built exactly as registerParallelTool builds
// it (modelCfgFor(cfg, sessionModel)), its LLM request carries the SESSION model
// even when a cheap child default is configured. Selecting a winner stays a
// judgement call on the model the operator chose for the session.
func TestParallelJudgeStaysOnParentModel(t *testing.T) {
	var (
		mu     sync.Mutex
		models []string
	)
	prov := observedProvider(&models, &mu, mockllm.TextTurn("winner: 1"))
	cfg := Config{Model: "parent-model", SubagentModel: "cheap-model-1.0"}

	reg := regForTest(prov, providerOpenAI, "parent-model")
	reg.contextWindows = map[string]map[string]int{providerOpenAI: {"parent-model": 333_000}}
	cfg.contextWindows = reg.contextWindows
	judge := buildParallelJudgeEngine(modelCfgFor(cfg, "parent-model"), reg, providerOpenAI, prov)
	if got := judge.ContextWindow(); got != 333_000 {
		t.Fatalf("judge ContextWindow = %d, want exact configured window 333000", got)
	}
	drainEngine(t, judge)
	mu.Lock()
	defer mu.Unlock()
	if len(models) == 0 || models[0] != "parent-model" {
		t.Fatalf("judge LLM request models = %v, want the SESSION model %q (the judge must NOT adopt SubagentModel)", models, "parent-model")
	}
}

// TestPerCallModelOverridesSubagentModel: with BOTH a SubagentModel and a per-call
// `model` override, the per-call override wins — the factory child runs (and is
// window-derived) on the per-call model, never the global child default.
func TestPerCallModelOverridesSubagentModel(t *testing.T) {
	var (
		mu     sync.Mutex
		models []string
	)
	prov := observedProvider(&models, &mu, mockllm.TextTurn("done"))
	reg := regForTest(prov, providerAnthropic, "claude-default")
	cfg := Config{Model: "claude-default", SubagentModel: "cheap-model-1.0"}

	factory := buildSubagentEngineFactory(cfg, reg, prov, providerAnthropic, "claude-default", nil)
	eng, ok := factory(catAnthropicModel)
	if !ok || eng == nil {
		t.Fatalf("factory(%q) = (%v, %v), want a non-nil engine", catAnthropicModel, eng, ok)
	}
	if got := eng.ContextWindow(); got != catAnthropicCtx {
		t.Fatalf("per-call child ContextWindow = %d, want the PER-CALL model's window %d", got, catAnthropicCtx)
	}
	drainEngine(t, eng)
	mu.Lock()
	defer mu.Unlock()
	if len(models) == 0 || models[0] != catAnthropicModel {
		t.Fatalf("per-call child LLM request models = %v, want the per-call override %q (per-call > SubagentModel)", models, catAnthropicModel)
	}
}

// TestPerDefModelOverridesSubagentModel: a def pinning its own model wins over the
// global SubagentModel at the resolveChildProvider seam (the def-resolved engine
// path), while an unpinned def falls through to the global default. The pure
// resolveModel half is already covered by TestResolveModelPrecedence; this
// exercises the same chain one seam higher (the one buildAgentSubagentEngines /
// buildMemberEngine actually call).
func TestPerDefModelOverridesSubagentModel(t *testing.T) {
	prov := mockllm.New()
	reg := regForTest(prov, providerAnthropic, "parent-model")
	cfg := Config{Model: "parent-model", SubagentModel: "global-sub-1.0"}

	_, _, model, _ := resolveChildProvider(cfg, reg,
		agents.AgentDef{Name: "pinned", Model: "pinned-id-1.0"}, prov, providerAnthropic, "parent-model")
	if model != "pinned-id-1.0" {
		t.Fatalf("def-pinned model resolved to %q, want %q (def.Model > SubagentModel)", model, "pinned-id-1.0")
	}

	_, _, model, _ = resolveChildProvider(cfg, reg,
		agents.AgentDef{Name: "plain"}, prov, providerAnthropic, "parent-model")
	if model != "global-sub-1.0" {
		t.Fatalf("unpinned def resolved to %q, want the global SubagentModel %q", model, "global-sub-1.0")
	}
}

// TestResolveChildProviderSameProviderModelWindow pins the panel fix to the ONE
// shared window rule (childWindowFor) at the resolveChildProvider seam: a
// SAME-provider def `model:` (no provider switch) re-derives the child's context
// window from ITS catalogued model — the pre-fix code derived the window only on
// a provider SWITCH, so a same-provider cheap def kept the parent default (0 ⇒
// 128k) and compacted on the wrong window. The full-inherit def stays at 0.
func TestResolveChildProviderSameProviderModelWindow(t *testing.T) {
	prov := mockllm.New()
	reg := regForTest(prov, providerAnthropic, "claude-default")
	cfg := Config{Model: "claude-default"}

	// Same-provider def model with a catalogued 200k window => window re-derived.
	_, pid, model, windowFn := resolveChildProvider(cfg, reg,
		agents.AgentDef{Name: "cheap", Model: catAnthropicModel}, prov, providerAnthropic, "claude-default")
	if pid != providerAnthropic || model != catAnthropicModel {
		t.Fatalf("resolved (provider, model) = (%q, %q), want (%q, %q)", pid, model, providerAnthropic, catAnthropicModel)
	}
	if window := windowFn(); window != catAnthropicCtx {
		t.Fatalf("same-provider def-model window = %d, want the def model's catalogued window %d (window must key on model change, not only provider switch)",
			window, catAnthropicCtx)
	}

	// Full inherit (no def model, no SubagentModel) over an uncatalogued parent model
	// => the resolver floors to defaultContextWindowTokens (128k). (Under the old
	// eager-int rule this returned 0, which engineDepsForProvider then floored to the
	// SAME 128k — the resolve-at-use closure folds that floor in.)
	_, _, model, windowFn = resolveChildProvider(cfg, reg,
		agents.AgentDef{Name: "plain"}, prov, providerAnthropic, "claude-default")
	if window := windowFn(); model != "claude-default" || window != defaultContextWindowTokens {
		t.Fatalf("full-inherit def resolved (model, window) = (%q, %d), want (claude-default, %d)", model, window, defaultContextWindowTokens)
	}

	// A def inheriting a configured SubagentModel (no def model:) re-derives too —
	// the same rule, exercised through the SubagentModel tier of the chain.
	subCfg := Config{Model: "claude-default", SubagentModel: catAnthropicModel}
	_, _, model, windowFn = resolveChildProvider(subCfg, reg,
		agents.AgentDef{Name: "plain"}, prov, providerAnthropic, "claude-default")
	if window := windowFn(); model != catAnthropicModel || window != catAnthropicCtx {
		t.Fatalf("SubagentModel-inheriting def resolved (model, window) = (%q, %d), want (%q, %d)",
			model, window, catAnthropicModel, catAnthropicCtx)
	}
}

// TestExplorerPromptKeysDeltaOnSubagentModel mirrors
// TestAgentPromptConfigKeysDeltaOnResolvedModel for the DEF-LESS explorer: on a
// cheap SubagentModel the explorer's prompt keys Env.Model on the CHEAP model and
// still carries the agency delta + the References convention.
func TestExplorerPromptKeysDeltaOnSubagentModel(t *testing.T) {
	cfg := Config{Model: "gpt-x", SubagentModel: "claude-cheap-1.0"}
	deps := childExplorerDeps(cfg, nil, mockllm.New(), "", cfg.Model, nil)

	pc := deps.PromptConfig
	if pc.Env.Model != "claude-cheap-1.0" {
		t.Fatalf("explorer Env.Model = %q, want the cheap SubagentModel (the model the child RUNS on)", pc.Env.Model)
	}
	if !strings.Contains(pc.Role, "Keep going until the task is actually resolved") {
		t.Fatalf("explorer Role missing the agency delta\nRole=%q", pc.Role)
	}
	if !strings.Contains(pc.Role, "References:") {
		t.Fatalf("explorer Role missing the References convention\nRole=%q", pc.Role)
	}
}

// --- normalizeSubagentModel fail-fast posture (panel MUST-FIX) -----------------
//
// A non-empty --subagent-model that does not resolve to a usable model id is a
// BUILD ERROR, never a warn-and-inert no-op: an inert override silently runs the
// whole child fleet on the EXPENSIVE parent model — the opposite of the flag's
// purpose. The error must name the flag, the value, and why it didn't resolve.

// TestNormalizeSubagentModelUnresolvableIsError covers both dead-selector shapes:
// an unknown BARE alias, a built-in alias that means inherit (sonnet/opus/haiku),
// and an operator alias mapped to an empty id.
func TestNormalizeSubagentModelUnresolvableIsError(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"unknown bare alias", Config{SubagentModel: "zippy"}},
		{"builtin inherit alias", Config{SubagentModel: "sonnet"}},
		{"operator alias mapped to empty", Config{SubagentModel: "fast", ModelAliases: map[string]string{"fast": ""}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeSubagentModel(tc.cfg)
			if err == nil {
				t.Fatalf("normalizeSubagentModel(%q) = (%q, nil), want a fail-fast error (an inert override runs the fleet on the parent model)",
					tc.cfg.SubagentModel, got)
			}
			if !strings.Contains(err.Error(), "--subagent-model") {
				t.Errorf("error must name the flag --subagent-model, got: %v", err)
			}
			if !strings.Contains(err.Error(), tc.cfg.SubagentModel) {
				t.Errorf("error must name the given value %q, got: %v", tc.cfg.SubagentModel, err)
			}
		})
	}
}

// TestBuildFailsOnUnresolvableSubagentModel is the production-wiring half: the
// REAL app.Build must propagate the normalizeSubagentModel error (fail-fast at
// startup, the --agent-source-url posture), not swallow it.
func TestBuildFailsOnUnresolvableSubagentModel(t *testing.T) {
	for _, bad := range []string{"zippy", "sonnet"} {
		built, err := Build(context.Background(), Config{
			Workspace:     t.TempDir(),
			Model:         "mock",
			UseMock:       true,
			SubagentModel: bad,
		})
		if err == nil {
			built.Close()
			t.Fatalf("Build(SubagentModel=%q) succeeded, want a fail-fast startup error", bad)
		}
		if !strings.Contains(err.Error(), "--subagent-model") || !strings.Contains(err.Error(), bad) {
			t.Errorf("Build error must name the flag and the value %q, got: %v", bad, err)
		}
	}
}

// TestNormalizeSubagentModelKeepsConcreteIDVerbatim: a valid concrete id is kept
// verbatim (modulo whitespace trim) and the build-once INFO fires exactly once.
func TestNormalizeSubagentModelKeepsConcreteIDVerbatim(t *testing.T) {
	diag := newCapturingDiagnostics()
	got, err := normalizeSubagentModel(Config{SubagentModel: " gpt-5-mini ", Diagnostics: diag})
	if err != nil {
		t.Fatalf("normalizeSubagentModel(concrete id): %v", err)
	}
	if got != "gpt-5-mini" {
		t.Fatalf("normalizeSubagentModel = %q, want the trimmed id kept verbatim (%q)", got, "gpt-5-mini")
	}
	if n := diag.countContaining("subagent default model ACTIVE"); n != 1 {
		t.Fatalf("active-override INFO emitted %d times, want exactly 1 (build-once fact)", n)
	}
}

// TestNormalizeSubagentModelKeepsOperatorAliasVerbatim pins the documented
// verbatim semantics for an operator-defined alias: lookupModelAlias maps it to
// the concrete id (so it VALIDATES), but normalize returns the ALIAS itself —
// per-child resolution (resolveModelFor/resolveDefaultChildModel) maps it
// silently downstream.
func TestNormalizeSubagentModelKeepsOperatorAliasVerbatim(t *testing.T) {
	cfg := Config{SubagentModel: "fast", ModelAliases: map[string]string{"fast": "gpt-4o-mini"}}
	got, err := normalizeSubagentModel(cfg)
	if err != nil {
		t.Fatalf("normalizeSubagentModel(operator alias): %v", err)
	}
	if got != "fast" {
		t.Fatalf("normalizeSubagentModel = %q, want the alias kept verbatim (%q)", got, "fast")
	}
	// The per-child def-less chain resolves the kept alias to the concrete id.
	model, _ := resolveDefaultChildModel(cfg, nil, providerMock, "parent-model")
	if model != "gpt-4o-mini" {
		t.Fatalf("resolveDefaultChildModel over the kept alias = %q, want the mapped concrete id %q", model, "gpt-4o-mini")
	}
}

// TestBuildNarratesSubagentModelExactlyOnce mirrors
// TestBuildConfigFactsLogOnceAcrossChildDerivations for the new fact: the REAL
// Build narrates the active child-default model EXACTLY ONCE through the injected
// Diagnostics, and a per-session (selector) derivation — which mints fresh child
// engines through the same builders — adds zero repeats.
func TestBuildNarratesSubagentModelExactlyOnce(t *testing.T) {
	diag := newCapturingDiagnostics()
	built, err := Build(context.Background(), Config{
		Workspace:     t.TempDir(),
		Model:         "mock",
		UseMock:       true,
		SubagentModel: "cheap-model-1.0",
		Diagnostics:   diag,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	// A selector session forces a PER-SESSION engine (fresh child engines); it must
	// not re-narrate the build-once fact.
	if _, err := built.Service.CreateSessionWithProvider(context.Background(), t.TempDir(),
		session.ModeDefault, session.Limits{}, server.ProviderSelector{ProviderID: providerMock}); err != nil {
		t.Fatalf("CreateSessionWithProvider(selector): %v", err)
	}

	if n := diag.countContaining("subagent default model ACTIVE"); n != 1 {
		t.Fatalf("active-override INFO emitted %d times, want exactly 1 (0 = the Build call dropped; >1 = per-derivation re-narration)", n)
	}
}

// TestRegisterParallelToolThreadsSubagentModel drives the REAL registration seam
// (registerParallelTool — the call path the deps-level tests above don't see):
// the registered Parallel tool's BRANCH child must carry the configured
// SubagentModel on its LLM request, proving registerParallelTool threads cfg +
// the registry + the session model together into buildParallelChildEngine. A
// revert to the pre-#35 modelCfgFor(cfg, s.model) wiring runs the branch on the
// session model and fails here. The branch WINDOW through this seam is asserted
// one level down (TestParallelBranchUsesSubagentModel over parallelChildDeps):
// ParallelTool does not expose its child engine, and widening engine/agent's API
// with an accessor only for this probe isn't worth it.
func TestRegisterParallelToolThreadsSubagentModel(t *testing.T) {
	var (
		mu     sync.Mutex
		models []string
	)
	prov := observedProvider(&models, &mu, mockllm.TextTurn("branch done"))
	cfg := Config{Workspace: t.TempDir(), Model: "parent-model", SubagentModel: catAnthropicModel, EnableParallel: true}
	reg := regForTest(prov, providerAnthropic, "parent-model")

	cat := tool.NewCatalog()
	registerParallelTool(context.Background(), cfg, cat, reg, nil, hookexec.New(nil),
		catalogAssets{forkReaper: agent.NewLRUForkReaper(1)},
		catalogSession{provider: prov, providerID: providerAnthropic, model: "parent-model"})

	pt, ok := cat.Lookup("Parallel")
	if !ok {
		t.Fatal("registerParallelTool did not register the Parallel tool")
	}
	ws, err := osfs.NewWorkspace(cfg.Workspace)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	call := session.NewToolCall("c1", "Parallel", json.RawMessage(`{"tasks":["probe"],"join":"first"}`))
	res, err := pt.Execute(context.Background(), call, testEnvironment(ws, nil))
	if err != nil {
		t.Fatalf("Parallel.Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("Parallel result is an error: %s", res.Content)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(models) == 0 || models[0] != catAnthropicModel {
		t.Fatalf("branch LLM request models = %v, want the SubagentModel %q (registerParallelTool must thread the def-less default into the branch engine)",
			models, catAnthropicModel)
	}
}

// TestRegisterParallelToolThreadsStore drives the REAL registration seam
// (registerParallelTool) and proves the SHARED session store reaches the ParallelTool
// (issue #30): a branch run under composition persists, and the persisted branch is
// loadable both directly from the store AND through the InspectSubagent tool by the
// surfaced "branch id:". A revert that drops agent.WithParallelStore(store) from
// registerParallelTool fails here (the branch is never persisted).
func TestRegisterParallelToolThreadsStore(t *testing.T) {
	store := memstore.New()
	prov := mockllm.New(mockllm.TextTurn("branch done"))
	cfg := Config{Workspace: t.TempDir(), Model: "parent-model", EnableParallel: true}
	reg := regForTest(prov, providerAnthropic, "parent-model")

	cat := tool.NewCatalog()
	registerParallelTool(context.Background(), cfg, cat, reg, store, hookexec.New(nil),
		catalogAssets{forkReaper: agent.NewLRUForkReaper(1)},
		catalogSession{provider: prov, providerID: providerAnthropic, model: "parent-model"})

	pt, ok := cat.Lookup("Parallel")
	if !ok {
		t.Fatal("registerParallelTool did not register the Parallel tool")
	}
	ws, err := osfs.NewWorkspace(cfg.Workspace)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	call := session.NewToolCall("c1", "Parallel", json.RawMessage(`{"tasks":["probe"]}`))
	res, err := pt.Execute(context.Background(), call, testEnvironment(ws, nil))
	if err != nil {
		t.Fatalf("Parallel.Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("Parallel result is an error: %s", res.Content)
	}

	// 1. The branch persisted under its deterministic id.
	const branchID = "parallel-c1-0"
	if !strings.Contains(res.Content, "branch id: "+branchID) {
		t.Fatalf("result missing the surfaced branch id %q:\n%s", branchID, res.Content)
	}
	if _, lerr := store.Load(context.Background(), session.SessionID(branchID)); lerr != nil {
		t.Fatalf("branch %q not persisted (store did not reach the ParallelTool): %v", branchID, lerr)
	}

	// 2. InspectSubagent (over the SAME store) loads the branch transcript by that id.
	inspect := agent.NewInspectSubagentTool(store)
	ires, err := inspect.Execute(context.Background(),
		session.NewToolCall("i1", "InspectSubagent", json.RawMessage(`{"agent_id":"`+branchID+`"}`)), testEnvironment(ws, nil))
	if err != nil {
		t.Fatalf("InspectSubagent.Execute: %v", err)
	}
	if ires.IsError {
		t.Fatalf("InspectSubagent must load the persisted branch by id, got error: %q", ires.Content)
	}
	if !strings.Contains(ires.Content, "branch done") {
		t.Fatalf("InspectSubagent result is not the branch transcript: %q", ires.Content)
	}
}

// TestSubagentModelRoutesChildToCheapModel is the end-to-end proof through the REAL
// composition (app.Build → server.Service, the providerConstructor mock seam — see
// docs/adr/0016-multi-provider.md §2): with --subagent-model configured, a turn that
// delegates to the default Subagent explorer sends the CHILD's LLM request with the
// cheap model while the PARENT's requests stay on the session model. All offline.
func TestSubagentModelRoutesChildToCheapModel(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	const cheapModel = "gpt-5-mini"

	var (
		mu     sync.Mutex
		models []string
	)
	built, err := Build(ctx, Config{
		Workspace:     workspace,
		NoSoul:        true,
		Model:         "gpt-5",
		SubagentModel: cheapModel,
		AllowAllTools: true, // auto-approve so the delegation turn runs unattended
		envDetector:   fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-x"}),
		// Strictly offline: refuse any (keyed) live model fetch ⇒ embedded floor.
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			// ONE mock per provider id; parent and explorer child share it (shared
			// cursor): parent turn 1 → Subagent call, child turn → summary, parent
			// turn 2 → final. The observer records each request's Model.
			return mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
				mu.Lock()
				models = append(models, r.Model)
				mu.Unlock()
			})},
				mockllm.ToolCallTurn(session.NewToolCall("c1", "Subagent", []byte(`{"prompt":"explore"}`))),
				mockllm.TextTurn("CHILD SUMMARY"),
				mockllm.TextTurn("parent done"),
			)
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	svc := built.Service

	sess, err := svc.CreateSession(ctx, workspace, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainRun(run); got != "parent done" {
		t.Fatalf("terminal text = %q, want the parent's final turn (the delegation must complete)", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(models) < 3 {
		t.Fatalf("recorded %d LLM requests (%v), want parent → child → parent", len(models), models)
	}
	if models[0] != "gpt-5" {
		t.Fatalf("parent request model = %q, want the session model gpt-5 (models=%v)", models[0], models)
	}
	var sawCheap bool
	for _, m := range models {
		if m == cheapModel {
			sawCheap = true
		}
	}
	if !sawCheap {
		t.Fatalf("no LLM request carried the configured SubagentModel %q — the default explorer ignored it (models=%v)", cheapModel, models)
	}
	if last := models[len(models)-1]; last != "gpt-5" {
		t.Fatalf("final (parent) request model = %q, want gpt-5 (models=%v)", last, models)
	}
}

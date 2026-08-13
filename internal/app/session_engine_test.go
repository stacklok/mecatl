package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/providercatalog"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// fakeSink records nothing; it exists only so a test can pass a non-nil EventSink
// and assert it threads through baseEngineDeps onto the per-session engine.
type fakeSink struct{}

func (fakeSink) Emit(context.Context, session.Event) {}

// configWithCollaborators returns a Config that turns ON the optional collaborators
// whose silent loss the [High] review flagged: a cascade compactor (a DISTINCT type
// from NewEngine's HeuristicCompactor default), the tiktoken counter, and slash
// commands (so CommandExpander is a real DirCommandExpander, not the NoopExpander).
func configWithCollaborators() Config {
	return Config{
		Model:          "test-model",
		Compaction:     "cascade",
		Tokenizer:      "tiktoken",
		EnableCommands: true,
		Sink:           fakeSink{},
	}
}

// TestBaseEngineDepsCarriesFullCollaboratorSet asserts the shared deps builder
// populates EVERY collaborator the main engine needs — the drift guard the [High]
// review required. Because both buildEngine and sessionEngineFactory build their
// agent.Deps through baseEngineDeps, a populated result here proves the per-session
// engine cannot silently drop the compactor, token counter, command expander,
// store, sink, logger, policy, or hooks.
func TestBaseEngineDepsCarriesFullCollaboratorSet(t *testing.T) {
	cfg := configWithCollaborators()
	provider := mockllm.New(mockllm.TextTurn("x"))
	store := memstore.New()
	policy := permpolicy.NewPolicy(defaultRules(), nil)
	hooks := hookexec.New(nil)

	// "test-model" is uncatalogued, so contextWindowFor → 0 → the 128k floor; this
	// keeps the floor assertion below honest as the genuinely-unknown-model case.
	reg := regForTest(provider, providerOpenAI, cfg.Model)
	deps := baseEngineDeps(cfg, reg, provider, store, policy, hooks, nil, prompt.RootAssembler{})

	if deps.Instructions == nil {
		t.Fatal("Instructions is nil — turn-0 project instructions / memory index would not assemble")
	}
	if deps.Compactor == nil {
		t.Fatal("Compactor is nil — per-session engine would never compact")
	}
	if _, ok := deps.Compactor.(agent.CascadeCompactor); !ok {
		t.Fatalf("Compactor = %T, want the configured agent.CascadeCompactor (not NewEngine's default)", deps.Compactor)
	}
	if deps.TokenCounter == nil {
		t.Fatal("TokenCounter is nil — compaction trigger would have no counter")
	}
	if deps.CommandExpander == nil {
		t.Fatal("CommandExpander is nil")
	}
	if _, isNoop := deps.CommandExpander.(prompt.NoopExpander); isNoop {
		t.Fatal("CommandExpander is the NoopExpander — slash commands would silently stop expanding")
	}
	if deps.Store == nil {
		t.Fatal("Store is nil — weaker durability")
	}
	if deps.Sink == nil {
		t.Fatal("Sink is nil — operator observability would not fire")
	}
	if deps.Policy == nil {
		t.Fatal("Policy is nil")
	}
	if deps.Hooks == nil {
		t.Fatal("Hooks is nil")
	}
	// The fixture's "test-model" is not in the catalog, so the default path falls
	// back to the 128k floor — the genuinely-unknown case (issue #63). A CATALOGUED
	// default model's real window is exercised by TestBaseEngineDepsResolvesDefaultModelWindow.
	if deps.ContextWindow() != defaultContextWindowTokens {
		t.Fatalf("ContextWindowTokens = %d, want %d (uncatalogued model floor)", deps.ContextWindow(), defaultContextWindowTokens)
	}
	if deps.CompactionRatio != defaultCompactionRatio {
		t.Fatalf("CompactionRatio = %v, want %v", deps.CompactionRatio, defaultCompactionRatio)
	}
}

// TestBaseEngineDepsResolvesDefaultModelWindow is the issue #63 regression guard: the
// DEFAULT-model engine deps must carry the model's REAL context window (resolved
// live-first via reg.meta.contextWindowFor), not the hardcoded 128k floor. Before the
// fix, baseEngineDeps passed contextWindow=0, so a 1M-context default model (gpt-5.5,
// catalogued at 1,050,000) compacted at ~102k. nil-meta test registry → catalog floor,
// which carries the real window.
func TestBaseEngineDepsResolvesDefaultModelWindow(t *testing.T) {
	const gpt55Ctx = 1_050_000
	cfg := Config{Model: "gpt-5.5"}
	provider := mockllm.New()
	reg := regForTest(provider, providerOpenAI, cfg.Model)

	deps := baseEngineDeps(cfg, reg, provider, memstore.New(),
		permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{})

	if deps.ContextWindow() != gpt55Ctx {
		t.Fatalf("default-model ContextWindowTokens = %d, want %d (issue #63: real per-model window, not the 128k floor)", deps.ContextWindow(), gpt55Ctx)
	}
}

// TestBaseEngineDepsContextWindowOverrideWins guards the precedence rule: an operator
// --context-window-override (Config.ContextWindowOverride) beats the resolved per-model
// window on the DEFAULT path too, exactly as on the selector path.
func TestBaseEngineDepsContextWindowOverrideWins(t *testing.T) {
	const override = 64_000
	cfg := Config{Model: "gpt-5.5", ContextWindowOverride: override}
	provider := mockllm.New()
	reg := regForTest(provider, providerOpenAI, cfg.Model)

	deps := baseEngineDeps(cfg, reg, provider, memstore.New(),
		permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{})

	if deps.ContextWindow() != override {
		t.Fatalf("ContextWindowTokens = %d, want the override %d (operator knob must win over the resolved 1,050,000 window)", deps.ContextWindow(), override)
	}
}

// TestBaseEngineDepsUnknownModelFloors guards the floor: a genuinely-unknown /
// uncatalogued default model resolves to 0 and falls back to defaultContextWindowTokens
// (128k) — the conservative floor preserved by the issue #63 fix.
func TestBaseEngineDepsUnknownModelFloors(t *testing.T) {
	cfg := Config{Model: "totally-made-up-model-xyz"}
	provider := mockllm.New()
	reg := regForTest(provider, providerOpenAI, cfg.Model)

	deps := baseEngineDeps(cfg, reg, provider, memstore.New(),
		permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{})

	if deps.ContextWindow() != defaultContextWindowTokens {
		t.Fatalf("unknown-model ContextWindowTokens = %d, want the %d floor", deps.ContextWindow(), defaultContextWindowTokens)
	}
}

// TestSessionEngineFactoryBuildsUsableEngine asserts the per-session factory builds
// a non-nil engine and a non-nil close even with NO MCP specs reachable (the
// best-effort path), proving it wires through baseEngineDeps. The factory is given
// an empty spec list so it connects nothing (offline — no real MCP server), and we
// assert it still returns a usable engine + close.
func TestSessionEngineFactoryBuildsUsableEngine(t *testing.T) {
	cfg := configWithCollaborators()
	provider := mockllm.New(mockllm.TextTurn("x"))
	store := memstore.New()
	policy := permpolicy.NewPolicy(defaultRules(), nil)
	hooks := hookexec.New(nil)

	reg := &providerRegistry{
		entries:   map[string]providerEntry{providerMock: {id: providerMock, provider: provider, available: true}},
		defaultID: providerMock,
	}
	factory := sessionEngineFactory(cfg, reg, provider, store, policy, hooks, nil, prompt.RootAssembler{}, catalogAssets{}, nil)

	// Zero selector + no specs: the per-session engine binds the DEFAULT provider.
	res, err := factory(context.Background(), server.ProviderSelector{}, []mcp.ServerConfig{}, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	eng, closeFn := res.Engine, res.Close
	if eng == nil {
		t.Fatal("factory returned a nil engine")
	}
	if closeFn == nil {
		t.Fatal("factory returned a nil close")
	}
	if cerr := closeFn(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
}

// twoProviderFactory builds a sessionEngineFactory over a registry with TWO
// DISTINCT mock-backed providers (openai / openrouter), each returning an
// identifying canned reply, so a routing test can assert WHICH provider the
// selected engine bound. The default provider is openai's mock.
func twoProviderFactory(t *testing.T) (server.SessionEngineFactory, *providerRegistry) {
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
	factory := sessionEngineFactory(cfg, reg, oa, store, policy, hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)
	return factory, reg
}

// runFactoryEngine builds an engine via the factory for sel, drives one turn, and
// returns the terminal text (so a test can assert the bound provider's reply).
func runFactoryEngine(t *testing.T, factory server.SessionEngineFactory, sel server.ProviderSelector) string {
	t.Helper()
	res, err := factory(context.Background(), sel, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory(%+v): %v", sel, err)
	}
	eng, closeFn := res.Engine, res.Close
	defer func() { _ = closeFn() }()
	sess := session.New("s1", session.ModeDefault, "/ws", session.Limits{MaxTurns: 5}, time.Now())
	ws := memfs.NewWorkspace("/ws")
	return drainRun(eng.Run(context.Background(), sess, ws, agent.RunRequest{Text: "hi", Parts: nil}))
}

// TestSessionEngineFactoryUnknownProvider: an unknown/unavailable provider id is a
// loud error wrapping server.ErrInvalidArgument, naming the id — never a silent
// fallback to the default.
func TestSessionEngineFactoryUnknownProvider(t *testing.T) {
	factory, _ := twoProviderFactory(t)
	_, err := factory(context.Background(), server.ProviderSelector{ProviderID: "anthropic"}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err == nil {
		t.Fatal("expected an error for an unknown provider id, got nil")
	}
	if !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("error = %v, want it to wrap server.ErrInvalidArgument", err)
	}
	if !strings.Contains(err.Error(), "anthropic") {
		t.Fatalf("error %q does not name the unknown provider id", err.Error())
	}
}

// TestSessionEngineFactorySelectsProviderDeps: selecting "openrouter" binds the
// OpenRouter provider — NOT the default (openai) — proving the factory routes the
// SELECTED provider's Deps. (Contamination guard at the factory level.)
func TestSessionEngineFactorySelectsProviderDeps(t *testing.T) {
	factory, _ := twoProviderFactory(t)
	if got := runFactoryEngine(t, factory, server.ProviderSelector{ProviderID: providerOpenRouter}); got != "OPENROUTER-REPLY" {
		t.Fatalf("selected openrouter but got %q, want the OpenRouter provider's reply", got)
	}
	// And the default (zero selector) routes to openai.
	if got := runFactoryEngine(t, factory, server.ProviderSelector{}); got != "OPENAI-REPLY" {
		t.Fatalf("zero selector got %q, want the default (openai) provider's reply", got)
	}
}

// TestSessionEngineFactoryModelPassthrough: an unknown-to-the-catalog model on an
// available provider flows through THE FACTORY verbatim — sel.ModelID reaches the
// provider's LLMRequest.Model unchanged (a regression that dropped or overrode
// sel.ModelID must fail this). It drives the factory end to end and captures the
// request the engine sends via a mock request observer (NOT a direct
// engineDepsForProvider call — that would only prove the helper, not that the
// factory forwards the selector).
func TestSessionEngineFactoryModelPassthrough(t *testing.T) {
	cfg := Config{Model: "default-model"}
	var gotModel string
	// The mock records the model that reached the provider on the run's LLMRequest.
	oa := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { gotModel = req.Model })},
		mockllm.TextTurn("ok"),
	)
	reg := &providerRegistry{
		entries:   map[string]providerEntry{providerOpenAI: {id: providerOpenAI, provider: oa, available: true}},
		defaultID: providerOpenAI,
	}
	store := memstore.New()
	policy := permpolicy.NewPolicy(defaultRules(), nil)
	factory := sessionEngineFactory(cfg, reg, oa, store, policy, hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)

	const unknownModel = "gpt-5-preview-not-in-catalog"
	res, err := factory(context.Background(),
		server.ProviderSelector{ProviderID: providerOpenAI, ModelID: unknownModel}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	eng, closeFn := res.Engine, res.Close
	defer func() { _ = closeFn() }()
	sess := session.New("s1", session.ModeDefault, "/ws", session.Limits{MaxTurns: 5}, time.Now())
	drainRun(eng.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "hi", Parts: nil}))

	if gotModel != unknownModel {
		t.Fatalf("provider saw model %q, want the verbatim passthrough %q (factory dropped/overrode sel.ModelID)", gotModel, unknownModel)
	}
}

// TestSessionEngineFactoryContextWindowFromCatalog: a selected model with a KNOWN
// catalog context limit produces that ContextWindowTokens on the built engine, and a
// passthrough (uncatalogued) model falls back to the 128k default — proving the
// compaction trigger agrees with the ListModels-advertised context_limit (Medium #2).
func TestSessionEngineFactoryContextWindowFromCatalog(t *testing.T) {
	// Pick a real catalogued openai model + its catalog context limit, so the test
	// asserts against the SAME data ListModels projects.
	cat := providercatalog.Default()
	p, ok := cat.Provider(providerOpenAI)
	if !ok {
		t.Fatal("openai not in catalog")
	}
	var knownModel string
	var knownLimit int
	for _, m := range p.Models() {
		if m.ContextLimit() > 0 {
			knownModel, knownLimit = m.ID(), m.ContextLimit()
			break
		}
	}
	if knownModel == "" {
		t.Skip("no catalogued openai model with a positive context limit")
	}

	factory, _ := twoProviderFactory(t)

	// Known model ⇒ the engine's window is the catalog limit.
	res, err := factory(context.Background(),
		server.ProviderSelector{ProviderID: providerOpenAI, ModelID: knownModel}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory(known): %v", err)
	}
	eng, closeFn := res.Engine, res.Close
	defer func() { _ = closeFn() }()
	if got := eng.ContextWindow(); got != knownLimit {
		t.Fatalf("ContextWindowTokens = %d, want the catalog limit %d for %q", got, knownLimit, knownModel)
	}

	// Passthrough (uncatalogued) model ⇒ the 128k default fallback.
	resPT, err := factory(context.Background(),
		server.ProviderSelector{ProviderID: providerOpenAI, ModelID: "totally-made-up-model"}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory(passthrough): %v", err)
	}
	engPT, closePT := resPT.Engine, resPT.Close
	defer func() { _ = closePT() }()
	if got := engPT.ContextWindow(); got != defaultContextWindowTokens {
		t.Fatalf("passthrough ContextWindowTokens = %d, want the %d default fallback", got, defaultContextWindowTokens)
	}
}

// TestSelectorEngineWindowSelfCorrectsAtUse is THE bug the per-path band-aids missed:
// a SELECTOR session's per-session engine resolves its compaction window LIVE at the
// point of use, so a live-catalog Swap AFTER the engine is built self-corrects the
// SAME engine WITHOUT a rebuild. The selector model is live-only (catalog floor 0 → the
// engine reports the 128k floor pre-swap); after Swap'ing a 1,050,000 live entry, the
// SAME engine's ContextWindow() jumps to 1,050,000.
//
// MUTATION-VERIFY: revert the engine to a frozen window (resolve once in
// engineDepsForProvider / read once in NewEngine) and the post-Swap read below stays
// at the 128k floor — this fails.
func TestSelectorEngineWindowSelfCorrectsAtUse(t *testing.T) {
	const (
		liveModel  = "openai/gpt-5.4" // live-only: NOT in the embedded catalog
		liveWindow = 1_050_000
	)
	cfg := Config{Model: "default-model"}
	oa := mockllm.New(mockllm.TextTurn("OPENAI-REPLY"))
	meta := newLiveMetaStore() // catalog-only: NO entry for the live-only model
	reg := &providerRegistry{
		entries:   map[string]providerEntry{providerOpenAI: {id: providerOpenAI, provider: oa, available: true}},
		defaultID: providerOpenAI,
		meta:      meta,
	}
	store := memstore.New()
	policy := permpolicy.NewPolicy(defaultRules(), nil)
	factory := sessionEngineFactory(cfg, reg, oa, store, policy, hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)

	res, err := factory(context.Background(),
		server.ProviderSelector{ProviderID: providerOpenAI, ModelID: liveModel}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory(selector): %v", err)
	}
	eng, closeFn := res.Engine, res.Close
	defer func() { _ = closeFn() }()

	// Pre-swap: the live-only model is uncatalogued → the resolver floors to 128k.
	if got := eng.ContextWindow(); got != defaultContextWindowTokens {
		t.Fatalf("pre-swap selector engine ContextWindow() = %d, want the %d floor (live-only model)", got, defaultContextWindowTokens)
	}

	// THE LIVE SWAP, AFTER the engine was built. The SAME engine must self-correct.
	reg.meta.Swap(map[string][]modelEntry{
		providerOpenAI: {{ID: liveModel, ContextLimit: liveWindow}},
	})
	if got := eng.ContextWindow(); got != liveWindow {
		t.Fatalf("post-swap selector engine ContextWindow() = %d, want the live %d WITHOUT a rebuild (resolve-at-use)", got, liveWindow)
	}
}

// TestSharedAndSelectorEngineResolveSameSource is the anti-drift guard: the SHARED
// (default) engine and a SELECTOR engine for the SAME model both resolve their
// compaction window through the SAME live-first contextWindowFor source — so mutating
// the live store moves BOTH. A regression that gave one a frozen window and the other a
// live one would diverge here.
func TestSharedAndSelectorEngineResolveSameSource(t *testing.T) {
	const (
		model      = "openai/gpt-5.4"
		liveWindow = 900_000
	)
	cfg := Config{Model: model} // the DEFAULT model is the same live-only model
	oa := mockllm.New(mockllm.TextTurn("REPLY"))
	meta := newLiveMetaStore()
	reg := &providerRegistry{
		entries:      map[string]providerEntry{providerOpenAI: {id: providerOpenAI, provider: oa, available: true}},
		defaultID:    providerOpenAI,
		defaultModel: model,
		meta:         meta,
	}
	store := memstore.New()
	policy := permpolicy.NewPolicy(defaultRules(), nil)

	shared := agent.NewEngine(baseEngineDeps(cfg, reg, oa, store, policy, hookexec.New(nil), nil, prompt.RootAssembler{}))
	factory := sessionEngineFactory(cfg, reg, oa, store, policy, hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)
	res, err := factory(context.Background(),
		server.ProviderSelector{ProviderID: providerOpenAI, ModelID: model}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory(selector): %v", err)
	}
	defer func() { _ = res.Close() }()
	selector := res.Engine

	// Both floor pre-swap.
	if sw, se := shared.ContextWindow(), selector.ContextWindow(); sw != defaultContextWindowTokens || se != defaultContextWindowTokens {
		t.Fatalf("pre-swap windows shared=%d selector=%d, want both %d", sw, se, defaultContextWindowTokens)
	}
	// One Swap moves BOTH (same source).
	reg.meta.Swap(map[string][]modelEntry{
		providerOpenAI: {{ID: model, ContextLimit: liveWindow}},
	})
	if sw, se := shared.ContextWindow(), selector.ContextWindow(); sw != liveWindow || se != liveWindow {
		t.Fatalf("post-swap windows shared=%d selector=%d, want both the live %d (one contextWindowFor source feeds both)", sw, se, liveWindow)
	}
}

// TestConfiguredContextWindowReachesSharedSelectorAndExplorerEngines exercises the
// production constructors for the shared engine, a selector-created per-session
// engine, and the common anonymous Subagent explorer. All three must retain the
// exact configured provider/model window as their live compaction resolver.
func TestConfiguredContextWindowReachesSharedSelectorAndExplorerEngines(t *testing.T) {
	const (
		model  = "vendor/final-routing-id"
		window = 654_321
	)
	cfg := Config{
		Model:          model,
		contextWindows: map[string]map[string]int{providerOpenAI: {model: window}},
	}
	provider := mockllm.New(mockllm.TextTurn("REPLY"))
	reg := &providerRegistry{
		entries:      map[string]providerEntry{providerOpenAI: {id: providerOpenAI, provider: provider, available: true}},
		defaultID:    providerOpenAI,
		defaultModel: model,
		meta:         newLiveMetaStore(),
	}
	store := memstore.New()
	policy := permpolicy.NewPolicy(defaultRules(), nil)

	shared := agent.NewEngine(baseEngineDeps(cfg, reg, provider, store, policy, hookexec.New(nil), nil, prompt.RootAssembler{}))
	factory := sessionEngineFactory(cfg, reg, provider, store, policy, hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)
	res, err := factory(context.Background(), server.ProviderSelector{ProviderID: providerOpenAI, ModelID: model}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory(selector): %v", err)
	}
	defer func() { _ = res.Close() }()
	explorer := buildChildEngine(cfg, reg, provider, providerOpenAI, model, nil)

	if got := shared.ContextWindow(); got != window {
		t.Fatalf("shared engine ContextWindow() = %d, want configured %d", got, window)
	}
	if got := res.Engine.ContextWindow(); got != window {
		t.Fatalf("selector engine ContextWindow() = %d, want configured %d", got, window)
	}
	if got := explorer.ContextWindow(); got != window {
		t.Fatalf("explorer engine ContextWindow() = %d, want configured %d", got, window)
	}
}

// TestSessionEngineFactorySelectorMCPCoexist: a non-default selector AND a
// (best-effort, offline) spec list resolve in ONE factory call — the engine binds
// the SELECTED provider and the call returns a usable engine + close (the MCP path
// is best-effort, so an unreachable server still yields a core-tool engine). Proves
// sel and specs are orthogonal inputs to one engine over one catalog.
func TestSessionEngineFactorySelectorMCPCoexist(t *testing.T) {
	factory, _ := twoProviderFactory(t)
	// One spec to an unreachable URL: best-effort connect logs-and-skips, the engine
	// is still built (core tools only) and bound to the SELECTED provider.
	res, err := factory(context.Background(),
		server.ProviderSelector{ProviderID: providerOpenRouter},
		[]mcp.ServerConfig{{Name: "docs", URL: "https://127.0.0.1:0/mcp"}}, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory(sel+specs): %v", err)
	}
	eng, closeFn := res.Engine, res.Close
	if eng == nil || closeFn == nil {
		t.Fatal("factory returned nil engine/close for sel+specs")
	}
	defer func() { _ = closeFn() }()
	// The bound provider is the SELECTED one (openrouter), not the default.
	sess := session.New("s1", session.ModeDefault, "/ws", session.Limits{MaxTurns: 5}, time.Now())
	if got := drainRun(eng.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "hi", Parts: nil})); got != "OPENROUTER-REPLY" {
		t.Fatalf("sel+specs turn routed to %q, want the selected (openrouter) provider's reply", got)
	}
}

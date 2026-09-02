package app

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// countingSessionEngineFactory wraps a real SessionEngineFactory, incrementing
// *calls on each invocation — the does-it-rehydrate probe (mirrors
// rehydrate_selector_test.go's recording factory).
func countingSessionEngineFactory(inner server.SessionEngineFactory, calls *int) server.SessionEngineFactory {
	return func(ctx context.Context, sel server.ProviderSelector, specs []mcp.ServerConfig, profile server.SessionProfile, ws string, mode session.PermissionMode) (server.SessionEngineResult, error) {
		*calls++
		return inner(ctx, sel, specs, profile, ws, mode)
	}
}

// liveWindowReg builds a single-provider registry whose default model is
// LIVE-ONLY: uncatalogued (catalog floor 0 ⇒ contextWindowFor returns 0 pre-swap),
// with an attached meta store the test can Swap to simulate the live model-catalog
// refresh populating a real window. It is the offline analogue of the OpenRouter
// openai/gpt-5.4 case — a default model present in the live listing but absent from
// the embedded catalog, whose build-time baked window WOULD be the 128k compaction
// floor under the old freeze-at-construction scheme.
func liveWindowReg(provider *mockllm.Provider, id, model string) *providerRegistry {
	meta := newLiveMetaStore()
	return &providerRegistry{
		entries:      map[string]providerEntry{id: {id: id, provider: provider, available: true}},
		defaultID:    id,
		defaultModel: model,
		meta:         meta,
	}
}

// defaultLiveWindowService builds a server.Service whose SHARED engine is built
// through the REAL baseEngineDeps (so its Deps.ContextWindow is the live-first
// reg.windowResolver, NOT a frozen scalar — the unification), whose SessionEngine is
// the REAL composition sessionEngineFactory, and whose ResolveContextWindow is the
// SAME live-first resolver Build wires. DefaultResolvedModel carries the baked
// identity; its window is no longer load-bearing (ResolvedModel resolves live-first).
func defaultLiveWindowService(t *testing.T, reg *providerRegistry, provider *mockllm.Provider, model string) (*server.Service, *int) {
	return defaultLiveWindowServiceCfg(t, reg, provider, Config{Model: model})
}

// defaultLiveWindowServiceCfg is defaultLiveWindowService with a caller-supplied
// Config, so a test can set ContextWindowOverride (or any other knob) and have it
// flow through the SAME single windowResolver into BOTH the engine and the echo.
func defaultLiveWindowServiceCfg(t *testing.T, reg *providerRegistry, provider *mockllm.Provider, cfg Config) (*server.Service, *int) {
	t.Helper()
	model := cfg.Model
	store := memstore.New()
	policy := permpolicy.NewPolicy(defaultRules(), nil)
	realFactory := sessionEngineFactory(cfg, reg, provider, store, policy, hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)
	var factoryCalls int
	factory := countingSessionEngineFactory(realFactory, &factoryCalls)

	// The shared engine is built through baseEngineDeps, so it carries the live-first
	// resolver — it RESOLVES the window at the point of use (every maybeCompact /
	// Engine.ContextWindow), never freezing a t=0 floor. baseEngineDeps leaves Catalog
	// unset by contract (the composition caller sets it), so wire one here.
	sharedDeps := baseEngineDeps(cfg, reg, provider, store, policy, hookexec.New(nil), nil, prompt.RootAssembler{})
	sharedDeps.Catalog = tool.NewCatalog()
	shared := agent.NewEngine(sharedDeps)
	svc, err := newTestServerService(server.Config{
		Engine:               shared,
		Store:                store,
		Workspaces:           func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		DefaultLimits:        session.Limits{MaxTurns: 5, MaxToolCalls: 10},
		Now:                  func() time.Time { return time.Unix(0, 0) },
		SessionEngine:        factory,
		DefaultResolvedModel: server.ResolvedModel{ProviderID: reg.Default(), ModelID: model},
		ResolveContextWindow: func(p, m string) int64 { return int64(reg.windowResolver(cfg, p, m)()) },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, &factoryCalls
}

// TestDefaultLiveOnlyModelSelfCorrectsAtUse is THE BUG (issue #66 engine-window fix),
// re-expressed for the resolve-at-use UNIFICATION: a DEFAULT-model session whose model
// is live-only (catalog floor 0) must, AFTER the live model-catalog swap, compact at
// the LIVE window — WITHOUT any rehydration. The shared engine no longer freezes a
// window at construction; it reads reg.windowResolver live on the next turn, so the
// echo heals AND the engine self-corrects with the per-session factory NEVER consulted.
//
// MUTATION-VERIFY: if Deps.ContextWindow is reverted to a frozen int (resolved once at
// construction), the shared engine stays at 0/128k after the Swap and the echo below
// fails — the structural guard the whole fix rests on.
func TestDefaultLiveOnlyModelSelfCorrectsAtUse(t *testing.T) {
	ctx := context.Background()
	const (
		liveModel  = "openai/gpt-5.4" // live-only: NOT in the embedded catalog
		liveWindow = 1_050_000
	)
	provider := mockllm.New(mockllm.TextTurn("PRE-SWAP"), mockllm.TextTurn("POST-SWAP"))
	reg := liveWindowReg(provider, providerOpenAI, liveModel)
	svc, factoryCalls := defaultLiveWindowService(t, reg, provider, liveModel)

	// Pre-swap: the live store has NO entry for the live-only model (catalog floor 0 →
	// the windowResolver returns the 128k default), and the echo reflects that floor.
	if got := reg.meta.contextWindowFor(providerOpenAI, liveModel); got != 0 {
		t.Fatalf("pre-swap contextWindowFor(%q) = %d, want 0 (live-only model, catalog floor)", liveModel, got)
	}

	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if got := svc.ResolvedModel(sess.ID).ContextWindow; got != defaultContextWindowTokens {
		t.Fatalf("pre-swap echo ContextWindow = %d, want the %d floor (live-only, pre-swap)", got, defaultContextWindowTokens)
	}

	// First run pre-swap: rides the shared engine.
	run1, err := svc.StartRunContent(ctx, sess.ID, "first turn", nil)
	if err != nil {
		t.Fatalf("StartRunContent (pre-swap): %v", err)
	}
	if got := drainRun(run1); got != "PRE-SWAP" {
		t.Fatalf("pre-swap reply = %q, want PRE-SWAP (shared engine)", got)
	}

	// THE LIVE SWAP: the background refresh populates the live window for the model.
	reg.meta.Swap(map[string][]modelEntry{
		providerOpenAI: {{ID: liveModel, ContextLimit: liveWindow}},
	})
	if got := reg.meta.contextWindowFor(providerOpenAI, liveModel); got != liveWindow {
		t.Fatalf("post-swap contextWindowFor(%q) = %d, want %d", liveModel, got, liveWindow)
	}

	// Second run post-swap: STILL the shared engine (resolve-at-use self-corrects —
	// the factory is never consulted for a default session).
	run2, err := svc.StartRunContent(ctx, sess.ID, "second turn", nil)
	if err != nil {
		t.Fatalf("StartRunContent (post-swap): %v", err)
	}
	if got := drainRun(run2); got != "POST-SWAP" {
		t.Fatalf("post-swap reply = %q, want POST-SWAP (shared engine, no rehydration)", got)
	}

	// THE KEY ASSERTION: the echo (live-first via the SAME resolver the engine reads)
	// now reports the live window — and the per-session factory was NEVER consulted.
	got := svc.ResolvedModel(sess.ID)
	if got.ContextWindow != liveWindow {
		t.Fatalf("post-swap ResolvedModel.ContextWindow = %d, want the live %d (resolve-at-use; NO rehydration)", got.ContextWindow, liveWindow)
	}
	if got.ProviderID != providerOpenAI || got.ModelID != liveModel {
		t.Fatalf("post-swap identity = %s/%s, want the verbatim default %s/%s", got.ProviderID, got.ModelID, providerOpenAI, liveModel)
	}
	if *factoryCalls != 0 {
		t.Fatalf("session-engine factory called %d times for a default session, want 0 (resolve-at-use needs no rehydration)", *factoryCalls)
	}
	// The DIRECT shared-engine Engine.ContextWindow() self-correction is asserted in
	// internal/adapter/server/resolved_model_test.go (registry-reachable) and at the
	// engine level in engine/agent (the resolve-at-use non-freeze guard).
}

// TestCataloguedDefaultModelEchoesCatalogWindow pins the catalogued-default bound: a
// CATALOGUED default model echoes (and the shared engine compacts at) its catalog
// window with NO rehydration — the per-session factory is never consulted.
func TestCataloguedDefaultModelEchoesCatalogWindow(t *testing.T) {
	ctx := context.Background()
	const (
		model       = "gpt-5"
		bakedWindow = 400_000 // the catalogued window, also what the live store reports
	)
	provider := mockllm.New(mockllm.TextTurn("SHARED-A"))
	reg := liveWindowReg(provider, providerOpenAI, model)
	// Seed the live store so contextWindowFor returns the catalogued window.
	reg.meta.Swap(map[string][]modelEntry{
		providerOpenAI: {{ID: model, ContextLimit: bakedWindow}},
	})

	svc, factoryCalls := defaultLiveWindowService(t, reg, provider, model)

	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRunContent(ctx, sess.ID, "turn", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	if got := drainRun(run); got != "SHARED-A" {
		t.Fatalf("reply = %q, want SHARED-A (catalogued default keeps the shared engine)", got)
	}
	if *factoryCalls != 0 {
		t.Fatalf("session-engine factory called %d times for a catalogued default session, want 0 (no rehydration)", *factoryCalls)
	}
	if got := svc.ResolvedModel(sess.ID).ContextWindow; got != bakedWindow {
		t.Fatalf("ResolvedModel.ContextWindow = %d, want %d (catalogued window, live-first)", got, bakedWindow)
	}
}

// TestContextWindowOverrideReachesEcho closes the latent-bug gap the architecture
// review flagged: the operator escape-hatch --context-window-override
// (cfg.ContextWindowOverride) now composes INSIDE the single windowResolver, and
// Build wires ResolveContextWindow over that SAME resolver — so the override must
// reach the resolved_model ECHO (Service.ResolvedModel), not just the engine's
// compaction trigger. Before the unification the echo ignored the override (it read
// a separate live-first path), so the footer could disagree with the window the
// engine actually compacted at. This guards both branches:
//
//   - a DEFAULT session (no per-session engine — the shared-engine echo path), and
//   - a SELECTOR session (a per-session engine — the selector echo path),
//
// proving the override WINS over the live store for either branch.
//
// MUTATION-VERIFY (non-vacuous): deleting the `if cfg.ContextWindowOverride > 0`
// clause in windowResolver (livemeta.go) makes BOTH assertions fail — the echo
// reverts to the live/catalog window instead of the override.
func TestContextWindowOverrideReachesEcho(t *testing.T) {
	ctx := context.Background()
	const (
		model     = "openai/gpt-5.4" // live-only; the live store reports a DIFFERENT window
		liveWin   = 1_050_000        // what the live store says — the override must beat this
		overrideW = 64_000           // the operator escape-hatch value
	)
	provider := mockllm.New(mockllm.TextTurn("X"))
	reg := liveWindowReg(provider, providerOpenAI, model)
	// Seed the live store with a window DISTINCT from the override, so a passing test
	// can only mean the override won (not a coincidental match).
	reg.meta.Swap(map[string][]modelEntry{
		providerOpenAI: {{ID: model, ContextLimit: liveWin}},
	})
	if got := reg.meta.contextWindowFor(providerOpenAI, model); got != liveWin {
		t.Fatalf("seed: contextWindowFor = %d, want the live %d", got, liveWin)
	}

	svc, _ := defaultLiveWindowServiceCfg(t, reg, provider, Config{Model: model, ContextWindowOverride: overrideW})

	// DEFAULT session (shared-engine echo path): the override beats the live window.
	def, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession(default): %v", err)
	}
	if got := svc.ResolvedModel(def.ID).ContextWindow; got != overrideW {
		t.Fatalf("default-session echo ContextWindow = %d, want the override %d (override must reach the echo, not just the engine; live store says %d)", got, overrideW, liveWin)
	}

	// SELECTOR session (per-session-engine echo path): same override, same single
	// windowResolver — it must win here too.
	sel, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{ProviderID: providerOpenAI, ModelID: model})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider(selector): %v", err)
	}
	if got := svc.ResolvedModel(sel.ID).ContextWindow; got != overrideW {
		t.Fatalf("selector-session echo ContextWindow = %d, want the override %d (override must reach the selector echo too)", got, overrideW)
	}
}

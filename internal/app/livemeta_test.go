package app

import (
	"testing"
)

// catalogued anthropic model with known catalog limits (models.dev.curated.json):
// claude-haiku-4-5 → ctx 200000, out 64000. Used as the catalog-floor anchor
// so the tests assert on stable embedded data, not a live endpoint.
const (
	catAnthropicModel  = "claude-haiku-4-5"
	catAnthropicCtx    = 200_000
	catAnthropicOutput = 64000
)

func TestResolveWindowCorePrecedenceAndExactProviderModel(t *testing.T) {
	reg := &providerRegistry{meta: newLiveMetaStore()}
	reg.meta.Swap(map[string][]modelEntry{
		providerAnthropic: {{ID: catAnthropicModel, ContextLimit: 555_000}},
		providerOpenAI:    {{ID: catAnthropicModel, ContextLimit: 444_000}},
	})
	cfg := Config{contextWindows: map[string]map[string]int{
		providerAnthropic: {catAnthropicModel: 333_000},
		providerToolhive:  {"deployment/opaque-routing-id": 222_000},
	}}
	if got, _ := reg.resolveWindowCore(cfg, providerAnthropic, catAnthropicModel); got != 333_000 {
		t.Fatalf("configured exact window = %d, want 333000", got)
	}
	if got, _ := reg.resolveWindowCore(cfg, providerOpenAI, catAnthropicModel); got != 444_000 {
		t.Fatalf("cross-provider configured lookup = %d, want live 444000", got)
	}
	if got, _ := reg.resolveWindowCore(cfg, providerToolhive, "deployment/opaque-routing-id"); got != 222_000 {
		t.Fatalf("opaque ToolHive configured ID = %d, want 222000", got)
	}
	if got := reg.windowResolver(cfg, providerAnthropic, "not-catalogued")(); got != defaultContextWindowTokens {
		t.Fatalf("unknown-model fallback = %d, want %d", got, defaultContextWindowTokens)
	}
	cfg.ContextWindowOverride = 111_000
	if got, _ := reg.resolveWindowCore(cfg, providerAnthropic, catAnthropicModel); got != 111_000 {
		t.Fatalf("CLI override = %d, want 111000", got)
	}
}

func TestConfiguredWindowUsesFinalAliasAndSlotModel(t *testing.T) {
	const finalModel = "vendor/final-routing-id"
	cfg := Config{
		ModelAliases:   map[string]string{"large": finalModel},
		ModelSlots:     map[string]string{slotCompaction: "large"},
		contextWindows: map[string]map[string]int{providerOpenAI: {finalModel: 345_000}},
	}
	aliasModel, known := lookupModelAlias(cfg, "large")
	if !known || aliasModel != finalModel {
		t.Fatalf("alias resolved to (%q, %v), want final id", aliasModel, known)
	}
	slotModel, configured := resolveSlotModel(cfg, slotCompaction, "parent/model")
	if !configured || slotModel != finalModel {
		t.Fatalf("slot resolved to (%q, %v), want final id", slotModel, configured)
	}
	reg := &providerRegistry{meta: newLiveMetaStore()}
	if got := reg.windowResolver(cfg, providerOpenAI, aliasModel)(); got != 345_000 {
		t.Fatalf("alias final-id window = %d, want 345000", got)
	}
	if got := reg.windowResolver(cfg, providerOpenAI, slotModel)(); got != 345_000 {
		t.Fatalf("slot final-id window = %d, want 345000", got)
	}
}

func TestConfiguredWindowEngineEchoAndModelListAgree(t *testing.T) {
	const (
		model  = "provider/model"
		window = 456_000
	)
	cfg := Config{contextWindows: map[string]map[string]int{providerOpenAI: {model: window}}}
	reg := &providerRegistry{
		meta:           newLiveMetaStore(),
		contextWindows: cfg.contextWindows,
	}
	reg.meta.Swap(map[string][]modelEntry{providerOpenAI: {{ID: model, ContextLimit: 123_000}}})
	engineWindow := reg.windowResolver(cfg, providerOpenAI, model)()
	echoWindow := reg.echoWindowResolver(cfg, providerOpenAI, model)()
	listedWindow := projectModelEntry(reg, providerOpenAI, modelEntry{ID: model, ContextLimit: 123_000}).GetContextLimit()
	if engineWindow != window || echoWindow != window || listedWindow != window {
		t.Fatalf("window disagreement: engine=%d echo=%d list=%d, want %d", engineWindow, echoWindow, listedWindow, window)
	}

	const globalOverride = 111_000
	cfg.ContextWindowOverride = globalOverride
	reg.contextWindowOverride = globalOverride
	engineWindow = reg.windowResolver(cfg, providerOpenAI, model)()
	echoWindow = reg.echoWindowResolver(cfg, providerOpenAI, model)()
	listedWindow = projectModelEntry(reg, providerOpenAI, modelEntry{ID: model, ContextLimit: 123_000}).GetContextLimit()
	if engineWindow != globalOverride || echoWindow != globalOverride || listedWindow != globalOverride {
		t.Fatalf("global override disagreement: engine=%d echo=%d list=%d, want %d", engineWindow, echoWindow, listedWindow, globalOverride)
	}
}

// TestLiveMetaStoreSeedFromCatalog: a freshly seeded store carries the catalog rows
// for every available provider, so a resolver read at t=0 (before any live swap)
// returns the catalog value — never an empty miss.
func TestLiveMetaStoreSeedFromCatalog(t *testing.T) {
	s := newLiveMetaStore()
	s.seedFromCatalog([]string{providerAnthropic})

	m, ok := s.lookup(providerAnthropic, catAnthropicModel)
	if !ok {
		t.Fatalf("seeded store missing catalogued model %q", catAnthropicModel)
	}
	if m.ContextLimit != catAnthropicCtx || m.OutputLimit != catAnthropicOutput {
		t.Fatalf("seed mismatch: ctx=%d out=%d, want %d/%d", m.ContextLimit, m.OutputLimit, catAnthropicCtx, catAnthropicOutput)
	}
	// The seed carries no live thinking descriptor (the catalog has no thinking bit).
	if m.Thinking.Known {
		t.Errorf("seeded entry must not claim a live thinking descriptor")
	}
}

// TestResolversByteIdenticalWithCatalogOnlyStore: with NO lister wired (the store is
// catalog-seeded only), the live-first helpers return EXACTLY the catalog floor — so
// the broadening is behaviour-preserving until a live source populates the store.
func TestResolversByteIdenticalWithCatalogOnlyStore(t *testing.T) {
	s := newLiveMetaStore()
	s.seedFromCatalog([]string{providerAnthropic})

	if got := s.outputLimitFor(providerAnthropic, catAnthropicModel); got != catAnthropicOutput {
		t.Errorf("outputLimitFor = %d, want catalog floor %d", got, catAnthropicOutput)
	}
	if got := s.contextWindowFor(providerAnthropic, catAnthropicModel); got != catAnthropicCtx {
		t.Errorf("contextWindowFor = %d, want catalog floor %d", got, catAnthropicCtx)
	}
	// The bare catalog functions must agree with the helper (the byte-identical claim).
	if anthropicOutputLimit(catAnthropicModel) != s.outputLimitFor(providerAnthropic, catAnthropicModel) {
		t.Error("outputLimitFor diverged from anthropicOutputLimit on a catalog-only store")
	}
	if catalogContextWindow(providerAnthropic, catAnthropicModel) != s.contextWindowFor(providerAnthropic, catAnthropicModel) {
		t.Error("contextWindowFor diverged from catalogContextWindow on a catalog-only store")
	}
}

// TestLiveFirstHelperPrefersLiveWhenPresent: a live entry with a non-zero field
// beats the catalog; a live entry whose field is ZERO falls back to the catalog
// floor (per-FIELD precedence, never a fabricated zero).
func TestLiveFirstHelperPrefersLiveWhenPresent(t *testing.T) {
	s := newLiveMetaStore()
	s.seedFromCatalog([]string{providerAnthropic})

	// A live swap: same model, a DIFFERENT (live) output ceiling and context window.
	const liveOut, liveCtx = 9999, 555_000
	s.Swap(map[string][]modelEntry{
		providerAnthropic: {{ID: catAnthropicModel, OutputLimit: liveOut, ContextLimit: liveCtx}},
	})
	if got := s.outputLimitFor(providerAnthropic, catAnthropicModel); got != liveOut {
		t.Errorf("outputLimitFor = %d, want live %d", got, liveOut)
	}
	if got := s.contextWindowFor(providerAnthropic, catAnthropicModel); got != liveCtx {
		t.Errorf("contextWindowFor = %d, want live %d", got, liveCtx)
	}

	// A live entry with ZERO fields ⇒ per-field fallback to the catalog floor.
	s.Swap(map[string][]modelEntry{
		providerAnthropic: {{ID: catAnthropicModel, OutputLimit: 0, ContextLimit: 0}},
	})
	if got := s.outputLimitFor(providerAnthropic, catAnthropicModel); got != catAnthropicOutput {
		t.Errorf("zero live output: outputLimitFor = %d, want catalog floor %d", got, catAnthropicOutput)
	}
	if got := s.contextWindowFor(providerAnthropic, catAnthropicModel); got != catAnthropicCtx {
		t.Errorf("zero live ctx: contextWindowFor = %d, want catalog floor %d", got, catAnthropicCtx)
	}
}

// TestLiveValueUpperClamped (Security #2): an ABSURD live value (hostile / MITM'd /
// buggy upstream) is UPPER-clamped before it leaves the helper, so it cannot flow
// verbatim into max_tokens (cost/400) or disable compaction (a gigantic window). A
// legitimate in-range live value is returned unchanged.
func TestLiveValueUpperClamped(t *testing.T) {
	s := newLiveMetaStore()
	s.seedFromCatalog([]string{providerAnthropic})

	// Way above any real Claude limit — must clamp to the caps.
	s.Swap(map[string][]modelEntry{
		providerAnthropic: {{ID: catAnthropicModel, OutputLimit: 999_999_999, ContextLimit: 999_999_999}},
	})
	if got := s.outputLimitFor(providerAnthropic, catAnthropicModel); got != maxLiveOutputLimit {
		t.Errorf("absurd live output: outputLimitFor = %d, want clamp %d", got, maxLiveOutputLimit)
	}
	if got := s.contextWindowFor(providerAnthropic, catAnthropicModel); got != maxLiveContextLimit {
		t.Errorf("absurd live ctx: contextWindowFor = %d, want clamp %d", got, maxLiveContextLimit)
	}

	// A legitimate in-range live value (above catalog, below the cap) is unchanged.
	const okOut, okCtx = 200_000, 1_500_000
	s.Swap(map[string][]modelEntry{
		providerAnthropic: {{ID: catAnthropicModel, OutputLimit: okOut, ContextLimit: okCtx}},
	})
	if got := s.outputLimitFor(providerAnthropic, catAnthropicModel); got != okOut {
		t.Errorf("in-range live output clamped unexpectedly: %d, want %d", got, okOut)
	}
	if got := s.contextWindowFor(providerAnthropic, catAnthropicModel); got != okCtx {
		t.Errorf("in-range live ctx clamped unexpectedly: %d, want %d", got, okCtx)
	}
}

// TestLiveMissFallsBackToCatalogRow: a model the LIVE swap omits but the catalog
// knows must still resolve via the catalog (live absence never erases the catalog).
func TestLiveMissFallsBackToCatalogRow(t *testing.T) {
	s := newLiveMetaStore()
	s.seedFromCatalog([]string{providerAnthropic})
	// A live swap that DROPS catAnthropicModel entirely (only an unrelated id present).
	s.Swap(map[string][]modelEntry{
		providerAnthropic: {{ID: "some-live-only-model", OutputLimit: 12345, ContextLimit: 99}},
	})
	// catAnthropicModel is absent from the live swap → catalog floor via the helper.
	if got := s.outputLimitFor(providerAnthropic, catAnthropicModel); got != catAnthropicOutput {
		t.Errorf("live miss: outputLimitFor = %d, want catalog floor %d", got, catAnthropicOutput)
	}
	if got := s.contextWindowFor(providerAnthropic, catAnthropicModel); got != catAnthropicCtx {
		t.Errorf("live miss: contextWindowFor = %d, want catalog floor %d", got, catAnthropicCtx)
	}
}

// TestModalitiesForFromLiveStore: modalitiesFor returns an explicitly declared
// live input-modality list. A non-nil empty list or ["text"] is authoritative
// text-only; omitted metadata (nil) is unknown, as is an absent model, so callers
// fall through to the exact catalog row and then adapter caps.
func TestModalitiesForFromLiveStore(t *testing.T) {
	s := newLiveMetaStore()
	s.seedFromCatalog([]string{providerAnthropic})

	// A live swap carrying modalities for an openrouter-style passthrough id.
	s.Swap(map[string][]modelEntry{
		providerOpenRouter: {{ID: "openai/gpt-4", InputModalities: []string{"text", "image"}}},
	})
	mods, found := s.modalitiesFor(providerOpenRouter, "openai/gpt-4")
	if !found {
		t.Fatal("modalitiesFor: found=false for a live entry with modalities, want true")
	}
	if len(mods) != 2 || mods[0] != "text" || mods[1] != "image" {
		t.Fatalf("modalitiesFor = %v, want [text image]", mods)
	}

	// An explicitly empty declaration is authoritative text-only.
	s.Swap(map[string][]modelEntry{
		providerOpenRouter: {{ID: "openai/gpt-4", InputModalities: []string{}}},
	})
	mods, found = s.modalitiesFor(providerOpenRouter, "openai/gpt-4")
	if !found {
		t.Error("modalitiesFor: found=false for an explicit empty declaration")
	}
	if len(mods) != 0 {
		t.Errorf("modalitiesFor = %v for an explicit empty declaration, want empty", mods)
	}

	// Omitted metadata is unknown and falls through to catalog/adapter resolution.
	s.Swap(map[string][]modelEntry{
		providerOpenRouter: {{ID: "openai/gpt-4"}},
	})
	if _, found := s.modalitiesFor(providerOpenRouter, "openai/gpt-4"); found {
		t.Error("modalitiesFor: found=true for omitted metadata, want false")
	}

	// An absent model is a clean miss.
	if _, found := s.modalitiesFor(providerOpenRouter, "no-such-model"); found {
		t.Error("modalitiesFor: found=true for an absent model, want false")
	}
}

// TestThinkingForFromLiveStore: thinkingFor returns the live descriptor only when a
// live source populated it (Known=true); a seed-only or absent entry yields
// known=false (the adapter then falls back to its prefix matrix).
func TestThinkingForFromLiveStore(t *testing.T) {
	s := newLiveMetaStore()
	s.seedFromCatalog([]string{providerAnthropic})

	// Seed-only entry: not live-known.
	if _, _, known := s.thinkingFor(providerAnthropic, catAnthropicModel); known {
		t.Error("seed-only entry must report known=false (defer to prefix matrix)")
	}

	// A live swap with a populated thinking descriptor (adaptive).
	s.Swap(map[string][]modelEntry{
		providerAnthropic: {{ID: catAnthropicModel, Thinking: thinkingDescriptor{Known: true, Adaptive: true}}},
	})
	a, e, known := s.thinkingFor(providerAnthropic, catAnthropicModel)
	if !known || !a || e {
		t.Errorf("live thinking: got adaptive=%v enabled=%v known=%v, want adaptive=true enabled=false known=true", a, e, known)
	}

	// An absent model is never known.
	if _, _, known := s.thinkingFor(providerAnthropic, "no-such-model"); known {
		t.Error("absent model must report known=false")
	}
}

// TestLiveMetaStoreNilSafe: every read on a nil store is a safe miss, and Swap/seed
// on a nil receiver are no-ops — so a hand-built test registry with no meta store
// cannot panic.
func TestLiveMetaStoreNilSafe(t *testing.T) {
	var s *liveMetaStore
	if _, ok := s.lookup(providerAnthropic, catAnthropicModel); ok {
		t.Error("nil store lookup should miss")
	}
	if got := s.outputLimitFor(providerAnthropic, catAnthropicModel); got != catAnthropicOutput {
		// nil store falls through to the catalog floor for anthropic.
		t.Errorf("nil store outputLimitFor = %d, want catalog floor %d", got, catAnthropicOutput)
	}
	if got := s.contextWindowFor(providerAnthropic, catAnthropicModel); got != catAnthropicCtx {
		t.Errorf("nil store contextWindowFor = %d, want catalog floor %d", got, catAnthropicCtx)
	}
	if _, _, known := s.thinkingFor(providerAnthropic, catAnthropicModel); known {
		t.Error("nil store thinkingFor should report known=false")
	}
	if _, found := s.modalitiesFor(providerAnthropic, catAnthropicModel); found {
		t.Error("nil store modalitiesFor should report found=false")
	}
	s.Swap(map[string][]modelEntry{providerAnthropic: {{ID: "x"}}}) // must not panic
	s.seedFromCatalog([]string{providerAnthropic})                  // must not panic
}

// --- echo resolver provisional-0 vs engine resolver never-0 (issue #66) ---

// regForResolver builds a minimal registry carrying ONLY the meta store the two
// context-window resolvers read — no providers/listers needed (the resolvers consult
// reg.meta + the catalog, not the entries).
func regForResolver(s *liveMetaStore) *providerRegistry {
	return &providerRegistry{meta: s}
}

const (
	// A model id that is NOT in the curated catalog (simulates a brand-new model
	// that appeared in the live listing but hasn't been re-pinned into the embed
	// yet). The live refresh is the ONLY source for its context window.
	liveOnlyModel = "openai/gpt-99-future"
	liveOnlyCtx   = 1_050_000 // below maxLiveContextLimit, so unclamped
)

// TestEchoResolverProvisionalThenSettled: for an uncatalogued live-only model the ECHO
// resolver returns a deliberate PROVISIONAL 0 pre-completion (the client's footer-heal
// gate keys off ==0), then the real live window once the refresh has marked completed
// AND the live entry has been swapped in.
func TestEchoResolverProvisionalThenSettled(t *testing.T) {
	s := newLiveMetaStore()
	s.seedFromCatalog([]string{providerOpenRouter}) // live-only model not in the seed
	reg := regForResolver(s)
	echo := reg.echoWindowResolver(Config{}, providerOpenRouter, liveOnlyModel)

	if got := echo(); got != 0 {
		t.Fatalf("pre-completion echo = %d, want 0 (provisional, uncatalogued live-only)", got)
	}
	// The live refresh lands: the live listing carries the model + window, and the flag
	// settles.
	s.Swap(map[string][]modelEntry{providerOpenRouter: {{ID: liveOnlyModel, ContextLimit: liveOnlyCtx}}})
	s.markRefreshCompleted()
	if got := echo(); got != liveOnlyCtx {
		t.Fatalf("post-swap echo = %d, want live %d", got, liveOnlyCtx)
	}
}

// TestEchoResolverSettledFloorStops: an uncatalogued live-only model that is STILL
// unknown after the refresh settles (no live entry ever arrived — e.g. a no-network
// fetch-fail) floors to 128k, NOT a perpetual provisional 0. This closes the footer-
// heal gate (the client stops refetching) — no-network boundedness.
func TestEchoResolverSettledFloorStops(t *testing.T) {
	s := newLiveMetaStore()
	s.seedFromCatalog([]string{providerOpenRouter})
	reg := regForResolver(s)
	echo := reg.echoWindowResolver(Config{}, providerOpenRouter, liveOnlyModel)

	if got := echo(); got != 0 {
		t.Fatalf("pre-completion echo = %d, want 0 (provisional)", got)
	}
	s.markRefreshCompleted() // settled, but the model never got a live entry
	if got := echo(); got != defaultContextWindowTokens {
		t.Fatalf("settled-but-unknown echo = %d, want the 128k floor %d (must not stick at 0)", got, defaultContextWindowTokens)
	}
}

// TestEchoResolverCataloguedModelNeverProvisional: a GENUINELY catalogued model is
// known-at-real-value, so the echo returns its catalog window EVEN pre-completion — it
// is never provisional. (catAnthropicModel's catalog window is 200k, a non-zero
// known value; the point is the echo never returns 0 for a catalogued model.)
func TestEchoResolverCataloguedModelNeverProvisional(t *testing.T) {
	s := newLiveMetaStore()
	s.seedFromCatalog([]string{providerAnthropic})
	reg := regForResolver(s)
	echo := reg.echoWindowResolver(Config{}, providerAnthropic, catAnthropicModel)

	// Pre-completion (refresh NOT settled) the catalogued window is still returned.
	if s.refreshCompleted() {
		t.Fatal("precondition: refresh must be uncompleted for this test")
	}
	if got := echo(); got != catAnthropicCtx {
		t.Fatalf("pre-completion echo for a catalogued model = %d, want catalog window %d (never provisional)", got, catAnthropicCtx)
	}
}

// TestEngineResolverNeverZero: for the SAME uncatalogued-pre-completion inputs the echo
// returns 0 for, the ENGINE resolver returns the 128k floor — the engine must never see
// 0 (it cannot run on a 0 window). This is the load-bearing engine-never-0 vs echo-0
// guard.
func TestEngineResolverNeverZero(t *testing.T) {
	s := newLiveMetaStore()
	s.seedFromCatalog([]string{providerOpenRouter})
	reg := regForResolver(s)

	echo := reg.echoWindowResolver(Config{}, providerOpenRouter, liveOnlyModel)
	engine := reg.windowResolver(Config{}, providerOpenRouter, liveOnlyModel)

	if got := echo(); got != 0 {
		t.Fatalf("echo = %d for an uncatalogued live-only model pre-completion, want 0", got)
	}
	if got := engine(); got != defaultContextWindowTokens {
		t.Fatalf("engine resolver = %d for the SAME inputs, want the 128k floor %d (engine must NEVER see 0)", got, defaultContextWindowTokens)
	}
	// And post-settle-but-still-unknown both agree on 128k (the only place they converge
	// in the unknown case).
	s.markRefreshCompleted()
	if got := echo(); got != defaultContextWindowTokens {
		t.Fatalf("settled echo = %d, want 128k floor", got)
	}
	if got := engine(); got != defaultContextWindowTokens {
		t.Fatalf("engine = %d, want 128k floor", got)
	}
}

// TestResolversOverrideWinsBothPrePost: the --context-window-override wins for BOTH
// resolvers, pre- and post-completion — never a provisional 0, never the floor.
func TestResolversOverrideWinsBothPrePost(t *testing.T) {
	const override = 777_000
	s := newLiveMetaStore()
	s.seedFromCatalog([]string{providerOpenRouter})
	reg := regForResolver(s)
	cfg := Config{ContextWindowOverride: override}

	echo := reg.echoWindowResolver(cfg, providerOpenRouter, liveOnlyModel)
	engine := reg.windowResolver(cfg, providerOpenRouter, liveOnlyModel)

	// Pre-completion: override wins (not provisional 0, not floor).
	if got := echo(); got != override {
		t.Fatalf("pre-completion echo with override = %d, want %d", got, override)
	}
	if got := engine(); got != override {
		t.Fatalf("pre-completion engine with override = %d, want %d", got, override)
	}
	// Post-completion: still the override.
	s.markRefreshCompleted()
	if got := echo(); got != override {
		t.Fatalf("post-completion echo with override = %d, want %d", got, override)
	}
	if got := engine(); got != override {
		t.Fatalf("post-completion engine with override = %d, want %d", got, override)
	}
}

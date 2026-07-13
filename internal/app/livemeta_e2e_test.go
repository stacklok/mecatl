package app

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/anthropic"
	"github.com/stacklok/mecatl/internal/adapter/openrouter"
)

// anthropicFixtureClient serves the trimmed anthropic /v1/models fixture (NO
// network) so the live anthropic lister path is exercised fully offline.
func anthropicFixtureClient(t *testing.T) *http.Client {
	t.Helper()
	fixture, err := os.ReadFile("../adapter/anthropic/testdata/models.json")
	if err != nil {
		t.Fatalf("read anthropic fixture: %v", err)
	}
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(string(fixture))),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})}
}

// regWithAnthropicLister builds a registry with a single available anthropic
// provider whose live lister is the REAL anthropic.Lister over a mock transport, and
// a catalog-seeded live-metadata store (mirroring buildProviderRegistry's seed).
func regWithAnthropicLister(t *testing.T, client *http.Client) *providerRegistry {
	t.Helper()
	meta := newLiveMetaStore()
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerAnthropic: {
				id:        providerAnthropic,
				provider:  mockllm.NewWith([]mockllm.Option{mockllm.WithCapabilities(port.ProviderCapabilities{Image: true})}, mockllm.TextTurn("x")),
				available: true,
				lister:    anthropicLister{inner: anthropic.NewLister("sk-test", "", client)},
			},
		},
		defaultID: providerAnthropic,
		meta:      meta,
	}
	meta.seedFromCatalog(reg.Available())
	return reg
}

// TestAnthropicLiveResolverPicksUpCeiling is the Slice B e2e: a keyed anthropic
// provider's live lister (mock transport) replaces the catalog, and the request-path
// resolvers read the LIVE output ceiling / context window / thinking descriptor over
// the catalog floor — proving the broadening reaches the resolvers, not just the
// picker.
func TestAnthropicLiveResolverPicksUpCeiling(t *testing.T) {
	reg := regWithAnthropicLister(t, anthropicFixtureClient(t))

	// Before the swap: the seed (catalog) drives the resolvers. claude-opus-4-8's
	// catalog output ceiling is 128000 (also the live value) — pick a model whose
	// LIVE value differs from the catalog to prove the swap. The fixture sets
	// claude-haiku-4-5 to out=64000/ctx=200000 matching catalog, so instead
	// assert the LIVE thinking descriptor (which the catalog cannot supply at all).
	if _, _, known := reg.meta.thinkingFor(providerAnthropic, "claude-opus-4-8"); known {
		t.Fatal("pre-swap: thinking must be unknown (catalog has no thinking bit)")
	}

	// Run the live snapshot + swap (the same two-sink path the background refresh uses).
	models, byProvider := liveModelSnapshot(context.Background(), port.NopDiagnostics{}, reg)
	if len(models) == 0 {
		t.Fatal("live snapshot empty")
	}
	reg.meta.Swap(byProvider)

	// Post-swap: the LIVE thinking descriptor is now known and adaptive for opus-4-8.
	a, e, known := reg.meta.thinkingFor(providerAnthropic, "claude-opus-4-8")
	if !known || !a || e {
		t.Errorf("post-swap opus thinking: adaptive=%v enabled=%v known=%v, want adaptive=true enabled=false known=true", a, e, known)
	}
	// And a manual (enabled) model is enabled-not-adaptive.
	a, e, known = reg.meta.thinkingFor(providerAnthropic, "claude-sonnet-4-5")
	if !known || a || !e {
		t.Errorf("post-swap sonnet thinking: adaptive=%v enabled=%v known=%v, want adaptive=false enabled=true known=true", a, e, known)
	}

	// The LIVE output ceiling + context window reach outputLimitFor / contextWindowFor.
	if got := reg.meta.outputLimitFor(providerAnthropic, "claude-opus-4-8"); got != 128000 {
		t.Errorf("live outputLimitFor(opus) = %d, want 128000", got)
	}
	if got := reg.meta.contextWindowFor(providerAnthropic, "claude-opus-4-8"); got != 1_000_000 {
		t.Errorf("live contextWindowFor(opus) = %d, want 1000000", got)
	}
}

// TestBuildProviderRegistrySeedsMetaStore proves the real construction path
// (buildProviderRegistry) attaches a non-nil meta store SEEDED from the catalog at
// t=0 (before any network), and wires a lister onto the anthropic entry — so every
// resolver has a catalog value before the live swap and the anthropic live path is
// armed.
func TestBuildProviderRegistrySeedsMetaStore(t *testing.T) {
	reg, err := buildProviderRegistry(Config{
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("x"))
		},
	}, fakeEnv(map[string]string{"ANTHROPIC_API_KEY": "sk-test"}))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	if reg.meta == nil {
		t.Fatal("registry meta store is nil (resolvers would have no t=0 seed)")
	}
	// The seed carries the catalog ceiling at t=0, before any live fetch.
	if got := reg.meta.outputLimitFor(providerAnthropic, catAnthropicModel); got != catAnthropicOutput {
		t.Errorf("t=0 seed outputLimitFor = %d, want catalog %d", got, catAnthropicOutput)
	}
	// The anthropic entry is armed with a live lister (even via the providerConstructor
	// test seam) so the live path runs for a keyed anthropic provider.
	entry, ok := reg.Lookup(providerAnthropic)
	if !ok || entry.lister == nil {
		t.Errorf("anthropic entry missing a live lister (live path not armed): ok=%v", ok)
	}
}

// TestAnthropicLiveCeilingOverridesCatalog proves a LIVE ceiling that DIFFERS from
// the catalog wins for the max_tokens resolver — the headline reliability win (a
// newly-described ceiling beats the embedded snapshot).
func TestAnthropicLiveCeilingOverridesCatalog(t *testing.T) {
	reg := regWithAnthropicLister(t, anthropicFixtureClient(t))

	// claude-haiku-4-5: catalog out=64000. Inject a DIFFERENT live ceiling via
	// a direct swap (the fixture matches catalog for this id, so we force a divergence
	// to assert live-wins unambiguously).
	const liveCeiling = 32768
	reg.meta.Swap(map[string][]modelEntry{
		providerAnthropic: {{ID: "claude-haiku-4-5", OutputLimit: liveCeiling, ContextLimit: 200_000}},
	})
	if got := reg.meta.outputLimitFor(providerAnthropic, "claude-haiku-4-5"); got != liveCeiling {
		t.Errorf("outputLimitFor = %d, want live %d (live must beat catalog 64000)", got, liveCeiling)
	}
	// A model the live swap dropped still resolves via the catalog floor.
	if got := reg.meta.outputLimitFor(providerAnthropic, "claude-opus-4-8"); got != 128000 {
		t.Errorf("dropped-from-live model: outputLimitFor = %d, want catalog floor 128000", got)
	}
}

// TestOpenRouterOutputLimitSurvivesSwapIntoStore (QA #6): a non-zero OpenRouter
// OutputLimit (top_provider.max_completion_tokens) survives the REAL openrouter
// lister mapping + the swap into the meta store and is returned by
// outputLimitFor(providerOpenRouter, …); a null/zero ceiling (fusion) ⇒ 0 on the
// resolver side (OpenRouter's output ceiling has no catalog floor in outputLimitFor —
// it is off the OpenRouter request path today, but captured for the store).
func TestOpenRouterOutputLimitSurvivesSwapIntoStore(t *testing.T) {
	fixture, err := os.ReadFile("../adapter/openrouter/testdata/models.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(string(fixture))),
			Header:     make(http.Header),
		}, nil
	})}
	meta := newLiveMetaStore()
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerOpenRouter: {
				id:        providerOpenRouter,
				provider:  mockllm.NewWith([]mockllm.Option{mockllm.WithCapabilities(port.ProviderCapabilities{Image: true})}, mockllm.TextTurn("x")),
				available: true,
				lister:    openRouterLister{inner: openrouter.NewLister(client)},
			},
		},
		defaultID: providerOpenRouter,
		meta:      meta,
	}
	meta.seedFromCatalog(reg.Available())

	// Run the real snapshot + swap (the two-sink path the background refresh uses).
	_, byProvider := liveModelSnapshot(context.Background(), port.NopDiagnostics{}, reg)
	meta.Swap(byProvider)

	// qwen/qwen3.7-plus has top_provider.max_completion_tokens = 65536 in the fixture.
	if got := meta.outputLimitFor(providerOpenRouter, "qwen/qwen3.7-plus"); got != 65536 {
		t.Errorf("openrouter outputLimitFor(qwen) = %d, want live 65536 (survived the swap)", got)
	}
	// The live context window also survives the swap into the store.
	if got := meta.contextWindowFor(providerOpenRouter, "qwen/qwen3.7-plus"); got != 1_000_000 {
		t.Errorf("openrouter contextWindowFor(qwen) = %d, want live 1000000", got)
	}
	// openrouter/fusion has null max_completion_tokens ⇒ OutputLimit 0 ⇒ resolver 0
	// (no openrouter output-ceiling catalog floor; off the request path today).
	if got := meta.outputLimitFor(providerOpenRouter, "openrouter/fusion"); got != 0 {
		t.Errorf("openrouter outputLimitFor(fusion) = %d, want 0 (null max_completion_tokens)", got)
	}
}

// TestOpenRouterLiveModalitiesGateSessionEcho is the headline bug e2e: the OpenRouter
// adapter is shared with openai (Capabilities() Image:true), but a TEXT-ONLY live
// model must report Image:false in the per-session capability echo (modelCapability),
// while a vision live model reports Image:true. It also asserts the picker
// (projectModelEntry) and the session echo derive image from the SAME live source, so
// they cannot disagree. Runs the REAL openrouter lister over the fixture, fully offline.
func TestOpenRouterLiveModalitiesGateSessionEcho(t *testing.T) {
	fixture, err := os.ReadFile("../adapter/openrouter/testdata/models.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(string(fixture))),
			Header:     make(http.Header),
		}, nil
	})}
	meta := newLiveMetaStore()
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerOpenRouter: {
				id:        providerOpenRouter,
				provider:  mockllm.NewWith([]mockllm.Option{mockllm.WithCapabilities(port.ProviderCapabilities{Image: true})}, mockllm.TextTurn("x")),
				available: true,
				lister:    openRouterLister{inner: openrouter.NewLister(client)},
			},
		},
		defaultID: providerOpenRouter,
		meta:      meta,
	}
	meta.seedFromCatalog(reg.Available())

	// Run the real snapshot + swap (the two-sink path the background refresh uses).
	picker, byProvider := liveModelSnapshot(context.Background(), port.NopDiagnostics{}, reg)
	meta.Swap(byProvider)

	const (
		textOnly = "nvidia/nemotron-3-ultra-550b-a55b:free" // input_modalities ["text"]
		vision   = "qwen/qwen3.7-plus"                      // input_modalities ["text","image"]
	)

	// The session echo path (modelCapability) gates image on the LIVE modalities.
	if got := modelCapability(reg, providerOpenRouter, textOnly); got.Image {
		t.Errorf("session echo Image = true for text-only %q, want false (the reported bug)", textOnly)
	}
	if got := modelCapability(reg, providerOpenRouter, vision); !got.Image {
		t.Errorf("session echo Image = false for vision %q, want true", vision)
	}

	// The picker path (projectModelEntry, surfaced in liveModelSnapshot) must AGREE.
	pickerImage := map[string]bool{}
	for _, m := range picker {
		pickerImage[m.Id] = m.Image
	}
	if pickerImage[textOnly] {
		t.Errorf("picker Image = true for text-only %q, want false (picker/echo must agree)", textOnly)
	}
	if !pickerImage[vision] {
		t.Errorf("picker Image = false for vision %q, want true (picker/echo must agree)", vision)
	}
	if got := modelCapability(reg, providerOpenRouter, textOnly).Image; got != pickerImage[textOnly] {
		t.Errorf("picker/echo DISAGREE for %q: echo=%v picker=%v", textOnly, got, pickerImage[textOnly])
	}
	if got := modelCapability(reg, providerOpenRouter, vision).Image; got != pickerImage[vision] {
		t.Errorf("picker/echo DISAGREE for %q: echo=%v picker=%v", vision, got, pickerImage[vision])
	}
}

package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/openaicompat"
	"github.com/stacklok/mecatl/internal/adapter/openrouter"
	"github.com/stacklok/mecatl/internal/syscaller"
)

// TestOpenCodeListerStampsImageModality is the F1 regression: OpenCode Go's
// /models envelope carries no modality metadata, so openCodeLister must stamp the
// adapter-static modalities (text+image). Otherwise a successful live refresh
// would flip an uncatalogued model's Image capability from the adapter-static
// default (true) to false via modelCapability's authoritative live-first rule.
func TestOpenCodeListerStampsImageModality(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"object":"list","data":[{"id":"glm-5.2","object":"model"}]}`)),
			Header:     make(http.Header),
		}, nil
	})}
	rows, err := openCodeLister{inner: openaicompat.NewLister("https://opencode.example/v1", "k", client)}.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if !hasImageModality(rows[0].InputModalities) {
		t.Errorf("InputModalities = %v, want to include image (a live refresh must not flip Image to false)", rows[0].InputModalities)
	}
	if !rows[0].ToolCall {
		t.Error("ToolCall = false, want true")
	}
}

func TestGatewayListerPropagatesContextWindow(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"object":"list","data":[{"id":"gpt-5.6","context_window":1050000}]}`)),
			Header:     make(http.Header),
		}, nil
	})}

	rows, err := gatewayLister{inner: openaicompat.NewLister("https://gateway.example/v1", "k", client)}.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if got := rows[0].ContextLimit; got != 1_050_000 {
		t.Errorf("ContextLimit = %d, want 1050000", got)
	}
}

// fixtureClient serves the trimmed openrouter fixture for the whole-Build e2e.
func fixtureClient(t *testing.T) *http.Client {
	t.Helper()
	fixture, err := os.ReadFile("../adapter/openrouter/testdata/models.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(string(fixture))),
			Header:     make(http.Header),
		}, nil
	})}
}

// offlineHTTPClient is a transport that refuses EVERY request, so a live-model
// lister wired into a full Build (anthropic and/or openrouter) fails fast and
// fail-safes to the embedded catalog WITHOUT ever touching the network. It is the
// strictly-offline guard for the multi-provider Build tests that arm a lister but
// don't care about the live result (CWE: never contact api.anthropic.com /
// openrouter.ai in a test).
func offlineHTTPClient() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("offline: live model fetch refused in test")
	})}
}

// fakeLister is a controllable modelLister: it returns canned models or an error,
// for the merge + fail-safe unit tests (no HTTP).
type fakeLister struct {
	models []modelEntry
	err    error
	calls  atomic.Int32
}

func (f *fakeLister) ListModels(context.Context) ([]modelEntry, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return f.models, nil
}

type principalLister struct {
	seen chan *session.Principal
}

func (l *principalLister) ListModels(ctx context.Context) ([]modelEntry, error) {
	l.seen <- session.PrincipalFromContext(ctx)
	return []modelEntry{{ID: "live/model"}}, nil
}

func TestStartupModelRefreshListerSeesSystemPrincipal(t *testing.T) {
	lister := &principalLister{seen: make(chan *session.Principal, 1)}
	reg := regWithLister(lister)
	closer := startLiveModelRefresh(port.NopDiagnostics{}, reg, newFakeSwapper(), false, 0)
	defer closer()

	select {
	case got := <-lister.seen:
		if got == nil {
			t.Fatal("lister principal is nil, want explicit system principal")
		}
		if got.Issuer != syscaller.Issuer || got.Subject != string(syscaller.RootModelCatalogRefresh) || got.GrantType != session.GrantTypeSystem {
			t.Fatalf("lister principal = %+v, want issuer=%q subject=%q grant=%q", got,
				syscaller.Issuer, syscaller.RootModelCatalogRefresh, session.GrantTypeSystem)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("startup model refresh did not call lister")
	}
}

// regWithLister builds a registry with openai (no lister) + openrouter (the given
// lister), both backed by canned mocks, for the composition merge tests.
func regWithLister(lister modelLister) *providerRegistry {
	return &providerRegistry{
		entries: map[string]providerEntry{
			providerOpenAI: {
				id:        providerOpenAI,
				provider:  mockllm.NewWith([]mockllm.Option{mockllm.WithCapabilities(port.ProviderCapabilities{Image: true})}, mockllm.TextTurn("x")),
				available: true,
			},
			providerOpenRouter: {
				id:        providerOpenRouter,
				provider:  mockllm.NewWith([]mockllm.Option{mockllm.WithCapabilities(port.ProviderCapabilities{Image: true})}, mockllm.TextTurn("x")),
				available: true,
				lister:    lister,
			},
		},
		defaultID: providerOpenAI,
	}
}

// TestLiveSnapshotReplacesEmbedded: a successful live result REPLACES the embedded
// openrouter subset (a live-only id appears; the live set drives the openrouter
// models). OpenAI (no lister) keeps its embedded subset.
func TestLiveSnapshotReplacesEmbedded(t *testing.T) {
	lister := &fakeLister{models: []modelEntry{
		{ID: "live-only/model", DisplayName: "Live Only", ContextLimit: 1_000_000, InputModalities: []string{"text"}},
		{ID: "live-only/vision", DisplayName: "Live Vision", ContextLimit: 200_000, InputModalities: []string{"text", "image"}, Reasoning: true},
	}}
	reg := regWithLister(lister)

	models := projectAll(reg, liveModelSnapshot(context.Background(), port.NopDiagnostics{}, reg))

	var orIDs, openaiCount int
	sawLiveOnly := false
	curated := orEmbeddedIDs(t)
	curatedSeen := false
	for _, m := range models {
		switch m.GetProviderId() {
		case providerOpenRouter:
			orIDs++
			if m.GetId() == "live-only/model" {
				sawLiveOnly = true
			}
			if curated[m.GetId()] {
				curatedSeen = true
			}
		case providerOpenAI:
			openaiCount++
		}
	}
	if !sawLiveOnly {
		t.Fatal("live-only model absent: live result did not REPLACE the embedded subset")
	}
	if orIDs != 2 {
		t.Fatalf("openrouter contributed %d models, want exactly the 2 live ones", orIDs)
	}
	if curatedSeen {
		t.Fatal("a curated embedded openrouter id survived a successful live fetch (replace, not union)")
	}
	if openaiCount == 0 {
		t.Fatal("openai (no lister) lost its embedded subset")
	}
}

// TestLiveSnapshotFallsBackOnError: a lister error ⇒ openrouter falls back to the
// embedded subset (never empty, never a crash). Same for an empty live list.
func TestLiveSnapshotFallsBackOnError(t *testing.T) {
	curated := orEmbeddedIDs(t)
	for _, tc := range []struct {
		name   string
		lister *fakeLister
	}{
		{"error", &fakeLister{err: errors.New("boom")}},
		{"empty", &fakeLister{models: nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := regWithLister(tc.lister)
			models := projectAll(reg, liveModelSnapshot(context.Background(), port.NopDiagnostics{}, reg))
			orCount := 0
			for _, m := range models {
				if m.GetProviderId() == providerOpenRouter {
					orCount++
					if !curated[m.GetId()] {
						t.Fatalf("non-embedded id %q after fallback", m.GetId())
					}
				}
			}
			if orCount != len(curated) {
				t.Fatalf("fallback openrouter count = %d, want embedded %d", orCount, len(curated))
			}
		})
	}
}

// TestLiveSnapshotAvailabilityGating: openrouter NOT in the registry (no key) ⇒ its
// lister is NEVER called, and no openrouter models appear. openai (keyed, no
// lister) shows its embedded subset.
func TestLiveSnapshotAvailabilityGating(t *testing.T) {
	lister := &fakeLister{models: []modelEntry{{ID: "should/never-appear"}}}
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerOpenAI: {id: providerOpenAI, provider: mockllm.New(mockllm.TextTurn("x")), available: true},
			// openrouter intentionally ABSENT (no key) — its lister exists in the test
			// but is not wired into any available entry.
		},
		defaultID: providerOpenAI,
	}
	models := projectAll(reg, liveModelSnapshot(context.Background(), port.NopDiagnostics{}, reg))
	for _, m := range models {
		if m.GetProviderId() == providerOpenRouter {
			t.Fatalf("openrouter model %q shown for an unavailable provider", m.GetId())
		}
	}
	if lister.calls.Load() != 0 {
		t.Fatalf("unavailable provider's lister was called %d times", lister.calls.Load())
	}
}

// TestLiveOnlyModelImageSingleSource: a live-only TEXT-ONLY model advertises
// image=false even though the (openai-adapter-backed) provider reports Image:true —
// proving the modality source for a live model is the LIVE metadata via the SHARED
// hasImageModality predicate, not the adapter-only fallback. A live VISION model
// advertises image=true.
func TestLiveOnlyModelImageSingleSource(t *testing.T) {
	lister := &fakeLister{models: []modelEntry{
		{ID: "text/only", InputModalities: []string{"text"}},
		{ID: "vision/cap", InputModalities: []string{"text", "image"}},
	}}
	reg := regWithLister(lister) // openrouter provider reports Image:true
	models := projectAll(reg, liveModelSnapshot(context.Background(), port.NopDiagnostics{}, reg))
	got := map[string]bool{}
	for _, m := range models {
		if m.GetProviderId() == providerOpenRouter {
			got[m.GetId()] = m.GetImage()
		}
	}
	if got["text/only"] {
		t.Error("text-only live model advertises image=true (adapter-only fallback leaked; modality source not live)")
	}
	if !got["vision/cap"] {
		t.Error("vision live model advertises image=false (adapter Image:true ∩ live image should be true)")
	}
	// Single-source check: the picker's image == adapterCaps.Image && hasImageModality(modalities).
	for _, m := range lister.models {
		want := hasImageModality(m.InputModalities) // adapter is Image:true here
		if got[m.ID] != want {
			t.Errorf("model %q image=%v, shared predicate says %v", m.ID, got[m.ID], want)
		}
	}
}

// orEmbeddedIDs returns the embedded openrouter model ids as a set, the fallback
// floor the tests assert against.
func orEmbeddedIDs(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, m := range embeddedModels(providerOpenRouter) {
		out[m.ID] = true
	}
	if len(out) == 0 {
		t.Fatal("no embedded openrouter models — fixture assumption broken")
	}
	return out
}

// --- E2E through the real openrouter adapter + a mock transport (no network) ---

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestLiveSnapshotThroughRealAdapter wires the REAL openrouter.Lister (over a mock
// transport serving the trimmed fixture) into a registry and asserts the merged
// snapshot reflects the live (fixture) openrouter models — more than the curated
// subset, and a fixture id present.
func TestLiveSnapshotThroughRealAdapter(t *testing.T) {
	fixture, err := os.ReadFile("../adapter/openrouter/testdata/models.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" {
			t.Error("CWE-200: Authorization header sent to keyless endpoint")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(string(fixture))),
			Header:     make(http.Header),
		}, nil
	})}
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
	}
	models := projectAll(reg, liveModelSnapshot(context.Background(), port.NopDiagnostics{}, reg))
	ids := map[string]*mecatlv1.ModelInfo{}
	for _, m := range models {
		ids[m.GetId()] = m
	}
	// A fixture id present (live, not embedded).
	if _, ok := ids["openrouter/fusion"]; !ok {
		t.Fatalf("fixture live model openrouter/fusion absent; got %d models", len(models))
	}
	// The image single-source: openrouter/fusion is text-only ⇒ image=false even
	// though the adapter reports Image:true.
	if ids["openrouter/fusion"].GetImage() {
		t.Error("text-only fixture model advertises image=true")
	}
	// qwen is text+image ⇒ image=true.
	if q, ok := ids["qwen/qwen3.7-plus"]; ok && !q.GetImage() {
		t.Error("text+image fixture model advertises image=false")
	}
}

// TestBuildAsyncSwap drives the FULL composition (app.Build → server.Service) with
// the live-model refresh forced SYNCHRONOUS (a deterministic test seam, no sleeps)
// and a mock transport serving the openrouter fixture. It proves: (1) the
// ModelSelection cap is honest from the embedded seed; (2) after the refresh, the
// snapshot reflects the LIVE openrouter models (a fixture id present); (3) no
// secret leaks. The non-sync (background) path is covered by the e2e + goleak.
func TestBuildAsyncSwap(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	const sentinelKey = "sk-SENTINEL-asyncswap"

	// First, a synchronous-OFF build to confirm the SEED (embedded) is present
	// immediately and the cap is honest even before any live fetch.
	seedBuilt, err := Build(ctx, Config{
		Workspace:   workspace,
		NoSoul:      true,
		envDetector: fakeEnv(map[string]string{"OPENROUTER_API_KEY": sentinelKey}),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("x"))
		},
		// No transport + no sync ⇒ a real (nil-client) lister would hit the network in
		// the background goroutine; to keep this strictly offline, force sync OFF AND
		// inject a transport so even the background goroutine is offline.
		liveModelHTTPClient: fixtureClient(t),
	})
	if err != nil {
		t.Fatalf("Build(seed): %v", err)
	}
	// The seed is present synchronously (embedded openrouter subset is non-empty), so
	// ModelSelection is honest from t=0 regardless of the background refresh.
	if len(seedBuilt.Service.ListModels(ctx)) == 0 {
		t.Fatal("seed snapshot empty: ModelSelection would be dishonest at t=0")
	}
	seedBuilt.Close()

	// Now a synchronous-refresh build: the refresh runs inline before Build returns,
	// so ListModels deterministically reflects the LIVE (fixture) set.
	built, err := Build(ctx, Config{
		Workspace:   workspace,
		NoSoul:      true,
		envDetector: fakeEnv(map[string]string{"OPENROUTER_API_KEY": sentinelKey}),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("x"))
		},
		liveModelHTTPClient:  fixtureClient(t),
		liveModelRefreshSync: true,
	})
	if err != nil {
		t.Fatalf("Build(sync): %v", err)
	}
	defer built.Close()

	models := built.Service.ListModels(ctx)
	ids := map[string]bool{}
	for _, m := range models {
		ids[m.GetId()] = true
		for _, f := range []string{m.GetId(), m.GetProviderId(), m.GetDisplayName()} {
			if strings.Contains(f, sentinelKey) {
				t.Fatalf("secret leaked into ListModels field %q", f)
			}
		}
	}
	if !ids["openrouter/fusion"] {
		t.Fatalf("post-refresh snapshot missing live fixture model; got %d models", len(models))
	}
}

// --- The REAL background (async) path: runSync=false ---

// fakeSwapper records SetModels calls (the seam startLiveModelRefresh writes
// through). It is concurrency-safe so the refresh goroutine can write while the test
// reads after the joiner, and signals the FIRST swap on the swapped channel so a test
// can wait for the async swap DETERMINISTICALLY (no sleep).
type fakeSwapper struct {
	mu      sync.Mutex
	last    []*mecatlv1.ModelInfo
	calls   int
	wasSet  bool
	swapped chan struct{}
}

func newFakeSwapper() *fakeSwapper { return &fakeSwapper{swapped: make(chan struct{}, 1)} }

func (s *fakeSwapper) SetModels(m []*mecatlv1.ModelInfo) {
	s.mu.Lock()
	s.last = m
	s.calls++
	s.wasSet = true
	s.mu.Unlock()
	select {
	case s.swapped <- struct{}{}:
	default:
	}
}

func (s *fakeSwapper) snapshot() (set bool, calls int, last []*mecatlv1.ModelInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wasSet, s.calls, s.last
}

// blockingLister blocks in ListModels until the refresh's ctx is cancelled, then
// returns its canned models WITHOUT error (the worst case for the no-overwrite
// guard: a fetch that "succeeds" right as cancellation lands must still be
// suppressed by the ctx.Err() check, never swapped). It signals started so the test
// knows the goroutine is inside the fetch before it cancels — making the cancel
// strictly mid-fetch (deterministic, no sleep).
type blockingLister struct {
	started chan struct{}
	models  []modelEntry
}

func (b *blockingLister) ListModels(ctx context.Context) ([]modelEntry, error) {
	close(b.started)
	<-ctx.Done() // unblock only on cancel — so cancel provably precedes the guard
	return b.models, nil
}

// TestAsyncRefreshSwapsAfterJoin exercises the REAL background path (runSync=false):
// the goroutine spawns, fetches, swaps, and the returned closer's cancel+wg.Wait
// JOINS it — so after the closer returns, the swap has DETERMINISTICALLY landed (no
// sleep, the WaitGroup is the barrier). This covers the concurrency the sync-mode
// tests never run, and goleak (leakmain_test.go) confirms no goroutine lingers.
func TestAsyncRefreshSwapsAfterJoin(t *testing.T) {
	lister := &fakeLister{models: []modelEntry{
		{ID: "live/a", InputModalities: []string{"text"}},
		{ID: "live/b", InputModalities: []string{"text", "image"}},
	}}
	reg := regWithLister(lister)
	swap := newFakeSwapper()

	closer := startLiveModelRefresh(port.NopDiagnostics{}, reg, swap, false, 0) // ASYNC

	// Wait for the background swap to land on its OWN (deterministic via the swapped
	// channel — NO sleep, NO poll), then join+cleanup. This exercises the real
	// goroutine-spawn → fetch → swap path; goleak (leakmain_test.go) confirms the
	// joiner leaves nothing behind.
	<-swap.swapped
	closer() // cancel + wg.Wait → the goroutine has fully unwound

	set, calls, last := swap.snapshot()
	if !set || calls != 1 {
		t.Fatalf("after join: set=%v calls=%d, want exactly one swap", set, calls)
	}
	// The swap carries the merged live set (live REPLACES embedded for openrouter,
	// openai keeps its embedded subset).
	var sawLiveA bool
	for _, m := range last {
		if m.GetProviderId() == providerOpenRouter && m.GetId() == "live/a" {
			sawLiveA = true
		}
	}
	if !sawLiveA {
		t.Fatal("post-join swap missing the live openrouter model")
	}
	if lister.calls.Load() != 1 {
		t.Fatalf("lister called %d times, want 1", lister.calls.Load())
	}
}

// gateLister signals `entered` when ListModels is reached and BLOCKS there until the
// test closes `release`, then returns its canned models. It is the causal barrier the
// delay-holds-swap test pins on: reaching `entered` PROVES the goroutine cleared the
// delay and is now inside the fetch, so a "swap has not happened yet" read taken while
// the lister is still blocked is synchronized — not a wall-clock guess.
type gateLister struct {
	entered    chan struct{}
	release    chan struct{}
	releaseOne sync.Once
	models     []modelEntry
	calls      atomic.Int32
}

func (g *gateLister) ListModels(context.Context) ([]modelEntry, error) {
	g.calls.Add(1)
	close(g.entered)
	<-g.release
	return g.models, nil
}

// unblock releases the gated fetch at most once (safe to call from both the test body
// and a t.Cleanup safety net without a double-close panic).
func (g *gateLister) unblock() { g.releaseOne.Do(func() { close(g.release) }) }

// TestAsyncRefreshDelayHoldsSwap exercises the issue-#66 footer-heal repro seam: a
// non-zero delay holds the async goroutine BEFORE the fetch/swap, so the swap cannot
// land until the delay elapses AND the fetch runs. This is CAUSAL, not timing-based:
// the gateLister blocks inside ListModels, so reaching `entered` is the barrier that
// the delay is over and the fetch has begun — and while the lister is still blocked the
// swap PROVABLY has not happened (SetModels runs only after ListModels returns). We
// assert the un-swapped state under that barrier, then release the fetch and wait on
// the swapped channel for the post-delay swap. A short delay keeps the test quick; its
// expiry is no longer load-bearing for the "not yet swapped" assertion (the gate is).
func TestAsyncRefreshDelayHoldsSwap(t *testing.T) {
	lister := &gateLister{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		models:  []modelEntry{{ID: "live/a", InputModalities: []string{"text"}}},
	}
	reg := regWithLister(lister)
	swap := newFakeSwapper()

	closer := startLiveModelRefresh(port.NopDiagnostics{}, reg, swap, false, 10*time.Millisecond)
	t.Cleanup(func() { lister.unblock(); closer() }) // safety net: never leave the goroutine parked

	// CAUSAL barrier: the fetch is entered ONLY after the delay timer fires. Until then
	// the goroutine is parked in the delay select, so the gate is not yet reached.
	<-lister.entered
	// The lister is now blocked inside ListModels and has NOT returned, so SetModels
	// provably cannot have run — this read is synchronized by the gate, not the clock.
	if set, _, _ := swap.snapshot(); set {
		t.Fatal("swap landed before the fetch returned — impossible unless the delay seam let the swap race the hold")
	}
	// Release the fetch; the swap now lands (deterministic via the swapped channel).
	lister.unblock()
	<-swap.swapped
	if set, calls, _ := swap.snapshot(); !set || calls != 1 {
		t.Fatalf("post-delay: set=%v calls=%d, want exactly one swap", set, calls)
	}
}

// TestAsyncRefreshDelayCancelDuringHold proves the delay sleep is CANCELLABLE: a
// shutdown during the (long) hold joins promptly via the closer WITHOUT swapping a
// stale snapshot — the delay select observes ctx.Done() and returns before the fetch.
func TestAsyncRefreshDelayCancelDuringHold(t *testing.T) {
	lister := &fakeLister{models: []modelEntry{{ID: "live/a", InputModalities: []string{"text"}}}}
	reg := regWithLister(lister)
	swap := newFakeSwapper()

	// A long delay the test never waits out; the closer cancels mid-hold.
	closer := startLiveModelRefresh(port.NopDiagnostics{}, reg, swap, false, time.Hour)
	closer() // cancel + wg.Wait: the goroutine unwinds out of the delay select

	if set, calls, _ := swap.snapshot(); set || calls != 0 {
		t.Fatalf("after cancel-during-hold: set=%v calls=%d, want NO swap (delay cancelled before fetch)", set, calls)
	}
	if lister.calls.Load() != 0 {
		t.Fatalf("lister called %d times, want 0 (cancel preceded the fetch)", lister.calls.Load())
	}
}

// TestAsyncRefreshCancelMidFetchDoesNotOverwrite exercises the ctx.Err() no-
// overwrite guard: the closer is called WHILE the fetch is blocked (shutdown mid-
// fetch). After release + join, SetModels must NOT have been called — the seed is
// preserved, never overwritten with a partial/empty result.
func TestAsyncRefreshCancelMidFetchDoesNotOverwrite(t *testing.T) {
	lister := &blockingLister{
		started: make(chan struct{}),
		models:  []modelEntry{{ID: "live/late", InputModalities: []string{"text"}}},
	}
	reg := regWithLister(lister)
	swap := newFakeSwapper()

	closer := startLiveModelRefresh(port.NopDiagnostics{}, reg, swap, false, 0) // ASYNC

	<-lister.started // the goroutine is now blocked inside ListModels (mid-fetch)

	// Close cancels the ctx (unblocking the fetch) THEN wg.Wait joins. The fetch
	// returns models without error, but the goroutine's ctx.Err() guard suppresses
	// the swap because cancellation provably preceded the return.
	closer()

	set, calls, _ := swap.snapshot()
	if set || calls != 0 {
		t.Fatalf("cancel mid-fetch overwrote the seed: set=%v calls=%d, want no swap", set, calls)
	}
}

// --- refresh-completed flag (issue #66 provisional-0 echo) ---
//
// markRefreshCompleted MUST be set in every SETTLE path (sync swap, async success,
// async fetch-fail/empty fallback, no-lister no-op) so the echo resolver stops
// returning a provisional 0 and floors an uncatalogued live-only model to 128k. It
// must NOT be set on the shutdown-cancel paths (a shutdown is not a settled refresh).

// TestRefreshCompletedSyncPath: the synchronous swap path settles the flag.
func TestRefreshCompletedSyncPath(t *testing.T) {
	reg := regWithLister(&fakeLister{models: []modelEntry{{ID: "live/a"}}})
	reg.meta = newLiveMetaStore()
	if reg.meta.refreshCompleted() {
		t.Fatal("refreshCompleted true before the refresh ran")
	}
	startLiveModelRefresh(port.NopDiagnostics{}, reg, newFakeSwapper(), true, 0) // SYNC
	if !reg.meta.refreshCompleted() {
		t.Fatal("sync swap path did not mark the refresh completed")
	}
}

// TestRefreshCompletedAsyncSuccess: a successful async swap settles the flag, observed
// after the join.
func TestRefreshCompletedAsyncSuccess(t *testing.T) {
	reg := regWithLister(&fakeLister{models: []modelEntry{{ID: "live/a"}}})
	reg.meta = newLiveMetaStore()
	swap := newFakeSwapper()
	closer := startLiveModelRefresh(port.NopDiagnostics{}, reg, swap, false, 0)
	<-swap.swapped
	closer() // join the goroutine so the markRefreshCompleted after the swap is visible
	if !reg.meta.refreshCompleted() {
		t.Fatal("async success path did not mark the refresh completed")
	}
}

// TestRefreshCompletedAsyncFetchFail: a live FETCH FAILURE still falls back to the
// embedded floor, Swaps, and SETTLES the flag — so a no-network deployment floors and
// STOPS (the echo never sticks at provisional 0). The flag is the same on the empty-
// result path (resolveProviderModels folds both into the embedded fallback Swap).
func TestRefreshCompletedAsyncFetchFail(t *testing.T) {
	reg := regWithLister(&fakeLister{err: errors.New("offline")})
	reg.meta = newLiveMetaStore()
	swap := newFakeSwapper()
	closer := startLiveModelRefresh(port.NopDiagnostics{}, reg, swap, false, 0)
	<-swap.swapped
	closer()
	if !reg.meta.refreshCompleted() {
		t.Fatal("async fetch-fail path did not mark the refresh completed (echo would stick at provisional 0)")
	}
}

// TestRefreshCompletedNoLister: an openai/anthropic/mock-only deployment (no lister
// anywhere) settles the flag BEFORE the no-op early return, so the echo resolver
// floors an uncatalogued model instead of returning provisional 0 forever.
func TestRefreshCompletedNoLister(t *testing.T) {
	reg := &providerRegistry{
		entries:   map[string]providerEntry{providerOpenAI: {id: providerOpenAI, provider: mockllm.New(mockllm.TextTurn("x")), available: true}},
		defaultID: providerOpenAI,
		meta:      newLiveMetaStore(),
	}
	closer := startLiveModelRefresh(port.NopDiagnostics{}, reg, newFakeSwapper(), false, 0)
	closer()
	if !reg.meta.refreshCompleted() {
		t.Fatal("no-lister no-op path did not mark the refresh completed")
	}
}

// TestRefreshNotCompletedOnShutdownCancel: a shutdown during the delay hold (and a
// shutdown mid-fetch) is NOT a settled refresh — the flag must stay false so a
// restart-style rebuild does not falsely treat the cancelled refresh as settled.
func TestRefreshNotCompletedOnShutdownCancel(t *testing.T) {
	// (a) cancel during the delay hold: the delay select observes ctx.Done and returns
	// before the fetch/swap, so the flag is never set.
	regA := regWithLister(&fakeLister{models: []modelEntry{{ID: "live/a"}}})
	regA.meta = newLiveMetaStore()
	closerA := startLiveModelRefresh(port.NopDiagnostics{}, regA, newFakeSwapper(), false, time.Hour)
	closerA() // cancel mid-hold + join
	if regA.meta.refreshCompleted() {
		t.Fatal("cancel-during-hold marked the refresh completed (a shutdown is not a settle)")
	}

	// (b) cancel mid-fetch: the ctx.Err() guard returns before the swap, so the flag is
	// never set either.
	lister := &blockingLister{started: make(chan struct{}), models: []modelEntry{{ID: "live/late"}}}
	regB := regWithLister(lister)
	regB.meta = newLiveMetaStore()
	closerB := startLiveModelRefresh(port.NopDiagnostics{}, regB, newFakeSwapper(), false, 0)
	<-lister.started
	closerB() // cancel mid-fetch + join
	if regB.meta.refreshCompleted() {
		t.Fatal("cancel-mid-fetch marked the refresh completed (a shutdown is not a settle)")
	}
}

// --- D3 last-known-good (issue #262) -----------------------------------------

// TestResolveProviderModels_LastKnownGood_EmptyEmbeddedCatalog: a provider with
// an EMPTY embedded catalog (toolhive — no per-credential catalog to embed)
// falls back to the LAST successful live snapshot on a subsequent error,
// instead of going empty on a transient outage.
func TestResolveProviderModels_LastKnownGood_EmptyEmbeddedCatalog(t *testing.T) {
	lister := &fakeLister{models: []modelEntry{{ID: "m1"}, {ID: "m2"}}}
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerToolhive: {id: providerToolhive, intentDriven: true, lister: lister, available: true},
		},
		outcomes: newLiveOutcomeStore(),
	}

	got := resolveProviderModels(context.Background(), port.NopDiagnostics{}, reg, providerToolhive)
	if len(got) != 2 {
		t.Fatalf("first fetch: got %d models, want 2", len(got))
	}
	if status, ok := reg.outcomes.getStatus(providerToolhive); !ok || status.State != statusOK {
		t.Fatalf("status after success = %+v", status)
	}

	lister.err = errors.New("connection refused")
	got = resolveProviderModels(context.Background(), port.NopDiagnostics{}, reg, providerToolhive)
	if len(got) != 2 {
		t.Fatalf("after error: got %d models, want the retained last-known-good 2, got %+v", len(got), got)
	}
	if status, ok := reg.outcomes.getStatus(providerToolhive); !ok || status.State != statusUnreachable {
		t.Fatalf("status after failure = %+v, want unreachable", status)
	}
}

// TestResolveProviderModels_HonestEmptyReplaces: for a provider with no
// embedded floor, an honest (200, []) live response REPLACES to empty (R3.1) —
// it is a genuine successful listing, not a failure to paper over.
func TestResolveProviderModels_HonestEmptyReplaces(t *testing.T) {
	lister := &fakeLister{models: nil}
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerToolhive: {id: providerToolhive, intentDriven: true, lister: lister, available: true},
		},
		outcomes: newLiveOutcomeStore(),
	}
	got := resolveProviderModels(context.Background(), port.NopDiagnostics{}, reg, providerToolhive)
	if len(got) != 0 {
		t.Fatalf("got %d models, want 0 (honest empty)", len(got))
	}
	if status, ok := reg.outcomes.getStatus(providerToolhive); !ok || status.State != statusEmpty {
		t.Fatalf("status = %+v, want empty", status)
	}
}

// TestResolveProviderModels_NeverListedSuccessfully_YieldsNil: a provider that
// has NEVER listed successfully (no embedded floor, no last-known-good yet)
// yields a genuinely empty list — never a fabrication.
func TestResolveProviderModels_NeverListedSuccessfully_YieldsNil(t *testing.T) {
	lister := &fakeLister{err: errors.New("down")}
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerToolhive: {id: providerToolhive, intentDriven: true, lister: lister, available: true},
		},
		outcomes: newLiveOutcomeStore(),
	}
	if got := resolveProviderModels(context.Background(), port.NopDiagnostics{}, reg, providerToolhive); got != nil {
		t.Fatalf("got %v, want nil (never listed successfully)", got)
	}
}

// TestRefreshStaleModels_Cooldown proves the on-demand /models-open refresh
// (R1.4) is cooldown-gated: two calls in quick succession trigger only ONE
// actual lister fetch.
func TestRefreshStaleModels_Cooldown(t *testing.T) {
	lister := &fakeLister{models: []modelEntry{{ID: "m1"}}}
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerToolhive: {id: providerToolhive, intentDriven: true, lister: lister, available: true},
		},
		meta:     newLiveMetaStore(),
		outcomes: newLiveOutcomeStore(),
	}
	reg.outcomes.recordFailure(providerToolhive, statusUnreachable, "hint") // stale, so it IS a refresh candidate
	st := &refreshStaleModelsState{}
	swap := newFakeSwapper()

	refreshStaleModels(context.Background(), port.NopDiagnostics{}, reg, swap, st)
	refreshStaleModels(context.Background(), port.NopDiagnostics{}, reg, swap, st)

	if got := lister.calls.Load(); got != 1 {
		t.Fatalf("lister calls = %d, want 1 (the second call should be cooldown-gated)", got)
	}
}

// TestRefreshStaleModels_CooldownExpiryRefetches is the cooldown test's
// necessary complement: once the cooldown window has genuinely ELAPSED, a
// subsequent call DOES re-fetch — proving refreshStaleModels does not wedge
// permanently after its first call (a bug the cooldown-only test above cannot
// catch, since it never advances time).
func TestRefreshStaleModels_CooldownExpiryRefetches(t *testing.T) {
	// The lister keeps failing across both calls (unlike the success case)
	// so the provider stays a "stale" (non-ok) refresh candidate on the
	// second call too — a lister that SUCCEEDED on the first call would
	// mark the provider "ok" and the second call would (correctly) skip a
	// now-healthy provider, which would defeat this test's purpose.
	lister := &fakeLister{err: errors.New("still down")}
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerToolhive: {id: providerToolhive, intentDriven: true, lister: lister, available: true},
		},
		meta:     newLiveMetaStore(),
		outcomes: newLiveOutcomeStore(),
	}
	reg.outcomes.recordFailure(providerToolhive, statusUnreachable, "hint")
	st := &refreshStaleModelsState{}
	swap := newFakeSwapper()

	refreshStaleModels(context.Background(), port.NopDiagnostics{}, reg, swap, st)
	if got := lister.calls.Load(); got != 1 {
		t.Fatalf("lister calls after first refresh = %d, want 1", got)
	}

	// Force the cooldown to have elapsed (never sleep in a test): back-date
	// st.last past refreshStaleModelsCooldown. The provider is still
	// unreachable (recordFailure keeps status non-ok), so it remains a
	// refresh candidate.
	st.mu.Lock()
	st.last = time.Now().Add(-refreshStaleModelsCooldown - time.Second)
	st.mu.Unlock()

	refreshStaleModels(context.Background(), port.NopDiagnostics{}, reg, swap, st)
	if got := lister.calls.Load(); got != 2 {
		t.Fatalf("lister calls after cooldown expiry = %d, want 2 (a re-fetch should fire)", got)
	}
}

// TestRefreshStaleModels_SkipsHealthyAndNonIntentDriven proves the on-demand
// refresh re-fetches ONLY a stale intent-driven provider: a healthy toolhive
// entry and a non-intent-driven (openrouter) entry are both skipped.
func TestRefreshStaleModels_SkipsHealthyAndNonIntentDriven(t *testing.T) {
	toolhiveLister := &fakeLister{models: []modelEntry{{ID: "m1"}}}
	orLister := &fakeLister{models: []modelEntry{{ID: "should-never-be-fetched-here"}}}
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerToolhive:   {id: providerToolhive, intentDriven: true, lister: toolhiveLister, available: true},
			providerOpenRouter: {id: providerOpenRouter, lister: orLister, available: true}, // not intentDriven
		},
		meta:     newLiveMetaStore(),
		outcomes: newLiveOutcomeStore(),
	}
	reg.outcomes.recordSuccess(providerToolhive, []modelEntry{{ID: "already-ok"}}) // healthy, not stale
	st := &refreshStaleModelsState{}

	refreshStaleModels(context.Background(), port.NopDiagnostics{}, reg, newFakeSwapper(), st)

	if got := toolhiveLister.calls.Load(); got != 0 {
		t.Fatalf("healthy toolhive lister calls = %d, want 0", got)
	}
	if got := orLister.calls.Load(); got != 0 {
		t.Fatalf("non-intent-driven openrouter lister calls = %d, want 0 (must never be re-fetched here)", got)
	}
}

// TestResolveProviderModels_OpenRouterRegressionPin: a KEYED provider with a
// non-empty embedded catalog (openrouter) must keep falling back to embedded
// on error — the D3 last-known-good fallback must NEVER divert it, even when
// a last-known-good snapshot happens to be recorded. Byte-identical to
// pre-#262 behaviour.
func TestResolveProviderModels_OpenRouterRegressionPin(t *testing.T) {
	lister := &fakeLister{err: errors.New("boom")}
	reg := regWithLister(lister)
	reg.outcomes = newLiveOutcomeStore()
	reg.outcomes.recordSuccess(providerOpenRouter, []modelEntry{{ID: "should-never-win"}})

	got := resolveProviderModels(context.Background(), port.NopDiagnostics{}, reg, providerOpenRouter)
	curated := orEmbeddedIDs(t)
	if len(got) != len(curated) {
		t.Fatalf("got %d models, want the embedded floor (%d)", len(got), len(curated))
	}
	for _, m := range got {
		if m.ID == "should-never-win" {
			t.Fatal("last-known-good leaked into a provider with a non-empty embedded floor")
		}
	}
}

// TestResolveProviderModels_OpenRouterFailure_NoToolhiveHintLeak is the
// cleanup pin (statusHintFor scoping): a non-toolhive provider's (openrouter)
// lister failure records state=unreachable but hint=="" — the ToolHive
// remediation copy ("start it with `thv llm proxy start`") must never leak
// onto a different vendor's outage, even though it is currently filtered off
// the wire anyway (providerStatusProto is intentDriven-scoped) — this pins
// the recorded state itself, the earlier layer, not just the wire filter.
func TestResolveProviderModels_OpenRouterFailure_NoToolhiveHintLeak(t *testing.T) {
	lister := &fakeLister{err: errors.New("boom")}
	reg := regWithLister(lister)
	reg.outcomes = newLiveOutcomeStore()

	resolveProviderModels(context.Background(), port.NopDiagnostics{}, reg, providerOpenRouter)

	status, ok := reg.outcomes.getStatus(providerOpenRouter)
	if !ok {
		t.Fatal("no status recorded for the failed openrouter fetch")
	}
	if status.State != statusUnreachable {
		t.Errorf("state = %q, want %q", status.State, statusUnreachable)
	}
	if status.Hint != "" {
		t.Errorf("hint = %q, want empty (the ToolHive hint must never leak onto a different vendor)", status.Hint)
	}
}

// --- F2: publishSnapshot merge (issue #262 review finding 2, lost-update race) ---

// regForMergeTest builds a minimal registry (no real listers needed — this
// pins publishSnapshot/mergeSwap directly, not the fetch) with an openrouter
// entry and an intent-driven toolhive entry, mirroring the shape the review
// finding described: a full-catalog KEYED provider alongside the ToolHive
// gateway's own re-fetch-on-demand path.
func regForMergeTest() *providerRegistry {
	return &providerRegistry{
		entries: map[string]providerEntry{
			providerOpenRouter: {id: providerOpenRouter, available: true},
			providerToolhive:   {id: providerToolhive, available: true, intentDriven: true},
		},
		defaultID: providerOpenRouter,
		meta:      newLiveMetaStore(),
		outcomes:  newLiveOutcomeStore(),
	}
}

// TestPublishSnapshotMergesPerProvider is the FALSIFIABLE oracle for the F2
// fix: publish a FULL snapshot (openrouter's full catalog + toolhive), then
// publish a toolhive-ONLY partial (mirroring refreshStaleModels re-fetching
// only the stale intent-driven providers). openrouter's full catalog must
// survive BOTH in reg.meta (the resolver-feeding sink) and in the last
// SetModels slice (the picker sink) — a whole-map replace on the second
// (partial) publish would silently drop it, exactly the lost-update race the
// review flagged. This test FAILS if publishSnapshot's merge is reverted to
// a whole-map Swap (mutation-checked below).
func TestPublishSnapshotMergesPerProvider(t *testing.T) {
	reg := regForMergeTest()
	swap := newFakeSwapper()

	full := map[string][]modelEntry{
		providerOpenRouter: {{ID: "or/a"}, {ID: "or/b"}, {ID: "or/c"}},
		providerToolhive:   {{ID: "th/a"}},
	}
	publishSnapshot(port.NopDiagnostics{}, reg, swap, full)

	partial := map[string][]modelEntry{
		providerToolhive: {{ID: "th/b"}},
	}
	publishSnapshot(port.NopDiagnostics{}, reg, swap, partial)

	// openrouter's full catalog must survive in reg.meta (the resolver sink)...
	for _, id := range []string{"or/a", "or/b", "or/c"} {
		if _, ok := reg.meta.lookup(providerOpenRouter, id); !ok {
			t.Fatalf("openrouter model %q lost from the meta store after a toolhive-only partial publish (whole-map clobber)", id)
		}
	}
	// toolhive itself must reflect the LATEST (partial) fetch, not the stale first one.
	if _, ok := reg.meta.lookup(providerToolhive, "th/b"); !ok {
		t.Fatal("toolhive's own partial refresh did not land in the meta store")
	}

	// ...AND in the last SetModels slice (the picker sink) — the two sinks must agree.
	_, _, last := swap.snapshot()
	var orCount int
	for _, m := range last {
		if m.GetProviderId() == providerOpenRouter {
			orCount++
		}
	}
	if orCount != 3 {
		t.Fatalf("picker slice shows %d openrouter models after the toolhive-only partial publish, want 3 (full catalog must survive)", orCount)
	}
}

// TestResolveOutcomeHonestEmptyStillRemoves pins D3's "honest empty REPLACES"
// semantics through the NEW merge path: publishing toolhive with [m-1] then
// re-publishing toolhive with an explicit-but-EMPTY list must REMOVE it from
// the meta store (an honest empty response is a real successful listing, not
// a no-op to be merged away) — mergeSwap's put() no-op-on-empty-list must not
// be mistaken for "nothing changed" when the provider IS present in fresh.
func TestResolveOutcomeHonestEmptyStillRemoves(t *testing.T) {
	reg := regForMergeTest()
	swap := newFakeSwapper()

	publishSnapshot(port.NopDiagnostics{}, reg, swap, map[string][]modelEntry{
		providerToolhive: {{ID: "m-1"}},
	})
	if _, ok := reg.meta.lookup(providerToolhive, "m-1"); !ok {
		t.Fatal("setup: toolhive model did not land in the meta store")
	}

	publishSnapshot(port.NopDiagnostics{}, reg, swap, map[string][]modelEntry{
		providerToolhive: {}, // present, explicitly empty
	})
	if _, ok := reg.meta.lookup(providerToolhive, "m-1"); ok {
		t.Fatal("an honest empty publish did not remove the provider's stale model (D3 semantics regressed)")
	}
	_, _, last := swap.snapshot()
	for _, m := range last {
		if m.GetProviderId() == providerToolhive {
			t.Fatalf("toolhive model %q survived an honest-empty publish, want removed", m.GetId())
		}
	}
}

// TestRefreshStaleModelsConcurrentWithBackgroundRefresh is the -race smoke
// test for F2: refreshStaleModels (the on-demand /models-open re-fetch,
// scoped to stale intent-driven providers) runs concurrently with a loop of
// direct full publishes (standing in for the one-shot background refresh's
// publishSnapshot call) against the SAME registry. Under the OLD whole-map
// publish this interleaving could permanently revert a keyed provider to a
// stale/partial view; under the merge fix neither path can clobber a
// provider the other one didn't touch. Run with -race.
func TestRefreshStaleModelsConcurrentWithBackgroundRefresh(t *testing.T) {
	orLister := &fakeLister{models: []modelEntry{{ID: "or/live-a"}, {ID: "or/live-b"}}}
	thLister := &fakeLister{models: []modelEntry{{ID: "th/live-a"}}}
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerOpenRouter: {id: providerOpenRouter, available: true, lister: orLister},
			providerToolhive:   {id: providerToolhive, available: true, intentDriven: true, lister: thLister},
		},
		defaultID: providerOpenRouter,
		meta:      newLiveMetaStore(),
		outcomes:  newLiveOutcomeStore(),
	}
	reg.outcomes.recordFailure(providerToolhive, statusUnreachable, "hint") // stale ⇒ a refresh candidate
	swap := newFakeSwapper()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			st := &refreshStaleModelsState{} // fresh state per iteration: no cooldown gate to fight
			refreshStaleModels(context.Background(), port.NopDiagnostics{}, reg, swap, st)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			byProvider := liveModelSnapshot(context.Background(), port.NopDiagnostics{}, reg)
			publishSnapshot(port.NopDiagnostics{}, reg, swap, byProvider)
		}
	}()
	wg.Wait()

	// Post-quiescence: BOTH providers' live lists must be present — neither
	// concurrent path permanently reverted the other's provider.
	if _, ok := reg.meta.lookup(providerOpenRouter, "or/live-a"); !ok {
		t.Fatal("openrouter live model lost after concurrent publish/refresh")
	}
	if _, ok := reg.meta.lookup(providerToolhive, "th/live-a"); !ok {
		t.Fatal("toolhive live model lost after concurrent publish/refresh")
	}
}

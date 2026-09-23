package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/openaicompat"
	"github.com/stacklok/mecatl/internal/adapter/openrouter"
	"github.com/stacklok/mecatl/internal/syscaller"
)

// Coordination, publication and admission regressions live in
// provider_discovery*_test.go; these tests retain the protocol/consumer proofs.
func TestOpenCodeListerStampsImageModality(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"object":"list","data":[{"id":"glm-5.2","object":"model"}]}`)), Header: make(http.Header)}, nil
	})}
	rows, err := openCodeLister{inner: openaicompat.NewLister("https://opencode.example/v1", "k", client)}.ListModels(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListModels: %v, %v", rows, err)
	}
	if !hasImageModality(rows[0].InputModalities) || !rows[0].ToolCall {
		t.Fatalf("static capability lost: %+v", rows[0])
	}
}

func TestGatewayListerPropagatesContextWindow(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"object":"list","data":[{"id":"gpt-5.6","context_window":1050000}]}`)), Header: make(http.Header)}, nil
	})}
	rows, err := gatewayLister{inner: openaicompat.NewLister("https://gateway.example/v1", "k", client)}.ListModels(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListModels: %v, %v", rows, err)
	}
	if rows[0].ContextLimit != 1050000 {
		t.Fatalf("window=%d", rows[0].ContextLimit)
	}
}

func fixtureClient(t *testing.T) *http.Client {
	t.Helper()
	fixture, err := os.ReadFile("../adapter/openrouter/testdata/models.json")
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(fixture))), Header: make(http.Header)}, nil
	})}
}

func offlineHTTPClient() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("offline: live model fetch refused in test")
	})}
}

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

type principalLister struct{ seen chan *session.Principal }

func (l *principalLister) ListModels(ctx context.Context) ([]modelEntry, error) {
	l.seen <- session.PrincipalFromContext(ctx)
	return []modelEntry{{ID: "live/model"}}, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func regWithLister(lister modelLister) *providerRegistry {
	return &providerRegistry{entries: map[string]providerEntry{
		providerOpenAI:     {id: providerOpenAI, available: true, provider: mockllm.NewWith([]mockllm.Option{mockllm.WithCapabilities(port.ProviderCapabilities{Image: true})}, mockllm.TextTurn("x"))},
		providerOpenRouter: {id: providerOpenRouter, available: true, provider: mockllm.NewWith([]mockllm.Option{mockllm.WithCapabilities(port.ProviderCapabilities{Image: true})}, mockllm.TextTurn("x")), lister: lister},
	}, defaultID: providerOpenAI}
}

func bindDiscoveryFixture(t *testing.T, reg *providerRegistry) *providerDiscovery {
	t.Helper()
	if reg.discovery == nil {
		reg.discovery = newProviderDiscovery(reg, Config{ContextWindowOverride: reg.contextWindowOverride, contextWindows: reg.contextWindows})
		if reg.meta == nil {
			reg.meta = newLiveMetaStore()
		}
		reg.meta.owner = reg.discovery
	}
	t.Cleanup(reg.discovery.Close)
	return reg.discovery
}

func discoverProviderModels(t *testing.T, reg *providerRegistry, pid string) []modelEntry {
	t.Helper()
	d := bindDiscoveryFixture(t, reg)
	view, err := d.request(context.Background(), pid, discoveryPicker)
	if err != nil {
		t.Fatal(err)
	}
	models := view.providers[pid].observations
	if len(models) == 0 {
		return providerInventoryFloor(reg, pid)
	}
	return mergeCustomProviderFloor(reg, pid, models)
}

func discoverAllModels(t *testing.T, reg *providerRegistry) []*mecatlv1.ModelInfo {
	t.Helper()
	d := bindDiscoveryFixture(t, reg)
	d.refresh(context.Background(), discoveryPicker)
	return d.CurrentModelSnapshot().Models
}

func TestStartupModelRefreshListerSeesSystemPrincipal(t *testing.T) {
	lister := &principalLister{seen: make(chan *session.Principal, 1)}
	d := bindDiscoveryFixture(t, regWithLister(lister))
	d.start(true, 0)
	got := <-lister.seen
	if got == nil || got.Issuer != syscaller.Issuer || got.Subject != string(syscaller.RootModelCatalogRefresh) || got.GrantType != session.GrantTypeSystem {
		t.Fatalf("principal=%+v", got)
	}
}

func orEmbeddedIDs(t *testing.T) map[string]bool {
	t.Helper()
	ids := map[string]bool{}
	for _, m := range embeddedModels(providerOpenRouter) {
		ids[m.ID] = true
	}
	if len(ids) == 0 {
		t.Fatal("missing embedded OpenRouter fixture")
	}
	return ids
}

func TestLiveSnapshotReplacesEmbedded(t *testing.T) {
	lister := &fakeLister{models: []modelEntry{
		{ID: "live-only/model", DisplayName: "Live Only", ContextLimit: 1000000, InputModalities: []string{"text"}},
		{ID: "live-only/vision", DisplayName: "Live Vision", ContextLimit: 200000, InputModalities: []string{"text", "image"}, Reasoning: true},
	}}
	models := discoverAllModels(t, regWithLister(lister))
	orCount, openaiCount := 0, 0
	ids := orEmbeddedIDs(t)
	found := false
	for _, m := range models {
		if m.ProviderId == providerOpenRouter {
			orCount++
			found = found || m.Id == "live-only/model"
			if ids[m.Id] {
				t.Fatalf("catalog ID survived successful listing: %s", m.Id)
			}
		}
		if m.ProviderId == providerOpenAI {
			openaiCount++
		}
	}
	if !found || orCount != 2 || openaiCount == 0 {
		t.Fatalf("live replacement: found=%t live=%d other=%d", found, orCount, openaiCount)
	}
}

func TestLiveSnapshotFallsBackOnError(t *testing.T) {
	for _, failure := range []error{errors.New("offline"), nil} {
		models := discoverAllModels(t, regWithLister(&fakeLister{err: failure}))
		ids := orEmbeddedIDs(t)
		count := 0
		for _, m := range models {
			if m.ProviderId == providerOpenRouter {
				count++
				if !ids[m.Id] {
					t.Fatalf("unexpected fallback %s", m.Id)
				}
			}
		}
		if count != len(ids) {
			t.Fatalf("fallback size=%d, want %d", count, len(ids))
		}
	}
}

func TestLiveSnapshotAvailabilityGating(t *testing.T) {
	reg := &providerRegistry{entries: map[string]providerEntry{providerOpenAI: {id: providerOpenAI, available: true, provider: mockllm.New()}}, defaultID: providerOpenAI}
	models := discoverAllModels(t, reg)
	if len(models) == 0 {
		t.Fatal("available provider floor missing")
	}
	for _, m := range models {
		if m.ProviderId != providerOpenAI {
			t.Fatalf("unavailable provider advertised: %v", m)
		}
	}
}

func TestLiveOnlyModelImageSingleSource(t *testing.T) {
	lister := &fakeLister{models: []modelEntry{{ID: "text/only", InputModalities: []string{"text"}}, {ID: "vision/cap", InputModalities: []string{"text", "image"}}}}
	models := discoverAllModels(t, regWithLister(lister))
	got := map[string]bool{}
	for _, m := range models {
		if m.ProviderId == providerOpenRouter {
			got[m.Id] = m.Image
		}
	}
	if len(got) != 2 || got["text/only"] || !got["vision/cap"] {
		t.Fatalf("modality projection: %v", got)
	}
}

func TestLiveSnapshotThroughRealAdapter(t *testing.T) {
	fixture, err := os.ReadFile("../adapter/openrouter/testdata/models.json")
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" {
			t.Error("Authorization sent to keyless listing")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(fixture)))}, nil
	})}
	models := discoverAllModels(t, regWithLister(openRouterLister{inner: openrouter.NewLister(client)}))
	ids := map[string]*mecatlv1.ModelInfo{}
	for _, m := range models {
		if m.ProviderId == providerOpenRouter {
			ids[m.Id] = m
		}
	}
	if ids["openrouter/fusion"] == nil || ids["openrouter/fusion"].Image {
		t.Fatal("fixture text-only model missing or image-capable")
	}
	if q, ok := ids["qwen/qwen3.7-plus"]; ok && !q.Image {
		t.Fatal("fixture image capability lost")
	}
}

func TestBuildAsyncSwap(t *testing.T) {
	const key = "sk-SENTINEL-asyncswap"
	for _, synchronous := range []bool{false, true} {
		built, err := buildIsolated(t, context.Background(), Config{
			Workspace: t.TempDir(), NoSoul: true, envDetector: fakeEnv(map[string]string{"OPENROUTER_API_KEY": key}),
			providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider { return mockllm.New(mockllm.TextTurn("x")) },
			liveModelHTTPClient: fixtureClient(t), liveModelRefreshSync: synchronous,
		})
		if err != nil {
			t.Fatal(err)
		}
		models := built.Service.ListModels(context.Background())
		found := false
		for _, m := range models {
			found = found || m.Id == "openrouter/fusion"
			for _, field := range []string{m.Id, m.ProviderId, m.DisplayName} {
				if strings.Contains(field, key) {
					t.Fatal("credential leaked into inventory")
				}
			}
		}
		built.Close()
		if !found {
			t.Fatal("live fixture missing from full Build inventory")
		}
	}
}

func TestContextWindowUnavailableNamesConcreteOverrideExample(t *testing.T) {
	err := contextWindowUnavailable("gateway", "unknown")
	if !strings.Contains(err.Error(), "models.context_windows.gateway.unknown: 128000") {
		t.Fatalf("missing actionable override: %v", err)
	}
}

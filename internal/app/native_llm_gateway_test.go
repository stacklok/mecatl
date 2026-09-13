package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type nativeBearerFixture struct {
	mu        sync.Mutex
	token     string
	refresh   string
	gets      int
	refreshes int
}

func (s *nativeBearerFixture) Token(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	if s.token == "" {
		return "", llmendpoint.ErrNotEnrolled
	}
	return s.token, nil
}

func (s *nativeBearerFixture) Refresh(_ context.Context, rejected string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshes++
	if s.token != rejected {
		return s.token, nil
	}
	if s.refresh == "" {
		return "", llmendpoint.ErrNotEnrolled
	}
	s.token = s.refresh
	return s.token, nil
}

type nativeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f nativeRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func nativeDefinition(id string) permconfig.ProviderDefinition {
	return permconfig.ProviderDefinition{
		ID: id, BaseURL: "https://gateway.example.test/base/v1", DefaultModel: "native-model", APIFlavor: "openai-responses",
		Auth: permconfig.ProviderAuth{Method: "oidc", OIDC: &permconfig.ProviderOIDC{
			CredentialStore: &permconfig.OIDCCredentialStore{Home: "/credentials"},
		}},
	}
}

func nativeRegistryConfig(id string, source llmendpoint.BearerSource, transport http.RoundTripper) Config {
	return Config{
		ProviderDefinitions: permconfig.ProviderDefinitions{id: nativeDefinition(id)},
		NativeEndpointCredentialLoader: nativeCredentialLoaderFunc(func(context.Context, permconfig.ProviderDefinition) (llmendpoint.BearerSource, error) {
			if source == nil {
				return nil, llmendpoint.ErrNotEnrolled
			}
			return source, nil
		}),
		nativeEndpointTransport: transport,
		MockProvider:            mockllm.New(mockllm.TextTurn("ok")),
	}
}

func nativeModels(models []*mecatlv1.ModelInfo) []*mecatlv1.ModelInfo {
	out := make([]*mecatlv1.ModelInfo, 0, len(models))
	for _, model := range models {
		if model.GetProviderId() == "native" {
			out = append(out, model)
		}
	}
	return out
}

func TestNativeLLMGatewayLogin_Scenario1_DurableIdentityFailsClosed(t *testing.T) {
	cfg := nativeRegistryConfig("native", &nativeBearerFixture{token: "token"}, nativeRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("unused") }))
	cfg.MockProvider = nil
	cfg.OpenAIKey = "fallback"
	reg, err := buildProviderRegistry(cfg, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Lookup("native"); !ok {
		t.Fatal("configured endpoint is unavailable")
	}

	delete(cfg.ProviderDefinitions, "native")
	rebuilt, err := buildProviderRegistry(cfg, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rebuilt.Lookup("native"); ok {
		t.Fatal("removed endpoint rebound")
	}
	if _, err := resolveProviderSelection(rebuilt, "native"); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("selection error = %v", err)
	}
}

func TestNativeLLMGatewayLogin_Scenario2_NoProviderEntitlementSurface(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(providerEntry{}), reflect.TypeOf(providerRegistry{}), reflect.TypeOf(permconfig.NativeEndpointDefinition{})} {
		for i := 0; i < typ.NumField(); i++ {
			name := strings.ToLower(typ.Field(i).Name)
			if strings.Contains(name, "principal") || strings.Contains(name, "entitlement") || strings.Contains(name, "allowed") {
				t.Fatalf("provider entitlement surface introduced: %s.%s", typ.Name(), typ.Field(i).Name)
			}
		}
	}
}

func TestNativeLLMGatewayLogin_Scenario2_DeploymentWideEndpointAvailability(t *testing.T) {
	source := &nativeBearerFixture{token: "shared"}
	cfg := nativeRegistryConfig("native", source, nativeRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("unused") }))
	cfg.MockProvider = nil
	cfg.OpenAIKey = "fallback"
	reg, err := buildProviderRegistry(cfg, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	first, err := resolveProviderSelection(reg, "native")
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolveProviderSelection(reg, "native")
	if err != nil {
		t.Fatal(err)
	}
	if first.provider != second.provider {
		t.Fatal("deployment-wide endpoint was reminted per caller")
	}
}

func TestNativeLLMGatewayLogin_Scenario3_NoCredentialedBuildDiscovery(t *testing.T) {
	var requests int
	cfg := nativeRegistryConfig("native", &nativeBearerFixture{token: "shared"}, nativeRoundTripFunc(func(*http.Request) (*http.Response, error) { requests++; return nil, errors.New("unexpected request") }))
	cfg.MockProvider = nil
	cfg.OpenAIKey = "fallback"
	reg, err := buildProviderRegistry(cfg, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	inventory := newResolvedModelInventory(modelSnapshot(reg))
	closeRefresh := startLiveModelRefresh(port.NopDiagnostics{}, reg, inventory, true, 0)
	closeRefresh()
	if requests != 0 {
		t.Fatalf("Build refresh made %d authenticated endpoint requests", requests)
	}
	models := nativeModels(inventory.CurrentModels())
	if len(models) != 1 || models[0].GetProviderId() != "native" || models[0].GetId() != "native-model" {
		t.Fatalf("inventory floor = %#v", models)
	}
}

func TestNativeLLMGatewayLogin_Scenario3_DeploymentWideInventoryConsistency(t *testing.T) {
	cfg := nativeRegistryConfig("native", &nativeBearerFixture{token: "shared"}, nativeRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("unused") }))
	cfg.MockProvider = nil
	cfg.OpenAIKey = "fallback"
	reg, err := buildProviderRegistry(cfg, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	models := nativeModels(modelSnapshot(reg))
	inventory := newResolvedModelInventory(models)
	result := newAgentModelDiscoveryTool(inventory)
	if len(inventory.CurrentModels()) != 1 || !modelDiscoveryAvailable(reg, inventory) {
		t.Fatal("resolved inventory diverged")
	}
	if result.inventory != inventory {
		t.Fatal("DiscoverModels does not share deployment inventory")
	}
	if _, err := resolveProviderSelection(reg, models[0].GetProviderId()); err != nil {
		t.Fatal(err)
	}
}

func TestNativeLLMGatewayLogin_Scenario3_GlobalLiveInventory(t *testing.T) {
	transport := nativeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer shared" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"live-model"}]}`)), Request: r}, nil
	})
	cfg := nativeRegistryConfig("native", &nativeBearerFixture{token: "shared"}, transport)
	cfg.MockProvider = nil
	cfg.OpenAIKey = "fallback"
	reg, err := buildProviderRegistry(cfg, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	inventory := newResolvedModelInventory(modelSnapshot(reg))
	refreshStaleModels(context.Background(), port.NopDiagnostics{}, reg, inventory, &refreshStaleModelsState{})
	models := nativeModels(inventory.CurrentModels())
	if len(models) != 2 || models[0].GetId() != "live-model" || models[1].GetId() != "native-model" {
		t.Fatalf("global live inventory = %#v", models)
	}
}

func TestNativeLLMGatewayLogin_Scenario3_MissingCredentialBehavior(t *testing.T) {
	cfg := nativeRegistryConfig("native", nil, nil)
	cfg.MockProvider = nil
	cfg.OpenAIKey = "fallback"
	reg, err := buildProviderRegistry(cfg, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Lookup("native"); ok {
		t.Fatal("not-enrolled endpoint is usable")
	}
	statuses := providerStatusProto(reg)
	if len(statuses) != 1 || statuses[0].GetProviderId() != "native" || statuses[0].GetState() != "not-enrolled" {
		t.Fatalf("statuses = %#v", statuses)
	}
	if _, err := resolveProviderSelection(reg, "native"); !errors.Is(err, llmendpoint.ErrNotEnrolled) {
		t.Fatalf("explicit selection = %v", err)
	}
	cfg.DefaultProvider = "native"
	if _, err := buildProviderRegistry(cfg, func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "not enrolled") {
		t.Fatalf("default build error = %v", err)
	}
}

func TestNativeLLMGatewayLogin_Scenario5_BearerBoundaryAndSingleRetry(t *testing.T) {
	source := &nativeBearerFixture{token: "old", refresh: "new"}
	var calls int
	client, err := llmendpoint.NewGatewayHTTPClient("https://gateway.example.test/base/v1", source, nativeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		want := "Bearer old"
		status := http.StatusUnauthorized
		if calls == 2 {
			want, status = "Bearer new", http.StatusOK
		}
		if r.Header.Get("Authorization") != want {
			t.Fatalf("call %d authorization = %q", calls, r.Header.Get("Authorization"))
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("response")), Request: r}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, "https://gateway.example.test/base/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer caller-secret")
	if _, err := client.Do(req); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || source.refreshes != 1 {
		t.Fatalf("calls=%d refreshes=%d", calls, source.refreshes)
	}
	getsBefore := source.gets
	if _, err := client.Get("https://evil.example.test/base/v1/responses"); err == nil {
		t.Fatal("off-origin request accepted")
	}
	if source.gets != getsBefore {
		t.Fatal("token retrieved before confinement validation")
	}
	if _, err := client.Get("https://gateway.example.test/base/v1/../escape"); err == nil {
		t.Fatal("base escape accepted")
	}
	if _, err := client.Get("https://gateway.example.test/base/v1/%2e%2e/escape"); err == nil {
		t.Fatal("encoded base escape accepted")
	}
	if source.gets != getsBefore {
		t.Fatal("escape retrieved a token")
	}
}

func TestNativeLLMGatewayLogin_Scenario7_ToolHiveIdentityCompatibility(t *testing.T) {
	cfg := nativeRegistryConfig("native", &nativeBearerFixture{token: "shared"}, nil)
	cfg.MockProvider = nil
	cfg.OpenAIKey = "fallback"
	cfg.ToolhiveLLMBaseURL = "http://127.0.0.1:19090/v1"
	reg, err := buildProviderRegistry(cfg, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	toolhive, ok := reg.Lookup(providerToolhive)
	if !ok || !toolhive.intentDriven || toolhive.id != providerToolhive {
		t.Fatalf("toolhive entry = %#v, %v", toolhive, ok)
	}
	if native, _ := reg.Lookup("native"); native.provider == toolhive.provider {
		t.Fatal("matching protocol rebound ToolHive")
	}
}

func TestNativeLLMGatewayLogin_Scenario7_CredentialAuthorityIsolation(t *testing.T) {
	cfg := nativeRegistryConfig("native", &nativeBearerFixture{token: "shared"}, nil)
	cfg.MockProvider = nil
	cfg.OpenAIKey = "fallback"
	reg, err := buildProviderRegistry(cfg, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := reg.Lookup("native")
	if !ok || entry.intentDriven || entry.id == providerToolhive {
		t.Fatalf("native authority aliased: %#v", entry)
	}
}

func TestNativeLLMGatewayLogin_Scenario7_NoCrossProviderFallback(t *testing.T) {
	cfg := nativeRegistryConfig("native", nil, nil)
	cfg.MockProvider = nil
	cfg.OpenAIKey = "fallback"
	reg, err := buildProviderRegistry(cfg, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if entry, err := resolveProviderSelection(reg, "native"); err == nil || entry.id != "" {
		t.Fatalf("native failure fell back: %#v, %v", entry, err)
	}
	if reg.Default() != providerOpenAI {
		t.Fatalf("native failure mutated default to %q", reg.Default())
	}
}

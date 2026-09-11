package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
)

// fakeEnv builds an envDetector backed by a fixed map, so registry construction
// runs entirely OFFLINE (no real process environment, no network). A var absent
// from the map resolves to "" (unavailable), exactly like an unset env var.
func fakeEnv(vars map[string]string) envDetector {
	return func(name string) string { return vars[name] }
}

// mockllmImageProvider is an offline mock provider that advertises image input, so a
// modelCapability intersection over a catalogued image model yields Image:true from
// the catalog side without any network. Mirrors capability_test.go's regWithProvider.
func mockllmImageProvider() port.LLMProvider {
	return mockllm.NewWith(
		[]mockllm.Option{mockllm.WithCapabilities(port.ProviderCapabilities{Image: true})},
		mockllm.TextTurn("x"),
	)
}

// TestRegistryNProviders: both keys set ⇒ both providers available, sorted, each
// with a non-nil constructed provider.
func TestRegistryNProviders(t *testing.T) {
	reg, err := buildProviderRegistry(Config{Model: "gpt-5"}, fakeEnv(map[string]string{
		"OPENAI_API_KEY":     "sk-openai",
		"OPENROUTER_API_KEY": "sk-openrouter",
	}))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	got := reg.Available()
	want := []string{"openai", "openrouter"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Available() = %v, want %v", got, want)
	}
	for _, id := range want {
		e, ok := reg.Lookup(id)
		if !ok {
			t.Fatalf("Lookup(%q): not found", id)
		}
		if e.provider == nil {
			t.Errorf("provider %q: nil port.LLMProvider", id)
		}
		if !e.available {
			t.Errorf("provider %q: available=false, want true", id)
		}
	}
	// Default prefers openai (single-provider back-compat).
	if reg.Default() != "openai" {
		t.Errorf("Default() = %q, want openai", reg.Default())
	}
	// OpenRouter must carry the default base URL for diagnostics.
	if e, _ := reg.Lookup("openrouter"); e.baseURL != openRouterDefaultBaseURL {
		t.Errorf("openrouter baseURL = %q, want %q", e.baseURL, openRouterDefaultBaseURL)
	}
}

func TestRegistryOpenAIBearerTokenFileAvailabilityAndConflict(t *testing.T) {
	baseURL := "https://gateway.example/v1"
	bearerConfig := Config{
		OpenAIBearerTokenFile: "/var/run/secrets/tokens/openai",
		ProviderOverrides: permconfig.ProviderOverrides{
			providerOpenAI: {BaseURL: baseURL},
		},
	}
	reg, err := buildProviderRegistry(bearerConfig, fakeEnv(nil))
	if err != nil {
		t.Fatalf("file-only registry: %v", err)
	}
	if got := reg.Available(); !reflect.DeepEqual(got, []string{providerOpenAI}) {
		t.Fatalf("file-only Available() = %v, want [%s]", got, providerOpenAI)
	}

	for _, tc := range []struct {
		name string
		cfg  Config
		env  map[string]string
	}{
		{name: "environment key", cfg: bearerConfig, env: map[string]string{"OPENAI_API_KEY": "key"}},
		{name: "direct config key", cfg: func() Config { cfg := bearerConfig; cfg.OpenAIKey = "key"; return cfg }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildProviderRegistry(tc.cfg, fakeEnv(tc.env))
			if err == nil || !strings.Contains(err.Error(), "remove the OpenAI API-key credential or remove --openai-bearer-token-file") {
				t.Fatalf("key+file error = %v, want actionable conflict", err)
			}
		})
	}

	withoutBaseURL := bearerConfig
	withoutBaseURL.ProviderOverrides = nil
	if _, err := buildProviderRegistry(withoutBaseURL, fakeEnv(nil)); err == nil || !strings.Contains(err.Error(), "requires an explicit nonempty --openai-base-url") {
		t.Fatalf("file without base URL error = %v, want explicit base URL requirement", err)
	}

	reg, err = buildProviderRegistry(Config{}, fakeEnv(map[string]string{"OPENAI_API_KEY": "key"}))
	if err != nil {
		t.Fatalf("API-key registry regression: %v", err)
	}
	if _, ok := reg.Lookup(providerOpenAI); !ok {
		t.Fatal("API-key OpenAI provider unavailable")
	}
}

func TestRegistryOpenAIBearerTokenFileResponsesWiring(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	write := func(token string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var hits atomic.Int32
	var headers []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		headers = append(headers, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_text.delta\n"+
			`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"registry-ok"}`+"\n\n"+
			"event: response.completed\n"+
			`data: {"type":"response.completed","sequence_number":1,"response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`+"\n\n")
	}))
	defer srv.Close()

	reg, err := buildProviderRegistry(Config{
		OpenAIBearerTokenFile: path,
		LLMMaxAttempts:        4,
		ProviderOverrides: permconfig.ProviderOverrides{
			providerOpenAI: {BaseURL: srv.URL + "/v1"},
		},
	}, fakeEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := reg.Lookup(reg.Default())
	if !ok || entry.id != providerOpenAI {
		t.Fatalf("selected registry entry = %#v, want OpenAI", entry)
	}

	for _, token := range []string{"first-registry-token", "second-registry-token"} {
		write(token)
		seq, err := entry.provider.Stream(t.Context(), port.LLMRequest{Model: "model", Messages: []session.Message{session.NewUserMessage("hi")}})
		if err != nil {
			t.Fatal(err)
		}
		var text string
		var completed bool
		for chunk, err := range seq {
			if err != nil {
				t.Fatal(err)
			}
			if chunk.Kind == port.ChunkText {
				text += chunk.Text
			}
			if chunk.Kind == port.ChunkDone {
				completed = true
			}
		}
		if text != "registry-ok" || !completed {
			t.Fatalf("Responses result text=%q completed=%v", text, completed)
		}
	}
	if hits.Load() != 2 {
		t.Fatalf("Responses hits = %d, want 2", hits.Load())
	}
	wantHeaders := []string{"Bearer first-registry-token", "Bearer second-registry-token"}
	if !reflect.DeepEqual(headers, wantHeaders) {
		t.Fatalf("Authorization headers = %#v, want rotating headers", headers)
	}
}

func TestRegistryOpenAIBearerCredentialFailureIsTerminalAndRedacted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-token-path")
	const tokenMaterial = "failure-token-material"
	if err := os.WriteFile(path, []byte(tokenMaterial+" invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()

	reg, err := buildProviderRegistry(Config{
		OpenAIBearerTokenFile: path,
		LLMMaxAttempts:        4,
		LLMBreakerThreshold:   1,
		ProviderOverrides: permconfig.ProviderOverrides{
			providerOpenAI: {BaseURL: srv.URL + "/v1"},
		},
	}, fakeEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := reg.Lookup(providerOpenAI)
	for range 2 {
		seq, err := entry.provider.Stream(t.Context(), port.LLMRequest{Model: "model", Messages: []session.Message{session.NewUserMessage("hi")}})
		streamErr := err
		if err == nil {
			for _, err := range seq {
				if err != nil {
					streamErr = err
				}
			}
		}
		if streamErr == nil {
			t.Fatal("credential failure was not returned")
		}
		got := streamErr.Error()
		for _, secret := range []string{path, tokenMaterial} {
			if strings.Contains(got, secret) {
				t.Fatalf("credential error leaked secret material: %q", got)
			}
		}
		if strings.Contains(got, "circuit breaker") {
			t.Fatalf("credential failure counted toward breaker: %q", got)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("underlying transport hits = %d, want 0", hits.Load())
	}
}

// TestRegistryEnvDetectionSingle: only OPENROUTER_API_KEY set ⇒ only openrouter
// available (OPENROUTER_API_KEY is not in openai's env[] list, so openai stays off).
func TestRegistryEnvDetectionSingle(t *testing.T) {
	reg, err := buildProviderRegistry(Config{}, fakeEnv(map[string]string{
		"OPENROUTER_API_KEY": "sk-openrouter",
	}))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	if got := reg.Available(); !reflect.DeepEqual(got, []string{"openrouter"}) {
		t.Fatalf("Available() = %v, want [openrouter]", got)
	}
	if _, ok := reg.Lookup("openai"); ok {
		t.Error("openai should be UNAVAILABLE: OPENROUTER_API_KEY is not in its env[] list")
	}
}

// TestRegistryEnvDetectionOpenRouterFallback: OpenRouter is available when EITHER
// its own key OR (by convention) an OpenAI key resolves — the multi-env-var slice.
func TestRegistryEnvDetectionOpenRouterFallback(t *testing.T) {
	// Only the dedicated OpenRouter key: openrouter available, openai NOT.
	reg, err := buildProviderRegistry(Config{}, fakeEnv(map[string]string{
		"OPENROUTER_API_KEY": "sk-openrouter",
	}))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	if got := reg.Available(); !reflect.DeepEqual(got, []string{"openrouter"}) {
		t.Fatalf("Available() = %v, want [openrouter]", got)
	}
	if reg.Default() != "openrouter" {
		t.Errorf("Default() = %q, want openrouter (only available provider)", reg.Default())
	}
}

// TestRegistryEnvDetectionMultiVar: a SHARED OPENAI_API_KEY makes BOTH openai and
// openrouter available (openrouter falls back to the OpenAI key).
func TestRegistryEnvDetectionMultiVar(t *testing.T) {
	reg, err := buildProviderRegistry(Config{}, fakeEnv(map[string]string{
		"OPENAI_API_KEY": "sk-shared",
	}))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	got := reg.Available()
	want := []string{"openai", "openrouter"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Available() = %v, want %v (openrouter falls back to OPENAI_API_KEY)", got, want)
	}
}

// TestRegistryZeroKeys: an empty environment and !UseMock ⇒ the named, actionable
// errNoProvider naming BOTH env vars and the offline escape hatches.
func TestRegistryZeroKeys(t *testing.T) {
	_, err := buildProviderRegistry(Config{}, fakeEnv(nil))
	if err == nil {
		t.Fatal("buildProviderRegistry with no keys: want error, got nil")
	}
	if !errors.Is(err, errNoProvider) {
		t.Fatalf("error = %v, want errNoProvider", err)
	}
	msg := err.Error()
	// The copy must name every accepted credential env var (per adapter), the
	// compatible/proxy base-URL overrides (the endpoint-without-public-key case),
	// the offline --mock escape hatch, and the docs pointer — so first-run is
	// self-explanatory without leaving the terminal.
	for _, want := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "OPENROUTER_API_KEY", "OPENCODE_API_KEY",
		"--openai-base-url", "--anthropic-base-url", "--openrouter-base-url", "--opencode-base-url",
		"--mock", "https://mecatl.dev/docs/features/choose-models",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q does not mention %q", msg, want)
		}
	}
}

// TestNoKeyInStartupLogs captures the slog output of a real buildProviderRegistry
// with SENTINEL keys and asserts the key NEVER appears in any startup log line
// (CWE-200). The per-provider "LLM provider available" line logs id + base URL
// only; the resilience line logs knobs only. The base URLs themselves carry no
// credential (no userinfo / ?key= form), so the only forbidden tokens are the
// sentinel key and the env-var names.
func TestNoKeyInStartupLogs(t *testing.T) {
	const sentinelKey = "sk-SENTINEL-startup-log"
	var buf bytes.Buffer
	// The registry's startup lines now route through the injected port.Diagnostics
	// (iteration 2), not slog.Default(); capture THAT sink so the no-key assertion
	// inspects the real bytes the operator would see.
	diag := slogdiag.New(&buf, false, port.LevelDebug)

	_, err := buildProviderRegistry(Config{
		Model:       "gpt-5",
		Diagnostics: diag,
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return nil // never used for logging; the diagnostics lines fire before/around it
		},
	}, fakeEnv(map[string]string{
		"OPENAI_API_KEY":     sentinelKey,
		"OPENROUTER_API_KEY": sentinelKey,
	}))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	logs := buf.String()
	for _, bad := range []string{sentinelKey} {
		if strings.Contains(logs, bad) {
			t.Fatalf("startup logs leaked the sentinel key %q:\n%s", bad, logs)
		}
	}
}

// TestRegistryZeroKeysViaBuildProvider: the same zero-keys error surfaces through
// the buildProvider shim (the Build call site).
func TestRegistryZeroKeysViaBuildProvider(t *testing.T) {
	_, _, err := buildProvider(context.Background(), Config{envDetector: fakeEnv(nil)})
	if !errors.Is(err, errNoProvider) {
		t.Fatalf("buildProvider zero-keys error = %v, want errNoProvider", err)
	}
}

// TestRegistryMockShortCircuit: UseMock ⇒ a single "mock" entry regardless of the
// environment (even with real keys present).
func TestRegistryMockShortCircuit(t *testing.T) {
	reg, err := buildProviderRegistry(Config{
		UseMock:               true,
		OpenAIKey:             "ignored-config-key",
		OpenAIBearerTokenFile: "/ignored/bearer-file",
	}, fakeEnv(map[string]string{
		"OPENAI_API_KEY":     "sk-openai",
		"OPENROUTER_API_KEY": "sk-openrouter",
	}))
	if err != nil {
		t.Fatalf("buildProviderRegistry(UseMock): %v", err)
	}
	if got := reg.Available(); !reflect.DeepEqual(got, []string{"mock"}) {
		t.Fatalf("Available() = %v, want [mock]", got)
	}
	if reg.Default() != "mock" {
		t.Errorf("Default() = %q, want mock", reg.Default())
	}
	if got := reg.defaultModel; got != "mock" {
		t.Errorf("defaultModel = %q, want mock", got)
	}
	e, ok := reg.Lookup("mock")
	if !ok || e.provider == nil {
		t.Fatal("mock entry missing or nil provider")
	}
	// The real providers must NOT be constructed under the mock short-circuit.
	if _, ok := reg.Lookup("openai"); ok {
		t.Error("openai should not exist under UseMock")
	}

	if _, err := buildProviderRegistry(Config{
		MockProvider:          mockllm.New(mockllm.TextTurn("scripted")),
		OpenAIKey:             "ignored-config-key",
		OpenAIBearerTokenFile: "/ignored/bearer-file",
	}, fakeEnv(map[string]string{"OPENAI_API_KEY": "ignored-env-key"})); err != nil {
		t.Fatalf("buildProviderRegistry(MockProvider) must ignore real auth conflicts: %v", err)
	}
}

// TestResolveDefaultModelPrecedence: --model (cfg.Model) flows to modelID; the
// providerID is the preferred available provider (openai first, else sorted).
func TestResolveDefaultModelPrecedence(t *testing.T) {
	// --model set: returned verbatim as the model id.
	reg, err := buildProviderRegistry(Config{Model: "gpt-5-mini"}, fakeEnv(map[string]string{
		"OPENAI_API_KEY": "sk",
	}))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	pid, mid := resolveDefaultModel(Config{Model: "gpt-5-mini"}, reg)
	if pid != "openai" {
		t.Errorf("providerID = %q, want openai", pid)
	}
	if mid != "gpt-5-mini" {
		t.Errorf("modelID = %q, want gpt-5-mini (the --model flag)", mid)
	}

	// --model unset, openai available: the per-provider default "gpt-5".
	regOpenAI, err := buildProviderRegistry(Config{}, fakeEnv(map[string]string{
		"OPENAI_API_KEY": "sk",
	}))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	pid, mid = resolveDefaultModel(Config{}, regOpenAI)
	if pid != "openai" {
		t.Errorf("providerID = %q, want openai", pid)
	}
	if mid != "gpt-5" {
		t.Errorf("modelID = %q, want gpt-5 (the openai per-provider default)", mid)
	}

	// --model unset, openrouter-only: the per-provider default "openai/gpt-5" — the
	// hardcoded bare "gpt-5" would be an INVALID id at the OpenRouter endpoint, which
	// is the whole point of the per-provider default table.
	regNoModel, err := buildProviderRegistry(Config{}, fakeEnv(map[string]string{
		"OPENROUTER_API_KEY": "sk",
	}))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	pid, mid = resolveDefaultModel(Config{}, regNoModel)
	if pid != "openrouter" {
		t.Errorf("providerID = %q, want openrouter (only available)", pid)
	}
	if mid != "openai/gpt-5" {
		t.Errorf("modelID = %q, want openai/gpt-5 (the openrouter per-provider default)", mid)
	}

	// A provider with NO table entry falls back to "" (the adapter/endpoint default).
	// Synthesize that directly: a registry whose default is an untabled provider id.
	regUntabled := &providerRegistry{
		entries:   map[string]providerEntry{"futureprovider": {id: "futureprovider", available: true}},
		defaultID: "futureprovider",
	}
	if _, mid := resolveDefaultModel(Config{}, regUntabled); mid != "" {
		t.Errorf("modelID = %q, want \"\" for a provider absent from builtinDefaultModel", mid)
	}

	// The registry stores the resolved default model on itself (ResolvedDefaultModel()).
	if got := regNoModel.ResolvedDefaultModel(); got != "openai/gpt-5" {
		t.Errorf("regNoModel.ResolvedDefaultModel() = %q, want openai/gpt-5", got)
	}
	if got := regOpenAI.ResolvedDefaultModel(); got != "gpt-5" {
		t.Errorf("regOpenAI.ResolvedDefaultModel() = %q, want gpt-5", got)
	}
}

// TestResolveDefaultModelServerConfiguredDefault covers the issue-#21 tier: the
// server-configured deployment-wide default (Config.DefaultProvider/DefaultModel)
// slots BELOW the --model operator override and ABOVE the per-provider builtin
// table, and a configured DefaultProvider overrides the preferred default
// provider when available.
func TestResolveDefaultModelServerConfiguredDefault(t *testing.T) {
	// One registry with BOTH providers available, so the preferred default is
	// openai and "openrouter" is an available non-preferred provider.
	reg, err := buildProviderRegistry(Config{}, fakeEnv(map[string]string{
		"OPENAI_API_KEY":     "sk",
		"OPENROUTER_API_KEY": "sk",
	}))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}

	// (a) server default beats the builtin: no --model, configured DefaultModel ⇒
	// the configured model on the PREFERRED provider (no DefaultProvider set —
	// the pair-coherence subtlety: the model applies to the preferred provider).
	pid, mid := resolveDefaultModel(Config{DefaultModel: "gpt-5-mini"}, reg)
	if pid != "openai" || mid != "gpt-5-mini" {
		t.Errorf("(default model only) = (%q, %q), want (openai, gpt-5-mini) — server default must beat the builtin gpt-5", pid, mid)
	}

	// (b) --model (cfg.Model) beats the server default.
	pid, mid = resolveDefaultModel(Config{Model: "gpt-5.1", DefaultModel: "gpt-5-mini"}, reg)
	if pid != "openai" || mid != "gpt-5.1" {
		t.Errorf("(--model + default model) = (%q, %q), want (openai, gpt-5.1) — the operator override must beat the server default", pid, mid)
	}

	// (c) configured provider overrides the preferred provider; with no model
	// configured anywhere the builtin table entry for THAT provider applies.
	pid, mid = resolveDefaultModel(Config{DefaultProvider: "openrouter"}, reg)
	if pid != "openrouter" || mid != "openai/gpt-5" {
		t.Errorf("(default provider only) = (%q, %q), want (openrouter, openai/gpt-5) — the configured provider must override the openai preference", pid, mid)
	}

	// (d) the full configured pair: provider + model both honoured.
	pid, mid = resolveDefaultModel(Config{DefaultProvider: "openrouter", DefaultModel: "openai/gpt-5-mini"}, reg)
	if pid != "openrouter" || mid != "openai/gpt-5-mini" {
		t.Errorf("(configured pair) = (%q, %q), want (openrouter, openai/gpt-5-mini)", pid, mid)
	}

	// (e) the resolver stays TOTAL on an unavailable DefaultProvider (the
	// fail-fast rejection is validateDefaultModel's job at Build): the
	// preference is simply kept.
	pid, _ = resolveDefaultModel(Config{DefaultProvider: "anthropic"}, reg)
	if pid != "openai" {
		t.Errorf("(unavailable default provider) providerID = %q, want openai (resolver keeps the preference, the validator errors)", pid)
	}

	// (f) a registry BUILT with a configured Config.DefaultModel resolves its
	// EFFECTIVE default (ResolvedDefaultModel — a resolution RESULT, distinct
	// from the Config.DefaultModel tier feeding it) to the configured value when
	// no --model outranks it; that result is what Build adopts as cfg.Model.
	regCfg, err := buildProviderRegistry(Config{DefaultModel: "gpt-5-mini"}, fakeEnv(map[string]string{
		"OPENAI_API_KEY": "sk",
	}))
	if err != nil {
		t.Fatalf("buildProviderRegistry(DefaultModel): %v", err)
	}
	if got := regCfg.ResolvedDefaultModel(); got != "gpt-5-mini" {
		t.Errorf("regCfg.ResolvedDefaultModel() = %q, want gpt-5-mini (the configured deployment default won the resolution)", got)
	}
}

// TestBuildProviderOpenRouterDefaultModel: an OpenRouter-only environment with an
// EMPTY --model resolves the per-provider default "openai/gpt-5" (NOT the bare
// "gpt-5", which is invalid at the OpenRouter endpoint) AND that resolved model is
// catalogued, so the DefaultCapabilities intersection resolves (image true). This is
// the integration-ish path: buildProvider through the envDetector +
// providerConstructor seams (fully offline), then modelCapability over the resolved
// default. It is the exact no-selection path an OpenRouter-only operator hits.
func TestBuildProviderOpenRouterDefaultModel(t *testing.T) {
	cfg := Config{
		// Empty Model = the no-selection path (the new flag default).
		envDetector: fakeEnv(map[string]string{"OPENROUTER_API_KEY": "sk"}),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			// An image-capable adapter so the intersection's image bit comes purely
			// from the catalog (openai/gpt-5 is catalogued image-capable for openrouter).
			return mockllmImageProvider()
		},
	}
	reg, _, err := buildProvider(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildProvider: %v", err)
	}
	if reg.Default() != providerOpenRouter {
		t.Fatalf("Default() = %q, want openrouter", reg.Default())
	}
	if got := reg.ResolvedDefaultModel(); got != "openai/gpt-5" {
		t.Fatalf("ResolvedDefaultModel() = %q, want openai/gpt-5 (per-provider default)", got)
	}
	// DefaultCapabilities (Build wires modelCapability(reg, reg.Default(), cfg.Model)
	// with cfg.Model adopted from reg.ResolvedDefaultModel()): the catalog lookup must
	// succeed for the resolved default, so image resolves true.
	caps := modelCapability(reg, reg.Default(), reg.ResolvedDefaultModel())
	if !caps.Image {
		t.Fatalf("DefaultCapabilities.Image = false, want true (catalog openai/gpt-5 ∩ adapter image)")
	}
}

// recordingEnv wraps an envDetector so a test can observe WHICH env var names the
// registry queried — proving the names come from the catalog (+ the composition
// augmentation), not a hardcoded inline map.
func recordingEnv(vars map[string]string, queried *[]string) envDetector {
	return func(name string) string {
		*queried = append(*queried, name)
		return vars[name]
	}
}

// TestRegistryEnvDetectionCatalogDriven proves the env-var names consulted come
// from the providercatalog env[] (plus the openrouter composition augmentation),
// not the deleted inline builtinProviderEnv map. openai is queried for
// OPENAI_API_KEY; openrouter for BOTH OPENROUTER_API_KEY and OPENAI_API_KEY.
func TestRegistryEnvDetectionCatalogDriven(t *testing.T) {
	var openaiQueried []string
	if got := providerKey("", "openai", recordingEnv(nil, &openaiQueried)); got != "" {
		t.Fatalf("providerKey(openai) = %q, want \"\" (nothing set)", got)
	}
	if !reflect.DeepEqual(openaiQueried, []string{"OPENAI_API_KEY"}) {
		t.Errorf("openai queried %v, want [OPENAI_API_KEY] (from catalog env[])", openaiQueried)
	}

	var orQueried []string
	if got := providerKey("", "openrouter", recordingEnv(nil, &orQueried)); got != "" {
		t.Fatalf("providerKey(openrouter) = %q, want \"\"", got)
	}
	// catalog env[] = [OPENROUTER_API_KEY]; composition appends OPENAI_API_KEY.
	if !reflect.DeepEqual(orQueried, []string{"OPENROUTER_API_KEY", "OPENAI_API_KEY"}) {
		t.Errorf("openrouter queried %v, want [OPENROUTER_API_KEY OPENAI_API_KEY] (catalog + augmentation)", orQueried)
	}
}

// TestProviderEnvVarsFromCatalog asserts the composition helper returns the
// catalog's env[] for a provider (the openrouter case includes the augmentation).
func TestProviderEnvVarsFromCatalog(t *testing.T) {
	if got := providerEnvVars("openai"); !reflect.DeepEqual(got, []string{"OPENAI_API_KEY"}) {
		t.Errorf("providerEnvVars(openai) = %v, want [OPENAI_API_KEY] (from catalog)", got)
	}
	if got := providerEnvVars("openrouter"); !reflect.DeepEqual(got, []string{"OPENROUTER_API_KEY", "OPENAI_API_KEY"}) {
		t.Errorf("providerEnvVars(openrouter) = %v, want [OPENROUTER_API_KEY OPENAI_API_KEY]", got)
	}
	// An unknown provider id is an honest empty (unavailable), never a panic.
	if got := providerEnvVars("nonesuch"); got != nil {
		t.Errorf("providerEnvVars(nonesuch) = %v, want nil", got)
	}
	// opencode (OpenCode Go) is NOT in the vendored catalog, so its env var comes
	// from the explicit composition arm, not the catalog.
	if got := providerEnvVars("opencode"); !reflect.DeepEqual(got, []string{"OPENCODE_API_KEY"}) {
		t.Errorf("providerEnvVars(opencode) = %v, want [OPENCODE_API_KEY]", got)
	}
}

// TestRegistryOpenCode: with OPENCODE_API_KEY set, OpenCode Go registers as an
// AVAILABLE provider over the Chat Completions adapter, resolves its built-in
// default model, and carries a live lister + a remint closure. It also pins the
// no-clamp effort contract — unlike openai, xhigh/max are NOT clamped to high.
func TestRegistryOpenCode(t *testing.T) {
	reg, err := buildProviderRegistry(Config{}, fakeEnv(map[string]string{
		"OPENCODE_API_KEY": "sk-opencode",
	}))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	e, ok := reg.Lookup("opencode")
	if !ok {
		t.Fatal("opencode UNAVAILABLE with OPENCODE_API_KEY set")
	}
	if e.provider == nil {
		t.Error("opencode provider is nil")
	}
	if e.lister == nil {
		t.Error("opencode has no live lister (the OpenAI-shaped /models opt-in)")
	}
	if e.remint == nil {
		t.Error("opencode has no remint closure (per-session effort re-mint would break)")
	}
	if reg.defaultModel != "glm-5.2" {
		t.Errorf("default model = %q, want glm-5.2", reg.defaultModel)
	}
	// No xhigh/max clamp for opencode (the endpoint accepts them; verified live).
	if got, clamped := clampEffortForProvider(providerOpenCode, "xhigh"); got != "xhigh" || clamped {
		t.Errorf("clampEffortForProvider(opencode, xhigh) = %q,%v; want xhigh,false (no clamp)", got, clamped)
	}
}

// TestOpenRouterOpenAIKeyFallbackPreserved is the §4.2 regression tripwire: with
// ONLY OPENAI_API_KEY set (no OPENROUTER_API_KEY), openrouter must still be
// available via the composition augmentation — preserving S1 behaviour exactly.
func TestOpenRouterOpenAIKeyFallbackPreserved(t *testing.T) {
	reg, err := buildProviderRegistry(Config{}, fakeEnv(map[string]string{
		"OPENAI_API_KEY": "sk-shared",
	}))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	if _, ok := reg.Lookup("openrouter"); !ok {
		t.Error("openrouter UNAVAILABLE with only OPENAI_API_KEY set; the composition fallback regressed")
	}
}

// streamOnce drives a single Stream against the entry's provider, draining the
// iterator so the underlying HTTP request is actually issued. It returns the outer
// Stream error (nil on a successful start). Offline: the provider must point at a
// local stub server.
func streamOnce(t *testing.T, p port.LLMProvider) {
	t.Helper()
	seq, err := p.Stream(context.Background(), port.LLMRequest{
		Model:    "m",
		Messages: []session.Message{session.NewUserMessage("hi")},
	})
	if err != nil {
		// A failure to even start the request (e.g. unroutable base URL) is a wiring
		// failure for this test's purposes.
		t.Fatalf("Stream returned outer error: %v", err)
	}
	for range seq { //nolint:revive // drain to force the HTTP round-trip
	}
}

// TestRegistryOpenRouterEndpointOverrideRoutes proves the openrouter entry's provider
// was constructed with openai.WithBaseURL from the effective endpoint override — i.e. the base
// URL is actually WIRED INTO THE ADAPTER, not merely stored on the logging-only
// entry.baseURL field. It points the override at a local stub, streams once, and
// asserts the stub received the request at the configured path.
func TestRegistryOpenRouterEndpointOverrideRoutes(t *testing.T) {
	var hit atomic.Int32
	var gotPath atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Add(1)
		gotPath.Store(r.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Minimal terminal SSE so the iterator completes promptly.
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","sequence_number":0,"response":{"status":"completed"}}` + "\n\n"))
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}))
	defer srv.Close()

	reg, err := buildProviderRegistry(Config{
		ProviderOverrides: permconfig.ProviderOverrides{
			providerOpenRouter: {BaseURL: srv.URL + "/api/v1"},
		},
	}, fakeEnv(map[string]string{"OPENROUTER_API_KEY": "sk-openrouter"}))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	entry, ok := reg.Lookup("openrouter")
	if !ok {
		t.Fatal("openrouter entry missing")
	}
	if entry.baseURL != srv.URL+"/api/v1" {
		t.Errorf("entry.baseURL = %q, want the override %q", entry.baseURL, srv.URL+"/api/v1")
	}

	streamOnce(t, entry.provider)

	if hit.Load() == 0 {
		t.Fatal("the OpenRouter provider did not route to the configured base URL (WithBaseURL not wired into the adapter)")
	}
	// The openai SDK appends "/responses" under the configured base path.
	if p, _ := gotPath.Load().(string); !strings.HasPrefix(p, "/api/v1") {
		t.Errorf("request path = %q, want it under the /api/v1 override base", p)
	}
}

// TestRegistryOpenRouterDefaultBaseURL asserts that WITHOUT an override the openrouter
// entry carries the public OpenRouter base URL (the same value newOpenAICompatEntry passes
// to openai.WithBaseURL — the override-routing test above proves that same field is
// the one wired into the adapter). (Should-add #3, default branch.)
func TestRegistryOpenRouterDefaultBaseURL(t *testing.T) {
	reg, err := buildProviderRegistry(Config{}, fakeEnv(map[string]string{
		"OPENROUTER_API_KEY": "sk-openrouter",
	}))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	entry, ok := reg.Lookup("openrouter")
	if !ok {
		t.Fatal("openrouter entry missing")
	}
	if entry.baseURL != openRouterDefaultBaseURL {
		t.Errorf("default openrouter baseURL = %q, want %q", entry.baseURL, openRouterDefaultBaseURL)
	}
	if !strings.Contains(entry.baseURL, "openrouter.ai") {
		t.Errorf("default openrouter baseURL %q does not route to openrouter.ai", entry.baseURL)
	}
}

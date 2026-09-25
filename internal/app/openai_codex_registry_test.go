package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/openaicodex"
	openaiadapter "github.com/stacklok/mecatl/provider/openai"
)

var codexRegistryNow = time.Date(2035, time.January, 2, 3, 4, 5, 0, time.UTC)

const codexRegistryAccountID = "acct-codex-registry"

func codexRegistryToken(t *testing.T) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	claims, err := json.Marshal(map[string]any{
		"exp": codexRegistryNow.Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": codexRegistryAccountID,
		},
	})
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(claims) + "." + base64.RawURLEncoding.EncodeToString([]byte("test-signature"))
}

func codexRegistryCredential(t *testing.T) openaicodex.Credential {
	t.Helper()
	credential, err := openaicodex.NewCredential(codexRegistryToken(t), "", "", codexRegistryNow)
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	return credential
}

func codexRegistryConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		// Most registry tests exercise provider construction rather than discovery;
		// an explicit model keeps those tests on the explicit-model bypass.
		Model:                 "gpt-5",
		OpenAICodexCredential: codexRegistryCredential(t),
		openAICodexNow:        func() time.Time { return codexRegistryNow },
	}
}

func codexPolicyOptions(policy openaicodex.RequestPolicy) []openaiadapter.Option {
	return []openaiadapter.Option{
		openaiadapter.WithHTTPClient(policy.HTTPClientWithFinalTransport(withCodexSessionCorrelationTransport)),
		openaiadapter.WithMaxRetries(0),
	}
}

// TestRegistryOpenAICodexAvailability pins the billing-identity boundary: the
// manual token and the OpenAI API key independently enable their own provider.
func TestRegistryOpenAICodexAvailability(t *testing.T) {
	newConfig := func() Config {
		cfg := codexRegistryConfig(t)
		cfg.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("offline"))
		}
		return cfg
	}

	t.Run("token only", func(t *testing.T) {
		reg, err := buildProviderRegistry(newConfig(), fakeEnv(nil))
		if err != nil {
			t.Fatalf("buildProviderRegistry: %v", err)
		}
		if got := reg.Available(); !reflect.DeepEqual(got, []string{providerOpenAICodex}) {
			t.Fatalf("Available() = %v, want [%s]", got, providerOpenAICodex)
		}
	})

	t.Run("API key only", func(t *testing.T) {
		cfg := Config{providerConstructor: newConfig().providerConstructor}
		reg, err := buildProviderRegistry(cfg, fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-api-only"}))
		if err != nil {
			t.Fatalf("buildProviderRegistry: %v", err)
		}
		if _, ok := reg.Lookup(providerOpenAICodex); ok {
			t.Fatal("OpenAI API key enabled openai-codex")
		}
	})

	t.Run("both", func(t *testing.T) {
		reg, err := buildProviderRegistry(newConfig(), fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-api"}))
		if err != nil {
			t.Fatalf("buildProviderRegistry: %v", err)
		}
		for _, id := range []string{providerOpenAI, providerOpenAICodex} {
			if _, ok := reg.Lookup(id); !ok {
				t.Errorf("missing independently selectable provider %q", id)
			}
		}
	})

	t.Run("expired snapshot", func(t *testing.T) {
		cfg := newConfig()
		cfg.openAICodexNow = func() time.Time { return codexRegistryNow.Add(2 * time.Hour) }
		_, err := buildProviderRegistry(cfg, fakeEnv(nil))
		if err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("expired Codex-only registry error = %v, want actionable expiry error", err)
		}
		if strings.Contains(err.Error(), codexRegistryToken(t)) {
			t.Fatal("expired credential error disclosed the bearer token")
		}
	})
}

// TestADR_0104_OpenAICodexDefaultPrecedence locks the accepted ADR's provider
// ladder without relying on map iteration or accidental alphabetic order.
func TestADR_0104_OpenAICodexDefaultPrecedence(t *testing.T) {
	constructor := func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(mockllm.TextTurn("offline"))
	}
	cases := []struct {
		name        string
		env         map[string]string
		withCodex   bool
		explicit    string
		wantDefault string
	}{
		{name: "sole codex", withCodex: true, wantDefault: providerOpenAICodex},
		{name: "openrouter stays default", env: map[string]string{"OPENROUTER_API_KEY": "sk-or"}, withCodex: true, wantDefault: providerOpenRouter},
		{name: "anthropic stays default", env: map[string]string{"ANTHROPIC_API_KEY": "sk-ant"}, withCodex: true, wantDefault: providerAnthropic},
		{name: "opencode stays default", env: map[string]string{"OPENCODE_API_KEY": "sk-code"}, withCodex: true, wantDefault: providerOpenCode},
		{name: "openai stays default", env: map[string]string{"OPENAI_API_KEY": "sk-oai"}, withCodex: true, wantDefault: providerOpenAI},
		{name: "explicit codex wins", env: map[string]string{"OPENAI_API_KEY": "sk-oai"}, withCodex: true, explicit: providerOpenAICodex, wantDefault: providerOpenAICodex},
	}
	reg := &providerRegistry{entries: map[string]providerEntry{
		providerOpenAICodex: {id: providerOpenAICodex, available: true},
		providerToolhive:    {id: providerToolhive, available: true, intentDriven: true},
	}}
	if got := preferredDefaultProvider(reg); got != providerOpenAICodex {
		t.Errorf("Codex plus intent gateway default = %q, want %q", got, providerOpenAICodex)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Model: "gpt-5", providerConstructor: constructor, DefaultProvider: tc.explicit}
			if tc.withCodex {
				cfg.OpenAICodexCredential = codexRegistryCredential(t)
				cfg.openAICodexNow = func() time.Time { return codexRegistryNow }
			}
			reg, err := buildProviderRegistry(cfg, fakeEnv(tc.env))
			if err != nil {
				t.Fatalf("buildProviderRegistry: %v", err)
			}
			if got := reg.Default(); got != tc.wantDefault {
				t.Fatalf("Default() = %q, want %q", got, tc.wantDefault)
			}
		})
	}

	for _, effort := range []string{effortXHigh, effortMax} {
		if got, clamped := clampEffortForProvider(providerOpenAICodex, effort); got != effortHigh || !clamped {
			t.Errorf("Codex clamp(%q) = (%q, %t), want (%q, true)", effort, got, clamped, effortHigh)
		}
	}
}

type codexCapturedRequest struct {
	method string
	url    string
	header http.Header
	body   []byte
}

type codexCaptureTransport struct {
	mu       sync.Mutex
	requests []codexCapturedRequest
}

func (c *codexCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.requests = append(c.requests, codexCapturedRequest{
		method: req.Method,
		url:    req.URL.String(),
		header: req.Header.Clone(),
		body:   append([]byte(nil), body...),
	})
	c.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"event: response.completed\n" +
				`data: {"type":"response.completed","sequence_number":0,"response":{"status":"completed"}}` + "\n\n")),
		Request: req,
	}, nil
}

func (c *codexCaptureTransport) snapshot() []codexCapturedRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]codexCapturedRequest, len(c.requests))
	copy(result, c.requests)
	return result
}

func streamCodexTestRequest(t *testing.T, provider port.LLMProvider) {
	t.Helper()
	ctx := port.WithRootSessionID(port.WithSessionID(context.Background(), "active-session"), "root-session")
	stream, err := provider.Stream(ctx, port.LLMRequest{
		Model:    "gpt-5",
		Messages: []session.Message{session.NewUserMessage("hello")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for _, streamErr := range stream {
		if streamErr != nil {
			t.Fatalf("stream: %v", streamErr)
		}
	}
}

// TestOpenAICodexOptionsSurviveEveryRemint exercises the captured variadic
// option seam directly: the initial adapter and two successive remints must all
// pass through the immutable request policy.
func TestOpenAICodexOptionsSurviveEveryRemint(t *testing.T) {
	credential := codexRegistryCredential(t)
	capture := &codexCaptureTransport{}
	policy, err := openaicodex.NewRequestPolicy(credential, func() time.Time { return codexRegistryNow }, capture)
	if err != nil {
		t.Fatalf("NewRequestPolicy: %v", err)
	}
	initial := newOpenAICompatEntry(
		Config{}, providerOpenAICodex, "policy-owned", openaicodex.BaseURL,
		codexPolicyOptions(policy)...,
	)
	streamCodexTestRequest(t, initial.provider) // initial construct

	cfg := Config{
		Model:                 "gpt-5",
		OpenAICodexCredential: credential,
		openAICodexNow:        func() time.Time { return codexRegistryNow },
		openAICodexTransport:  capture,
	}
	reg, err := buildProviderRegistry(cfg, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	entry, ok := reg.Lookup(providerOpenAICodex)
	if !ok {
		t.Fatal("openai-codex entry missing")
	}
	streamCodexTestRequest(t, entry.provider)                                        // build-time default/capability remint
	streamCodexTestRequest(t, entry.remint(effortHigh, port.ProviderCapabilities{})) // session remint

	requests := capture.snapshot()
	if len(requests) != 3 {
		t.Fatalf("captured %d requests, want 3", len(requests))
	}
	wantURL := openaicodex.BaseURL + "/responses"
	for i, req := range requests {
		if req.method != http.MethodPost || req.url != wantURL {
			t.Errorf("request %d target = %s %s, want POST %s", i, req.method, req.url, wantURL)
		}
		if req.header.Get("ChatGPT-Account-ID") != codexRegistryAccountID ||
			req.header.Get("originator") != "mecatl" || req.header.Get("User-Agent") != openaicodex.UserAgent {
			t.Errorf("request %d lost one or more Codex policy headers", i)
		}
		if req.header.Get("Authorization") != "Bearer "+codexRegistryToken(t) ||
			req.header.Get("Accept") != "text/event-stream" ||
			req.header.Get("Content-Type") != "application/json" ||
			req.header.Get("X-Stainless-Retry-Count") != "0" {
			t.Errorf("request %d lost one or more fixed request-policy values", i)
		}
		if req.header.Get("X-Mecatl-Session-ID") != "active-session" || req.header.Get(rootSessionIDHeader) != "root-session" {
			t.Errorf("request %d correlation = active %q root %q", i,
				req.header.Get("X-Mecatl-Session-ID"), req.header.Get(rootSessionIDHeader))
		}
	}
}

// TestOpenAICodexRequestBodyParity proves Codex reuses the neutral Responses
// builder. The fixed endpoint and policy-owned headers differ; the JSON does not.
func TestOpenAICodexRequestBodyParity(t *testing.T) {
	credential := codexRegistryCredential(t)
	openAICapture := &codexCaptureTransport{}
	codexCapture := &codexCaptureTransport{}
	sharedConfig := Config{ReasoningEffort: effortHigh}

	openAI := newOpenAICompatEntry(sharedConfig, providerOpenAI, "sk-api", "https://api.openai.test/v1",
		openaiadapter.WithHTTPClient(&http.Client{Transport: openAICapture}))
	policy, err := openaicodex.NewRequestPolicy(credential, func() time.Time { return codexRegistryNow }, codexCapture)
	if err != nil {
		t.Fatalf("NewRequestPolicy: %v", err)
	}
	codex := newOpenAICompatEntry(sharedConfig, providerOpenAICodex, "policy-owned", openaicodex.BaseURL,
		codexPolicyOptions(policy)...)

	streamCodexTestRequest(t, openAI.provider)
	streamCodexTestRequest(t, codex.provider)
	openAIRequests, codexRequests := openAICapture.snapshot(), codexCapture.snapshot()
	if len(openAIRequests) != 1 || len(codexRequests) != 1 {
		t.Fatalf("captured OpenAI/Codex request counts = %d/%d, want 1/1", len(openAIRequests), len(codexRequests))
	}
	if openAIRequests[0].method != http.MethodPost || codexRequests[0].method != http.MethodPost {
		t.Fatalf("OpenAI/Codex methods = %q/%q, want POST/POST", openAIRequests[0].method, codexRequests[0].method)
	}
	if !bytes.Equal(openAIRequests[0].body, codexRequests[0].body) {
		t.Fatalf("neutral Responses JSON drifted\nopenai: %s\ncodex:  %s", openAIRequests[0].body, codexRequests[0].body)
	}
	if openAIRequests[0].url == codexRequests[0].url {
		t.Fatal("provider-specific URLs unexpectedly equal")
	}
}

// TestOpenAICodexAbsentPreservesExistingProviders pins the exact pre-Codex
// availability/default/base-URL behavior when no manual credential is configured.
func TestOpenAICodexAbsentPreservesExistingProviders(t *testing.T) {
	const toolhiveBaseURL = "http://127.0.0.1:14000/v1"
	cfg := Config{
		ToolhiveLLMBaseURL:  toolhiveBaseURL,
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("offline"))
		},
	}
	reg, err := buildProviderRegistry(cfg, fakeEnv(map[string]string{
		"OPENAI_API_KEY":     "sk-openai",
		"OPENROUTER_API_KEY": "sk-openrouter",
	}))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	if got, want := reg.Available(), []string{providerOpenAI, providerOpenRouter, providerOpenRouterAnthropic, providerToolhive, providerToolhiveAnthropic}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Available() = %v, want %v", got, want)
	}
	if reg.Default() != providerOpenAI {
		t.Fatalf("Default() = %q, want %q", reg.Default(), providerOpenAI)
	}
	if entry, ok := reg.Lookup(providerOpenRouter); !ok || entry.baseURL != openRouterDefaultBaseURL {
		t.Fatalf("openrouter presence/base URL match = (%t, %t), want (true, true)", ok, ok && entry.baseURL == openRouterDefaultBaseURL)
	}
	if entry, ok := reg.Lookup(providerToolhive); !ok || entry.baseURL != toolhiveBaseURL || !entry.intentDriven || entry.lister == nil {
		t.Fatal("absent Codex configuration changed the existing ToolHive construction contract")
	}
	if _, ok := reg.Lookup(providerOpenAICodex); ok {
		t.Fatal("absent Codex credential registered openai-codex")
	}
}

type codexModelsTransport struct {
	mu     sync.Mutex
	body   string
	status int
	calls  int
}

func (c *codexModelsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.calls++
	body, status := c.body, c.status
	c.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func (c *codexModelsTransport) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func codexModelsConfig(t *testing.T, transport http.RoundTripper) Config {
	t.Helper()
	cfg := codexRegistryConfig(t)
	cfg.Model = "explicit-model"
	cfg.openAICodexTransport = transport
	return cfg
}

func TestADR_0104_OpenAICodexNeverFallsBackToAPIInventory(t *testing.T) {
	if embedded := embeddedModels(providerOpenAICodex); len(embedded) != 0 {
		t.Fatalf("openai-codex embedded inventory = %#v, want empty", embedded)
	}
	for _, tc := range []struct {
		name      string
		status    int
		wantState string
	}{
		{name: "401", status: http.StatusUnauthorized, wantState: statusUnauthorized},
		{name: "403", status: http.StatusForbidden, wantState: statusUnauthorized},
		{name: "successful empty", status: http.StatusOK, wantState: statusEmpty},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &codexModelsTransport{status: tc.status, body: `{"models":[]}`}
			reg, err := buildProviderRegistry(codexModelsConfig(t, transport), fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-api"}))
			if err != nil {
				t.Fatalf("buildProviderRegistry: %v", err)
			}
			entry, ok := reg.Lookup(providerOpenAICodex)
			if !ok || entry.lister == nil {
				t.Fatal("openai-codex did not install its native entitlement lister")
			}
			models := discoverProviderModels(t, reg, providerOpenAICodex)
			if len(models) != 0 {
				t.Fatalf("Codex inventory fell back to %d API models: %#v", len(models), models)
			}
			outcome := reg.discovery.snapshot().providers[providerOpenAICodex].outcome
			if outcome.State != tc.wantState {
				t.Fatalf("outcome = %#v, want state %q", outcome, tc.wantState)
			}
		})
	}
}

func TestProviderModelDiscovery_Scenario2_CodexCatalogProvenance(t *testing.T) {
	apiModels := embeddedModels(providerOpenAI)
	if len(apiModels) == 0 {
		t.Fatal("test requires one embedded OpenAI metadata row")
	}
	var match modelEntry
	for _, candidate := range apiModels {
		if candidate.ContextLimit > 0 {
			match = candidate
			break
		}
	}
	if match.ID == "" {
		t.Fatal("test requires a positive OpenAI catalog window")
	}
	unknownID := "codex-entitled-unknown"
	body := fmt.Sprintf(`{"models":[
		{"slug":%q,"display_name":"","visibility":"list"},
		{"slug":%q,"display_name":"Unknown","visibility":"list"}
	]}`, match.ID, unknownID)
	transport := &codexModelsTransport{body: body}
	reg, err := buildProviderRegistry(codexModelsConfig(t, transport), fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	models := discoverProviderModels(t, reg, providerOpenAICodex)
	if len(models) != 2 {
		t.Fatalf("Codex inventory length = %d, want exactly entitlement length 2: %#v", len(models), models)
	}
	if models[0].ID != match.ID || models[0].ContextLimit != 0 ||
		!reflect.DeepEqual(models[0].InputModalities, match.InputModalities) {
		t.Fatalf("matching entitlement metadata = %#v, want enrichment from %#v", models[0], match)
	}
	resolved := resolveModelWindow(Config{}, reg.discovery.snapshot(), providerOpenAICodex, match.ID)
	if resolved.source != windowCatalog || resolved.tokens != match.ContextLimit {
		t.Fatalf("omitted Codex window provenance=%+v, want catalog/%d", resolved, match.ContextLimit)
	}
	projection := reg.discovery.CurrentModelSnapshot().Models
	if len(projection) != 2 {
		t.Fatalf("unentitled models advertised: %v", projection)
	}
	for _, row := range projection {
		if row.Id == match.ID && row.ContextLimit != int64(match.ContextLimit) {
			t.Fatalf("resolved catalog window not displayed: %v", row)
		}
	}
	if models[1].ID != unknownID || !reflect.DeepEqual(models[1].InputModalities, []string{"text", "image"}) {
		t.Fatalf("unknown entitlement = %#v, want selectable with adapter-static modalities", models[1])
	}
	if got := reg.windowResolver(Config{}, providerOpenAICodex, unknownID)(); got != defaultContextWindowTokens {
		t.Fatalf("unknown entitlement context window = %d, want conservative %d floor", got, defaultContextWindowTokens)
	}
}

func TestOpenAICodexDefaultBootstrap(t *testing.T) {
	modelsBody := `{"models":[
		{"slug":"server-first","display_name":"First","visibility":"list"},
		{"slug":"server-second","display_name":"Second","visibility":"list"}
	]}`
	t.Run("sole Codex chooses first entitlement", func(t *testing.T) {
		transport := &codexModelsTransport{body: modelsBody}
		cfg := codexRegistryConfig(t)
		cfg.Model = ""
		cfg.openAICodexTransport = transport
		reg, err := buildProviderRegistry(cfg, fakeEnv(nil))
		if err != nil {
			t.Fatalf("buildProviderRegistry: %v", err)
		}
		if got := reg.ResolvedDefaultModel(); got != "server-first" {
			t.Fatalf("ResolvedDefaultModel() = %q, want server-first", got)
		}
		if transport.callCount() != 1 {
			t.Fatalf("bootstrap calls = %d, want 1", transport.callCount())
		}
		reg.discovery.start(true, 0)
		t.Cleanup(reg.discovery.Close)
		if got := reg.discovery.snapshot().providers[providerOpenAICodex].observations; len(got) != 2 || got[0].ID != "server-first" {
			t.Fatalf("initial live snapshot = %#v, want bootstrapped entitlements", got)
		}
		if transport.callCount() != 1 {
			t.Fatalf("bootstrap plus initial refresh calls = %d, want 1", transport.callCount())
		}
	})

	t.Run("caller cancellation stops discovery", func(t *testing.T) {
		calls := 0
		cfg := codexRegistryConfig(t)
		cfg.Model = ""
		cfg.openAICodexTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("unexpected wire call")
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := buildProviderRegistryContext(ctx, cfg, fakeEnv(nil))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("bootstrap error = %v, want context.Canceled", err)
		}
		if calls != 0 {
			t.Fatalf("cancelled bootstrap made %d wire calls, want 0", calls)
		}
	})

	t.Run("explicit model bypasses discovery", func(t *testing.T) {
		transport := &codexModelsTransport{body: modelsBody}
		reg, err := buildProviderRegistry(codexModelsConfig(t, transport), fakeEnv(nil))
		if err != nil {
			t.Fatalf("buildProviderRegistry: %v", err)
		}
		if got := reg.ResolvedDefaultModel(); got != "explicit-model" {
			t.Fatalf("ResolvedDefaultModel() = %q, want explicit-model", got)
		}
		if transport.callCount() != 0 {
			t.Fatalf("explicit model made %d discovery calls, want 0", transport.callCount())
		}
	})

	t.Run("existing provider default bypasses Codex discovery", func(t *testing.T) {
		transport := &codexModelsTransport{body: modelsBody}
		cfg := codexRegistryConfig(t)
		cfg.Model = ""
		cfg.openAICodexTransport = transport
		reg, err := buildProviderRegistry(cfg, fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-api"}))
		if err != nil {
			t.Fatalf("buildProviderRegistry: %v", err)
		}
		if reg.Default() != providerOpenAI {
			t.Fatalf("Default() = %q, want %q", reg.Default(), providerOpenAI)
		}
		if transport.callCount() != 0 {
			t.Fatalf("non-Codex default made %d discovery calls, want 0", transport.callCount())
		}
	})

	for _, tc := range []struct {
		name      string
		transport *codexModelsTransport
		contains  string
	}{
		{name: "unauthorized is fatal", transport: &codexModelsTransport{status: http.StatusUnauthorized}, contains: "unauthorized"},
		{name: "empty is fatal", transport: &codexModelsTransport{body: `{"models":[]}`}, contains: "no picker-visible models"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := codexRegistryConfig(t)
			cfg.Model = ""
			cfg.openAICodexTransport = tc.transport
			_, err := buildProviderRegistry(cfg, fakeEnv(nil))
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("bootstrap error = %v, want substring %q", err, tc.contains)
			}
		})
	}

	t.Run("unreachable is fatal without explicit model", func(t *testing.T) {
		cfg := codexRegistryConfig(t)
		cfg.Model = ""
		cfg.openAICodexTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("offline")
		})
		_, err := buildProviderRegistry(cfg, fakeEnv(nil))
		if err == nil || !strings.Contains(err.Error(), "unreachable") {
			t.Fatalf("bootstrap error = %v, want unreachable", err)
		}
	})
}

func TestOpenAICodexLiveMetadataConverges(t *testing.T) {
	const (
		imageModelID = "codex-live-image"
		textModelID  = "codex-live-text"
	)
	transport := &codexModelsTransport{body: `{"models":[
		{"slug":"codex-live-image","display_name":"Live Image","visibility":"list",
		 "context_window":0,"max_context_window":196000,"input_modalities":["text","image"],
		 "supported_reasoning_levels":[{"effort":"medium"}]},
		{"slug":"codex-live-text","display_name":"Live Text","visibility":"list",
		 "context_window":144000,"max_context_window":999999,"input_modalities":["text"],
		 "supported_reasoning_levels":[]}
	]}`}
	reg, err := buildProviderRegistry(codexModelsConfig(t, transport), fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	picker := discoverAllModels(t, reg)
	if len(picker) != 2 {
		t.Fatalf("picker metadata did not converge: %#v", picker)
	}
	byID := map[string]*mecatlv1.ModelInfo{}
	for _, model := range picker {
		byID[model.GetId()] = model
	}
	imageModel, imageOK := byID[imageModelID]
	textModel, textOK := byID[textModelID]
	if !imageOK || !imageModel.GetImage() || !imageModel.GetReasoning() || imageModel.GetContextLimit() != 196000 {
		t.Fatalf("image picker metadata did not converge: %#v", imageModel)
	}
	if !textOK || textModel.GetImage() || textModel.GetReasoning() || textModel.GetContextLimit() != 144000 {
		t.Fatalf("text picker metadata did not converge: %#v", textModel)
	}
	if caps := modelCapability(reg, providerOpenAICodex, imageModelID); !caps.Image {
		t.Fatalf("session capability did not converge: %+v", caps)
	}
	if caps := modelCapability(reg, providerOpenAICodex, textModelID); caps.Image {
		t.Fatalf("text-only session capability gained image support: %+v", caps)
	}
	if supported, known := modelReasoningSupport(reg, providerOpenAICodex, imageModelID); !known || !supported {
		t.Fatalf("reasoning support = (%t, %t), want (true, true)", supported, known)
	}
	if got := reg.windowResolver(Config{}, providerOpenAICodex, imageModelID)(); got != 196000 {
		t.Fatalf("context window = %d, want 196000", got)
	}
	if got := reg.windowResolver(Config{}, providerOpenAICodex, textModelID)(); got != 144000 {
		t.Fatalf("text context window = %d, want primary context_window 144000", got)
	}
}

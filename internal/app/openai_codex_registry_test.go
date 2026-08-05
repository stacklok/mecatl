package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/openaicodex"
	openaiadapter "github.com/stacklok/mecatl/provider/openai"
)

var codexRegistryNow = time.Date(2035, time.January, 2, 3, 4, 5, 0, time.UTC)

func codexRegistryCredential(t *testing.T) openaicodex.Credential {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	claims, err := json.Marshal(map[string]any{
		"exp": codexRegistryNow.Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-codex-registry",
		},
	})
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(claims)
	signature := base64.RawURLEncoding.EncodeToString([]byte("test-signature"))
	credential, err := openaicodex.NewCredential(header+"."+payload+"."+signature, "", "", codexRegistryNow)
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	return credential
}

func codexRegistryConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		OpenAICodexCredential: codexRegistryCredential(t),
		openAICodexNow:        func() time.Time { return codexRegistryNow },
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
		if strings.Contains(err.Error(), cfg.OpenAICodexCredential.AccessToken()) {
			t.Fatal("expired credential error disclosed the bearer token")
		}
	})
}

// TestADR_0083_OpenAICodexDefaultPrecedence locks the accepted ADR's provider
// ladder without relying on map iteration or accidental alphabetic order.
func TestADR_0083_OpenAICodexDefaultPrecedence(t *testing.T) {
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
			cfg := Config{providerConstructor: constructor, DefaultProvider: tc.explicit}
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
	stream, err := provider.Stream(context.Background(), port.LLMRequest{
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
		Config{}, providerOpenAICodex, credential.AccessToken(), openaicodex.BaseURL,
		openaiadapter.WithRequestOption(policy.Options()...),
	)
	streamCodexTestRequest(t, initial.provider) // initial construct

	cfg := Config{
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
		if req.header.Get("ChatGPT-Account-ID") != credential.AccountID() ||
			req.header.Get("originator") != "mecatl" || req.header.Get("User-Agent") != openaicodex.UserAgent {
			t.Errorf("request %d lost one or more Codex policy headers", i)
		}
		if req.header.Get("Authorization") != "Bearer "+credential.AccessToken() ||
			req.header.Get("Accept") != "text/event-stream" ||
			req.header.Get("Content-Type") != "application/json" ||
			req.header.Get("X-Stainless-Retry-Count") != "0" {
			t.Errorf("request %d lost one or more fixed request-policy values", i)
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
	codex := newOpenAICompatEntry(sharedConfig, providerOpenAICodex, credential.AccessToken(), openaicodex.BaseURL,
		openaiadapter.WithRequestOption(policy.Options()...))

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
	if got, want := reg.Available(), []string{providerOpenAI, providerOpenRouter, providerToolhive}; !reflect.DeepEqual(got, want) {
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

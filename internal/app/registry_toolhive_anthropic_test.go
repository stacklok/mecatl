package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/toolhivellm"
	"github.com/stacklok/mecatl/provider/anthropic"
)

const toolhiveCompletedMessageSSE = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

type toolhiveWireCapture struct {
	mu             sync.Mutex
	paths          []string
	authorizations []string
	xAPIKeys       []string
	nativeBody     string
	responsesBody  string
	responsesHits  int
}

func (c *toolhiveWireCapture) handler(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.paths = append(c.paths, r.Method+" "+r.URL.Path)
	c.authorizations = append(c.authorizations, r.Header.Get("Authorization"))
	c.xAPIKeys = append(c.xAPIKeys, r.Header.Get("X-Api-Key"))
	if strings.HasSuffix(r.URL.Path, "/v1/responses") {
		c.responsesHits++
	}
	c.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/anthropic/v1/models"):
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolhiveAnthropicFixtureJSON)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/v1/models"):
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolhiveFixtureJSON)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/anthropic/v1/messages"):
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.nativeBody = string(body)
		c.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, toolhiveCompletedMessageSSE)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/responses"):
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.responsesBody = string(body)
		c.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_text.delta\n"+
			`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"done"}`+"\n\n"+
			"event: response.completed\n"+
			`data: {"type":"response.completed","sequence_number":1,"response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`+"\n\n")
	default:
		http.Error(w, "unexpected test path", http.StatusNotFound)
	}
}

func (c *toolhiveWireCapture) snapshot() (paths, auth, xAPI []string, nativeBody, responsesBody string, responses int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.paths...), append([]string(nil), c.authorizations...),
		append([]string(nil), c.xAPIKeys...), c.nativeBody, c.responsesBody, c.responsesHits
}

func TestADR_0325_ProtocolSpecificWireRouting(t *testing.T) {
	capture := &toolhiveWireCapture{}
	gateway := httptest.NewServer(http.HandlerFunc(capture.handler))
	defer gateway.Close()

	reg, err := buildProviderRegistry(Config{
		ToolhiveLLMBaseURL:  gateway.URL + "/v1",
		liveModelHTTPClient: gateway.Client(),
		LLMMaxAttempts:      1,
	}, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	if got, want := reg.Available(), []string{providerToolhive, providerToolhiveAnthropic}; !equalStrings(got, want) {
		t.Fatalf("Available() = %v, want %v", got, want)
	}
	if reg.Default() != providerToolhive {
		t.Fatalf("Default() = %q, want legacy %q", reg.Default(), providerToolhive)
	}

	native, ok := reg.Lookup(providerToolhiveAnthropic)
	if !ok {
		t.Fatal("toolhive-anthropic entry missing")
	}
	models := reg.discovery.snapshot().providers[providerToolhiveAnthropic].observations
	if len(models) != 1 {
		t.Fatalf("native last-known-good = %+v", models)
	}
	seq, err := native.provider.Stream(context.Background(), port.LLMRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []session.Message{session.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("construct native Anthropic stream: %v", err)
	}
	for _, streamErr := range seq {
		if streamErr != nil {
			t.Fatalf("native Anthropic stream: %v", streamErr)
		}
	}

	paths, auth, xAPI, body, _, responses := capture.snapshot()
	for _, want := range []string{"GET /v1/models", "GET /anthropic/v1/models", "POST /anthropic/v1/messages"} {
		if !slices.Contains(paths, want) {
			t.Errorf("requests %v missing %q", paths, want)
		}
	}
	if responses != 0 {
		t.Fatalf("native selection issued %d /v1/responses requests, want 0", responses)
	}
	if !strings.Contains(body, `"messages"`) || strings.Contains(body, `"input"`) {
		t.Fatalf("native request is not Anthropic Messages wire format: %s", body)
	}
	var requestBody struct {
		MaxTokens int64 `json:"max_tokens"`
		Thinking  struct {
			Type string `json:"type"`
		} `json:"thinking"`
	}
	if err := json.Unmarshal([]byte(body), &requestBody); err != nil {
		t.Fatalf("decode native request: %v", err)
	}
	if requestBody.MaxTokens != 64_000 || requestBody.Thinking.Type != "adaptive" {
		t.Fatalf("native request metadata controls = max_tokens:%d thinking:%q, want 64000/adaptive; body=%s",
			requestBody.MaxTokens, requestBody.Thinking.Type, body)
	}
	for i := range auth {
		if paths[i] == "GET /v1/models" {
			continue // legacy OpenAI proxy behavior is covered by its existing tests.
		}
		if auth[i] != "Bearer "+toolhivellm.PlaceholderToken || xAPI[i] != "" {
			t.Fatalf("native proxy auth on %s = Authorization %q, X-Api-Key %q", paths[i], auth[i], xAPI[i])
		}
	}

	m := models[0]
	if m.ContextLimit != 1_000_000 || m.OutputLimit != 64_000 || !m.Reasoning || !m.Thinking.Adaptive {
		t.Fatalf("native metadata = %+v", m)
	}
	projected := projectModelEntry(reg, Config{}, reg.discovery.snapshot(), providerToolhiveAnthropic, m)
	if !projected.GetImage() || !projected.GetReasoning() || projected.GetContextLimit() != 1_000_000 {
		t.Fatalf("projected native metadata = %+v", projected)
	}
	if got := reg.meta.outputLimitFor(providerToolhiveAnthropic, m.ID); got != 64_000 {
		t.Fatalf("native output limit = %d, want 64000", got)
	}
}

func TestToolhiveNativeAnthropic_BuildPublishesProbeMetadataBeforeImmediateRun(t *testing.T) {
	capture := &toolhiveWireCapture{}
	gateway := httptest.NewServer(http.HandlerFunc(capture.handler))
	defer gateway.Close()

	built, err := buildIsolated(t, context.Background(), Config{
		Workspace:             t.TempDir(),
		NoSoul:                true,
		ToolhiveLLMBaseURL:    gateway.URL + "/v1",
		liveModelHTTPClient:   gateway.Client(),
		liveModelRefreshDelay: time.Hour,
		LLMMaxAttempts:        1,
		envDetector:           fakeEnv(nil),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSessionWithProvider(context.Background(), session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: providerToolhiveAnthropic, ModelID: "claude-sonnet-4-6"})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	run, err := built.Service.StartRun(context.Background(), sess.ID, "reply once")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	runEvents(run)

	paths, _, _, body, _, responses := capture.snapshot()
	if !slices.Contains(paths, "POST /anthropic/v1/messages") || responses != 0 {
		t.Fatalf("immediate native run requests = %v, Responses hits = %d", paths, responses)
	}
	var requestBody struct {
		MaxTokens int64 `json:"max_tokens"`
		Thinking  struct {
			Type string `json:"type"`
		} `json:"thinking"`
	}
	if err := json.Unmarshal([]byte(body), &requestBody); err != nil {
		t.Fatalf("decode immediate native request: %v", err)
	}
	if requestBody.MaxTokens != 64_000 || requestBody.Thinking.Type != "adaptive" {
		t.Fatalf("immediate native metadata = max_tokens:%d thinking:%q; body=%s",
			requestBody.MaxTokens, requestBody.Thinking.Type, body)
	}
}

func TestToolhiveNativeAnthropic_Scenario1_Registration(t *testing.T) {
	reg, err := buildProviderRegistry(Config{
		ToolhiveLLMBaseURL:  "http://127.0.0.1:14000/v1",
		liveModelHTTPClient: toolhiveModelsClient(t, toolhiveFixtureJSON),
	}, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	if got, want := reg.Available(), []string{providerToolhive, providerToolhiveAnthropic}; !equalStrings(got, want) {
		t.Fatalf("Available() = %v, want %v", got, want)
	}
	if reg.Default() != providerToolhive {
		t.Fatalf("Default() = %q, want legacy provider %q", reg.Default(), providerToolhive)
	}
	if _, ok := resolveToolhiveIntent(Config{}); ok {
		t.Fatal("zero/disabled ToolHive intent registered providers")
	}
}

func TestToolhiveNativeAnthropic_Scenario1_Metadata(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(toolhiveAnthropicFixtureJSON)),
		}, nil
	})}
	lister := anthropicLister{inner: anthropic.NewLister("", "https://gateway.example/anthropic", client)}
	models, err := lister.ListModels(context.Background())
	if err != nil || len(models) != 1 {
		t.Fatalf("ListModels = %+v, %v", models, err)
	}
	if len(embeddedModels(providerToolhiveAnthropic)) != 0 {
		t.Fatal("public Anthropic catalog became unverified ToolHive inventory")
	}
	meta := newMetadataFixture()
	meta.setMetadataFixture(map[string][]modelEntry{providerToolhiveAnthropic: models})
	model := models[0]
	adaptive, enabled, known := meta.thinkingFor(providerToolhiveAnthropic, model.ID)
	if model.ContextLimit != 1_000_000 || meta.outputLimitFor(providerToolhiveAnthropic, model.ID) != 64_000 ||
		!hasImageModality(model.InputModalities) || !known || !adaptive || enabled {
		t.Fatalf("metadata projection = %+v, thinking=(%v,%v,%v)", model, adaptive, enabled, known)
	}
}

func TestToolhiveNativeAnthropic_Scenario2_DirectAuthentication(t *testing.T) {
	capture := &toolhiveWireCapture{}
	gateway := httptest.NewServer(http.HandlerFunc(capture.handler))
	defer gateway.Close()
	cfgPath := writeToolhiveConfigWithOIDC(t, gateway.URL+"/gateway", gateway.URL+"/issuer", "client-123")

	var factoryCalls atomic.Int32
	var tokenCalls atomic.Int32
	reg, err := buildProviderRegistry(Config{
		ToolhiveLLM:         true,
		ToolhiveLLMMode:     "direct",
		toolhiveConfigPath:  cfgPath,
		liveModelHTTPClient: gateway.Client(),
		LLMMaxAttempts:      1,
		toolhiveTokenSourceFactory: func(string, port.Diagnostics) (toolhivellm.TokenSourceFunc, error) {
			factoryCalls.Add(1)
			return func(context.Context) (string, error) {
				return fmt.Sprintf("fresh-%d", tokenCalls.Add(1)), nil
			}, nil
		},
	}, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	if factoryCalls.Load() != 1 {
		t.Fatalf("token-source factory calls = %d, want 1 shared family source", factoryCalls.Load())
	}
	if reg.Default() != providerToolhive {
		t.Fatalf("Default() = %q, want %q", reg.Default(), providerToolhive)
	}
	legacy, _ := reg.Lookup(providerToolhive)
	native, _ := reg.Lookup(providerToolhiveAnthropic)
	providers := []struct {
		name     string
		provider port.LLMProvider
	}{
		{name: "legacy", provider: legacy.provider},
		{name: "native", provider: native.provider},
		{name: "legacy-remint", provider: legacy.remint("high", port.ProviderCapabilities{})},
		{name: "native-remint", provider: native.remint("high", port.ProviderCapabilities{})},
	}
	for _, candidate := range providers {
		if _, err := driveStream(candidate.provider); err != nil {
			t.Fatalf("%s direct stream: %v", candidate.name, err)
		}
	}

	paths, auth, xAPI, _, responsesBody, responses := capture.snapshot()
	for _, want := range []string{
		"GET /gateway/v1/models",
		"GET /gateway/anthropic/v1/models",
		"POST /gateway/v1/responses",
		"POST /gateway/anthropic/v1/messages",
	} {
		if !slices.Contains(paths, want) {
			t.Errorf("requests %v missing %q", paths, want)
		}
	}
	if responses != 2 || !strings.Contains(responsesBody, `"input"`) || strings.Contains(responsesBody, `"messages"`) {
		t.Fatalf("legacy direct Responses requests = %d, body %q", responses, responsesBody)
	}
	seen := make(map[string]bool)
	for i, value := range auth {
		if !strings.HasPrefix(value, "Bearer fresh-") || value == "Bearer thv-proxy" || xAPI[i] != "" {
			t.Fatalf("direct auth on %s = Authorization %q, X-Api-Key %q", paths[i], value, xAPI[i])
		}
		seen[value] = true
	}
	if len(seen) != len(auth) || int(tokenCalls.Load()) != len(auth) {
		t.Fatalf("fresh-token accounting: unique=%d token_calls=%d requests=%d", len(seen), tokenCalls.Load(), len(auth))
	}
}

func TestToolhiveNativeAnthropic_Scenario2_TransportSecurity(t *testing.T) {
	base := &captureTransport{}
	client := newToolhiveBearerClient(&http.Client{Transport: base},
		func(context.Context) (string, error) { return toolhivellm.PlaceholderToken, nil })
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:14000/anthropic/v1/models", nil)
	req.Header.Set("Authorization", "Bearer stale")
	req.Header.Set("X-Api-Key", "must-not-forward")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	_ = resp.Body.Close()
	if got := base.auth.Load().(string); got != "Bearer "+toolhivellm.PlaceholderToken {
		t.Fatalf("Authorization = %q", got)
	}
	if got := base.xAPI.Load().(string); got != "" {
		t.Fatalf("X-Api-Key = %q, want stripped", got)
	}
	if gatewayURLIsHTTPS("http://gateway.example") {
		t.Fatal("non-loopback cleartext gateway passed the direct-mode HTTPS gate")
	}
}

func TestToolhiveNativeAnthropic_DirectCatalogDiagnosticsRedactResponseBody(t *testing.T) {
	const reflectedBearer = "reflected-bearer-do-not-log"
	var seenAuth atomic.Value
	base := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seenAuth.Store(req.Header.Get("Authorization"))
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"` + reflectedBearer + `"}}`)),
		}, nil
	})}
	client := newToolhiveBearerClient(base,
		func(context.Context) (string, error) { return reflectedBearer, nil })
	entry := providerEntry{
		id:           providerToolhiveAnthropic,
		available:    true,
		intentDriven: true,
		toolhiveMode: toolhiveModeDirect,
		lister: anthropicLister{inner: anthropic.NewLister(
			"", "https://gateway.example/anthropic", client)},
	}
	reg := &providerRegistry{
		entries: map[string]providerEntry{entry.id: entry},
	}
	diag := newCapturingDiagnostics()

	discovery := newProviderDiscovery(reg, Config{Diagnostics: diag})
	defer discovery.Close()
	view, err := discovery.request(context.Background(), entry.id, discoveryPicker)
	if err != nil {
		t.Fatal(err)
	}
	if got := view.providers[entry.id].observations; got != nil {
		t.Fatalf("unauthorized native catalog = %+v, want no inventory", got)
	}
	if got, _ := seenAuth.Load().(string); got != "Bearer "+reflectedBearer {
		t.Fatalf("catalog Authorization = %q", got)
	}
	records := strings.Join(diag.capturedStrings(), "\n")
	if strings.Contains(records, reflectedBearer) || strings.Contains(records, `"error"`) {
		t.Fatalf("diagnostics exposed native response body or credential: %s", records)
	}
	if !strings.Contains(records, providerToolhiveAnthropic) || !strings.Contains(records, statusUnauthorized) {
		t.Fatalf("diagnostics lost provider/state classification: %s", records)
	}
}

func TestToolhiveDirectRemintedProvidersRetainAuthPathsAndRedirectRefusal(t *testing.T) {
	var attackerHits atomic.Int32
	attacker := httptest.NewServer(terminalSSEHandler(&attackerHits))
	defer attacker.Close()

	var mu sync.Mutex
	var paths, authorizations, xAPIKeys []string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		paths = append(paths, req.URL.Path)
		authorizations = append(authorizations, req.Header.Get("Authorization"))
		xAPIKeys = append(xAPIKeys, req.Header.Get("X-Api-Key"))
		mu.Unlock()
		http.Redirect(w, req, attacker.URL, http.StatusTemporaryRedirect)
	}))
	defer gateway.Close()

	intent := toolhiveIntent{
		mode:       toolhiveModeDirect,
		baseURL:    gateway.URL + "/v1",
		gatewayURL: gateway.URL,
	}
	client := newToolhiveBearerClient(nil,
		func(context.Context) (string, error) { return "fresh-remint", nil })
	meta := newLiveMetaStore()
	cfg := Config{LLMMaxAttempts: 1}
	entries := []providerEntry{
		newDirectGatewayEntry(cfg, providerToolhive, intent, client),
		newToolhiveAnthropicEntry(cfg, intent, meta, client),
	}
	for _, entry := range entries {
		// The security contract is refusal to follow the redirect. Adapter-level
		// handling of the retained 307 response differs, so the target hit count
		// below is the authoritative assertion.
		_, _ = driveStream(entry.remint("high", port.ProviderCapabilities{}))
	}

	mu.Lock()
	defer mu.Unlock()
	for _, want := range []string{"/v1/responses", "/anthropic/v1/messages"} {
		if !slices.Contains(paths, want) {
			t.Errorf("reminted requests %v missing %q", paths, want)
		}
	}
	for i := range authorizations {
		if authorizations[i] != "Bearer fresh-remint" || xAPIKeys[i] != "" {
			t.Errorf("reminted auth on %s = Authorization %q, X-Api-Key %q",
				paths[i], authorizations[i], xAPIKeys[i])
		}
	}
	if got := attackerHits.Load(); got != 0 {
		t.Fatalf("redirect target received %d reminted requests", got)
	}
}

func TestToolhiveNativeAnthropic_Scenario1_DiscoveryPaths(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"http://127.0.0.1:14000/v1", "http://127.0.0.1:14000/anthropic"},
		{"https://gateway.example/prefix/v1/", "https://gateway.example/prefix/anthropic"},
		{"https://gateway.example/prefix/v1/extra", "https://gateway.example/prefix/v1/extra/anthropic"},
		{"https://user:secret@gateway.example/prefix/v1?token=secret#fragment", "https://gateway.example/prefix/anthropic"},
	} {
		if got := toolhiveAnthropicBaseURL(tc.in); got != tc.want {
			t.Errorf("toolhiveAnthropicBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestToolhiveNativeAnthropic_Scenario3_IndependentOutcomes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		openAI := &fakeLister{models: []modelEntry{{ID: "openai-good"}}}
		native := &fakeLister{err: fmt.Errorf("native down")}
		reg := &providerRegistry{entries: map[string]providerEntry{
			providerToolhive:          {id: providerToolhive, available: true, intentDriven: true, lister: openAI},
			providerToolhiveAnthropic: {id: providerToolhiveAnthropic, available: true, intentDriven: true, lister: native},
		}}
		d := bindDiscoveryFixture(t, reg)
		d.refresh(context.Background(), discoveryPicker)
		synctest.Wait()
		first := d.snapshot()
		if len(first.providers[providerToolhive].observations) != 1 || len(first.providers[providerToolhiveAnthropic].observations) != 0 {
			t.Fatalf("first snapshot=%+v", first.providers)
		}
		openAI.err = fmt.Errorf("openai down")
		native.err, native.models = nil, []modelEntry{{ID: "native-good"}}
		time.Sleep(discoveryCooldown)
		d.refresh(context.Background(), discoveryPicker)
		synctest.Wait()
		second := d.snapshot()
		if got := second.providers[providerToolhive]; len(got.observations) != 1 || got.observations[0].ID != "openai-good" || got.outcome.State != statusUnreachable {
			t.Fatalf("OpenAI last-good/outcome=%+v", got)
		}
		if got := second.providers[providerToolhiveAnthropic]; len(got.observations) != 1 || got.observations[0].ID != "native-good" || got.outcome.State != statusOK {
			t.Fatalf("native healthy catalog=%+v", got)
		}
		native.err = fmt.Errorf("native down again")
		time.Sleep(discoveryCooldown)
		d.refresh(context.Background(), discoveryPicker)
		third := d.snapshot()
		for _, pid := range []string{providerToolhive, providerToolhiveAnthropic} {
			if !reflect.DeepEqual(third.providers[pid].observations, second.providers[pid].observations) {
				t.Fatalf("dual failure erased %s", pid)
			}
		}
	})
}

func TestToolhiveNativeAnthropic_Scenario3_DefaultCompatibility(t *testing.T) {
	reg, err := buildProviderRegistry(Config{
		ToolhiveLLMBaseURL:  "http://127.0.0.1:14000/v1",
		liveModelHTTPClient: toolhiveModelsClient(t, toolhiveFixtureJSON),
	}, fakeEnv(nil))
	if err != nil {
		t.Fatalf("healthy build: %v", err)
	}
	if reg.Default() != providerToolhive || reg.ResolvedDefaultModel() != "claude-sonnet-4-6" {
		t.Fatalf("default = (%q,%q), want legacy toolhive first-listed model", reg.Default(), reg.ResolvedDefaultModel())
	}

	emptyOpenAI := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"object":"list","data":[]}`
		if strings.HasSuffix(req.URL.Path, "/anthropic/v1/models") {
			body = toolhiveAnthropicFixtureJSON
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	if _, err := buildProviderRegistry(Config{
		ToolhiveLLMBaseURL: "http://127.0.0.1:14000/v1", liveModelHTTPClient: emptyOpenAI,
	}, fakeEnv(nil)); err == nil {
		t.Fatal("honest empty catalog for default toolhive did not fail Build")
	}

	emptyNative := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := toolhiveFixtureJSON
		if strings.HasSuffix(req.URL.Path, "/anthropic/v1/models") {
			body = `{"data":[],"has_more":false}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	if models, listErr := (anthropicLister{inner: anthropic.NewLister("", "http://127.0.0.1:14000/anthropic", emptyNative)}).ListModels(context.Background()); listErr != nil || len(models) != 0 {
		t.Fatalf("empty native fixture did not decode honestly: models=%+v err=%v", models, listErr)
	}
	nativeReg, err := buildProviderRegistry(Config{
		ToolhiveLLMBaseURL: "http://127.0.0.1:14000/v1", liveModelHTTPClient: emptyNative,
		DefaultProvider: providerToolhiveAnthropic,
	}, fakeEnv(nil))
	if err == nil {
		t.Fatalf("honest empty native catalog for explicit native default did not fail Build: default=%q status=%+v",
			nativeReg.Default(), nativeReg.discovery.CurrentModelSnapshot().ProviderStatus)
	}
}

type concurrentProbeLister struct {
	id      string
	started chan<- string
	release <-chan struct{}
}

func (l concurrentProbeLister) ListModels(context.Context) ([]modelEntry, error) {
	l.started <- l.id
	<-l.release
	return []modelEntry{{ID: l.id}}, nil
}

type deadlineObservation struct {
	ok        bool
	remaining time.Duration
}

type deadlineAwareLister struct {
	modelID  string
	stall    bool
	observed chan<- deadlineObservation
}

func (l deadlineAwareLister) ListModels(ctx context.Context) ([]modelEntry, error) {
	deadline, ok := ctx.Deadline()
	remaining := time.Duration(0)
	if ok {
		remaining = time.Until(deadline)
	}
	l.observed <- deadlineObservation{ok: ok, remaining: remaining}
	if l.stall {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return []modelEntry{{ID: l.modelID}}, nil
}

func requireDeadlineNear(t *testing.T, observed <-chan deadlineObservation, want time.Duration) {
	t.Helper()
	select {
	case got := <-observed:
		if !got.ok || got.remaining < want-300*time.Millisecond || got.remaining > want+100*time.Millisecond {
			t.Fatalf("observed deadline = (ok=%v, remaining=%v), want near %v", got.ok, got.remaining, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("did not observe %v deadline", want)
	}
}

func TestToolhiveProbeDeadlinePublishesHealthySibling(t *testing.T) {
	observed := make(chan deadlineObservation, 2)
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerToolhive: {
				id: providerToolhive, available: true,
				lister: deadlineAwareLister{modelID: "openai-healthy", observed: observed},
			},
			providerToolhiveAnthropic: {
				id: providerToolhiveAnthropic, available: true,
				lister: deadlineAwareLister{stall: true, observed: observed},
			},
		},
		meta: newLiveMetaStore(),
	}

	bindDiscoveryFixture(t, reg)
	started := time.Now()
	if err := probeToolhive(reg, Config{}); err != nil {
		t.Fatalf("probeToolhive: %v", err)
	}
	if elapsed := time.Since(started); elapsed < toolhiveProbeTimeout-300*time.Millisecond || elapsed > toolhiveProbeTimeout+time.Second {
		t.Fatalf("probe elapsed = %v, want bounded near %v", elapsed, toolhiveProbeTimeout)
	}
	requireDeadlineNear(t, observed, toolhiveProbeTimeout)
	requireDeadlineNear(t, observed, toolhiveProbeTimeout)
	if model, ok := reg.meta.lookup(providerToolhive, "openai-healthy"); !ok || model.ID != "openai-healthy" {
		t.Fatalf("healthy probe metadata was not published: %+v, ok=%v", model, ok)
	}
	if _, ok := reg.meta.lookup(providerToolhiveAnthropic, "openai-healthy"); ok {
		t.Fatal("failed native probe published sibling metadata under the wrong provider")
	}
}

func TestToolhiveBackgroundRefreshCarriesOperationDeadline(t *testing.T) {
	observed := make(chan deadlineObservation, 2)
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerToolhive: {
				id: providerToolhive, available: true,
				lister: deadlineAwareLister{modelID: "openai", observed: observed},
			},
			providerToolhiveAnthropic: {
				id: providerToolhiveAnthropic, available: true,
				lister: deadlineAwareLister{modelID: "native", observed: observed},
			},
		},
		meta: newLiveMetaStore(),
	}

	bindDiscoveryFixture(t, reg).start(true, 0)
	requireDeadlineNear(t, observed, liveModelRefreshTimeout)
	requireDeadlineNear(t, observed, liveModelRefreshTimeout)
}

func TestToolhiveStaleRefreshDeadlinePublishesHealthySibling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		observed := make(chan deadlineObservation, 2)
		reg := &providerRegistry{
			entries: map[string]providerEntry{
				providerToolhive: {
					id: providerToolhive, available: true, intentDriven: true,
					lister: deadlineAwareLister{modelID: "openai-recovered", observed: observed},
				},
				providerToolhiveAnthropic: {
					id: providerToolhiveAnthropic, available: true, intentDriven: true,
					lister: deadlineAwareLister{stall: true, observed: observed},
				},
			},
			meta: newLiveMetaStore(),
		}

		bindDiscoveryFixture(t, reg)
		started := time.Now()
		reg.discovery.refresh(context.Background(), discoveryPicker)
		if elapsed := time.Since(started); elapsed != liveModelRefreshTimeout {
			t.Fatalf("refresh elapsed = %v, want %v", elapsed, liveModelRefreshTimeout)
		}
		requireDeadlineNear(t, observed, liveModelRefreshTimeout)
		requireDeadlineNear(t, observed, liveModelRefreshTimeout)
		if model, ok := reg.meta.lookup(providerToolhive, "openai-recovered"); !ok || model.ID != "openai-recovered" {
			t.Fatalf("healthy stale-refresh metadata was not published: %+v, ok=%v", model, ok)
		}
	})
}

func TestToolhiveProtocolRefreshesStartConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	defer closeIfOpen(release)
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerToolhive: {id: providerToolhive, available: true, lister: concurrentProbeLister{
				id: "openai", started: started, release: release,
			}},
			providerToolhiveAnthropic: {id: providerToolhiveAnthropic, available: true, lister: concurrentProbeLister{
				id: "anthropic", started: started, release: release,
			}},
		},
	}
	bindDiscoveryFixture(t, reg)
	done := make(chan struct{})
	go func() {
		reg.discovery.refresh(context.Background(), discoveryPicker)
		close(done)
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("both protocol fetches did not start before either was released")
		}
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("concurrent refresh did not finish")
	}
}

func TestToolhiveStaleRefreshesStartConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	defer closeIfOpen(release)
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerToolhive: {id: providerToolhive, available: true, intentDriven: true, lister: concurrentProbeLister{
				id: "openai", started: started, release: release,
			}},
			providerToolhiveAnthropic: {id: providerToolhiveAnthropic, available: true, intentDriven: true, lister: concurrentProbeLister{
				id: "anthropic", started: started, release: release,
			}},
		},
		meta: newLiveMetaStore(),
	}

	bindDiscoveryFixture(t, reg)
	done := make(chan struct{})
	go func() {
		reg.discovery.refresh(context.Background(), discoveryPicker)
		close(done)
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("both stale protocol fetches did not start before either was released")
		}
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("concurrent stale refresh did not finish")
	}
}

func TestDiscoveryConcurrencyIncludesCustomProviders(t *testing.T) {
	started := make(chan string, 2)
	releaseFirst := make(chan struct{})
	releaseSecond := make(chan struct{})
	defer closeIfOpen(releaseFirst)
	defer closeIfOpen(releaseSecond)
	reg := &providerRegistry{
		entries: map[string]providerEntry{
			"custom-a": {id: "custom-a", available: true, lister: concurrentProbeLister{
				id: "custom-a", started: started, release: releaseFirst,
			}},
			"custom-b": {id: "custom-b", available: true, lister: concurrentProbeLister{
				id: "custom-b", started: started, release: releaseSecond,
			}},
		},
	}
	bindDiscoveryFixture(t, reg)
	done := make(chan struct{})
	go func() {
		reg.discovery.refresh(context.Background(), discoveryPicker)
		close(done)
	}()

	seen := map[string]bool{}
	for range 2 {
		select {
		case got := <-started:
			seen[got] = true
		case <-time.After(time.Second):
			t.Fatal("providers did not start independently before release")
		}
	}
	if !seen["custom-a"] || !seen["custom-b"] {
		t.Fatalf("started=%v", seen)
	}
	close(releaseFirst)
	close(releaseSecond)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sequential provider refresh did not finish")
	}
}

func TestStatusHintForToolhiveUsesRoutingMode(t *testing.T) {
	proxy := providerEntry{id: providerToolhive, toolhiveMode: toolhiveModeProxy}
	direct := providerEntry{id: providerToolhiveAnthropic, toolhiveMode: toolhiveModeDirect}

	if got := statusHintFor(proxy, statusUnreachable); got != toolhiveStatusHints[statusUnreachable] {
		t.Fatalf("proxy hint = %q, want %q", got, toolhiveStatusHints[statusUnreachable])
	}
	if got := statusHintFor(direct, statusUnreachable); got != toolhiveDirectStatusHints[statusUnreachable] {
		t.Fatalf("direct hint = %q, want %q", got, toolhiveDirectStatusHints[statusUnreachable])
	}
}

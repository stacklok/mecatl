package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	openaioption "github.com/openai/openai-go/v3/option"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/jevrouter"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/toolhivellm"
	anthropicprovider "github.com/stacklok/mecatl/provider/anthropic"
	openairesponse "github.com/stacklok/mecatl/provider/openai"
	openaichatprovider "github.com/stacklok/mecatl/provider/openaichat"
)

func TestRootSessionProviderCorrelation_ConfiguredGuardrailDispatchPreservesRoot(t *testing.T) {
	verdict := `{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`
	checker := &recordingProvider{inner: mockllm.New(mockllm.TextTurn(verdict), mockllm.TextTurn(verdict))}
	cfg := Config{
		UseMock:         true,
		GuardrailsModel: "checker-model",
		GuardrailsRules: []GuardrailRule{{Match: "Grep", Phases: []string{"pre"}, Mode: "block"}},
	}
	reviewer := buildGuardrailsActionReviewer(cfg, nil, checker, providerMock, nil)
	if reviewer == nil {
		t.Fatal("configured guardrail factory returned nil reviewer")
	}
	catalog := tool.NewCatalog()
	catalog.MustRegister(stubTool{name: "Grep"})
	parent := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("guarded", "Grep", []byte(`{}`))),
		mockllm.TextTurn("done"),
	)
	deps := agent.Deps{
		LLM: parent, Catalog: catalog, Model: "parent-model",
		Policy: permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil),
	}
	attachGuardrailReviewer(&deps, reviewer, nil)
	engine := agent.NewEngine(deps)
	sess := session.New("s1", session.ModeDefault,
		session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"},
		session.Limits{}, time.Unix(1, 0))
	for range engine.Run(port.WithRootSessionID(context.Background(), "main"), sess, memEnvironment("/ws"), agent.RunRequest{Text: "go"}).Events() {
	}

	model, active, root := checker.lastObservation()
	if model != "checker-model" || !strings.HasPrefix(string(active), "guardrail-review-") || root != "main" {
		t.Fatalf("configured guardrail provider observation = model %q active %q root %q", model, active, root)
	}
}

func TestRootSessionProviderCorrelation_Scenario2_ProviderAttemptsCarryBothIDs(t *testing.T) {
	providers := []struct {
		name  string
		model string
		entry func(string) providerEntry
	}{
		{"openai-responses", "gpt-5", func(baseURL string) providerEntry {
			return newOpenAICompatEntry(Config{LLMMaxAttempts: 2}, providerOpenAI, "test", baseURL)
		}},
		{"openai-chat-completions", "test-model", func(baseURL string) providerEntry {
			return newOpenCodeEntry(Config{LLMMaxAttempts: 2}, providerOpenCode, "test", baseURL)
		}},
		{"anthropic", "claude-sonnet-4-5", func(baseURL string) providerEntry {
			return newAnthropicEntryFor(Config{LLMMaxAttempts: 2}, providerAnthropic, "test", baseURL, newLiveMetaStore(), false)
		}},
	}
	correlations := []struct{ name, active, root string }{
		{name: "main-and-compaction", active: "main-session", root: "main-session"},
		{name: "child", active: "child-session", root: "main-session"},
	}
	for _, provider := range providers {
		for _, correlation := range correlations {
			t.Run(provider.name+"/"+correlation.name, func(t *testing.T) {
				var mu sync.Mutex
				var got []http.Header
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					mu.Lock()
					got = append(got, req.Header.Clone())
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"type":"error","error":{"type":"server_error","message":"retry"}}`))
				}))
				defer srv.Close()
				ctx := port.WithRootSessionID(port.WithSessionID(context.Background(), session.SessionID(correlation.active)), session.SessionID(correlation.root))
				seq, _ := provider.entry(srv.URL).provider.Stream(ctx, port.LLMRequest{
					Model: provider.model, Messages: []session.Message{session.NewUserMessage("hello")},
				})
				if seq != nil {
					for range seq {
					}
				}
				mu.Lock()
				defer mu.Unlock()
				if len(got) < 2 {
					t.Fatalf("provider attempts = %d, want initial plus at least one retry", len(got))
				}
				for i, headers := range got {
					if headers.Get("X-Mecatl-Session-ID") != correlation.active || headers.Get(rootSessionIDHeader) != correlation.root {
						t.Fatalf("attempt %d correlation = active %q root %q", i+1, headers.Get("X-Mecatl-Session-ID"), headers.Get(rootSessionIDHeader))
					}
				}
			})
		}
	}
}

func TestRootSessionProviderCorrelation_Scenario2_ConcurrentProviderIsolation(t *testing.T) {
	providers := []struct {
		name  string
		model string
		entry func(string) providerEntry
	}{
		{"openai-responses", "gpt-5", func(baseURL string) providerEntry {
			return newOpenAICompatEntry(Config{LLMMaxAttempts: 2}, providerOpenAI, "test", baseURL,
				openairesponse.WithMaxRetries(0))
		}},
		{"openai-chat-completions", "test-model", func(baseURL string) providerEntry {
			return newOpenCodeEntry(Config{LLMMaxAttempts: 2}, providerOpenCode, "test", baseURL,
				openaichatprovider.WithRequestOption(openaioption.WithMaxRetries(0)))
		}},
		{"anthropic", "claude-sonnet-4-5", func(baseURL string) providerEntry {
			return newAnthropicEntryFor(Config{LLMMaxAttempts: 2}, providerAnthropic, "test", baseURL, newLiveMetaStore(), false,
				anthropicprovider.WithRequestOption(anthropicoption.WithMaxRetries(0)))
		}},
	}

	for _, tc := range providers {
		t.Run(tc.name, func(t *testing.T) {
			const concurrent = 2
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			release := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
			stageArrivals := [2]int{}
			payloadAttempts := make(map[string]int, concurrent)
			type observedRequest struct{ payload, active, root string }
			var (
				mu       sync.Mutex
				observed []observedRequest
			)
			handlerErr := make(chan error, concurrent*2)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					handlerErr <- fmt.Errorf("read request: %w", err)
					return
				}
				payload := ""
				for i := 0; i < concurrent; i++ {
					candidate := fmt.Sprintf("payload-%d", i)
					if strings.Contains(string(body), candidate) {
						payload = candidate
						break
					}
				}
				if payload == "" {
					handlerErr <- fmt.Errorf("request has no unique payload attribution: %s", body)
					w.WriteHeader(http.StatusBadRequest)
					return
				}

				mu.Lock()
				stage := payloadAttempts[payload]
				payloadAttempts[payload]++
				if stage < len(release) {
					stageArrivals[stage]++
					if stageArrivals[stage] == concurrent {
						close(release[stage])
					}
				}
				observed = append(observed, observedRequest{payload, req.Header.Get("X-Mecatl-Session-ID"), req.Header.Get(rootSessionIDHeader)})
				mu.Unlock()
				if stage >= len(release) {
					handlerErr <- fmt.Errorf("payload %q made unexpected attempt %d", payload, stage+1)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				select {
				case <-release[stage]:
				case <-req.Context().Done():
					handlerErr <- fmt.Errorf("attempt %d for %q canceled at barrier: %w", stage+1, payload, req.Context().Err())
					return
				case <-ctx.Done():
					handlerErr <- fmt.Errorf("attempt %d barrier for %q: %w", stage+1, payload, ctx.Err())
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"message":"retry","type":"server_error"}}`))
			}))
			defer srv.Close()

			provider := tc.entry(srv.URL).provider
			attemptResult := make(chan error, concurrent)
			var wg sync.WaitGroup
			for i := 0; i < concurrent; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					active, root := fmt.Sprintf("active-%d", i), fmt.Sprintf("root-%d", i)
					requestCtx := port.WithRootSessionID(port.WithSessionID(ctx, session.SessionID(active)), session.SessionID(root))
					seq, err := provider.Stream(requestCtx, port.LLMRequest{
						Model: tc.model, Messages: []session.Message{session.NewUserMessage(fmt.Sprintf("payload-%d", i))},
					})
					if err == nil && seq != nil {
						for _, streamErr := range seq {
							if streamErr != nil {
								err = streamErr
							}
						}
					}
					attemptResult <- err
				}(i)
			}
			wg.Wait()
			close(attemptResult)
			for err := range attemptResult {
				if err == nil {
					t.Error("two failed outer attempts returned nil error")
				} else if ctx.Err() != nil {
					t.Errorf("provider did not finish before bound: %v", err)
				}
			}
			close(handlerErr)
			for err := range handlerErr {
				t.Error(err)
			}

			mu.Lock()
			defer mu.Unlock()
			if len(observed) != concurrent*2 {
				t.Fatalf("requests = %d, want %d (exactly two outer attempts per payload): %+v", len(observed), concurrent*2, observed)
			}
			for i := 0; i < concurrent; i++ {
				payload := fmt.Sprintf("payload-%d", i)
				if payloadAttempts[payload] != 2 {
					t.Errorf("%s attempts = %d, want 2", payload, payloadAttempts[payload])
				}
				for _, got := range observed {
					if got.payload == payload && (got.active != fmt.Sprintf("active-%d", i) || got.root != fmt.Sprintf("root-%d", i)) {
						t.Errorf("%s correlation = active %q root %q", payload, got.active, got.root)
					}
				}
			}
		})
	}
}

func TestRootSessionProviderCorrelation_OpenAIEncryptedContentFallback(t *testing.T) {
	type capturedRequest struct {
		header http.Header
		body   []byte
	}
	var captured []capturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		captured = append(captured, capturedRequest{header: req.Header.Clone(), body: body})
		if len(captured) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified or decrypted"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_text.delta\n"+
			`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"recovered"}`+"\n\n"+
			"event: response.completed\n"+
			`data: {"type":"response.completed","sequence_number":1,"response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`+"\n\n")
	}))
	defer srv.Close()

	entry := newOpenAICompatEntry(Config{LLMMaxAttempts: 1}, providerOpenAI, "test", srv.URL,
		openairesponse.WithMaxRetries(0))
	assistant := session.NewAssistantMessage("visible history", `{"v":1,"items":[{"i":"rs_bad","e":"opaque-blob"}]}`, nil)
	ctx := port.WithRootSessionID(port.WithSessionID(context.Background(), "active-session"), "root-session")
	seq, err := entry.provider.Stream(ctx, port.LLMRequest{Model: "gpt-5", Messages: []session.Message{
		session.NewUserMessage("recover"), assistant,
	}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for _, streamErr := range seq {
		if streamErr != nil {
			t.Fatalf("stream: %v", streamErr)
		}
	}
	if len(captured) != 2 {
		t.Fatalf("inference requests = %d, want initial plus cleaned replay", len(captured))
	}
	for i, request := range captured {
		if request.header.Get("X-Mecatl-Session-ID") != "active-session" || request.header.Get(rootSessionIDHeader) != "root-session" {
			t.Errorf("inference request %d correlation = active %q root %q", i+1,
				request.header.Get("X-Mecatl-Session-ID"), request.header.Get(rootSessionIDHeader))
		}
	}
	for i, wantReasoning := range []int{1, 0} {
		var wire struct {
			Input []map[string]any `json:"input"`
		}
		if err := json.Unmarshal(captured[i].body, &wire); err != nil {
			t.Fatalf("decode inference request %d: %v", i+1, err)
		}
		gotReasoning := 0
		for _, item := range wire.Input {
			if item["type"] == "reasoning" {
				gotReasoning++
			}
		}
		if gotReasoning != wantReasoning {
			t.Errorf("inference request %d reasoning items = %d, want %d", i+1, gotReasoning, wantReasoning)
		}
	}
}

func TestRootSessionProviderCorrelation_Scenario2_OpenCodeRefusesRedirects(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Add(1)
	}))
	defer target.Close()
	var active, root string
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		active = req.Header.Get("X-Mecatl-Session-ID")
		root = req.Header.Get(rootSessionIDHeader)
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	provider := newOpenCodeEntry(Config{LLMMaxAttempts: 1}, providerOpenCode, "test", source.URL).provider
	ctx := port.WithRootSessionID(port.WithSessionID(context.Background(), "active"), "root")
	seq, _ := provider.Stream(ctx, port.LLMRequest{Model: "test", Messages: []session.Message{session.NewUserMessage("hello")}})
	if seq != nil {
		for range seq {
		}
	}
	if redirected.Load() != 0 {
		t.Fatalf("redirect target requests = %d, want 0", redirected.Load())
	}
	if active != "active" || root != "root" {
		t.Fatalf("initial correlation headers = active %q root %q", active, root)
	}
}

func TestRootSessionProviderCorrelation_Scenario2_InvalidRootFailsOpen(t *testing.T) {
	valid256 := strings.Repeat("x", 256)
	for _, tc := range []struct {
		name string
		root session.SessionID
		want string
	}{
		{"absent", "", ""}, {"one-byte", "x", "x"}, {"leading-space", " root", ""}, {"trailing-space", "root ", ""},
		{"control", "root\nvalue", ""}, {"delete", "root\x7f", ""}, {"non-ascii", "røøt", ""}, {"too-long", session.SessionID(strings.Repeat("x", 257)), ""},
		{"interior-space", "root session", "root session"}, {"maximum", session.SessionID(valid256), valid256},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var root, active string
			base := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				root, active = req.Header.Get(rootSessionIDHeader), req.Header.Get("X-Mecatl-Session-ID")
				return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: req}, nil
			})}
			client := withRootSessionCorrelation(base)
			ctx := context.Background()
			if tc.root != "" {
				ctx = port.WithRootSessionID(ctx, tc.root)
			}
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test", nil)
			req.Header.Set(rootSessionIDHeader, "spoofed")
			req.Header.Set("X-Mecatl-Session-ID", "active")
			resp, err := client.Do(req)
			if err != nil || resp.StatusCode != http.StatusNoContent {
				t.Fatalf("request failed open: response=%v err=%v", resp, err)
			}
			if root != tc.want || active != "active" {
				t.Fatalf("headers = root %q active %q, want root %q active unchanged", root, active, tc.want)
			}
			if base.Transport == client.Transport || req.Header.Get(rootSessionIDHeader) != "spoofed" {
				t.Fatal("decorator mutated shared client or request")
			}
		})
	}
}

func TestRootSessionProviderCorrelation_Scenario3_LLMRouterCarriesRoot(t *testing.T) {
	var active, root string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		active, root = req.Header.Get("X-Mecatl-Session-ID"), req.Header.Get(rootSessionIDHeader)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: response.created
data: {"type":"response.created","sequence_number":0,"response":{"id":"response-router","status":"in_progress","output":[]}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":1,"item_id":"message-router","output_index":0,"content_index":0,"delta":"{\"category\":\"small\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":{"id":"message-router","type":"message","status":"completed","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"{\"category\":\"small\"}","annotations":[]}]}}

event: response.completed
data: {"type":"response.completed","sequence_number":3,"response":{"id":"response-router","status":"completed","usage":{"input_tokens":1,"input_tokens_details":{},"output_tokens":1,"output_tokens_details":{},"total_tokens":2}}}

`))
	}))
	defer srv.Close()
	entry := newOpenAICompatEntry(Config{LLMMaxAttempts: 1}, providerOpenAI, "test", srv.URL)
	classifier := agent.NewEngine(agent.Deps{
		LLM: entry.provider, Catalog: tool.NewCatalog(), Model: "gpt-5", MaxNoProgressNudges: -1,
	})
	category, _, _, ok := agent.RunModelRouter(port.WithRootSessionID(context.Background(), "invoking-conversation"), classifier, agent.ModelRouteRequest{
		TaskPrompt: "route", Categories: []agent.ModelRouteCategory{{Name: "small", Description: "small task"}}, Default: "small",
	})
	if !ok || category != "small" {
		t.Fatalf("model router result = category %q ok %v", category, ok)
	}
	if !strings.HasPrefix(active, "model-router-") || active == "invoking-conversation" || root != "invoking-conversation" {
		t.Fatalf("router correlation = active %q root %q", active, root)
	}
}

func TestRootSessionProviderCorrelation_Scenario3_ConfiguredRouterPreservesRoot(t *testing.T) {
	ctx := context.Background()
	type observation struct {
		model        string
		active, root session.SessionID
	}
	var (
		mu  sync.Mutex
		obs []observation
	)
	provider := correlationObservingProvider{inner: mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("c1", "Subagent", []byte(`{"prompt":"review the diff","agent":"reviewer"}`))),
		mockllm.TextTurn(`{"category":"large"}`),
		mockllm.TextTurn("CHILD SUMMARY"),
		mockllm.TextTurn("parent done"),
	), observe: func(ctx context.Context, r port.LLMRequest) {
		active, _ := port.SessionIDFromContext(ctx)
		root, _ := port.RootSessionIDFromContext(ctx)
		mu.Lock()
		obs = append(obs, observation{r.Model, active, root})
		mu.Unlock()
	}}
	cfg := Config{
		Model:                 "gpt-5",
		RouterCategories:      routerTaxonomyCategories(),
		RouterDefaultCategory: "small",
	}
	// An unpinned def is routable, so delegating to it consults the configured
	// semantic router through the production per-session engine wiring.
	assets := catalogAssets{agentReg: agents.NewRegistry([]agents.AgentDef{
		{Name: "reviewer", Description: "reviewer specialist", Body: "REVIEWER-DEF-BODY"},
	})}
	factory := sessionEngineFactory(cfg, regForTest(provider, providerOpenAI, cfg.Model), provider, memstore.New(),
		permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil), hookexec.New(nil), nil, prompt.RootAssembler{}, assets, nil)
	built, err := factory(ctx, server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = built.Close() }()

	// The parent's active ID differs from its inherited root, so a caller that reset
	// the root to its immediate parent would be detectable at the classifier request.
	sess := session.New("parent-active", session.ModeDefault,
		session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"},
		session.Limits{}, time.Unix(1, 0))
	for range built.Engine.Run(port.WithRootSessionID(ctx, "inherited-root"), sess, memEnvironment("/ws"), agent.RunRequest{Text: "go"}).Events() {
	}

	mu.Lock()
	defer mu.Unlock()
	if len(obs) != 4 {
		t.Fatalf("recorded %d requests (%+v), want 4 (parent→classifier→child→parent)", len(obs), obs)
	}
	if obs[0].active != "parent-active" || obs[0].root != "inherited-root" {
		t.Fatalf("parent correlation = active %q root %q", obs[0].active, obs[0].root)
	}
	if classifier := obs[1]; !strings.HasPrefix(string(classifier.active), "model-router-") || classifier.root != "inherited-root" {
		t.Fatalf("configured router classifier correlation = active %q root %q, want model-router-* and inherited-root", classifier.active, classifier.root)
	}
	if obs[2].model != routerLarge || obs[2].root != "inherited-root" {
		t.Fatalf("routed child = model %q root %q, want %q and inherited-root", obs[2].model, obs[2].root, routerLarge)
	}
}

// correlationObservingProvider reports each request's context-carried correlation
// before delegating, so composition tests can observe identities at the provider port.
type correlationObservingProvider struct {
	inner   port.LLMProvider
	observe func(context.Context, port.LLMRequest)
}

func (p correlationObservingProvider) Capabilities() port.ProviderCapabilities {
	return p.inner.Capabilities()
}

func (p correlationObservingProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.observe(ctx, req)
	return p.inner.Stream(ctx, req)
}

func TestRootSessionProviderCorrelation_Scenario3_JevCarriesRoot(t *testing.T) {
	var pairs [][2]string
	srv := newJevCorrelationServer(t, func(req *http.Request) {
		pairs = append(pairs, [2]string{req.Header.Get(rootSessionIDHeader), req.Header.Get("X-Mecatl-Session-ID")})
	})
	cfg := Config{RouterBackend: routerBackendJev, TypesafeAPIKey: "secret", RouterJevBaseURL: srv.URL,
		RouterCategories: []permconfig.RouterCategory{{Name: "small", Description: "small", Model: "model"}}}
	if err := prepareJevRouter(&cfg); err != nil {
		t.Fatal(err)
	}
	for _, root := range []session.SessionID{"conversation-root", "other-root"} {
		ctx := port.WithRootSessionID(port.WithSessionID(context.Background(), "active-must-not-project"), root)
		result := cfg.jevRouter.Route(ctx, "task", []jevrouter.Category{{Name: "small", Description: "small"}})
		if !result.OK {
			t.Fatalf("root %q result=%+v", root, result)
		}
	}
	want := [][2]string{{"conversation-root", ""}, {"other-root", ""}}
	if len(pairs) != len(want) || pairs[0] != want[0] || pairs[1] != want[1] {
		t.Fatalf("shared Jev client correlation pairs = %v, want %v", pairs, want)
	}
}

func TestRootSessionProviderCorrelation_Scenario3_JevRefusesRedirects(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Add(1)
	}))
	defer target.Close()
	var root string
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		root = req.Header.Get(rootSessionIDHeader)
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	cfg := Config{RouterBackend: routerBackendJev, TypesafeAPIKey: "secret", RouterJevBaseURL: source.URL,
		RouterCategories: []permconfig.RouterCategory{{Name: "small", Description: "small", Model: "model"}}}
	if err := prepareJevRouter(&cfg); err != nil {
		t.Fatal(err)
	}
	result := cfg.jevRouter.Route(port.WithRootSessionID(context.Background(), "conversation-root"), "task",
		[]jevrouter.Category{{Name: "small", Description: "small"}})
	if result.OK || redirected.Load() != 0 || root != "conversation-root" {
		t.Fatalf("redirect result=%+v target requests=%d root=%q", result, redirected.Load(), root)
	}
}

func TestRootSessionProviderCorrelation_Scenario3_JevInvalidRootFailsOpen(t *testing.T) {
	var root string
	srv := newJevCorrelationServer(t, func(req *http.Request) { root = req.Header.Get(rootSessionIDHeader) })
	cfg := Config{RouterBackend: routerBackendJev, TypesafeAPIKey: "secret", RouterJevBaseURL: srv.URL,
		RouterCategories: []permconfig.RouterCategory{{Name: "small", Description: "small", Model: "model"}}}
	if err := prepareJevRouter(&cfg); err != nil {
		t.Fatal(err)
	}
	result := cfg.jevRouter.Route(port.WithRootSessionID(context.Background(), " invalid"), "task",
		[]jevrouter.Category{{Name: "small", Description: "small"}})
	if !result.OK || root != "" {
		t.Fatalf("result=%+v root header=%q", result, root)
	}
}

func newJevCorrelationServer(t *testing.T, observe func(*http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		observe(req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"delegated-model-category":{"type":"choice","choice":"small","probabilities":{"small":1},"confidence":1}},"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestRootSessionProviderCorrelation_Scenario2_EveryEntryCarriesBothIDs builds each
// registry entry that supplies its own inference HTTP client (and so must wrap it
// with withRootSessionCorrelation itself) and proves both correlation headers reach
// the wire. A new entry with its own client belongs in this table.
func TestRootSessionProviderCorrelation_Scenario2_EveryEntryCarriesBothIDs(t *testing.T) {
	const active, root = "child-session", "main-session"
	tokenFile := filepath.Join(t.TempDir(), "openai-token")
	if err := os.WriteFile(tokenFile, []byte("bearer-file-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{LLMMaxAttempts: 1}
	custom := func(flavor, baseURL string) permconfig.ProviderDefinition {
		return permconfig.ProviderDefinition{ID: "custom", BaseURL: baseURL, DefaultModel: "test-model", APIFlavor: flavor}
	}
	// toolhiveEntries builds both ToolHive protocol entries through the production
	// family constructor, so each mode's own client wiring is what gets exercised.
	toolhiveEntries := func(mode toolhiveRoutingMode, baseURL string) (providerEntry, providerEntry) {
		thCfg := Config{LLMMaxAttempts: 1, toolhiveTokenSourceFactory: func(string, port.Diagnostics) (toolhivellm.TokenSourceFunc, error) {
			return func(context.Context) (string, error) { return "toolhive-token", nil }, nil
		}}
		return newToolhiveEntries(thCfg, toolhiveIntent{mode: mode, baseURL: baseURL}, "", newLiveMetaStore())
	}
	entries := []struct {
		name  string
		model string
		// entry builds the entry against baseURL; capture is the transport for
		// entries whose endpoint is fixed and can only be observed below the SDK.
		entry func(t *testing.T, baseURL string, capture http.RoundTripper) providerEntry
	}{
		{"openai-bearer-token-file", "gpt-5", func(t *testing.T, baseURL string, _ http.RoundTripper) providerEntry {
			reg, err := buildProviderRegistry(Config{
				OpenAIBearerTokenFile: tokenFile, LLMMaxAttempts: 1,
				ProviderOverrides: permconfig.ProviderOverrides{providerOpenAI: {BaseURL: baseURL}},
			}, fakeEnv(nil))
			if err != nil {
				t.Fatal(err)
			}
			entry, ok := reg.Lookup(providerOpenAI)
			if !ok {
				t.Fatal("bearer-token-file OpenAI entry is unavailable")
			}
			return entry
		}},
		{"openai-codex", "gpt-5", func(t *testing.T, _ string, capture http.RoundTripper) providerEntry {
			entry, err := newOpenAICodexEntry(Config{
				LLMMaxAttempts: 1, OpenAICodexCredential: codexRegistryCredential(t),
				openAICodexNow: func() time.Time { return codexRegistryNow }, openAICodexTransport: capture,
			})
			if err != nil {
				t.Fatal(err)
			}
			return entry
		}},
		{"native-oidc", "native-model", func(t *testing.T, _ string, capture http.RoundTripper) providerEntry {
			entry, err := newNativeProviderEntry(Config{LLMMaxAttempts: 1, nativeEndpointTransport: capture},
				nativeDefinition("native"), &nativeBearerFixture{token: "native-token"})
			if err != nil {
				t.Fatal(err)
			}
			return entry
		}},
		{"custom-openai-responses", "test-model", func(_ *testing.T, baseURL string, _ http.RoundTripper) providerEntry {
			return newCustomProviderEntry(cfg, custom("openai-responses", baseURL), "key", newLiveMetaStore())
		}},
		{"custom-openai-chat-completions", "test-model", func(_ *testing.T, baseURL string, _ http.RoundTripper) providerEntry {
			return newCustomProviderEntry(cfg, custom("openai-chat-completions", baseURL), "key", newLiveMetaStore())
		}},
		{"custom-anthropic-messages", "claude-sonnet-4-5", func(_ *testing.T, baseURL string, _ http.RoundTripper) providerEntry {
			return newCustomProviderEntry(cfg, custom("anthropic-messages", baseURL), "key", newLiveMetaStore())
		}},
		{"toolhive-proxy-openai", "gpt-5", func(_ *testing.T, baseURL string, _ http.RoundTripper) providerEntry {
			openAI, _ := toolhiveEntries(toolhiveModeProxy, baseURL)
			return openAI
		}},
		{"toolhive-proxy-anthropic", "claude-sonnet-4-5", func(_ *testing.T, baseURL string, _ http.RoundTripper) providerEntry {
			_, anthropicEntry := toolhiveEntries(toolhiveModeProxy, baseURL)
			return anthropicEntry
		}},
		{"toolhive-direct-openai", "gpt-5", func(_ *testing.T, baseURL string, _ http.RoundTripper) providerEntry {
			openAI, _ := toolhiveEntries(toolhiveModeDirect, baseURL)
			return openAI
		}},
		{"toolhive-direct-anthropic", "claude-sonnet-4-5", func(_ *testing.T, baseURL string, _ http.RoundTripper) providerEntry {
			_, anthropicEntry := toolhiveEntries(toolhiveModeDirect, baseURL)
			return anthropicEntry
		}},
	}
	for _, tc := range entries {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var got []http.Header
			record := func(h http.Header) {
				mu.Lock()
				defer mu.Unlock()
				got = append(got, h.Clone())
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				record(req.Header)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"stop"}}`))
			}))
			defer srv.Close()
			capture := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				record(req.Header)
				return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}},
					Body: io.NopCloser(strings.NewReader(`{"error":{"message":"stop"}}`)), Request: req}, nil
			})

			ctx := port.WithRootSessionID(port.WithSessionID(context.Background(), active), root)
			seq, _ := tc.entry(t, srv.URL+"/v1", capture).provider.Stream(ctx, port.LLMRequest{
				Model: tc.model, Messages: []session.Message{session.NewUserMessage("hello")},
			})
			if seq != nil {
				for range seq {
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(got) == 0 {
				t.Fatal("no inference request reached the endpoint")
			}
			wantActive, wantRoot := active, root
			for i, headers := range got {
				if headers.Get("X-Mecatl-Session-ID") != wantActive || headers.Get(rootSessionIDHeader) != wantRoot {
					t.Fatalf("request %d correlation = active %q root %q, want active %q root %q",
						i+1, headers.Get("X-Mecatl-Session-ID"), headers.Get(rootSessionIDHeader), wantActive, wantRoot)
				}
			}
		})
	}
}

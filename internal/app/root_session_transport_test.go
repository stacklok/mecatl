package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/jevrouter"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/toolhivellm"
)

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
	const count = 16
	seen := make(chan string, count*8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen <- req.Header.Get("X-Mecatl-Session-ID") + "/" + req.Header.Get(rootSessionIDHeader)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"retry","type":"server_error"}}`))
	}))
	defer srv.Close()
	provider := newOpenAICompatEntry(Config{LLMMaxAttempts: 2}, providerOpenAI, "test", srv.URL).provider
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			active, root := fmt.Sprintf("active-%d", i), fmt.Sprintf("root-%d", i)
			ctx := port.WithRootSessionID(port.WithSessionID(context.Background(), session.SessionID(active)), session.SessionID(root))
			seq, _ := provider.Stream(ctx, port.LLMRequest{Model: "gpt-5", Messages: []session.Message{session.NewUserMessage("hello")}})
			if seq != nil {
				for range seq {
				}
			}
		}(i)
	}
	wg.Wait()
	close(seen)
	got := make(map[string]int, count)
	for pair := range seen {
		got[pair]++
	}
	attempts := 0
	for i := 0; i < count; i++ {
		want := fmt.Sprintf("active-%d/root-%d", i, i)
		if got[want] < 2 {
			t.Errorf("isolated pair %q attempts = %d, want at least 2; got %v", want, got[want], got)
		}
		if attempts == 0 {
			attempts = got[want]
		} else if got[want] != attempts {
			t.Errorf("isolated pair %q attempts = %d, want %d", want, got[want], attempts)
		}
	}
	if len(got) != count {
		t.Errorf("unexpected cross-stamped pairs: %v", got)
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
		// ownedHeaders marks an endpoint whose request policy rebuilds the complete
		// header set from constants, so neither correlation field reaches the wire.
		ownedHeaders bool
		// entry builds the entry against baseURL; capture is the transport for
		// entries whose endpoint is fixed and can only be observed below the SDK.
		entry func(t *testing.T, baseURL string, capture http.RoundTripper) providerEntry
	}{
		{"openai-bearer-token-file", "gpt-5", false, func(t *testing.T, baseURL string, _ http.RoundTripper) providerEntry {
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
		{"openai-codex", "gpt-5", true, func(t *testing.T, _ string, capture http.RoundTripper) providerEntry {
			entry, err := newOpenAICodexEntry(Config{
				LLMMaxAttempts: 1, OpenAICodexCredential: codexRegistryCredential(t),
				openAICodexNow: func() time.Time { return codexRegistryNow }, openAICodexTransport: capture,
			})
			if err != nil {
				t.Fatal(err)
			}
			return entry
		}},
		{"native-oidc", "native-model", false, func(t *testing.T, _ string, capture http.RoundTripper) providerEntry {
			entry, err := newNativeProviderEntry(Config{LLMMaxAttempts: 1, nativeEndpointTransport: capture},
				nativeDefinition("native"), &nativeBearerFixture{token: "native-token"})
			if err != nil {
				t.Fatal(err)
			}
			return entry
		}},
		{"custom-openai-responses", "test-model", false, func(_ *testing.T, baseURL string, _ http.RoundTripper) providerEntry {
			return newCustomProviderEntry(cfg, custom("openai-responses", baseURL), "key", newLiveMetaStore())
		}},
		{"custom-openai-chat-completions", "test-model", false, func(_ *testing.T, baseURL string, _ http.RoundTripper) providerEntry {
			return newCustomProviderEntry(cfg, custom("openai-chat-completions", baseURL), "key", newLiveMetaStore())
		}},
		{"custom-anthropic-messages", "claude-sonnet-4-5", false, func(_ *testing.T, baseURL string, _ http.RoundTripper) providerEntry {
			return newCustomProviderEntry(cfg, custom("anthropic-messages", baseURL), "key", newLiveMetaStore())
		}},
		{"toolhive-proxy-openai", "gpt-5", false, func(_ *testing.T, baseURL string, _ http.RoundTripper) providerEntry {
			openAI, _ := toolhiveEntries(toolhiveModeProxy, baseURL)
			return openAI
		}},
		{"toolhive-proxy-anthropic", "claude-sonnet-4-5", false, func(_ *testing.T, baseURL string, _ http.RoundTripper) providerEntry {
			_, anthropicEntry := toolhiveEntries(toolhiveModeProxy, baseURL)
			return anthropicEntry
		}},
		{"toolhive-direct-openai", "gpt-5", false, func(_ *testing.T, baseURL string, _ http.RoundTripper) providerEntry {
			openAI, _ := toolhiveEntries(toolhiveModeDirect, baseURL)
			return openAI
		}},
		{"toolhive-direct-anthropic", "claude-sonnet-4-5", false, func(_ *testing.T, baseURL string, _ http.RoundTripper) providerEntry {
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
			if tc.ownedHeaders {
				wantActive, wantRoot = "", ""
			}
			for i, headers := range got {
				if headers.Get("X-Mecatl-Session-ID") != wantActive || headers.Get(rootSessionIDHeader) != wantRoot {
					t.Fatalf("request %d correlation = active %q root %q, want active %q root %q",
						i+1, headers.Get("X-Mecatl-Session-ID"), headers.Get(rootSessionIDHeader), wantActive, wantRoot)
				}
			}
		})
	}
}

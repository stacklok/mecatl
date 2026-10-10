package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	refsearch "github.com/stacklok/mecatl/engine/adapter/search"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestOperatorWebSearchRequestShape(t *testing.T) {
	for _, tc := range []struct {
		name, header, param, wantHeader, wantParam string
		headerSet, paramSet                        bool
	}{
		{"settings defaults", "", "", "X-Settings", "lookup", false, false},
		{"explicit flags override individually", "X-Flag", "search", "X-Flag", "search", true, true},
		{"header only", "X-Flag", "", "X-Flag", "lookup", true, false},
		{"empty explicit flags restore adapter defaults", "", "", "Authorization", "q", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.wantHeader == "Authorization" {
					if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
						t.Errorf("Authorization = %q", got)
					}
				} else if got := r.Header.Get(tc.wantHeader); got != "test-key" {
					t.Errorf("credential header %q = %q", tc.wantHeader, got)
				}
				if got := r.URL.Query().Get(tc.wantParam); got != "test query" {
					t.Errorf("query parameter %q = %q", tc.wantParam, got)
				}
				if strings.Contains(r.URL.RawQuery, "test-key") {
					t.Error("credential leaked into query")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"results":[]}`))
			}))
			defer server.Close()
			cfg := Config{PermissionConfigs: []string{writeOperatorSettingsFile(t, fmt.Sprintf("websearch:\n  url: %s\n  auth_header: X-Settings\n  query_param: lookup\n", server.URL))},
				permConfigEnv: isolatedPermConfigEnv(t), WebSearchAuthHeader: tc.header, WebSearchQueryParam: tc.param,
				WebSearchAuthHeaderFlagSet: tc.headerSet, WebSearchQueryParamFlagSet: tc.paramSet, WebSearchAPIKey: "test-key"}
			cfg.permResolver = buildPermResolver(cfg)
			cfg = foldOperatorWebSearch(cfg)
			if _, err := buildSearchProvider(t.Context(), cfg).Search(t.Context(), tool.SearchQuery{Query: "test query"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBuildWebSearchCredentialLoaderSendsGenericAuthHeader(t *testing.T) {
	const apiKey = "test-websearch-key"
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-Search-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	loader := &capturingProviderCredentialLoader{profile: ProviderCredentials{WebSearchAPIKey: apiKey}}
	built, err := buildIsolated(t, t.Context(), Config{
		Workspace: t.TempDir(), Model: "mock", NoSoul: true,
		MockProvider: mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("search", "WebSearch", json.RawMessage(`{"query":"test query"}`))),
			mockllm.TextTurn("done"),
		),
		PermissionConfigs:        []string{writeOperatorSettingsFile(t, fmt.Sprintf("websearch:\n  url: %s\n  auth_header: X-Search-Key\n", server.URL))},
		permConfigEnv:            isolatedPermConfigEnv(t),
		ProviderCredentialLoader: loader,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()

	harnessRun(t, built, t.Context(), harnessCreate(t, built, t.Context()), "search")
	if loader.calls != 1 {
		t.Fatalf("provider credential loader calls = %d, want 1", loader.calls)
	}
	if gotAuth != apiKey {
		t.Fatalf("configured auth header = %q, want resolved credential", gotAuth)
	}
}

func TestBuildWebSearchCredentialLoaderDoesNotSendSearXNGAuthHeader(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	built, err := buildIsolated(t, t.Context(), Config{
		Workspace: t.TempDir(), Model: "mock", NoSoul: true,
		MockProvider: mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("search", "WebSearch", json.RawMessage(`{"query":"test query"}`))),
			mockllm.TextTurn("done"),
		),
		PermissionConfigs:        []string{writeOperatorSettingsFile(t, fmt.Sprintf("websearch:\n  searxng:\n    url: %s\n", server.URL))},
		permConfigEnv:            isolatedPermConfigEnv(t),
		ProviderCredentialLoader: &capturingProviderCredentialLoader{profile: ProviderCredentials{WebSearchAPIKey: "test-websearch-key"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()

	harnessRun(t, built, t.Context(), harnessCreate(t, built, t.Context()), "search")
	if gotAuth != "" {
		t.Fatalf("SearXNG Authorization header = %q, want empty", gotAuth)
	}
}

func TestBuildProjectWebSearchCannotSelectBackend(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".mecatl")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.yaml"), []byte("websearch:\n  enabled: false\n  unexpected: ignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &searchDiag{}
	built, err := buildIsolated(t, t.Context(), Config{Workspace: workspace, UseMock: true, NoSoul: true,
		PermissionsConventional: true, TrustProject: true, permConfigEnv: isolatedPermConfigEnv(t),
		BraveAPIKey: "test-brave-secret", Diagnostics: d})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	if logs := d.all(); !strings.Contains(logs, "WebSearch ENABLED with Brave backend") || strings.Contains(logs, "test-brave-secret") {
		t.Fatalf("project settings changed search backend or logged secret: %s", logs)
	}
}

func TestBuildOperatorWebSearchEndpointDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"generic valid", "websearch:\n  url: https://search.example/search\n", "operator HTTP backend (settings.yaml) endpoint https://search.example/search"},
		{"SearXNG valid", "websearch:\n  searxng:\n    url: https://searx.example/search\n", "SearXNG backend (settings.yaml) endpoint https://searx.example/search"},
		{"generic query string", "websearch:\n  url: https://search.example/search?api_key=secret-value\n", "operator HTTP backend misconfigured"},
		{"SearXNG query string", "websearch:\n  searxng:\n    url: https://searx.example/search?api_key=secret-value\n", "operator SearXNG backend misconfigured"},
		{"generic userinfo", "websearch:\n  url: https://user:secret-value@search.example/search\n", "operator HTTP backend misconfigured"},
		{"SearXNG userinfo", "websearch:\n  searxng:\n    url: https://user:secret-value@searx.example/search\n", "operator SearXNG backend misconfigured"},
		{"malformed", "websearch:\n  url: '://secret-value'\n", "operator HTTP backend misconfigured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &searchDiag{}
			built, err := buildIsolated(t, t.Context(), Config{Workspace: t.TempDir(), UseMock: true, NoSoul: true,
				PermissionConfigs: []string{writeOperatorSettingsFile(t, tc.body)}, permConfigEnv: isolatedPermConfigEnv(t), Diagnostics: d})
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			if logs := d.all(); !strings.Contains(logs, tc.want) || strings.Contains(logs, "secret-value") {
				t.Fatalf("endpoint diagnostics mismatch or rejected URL disclosed: %s", logs)
			}
		})
	}
}

func TestRejectedWebSearchEndpointDoesNotFallThroughOrLeak(t *testing.T) {
	for _, tc := range []struct {
		name, settings, generic, searx string
	}{
		{"settings generic query", "websearch:\n  url: https://search.example/search?api_key=secret-value\n", "", ""},
		{"settings generic userinfo", "websearch:\n  url: https://user:secret-value@search.example/search\n", "", ""},
		{"settings generic malformed", "websearch:\n  url: https://user:secret-value@search.example/%zz\n", "", ""},
		{"settings SearXNG query", "websearch:\n  searxng:\n    url: https://searx.example/search?api_key=secret-value\n", "", ""},
		{"settings SearXNG fragment", "websearch:\n  searxng:\n    url: https://searx.example/search#secret-value\n", "", ""},
		{"CLI generic query", "websearch: {}\n", "https://search.example/search?api_key=secret-value", ""},
		{"CLI generic malformed", "websearch: {}\n", "https://user:secret-value@search.example/%zz", ""},
		{"CLI generic fragment", "websearch: {}\n", "https://search.example/search#secret-value", ""},
		{"legacy SearXNG query", "websearch: {}\n", "", "https://searx.example/search?api_key=secret-value"},
		{"legacy SearXNG userinfo", "websearch: {}\n", "", "https://user:secret-value@searx.example/search"},
		{"legacy SearXNG malformed", "websearch: {}\n", "", "https://user:secret-value@searx.example/%zz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &searchDiag{}
			cfg := Config{PermissionConfigs: []string{writeOperatorSettingsFile(t, tc.settings)},
				permConfigEnv: isolatedPermConfigEnv(t), WebSearchURL: tc.generic, SearXNGURL: tc.searx,
				BraveAPIKey: "fallback-brave", Diagnostics: d}
			cfg.permResolver = buildPermResolver(cfg)
			cfg = foldOperatorWebSearch(cfg)
			p := buildSearchProvider(t.Context(), cfg)
			if _, ok := p.(refsearch.BackendDown); !ok {
				t.Fatalf("rejected selected backend fell through: %T", p)
			}
			if logs := d.all(); !strings.Contains(logs, "misconfigured") || strings.Contains(logs, "secret-value") || strings.Contains(logs, "fallback-brave") {
				t.Fatalf("rejected endpoint diagnostics: %s", logs)
			}
		})
	}
}

func TestOperatorWebSearchToolBehavior(t *testing.T) {
	t.Run("settings disabled reports settings cause", func(t *testing.T) {
		cfg := Config{
			PermissionConfigs: []string{writeOperatorSettingsFile(t, "websearch:\n  enabled: false\n")},
			permConfigEnv:     isolatedPermConfigEnv(t),
		}
		cfg.permResolver = buildPermResolver(cfg)
		cfg = foldOperatorWebSearch(cfg)

		result, err := refsearch.NewWebSearchTool(buildSearchProvider(t.Context(), cfg)).Execute(t.Context(),
			session.NewToolCall("search", "WebSearch", json.RawMessage(`{"query":"test"}`)), testEnvironment(memfs.NewWorkspace("/"), nil))
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError || !strings.Contains(result.Content, "websearch.enabled: false") || strings.Contains(result.Content, "--websearch=off") {
			t.Fatalf("settings-disabled tool result = %+v", result)
		}
	})

	t.Run("explicit flag preserves kill-switch wording", func(t *testing.T) {
		result, err := refsearch.NewWebSearchTool(buildSearchProvider(t.Context(), Config{WebSearchOff: true})).Execute(t.Context(),
			session.NewToolCall("search", "WebSearch", json.RawMessage(`{"query":"test"}`)), testEnvironment(memfs.NewWorkspace("/"), nil))
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError || !strings.Contains(result.Content, "--websearch=off") {
			t.Fatalf("explicit kill-switch tool result = %+v", result)
		}
	})

	t.Run("explicit generic URL overrides settings disabled", func(t *testing.T) {
		called := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[{"title":"explicit backend"}]}`))
		}))
		defer server.Close()

		cfg := Config{
			PermissionConfigs: []string{writeOperatorSettingsFile(t, "websearch:\n  enabled: false\n")},
			permConfigEnv:     isolatedPermConfigEnv(t),
			WebSearchURL:      server.URL,
		}
		cfg.permResolver = buildPermResolver(cfg)
		cfg = foldOperatorWebSearch(cfg)
		result, err := refsearch.NewWebSearchTool(buildSearchProvider(t.Context(), cfg)).Execute(t.Context(),
			session.NewToolCall("search", "WebSearch", json.RawMessage(`{"query":"test"}`)), testEnvironment(memfs.NewWorkspace("/"), nil))
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError || !called || !strings.Contains(result.Content, "explicit backend") {
			t.Fatalf("explicit generic backend tool result = %+v, called=%v", result, called)
		}
	})
}

func TestBuildOperatorWebSearchPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, settings, url, searx, want string
		off                              bool
	}{
		{"settings URL beats SearXNG and Brave", "websearch:\n  url: https://settings.example/search\n  searxng:\n    url: https://configured-searx.example/search\n", "", "https://legacy.example/search", "operator HTTP backend (settings.yaml)", false},
		{"empty URL flag does not beat settings URL", "websearch:\n  url: https://settings.example/search\n", "  ", "", "operator HTTP backend (settings.yaml)", false},
		{"settings SearXNG beats legacy", "websearch:\n  searxng:\n    url: https://configured-searx.example/search\n", "", "https://legacy.example/search", "SearXNG backend (settings.yaml)", false},
		{"legacy beats Brave", "websearch: {}\n", "", "https://legacy.example/search", "SearXNG backend (SEARXNG_URL) endpoint https://legacy.example/search", false},
		{"Brave beats Exa", "websearch: {}\n", "", "", "Brave backend", false},
		{"disabled beats configured URL", "websearch:\n  enabled: false\n  url: https://settings.example/search\n", "", "https://legacy.example/search", "DISABLED by operator", false},
		{"explicit URL beats disabled", "websearch:\n  enabled: false\n", "https://flag.example/search", "", "explicit HTTP backend (--websearch-url; wins over env/default) endpoint https://flag.example/search", false},
		{"empty flag does not beat disabled", "websearch:\n  enabled: false\n", "  ", "", "DISABLED by operator", false},
		{"off beats explicit and settings", "websearch:\n  enabled: true\n  url: https://settings.example/search\n", "https://flag.example/search", "", "DISABLED by operator", true},
		{"Exa default", "websearch: {}\n", "", "", "Exa backend", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &searchDiag{}
			cfg := Config{Workspace: t.TempDir(), UseMock: true, NoSoul: true,
				PermissionConfigs: []string{writeOperatorSettingsFile(t, tc.settings)},
				permConfigEnv:     isolatedPermConfigEnv(t), Diagnostics: d,
				WebSearchURL: tc.url, SearXNGURL: tc.searx, WebSearchOff: tc.off,
				BraveAPIKey: "test-brave-secret", ExaAPIKey: "test-exa-secret"}
			if tc.name == "Exa default" {
				cfg.BraveAPIKey = ""
			}
			built, err := buildIsolated(t, t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(built.Close)
			if logs := d.all(); !strings.Contains(logs, tc.want) || strings.Contains(logs, "test-brave-secret") || strings.Contains(logs, "test-exa-secret") {
				t.Fatalf("search backend not selected or secret logged: %s", logs)
			}
		})
	}
}

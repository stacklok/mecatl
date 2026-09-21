package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/envscrub"
	"github.com/stacklok/mecatl/internal/adapter/jevrouter"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func jevTestServer(t *testing.T, choice string, confidence float64, input, output int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = fmt.Fprintf(w, `{"model":"jev-1.13.0","answers":{"delegated-model-category":{"type":"choice","choice":%q,"probabilities":{"small":0.9,"large":0.1},"confidence":%v}},"usage":{"input_tokens":%d,"output_tokens":%d}}`, choice, confidence, input, output)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func jevRouterConfig(t *testing.T, srv *httptest.Server) Config {
	t.Helper()
	router, err := jevrouter.New(jevrouter.Options{APIKey: "secret", BaseURL: srv.URL, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	cfg := routerTaxonomyCfg()
	cfg.RouterBackend = "jev"
	cfg.jevRouter = router
	return cfg
}

func TestADR_0350_Scenario1_BackendSelection(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn(`{"category":"large"}`))
	llmCfg := routerTaxonomyCfg()
	llmFn := buildModelRouterTask(llmCfg, regForTest(llm, providerAnthropic, llmCfg.Model), llm, providerAnthropic, llmCfg.Model)
	category, model, _, _, ok := llmFn(t.Context(), "task")
	if !ok || category != "large" || model != routerLarge {
		t.Fatalf("default backend route = %q/%q/%v", category, model, ok)
	}

	srv, calls := jevTestServer(t, "small", 1, 2, 1)
	jevCfg := jevRouterConfig(t, srv)
	jevFn := buildModelRouterTask(jevCfg, regForTest(llm, providerAnthropic, jevCfg.Model), llm, providerAnthropic, jevCfg.Model)
	category, model, _, _, ok = jevFn(t.Context(), "task")
	if !ok || category != "small" || model != routerSmall || calls.Load() != 1 {
		t.Fatalf("jev route = %q/%q/%v calls=%d", category, model, ok, calls.Load())
	}

	jevCfg.RouterDisabled = true
	jevCfg.jevRouter = nil
	if fn := buildModelRouterTask(jevCfg, nil, nil, "", ""); fn != nil {
		t.Fatal("disabled Jev router constructed a callback")
	}
}

func configWithRouterYAML(t *testing.T, body string) Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Config{Model: "session-model", UseMock: true, NoSoul: true, Workspace: t.TempDir(), UserModelDir: t.TempDir(), permResolver: permconfig.New(permconfig.Options{ExplicitFiles: []string{path}})}
}

func TestADR_0350_Scenario1_ValidationAndDisabledPrecedence(t *testing.T) {
	active := func(extra string) string {
		return "models:\n  router:\n    backend: jev\n" + extra + "    categories:\n      - name: small\n        description: small\n        model: gpt-5-mini\n"
	}
	for _, tc := range []struct {
		name, yaml string
		withKey    bool
	}{
		{"missing credential", active(""), false},
		{"explicit classifier slot", active("    classifier-slot: cheap\n"), true},
		{"explicit default category", active("    default-category: small\n"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := foldOperatorModelRouter(configWithRouterYAML(t, tc.yaml))
			if tc.withKey {
				cfg.TypesafeAPIKey = "secret"
			}
			if err := prepareJevRouter(&cfg); err == nil {
				t.Fatal("active invalid Jev configuration accepted")
			}
		})
	}
	llmWithJev := configWithRouterYAML(t, "models:\n  router:\n    backend: llm\n    jev:\n      model: jev-1.13.0\n    categories:\n      - name: small\n        description: small\n        model: gpt-5-mini\n")
	llmWithJev = foldOperatorModelRouter(llmWithJev)
	if err := prepareJevRouter(&llmWithJev); err == nil {
		t.Fatal("active LLM router accepted a jev block")
	}

	for _, yaml := range []string{
		"models:\n  router:\n    disabled: true\n    backend: jev\n    classifier-slot: cheap\n    categories:\n      - name: small\n        description: small\n        model: gpt-5-mini\n",
		"models:\n  router:\n    backend: jev\n    default-category: small\n",
	} {
		cfg := foldOperatorModelRouter(configWithRouterYAML(t, yaml))
		if err := prepareJevRouter(&cfg); err != nil {
			t.Fatalf("inactive router applied active checks: %v", err)
		}
		if cfg.jevRouter != nil {
			t.Fatal("inactive router constructed a client")
		}
	}

	inherited := foldOperatorModelRouter(configWithRouterYAML(t, active("")))
	inherited.TypesafeAPIKey = "secret"
	inherited.ModelSlots = map[string]string{"router": "cheap"}
	if err := prepareJevRouter(&inherited); err != nil {
		t.Fatalf("inherited router slot was treated as an explicit LLM-only conflict: %v", err)
	}
}

func TestJevEndpointAndCredentialDoNotSelectBackend(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn(`{"category":"small"}`))
	cfg := routerTaxonomyCfg()
	cfg.TypesafeAPIKey = "present-but-not-selected"
	cfg.RouterJevBaseURL = "http://127.0.0.1:1"
	fn := buildModelRouterTask(cfg, regForTest(llm, providerAnthropic, cfg.Model), llm, providerAnthropic, cfg.Model)
	category, _, _, _, ok := fn(t.Context(), "task")
	if !ok || category != "small" {
		t.Fatal("credential or endpoint presence selected Jev")
	}
}

func TestJevBackendUsesExistingRouterCallback(t *testing.T) {
	srv, calls := jevTestServer(t, "small", 1, 1, 1)
	cfg := jevRouterConfig(t, srv)
	fn := buildModelRouterTask(cfg, nil, nil, "", "")
	var callback agent.Deps
	callback.SubagentModelRouter = fn
	category, model, _, reason, ok := callback.SubagentModelRouter(t.Context(), "eligible delegation")
	if !ok || category != "small" || model != routerSmall || reason != "" || calls.Load() != 1 {
		t.Fatalf("existing callback contract changed: %q/%q reason=%q ok=%v calls=%d", category, model, reason, ok, calls.Load())
	}
}

func TestADR_0350_Scenario3_CompositionParity(t *testing.T) {
	srv, calls := jevTestServer(t, "small", 1, 4, 2)
	cfg := jevRouterConfig(t, srv)
	cfg.ModelAliases = map[string]string{"tiny": "resolved-model"}
	cfg.RouterCategories[0].Model = "tiny"
	for _, path := range []string{"shared", "per-session-no-fs"} {
		fn := buildModelRouterTask(cfg, nil, nil, "", "")
		category, model, usage, reason, ok := fn(context.Background(), path)
		if !ok || category != "small" || model != "resolved-model" || reason != "" || usage != (session.Usage{InputTokens: 4, OutputTokens: 2}) {
			t.Fatalf("%s route = %q/%q usage=%+v reason=%q ok=%v", path, category, model, usage, reason, ok)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("shared client calls = %d, want two paths through one client", calls.Load())
	}
}

func TestADR_0350_Scenario4_SecretAndContentRedaction(t *testing.T) {
	if _, ok := envscrub.DenyExact["TYPESAFE_API_KEY"]; !ok {
		t.Fatal("TYPESAFE_API_KEY missing from exact scrub set")
	}
	if _, ok := envscrub.NonOverridableExact["TYPESAFE_API_KEY"]; !ok {
		t.Fatal("TYPESAFE_API_KEY can be restored by command environment inheritance")
	}
	got := envscrub.ScrubWithInherited([]string{"PATH=/bin", "TYPESAFE_API_KEY=secret"}, []string{"TYPESAFE_API_KEY"}, envscrub.NonOverridableExact)
	if strings.Join(got, "\n") != "PATH=/bin" {
		t.Fatalf("scrubbed environment = %q", got)
	}

	secret, task, hostile := "credential-value", "private delegated task", "raw hostile response"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if got := req.Header.Get("Authorization"); got != "Bearer "+secret {
			t.Fatalf("authorization = %q", got)
		}
		_, _ = w.Write([]byte(hostile))
	}))
	t.Cleanup(srv.Close)
	router, err := jevrouter.New(jevrouter.Options{APIKey: secret, BaseURL: srv.URL, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	cfg := routerTaxonomyCfg()
	cfg.RouterBackend, cfg.jevRouter = "jev", router
	category, _, _, reason, ok := buildModelRouterTask(cfg, nil, nil, "", "")(t.Context(), task)
	if ok || category != "" || reason != jevrouter.MissInvalidResponse {
		t.Fatalf("unexpected result: %q %q %v", category, reason, ok)
	}
	for _, forbidden := range []string{secret, task, hostile} {
		if strings.Contains(reason, forbidden) {
			t.Fatalf("reason leaked %q: %q", forbidden, reason)
		}
	}
}

func TestJevMappingMissRetainsUsage(t *testing.T) {
	srv, _ := jevTestServer(t, "small", 1, 9, 3)
	cfg := jevRouterConfig(t, srv)
	cfg.RouterCategories[0].Model = "sonnet"
	_, _, usage, reason, ok := buildModelRouterTask(cfg, nil, nil, "", "")(t.Context(), "task")
	if ok || usage.InputTokens != 9 || usage.OutputTokens != 3 || !strings.HasPrefix(reason, "category-target-unresolvable") {
		t.Fatalf("mapping miss usage=%+v reason=%q ok=%v", usage, reason, ok)
	}
}

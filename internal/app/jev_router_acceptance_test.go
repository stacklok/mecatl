package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/envscrub"
	"github.com/stacklok/mecatl/internal/adapter/jevrouter"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	serveradapter "github.com/stacklok/mecatl/internal/adapter/server"
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
	category, model, _, _, ok := callModelRouter(t.Context(), llmFn, "task")
	if !ok || category != "large" || model != routerLarge {
		t.Fatalf("default backend route = %q/%q/%v", category, model, ok)
	}

	srv, calls := jevTestServer(t, "small", 1, 2, 1)
	jevCfg := jevRouterConfig(t, srv)
	jevFn := buildModelRouterTask(jevCfg, regForTest(llm, providerAnthropic, jevCfg.Model), llm, providerAnthropic, jevCfg.Model)
	category, model, _, _, ok = callModelRouter(t.Context(), jevFn, "task")
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
		{"explicit empty classifier slot", active("    classifier-slot: \"\"\n"), true},
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
	inherited.RouterClassifierSlot = "inherited-slot"
	inherited.RouterDefaultCategory = "small"
	if err := prepareJevRouter(&inherited); err != nil {
		t.Fatalf("inherited absent router defaults were treated as explicit LLM-only conflicts: %v", err)
	}

	withDefault := foldOperatorModelRouter(configWithRouterYAML(t, active("    default-category: medium\n")))
	withDefault.TypesafeAPIKey = "secret"
	if err := prepareJevRouter(&withDefault); err != nil {
		t.Fatalf("default-category must be valid for Jev: %v", err)
	}
}

func TestJevEndpointAndCredentialDoNotSelectBackend(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn(`{"category":"small"}`))
	cfg := routerTaxonomyCfg()
	cfg.TypesafeAPIKey = "present-but-not-selected"
	cfg.RouterJevBaseURL = "http://127.0.0.1:1"
	fn := buildModelRouterTask(cfg, regForTest(llm, providerAnthropic, cfg.Model), llm, providerAnthropic, cfg.Model)
	category, _, _, _, ok := callModelRouter(t.Context(), fn, "task")
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
	category, model, _, reason, ok := callModelRouter(t.Context(), callback.SubagentModelRouter, "eligible delegation")
	if !ok || category != "small" || model != routerSmall || reason != "" || calls.Load() != 1 {
		t.Fatalf("existing callback contract changed: %q/%q reason=%q ok=%v calls=%d", category, model, reason, ok, calls.Load())
	}
}

type jevBuildProvider struct {
	mu              sync.Mutex
	childModels     []string
	classifierCalls atomic.Int32
	callSerial      atomic.Int32
	subagentArgs    []byte
}

func (*jevBuildProvider) Capabilities() port.ProviderCapabilities { return port.ProviderCapabilities{} }

func (p *jevBuildProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	var chunks []port.Chunk
	switch {
	case isClassifierRequest(req):
		p.classifierCalls.Add(1)
		chunks = []port.Chunk{mockllm.TextChunk(`{"category":"large"}`), mockllm.DoneChunk(session.StopEndTurn)}
	case requestHasTool(req, "Subagent") && !requestHasToolResult(req):
		id := fmt.Sprintf("delegate-%d", p.callSerial.Add(1))
		args := p.subagentArgs
		if len(args) == 0 {
			args = []byte(`{"prompt":"inspect the task"}`)
		}
		chunks = []port.Chunk{
			mockllm.ToolCallChunk(session.NewToolCall(session.ToolCallID(id), "Subagent", args)),
			mockllm.DoneChunk(session.StopEndTurn),
		}
	case requestHasTool(req, "Subagent"):
		chunks = []port.Chunk{mockllm.TextChunk("parent done"), mockllm.DoneChunk(session.StopEndTurn)}
	default:
		p.mu.Lock()
		p.childModels = append(p.childModels, req.Model)
		p.mu.Unlock()
		chunks = []port.Chunk{mockllm.TextChunk("child done"), mockllm.DoneChunk(session.StopEndTurn)}
	}
	return func(yield func(port.Chunk, error) bool) {
		for _, chunk := range chunks {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if !yield(chunk, nil) {
				return
			}
		}
	}, nil
}

func requestHasTool(req port.LLMRequest, name string) bool {
	for _, spec := range req.Tools {
		if spec.Name == name {
			return true
		}
	}
	return false
}

func requestHasToolResult(req port.LLMRequest) bool {
	for _, message := range req.Messages {
		if message.Role == session.RoleTool {
			return true
		}
	}
	return false
}

func (p *jevBuildProvider) models() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.childModels...)
}

func TestADR_0350_Scenario3_CompositionParity(t *testing.T) {
	buildSource, err := os.ReadFile("build.go")
	if err != nil {
		t.Fatalf("read composition source: %v", err)
	}
	const routerWiring = "deps.SubagentModelRouter = buildModelRouterTask("
	if got := strings.Count(string(buildSource), routerWiring); got != 2 {
		t.Fatalf("shared and per-session router wiring sites = %d, want 2", got)
	}

	srv, calls := jevTestServer(t, "small", 1, 4, 2)
	provider := &jevBuildProvider{}
	built, err := buildIsolated(t, t.Context(), Config{
		Workspace:        t.TempDir(),
		UserModelDir:     t.TempDir(),
		UseMock:          true,
		MockProvider:     provider,
		NoSoul:           true,
		NoUserModel:      true,
		NoShell:          true,
		AllowAllTools:    true,
		Model:            "parent-model",
		RouterBackend:    "jev",
		RouterJevBaseURL: srv.URL,
		TypesafeAPIKey:   "secret",
		ModelAliases:     map[string]string{"tiny": "resolved-model"},
		RouterCategories: []permconfig.RouterCategory{
			{Name: "small", Description: "small task", Model: "tiny"},
			{Name: "large", Description: "large task", Model: "other-model"},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	defaultSession, err := built.Service.CreateSession(t.Context(), session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession(default): %v", err)
	}
	selected, err := built.Service.CreateSessionWithProvider(t.Context(), session.ModeDefault, defaultLimits(),
		serveradapter.ProviderSelector{ProviderID: providerMock, ModelID: "selected-parent"})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	noFS, err := built.Service.CreateSessionWithProfile(t.Context(), session.ModeDefault, defaultLimits(),
		serveradapter.ProviderSelector{}, serveradapter.ProfileNoFS)
	if err != nil {
		t.Fatalf("CreateSessionWithProfile(no-fs): %v", err)
	}

	for name, created := range map[string]*session.Session{"default": defaultSession, "selected": selected, "no-fs": noFS} {
		run, runErr := built.Service.StartRun(t.Context(), created.ID, "delegate")
		if runErr != nil {
			t.Fatalf("StartRun(%s): %v", name, runErr)
		}
		if got := drainRun(run); got != "parent done" {
			t.Fatalf("%s terminal text = %q", name, got)
		}
	}

	if provider.classifierCalls.Load() != 0 {
		t.Fatalf("Jev backend invoked the LLM classifier %d times", provider.classifierCalls.Load())
	}
	if calls.Load() != 3 {
		t.Fatalf("Build-owned Jev client calls = %d, want one route from each engine path", calls.Load())
	}
	models := provider.models()
	if len(models) != 3 {
		t.Fatalf("child requests = %v, want three", models)
	}
	for i, model := range models {
		if model != "resolved-model" {
			t.Fatalf("child model[%d] = %q, want local alias target resolved-model", i, model)
		}
	}
}

func jevChoiceServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		var body struct {
			State string `json:"state"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode Jev request: %v", err)
			return
		}
		choice := "small"
		if strings.Contains(strings.ToLower(body.State), "alpha") {
			choice = "large"
		}
		_, _ = fmt.Fprintf(w, `{"model":"jev-1.13.0","answers":{"delegated-model-category":{"type":"choice","choice":%q,"probabilities":{"small":0.5,"large":0.5},"confidence":1}},"usage":{"input_tokens":1,"output_tokens":1}}`, choice)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func applyJevRouting(cfg *Config, srv *httptest.Server) {
	cfg.RouterBackend = "jev"
	cfg.RouterJevBaseURL = srv.URL
	cfg.TypesafeAPIKey = "secret"
	cfg.ModelAliases = map[string]string{"small-alias": routerSmall, "large-alias": routerLarge}
	cfg.RouterCategories = []permconfig.RouterCategory{
		{Name: "small", Description: "small task", Model: "small-alias"},
		{Name: "large", Description: "large task", Model: "large-alias"},
	}
	cfg.RouterDefaultCategory = ""
}

func TestADR_0350_Scenario2_ExistingRoutingSemantics(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []byte
		agentSetup bool
	}{
		{name: "unpinned named specialist", args: []byte(`{"prompt":"review","agent":"reviewer"}`), agentSetup: true},
		{name: "writable explorer", args: []byte(`{"prompt":"edit","mode":"read-write"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := jevChoiceServer(t)
			provider := &jevBuildProvider{subagentArgs: tc.args}
			cfg := Config{
				Workspace: t.TempDir(), UserModelDir: t.TempDir(), UseMock: true, MockProvider: provider,
				NoSoul: true, NoShell: true, AllowAllTools: true, Model: "parent-model",
			}
			applyJevRouting(&cfg, srv)
			if tc.agentSetup {
				agentsDir := t.TempDir()
				writeAgentDefFile(t, agentsDir, "reviewer", "", "JEV-ROUTED-REVIEWER")
				cfg.AgentsDirs = []string{agentsDir}
			}
			built, err := buildIsolated(t, t.Context(), cfg)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			defer built.Close()
			created, err := built.Service.CreateSession(t.Context(), session.ModeDefault, defaultLimits())
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			run, err := built.Service.StartRun(t.Context(), created.ID, "delegate")
			if err != nil {
				t.Fatalf("StartRun: %v", err)
			}
			if got := drainRun(run); got != "parent done" {
				t.Fatalf("terminal text = %q", got)
			}
			if calls.Load() != 1 || provider.classifierCalls.Load() != 0 {
				t.Fatalf("Jev calls=%d LLM classifier calls=%d, want 1/0", calls.Load(), provider.classifierCalls.Load())
			}
			models := provider.models()
			if len(models) != 1 || models[0] != routerSmall {
				t.Fatalf("delegated child models = %v, want [%s]", models, routerSmall)
			}
		})
	}

	t.Run("team members route once for their lifetime", func(t *testing.T) {
		srv, calls := jevChoiceServer(t)
		teamCall := session.NewToolCall("c1", "Team", []byte(`{"goal":"do work","members":[{"name":"lead","role":"alpha architecture"},{"name":"worker","role":"rename a variable"}]}`))
		provider := &routingProvider{parentTool: "Team", parentCall: teamCall}
		cfg := routerE2ECfg(t.TempDir(), func() port.LLMProvider { return provider }, func(cfg *Config) {
			cfg.EnableTeams = true
			applyJevRouting(cfg, srv)
		})
		built, err := buildIsolated(t, t.Context(), cfg)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer built.Close()
		created, err := built.Service.CreateSession(t.Context(), session.ModeDefault, defaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		run, err := built.Service.StartRun(t.Context(), created.ID, "form team")
		if err != nil {
			t.Fatal(err)
		}
		drainRun(run)
		if calls.Load() != 2 {
			t.Fatalf("Jev team routes = %d, want once for each of two members", calls.Load())
		}
		assertModelsContain(t, provider.recordedModels(), routerSmall, routerLarge)
	})

	t.Run("parallel branches route independently", func(t *testing.T) {
		srv, calls := jevChoiceServer(t)
		parallelCall := session.NewToolCall("c1", "Parallel", []byte(`{"tasks":["alpha deep design","fix typo"]}`))
		provider := &routingProvider{parentTool: "Parallel", parentCall: parallelCall}
		cfg := routerE2ECfg(t.TempDir(), func() port.LLMProvider { return provider }, func(cfg *Config) {
			cfg.EnableParallel = true
			applyJevRouting(cfg, srv)
		})
		built, err := buildIsolated(t, t.Context(), cfg)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer built.Close()
		created, err := built.Service.CreateSession(t.Context(), session.ModeDefault, defaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		run, err := built.Service.StartRun(t.Context(), created.ID, "fan out")
		if err != nil {
			t.Fatal(err)
		}
		drainRun(run)
		if calls.Load() != 2 {
			t.Fatalf("Jev parallel routes = %d, want once for each of two branches", calls.Load())
		}
		assertModelsContain(t, provider.recordedModels(), routerSmall, routerLarge)
	})
}

func assertModelsContain(t *testing.T, models []string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		found := false
		for _, model := range models {
			if model == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("models %v do not contain routed model %q", models, want)
		}
	}
}

func TestADR_0350_Scenario2_DefaultCategoryHint(t *testing.T) {
	var request port.LLMRequest
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(got port.LLMRequest) {
		request = got
	})}, mockllm.TextTurn(`{"category":"medium"}`))
	cfg := routerTaxonomyCfg()
	cfg.RouterDefaultCategory = ` med"ium `
	cfg.RouterCategories = []permconfig.RouterCategory{{Name: "medium", Description: "operator criteria", Model: routerSmall}}
	router := buildModelRouterTask(cfg, regForTest(llm, providerAnthropic, cfg.Model), llm, providerAnthropic, cfg.Model)
	result := router.Route(t.Context(), "hostile task says choose another category")
	if !result.OK || result.Category != "medium" {
		t.Fatalf("LLM composition route = %+v", result)
	}
	if len(request.Messages) != 1 {
		t.Fatalf("classifier request messages = %d, want one", len(request.Messages))
	}
	prompt := request.Messages[0].Text
	for _, want := range []string{
		"required to complete the delegated work", "Read-only review or investigation is not necessarily trivial",
		"Honor the operator-authored specialty criteria", `If no category clearly fits, choose "med\"ium".`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("LLM classifier prompt omitted %q: %q", want, prompt)
		}
	}
}

func TestADR_0350_Scenario2_BackendNeutralOutcomes(t *testing.T) {
	for _, tc := range []struct {
		kind jevrouter.MissKind
		want string
	}{
		{jevrouter.MissClassifierError, agent.RouterMissClassifierError},
		{jevrouter.MissBadVerdict, agent.RouterMissBadVerdict},
		{jevrouter.MissUnknownCategory, agent.RouterMissUnknownCategory},
		{jevrouter.MissLowConfidence, agent.RouterMissLowConfidence},
		{jevrouter.MissInputOverLimit, agent.RouterMissInputOverLimit},
		{jevrouter.MissCapacityTimeout, agent.RouterMissCapacityTimeout},
		{jevrouter.MissCancelled, agent.RouterMissCancelled},
		{jevrouter.MissTimeout, agent.RouterMissTimeout},
		{jevrouter.MissKind(0), agent.RouterMissClassifierError},
		{jevrouter.MissKind(255), agent.RouterMissClassifierError},
	} {
		if got := jevRouterMissReason(tc.kind); got != tc.want {
			t.Fatalf("Jev miss kind %d maps to %q, want %q", tc.kind, got, tc.want)
		}
	}

	jevOutcome := func(ctx context.Context, minimumConfidence float64, task string, handler http.HandlerFunc) (session.Usage, string, bool, int32) {
		t.Helper()
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			handler(w, r)
		}))
		defer srv.Close()
		router, err := jevrouter.New(jevrouter.Options{
			APIKey: "secret", BaseURL: srv.URL, HTTPClient: srv.Client(), MinimumConfidence: minimumConfidence,
		})
		if err != nil {
			t.Fatal(err)
		}
		cfg := routerTaxonomyCfg()
		cfg.RouterBackend, cfg.jevRouter = "jev", router
		_, _, usage, reason, ok := callModelRouter(ctx, buildModelRouterTask(cfg, nil, nil, "", ""), task)
		return usage, reason, ok, calls.Load()
	}

	t.Run("actual Jev SDK outcomes cross composition", func(t *testing.T) {
		for _, tc := range []struct {
			name       string
			confidence float64
			task       string
			response   string
			wantReason string
			wantUsage  session.Usage
			wantCalls  int32
		}{
			{
				name: "structured unknown category", task: "task",
				response:   `{"model":"jev-1.13.0","answers":{"delegated-model-category":{"type":"choice","choice":"not-offered","probabilities":{"not-offered":1},"confidence":1}},"usage":{"input_tokens":2,"output_tokens":1}}`,
				wantReason: agent.RouterMissUnknownCategory, wantUsage: session.Usage{InputTokens: 2, OutputTokens: 1}, wantCalls: 1,
			},
			{
				name: "low confidence", confidence: 0.9, task: "task",
				response:   `{"model":"jev-1.13.0","answers":{"delegated-model-category":{"type":"choice","choice":"small","probabilities":{"small":0.6,"large":0.4},"confidence":0.6}},"usage":{"input_tokens":4,"output_tokens":2}}`,
				wantReason: agent.RouterMissLowConfidence, wantUsage: session.Usage{InputTokens: 4, OutputTokens: 2}, wantCalls: 1,
			},
			{
				name: "protocol error retains usage", task: "task",
				response:   `{"model":"jev-1.13.0","answers":{},"usage":{"input_tokens":5,"output_tokens":3}}`,
				wantReason: agent.RouterMissBadVerdict, wantUsage: session.Usage{InputTokens: 5, OutputTokens: 3}, wantCalls: 1,
			},
			{
				name: "local input cap", task: strings.Repeat("x", 1<<16+1),
				wantReason: agent.RouterMissInputOverLimit, wantCalls: 0,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				usage, reason, ok, calls := jevOutcome(t.Context(), tc.confidence, tc.task, func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte(tc.response))
				})
				if ok || reason != tc.wantReason || usage != tc.wantUsage || calls != tc.wantCalls {
					t.Fatalf("outcome = usage=%+v reason=%q ok=%v calls=%d; want usage=%+v reason=%q calls=%d",
						usage, reason, ok, calls, tc.wantUsage, tc.wantReason, tc.wantCalls)
				}
			})
		}
	})

	t.Run("live caller deadline is canonical timeout", func(t *testing.T) {
		entered := make(chan struct{})
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			close(entered)
			<-r.Context().Done()
			return nil, r.Context().Err()
		})}
		router, err := jevrouter.New(jevrouter.Options{APIKey: "secret", BaseURL: "https://example.com", HTTPClient: client})
		if err != nil {
			t.Fatal(err)
		}
		cfg := routerTaxonomyCfg()
		cfg.RouterBackend, cfg.jevRouter = "jev", router
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		_, _, usage, reason, ok := callModelRouter(ctx, buildModelRouterTask(cfg, nil, nil, "", ""), "task")
		select {
		case <-entered:
		default:
			t.Fatal("deadline test never entered the SDK HTTP request")
		}
		if ok || reason != agent.RouterMissTimeout || usage != (session.Usage{}) {
			t.Fatalf("deadline outcome = usage=%+v reason=%q ok=%v", usage, reason, ok)
		}
	})

	llmReason := func(ctx context.Context, turn mockllm.Turn) string {
		t.Helper()
		cfg := routerTaxonomyCfg()
		llm := mockllm.New(turn)
		fn := buildModelRouterTask(cfg, regForTest(llm, providerAnthropic, cfg.Model), llm, providerAnthropic, cfg.Model)
		_, _, _, reason, ok := callModelRouter(ctx, fn, "task")
		if ok {
			t.Fatal("LLM classifier unexpectedly routed")
		}
		return reason
	}
	if got := llmReason(t.Context(), mockllm.TextTurn("malformed")); got != agent.RouterMissBadVerdict {
		t.Fatalf("LLM malformed reason = %q", got)
	}
	if got := llmReason(t.Context(), mockllm.ErrorTurn(errors.New("opaque provider failure: deadline text is not evidence"))); got != agent.RouterMissClassifierError {
		t.Fatalf("LLM provider failure reason = %q", got)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if got := llmReason(cancelled, mockllm.TextTurn(`{"category":"small"}`)); got != agent.RouterMissCancelled {
		t.Fatalf("LLM cancellation reason = %q", got)
	}
	deadline, cancelDeadline := context.WithTimeout(t.Context(), 0)
	defer cancelDeadline()
	if got := llmReason(deadline, mockllm.TextTurn(`{"category":"small"}`)); got != agent.RouterMissTimeout {
		t.Fatalf("LLM deadline reason = %q", got)
	}

	jevReason := func(ctx context.Context, handler http.HandlerFunc) string {
		t.Helper()
		srv := httptest.NewServer(handler)
		t.Cleanup(srv.Close)
		router, err := jevrouter.New(jevrouter.Options{APIKey: "secret", BaseURL: srv.URL, HTTPClient: srv.Client()})
		if err != nil {
			t.Fatal(err)
		}
		cfg := routerTaxonomyCfg()
		cfg.RouterBackend, cfg.jevRouter = "jev", router
		_, _, _, reason, ok := callModelRouter(ctx, buildModelRouterTask(cfg, nil, nil, "", ""), "task")
		if ok {
			t.Fatal("Jev classifier unexpectedly routed")
		}
		return reason
	}
	if got := jevReason(t.Context(), func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("malformed")) }); got != agent.RouterMissBadVerdict {
		t.Fatalf("Jev malformed reason = %q", got)
	}
	if got := jevReason(t.Context(), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "deadline words are not evidence", http.StatusBadGateway)
	}); got != agent.RouterMissClassifierError {
		t.Fatalf("Jev transport reason = %q", got)
	}
	jevCancelled, jevCancel := context.WithCancel(t.Context())
	jevCancel()
	if got := jevReason(jevCancelled, func(http.ResponseWriter, *http.Request) { t.Fatal("cancelled Jev request performed I/O") }); got != agent.RouterMissCancelled {
		t.Fatalf("Jev cancellation reason = %q", got)
	}
	jevDeadline, jevDeadlineCancel := context.WithTimeout(t.Context(), 0)
	defer jevDeadlineCancel()
	if got := jevReason(jevDeadline, func(http.ResponseWriter, *http.Request) { t.Fatal("expired Jev request performed I/O") }); got != agent.RouterMissTimeout {
		t.Fatalf("Jev deadline reason = %q", got)
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
	category, _, _, reason, ok := callModelRouter(t.Context(), buildModelRouterTask(cfg, nil, nil, "", ""), task)
	if ok || category != "" || reason != agent.RouterMissBadVerdict {
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
	_, _, usage, reason, ok := callModelRouter(t.Context(), buildModelRouterTask(cfg, nil, nil, "", ""), "task")
	if ok || usage.InputTokens != 9 || usage.OutputTokens != 3 || !strings.HasPrefix(reason, "category-target-unresolvable") {
		t.Fatalf("mapping miss usage=%+v reason=%q ok=%v", usage, reason, ok)
	}
}

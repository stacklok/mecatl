package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestADR_0369_Scenario1_UniversalAliasTargets(t *testing.T) {
	const (
		parentProvider = providerOpenAI
		parentModel    = "parent-model"
		pairProvider   = providerOpenRouter
		pairModel      = "vendor/opaque:model.v1"
	)
	parent := mockllm.New()
	pair := mockllm.New()
	reg := twoProviderReg(parent, parentProvider, parentModel, pair, pairProvider)
	cfg := Config{
		Model:         parentModel,
		SubagentModel: "strong",
		ModelAliasTargets: ModelAliases{
			"contextual": {Model: "context-model"},
			"strong":     {ProviderID: pairProvider, Model: pairModel},
		},
	}

	t.Run("single resolver preserves contextual scalar and explicit pair", func(t *testing.T) {
		contextual, err := resolveModelTarget(cfg, parentProvider, "", "contextual")
		if err != nil || contextual != (ModelTarget{ProviderID: parentProvider, Model: "context-model"}) {
			t.Fatalf("contextual target = %+v, %v", contextual, err)
		}
		paired, err := resolveModelTarget(cfg, parentProvider, "", "strong")
		if err != nil || paired != (ModelTarget{ProviderID: pairProvider, Model: pairModel}) {
			t.Fatalf("paired target = %+v, %v", paired, err)
		}
	})

	t.Run("deployment default pair applies before new session construction", func(t *testing.T) {
		built, err := buildIsolated(t, t.Context(), Config{
			NoSoul:                true,
			DefaultModel:          "strong",
			ContextWindowOverride: 128000,
			ModelAliasTargets: ModelAliases{
				"strong": {ProviderID: pairProvider, Model: pairModel},
			},
			envDetector: fakeEnv(map[string]string{
				"OPENAI_API_KEY":     "test-key",
				"OPENROUTER_API_KEY": "test-key",
			}),
			liveModelHTTPClient: offlineHTTPClient(),
			providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
				return mockllm.New(mockllm.TextTurn("reply-from-" + id))
			},
		})
		if err != nil {
			t.Fatalf("Build pair default: %v", err)
		}
		defer built.Close()
		sess, err := built.Service.CreateSession(t.Context(), "", defaultLimits())
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		run, err := built.Service.StartRun(t.Context(), sess.ID, "hello")
		if err != nil {
			t.Fatalf("StartRun: %v", err)
		}
		if got := drainRun(run); got != "reply-from-"+pairProvider {
			t.Fatalf("zero-selector session reply = %q, want pair provider", got)
		}
	})

	t.Run("operator models.default pair applies before new session construction", func(t *testing.T) {
		settings := filepath.Join(t.TempDir(), "settings.yaml")
		if err := os.WriteFile(settings, []byte(`models:
  default: strong
  aliases:
    strong:
      provider: openrouter
      model: vendor/opaque:model.v1
`), 0o600); err != nil {
			t.Fatalf("write settings: %v", err)
		}
		resolver := permconfig.New(permconfig.Options{ExplicitFiles: []string{settings}})
		probe := foldOperatorModelSlots(Config{permResolver: resolver})
		probe, err := foldOperatorPairModelDefault(probe, captureCLIModelKeys(Config{}))
		if err != nil || probe.DefaultProvider != pairProvider || probe.DefaultModel != pairModel {
			t.Fatalf("operator pair pre-fold = provider %q model %q err=%v", probe.DefaultProvider, probe.DefaultModel, err)
		}
		conflict := foldOperatorModelSlots(Config{permResolver: resolver, DefaultProvider: parentProvider})
		if _, err := foldOperatorPairModelDefault(conflict, captureCLIModelKeys(Config{})); err == nil {
			t.Fatal("operator pair default unexpectedly accepted a conflicting explicit default provider")
		}
		built, err := buildIsolated(t, t.Context(), Config{
			NoSoul:                true,
			ContextWindowOverride: 128000,
			PermissionConfigs:     []string{settings},
			envDetector: fakeEnv(map[string]string{
				"OPENAI_API_KEY":     "test-key",
				"OPENROUTER_API_KEY": "test-key",
			}),
			liveModelHTTPClient: offlineHTTPClient(),
			providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
				return mockllm.New(mockllm.TextTurn("reply-from-" + id))
			},
		})
		if err != nil {
			t.Fatalf("Build operator pair default: %v", err)
		}
		defer built.Close()
		sess, err := built.Service.CreateSession(t.Context(), "", defaultLimits())
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		run, err := built.Service.StartRun(t.Context(), sess.ID, "hello")
		if err != nil {
			t.Fatalf("StartRun: %v", err)
		}
		if got := drainRun(run); got != "reply-from-"+pairProvider {
			t.Fatalf("operator-default session reply = %q, want pair provider", got)
		}
	})

	t.Run("global default and agent definition carry pair", func(t *testing.T) {
		if normalized, err := normalizeSubagentModel(cfg); err != nil || normalized != "strong" {
			t.Fatalf("normalize pair default = %q, %v", normalized, err)
		}
		childProvider, providerID, model, _ := resolveChildProvider(cfg, reg, agents.AgentDef{}, parent, parentProvider, parentModel)
		if childProvider != pair || providerID != pairProvider || model != pairModel {
			t.Fatalf("global child = (%T, %q, %q), want pair provider/model", childProvider, providerID, model)
		}
		deps := childExplorerDeps(cfg, reg, parent, parentProvider, parentModel, nil)
		if deps.LLM != pair || deps.Model != pairModel || deps.PromptConfig.Env.Model != pairModel {
			t.Fatalf("global child deps did not rederive on pair target: LLM=%T model=%q prompt-model=%q", deps.LLM, deps.Model, deps.PromptConfig.Env.Model)
		}

		def := agents.AgentDef{Name: "specialist", Model: "strong"}
		childProvider, providerID, model, _ = resolveChildProvider(cfg, reg, def, parent, parentProvider, parentModel)
		if childProvider != pair || providerID != pairProvider || model != pairModel {
			t.Fatalf("definition child = (%T, %q, %q), want pair provider/model", childProvider, providerID, model)
		}
	})

	t.Run("global pair default emits actual Subagent and Parallel provider", func(t *testing.T) {
		built, err := buildIsolated(t, t.Context(), Config{
			Workspace:             t.TempDir(),
			NoSoul:                true,
			Model:                 parentModel,
			DefaultProvider:       parentProvider,
			SubagentModel:         "strong",
			ModelAliasTargets:     cfg.ModelAliasTargets,
			EnableParallel:        true,
			AllowAllTools:         true,
			ContextWindowOverride: 128000,
			envDetector: fakeEnv(map[string]string{
				"OPENAI_API_KEY":     "test-key",
				"OPENROUTER_API_KEY": "test-key",
			}),
			liveModelHTTPClient: offlineHTTPClient(),
			providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
				if id == parentProvider {
					return mockllm.New(
						mockllm.ToolCallTurn(
							session.NewToolCall("sub", "Subagent", []byte(`{"prompt":"inspect"}`)),
							session.NewToolCall("par", "Parallel", []byte(`{"tasks":["inspect"]}`)),
						),
						mockllm.TextTurn("parent done"),
					)
				}
				return mockllm.New(mockllm.TextTurn("child done"), mockllm.TextTurn("branch done"))
			},
		})
		if err != nil {
			t.Fatalf("Build pair child default: %v", err)
		}
		defer built.Close()
		sess, err := built.Service.CreateSession(t.Context(), session.ModeDefault, defaultLimits())
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		run, err := built.Service.StartRun(t.Context(), sess.ID, "delegate")
		if err != nil {
			t.Fatalf("StartRun: %v", err)
		}
		var subagentProvider, parallelProvider, terminal string
		for ev := range run.Events() {
			if ev.Type == session.EvSubagentStart && ev.Subagent != nil {
				subagentProvider = ev.Subagent.Provider
			}
			if ev.Type == session.EvParallelBranch && ev.Parallel != nil && ev.Parallel.Kind == session.ParallelBranchStart {
				parallelProvider = ev.Parallel.Provider
			}
			if ev.Type == session.EvResult && ev.Result != nil {
				terminal = ev.Result.Text
			}
		}
		if !strings.Contains(terminal, "parent done") || subagentProvider != pairProvider || parallelProvider != pairProvider {
			t.Fatalf("delegation evidence terminal=%q subagent=%q parallel=%q, want pair provider %q", terminal, subagentProvider, parallelProvider, pairProvider)
		}
	})

	t.Run("conflicting definition provider falls back without discarding pair provider", func(t *testing.T) {
		def := agents.AgentDef{Name: "specialist", Provider: parentProvider, Model: "strong"}
		_, providerID, model, _ := resolveChildProvider(cfg, reg, def, parent, parentProvider, parentModel)
		if providerID != parentProvider || model != parentModel {
			t.Fatalf("conflicting definition resolved to %q/%q, want forgiving parent fallback", providerID, model)
		}
	})

	t.Run("automatic router carries pair and provider factory rederives dependencies", func(t *testing.T) {
		classifier := mockllm.New(mockllm.TextTurn(`{"category":"small"}`))
		reg.entries[parentProvider] = providerEntry{id: parentProvider, provider: classifier, available: true}
		routerCfg := routerTaxonomyCfg()
		routerCfg.ModelAliasTargets = cfg.ModelAliasTargets
		routerCfg.RouterCategories[0].Model = "strong"
		route := buildModelRouterTask(routerCfg, reg, classifier, parentProvider, parentModel).Route(context.Background(), "route me")
		if !route.OK || route.Provider != pairProvider || route.Model != pairModel {
			t.Fatalf("router target = %+v, want %s/%s", route, pairProvider, pairModel)
		}

		var mu sync.Mutex
		var seen []string
		observedPair := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
			mu.Lock()
			seen = append(seen, req.Model)
			mu.Unlock()
		})}, mockllm.TextTurn("done"))
		reg.entries[pairProvider] = providerEntry{id: pairProvider, provider: observedPair, available: true}
		factory := buildSubagentTargetEngineFactory(routerCfg, reg, classifier, parentProvider, nil)
		eng, ok := factory(agent.ModelTarget{Provider: route.Provider, Model: route.Model})
		if !ok || eng == nil {
			t.Fatal("pair target factory declined a registered provider")
		}
		drainEngine(t, eng)
		mu.Lock()
		defer mu.Unlock()
		if len(seen) != 1 || seen[0] != pairModel {
			t.Fatalf("pair provider requests = %v, want [%s]", seen, pairModel)
		}
	})
}

func TestADR_0369_Scenario1_NamedAutomaticRouterPairAliasTarget(t *testing.T) {
	const (
		parentModel = "parent-model"
		pairModel   = "opaque/provider-specific:model"
		bodyMarker  = "PAIR-ROUTED-SPECIALIST-SCOPE"
	)
	workspace, agentsDir := t.TempDir(), t.TempDir()
	writeAgentDefFile(t, agentsDir, "reviewer", "", bodyMarker)
	var (
		mu       sync.Mutex
		pairReqs []reqRec
	)
	built, err := buildIsolated(t, t.Context(), Config{
		Workspace:       workspace,
		NoSoul:          true,
		Model:           parentModel,
		DefaultProvider: providerOpenAI,
		AgentsDirs:      []string{agentsDir},
		ModelAliasTargets: ModelAliases{
			"pair-route": {ProviderID: providerOpenRouter, Model: pairModel},
		},
		RouterCategories:      []permconfig.RouterCategory{{Name: "deep", Description: "deep specialist work", Model: "pair-route"}},
		RouterDefaultCategory: "deep",
		AllowAllTools:         true,
		envDetector:           fakeEnv(map[string]string{"OPENAI_API_KEY": "test-key", "OPENROUTER_API_KEY": "test-key"}),
		liveModelHTTPClient:   offlineHTTPClient(),
		providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
			if id == providerOpenRouter {
				return mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
					mu.Lock()
					pairReqs = append(pairReqs, reqRec{model: r.Model, system: r.System.Render()})
					mu.Unlock()
				})}, mockllm.TextTurn("pair child done"))
			}
			return mockllm.New(
				mockllm.ToolCallTurn(session.NewToolCall("named", "Subagent", []byte(`{"prompt":"review","agent":"reviewer"}`))),
				mockllm.TextTurn(`{"category":"deep"}`),
				mockllm.TextTurn("parent done"),
			)
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(t.Context(), session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(t.Context(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	terminal, starts := drainRunWithSubagentStart(run)
	if terminal != "parent done" || len(starts) != 1 {
		t.Fatalf("run = terminal %q starts %+v", terminal, starts)
	}
	start := starts[0]
	if start.Provider != providerOpenRouter || start.Model != pairModel || start.RoutedCategory != "deep" || start.RoutedModel != pairModel || start.RoutingReason != "" {
		t.Fatalf("named pair route evidence = %+v", start)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(pairReqs) != 1 || pairReqs[0].model != pairModel || !strings.Contains(pairReqs[0].system, bodyMarker) {
		t.Fatalf("pair-provider requests = %+v, want one scoped %q request", pairReqs, pairModel)
	}
}

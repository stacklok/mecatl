package app

import (
	"context"
	"sync"
	"testing"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
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

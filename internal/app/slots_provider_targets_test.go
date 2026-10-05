package app

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestProviderAwareAuxiliarySlotsUseIsolatedTargets(t *testing.T) {
	const (
		parentProvider = "parent"
		targetProvider = "target"
		parentModel    = "parent-model"
		targetModel    = "target-model"
	)
	parentCalls, targetCalls := 0, 0
	parent := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(port.LLMRequest) { parentCalls++ })}, mockllm.TextTurn("parent"))
	target := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(port.LLMRequest) { targetCalls++ })}, mockllm.TextTurn(`{"title":"target title"}`))
	reg := twoProviderReg(parent, parentProvider, parentModel, target, targetProvider)
	cfg := Config{
		Model:                    parentModel,
		SubagentAskReviewerModel: parentModel,
		ModelSlots:               map[string]string{slotAskReviewer: "utility", slotTitle: "utility"},
		ModelAliasTargets: ModelAliases{"utility": {
			ProviderID: targetProvider,
			Model:      targetModel,
		}},
	}

	deps, ok := askAdjudicatorDeps(cfg, reg, parent, parentProvider, parentModel)
	if !ok {
		t.Fatal("provider-aware ask-reviewer slot was not configured")
	}
	if deps.LLM != target || deps.Model != targetModel {
		t.Fatalf("ask-reviewer deps = provider %T model %q, want isolated target provider/model", deps.LLM, deps.Model)
	}

	generatorFactory := titleGeneratorForSession(cfg, reg)
	if generatorFactory == nil {
		t.Fatal("provider-aware title slot did not enable title generation")
	}
	result := generatorFactory(server.ProviderSelector{ProviderID: parentProvider, ModelID: parentModel}).Generate(context.Background(), []string{"source"})
	if result.ProviderID != targetProvider || result.ModelID != targetModel {
		t.Fatalf("title attribution = %s/%s, want %s/%s", result.ProviderID, result.ModelID, targetProvider, targetModel)
	}
	if parentCalls != 0 || targetCalls != 1 {
		t.Fatalf("title provider calls = parent:%d target:%d, want 0/1", parentCalls, targetCalls)
	}
}

func TestProviderAwareAuxiliaryConsumersPreserveProvider(t *testing.T) {
	const parentID, targetID = "parent", "target"
	parent := mockllm.New(mockllm.TextTurn(`{"category":"large"}`))
	target := mockllm.New(mockllm.TextTurn(`{"category":"large"}`))
	reg := twoProviderReg(parent, parentID, "parent-model", target, targetID)
	cfg := Config{
		Model:                 "parent-model",
		Compaction:            "cascade",
		ModelSlots:            map[string]string{},
		ModelAliasTargets:     ModelAliases{"utility": {ProviderID: targetID, Model: "target-model"}},
		modelProviderRegistry: reg,
	}
	for _, slot := range []string{slotCompaction, slotGuardrail, slotReflection, slotRouter} {
		cfg.ModelSlots[slot] = "utility"
	}

	deps := engineDepsForProvider(cfg, parent, session.ProviderModelID{ProviderID: parentID, ModelID: cfg.Model}, func() int { return defaultContextWindowTokens }, nil, childPermPolicy(cfg), nil, nil, nil)
	compactor, ok := deps.Compactor.(agent.CascadeCompactor)
	if !ok || compactor.LLM != target || compactor.Model != "target-model" {
		t.Fatalf("compactor target = %T/%q, want isolated target provider/model", compactor.LLM, compactor.Model)
	}

	providerID, model, _, configured, err := resolveGuardrailBinding(cfg, reg)
	if err != nil || !configured || providerID != targetID || model != "target-model" {
		t.Fatalf("guardrail target = %s/%s configured=%v err=%v, want %s/target-model", providerID, model, configured, err, targetID)
	}

	cfg.RouterCategories = []permconfig.RouterCategory{{Name: "large", Description: "large", Model: "parent-model"}}
	router := buildModelRouterTask(cfg, reg, parent, parentID, cfg.Model)
	if router == nil {
		t.Fatal("router classifier was not built")
	}
	result := router.Route(context.Background(), "classify")
	if !result.OK {
		t.Fatalf("router classification failed: %+v", result)
	}
	if router.ClassifierModel != "target-model" {
		t.Fatalf("router classifier model = %q, want target-model", router.ClassifierModel)
	}
}

func TestProviderAwareAuxiliarySlotResolutionFailsSoft(t *testing.T) {
	parent := mockllm.New()
	reg := regForTest(parent, "parent", "parent-model")
	cfg := Config{
		ModelSlots: map[string]string{slotCompaction: "utility"},
		ModelAliasTargets: ModelAliases{"utility": {
			ProviderID: "runtime-unavailable",
			Model:      "target-model",
		}},
	}
	provider, providerID, model, configured := resolveAuxiliarySlotTarget(cfg, reg, slotCompaction, parent, "parent", "parent-model")
	if configured || provider != parent || providerID != "parent" || model != "parent-model" {
		t.Fatalf("runtime-unavailable fallback = provider %T %s/%s configured=%v, want ordinary parent pair and false", provider, providerID, model, configured)
	}
}

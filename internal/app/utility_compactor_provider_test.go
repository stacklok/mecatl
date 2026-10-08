package app

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// TestUtilityCascadeCompactorsUseParentProvider prevents deployment-default
// attribution from leaking into utilities constructed for a selected session
// provider. The utilities have private engines, so their deps builders are the
// narrow composition seam where the nested compactor remains observable.
func TestUtilityCascadeCompactorsUseParentProvider(t *testing.T) {
	const (
		deploymentProvider = "deployment-default"
		parentProvider     = "session-selected"
		parentModel        = "session-model"
	)
	provider := mockllm.New()
	reg := regForTest(provider, deploymentProvider, parentModel)
	cfg := Config{
		Model:      parentModel,
		UseMock:    true,
		Compaction: "cascade",
	}

	assertCascadeProvider := func(name string, deps agent.Deps) {
		t.Helper()
		compactor, ok := deps.Compactor.(agent.CascadeCompactor)
		if !ok {
			t.Fatalf("%s compactor = %T, want agent.CascadeCompactor", name, deps.Compactor)
		}
		want := session.ProviderModelID{ProviderID: parentProvider, ModelID: compactor.Model}
		if got := compactor.ProviderModel; got != want {
			t.Fatalf("%s cascade ProviderModel = %+v, want %+v", name, got, want)
		}
		if got := deps.ProviderModel; got != want {
			t.Fatalf("%s engine ProviderModel = %+v, want %+v", name, got, want)
		}
	}

	primaryModel := session.ProviderModelID{ProviderID: parentProvider, ModelID: parentModel}
	primary := engineDepsForProvider(cfg, provider, primaryModel, func() int { return defaultContextWindowTokens }, nil, nil, nil, nil, nil)
	if got := primary.ProviderModel; got != primaryModel {
		t.Fatalf("primary ProviderModel = %+v, want %+v", got, primaryModel)
	}
	assertCascadeProvider("primary", primary)

	assertCascadeProvider("parallel judge", parallelJudgeDeps(cfg, reg, parentProvider, provider))

	askCfg := cfg
	askCfg.SubagentAskReviewerModel = "reviewer-model"
	askDeps, ok := askAdjudicatorDeps(askCfg, reg, provider, parentProvider, parentModel)
	if !ok {
		t.Fatal("ask adjudicator dependencies were not built")
	}
	assertCascadeProvider("ask adjudicator", askDeps)

	routerCfg := cfg
	routerCfg.RouterCategories = routerTaxonomyCfg().RouterCategories
	assertCascadeProvider("model router", modelRouterDeps(routerCfg, reg, provider, parentProvider, parentModel))

	guardrailsCfg := cfg
	guardrailsCfg.GuardrailsModel = "guardrail-model"
	guardrailsDeps, ok := guardrailsCheckerDeps(guardrailsCfg, reg, provider, parentProvider, parentModel)
	if !ok {
		t.Fatal("guardrails checker dependencies were not built")
	}
	assertCascadeProvider("guardrails checker", guardrailsDeps)
}

func TestUtilityCascadeCompactorUsesSelectedProviderAndSlotModel(t *testing.T) {
	const (
		deploymentProvider = "deployment-default"
		selectedProvider   = "session-selected"
		selectedModel      = "session-primary-model"
		reviewerModel      = "reviewer-primary-model"
		compactionModel    = "compaction-slot-model"
	)
	provider := mockllm.New()
	reg := regForTest(provider, deploymentProvider, "deployment-model")
	cfg := Config{
		Model:                    "deployment-model",
		Compaction:               "cascade",
		SubagentAskReviewerModel: reviewerModel,
		ModelSlots:               map[string]string{slotCompaction: slotCheap},
		ModelAliases:             map[string]string{slotCheap: compactionModel},
	}

	deps, ok := askAdjudicatorDeps(cfg, reg, provider, selectedProvider, selectedModel)
	if !ok {
		t.Fatal("ask adjudicator dependencies were not built")
	}
	if want := (session.ProviderModelID{ProviderID: selectedProvider, ModelID: reviewerModel}); deps.ProviderModel != want {
		t.Fatalf("ask adjudicator ProviderModel = %+v, want %+v", deps.ProviderModel, want)
	}
	compactor, ok := deps.Compactor.(agent.CascadeCompactor)
	if !ok {
		t.Fatalf("ask adjudicator compactor = %T, want agent.CascadeCompactor", deps.Compactor)
	}
	if want := (session.ProviderModelID{ProviderID: selectedProvider, ModelID: compactionModel}); compactor.ProviderModel != want {
		t.Fatalf("ask adjudicator cascade ProviderModel = %+v, want %+v", compactor.ProviderModel, want)
	}
}

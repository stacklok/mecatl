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
		Model:               parentModel,
		UseMock:             true,
		Compaction:          "cascade",
		auxiliaryProviderID: deploymentProvider,
	}

	assertCascadeProvider := func(name string, deps agent.Deps) {
		t.Helper()
		compactor, ok := deps.Compactor.(agent.CascadeCompactor)
		if !ok {
			t.Fatalf("%s compactor = %T, want agent.CascadeCompactor", name, deps.Compactor)
		}
		if got, want := compactor.ProviderModel, (session.ProviderModelID{ProviderID: parentProvider, ModelID: compactor.Model}); got != want {
			t.Fatalf("%s cascade ProviderModel = %+v, want %+v", name, got, want)
		}
	}

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

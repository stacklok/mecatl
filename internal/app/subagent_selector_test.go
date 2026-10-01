package app

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestSubagentSelectorComposition(t *testing.T) {
	parent := mockllm.New(mockllm.TextTurn("parent"))
	second := mockllm.New(mockllm.TextTurn("second"))
	reg := twoProviderReg(parent, "parent", "parent-model", second, "second")
	cfg := Config{
		ModelAliasTargets: ModelAliases{
			"pair":        {ProviderID: "second", Model: "pair-model"},
			"parent-pair": {ProviderID: "parent", Model: "parent-pair-model"},
			"same":        {Model: "same-model"},
		},
		RouterCategories: []permconfig.RouterCategory{
			{Name: "deep", Model: "pair"},
			{Name: "quick", Model: "same"},
		},
	}
	resolve := buildSubagentSelectorResolver(cfg, reg, "parent")

	for _, tc := range []struct {
		name             string
		provider, model  string
		want             agent.ModelTarget
		actual, category string
		providerBearing  bool
	}{
		{name: "literal", model: "opaque", want: agent.ModelTarget{Model: "opaque"}, actual: "parent"},
		{name: "pair alias", model: "pair", want: agent.ModelTarget{Provider: "second", Model: "pair-model"}, actual: "second", providerBearing: true},
		{name: "parent-provider pair alias preserves provenance", model: "parent-pair", want: agent.ModelTarget{Model: "parent-pair-model"}, actual: "parent", providerBearing: true},
		{name: "explicit scalar alias", provider: "second", model: "same", want: agent.ModelTarget{Provider: "second", Model: "same-model"}, actual: "second", providerBearing: true},
		{name: "router pair", provider: reservedModelRouterProvider, model: "deep", want: agent.ModelTarget{Provider: "second", Model: "pair-model"}, actual: "second", category: "deep", providerBearing: true},
		{name: "router contextual", provider: reservedModelRouterProvider, model: "quick", want: agent.ModelTarget{Model: "same-model"}, actual: "parent", category: "quick", providerBearing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolve(tc.provider, tc.model)
			if err != nil || got.Target != tc.want || got.ActualProvider != tc.actual || got.ExplicitRouterCategory != tc.category || got.ProviderBearing != tc.providerBearing {
				t.Fatalf("resolve(%q,%q) = %+v, err=%v", tc.provider, tc.model, got, err)
			}
		})
	}

	for _, tc := range []struct{ provider, model string }{
		{provider: "missing", model: "x"},
		{provider: "parent", model: "pair"},
		{provider: reservedModelRouterProvider, model: "missing"},
		{provider: "parent\nsecond", model: "x"},
		{provider: "parent", model: "x\u202Ey"},
		{provider: "parent", model: strings.Repeat("x", maxAgentModelDiscoveryFilterBytes+1)},
		{provider: "parent", model: string([]byte{0xff})},
	} {
		if _, err := resolve(tc.provider, tc.model); err == nil {
			t.Fatalf("resolve(%q,%q) unexpectedly succeeded", tc.provider, tc.model)
		}
	}

	factory := buildSubagentTargetEngineFactory(cfg, reg, parent, "parent", nil)
	eng, ok := factory(agent.ModelTarget{Provider: "second", Model: "fresh-model"})
	if !ok || eng == nil || eng.Model() != "fresh-model" {
		t.Fatalf("cross-provider factory = %v, %v", eng, ok)
	}
}

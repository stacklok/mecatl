package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestSettingsScalarSelectorsBindDefaultProvider(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(settings, []byte(`models:
  default_provider: openai
  default: main
  subagent: child-model
  aliases:
    main: {provider: openrouter, model: parent-model}
    coder: child-model
  slots:
    compaction: utility-model
    plan: coder
    guardrail: coder
  router:
    categories:
      - name: medium
        description: ordinary work
        model: coder
      - name: direct
        description: direct model
        model: direct-model
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{permResolver: permconfig.New(permconfig.Options{ExplicitFiles: []string{settings}}), ModelAliases: map[string]string{"cli": "cli-model"}}
	keys := captureCLIModelKeys(cfg)
	cfg = foldOperatorModelSlots(cfg)
	cfg = foldOperatorDefaultProvider(cfg)
	cfg = bindOperatorModelProvider(cfg, keys)
	cfg = foldOperatorPairModelDefault(cfg, keys)
	if cfg.DefaultProvider != "openrouter" {
		t.Fatalf("main pair = %q", cfg.DefaultProvider)
	}
	cfg = foldOperatorModelRouter(cfg)
	cfg = foldOperatorSubagentModel(cfg, keys)
	parent := mockllm.New(mockllm.TextTurn("parent"))
	child := mockllm.New(mockllm.TextTurn("child"))
	reg := twoProviderReg(parent, "openrouter", "parent-model", child, "openai")
	resolve := buildSubagentSelectorResolver(cfg, reg, "openrouter")
	for _, tc := range []struct {
		name, provider, model, wantProvider, wantModel string
	}{
		{"configured alias", "", "coder", "openai", "child-model"},
		{"router alias", reservedModelRouterProvider, "medium", "openai", "child-model"},
		{"router literal", reservedModelRouterProvider, "direct", "openai", "direct-model"},
		{"direct literal", "", "child-model", "openrouter", "child-model"},
		{"CLI scalar alias", "", "cli", "openrouter", "cli-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolve(tc.provider, tc.model)
			if err != nil || got.ActualProvider != tc.wantProvider || got.Target.Model != tc.wantModel {
				t.Fatalf("selector = %+v, err=%v", got, err)
			}
		})
	}
	if _, err := resolve("openrouter", "coder"); err == nil {
		t.Fatal("explicit provider overrode a settings-bound alias")
	}
	if target, ok := resolveSlotTarget(cfg, slotCompaction, "openrouter"); !ok || target.ProviderID != "openai" || target.Model != "utility-model" {
		t.Fatalf("configured slot = %+v, ok=%t", target, ok)
	}
	if target, ok := resolveSlotTarget(cfg, slotPlan, "openrouter"); !ok || target.ProviderID != "openai" || target.Model != "child-model" {
		t.Fatalf("provider-bound plan target = %+v, ok=%t", target, ok)
	}
	if provider, model, _, configured, err := resolveGuardrailBinding(cfg, reg); err != nil || !configured || provider != "openai" || model != "child-model" {
		t.Fatalf("guardrail slot = (%q, %q), configured=%t err=%v", provider, model, configured, err)
	}
	if provider, model := resolveProviderModel(cfg, reg, agentfs.AgentDef{}, "openrouter", "parent-model"); provider != "openai" || model != "child-model" {
		t.Fatalf("configured subagent default = (%q, %q)", provider, model)
	}
	if provider, model := resolveProviderModel(cfg, reg, agentfs.AgentDef{Model: "coder"}, "openrouter", "parent-model"); provider != "openai" || model != "child-model" {
		t.Fatalf("agent definition alias = (%q, %q)", provider, model)
	}
	cli := Config{permResolver: cfg.permResolver, DefaultProvider: "openrouter", DefaultProviderFlagSet: true}
	cliKeys := captureCLIModelKeys(cli)
	cli = foldOperatorModelSlots(cli)
	cli = bindOperatorModelProvider(foldOperatorDefaultProvider(cli), cliKeys)
	if target, known := lookupModelAliasTarget(cli, "coder"); !known || target.ProviderID != "openrouter" || target.Model != "child-model" {
		t.Fatalf("CLI default-provider precedence = %+v, known=%t", target, known)
	}
}

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

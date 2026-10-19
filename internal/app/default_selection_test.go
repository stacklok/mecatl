package app

import (
	"context"
	"testing"
)

func TestProviderSetupFollowup_Scenario3_SharedDefaultResolution(t *testing.T) {
	for _, tc := range []struct {
		name, selector, declared, alias string
		wantOK                          bool
	}{
		{"bare declared", "model", "model", "", true},
		{"operator alias", "chosen", "model", "model", true},
		{"inherit", "inherit", "model", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := customProviderConfig()
			def := cfg.ProviderDefinitions["gateway-responses"]
			def.DefaultModel = tc.declared
			cfg.ProviderDefinitions[def.ID] = def
			cfg.DefaultProvider, cfg.DefaultModel = def.ID, tc.selector
			cfg.envDetector = fakeEnv(nil)
			cfg.skipProviderNetworkDiscovery = true
			if tc.alias != "" {
				cfg.ModelAliases = map[string]string{tc.selector: tc.alias}
			}
			provider, model, err := ResolveDeploymentDefault(t.Context(), cfg)
			if (err == nil) != tc.wantOK {
				t.Fatalf("shared resolver success = %v, want %v: %v", err == nil, tc.wantOK, err)
			}
			if !tc.wantOK {
				return
			}
			cfg.DefaultProvider, cfg.DefaultModel = provider, model
			cfg.Workspace = t.TempDir()
			cfg.NoSoul = true
			cfg.permConfigEnv = isolatedPermConfigEnv(t)
			built, err := Build(t.Context(), cfg)
			if err != nil {
				t.Fatalf("ordinary startup rejected shared selection: %v", err)
			}
			defer built.Close()
			if provider != def.ID || model != tc.declared {
				t.Fatalf("resolved pair = %s/%s", provider, model)
			}
		})
	}
}

func TestResolveDeploymentDefaultAgreesWithStartupResolver(t *testing.T) {
	cfg := Config{
		DefaultProvider: "openrouter",
		DefaultModel:    "openai/gpt-5-mini",
		OpenRouterKey:   "test-key",
		ToolhiveLLM:     false,
	}
	reg, err := buildProviderRegistry(cfg, func(string) string { return "" })
	if err != nil {
		t.Fatalf("build startup registry: %v", err)
	}
	if err := validateDefaultModel(cfg, reg); err != nil {
		t.Fatalf("validate startup default: %v", err)
	}
	provider, model, err := ResolveDeploymentDefault(context.Background(), cfg)
	if err != nil {
		t.Fatalf("resolve update default: %v", err)
	}
	if provider != reg.Default() || model != reg.ResolvedDefaultModel() {
		t.Fatalf("update resolver = (%q, %q), startup = (%q, %q)", provider, model, reg.Default(), reg.ResolvedDefaultModel())
	}
}

func TestApplyDeploymentDefaultTargetDoesNotResolveAliasedModelTwice(t *testing.T) {
	cfg := Config{
		DefaultProvider:       "openai",
		DefaultModel:          "deep",
		defaultModelFromAlias: true,
		ModelAliasTargets: ModelAliases{
			"deep": {ProviderID: "openrouter", Model: "other-model"},
		},
	}
	got, err := applyDeploymentDefaultTarget(cfg)
	if err != nil || got.DefaultProvider != "openai" || got.DefaultModel != "deep" {
		t.Fatalf("pre-resolved alias changed: provider=%q model=%q err=%v", got.DefaultProvider, got.DefaultModel, err)
	}
}

func TestResolveDeploymentDefaultPairAliasOverridesFallbackProvider(t *testing.T) {
	cfg := Config{
		DefaultProvider: "openai",
		DefaultModel:    "strong",
		ModelAliasTargets: ModelAliases{
			"strong": {ProviderID: "openrouter", Model: "vendor/opaque:model.v1"},
		},
		OpenAIKey:     "test-key",
		OpenRouterKey: "test-key",
		ToolhiveLLM:   false,
	}
	provider, model, err := ResolveDeploymentDefault(t.Context(), cfg)
	if err != nil {
		t.Fatalf("resolve paired default: %v", err)
	}
	if provider != "openrouter" || model != "vendor/opaque:model.v1" {
		t.Fatalf("resolved pair = (%q, %q)", provider, model)
	}

	cfg.ModelAliasTargets["strong"] = ModelTarget{ProviderID: "unavailable", Model: "opaque"}
	if _, _, err := ResolveDeploymentDefault(t.Context(), cfg); err == nil {
		t.Fatal("unavailable alias provider fell back to the configured provider")
	}
}

func TestResolveDeploymentDefaultUsesProviderModelFallback(t *testing.T) {
	provider, model, err := ResolveDeploymentDefault(context.Background(), Config{
		DefaultProvider: "openrouter",
		OpenRouterKey:   "test-key",
		ToolhiveLLM:     false,
	})
	if err != nil {
		t.Fatalf("resolve fallback: %v", err)
	}
	if provider != providerOpenRouter || model != "openai/gpt-5" {
		t.Fatalf("fallback = (%q, %q), want (%q, %q)", provider, model, providerOpenRouter, "openai/gpt-5")
	}
}

func TestResolveDeploymentDefaultRejectsUnavailableProvider(t *testing.T) {
	if _, _, err := ResolveDeploymentDefault(context.Background(), Config{DefaultProvider: "anthropic", ToolhiveLLM: false}); err == nil {
		t.Fatal("unavailable provider was accepted")
	}
}

func TestResolveDeploymentDefaultAcceptsUncataloguedConcreteModel(t *testing.T) {
	provider, model, err := ResolveDeploymentDefault(context.Background(), Config{
		DefaultProvider: "openai",
		DefaultModel:    "not-a-model",
		OpenAIKey:       "test-key",
		ToolhiveLLM:     false,
	})
	if err != nil {
		t.Fatalf("resolve uncatalogued model: %v", err)
	}
	if provider != providerOpenAI || model != "not-a-model" {
		t.Fatalf("resolved = (%q, %q), want (%q, %q)", provider, model, providerOpenAI, "not-a-model")
	}
}

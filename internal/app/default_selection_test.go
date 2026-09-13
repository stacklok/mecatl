package app

import (
	"context"
	"testing"
)

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

func TestResolveDeploymentDefaultRejectsUnavailablePair(t *testing.T) {
	if _, _, err := ResolveDeploymentDefault(context.Background(), Config{DefaultProvider: "anthropic", ToolhiveLLM: false}); err == nil {
		t.Fatal("unavailable provider was accepted")
	}
	if _, _, err := ResolveDeploymentDefault(context.Background(), Config{DefaultProvider: "openai", DefaultModel: "not-a-model", OpenAIKey: "test-key", ToolhiveLLM: false}); err == nil {
		t.Fatal("unknown model was accepted")
	}
}

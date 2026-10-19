package app

import (
	"strings"
	"testing"
)

func TestModelAliasTargetsValidateWithoutInventoryProbe(t *testing.T) {
	cfg := Config{UseMock: true, ModelAliasTargets: ModelAliases{
		"scalar": {Model: "pair"},
		"pair":   {ProviderID: providerMock, Model: "opaque/not-in-inventory"},
	}}
	reg, err := buildProviderRegistry(cfg, fakeEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateModelAliases(cfg.ModelAliasTargets, reg); err != nil {
		t.Fatalf("valid aliases: %v", err)
	}
	if got, ok := lookupModelAliasTarget(cfg, "scalar"); !ok || got.ProviderID != "" || got.Model != "pair" {
		t.Fatalf("single-hop scalar lookup = %+v, %v", got, ok)
	}
	if got, ok := lookupModelAliasTarget(cfg, "pair"); !ok || got.ProviderID != providerMock || got.Model != "opaque/not-in-inventory" {
		t.Fatalf("pair lookup = %+v, %v", got, ok)
	}

	for name, target := range map[string]ModelTarget{
		"reserved":    {ProviderID: "model-router", Model: "category"},
		"unknown":     {ProviderID: "missing", Model: "opaque"},
		"no model":    {ProviderID: providerMock},
		"no provider": {Model: ""},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateModelAliases(ModelAliases{"bad": target}, reg)
			if err == nil || len(err.Error()) > 512 || (target.Model != "" && strings.Contains(err.Error(), target.Model)) {
				t.Fatalf("validation error = %v", err)
			}
		})
	}

	if built, err := buildIsolated(t, t.Context(), Config{
		UseMock: true, NoSoul: true,
		ModelAliasTargets: ModelAliases{"unmatched": {ProviderID: providerMock}},
	}); err == nil {
		built.Close()
		t.Fatal("Build accepted an unmatched CLI provider target")
	}
}

func TestModelAliasWholeTargetPrecedenceAndProviderConflict(t *testing.T) {
	cfg := Config{ModelAliasTargets: ModelAliases{"fast": {ProviderID: "anthropic", Model: "claude"}}}
	if _, err := resolveModelTarget(cfg, "openai", "openai", "fast"); err == nil {
		t.Fatal("conflicting explicit provider accepted")
	}
	got, err := resolveModelTarget(cfg, "openai", "anthropic", "fast")
	if err != nil || got.ProviderID != "anthropic" || got.Model != "claude" {
		t.Fatalf("matching target = %+v, %v", got, err)
	}
}

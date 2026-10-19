package main

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/internal/app"
)

func TestModelAliasProviderFlagPairsIndependentOfOrder(t *testing.T) {
	cfg, err := parseFlags([]string{"--model-alias-provider", "fast=anthropic", "--model-alias", "fast=opaque/id"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.modelAliases.AsMap()["fast"] != "opaque/id" || cfg.modelAliasProviders.AsMap()["fast"] != "anthropic" {
		t.Fatalf("alias pair not retained: models=%v providers=%v", cfg.modelAliases.AsMap(), cfg.modelAliasProviders.AsMap())
	}
}

func TestModelAliasProviderPairReachesEmbeddedConfig(t *testing.T) {
	_, cfg, err := parseTransportFlags(modeLocal, nil, []string{"--model-alias-provider", "fast=mock", "--model-alias", "fast=opaque/id"})
	if err != nil {
		t.Fatal(err)
	}

	appCfg := embeddedConfig(cfg, nil)
	target, ok := appCfg.ModelAliasTargets["fast"]
	if !ok || target.ProviderID != "mock" || target.Model != "opaque/id" {
		t.Fatalf("ModelAliasTargets[fast] = %+v, %v; want mock/opaque/id", target, ok)
	}
	if _, ok := appCfg.ModelAliases["fast"]; ok {
		t.Fatalf("ModelAliases retained provider-paired alias: %v", appCfg.ModelAliases)
	}
}

func TestModelAliasProviderStartupValidationAndWholeTargetReplacement(t *testing.T) {
	_, unmatched, err := parseTransportFlags(modeLocal, nil, []string{"--model-alias-provider", "fast=mock"})
	if err != nil {
		t.Fatal(err)
	}
	unmatchedCfg := embeddedConfig(unmatched, nil)
	unmatchedCfg.Workspace, unmatchedCfg.UseMock, unmatchedCfg.NoSoul = t.TempDir(), true, true
	if built, err := app.Build(context.Background(), unmatchedCfg); err == nil {
		built.Close()
		t.Fatal("startup accepted unmatched --model-alias-provider")
	}

	_, replacement, err := parseTransportFlags(modeLocal, nil, []string{"--model-alias", "fast=cli-model"})
	if err != nil {
		t.Fatal(err)
	}
	target := embeddedConfig(replacement, nil).ModelAliasTargets["fast"]
	if target.ProviderID != "" || target.Model != "cli-model" {
		t.Fatalf("CLI scalar target = %+v, want contextual cli-model with no inherited provider", target)
	}
}

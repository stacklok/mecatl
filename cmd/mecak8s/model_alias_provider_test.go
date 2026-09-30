package main

import "testing"

func TestModelAliasProviderFlagPairsIndependentOfOrder(t *testing.T) {
	cfg, err := parseFlags([]string{"--model-alias-provider", "fast=anthropic", "--model-alias", "fast=opaque/id"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.modelAliases.AsMap()["fast"] != "opaque/id" || cfg.modelAliasProviders.AsMap()["fast"] != "anthropic" {
		t.Fatalf("alias pair not retained: models=%v providers=%v", cfg.modelAliases.AsMap(), cfg.modelAliasProviders.AsMap())
	}
}

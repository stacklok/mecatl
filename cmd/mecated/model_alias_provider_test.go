package main

import "testing"

func TestModelAliasProviderPairReachesAppConfig(t *testing.T) {
	cfg, err := parseFlags([]string{"--model-alias-provider", "fast=mock", "--model-alias", "fast=opaque/id"})
	if err != nil {
		t.Fatal(err)
	}

	appCfg := appConfig(cfg, nil, nil, nil, nil, nil)
	target, ok := appCfg.ModelAliasTargets["fast"]
	if !ok || target.ProviderID != "mock" || target.Model != "opaque/id" {
		t.Fatalf("ModelAliasTargets[fast] = %+v, %v; want mock/opaque/id", target, ok)
	}
	if _, ok := appCfg.ModelAliases["fast"]; ok {
		t.Fatalf("ModelAliases retained provider-paired alias: %v", appCfg.ModelAliases)
	}
}

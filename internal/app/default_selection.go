package app

import (
	"context"
	"fmt"
	"strings"
)

func applyDeploymentDefaultTarget(cfg Config) (Config, error) {
	selector := strings.TrimSpace(cfg.DefaultModel)
	if selector == "" {
		return cfg, nil
	}
	target, known := lookupModelAliasTarget(cfg, selector)
	if !known {
		return cfg, nil
	}
	if strings.TrimSpace(target.Model) == "" {
		return cfg, fmt.Errorf("default model %q means inherit", selector)
	}
	if target.ProviderID != "" {
		if cfg.DefaultProvider != "" && strings.TrimSpace(cfg.DefaultProvider) != target.ProviderID {
			return cfg, fmt.Errorf("default model alias provider conflicts with default provider")
		}
		cfg.DefaultProvider = target.ProviderID
		cfg.defaultModelFromAlias = true
	}
	cfg.DefaultModel = strings.TrimSpace(target.Model)
	return cfg, nil
}

// ResolveDeploymentDefault validates a persisted deployment default's provider with
// the same registry and default-model resolver used at startup, without contacting
// a provider. A supplied model is validated as an alias or concrete model selector,
// but not against an offline catalog: its availability is provider-runtime truth.
// It returns the concrete provider/model pair selected by startup.
func ResolveDeploymentDefault(ctx context.Context, cfg Config) (string, string, error) {
	cfg.DefaultProvider = strings.TrimSpace(cfg.DefaultProvider)
	cfg.DefaultModel = strings.TrimSpace(cfg.DefaultModel)
	var err error
	cfg, err = applyDeploymentDefaultTarget(cfg)
	if err != nil {
		return "", "", err
	}
	cfg.skipProviderNetworkDiscovery = true
	reg, err := buildProviderRegistryContext(ctx, cfg, cfg.envDetector)
	if err != nil {
		return "", "", err
	}
	if err := validateDefaultProvider(cfg, reg); err != nil {
		return "", "", err
	}
	model := reg.ResolvedDefaultModel()
	if model == "" {
		return "", "", fmt.Errorf("default provider %q has no offline-known default model; specify MODEL", reg.Default())
	}
	return reg.Default(), model, nil
}

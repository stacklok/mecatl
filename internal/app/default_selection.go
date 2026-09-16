package app

import (
	"context"
	"fmt"
	"strings"
)

// ResolveDeploymentDefault validates a persisted deployment default's provider with
// the same registry and default-model resolver used at startup, without contacting
// a provider. A supplied model is validated as an alias or concrete model selector,
// but not against an offline catalog: its availability is provider-runtime truth.
// It returns the concrete provider/model pair selected by startup.
func ResolveDeploymentDefault(ctx context.Context, cfg Config) (string, string, error) {
	cfg.DefaultProvider = strings.TrimSpace(cfg.DefaultProvider)
	cfg.DefaultModel = strings.TrimSpace(cfg.DefaultModel)
	if cfg.DefaultModel != "" {
		model, known := lookupModelAlias(cfg, cfg.DefaultModel)
		if !known || model == "" {
			return "", "", fmt.Errorf("default model %q is unknown or means inherit", cfg.DefaultModel)
		}
		cfg.DefaultModel = model
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

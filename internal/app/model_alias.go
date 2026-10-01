package app

import (
	"fmt"
	"strings"
)

const reservedModelRouterProvider = "model-router"

// ModelTarget is a composition-owned alias target. Empty ProviderID means the
// model is relative to the consumer's contextual provider.
type ModelTarget struct {
	ProviderID string
	Model      string
}

// ModelAliases is the typed operator alias map retained inside composition.
type ModelAliases map[string]ModelTarget

func lookupModelAliasTarget(cfg Config, selector string) (ModelTarget, bool) {
	selector = strings.TrimSpace(selector)
	if target, ok := cfg.ModelAliasTargets[selector]; ok {
		return target, true
	}
	if model, ok := cfg.ModelAliases[selector]; ok {
		return ModelTarget{Model: strings.TrimSpace(model)}, true
	}
	if model, ok := builtinModelAliases[selector]; ok {
		return ModelTarget{Model: model}, true
	}
	if strings.ContainsAny(selector, "/:.-") {
		return ModelTarget{Model: selector}, true
	}
	return ModelTarget{}, false
}

func validateModelAliases(aliases ModelAliases, reg *providerRegistry) error {
	for name, target := range aliases {
		name = strings.TrimSpace(name)
		providerID := strings.TrimSpace(target.ProviderID)
		model := strings.TrimSpace(target.Model)
		if name == "" || model == "" {
			return fmt.Errorf("model alias configuration is invalid: alias names and models must be non-empty")
		}
		if providerID == "" {
			continue
		}
		if providerID == reservedModelRouterProvider {
			return fmt.Errorf("model alias %q uses reserved provider %q", name, reservedModelRouterProvider)
		}
		if _, ok := reg.Lookup(providerID); !ok {
			return fmt.Errorf("model alias %q names an unknown or unavailable provider", name)
		}
	}
	return nil
}

func resolveConfiguredModelTarget(cfg Config, contextualProvider, selector string) (ModelTarget, error) {
	if cfg.modelBindingProvider != "" {
		contextualProvider = cfg.modelBindingProvider
	}
	return resolveModelTarget(cfg, contextualProvider, "", selector)
}

func resolveModelTarget(cfg Config, contextualProvider, explicitProvider, selector string) (ModelTarget, error) {
	target, known := lookupModelAliasTarget(cfg, selector)
	if !known || strings.TrimSpace(target.Model) == "" {
		return ModelTarget{}, fmt.Errorf("unknown model selector")
	}
	explicitProvider = strings.TrimSpace(explicitProvider)
	if target.ProviderID != "" {
		if explicitProvider != "" && explicitProvider != target.ProviderID {
			return ModelTarget{}, fmt.Errorf("model selector provider conflicts with alias provider")
		}
		return target, nil
	}
	if explicitProvider != "" {
		target.ProviderID = explicitProvider
	} else {
		target.ProviderID = strings.TrimSpace(contextualProvider)
	}
	return target, nil
}

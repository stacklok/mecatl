package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

func (c providerCommands) runSetDefault(ctx context.Context, res invocationResolution, stdout, stderr io.Writer) error {
	if len(res.remaining) == 1 && isHelpMetaFlag(res.remaining[0]) {
		return providerHelpResult(stderr, providerActionSetDefault)
	}
	requestedModel := optionalProviderDefaultModel(res.remaining)
	provider, model, err := c.backend.resolveDefault(ctx, res.providerName, requestedModel)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return providerCredentialCancellation(stderr)
		}
		return fmt.Errorf("providers set-default: %w", err)
	}
	if requestedModel != "" {
		model = requestedModel
	}
	state, err := c.backend.updateDefaults(ctx, c.backend.settingsPath(), permconfig.DefaultUpdate{Provider: provider, Model: model})
	if state == authfile.CommitReplacementAppliedDurabilityUnknown {
		return fmt.Errorf("providers set-default: replacement_applied_durability_unknown; the default may already be active. Inspect `mecatui providers status` and settings before a manual retry: %w", err)
	}
	if err != nil {
		if state == authfile.CommitNotApplied && errors.Is(err, context.Canceled) {
			return providerCredentialCancellation(stderr)
		}
		return fmt.Errorf("providers set-default: save deployment default: %w", err)
	}
	if state == authfile.CommitNotApplied {
		return errors.New("providers set-default: deployment default was not saved")
	}
	_, err = fmt.Fprintf(stdout, "Embedded deployment default set to provider %q, model %q.\n", provider, model)
	return err
}

func optionalProviderDefaultModel(args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	return ""
}

func resolveProviderDefaultWithContext(ctx context.Context, provider, model string) (string, string, error) {
	inspection, err := inspectLocalProviders()
	if err != nil {
		return "", "", fmt.Errorf("inspect local providers: %w", err)
	}
	preservedModel := ""
	if model == "" && inspection.selectedProvider == provider {
		model = inspection.selectedModel
		preservedModel = model
	}
	nativeLoader := &cliconfig.NativeEndpointLoader{}
	defer func() { _ = nativeLoader.Close() }()
	cfg := app.Config{
		DefaultProvider:                provider,
		DefaultModel:                   model,
		ModelAliases:                   inspection.aliases,
		ProviderDefinitions:            inspection.definitions,
		CustomProviderAPIKeys:          customProviderAPIKeys(inspection.credentials, inspection.definitions),
		OpenAIKey:                      inspection.credentials.OpenAI,
		OpenRouterKey:                  inspection.credentials.OpenRouter,
		AnthropicKey:                   inspection.credentials.Anthropic,
		OpenCodeKey:                    inspection.credentials.OpenCode,
		OpenAICodexCredential:          inspection.credentials.OpenAICodex,
		NativeEndpointCredentialLoader: nativeLoader,
		ToolhiveLLM:                    true,
	}
	// A stored subscription sign-in is a provider path here exactly as it is at
	// startup. The registry registers openai-codex only from a sign-in or a
	// manual token, so without this a signed-in operator is refused the Codex
	// default with an API-key-only message. A store failure is reported and
	// resolution continues: an API-key default must not become unroutable
	// because the credential store is unreadable.
	if err := cliconfig.AttachSubscriptionCredentials(ctx, &cfg); err != nil {
		slog.Warn("subscription sign-in could not be loaded", "error", err)
	}
	resolvedProvider, resolvedModel, err := app.ResolveDeploymentDefault(ctx, cfg)
	if err == nil && preservedModel != "" {
		resolvedModel = preservedModel
	}
	return resolvedProvider, resolvedModel, err
}

func customProviderAPIKeys(credentials cliconfig.ResolvedCredentials, definitions permconfig.ProviderDefinitions) map[string]string {
	keys := make(map[string]string, len(definitions))
	for id, definition := range definitions {
		if definition.Auth.Method == providerAuthAPIKey {
			if key := credentials.CustomAPIKey(id); key != "" {
				keys[id] = key
			}
		}
	}
	return keys
}

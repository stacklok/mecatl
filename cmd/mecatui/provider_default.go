package main

import (
	"context"
	"errors"
	"fmt"
	"io"

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
		return fmt.Errorf("providers set-default: %w", err)
	}
	if requestedModel != "" {
		model = requestedModel
	}
	state, err := c.backend.updateDefaults(ctx, c.backend.settingsPath(), permconfig.DefaultUpdate{Provider: provider, Model: model})
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
	nativeLoader := &cliconfig.NativeEndpointLoader{}
	defer func() { _ = nativeLoader.Close() }()
	return app.ResolveDeploymentDefault(ctx, app.Config{
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
	})
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

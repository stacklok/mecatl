package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

var (
	updateProviderDefaults           = permconfig.UpdateDefaults
	resolveProviderDefaultForCommand = resolveProviderDefault
)

func runProviderSetDefaultCommand(res invocationResolution, stdout, stderr io.Writer) error {
	if len(res.remaining) == 1 && isHelpMetaFlag(res.remaining[0]) {
		_, err := fmt.Fprintln(stderr, "Usage: mecatui providers set-default PROVIDER [MODEL]")
		return errors.Join(flag.ErrHelp, err)
	}
	requestedModel := optionalProviderDefaultModel(res.remaining)
	provider, model, err := resolveProviderDefaultForCommand(res.llmEndpoint, requestedModel)
	if err != nil {
		return fmt.Errorf("providers set-default: %w", err)
	}
	if requestedModel != "" {
		model = requestedModel
	}
	state, err := updateProviderDefaults(context.Background(), providerSettingsPath(), permconfig.DefaultUpdate{Provider: provider, Model: model})
	if err != nil {
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

func resolveProviderDefault(provider, model string) (string, string, error) {
	inspection, err := inspectLocalProviders()
	if err != nil {
		return "", "", fmt.Errorf("inspect local providers: %w", err)
	}
	nativeLoader := &cliconfig.NativeEndpointLoader{}
	defer func() { _ = nativeLoader.Close() }()
	return app.ResolveDeploymentDefault(context.Background(), app.Config{
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
		if definition.Auth.Method == "api_key" {
			if key := credentials.CustomAPIKey(id); key != "" {
				keys[id] = key
			}
		}
	}
	return keys
}

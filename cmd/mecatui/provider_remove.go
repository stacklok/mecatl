package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

var readProviderRemovalConfirmation = readProviderRemovalConfirmationFromTerminal

func readProviderRemovalConfirmationFromTerminal(provider string) (bool, error) {
	if _, err := fmt.Fprintf(os.Stderr, "Remove provider definition and locally managed credentials for %q? Type 'remove' to confirm: ", provider); err != nil {
		return false, err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if errors.Is(err, io.EOF) && line == "" {
		return false, context.Canceled
	}
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(line) == "remove", nil
}

func runProviderRemoveCommand(res invocationResolution, stdout, stderr io.Writer) error {
	if len(res.remaining) == 1 && isHelpMetaFlag(res.remaining[0]) {
		return providerHelpResult(stderr, providerActionRemove)
	}
	cfg, err := loadProviderCredentialConfig()
	if err != nil {
		return fmt.Errorf("providers remove: load configured providers: %w", err)
	}
	definition, ok := cfg.definitions[res.llmEndpoint]
	if !ok {
		return fmt.Errorf("provider %q is not a configured custom provider; remove is unavailable", res.llmEndpoint)
	}
	confirmed, err := readProviderRemovalConfirmation(res.llmEndpoint)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return providerCredentialCancellation(stderr)
		}
		return fmt.Errorf("providers remove: confirm removal: %w", err)
	}
	if !confirmed {
		return providerCredentialCancellation(stderr)
	}

	credentialRemoved, err := removeProviderCredential(res.llmEndpoint, definition, cfg.authPath, stderr)
	if err != nil {
		return err
	}
	state, err := updateProviderMap(context.Background(), providerSettingsPath(), permconfig.ProviderMapUpdate{Provider: res.llmEndpoint})
	if err != nil {
		if credentialRemoved {
			return fmt.Errorf("providers remove: locally managed credentials for %q were removed, but provider definition remains: %w", res.llmEndpoint, err)
		}
		return fmt.Errorf("providers remove: provider definition for %q remains: %w", res.llmEndpoint, err)
	}
	if state == authfile.CommitNotApplied {
		if credentialRemoved {
			return fmt.Errorf("providers remove: locally managed credentials for %q were removed, but provider definition was not removed", res.llmEndpoint)
		}
		return fmt.Errorf("providers remove: provider definition for %q was not removed", res.llmEndpoint)
	}
	if credentialRemoved {
		_, err = fmt.Fprintf(stdout, "Removed custom provider %q and its locally managed credentials.\n", res.llmEndpoint)
	} else {
		_, err = fmt.Fprintf(stdout, "Removed custom provider %q.\n", res.llmEndpoint)
	}
	return err
}

func removeProviderCredential(provider string, definition permconfig.ProviderDefinition, authPath string, stderr io.Writer) (bool, error) {
	switch definition.Auth.Method {
	case "api_key":
		if _, err := updateProviderAPIKey(context.Background(), authPath, authfile.APIKeyUpdate{Provider: provider}); err != nil {
			return false, fmt.Errorf("providers remove: provider definition for %q remains; locally managed API key may remain: %w", provider, err)
		}
		return true, nil
	case "oidc":
		ctx, cancel := newNativeLLMEnrollmentContext()
		defer cancel()
		runtime, err := openProviderOIDCRuntime(ctx, definition, false, stderr)
		if err == nil {
			defer func() { _ = runtime.Close() }()
			err = runtime.Logout(ctx)
		}
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return false, providerCredentialCancellation(stderr)
			}
			return false, fmt.Errorf("providers remove: provider definition for %q remains; locally managed OIDC credentials may remain: %w", provider, err)
		}
		return true, nil
	case providerAuthNone:
		return false, nil
	default:
		return false, fmt.Errorf("providers remove: provider definition for %q remains; unsupported auth.method %q", provider, definition.Auth.Method)
	}
}

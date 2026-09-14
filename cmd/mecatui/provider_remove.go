package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func readProviderRemovalConfirmationFromTerminal(ctx context.Context, provider string) (bool, error) {
	line, err := readProviderFieldFromTerminal(ctx, fmt.Sprintf("Remove provider definition and locally managed credentials for %q? Type 'remove' to confirm", provider))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(line) == "remove", nil
}

func (c providerCommands) runRemove(ctx context.Context, res invocationResolution, stdout, stderr io.Writer) error {
	if len(res.remaining) == 1 && isHelpMetaFlag(res.remaining[0]) {
		return providerHelpResult(stderr, providerActionRemove)
	}
	cfg, err := c.backend.loadCredentials()
	if err != nil {
		return fmt.Errorf("providers remove: load configured providers: %w", err)
	}
	definition, ok := cfg.definitions[res.providerName]
	if !ok {
		return fmt.Errorf("provider %q is not a configured custom provider; remove is unavailable", res.providerName)
	}
	confirmed, err := c.terminal.readRemoval(ctx, res.providerName)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return providerCredentialCancellation(stderr)
		}
		return fmt.Errorf("providers remove: confirm removal: %w", err)
	}
	if !confirmed {
		return providerCredentialCancellation(stderr)
	}

	credentialRemoved, err := c.removeCredential(ctx, res.providerName, definition, cfg.authPath, stderr)
	if err != nil {
		return err
	}
	state, err := c.backend.updateProviderMap(ctx, c.backend.settingsPath(), permconfig.ProviderMapUpdate{
		Provider:           res.providerName,
		ExpectedDefinition: &definition,
	})
	if err != nil {
		if credentialRemoved {
			return fmt.Errorf("providers remove: locally managed credentials for %q were removed, but provider definition remains: %w", res.providerName, err)
		}
		return fmt.Errorf("providers remove: provider definition for %q remains: %w", res.providerName, err)
	}
	if state == authfile.CommitNotApplied {
		if credentialRemoved {
			return fmt.Errorf("providers remove: locally managed credentials for %q were removed, but provider definition was not removed", res.providerName)
		}
		return fmt.Errorf("providers remove: provider definition for %q was not removed", res.providerName)
	}
	if credentialRemoved {
		_, err = fmt.Fprintf(stdout, "Removed custom provider %q and its locally managed credentials.\n", res.providerName)
	} else {
		_, err = fmt.Fprintf(stdout, "Removed custom provider %q.\n", res.providerName)
	}
	return err
}

func (c providerCommands) removeCredential(ctx context.Context, provider string, definition permconfig.ProviderDefinition, authPath string, stderr io.Writer) (bool, error) {
	switch definition.Auth.Method {
	case "api_key":
		state, err := c.backend.updateAPIKey(ctx, authPath, authfile.APIKeyUpdate{Provider: provider})
		if err != nil {
			return false, fmt.Errorf("providers remove: provider definition for %q remains; locally managed API key may remain: %w", provider, err)
		}
		return state == authfile.CommitDurable, nil
	case providerAuthOIDC:
		lifecycleCtx, cancel := context.WithTimeout(ctx, nativeLLMEnrollmentTimeout)
		defer cancel()
		runtime, err := c.backend.openOIDCRuntime(lifecycleCtx, definition, false, stderr)
		if err == nil {
			defer func() { _ = runtime.Close() }()
			err = runtime.Logout(lifecycleCtx)
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

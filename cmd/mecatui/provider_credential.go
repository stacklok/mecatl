package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

type providerCredentialConfig struct {
	definitions permconfig.ProviderDefinitions
	authPath    string
}

func currentProviderCredentialConfig() (providerCredentialConfig, error) {
	resolver := permconfig.NewWithEnv(permconfig.Options{Conventional: true}, xdgconfig.OSEnv)
	definitions, _, err := resolver.OperatorProviders()
	if err != nil {
		return providerCredentialConfig{}, err
	}
	path := authfile.DefaultPath(xdgconfig.OSEnv)
	if store := resolver.OperatorCredentialStore(); store != nil && store.APIKey != nil {
		path = store.APIKey.File
	}
	return providerCredentialConfig{definitions: definitions, authPath: path}, nil
}

func readHiddenProviderAPIKey(ctx context.Context, provider string) (string, error) {
	return readProviderTerminalLine(ctx, os.Stdin, os.Stderr, "API key for "+providerDisplay(provider), true)
}

var (
	errProviderCredentialCancelled = errors.New("provider credential prompt cancelled")
	errProviderSaveDeclined        = errors.New("provider API key save declined")
	errProviderInputTooLong        = errors.New("provider input exceeds the 8 KiB acceptance limit")
)

func (c providerCommands) runCredential(ctx context.Context, res invocationResolution, stdout, stderr io.Writer) (err error) {
	defer func() {
		if errors.Is(err, errProviderSaveDeclined) {
			err = nil
		}
	}()
	if len(res.remaining) == 1 && isHelpMetaFlag(res.remaining[0]) {
		return providerHelpResult(stderr, res.providerAction)
	}
	if res.providerName == toolHiveEndpointID {
		return c.runToolHiveCredential(ctx, res, stderr)
	}
	cfg, err := c.backend.loadCredentials()
	if err != nil {
		return fmt.Errorf("providers %s: load configured providers: %w", res.providerAction, err)
	}
	if isBuiltinAPIKeyProvider(res.providerName) {
		if len(res.remaining) != 0 {
			return errors.New("providers login: --no-browser is available only for auth.method oidc")
		}
		return c.runAPIKey(ctx, res, cfg.authPath, stdout, stderr)
	}
	definition, ok := cfg.definitions[res.providerName]
	if !ok {
		return fmt.Errorf("provider %q is not a configured custom provider; login and logout are unavailable", res.providerName)
	}
	switch definition.Auth.Method {
	case providerAuthAPIKey:
		if len(res.remaining) != 0 {
			return errors.New("providers login: --no-browser is available only for auth.method oidc")
		}
		return c.runAPIKey(ctx, res, cfg.authPath, stdout, stderr)
	case providerAuthOIDC:
		return c.runOIDC(ctx, res, definition, stdout, stderr)
	default:
		return fmt.Errorf("provider %q uses auth.method %q; login and logout require locally managed credentials", res.providerName, definition.Auth.Method)
	}
}

func isBuiltinAPIKeyProvider(provider string) bool {
	switch provider {
	case providerAnthropicID, providerOpenAIID, providerOpenCodeID, providerOpenRouterID:
		return true
	default:
		return false
	}
}

func (c providerCommands) runAPIKey(ctx context.Context, res invocationResolution, authPath string, stdout, stderr io.Writer) error {
	var key *string
	if res.providerAction == providerActionLogin {
		if err := writeProviderKeyGuidance(stderr, res.providerName); err != nil {
			return err
		}
		inspection, err := c.backend.inspect()
		if err != nil {
			return err
		}
		if strings.HasSuffix(inspection.sources[res.providerName], " (environment)") {
			if _, err := fmt.Fprintln(stderr, "Warning: the effective environment credential will still win over the saved file key; saving does not change that environment."); err != nil {
				return err
			}
		}
		entered, err := c.terminal.readAPIKey(ctx, res.providerName)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return c.credentialCancellation(stderr)
			}
			if errors.Is(err, errProviderInputTooLong) {
				return errProviderInputTooLong
			}
			return errors.New("providers login: could not read API key from the local terminal")
		}
		if err := validateProviderAPIKey(entered); err != nil {
			return err
		}
		save, err := c.confirmProviderAction(ctx, "Save this API key to the configured owner-only plaintext file? [y/N]")
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return c.credentialCancellation(stderr)
			}
			return errors.New("providers login: could not confirm saving; no API key saved")
		}
		if !save {
			if _, err := fmt.Fprintln(stdout, "API key not saved; no credential changes made."); err != nil {
				return err
			}
			return errProviderSaveDeclined
		}
		key = &entered
	}
	state, err := c.backend.updateAPIKey(ctx, authPath, authfile.APIKeyUpdate{Provider: res.providerName, APIKey: key})
	if err != nil {
		if state == authfile.CommitNotApplied && errors.Is(err, context.Canceled) {
			return c.credentialCancellation(stderr)
		}
		return fmt.Errorf("providers %s: update locally managed API key: %w", res.providerAction, err)
	}
	if res.providerAction == providerActionLogin {
		if state == authfile.CommitNoop {
			_, err = fmt.Fprintf(stdout, "API key for provider %q is already configured\n", res.providerName)
		} else {
			_, err = fmt.Fprintf(stdout, "API key saved for provider %q\n", res.providerName)
		}
		return err
	}
	if state == authfile.CommitNoop {
		_, err = fmt.Fprintf(stdout, "no locally managed API key for provider %q\n", res.providerName)
	} else {
		_, err = fmt.Fprintf(stdout, "removed locally managed API key for provider %q\n", res.providerName)
	}
	return err
}

func validateProviderAPIKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("providers login: API key cannot be empty")
	}
	if len(key) > 8*1024 {
		return errProviderInputTooLong
	}
	if !utf8.ValidString(key) || strings.ContainsFunc(key, func(r rune) bool { return !unicode.IsPrint(r) || isTrustControl(r) }) {
		return errors.New("providers login: API key contains unsupported control characters or invalid text")
	}
	return nil
}

func (c providerCommands) runOIDC(ctx context.Context, res invocationResolution, definition permconfig.ProviderDefinition, stdout, stderr io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, nativeLLMEnrollmentTimeout)
	defer cancel()
	noBrowser := len(res.remaining) == 1 && res.remaining[0] == "--no-browser"
	if res.providerAction == providerActionLogin && c.backend.prepareOIDCRoot != nil {
		if definition.Auth.OIDC == nil || definition.Auth.OIDC.CredentialStore == nil {
			return providerOIDCLifecycleError(res.providerAction, res.providerName, errors.New("OIDC credential store is not configured"))
		}
		if err := c.backend.prepareOIDCRoot(definition.Auth.OIDC.CredentialStore.Home); err != nil {
			return providerOIDCLifecycleError(res.providerAction, res.providerName, err)
		}
	}
	runtime, err := c.backend.openOIDCRuntime(ctx, definition, noBrowser, stderr)
	if err == nil {
		defer func() { _ = runtime.Close() }()
		if res.providerAction == providerActionLogin {
			err = runtime.Login(ctx)
		} else {
			err = runtime.Logout(ctx)
		}
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return c.credentialCancellation(stderr)
		}
		return providerOIDCLifecycleError(res.providerAction, res.providerName, err)
	}
	if res.providerAction == providerActionLogin {
		_, err = fmt.Fprintf(stdout, "OIDC login successful for provider %q\n", res.providerName)
	} else {
		_, err = fmt.Fprintf(stdout, "removed locally managed OIDC credentials for provider %q\n", res.providerName)
	}
	return err
}

func writeProviderKeyGuidance(out io.Writer, provider string) error {
	guidance := "Custom provider: obtain an API key from your operator or service documentation; use the configured transport, not a guessed console."
	switch provider {
	case providerAnthropicID:
		guidance = "Anthropic API: create a developer key at https://console.anthropic.com/settings/keys . Claude consumer subscriptions do not include API usage."
	case providerOpenAIID:
		guidance = "OpenAI API key (not the manual OpenAI Codex subscription token): https://platform.openai.com/api-keys . ChatGPT consumer subscriptions do not include developer API usage."
	case providerOpenRouterID:
		guidance = "OpenRouter API: create a key at https://openrouter.ai/settings/keys and arrange API credits/billing. Consumer chat subscriptions do not fund this API."
	case providerOpenCodeID:
		guidance = "OpenCode Go API: obtain a key with an active Go subscription at https://opencode.ai/go . Go is not interchangeable with a Zen key, subscription, or endpoint; unrelated consumer subscriptions do not grant Go API access."
	}
	_, err := fmt.Fprintln(out, guidance+"\nAPI use may incur charges; check the service's billing terms. No browser is opened.\nSaving is optional and requires separate consent. The configured credential_store.api_key.file is owner-only plaintext, readable by same-UID processes, including permitted agent Shell commands. Never paste a key into a command argument.")
	return err
}

func (c providerCommands) confirmProviderAction(ctx context.Context, prompt string) (bool, error) {
	value, err := c.terminal.readField(ctx, prompt)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(value), "y") || strings.EqualFold(strings.TrimSpace(value), "yes"), nil
}

func (c providerCommands) credentialCancellation(stderr io.Writer) error {
	if c.deferCredentialCancellation {
		return errProviderCredentialCancelled
	}
	return providerCredentialCancellation(stderr)
}

func providerCredentialCancellation(stderr io.Writer) error {
	_, writeErr := fmt.Fprintln(stderr, "Cancelled; no changes made.")
	if writeErr != nil {
		return writeErr
	}
	return errProviderCredentialCancelled
}

func (c providerCommands) runToolHiveCredential(ctx context.Context, res invocationResolution, stderr io.Writer) error {
	if res.providerAction != providerActionLogin {
		return errors.New("ToolHive owns this provider lifecycle; use `thv llm` tooling")
	}
	ctx, cancel := context.WithTimeout(ctx, nativeLLMEnrollmentTimeout)
	defer cancel()
	if err := c.backend.toolHiveLogin(ctx, len(res.remaining) == 1 && res.remaining[0] == "--no-browser"); err != nil {
		if errors.Is(err, context.Canceled) {
			return c.credentialCancellation(stderr)
		}
		return errors.New("ToolHive LLM gateway login failed")
	}
	_, err := fmt.Fprintln(stderr, "ToolHive LLM gateway login successful")
	return err
}

func providerOIDCLifecycleError(action, provider string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("provider OIDC lifecycle timed out; retry and complete the browser callback within five minutes")
	}
	return fmt.Errorf("provider %s OIDC lifecycle failed; retry or run `mecatui providers status %s` for local state", action, provider)
}

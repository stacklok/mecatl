package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

type providerCredentialConfig struct {
	definitions permconfig.ProviderDefinitions
	authPath    string
}

var (
	loadProviderCredentialConfig = currentProviderCredentialConfig
	readProviderAPIKey           = readHiddenProviderAPIKey
	updateProviderAPIKey         = authfile.UpdateAPIKey
	openProviderOIDCRuntime      = func(ctx context.Context, definition permconfig.ProviderDefinition, noBrowser bool, urlWriter io.Writer) (nativeEndpointRuntime, error) {
		return openNativeEndpointRuntime(ctx, definition, noBrowser, urlWriter)
	}
)

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

func readHiddenProviderAPIKey(provider string) (string, error) {
	if _, err := fmt.Fprintf(os.Stderr, "API key for %s: ", provider); err != nil {
		return "", err
	}
	key, err := term.ReadPassword(int(os.Stdin.Fd()))
	_, _ = fmt.Fprintln(os.Stderr)
	return string(key), err
}

var errProviderCredentialCancelled = errors.New("provider credential prompt cancelled")

func runProviderCredentialCommand(res invocationResolution, stdout, stderr io.Writer) error {
	if len(res.remaining) == 1 && isHelpMetaFlag(res.remaining[0]) {
		_, err := fmt.Fprintln(stderr, "Usage: mecatui providers login PROVIDER [--no-browser] | mecatui providers logout PROVIDER")
		return errors.Join(flag.ErrHelp, err)
	}
	if res.llmEndpoint == toolHiveEndpointID {
		return runToolHiveProviderCredentialCommand(res, stderr)
	}
	cfg, err := loadProviderCredentialConfig()
	if err != nil {
		return fmt.Errorf("providers %s: load configured providers: %w", res.llmAction, err)
	}
	if isBuiltinAPIKeyProvider(res.llmEndpoint) {
		return runProviderAPIKeyCommand(res, cfg.authPath, stdout, stderr)
	}
	definition, ok := cfg.definitions[res.llmEndpoint]
	if !ok {
		return fmt.Errorf("provider %q is not a configured custom provider; login and logout are unavailable", res.llmEndpoint)
	}
	switch definition.Auth.Method {
	case "api_key":
		if len(res.remaining) != 0 {
			return errors.New("providers login: --no-browser is available only for auth.method oidc")
		}
		return runProviderAPIKeyCommand(res, cfg.authPath, stdout, stderr)
	case "oidc":
		return runProviderOIDCCommand(res, definition, stdout, stderr)
	default:
		return fmt.Errorf("provider %q uses auth.method %q; login and logout require locally managed credentials", res.llmEndpoint, definition.Auth.Method)
	}
}

func isBuiltinAPIKeyProvider(provider string) bool {
	switch provider {
	case "anthropic", "openai", "opencode", "openrouter":
		return true
	default:
		return false
	}
}

func runProviderAPIKeyCommand(res invocationResolution, authPath string, stdout, stderr io.Writer) error {
	var key *string
	if res.llmAction == providerActionLogin {
		entered, err := readProviderAPIKey(res.llmEndpoint)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return providerCredentialCancellation(stderr)
			}
			return fmt.Errorf("providers login: read API key: %w", err)
		}
		if strings.TrimSpace(entered) == "" {
			return errors.New("providers login: API key cannot be empty")
		}
		key = &entered
	}
	state, err := updateProviderAPIKey(context.Background(), authPath, authfile.APIKeyUpdate{Provider: res.llmEndpoint, APIKey: key})
	if err != nil {
		return fmt.Errorf("providers %s: update locally managed API key: %w", res.llmAction, err)
	}
	if res.llmAction == providerActionLogin {
		if state == authfile.CommitNoop {
			_, err = fmt.Fprintf(stdout, "API key for provider %q is already configured\n", res.llmEndpoint)
		} else {
			_, err = fmt.Fprintf(stdout, "API key saved for provider %q\n", res.llmEndpoint)
		}
		return err
	}
	if state == authfile.CommitNoop {
		_, err = fmt.Fprintf(stdout, "no locally managed API key for provider %q\n", res.llmEndpoint)
	} else {
		_, err = fmt.Fprintf(stdout, "removed locally managed API key for provider %q\n", res.llmEndpoint)
	}
	return err
}

func runProviderOIDCCommand(res invocationResolution, definition permconfig.ProviderDefinition, stdout, stderr io.Writer) error {
	noBrowser := len(res.remaining) == 1 && res.remaining[0] == "--no-browser"
	ctx, cancel := newNativeLLMEnrollmentContext(nativeLLMEnrollmentTimeout)
	defer cancel()
	runtime, err := openProviderOIDCRuntime(ctx, definition, noBrowser, stderr)
	if err == nil {
		defer func() { _ = runtime.Close() }()
		if res.llmAction == providerActionLogin {
			err = runtime.Login(ctx)
		} else {
			err = runtime.Logout(ctx)
		}
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return providerCredentialCancellation(stderr)
		}
		return providerOIDCLifecycleError(res.llmAction, res.llmEndpoint, err)
	}
	if res.llmAction == providerActionLogin {
		_, err = fmt.Fprintf(stdout, "OIDC login successful for provider %q\n", res.llmEndpoint)
	} else {
		_, err = fmt.Fprintf(stdout, "removed locally managed OIDC credentials for provider %q\n", res.llmEndpoint)
	}
	return err
}

func providerCredentialCancellation(stderr io.Writer) error {
	_, writeErr := fmt.Fprintln(stderr, "Cancelled; no changes made.")
	if writeErr != nil {
		return writeErr
	}
	return errProviderCredentialCancelled
}

func runToolHiveProviderCredentialCommand(res invocationResolution, stderr io.Writer) error {
	if res.llmAction != providerActionLogin {
		return errors.New("ToolHive owns this provider lifecycle; use `thv llm` tooling")
	}
	ctx, cancel := newNativeLLMEnrollmentContext(nativeLLMEnrollmentTimeout)
	defer cancel()
	if err := executeToolHiveLogin(ctx, len(res.remaining) == 1 && res.remaining[0] == "--no-browser"); err != nil {
		if errors.Is(err, context.Canceled) {
			return providerCredentialCancellation(stderr)
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

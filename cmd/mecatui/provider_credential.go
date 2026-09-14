package main

import (
	"context"
	"errors"
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
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := fmt.Fprintf(os.Stderr, "API key for %s: ", provider); err != nil {
		return "", err
	}
	result := make(chan struct {
		key []byte
		err error
	})
	go func() {
		key, err := term.ReadPassword(int(os.Stdin.Fd()))
		got := struct {
			key []byte
			err error
		}{key, err}
		select {
		case result <- got:
		case <-ctx.Done():
			clear(key)
		}
	}()
	select {
	case <-ctx.Done():
		_, _ = fmt.Fprintln(os.Stderr)
		return "", ctx.Err()
	case got := <-result:
		_, _ = fmt.Fprintln(os.Stderr)
		key := string(got.key)
		clear(got.key)
		return key, got.err
	}
}

var errProviderCredentialCancelled = errors.New("provider credential prompt cancelled")

func (c providerCommands) runCredential(ctx context.Context, res invocationResolution, stdout, stderr io.Writer) error {
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
	case "anthropic", "openai", "opencode", "openrouter":
		return true
	default:
		return false
	}
}

func (c providerCommands) runAPIKey(ctx context.Context, res invocationResolution, authPath string, stdout, stderr io.Writer) error {
	var key *string
	if res.providerAction == providerActionLogin {
		entered, err := c.terminal.readAPIKey(ctx, res.providerName)
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
	state, err := c.backend.updateAPIKey(ctx, authPath, authfile.APIKeyUpdate{Provider: res.providerName, APIKey: key})
	if err != nil {
		if state == authfile.CommitNotApplied && errors.Is(err, context.Canceled) {
			return providerCredentialCancellation(stderr)
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
			return providerCredentialCancellation(stderr)
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

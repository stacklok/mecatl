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
		_, err := fmt.Fprintln(stderr, "Usage: mecatui providers login PROVIDER | mecatui providers logout PROVIDER")
		return errors.Join(flag.ErrHelp, err)
	}
	cfg, err := loadProviderCredentialConfig()
	if err != nil {
		return fmt.Errorf("providers %s: load configured providers: %w", res.llmAction, err)
	}
	definition, ok := cfg.definitions[res.llmEndpoint]
	if !ok {
		return fmt.Errorf("provider %q is not a configured custom provider; API-key login and logout are unavailable", res.llmEndpoint)
	}
	if definition.Auth.Method != "api_key" {
		return fmt.Errorf("provider %q uses auth.method %q; API-key login and logout are available only for auth.method api_key", res.llmEndpoint, definition.Auth.Method)
	}

	var key *string
	if res.llmAction == providerActionLogin {
		entered, err := readProviderAPIKey(res.llmEndpoint)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				_, writeErr := fmt.Fprintln(stderr, "Cancelled; no changes made.")
				if writeErr != nil {
					return writeErr
				}
				return errProviderCredentialCancelled
			}
			return fmt.Errorf("providers login: read API key: %w", err)
		}
		if strings.TrimSpace(entered) == "" {
			return errors.New("providers login: API key cannot be empty")
		}
		key = &entered
	}
	state, err := updateProviderAPIKey(context.Background(), cfg.authPath, authfile.APIKeyUpdate{Provider: res.llmEndpoint, APIKey: key})
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

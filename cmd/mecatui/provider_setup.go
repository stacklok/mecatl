package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const customProviderSetupChoice = "custom"

var readProviderSetupField = readProviderAddField

func runProviderSetupCommand(res invocationResolution, stdout, stderr io.Writer) error {
	if len(res.remaining) == 1 && isHelpMetaFlag(res.remaining[0]) {
		_, err := fmt.Fprintln(stderr, "Usage: mecatui providers setup [PROVIDER]\n\nChoose a provider interactively, or name a configured provider to log in. An unknown name starts custom provider setup.")
		return errors.Join(flag.ErrHelp, err)
	}

	provider := res.llmEndpoint
	if provider == "" {
		var err error
		provider, err = chooseProviderForSetup(stdout)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return providerCredentialCancellation(stderr)
			}
			return fmt.Errorf("providers setup: select provider: %w", err)
		}
		if provider == customProviderSetupChoice {
			provider, err = readProviderSetupField("Custom provider name")
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return providerCredentialCancellation(stderr)
				}
				return fmt.Errorf("providers setup: read provider name: %w", err)
			}
			if provider == "" {
				return errors.New("providers setup: custom provider name cannot be empty")
			}
		}
	}
	return runNamedProviderSetup(provider, stdout, stderr)
}

func chooseProviderForSetup(out io.Writer) (string, error) {
	statuses, err := loadProviderStatuses()
	if err != nil {
		return "", err
	}
	if _, err := fmt.Fprintln(out, "Provider setup\n\nChoose a provider:"); err != nil {
		return "", err
	}
	for i, status := range statuses {
		if _, err := fmt.Fprintf(out, "  %d. %s (%s)\n", i+1, status.Name, providerSetupCapability(status)); err != nil {
			return "", err
		}
	}
	if _, err := fmt.Fprintf(out, "  %d. custom (custom provider: API key, OIDC, or no authentication)\n", len(statuses)+1); err != nil {
		return "", err
	}
	selected, err := readProviderSetupField("Selection")
	if err != nil {
		return "", err
	}
	choice, err := strconv.Atoi(strings.TrimSpace(selected))
	if err != nil || choice < 1 || choice > len(statuses)+1 {
		return "", errors.New("enter a listed provider number")
	}
	if choice == len(statuses)+1 {
		return customProviderSetupChoice, nil
	}
	return statuses[choice-1].Name, nil
}

func providerSetupCapability(status providerStatus) string {
	switch status.Name {
	case toolHiveEndpointID:
		return "external lifecycle"
	case "openai-codex":
		return "manual credential"
	}
	if status.Class == "custom" {
		return "custom " + status.Auth
	}
	return "API key"
}

func runNamedProviderSetup(provider string, stdout, stderr io.Writer) error {
	statuses, err := loadProviderStatuses()
	if err != nil {
		return err
	}
	for _, status := range statuses {
		if status.Name != provider {
			continue
		}
		if provider == "openai-codex" {
			return errors.New("providers setup: openai-codex uses a manually managed credential; run `mecatui providers status openai-codex` for local state")
		}
		return runProviderCredentialCommand(invocationResolution{mode: modeProviderCredential, llmAction: providerActionLogin, llmEndpoint: provider}, stdout, stderr)
	}
	return runProviderAddCommand(invocationResolution{mode: modeProviderAdd, llmAction: providerActionAdd, llmEndpoint: provider}, stdout, stderr)
}

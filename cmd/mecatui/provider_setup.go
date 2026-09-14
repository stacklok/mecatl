package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

const customProviderSetupChoice = "custom"

func (c providerCommands) runSetup(ctx context.Context, res invocationResolution, stdout, stderr io.Writer) error {
	if len(res.remaining) == 1 && isHelpMetaFlag(res.remaining[0]) {
		return providerHelpResult(stderr, providerActionSetup)
	}

	provider := res.providerName
	if provider == "" {
		var err error
		provider, err = c.chooseForSetup(ctx, stdout)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return providerCredentialCancellation(stderr)
			}
			return fmt.Errorf("providers setup: select provider: %w", err)
		}
		if provider == customProviderSetupChoice {
			provider, err = c.terminal.readField(ctx, "Custom provider name")
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
	return c.runNamedSetup(ctx, provider, stdout, stderr)
}

func (c providerCommands) chooseForSetup(ctx context.Context, out io.Writer) (string, error) {
	inspection, err := c.backend.inspect()
	if err != nil {
		return "", err
	}
	statuses := c.statuses(ctx, inspection, true)
	statuses = providerSetupCandidates(statuses)
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
	selected, err := c.terminal.readField(ctx, "Selection")
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

func providerSetupCandidates(statuses []providerStatus) []providerStatus {
	candidates := slices.DeleteFunc(slices.Clone(statuses), func(status providerStatus) bool {
		if status.Name == openAICodexEndpointID {
			return true
		}
		return status.Class == "custom" && status.AuthMethod == providerAuthNone
	})
	slices.SortFunc(candidates, func(a, b providerStatus) int { return strings.Compare(a.Name, b.Name) })
	return candidates
}

func providerSetupCapability(status providerStatus) string {
	switch status.Name {
	case toolHiveEndpointID:
		return "external lifecycle"
	case openAICodexEndpointID:
		return "manual credential"
	}
	if status.Class == "custom" {
		if status.AuthMethod == providerAuthOIDC {
			return "custom OIDC"
		}
		return "custom API key"
	}
	return "API key"
}

func (c providerCommands) runNamedSetup(ctx context.Context, provider string, stdout, stderr io.Writer) error {
	inspection, err := c.backend.inspect()
	if err != nil {
		return err
	}
	for _, status := range c.statuses(ctx, inspection, true) {
		if status.Name != provider {
			continue
		}
		if provider == openAICodexEndpointID {
			return errors.New("providers setup: openai-codex uses a manually managed credential; run `mecatui providers status openai-codex` for local state")
		}
		return c.runCredential(ctx, invocationResolution{mode: modeProviderCredential, providerAction: providerActionLogin, providerName: provider}, stdout, stderr)
	}
	return c.runAdd(ctx, invocationResolution{mode: modeProviderAdd, providerAction: providerActionAdd, providerName: provider}, stdout, stderr)
}

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
	inspection, err := c.inspectForEnrollment()
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
			return !status.Configured
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
	inspection, err := c.inspectForEnrollment()
	if err != nil {
		return err
	}
	for _, status := range c.statuses(ctx, inspection, true) {
		if status.Name != provider {
			continue
		}
		if provider == openAICodexEndpointID {
			if !status.Configured {
				return writeProviderStatus(stdout, status)
			}
			if _, err := fmt.Fprintln(stdout, "Reusing the locally usable manual OpenAI Codex subscription token; no credential writes or entitlement checks."); err != nil {
				return err
			}
			return c.offerSetupDefault(ctx, provider, "", stdout, stderr)
		}
		if status.AuthMethod == providerAuthNone {
			return c.offerSetupDefault(ctx, provider, "", stdout, stderr)
		}
		if status.AuthMethod != providerAuthAPIKey {
			if err := c.runCredential(ctx, invocationResolution{mode: modeProviderCredential, providerAction: providerActionLogin, providerName: provider}, stdout, stderr); err != nil {
				return err
			}
			if status.AuthMethod == providerAuthOIDC {
				return c.offerSetupDefault(ctx, provider, "Completed OIDC enrollment remains saved", stdout, stderr)
			}
			return nil
		}
		if status.Configured {
			reuse, err := c.confirmProviderAction(ctx, "Reuse the effective credential from "+providerDisplay(status.Source)+"? [y/N; no replaces it]")
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return providerCredentialCancellation(stderr)
				}
				return err
			}
			if reuse {
				return c.offerSetupDefault(ctx, provider, "", stdout, stderr)
			}
		}
		cfg, err := c.backend.loadCredentials()
		if err != nil {
			return err
		}
		err = c.runAPIKey(ctx, invocationResolution{providerAction: providerActionLogin, providerName: provider}, cfg.authPath, stdout, stderr)
		if errors.Is(err, errProviderSaveDeclined) {
			return nil
		}
		if err != nil {
			return err
		}
		return c.offerSetupDefault(ctx, provider, "API key remains saved", stdout, stderr)
	}
	return c.setupCustomProvider(ctx, provider, stdout, stderr)
}

func (c providerCommands) setupCustomProvider(ctx context.Context, provider string, stdout, stderr io.Writer) error {
	if err := c.runAdd(ctx, invocationResolution{mode: modeProviderAdd, providerAction: providerActionAdd, providerName: provider}, stdout, stderr); err != nil {
		return err
	}
	inspection, err := c.inspectForEnrollment()
	if err != nil {
		return fmt.Errorf("provider definition was saved; inspect before continuing: %w", err)
	}
	if _, exists := inspection.definitions[provider]; !exists {
		return errors.New("provider definition is no longer present; credentials may remain saved. Inspect `mecatui providers status` and settings before retrying")
	}
	for _, status := range c.statuses(ctx, inspection, true) {
		if status.Name == provider && (status.Configured || status.AuthMethod == providerAuthNone) {
			return c.offerSetupDefault(ctx, provider, "Provider definition and any completed credential enrollment remain saved", stdout, stderr)
		}
	}
	return nil
}

func (c providerCommands) offerSetupDefault(ctx context.Context, provider, committed string, stdout, stderr io.Writer) error {
	// Capture downstream cancellation output: the default action cannot claim
	// whole-command rollback after the independently committed key operation.
	var defaultErr strings.Builder
	err := c.setupDefault(ctx, provider, stdout, &defaultErr)
	if committed != "" && errors.Is(err, errProviderCredentialCancelled) {
		if _, writeErr := fmt.Fprintln(stderr, "Cancelled; "+committed+". Deployment default was not changed."); writeErr != nil {
			return writeErr
		}
		return errProviderCredentialCancelled
	}
	if _, writeErr := io.WriteString(stderr, defaultErr.String()); writeErr != nil {
		return writeErr
	}
	if committed != "" && err != nil {
		return fmt.Errorf("%s; default selection failed: %w", committed, err)
	}
	return err
}

func (c providerCommands) setupDefault(ctx context.Context, provider string, stdout, stderr io.Writer) error {
	selectDefault, err := c.confirmProviderAction(ctx, "Set this provider as the embedded deployment default? [y/N]")
	if errors.Is(err, context.Canceled) {
		return providerCredentialCancellation(stderr)
	}
	if err != nil || !selectDefault {
		return err
	}
	model, err := c.terminal.readField(ctx, "Model selector (blank keeps the current selector or declared default; no network lookup)")
	if errors.Is(err, context.Canceled) {
		return providerCredentialCancellation(stderr)
	}
	if err != nil {
		return err
	}
	var args []string
	if model != "" {
		args = []string{model}
	}
	return c.runSetDefault(ctx, invocationResolution{providerName: provider, remaining: args}, stdout, stderr)
}

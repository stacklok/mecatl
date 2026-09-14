package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func setupCommands(t *testing.T, inspection providerInspection, input ...string) providerCommands {
	commands := testProviderCommands()
	commands.backend.inspect = providerInspectionLoader(inspection)
	commands.terminal.readField = providerInput(t, input...)
	return commands
}

func TestProviderSetupMenuSelectionAndCapabilityLabels(t *testing.T) {
	commands := setupCommands(t, providerInspection{definitions: permconfig.ProviderDefinitions{"corp": {ID: "corp", Auth: permconfig.ProviderAuth{Method: providerAuthAPIKey}}}}, "6")
	commands.backend.toolHiveAvailable = func() bool { return true }
	var output bytes.Buffer
	provider, err := commands.chooseForSetup(context.Background(), &output)
	if err != nil || provider != toolHiveEndpointID {
		t.Fatalf("selection = %q, %v", provider, err)
	}
	for _, want := range []string{"1. anthropic (API key)", "corp (custom API key)", "toolhive (external lifecycle)", "custom (custom provider"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("menu missing %q:\n%s", want, output.String())
		}
	}
}

func TestProviderSetupMenuUsesFullInventoryNotBareStatus(t *testing.T) {
	commands := setupCommands(t, providerInspection{definitions: permconfig.ProviderDefinitions{"corp-oidc": {ID: "corp-oidc", Auth: permconfig.ProviderAuth{Method: providerAuthOIDC}}, "local": {ID: "local", Auth: permconfig.ProviderAuth{Method: providerAuthNone}}}}, "1")
	commands.backend.openOIDCRuntime = func(context.Context, permconfig.ProviderDefinition, bool, io.Writer) (nativeEndpointRuntime, error) {
		return &fakeProviderOIDCRuntime{}, nil
	}
	var output bytes.Buffer
	provider, err := commands.chooseForSetup(context.Background(), &output)
	if err != nil || provider != "anthropic" {
		t.Fatalf("selection = %q, %v", provider, err)
	}
	if strings.Contains(output.String(), "openai-codex") || strings.Contains(output.String(), "local (") {
		t.Fatalf("unsupported setup choice: %s", output.String())
	}
}

func TestProviderSetupCustomPromptStatesProviderIDFormat(t *testing.T) {
	commands := testProviderCommands()
	commands.backend.inspect = providerInspectionLoader(providerInspection{})
	var prompts []string
	commands.terminal.readField = func(_ context.Context, prompt string) (string, error) {
		prompts = append(prompts, prompt)
		if len(prompts) == 1 {
			return "5", nil
		}
		return "", context.Canceled
	}

	err := commands.runSetup(context.Background(), invocationResolution{mode: modeProviderSetup}, io.Discard, io.Discard)
	if !errors.Is(err, errProviderCredentialCancelled) {
		t.Fatalf("setup error = %v, want cancellation after custom provider prompt", err)
	}
	if len(prompts) < 2 || prompts[1] != "Custom provider ID (1-63 lowercase letters, digits, or hyphens; start with a letter and end with a letter or digit)" {
		t.Fatalf("custom provider prompt = %q", prompts)
	}
}

func TestProviderSetupNamedCustomDispatchesToLoginWithoutDefinitionEdit(t *testing.T) {
	path := providerCredentialTestFile(t, "providers:\n  custom:\n    api_key: old-secret\n")
	inspection := providerInspection{definitions: permconfig.ProviderDefinitions{"custom": {ID: "custom", Auth: permconfig.ProviderAuth{Method: providerAuthAPIKey}}}}
	commands := setupCommands(t, inspection, "yes", "no")
	commands.backend.loadCredentials = providerCredentialConfigLoader(providerCredentialConfig{definitions: inspection.definitions, authPath: path})
	commands.terminal.readAPIKey = func(context.Context, string) (string, error) { return "new-secret", nil }
	commands.backend.updateAPIKey = authfile.UpdateAPIKey
	if err := commands.runSetup(context.Background(), invocationResolution{mode: modeProviderSetup, providerName: "custom"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := readProviderCredentialTestFile(t, path); !strings.Contains(got, "new-secret") || strings.Contains(got, "old-secret") {
		t.Fatalf("credential update = %q", got)
	}
}

func TestProviderSetupCancellationBeforeMutation(t *testing.T) {
	commands := setupCommands(t, providerInspection{}, "1")
	commands.backend.loadCredentials = providerCredentialConfigLoader(providerCredentialConfig{authPath: "/safe/auth.yaml"})
	commands.terminal.readAPIKey = func(context.Context, string) (string, error) { return "", context.Canceled }
	commands.backend.updateAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		t.Fatal("cancelled setup wrote")
		return authfile.CommitNotApplied, nil
	}
	var stdout, stderr bytes.Buffer
	err := commands.runSetup(context.Background(), invocationResolution{mode: modeProviderSetup}, &stdout, &stderr)
	if !errors.Is(err, errProviderCredentialCancelled) || !strings.HasSuffix(stderr.String(), "Cancelled; no changes made.\n") {
		t.Fatalf("cancellation err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}

func TestNoProviderRecoveryIsStructuredAndLocalOnly(t *testing.T) {
	err := validateEmbeddedProvider(config{transportMode: modeLocal})
	for _, want := range []string{"mecatui providers setup", "mecatui connect ADDRESS", "remote mecated's provider configuration"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("recovery missing %q: %v", want, err)
		}
	}
}

func TestProviderSetupRejectsInvalidCustomProviderNameBeforeCollectingDefinition(t *testing.T) {
	commands := setupCommands(t, providerInspection{}, "5", "MYPROVIDER")
	wrote := false
	commands.backend.updateProviderMap = func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		wrote = true
		return authfile.CommitDurable, nil
	}
	var stdout, stderr bytes.Buffer
	err := commands.runSetup(context.Background(), invocationResolution{mode: modeProviderSetup}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "lowercase letter") {
		t.Fatalf("runSetup error = %v, want a lowercase-letter validation error", err)
	}
	if wrote {
		t.Fatal("runSetup wrote a provider definition for an invalid name")
	}
}

func TestProviderSetupRollsBackDefinitionWhenLoginIsCancelled(t *testing.T) {
	commands := setupCommands(t, providerInspection{}, "https://gateway.example", "1", "model-1", "1")
	commands.backend.settingsPath = func() string { return "/safe/settings.yaml" }
	writes := 0
	commands.backend.updateProviderMap = func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		writes++
		return authfile.CommitDurable, nil
	}
	commands.backend.loadCredentials = providerCredentialConfigLoader(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: providerAuthAPIKey}}}})
	commands.terminal.readAPIKey = func(context.Context, string) (string, error) { return "", context.Canceled }
	var stdout, stderr bytes.Buffer
	err := commands.runSetup(context.Background(), invocationResolution{mode: modeProviderSetup, providerName: "custom"}, &stdout, &stderr)
	if !errors.Is(err, errProviderCredentialCancelled) || writes != 2 || stdout.Len() != 0 || !strings.HasSuffix(stderr.String(), "Cancelled; no changes made.\n") {
		t.Fatalf("rollback err=%v writes=%d stdout=%q stderr=%q", err, writes, stdout.String(), stderr.String())
	}
}

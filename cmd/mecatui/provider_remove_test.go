package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func removeCommands(cfg providerCredentialConfig) providerCommands {
	commands := testProviderCommands()
	commands.backend.loadCredentials = providerCredentialConfigLoader(cfg)
	commands.terminal.readRemoval = func(context.Context, string) (bool, error) { return true, nil }
	commands.backend.settingsPath = func() string { return "/safe/settings.yaml" }
	return commands
}

func TestProviderRemoveRejectsStockProvider(t *testing.T) {
	commands := removeCommands(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{}})
	commands.terminal.readRemoval = func(context.Context, string) (bool, error) { t.Fatal("stock provider prompted"); return false, nil }
	if err := commands.runRemove(context.Background(), providerRemoveResolution("openai"), io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "not a configured custom provider") {
		t.Fatalf("error = %v", err)
	}
}

func TestProviderRemoveRequiresOneExplicitConfirmationBeforeMutation(t *testing.T) {
	commands := removeCommands(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: providerAuthAPIKey}}}})
	prompts := 0
	commands.terminal.readRemoval = func(_ context.Context, _ string) (bool, error) { prompts++; return false, nil }
	commands.backend.updateAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		t.Fatal("unconfirmed removal wrote")
		return authfile.CommitNotApplied, nil
	}
	var stderr bytes.Buffer
	err := commands.runRemove(context.Background(), providerRemoveResolution("custom"), io.Discard, &stderr)
	if !errors.Is(err, errProviderCredentialCancelled) || prompts != 1 || stderr.String() != "Cancelled; no changes made.\n" {
		t.Fatalf("err=%v prompts=%d stderr=%q", err, prompts, stderr.String())
	}
}

func TestProviderRemoveSelectsAPIKeyAndOIDCCredentialLifecycle(t *testing.T) {
	for _, method := range []string{providerAuthAPIKey, providerAuthOIDC} {
		t.Run(method, func(t *testing.T) {
			commands := removeCommands(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: method}}}, authPath: "/safe/auth.yaml"})
			apiCalls := 0
			fake := &fakeProviderOIDCRuntime{}
			commands.backend.updateAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
				apiCalls++
				return authfile.CommitDurable, nil
			}
			commands.backend.openOIDCRuntime = func(context.Context, permconfig.ProviderDefinition, bool, io.Writer) (nativeEndpointRuntime, error) {
				return fake, nil
			}
			commands.backend.updateProviderMap = func(_ context.Context, _ string, update permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
				if update.ExpectedDefinition == nil || update.ExpectedDefinition.Auth.Method != method {
					t.Fatalf("remove update = %#v, want loaded definition precondition", update)
				}
				return authfile.CommitDurable, nil
			}
			if err := commands.runRemove(context.Background(), providerRemoveResolution("custom"), io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			if method == providerAuthAPIKey && (apiCalls != 1 || fake.logouts != 0) {
				t.Fatalf("API calls=%d logouts=%d", apiCalls, fake.logouts)
			}
			if method == providerAuthOIDC && (apiCalls != 0 || fake.logouts != 1) {
				t.Fatalf("API calls=%d logouts=%d", apiCalls, fake.logouts)
			}
		})
	}
}

func TestProviderRemoveDoesNotClaimAbsentAPIKeyWasRemoved(t *testing.T) {
	commands := removeCommands(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: providerAuthAPIKey}}}})
	commands.backend.updateAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		return authfile.CommitNoop, nil
	}
	commands.backend.updateProviderMap = func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		return authfile.CommitDurable, nil
	}
	var stdout bytes.Buffer
	if err := commands.runRemove(context.Background(), providerRemoveResolution("custom"), &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "Removed custom provider \"custom\".\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestProviderRemoveDeletesDefinitionWithoutChangingSelectedDefault(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "settings")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(dir, "settings.yaml")
	if err := os.WriteFile(settings, []byte("models:\n  default_provider: custom\n  default: custom-model\nproviders:\n  custom:\n    base_url: https://custom.example\n    default_model: custom-model\n    api_flavor: openai-responses\n    auth: {method: none}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commands := removeCommands(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {
		ID: "custom", BaseURL: "https://custom.example", DefaultModel: "custom-model", APIFlavor: "openai-responses", Auth: permconfig.ProviderAuth{Method: providerAuthNone},
	}}})
	commands.backend.settingsPath = func() string { return settings }
	commands.backend.updateProviderMap = permconfig.UpdateProviderMap
	if err := commands.runRemove(context.Background(), providerRemoveResolution("custom"), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(settings)
	if strings.Contains(string(data), "custom:\n    base_url") || !strings.Contains(string(data), "default_provider: custom") {
		t.Fatalf("settings: %s", data)
	}
}

func TestProviderRemoveReportsCredentialFailureTruthfully(t *testing.T) {
	commands := removeCommands(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: providerAuthAPIKey}}}})
	commands.backend.updateAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		return authfile.CommitNotApplied, errors.New("credential store unavailable")
	}
	err := commands.runRemove(context.Background(), providerRemoveResolution("custom"), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "definition for \"custom\" remains") {
		t.Fatalf("error = %v", err)
	}
}

func TestProviderRemoveReportsPartialConfigurationFailureTruthfully(t *testing.T) {
	commands := removeCommands(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: providerAuthAPIKey}}}})
	commands.backend.updateAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		return authfile.CommitDurable, nil
	}
	commands.backend.updateProviderMap = func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		return authfile.CommitNotApplied, errors.New("settings unavailable")
	}
	err := commands.runRemove(context.Background(), providerRemoveResolution("custom"), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "credentials for \"custom\" were removed") {
		t.Fatalf("error = %v", err)
	}
}

func providerRemoveResolution(provider string) invocationResolution {
	return invocationResolution{mode: modeProviderRemove, providerAction: providerActionRemove, providerName: provider}
}

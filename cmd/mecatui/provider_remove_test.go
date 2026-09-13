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

func TestProviderRemoveRejectsStockProvider(t *testing.T) {
	restoreProviderRemoveSeams(t, providerCredentialConfig{definitions: permconfig.ProviderDefinitions{}})
	readProviderRemovalConfirmation = func(string) (bool, error) { t.Fatal("stock provider must not prompt"); return false, nil }
	updateProviderAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		t.Fatal("stock provider must not alter credentials")
		return authfile.CommitNotApplied, nil
	}
	updateProviderMap = func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		t.Fatal("stock provider must not alter settings")
		return authfile.CommitNotApplied, nil
	}

	err := runProviderRemoveCommand(providerRemoveResolution("openai"), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "not a configured custom provider") {
		t.Fatalf("remove stock provider error = %v", err)
	}
}

func TestProviderRemoveRequiresOneExplicitConfirmationBeforeMutation(t *testing.T) {
	restoreProviderRemoveSeams(t, providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: "api_key"}}}, authPath: "/safe/auth.yaml"})
	prompts := 0
	readProviderRemovalConfirmation = func(provider string) (bool, error) {
		prompts++
		if provider != "custom" {
			t.Fatalf("confirmation provider = %q", provider)
		}
		return false, nil
	}
	updateProviderAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		t.Fatal("unconfirmed removal must not alter credentials")
		return authfile.CommitNotApplied, nil
	}
	updateProviderMap = func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		t.Fatal("unconfirmed removal must not alter settings")
		return authfile.CommitNotApplied, nil
	}
	var stdout, stderr bytes.Buffer
	err := runProviderRemoveCommand(providerRemoveResolution("custom"), &stdout, &stderr)
	if !errors.Is(err, errProviderCredentialCancelled) || prompts != 1 || stdout.Len() != 0 || stderr.String() != "Cancelled; no changes made.\n" {
		t.Fatalf("unconfirmed removal = err=%v prompts=%d stdout=%q stderr=%q", err, prompts, stdout.String(), stderr.String())
	}
}

func TestProviderRemoveSelectsAPIKeyAndOIDCCredentialLifecycle(t *testing.T) {
	for _, tc := range []struct{ name, method string }{{"API key", "api_key"}, {"OIDC", "oidc"}} {
		t.Run(tc.name, func(t *testing.T) {
			restoreProviderRemoveSeams(t, providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: tc.method}}}, authPath: "/safe/auth.yaml"})
			readProviderRemovalConfirmation = func(string) (bool, error) { return true, nil }
			apiCalls := 0
			fake := &fakeProviderOIDCRuntime{}
			updateProviderAPIKey = func(_ context.Context, path string, update authfile.APIKeyUpdate) (authfile.CommitState, error) {
				apiCalls++
				if path != "/safe/auth.yaml" || update.Provider != "custom" || update.APIKey != nil {
					t.Fatalf("API-key deletion = path=%q update=%#v", path, update)
				}
				return authfile.CommitDurable, nil
			}
			openProviderOIDCRuntime = func(context.Context, permconfig.ProviderDefinition, bool, io.Writer) (nativeEndpointRuntime, error) {
				return fake, nil
			}
			updateProviderMap = func(_ context.Context, _ string, update permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
				if update.Provider != "custom" || update.Definition != nil {
					t.Fatalf("definition deletion = %#v", update)
				}
				return authfile.CommitDurable, nil
			}
			if err := runProviderRemoveCommand(providerRemoveResolution("custom"), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			if tc.method == "api_key" && (apiCalls != 1 || fake.logouts != 0) {
				t.Fatalf("API-key calls=%d OIDC logouts=%d", apiCalls, fake.logouts)
			}
			if tc.method == "oidc" && (apiCalls != 0 || fake.logouts != 1) {
				t.Fatalf("API-key calls=%d OIDC logouts=%d", apiCalls, fake.logouts)
			}
		})
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
	restoreProviderRemoveSeams(t, providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: "none"}}}})
	providerSettingsPath = func() string { return settings }
	readProviderRemovalConfirmation = func(string) (bool, error) { return true, nil }
	updateProviderMap = permconfig.UpdateProviderMap
	var stdout, stderr bytes.Buffer
	if err := runProviderRemoveCommand(providerRemoveResolution("custom"), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "custom:\n    base_url") || !strings.Contains(string(data), "default_provider: custom") {
		t.Fatalf("removal did not preserve selected default:\n%s", data)
	}
}

func TestProviderRemoveReportsCredentialFailureTruthfully(t *testing.T) {
	restoreProviderRemoveSeams(t, providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: "api_key"}}}, authPath: "/safe/auth.yaml"})
	readProviderRemovalConfirmation = func(string) (bool, error) { return true, nil }
	updateProviderAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		return authfile.CommitNotApplied, errors.New("credential store unavailable")
	}
	updateProviderMap = func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		t.Fatal("definition must remain when credential removal fails")
		return authfile.CommitNotApplied, nil
	}
	err := runProviderRemoveCommand(providerRemoveResolution("custom"), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "definition for \"custom\" remains") || !strings.Contains(err.Error(), "API key may remain") {
		t.Fatalf("credential failure error = %v", err)
	}
}

func TestProviderRemoveReportsPartialConfigurationFailureTruthfully(t *testing.T) {
	restoreProviderRemoveSeams(t, providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: "api_key"}}}, authPath: "/safe/auth.yaml"})
	readProviderRemovalConfirmation = func(string) (bool, error) { return true, nil }
	updateProviderAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		return authfile.CommitDurable, nil
	}
	updateProviderMap = func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		return authfile.CommitNotApplied, errors.New("settings unavailable")
	}
	err := runProviderRemoveCommand(providerRemoveResolution("custom"), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "credentials for \"custom\" were removed") || !strings.Contains(err.Error(), "definition remains") {
		t.Fatalf("partial failure error = %v", err)
	}
}

func providerRemoveResolution(provider string) invocationResolution {
	return invocationResolution{mode: modeProviderRemove, llmAction: providerActionRemove, llmEndpoint: provider}
}

func restoreProviderRemoveSeams(t *testing.T, cfg providerCredentialConfig) {
	t.Helper()
	oldLoad, oldConfirm, oldAPI, oldOIDC, oldMap, oldPath := loadProviderCredentialConfig, readProviderRemovalConfirmation, updateProviderAPIKey, openProviderOIDCRuntime, updateProviderMap, providerSettingsPath
	t.Cleanup(func() {
		loadProviderCredentialConfig, readProviderRemovalConfirmation, updateProviderAPIKey, openProviderOIDCRuntime, updateProviderMap, providerSettingsPath = oldLoad, oldConfirm, oldAPI, oldOIDC, oldMap, oldPath
	})
	loadProviderCredentialConfig = func() (providerCredentialConfig, error) { return cfg, nil }
}

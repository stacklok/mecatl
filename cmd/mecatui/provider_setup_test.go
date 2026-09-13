package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestProviderSetupMenuSelectionAndCapabilityLabels(t *testing.T) {
	oldStatuses, oldRead := loadProviderStatuses, readProviderSetupField
	t.Cleanup(func() {
		loadProviderStatuses, readProviderSetupField = oldStatuses, oldRead
	})
	loadProviderStatuses = func() ([]providerStatus, error) {
		return []providerStatus{
			{Name: "openai", Class: providerClassBuiltin},
			{Name: toolHiveEndpointID, Class: "external"},
			{Name: "corp", Class: "custom", Auth: "configured"},
		}, nil
	}
	readProviderSetupField = func(string) (string, error) { return "2", nil }

	var output bytes.Buffer
	provider, err := chooseProviderForSetup(&output)
	if err != nil || provider != toolHiveEndpointID {
		t.Fatalf("selection = %q, %v", provider, err)
	}
	for _, want := range []string{
		"1. openai (API key)",
		"2. toolhive (external lifecycle)",
		"3. corp (custom configured)",
		"4. custom (custom provider: API key, OIDC, or no authentication)",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("menu missing %q:\n%s", want, output.String())
		}
	}
}

func TestProviderSetupNamedCustomDispatchesToLoginWithoutDefinitionEdit(t *testing.T) {
	path := providerCredentialTestFile(t, "providers:\n  custom:\n    api_key: old-secret\n")
	oldStatuses, oldLoad, oldRead := loadProviderStatuses, loadProviderCredentialConfig, readProviderAPIKey
	t.Cleanup(func() {
		loadProviderStatuses, loadProviderCredentialConfig, readProviderAPIKey = oldStatuses, oldLoad, oldRead
	})
	loadProviderStatuses = func() ([]providerStatus, error) {
		return []providerStatus{{Name: "custom", Class: "custom", Auth: "not configured"}}, nil
	}
	loadProviderCredentialConfig = func() (providerCredentialConfig, error) {
		return providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: "api_key"}}}, authPath: path}, nil
	}
	readProviderAPIKey = func(string) (string, error) { return "new-secret", nil }

	var stdout, stderr bytes.Buffer
	if err := runProviderSetupCommand(invocationResolution{mode: modeProviderSetup, llmEndpoint: "custom"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if got := readProviderCredentialTestFile(t, path); !strings.Contains(got, "new-secret") || strings.Contains(got, "old-secret") {
		t.Fatalf("credential update = %q", got)
	}
	if stdout.String() != "API key saved for provider \"custom\"\n" || stderr.Len() != 0 {
		t.Fatalf("output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestProviderSetupCancellationBeforeMutation(t *testing.T) {
	oldStatuses, oldRead, oldUpdate := loadProviderStatuses, readProviderSetupField, updateProviderAPIKey
	t.Cleanup(func() {
		loadProviderStatuses, readProviderSetupField, updateProviderAPIKey = oldStatuses, oldRead, oldUpdate
	})
	loadProviderStatuses = func() ([]providerStatus, error) {
		return []providerStatus{{Name: "openai", Class: providerClassBuiltin}}, nil
	}
	readProviderSetupField = func(string) (string, error) { return "1", nil }
	oldLoad, oldKeyRead := loadProviderCredentialConfig, readProviderAPIKey
	t.Cleanup(func() { loadProviderCredentialConfig, readProviderAPIKey = oldLoad, oldKeyRead })
	loadProviderCredentialConfig = func() (providerCredentialConfig, error) {
		return providerCredentialConfig{authPath: "/safe/auth.yaml"}, nil
	}
	readProviderAPIKey = func(string) (string, error) { return "", context.Canceled }
	updateProviderAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		t.Fatal("cancelled setup must not write")
		return authfile.CommitNotApplied, nil
	}

	var stdout, stderr bytes.Buffer
	err := runProviderSetupCommand(invocationResolution{mode: modeProviderSetup}, &stdout, &stderr)
	if !errors.Is(err, errProviderCredentialCancelled) || !strings.Contains(stdout.String(), "Provider setup\n") || stderr.String() != "Cancelled; no changes made.\n" {
		t.Fatalf("cancellation err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}

func TestNoProviderRecoveryDoesNotStartSetup(t *testing.T) {
	cfg := config{transportMode: modeLocal}
	err := validateEmbeddedProvider(cfg)
	if err == nil || !strings.Contains(err.Error(), "mecatui providers setup") || !strings.Contains(err.Error(), "connect to mecated") {
		t.Fatalf("no-provider recovery = %v", err)
	}
}

func TestProviderSetupReportsCompletedDefinitionWhenLoginIsCancelled(t *testing.T) {
	oldStatuses, oldRead, oldUpdate := loadProviderStatuses, readProviderAddField, updateProviderMap
	t.Cleanup(func() {
		loadProviderStatuses, readProviderAddField, updateProviderMap = oldStatuses, oldRead, oldUpdate
	})
	loadProviderStatuses = func() ([]providerStatus, error) { return nil, nil }
	values := []string{"https://gateway.example", "openai-responses", "model-1", "api_key"}
	readProviderAddField = func(string) (string, error) {
		if len(values) == 0 {
			t.Fatal("unexpected provider definition prompt")
		}
		value := values[0]
		values = values[1:]
		return value, nil
	}
	updateProviderMap = func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		return authfile.CommitDurable, nil
	}
	oldLoad, oldKeyRead := loadProviderCredentialConfig, readProviderAPIKey
	t.Cleanup(func() { loadProviderCredentialConfig, readProviderAPIKey = oldLoad, oldKeyRead })
	loadProviderCredentialConfig = func() (providerCredentialConfig, error) {
		return providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: "api_key"}}}, authPath: "/safe/auth.yaml"}, nil
	}
	readProviderAPIKey = func(string) (string, error) { return "", context.Canceled }

	var stdout, stderr bytes.Buffer
	err := runProviderSetupCommand(invocationResolution{mode: modeProviderSetup, llmEndpoint: "custom"}, &stdout, &stderr)
	if err != nil || stdout.String() != "Provider definition saved for \"custom\"\n" || stderr.String() != "Login cancelled; provider definition saved for \"custom\".\n" {
		t.Fatalf("result err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}

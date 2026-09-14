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
	oldStatuses, oldAllStatuses, oldRead := loadProviderStatuses, loadAllProviderStatuses, readProviderSetupField
	t.Cleanup(func() {
		loadProviderStatuses, loadAllProviderStatuses, readProviderSetupField = oldStatuses, oldAllStatuses, oldRead
	})
	loadAllProviderStatuses = func() ([]providerStatus, error) {
		return []providerStatus{
			{Name: "openai", Class: providerClassBuiltin, Auth: "not configured"},
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

func TestProviderSetupMenuUsesFullInventoryNotBareStatus(t *testing.T) {
	oldStatuses, oldAllStatuses, oldRead := loadProviderStatuses, loadAllProviderStatuses, readProviderSetupField
	t.Cleanup(func() {
		loadProviderStatuses, loadAllProviderStatuses, readProviderSetupField = oldStatuses, oldAllStatuses, oldRead
	})
	loadProviderStatuses = func() ([]providerStatus, error) {
		return []providerStatus{{Name: "openai", Class: providerClassBuiltin, Auth: "configured", DefaultModel: "gpt-5", Next: "ready to use"}}, nil
	}
	loadAllProviderStatuses = func() ([]providerStatus, error) {
		return []providerStatus{
			{Name: "anthropic", Class: providerClassBuiltin, Auth: "not configured"},
			{Name: "openai", Class: providerClassBuiltin, Auth: "configured"},
			{Name: "corp", Class: "custom", Auth: "not configured"},
		}, nil
	}
	readProviderSetupField = func(string) (string, error) { return "1", nil }

	bare := resolveInvocation([]string{"mecatui", "providers"})
	var statusOutput, setupOutput bytes.Buffer
	if err := runProviderStatusCommand(bare, &statusOutput, &bytes.Buffer{}); err != nil {
		t.Fatalf("bare providers: %v", err)
	}
	provider, err := chooseProviderForSetup(&setupOutput)
	if err != nil || provider != "anthropic" {
		t.Fatalf("newly unconfigured stock selection = %q, %v", provider, err)
	}
	if strings.Contains(statusOutput.String(), "anthropic") || !strings.Contains(statusOutput.String(), "openai (built-in)") {
		t.Fatalf("bare configured-only output = %q", statusOutput.String())
	}
	if strings.Contains(setupOutput.String(), "openai (API key)") {
		t.Fatalf("setup menu included configured stock provider: %q", setupOutput.String())
	}
	for _, want := range []string{
		"1. anthropic (API key)",
		"2. corp (custom not configured)",
		"3. custom (custom provider: API key, OIDC, or no authentication)",
	} {
		if !strings.Contains(setupOutput.String(), want) {
			t.Errorf("setup menu missing %q:\n%s", want, setupOutput.String())
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
	oldStatuses, oldAllStatuses, oldRead, oldUpdate := loadProviderStatuses, loadAllProviderStatuses, readProviderSetupField, updateProviderAPIKey
	t.Cleanup(func() {
		loadProviderStatuses, loadAllProviderStatuses, readProviderSetupField, updateProviderAPIKey = oldStatuses, oldAllStatuses, oldRead, oldUpdate
	})
	loadAllProviderStatuses = func() ([]providerStatus, error) {
		return []providerStatus{{Name: "openai", Class: providerClassBuiltin, Auth: "not configured"}}, nil
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

func TestNoProviderRecoveryIsStructuredAndLocalOnly(t *testing.T) {
	cfg := config{transportMode: modeLocal}
	err := validateEmbeddedProvider(cfg)
	if err == nil {
		t.Fatal("missing provider error")
	}
	want := []string{
		"no LLM provider configured for the embedded server. Choose one:",
		"  1. Run `mecatui providers setup` to configure a direct provider.",
		"  2. Set a provider API key in the environment or use `--api-key-file PATH`.",
		"  3. Enable a ToolHive LLM gateway.",
		"  4. Start with `--mock` for offline testing.",
		"  5. Connect to an existing remote server with `mecatui connect ADDRESS`.",
		"These options configure only the embedded server; a remote mecated's provider configuration is managed by its operator.",
	}
	for _, line := range want {
		if !strings.Contains(err.Error(), line) {
			t.Errorf("recovery output missing %q:\n%s", line, err)
		}
	}
	if strings.Contains(err.Error(), "run setup automatically") {
		t.Errorf("recovery must not launch setup: %s", err)
	}
}

func TestProviderSetupReportsCompletedDefinitionWhenLoginIsCancelled(t *testing.T) {
	oldStatuses, oldRead, oldUpdate := loadProviderStatuses, readProviderAddField, updateProviderMap
	t.Cleanup(func() {
		loadProviderStatuses, readProviderAddField, updateProviderMap = oldStatuses, oldRead, oldUpdate
	})
	loadProviderStatuses = func() ([]providerStatus, error) { return nil, nil }
	values := []string{"https://gateway.example", "1", "model-1", "1"}
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

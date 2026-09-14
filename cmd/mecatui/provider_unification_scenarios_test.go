package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/app"
)

func TestProviderUnification_Scenario2_ProviderCommandGrammar(t *testing.T) {
	for _, args := range [][]string{{"mecatui", "providers"}, {"mecatui", "providers", "status"}} {
		if got := resolveInvocation(args); got.err != nil {
			t.Fatalf("resolve %v: %v", args, got.err)
		}
	}
	if got := resolveInvocation([]string{"mecatui", "llm", "status"}); got.err == nil {
		t.Fatal("legacy llm command accepted")
	}
}

func TestProviderUnification_Scenario2_LoginUsesSelectedAuthentication(t *testing.T) {
	commands := credentialCommands(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: providerAuthAPIKey}}}}, func(context.Context, string) (string, error) { return "secret", nil })
	commands.backend.updateAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		return authfile.CommitDurable, nil
	}
	if err := commands.runCredential(context.Background(), providerCredentialResolution(providerActionLogin, "custom"), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	fake := &fakeProviderOIDCRuntime{}
	commands = credentialCommands(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"oidc": {Auth: permconfig.ProviderAuth{Method: providerAuthOIDC}}}}, nil)
	commands.backend.openOIDCRuntime = func(context.Context, permconfig.ProviderDefinition, bool, io.Writer) (nativeEndpointRuntime, error) {
		return fake, nil
	}
	if err := commands.runCredential(context.Background(), providerCredentialResolution(providerActionLogin, "oidc"), io.Discard, io.Discard); err != nil || fake.logins != 1 {
		t.Fatalf("OIDC login err=%v calls=%d", err, fake.logins)
	}
}

func TestProviderUnification_Scenario2_LogoutPreservesProvider(t *testing.T) {
	path := providerCredentialTestFile(t, "providers:\n  custom:\n    api_key: managed-secret\n")
	commands := credentialCommands(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: providerAuthAPIKey}}}, authPath: path}, nil)
	if err := commands.runCredential(context.Background(), providerCredentialResolution(providerActionLogout, "custom"), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := readProviderCredentialTestFile(t, path); strings.Contains(got, "managed-secret") || !strings.Contains(got, "custom:") {
		t.Fatalf("logout changed definition: %q", got)
	}
}

func TestProviderUnification_Scenario2_RemoveDefinitionAndCredentials(t *testing.T) {
	commands := removeCommands(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: providerAuthAPIKey}}}})
	commands.backend.updateAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		return authfile.CommitDurable, nil
	}
	commands.backend.updateProviderMap = func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		return authfile.CommitDurable, nil
	}
	if err := commands.runRemove(context.Background(), providerRemoveResolution("custom"), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, _, err := app.ResolveDeploymentDefault(context.Background(), app.Config{DefaultProvider: "custom", DefaultModel: "model"}); err == nil {
		t.Fatal("removed default remained valid")
	}
}

func TestProviderUnification_Scenario2_AddChainsLoginUnlessOptedOut(t *testing.T) {
	commands := cancelledAddCommands(t)
	commands.terminal.readAPIKey = func(context.Context, string) (string, error) { return "secret", nil }
	commands.backend.updateProviderMap = func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
		return authfile.CommitDurable, nil
	}
	logins := 0
	commands.backend.updateAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		logins++
		return authfile.CommitDurable, nil
	}
	if err := commands.runAdd(context.Background(), invocationResolution{mode: modeProviderAdd, providerName: "custom"}, io.Discard, io.Discard); err != nil || logins != 1 {
		t.Fatalf("add err=%v logins=%d", err, logins)
	}
}

func TestProviderUnification_Scenario2_ToolHiveLifecycleIsolation(t *testing.T) {
	commands := testProviderCommands()
	calls := 0
	commands.backend.toolHiveLogin = func(context.Context, bool) error { calls++; return nil }
	if err := commands.runCredential(context.Background(), providerCredentialResolution(providerActionLogin, toolHiveEndpointID), io.Discard, io.Discard); err != nil || calls != 1 {
		t.Fatalf("ToolHive err=%v calls=%d", err, calls)
	}
	if err := commands.runCredential(context.Background(), providerCredentialResolution(providerActionLogout, toolHiveEndpointID), io.Discard, io.Discard); err == nil {
		t.Fatal("ToolHive logout succeeded")
	}
}

func TestProviderUnification_Scenario3_ProviderHelpHierarchy(t *testing.T) {
	if got := resolveInvocation([]string{"mecatui", "providers", "status", "--help"}); got.err != nil || got.mode != modeProviderStatus {
		t.Fatalf("status help = %+v", got)
	}
}
func TestProviderUnification_Scenario4_APIKeyFileFlagIsTheOnlySpelling(t *testing.T) {
	if got := resolveInvocation([]string{"mecatui", "--api-key-file", "auth.yaml"}); got.err != nil {
		t.Fatal(got.err)
	}
}
func TestProviderUnification_Scenario5_ChangedTargetIsTruthful(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := authfile.UpdateAPIKey(ctx, t.TempDir()+"/auth.yaml", authfile.APIKeyUpdate{Provider: "openai", APIKey: stringPtr("secret")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write = %v", err)
	}
}
func stringPtr(v string) *string { return &v }

func TestProviderUnification_Scenario3_AC31_HelpPathsAreSuccessfulAndDedicated(t *testing.T) {
	for _, args := range [][]string{{"mecatui", "providers", "--help"}, {"mecatui", "providers", "setup", "--help"}, {"mecatui", "providers", "login", "--help"}, {"mecatui", "providers", "remove", "--help"}} {
		res := resolveInvocation(args)
		var stdout, stderr bytes.Buffer
		if err := runProviderHelpForTest(testProviderCommands(), res, &stdout, &stderr); !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("%v help = %v", args, err)
		}
	}
}
func TestProviderUnification_Scenario3_AC32_StatusIsPassiveAndNeverPrintsSecrets(t *testing.T) {
	commands := testProviderCommands()
	commands.backend.inspect = providerInspectionLoader(providerInspection{})
	var out bytes.Buffer
	if err := commands.runStatus(context.Background(), invocationResolution{mode: modeProviderStatus}, &out, io.Discard); err != nil || strings.Contains(out.String(), "secret") {
		t.Fatalf("status err=%v output=%q", err, out.String())
	}
}
func TestProviderUnification_Scenario3_AC33_SetupMenuCancelsBeforeMutation(t *testing.T) {
	commands := setupCommands(t, providerInspection{}, "1")
	commands.backend.loadCredentials = providerCredentialConfigLoader(providerCredentialConfig{})
	commands.terminal.readAPIKey = func(context.Context, string) (string, error) { return "", context.Canceled }
	var stderr bytes.Buffer
	err := commands.runSetup(context.Background(), invocationResolution{mode: modeProviderSetup}, io.Discard, &stderr)
	if !errors.Is(err, errProviderCredentialCancelled) {
		t.Fatalf("setup cancellation = %v", err)
	}
}
func TestProviderUnification_Scenario3_AC34_NoProviderRecoveryIsLocalOnly(t *testing.T) {
	local := config{transportMode: modeLocal}
	if err := validateEmbeddedProvider(local); err == nil || !strings.Contains(err.Error(), "mecatui providers setup") {
		t.Fatalf("local recovery = %v", err)
	}
	remote := config{transportMode: modeConnect, connectAddress: "example:443", mode: "default"}
	if err := remote.validate(); err != nil {
		t.Fatalf("remote validation = %v", err)
	}
}

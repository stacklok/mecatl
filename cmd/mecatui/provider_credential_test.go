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
	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func credentialCommands(cfg providerCredentialConfig, input func(context.Context, string) (string, error)) providerCommands {
	commands := testProviderCommands()
	commands.backend.loadCredentials = providerCredentialConfigLoader(cfg)
	commands.backend.updateAPIKey = authfile.UpdateAPIKey
	commands.terminal.readField = func(context.Context, string) (string, error) { return "yes", nil }
	if input != nil {
		commands.terminal.readAPIKey = input
	}
	return commands
}

func TestProviderCredentialLoginReplacesOnlyCustomAPIKey(t *testing.T) {
	path := providerCredentialTestFile(t, "providers:\n  custom:\n    api_key: old-secret\n  other:\n    api_key: retained-secret\n")
	commands := credentialCommands(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: providerAuthAPIKey}}}, authPath: path}, func(context.Context, string) (string, error) { return "replacement-secret", nil })
	var stdout, stderr bytes.Buffer
	if err := commands.runCredential(context.Background(), providerCredentialResolution(providerActionLogin, "custom"), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	content := readProviderCredentialTestFile(t, path)
	if strings.Contains(content, "old-secret") || !strings.Contains(content, "replacement-secret") || !strings.Contains(content, "retained-secret") {
		t.Fatalf("credentials not preserved: %q", content)
	}
	if strings.Contains(stdout.String()+stderr.String(), "secret") {
		t.Fatal("credential output leaked a secret")
	}
}

func TestProviderCredentialLogoutRemovesOnlyLocalKey(t *testing.T) {
	path := providerCredentialTestFile(t, "providers:\n  custom:\n    api_key: managed-secret\n  other:\n    api_key: retained-secret\n")
	commands := credentialCommands(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: providerAuthAPIKey}}}, authPath: path}, nil)
	if err := commands.runCredential(context.Background(), providerCredentialResolution(providerActionLogout, "custom"), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	content := readProviderCredentialTestFile(t, path)
	if strings.Contains(content, "managed-secret") || strings.Contains(content, "custom:") || !strings.Contains(content, "retained-secret") {
		t.Fatalf("logout did not preserve unrelated data: %q", content)
	}
}

func TestProviderCredentialLoginCancellationDoesNotWrite(t *testing.T) {
	path := providerCredentialTestFile(t, "providers:\n  custom:\n    api_key: retained-secret\n")
	commands := credentialCommands(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: providerAuthAPIKey}}}, authPath: path}, func(context.Context, string) (string, error) { return "", context.Canceled })
	var stdout, stderr bytes.Buffer
	err := commands.runCredential(context.Background(), providerCredentialResolution(providerActionLogin, "custom"), &stdout, &stderr)
	if !errors.Is(err, errProviderCredentialCancelled) || !strings.HasSuffix(stderr.String(), "Cancelled; no changes made.\n") || stdout.Len() != 0 {
		t.Fatalf("cancellation err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if readProviderCredentialTestFile(t, path) != "providers:\n  custom:\n    api_key: retained-secret\n" {
		t.Fatal("cancelled login wrote credentials")
	}
}

func TestProviderCredentialOIDCLifecycleUsesConfiguredDefinition(t *testing.T) {
	definition := permconfig.ProviderDefinition{ID: "oidc", BaseURL: "https://gateway.example", Auth: permconfig.ProviderAuth{Method: providerAuthOIDC}}
	commands := credentialCommands(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"oidc": definition}}, nil)
	fake := &fakeProviderOIDCRuntime{}
	var got permconfig.ProviderDefinition
	var gotNoBrowser bool
	commands.backend.openOIDCRuntime = func(_ context.Context, def permconfig.ProviderDefinition, noBrowser bool, _ io.Writer) (nativeEndpointRuntime, error) {
		got, gotNoBrowser = def, noBrowser
		return fake, nil
	}
	res := providerCredentialResolution(providerActionLogin, "oidc")
	res.remaining = []string{"--no-browser"}
	if err := commands.runCredential(context.Background(), res, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got != definition || !gotNoBrowser || fake.logins != 1 || fake.closes != 1 {
		t.Fatalf("OIDC handoff definition=%#v noBrowser=%t calls=%#v", got, gotNoBrowser, fake)
	}
	if err := commands.runCredential(context.Background(), providerCredentialResolution(providerActionLogout, "oidc"), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if fake.logouts != 1 {
		t.Fatalf("OIDC logout calls = %d", fake.logouts)
	}
}

func TestProviderCredentialOIDCCancellationPreservesCLIContract(t *testing.T) {
	commands := credentialCommands(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"oidc": {Auth: permconfig.ProviderAuth{Method: providerAuthOIDC}}}}, nil)
	commands.backend.openOIDCRuntime = func(context.Context, permconfig.ProviderDefinition, bool, io.Writer) (nativeEndpointRuntime, error) {
		return &fakeProviderOIDCRuntime{loginErr: context.Canceled}, nil
	}
	var stdout, stderr bytes.Buffer
	err := commands.runCredential(context.Background(), providerCredentialResolution(providerActionLogin, "oidc"), &stdout, &stderr)
	if !errors.Is(err, errProviderCredentialCancelled) || stdout.Len() != 0 || !strings.Contains(stderr.String(), "Cancelled; OIDC enrollment did not complete.") || strings.Contains(stderr.String(), "no changes made") {
		t.Fatalf("OIDC cancellation err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}

func TestProviderCredentialToolHiveHandoff(t *testing.T) {
	commands := testProviderCommands()
	var noBrowser bool
	commands.backend.toolHiveLogin = func(_ context.Context, got bool) error { noBrowser = got; return nil }
	res := providerCredentialResolution(providerActionLogin, toolHiveEndpointID)
	res.remaining = []string{"--no-browser"}
	if err := commands.runCredential(context.Background(), res, io.Discard, io.Discard); err != nil || !noBrowser {
		t.Fatalf("ToolHive handoff err=%v noBrowser=%t", err, noBrowser)
	}
	if err := commands.runCredential(context.Background(), providerCredentialResolution(providerActionLogout, toolHiveEndpointID), io.Discard, io.Discard); err == nil {
		t.Fatal("ToolHive logout must remain externally owned")
	}
}

func TestProviderCredentialRejectsNoBrowserForBuiltinAPIKey(t *testing.T) {
	commands := credentialCommands(providerCredentialConfig{}, func(context.Context, string) (string, error) {
		t.Fatal("invalid flag must be rejected before prompting")
		return "", nil
	})
	res := providerCredentialResolution(providerActionLogin, "openai")
	res.remaining = []string{"--no-browser"}
	if err := commands.runCredential(context.Background(), res, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "available only for auth.method oidc") {
		t.Fatalf("built-in --no-browser error = %v", err)
	}
}

func TestProviderCredentialRejectsUnavailableProviderWithoutMutation(t *testing.T) {
	commands := credentialCommands(providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: providerAuthAPIKey}}, "none": {Auth: permconfig.ProviderAuth{Method: providerAuthNone}}}}, func(context.Context, string) (string, error) { t.Fatal("secret input requested"); return "", nil })
	commands.backend.updateAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		t.Fatal("credentials updated")
		return authfile.CommitNotApplied, nil
	}
	for _, res := range []invocationResolution{providerCredentialResolution(providerActionLogin, "missing"), providerCredentialResolution(providerActionLogout, "none")} {
		if err := commands.runCredential(context.Background(), res, io.Discard, io.Discard); err == nil {
			t.Fatalf("%s %s succeeded", res.providerAction, res.providerName)
		}
	}
}

type fakeProviderOIDCRuntime struct {
	logins, logouts, closes int
	loginErr                error
}

func (f *fakeProviderOIDCRuntime) Login(context.Context) error { f.logins++; return f.loginErr }
func (*fakeProviderOIDCRuntime) Status(context.Context) llmendpoint.Status {
	return llmendpoint.StatusNotEnrolled
}
func (f *fakeProviderOIDCRuntime) Logout(context.Context) error { f.logouts++; return nil }
func (f *fakeProviderOIDCRuntime) Close() error                 { f.closes++; return nil }

func providerCredentialResolution(action, provider string) invocationResolution {
	return invocationResolution{mode: modeProviderCredential, providerAction: action, providerName: provider}
}

func providerCredentialTestFile(t *testing.T, content string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "auth.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
func readProviderCredentialTestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

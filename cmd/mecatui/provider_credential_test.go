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

func TestProviderCredentialLoginReplacesOnlyCustomAPIKey(t *testing.T) {
	path := providerCredentialTestFile(t, "providers:\n  custom:\n    api_key: old-secret\n  other:\n    api_key: retained-secret\n")
	restoreProviderCredentialSeams(t, providerCredentialConfig{definitions: permconfig.ProviderDefinitions{
		"custom": {Auth: permconfig.ProviderAuth{Method: "api_key"}},
	}, authPath: path}, func(string) (string, error) { return "replacement-secret", nil })

	var stdout, stderr bytes.Buffer
	if err := runProviderCredentialCommand(providerCredentialResolution(providerActionLogin, "custom"), &stdout, &stderr); err != nil {
		t.Fatalf("login: %v", err)
	}
	content := readProviderCredentialTestFile(t, path)
	if strings.Contains(content, "old-secret") || !strings.Contains(content, "replacement-secret") || !strings.Contains(content, "retained-secret") {
		t.Fatalf("credentials were not preserved/replaced correctly: %q", content)
	}
	if strings.Contains(stdout.String()+stderr.String(), "secret") {
		t.Fatalf("credential command output leaked a secret: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestProviderCredentialLogoutRemovesOnlyLocalKey(t *testing.T) {
	path := providerCredentialTestFile(t, "providers:\n  custom:\n    api_key: managed-secret\n  other:\n    api_key: retained-secret\n")
	restoreProviderCredentialSeams(t, providerCredentialConfig{definitions: permconfig.ProviderDefinitions{
		"custom": {Auth: permconfig.ProviderAuth{Method: "api_key"}},
	}, authPath: path}, nil)

	var stdout, stderr bytes.Buffer
	if err := runProviderCredentialCommand(providerCredentialResolution(providerActionLogout, "custom"), &stdout, &stderr); err != nil {
		t.Fatalf("logout: %v", err)
	}
	content := readProviderCredentialTestFile(t, path)
	if strings.Contains(content, "managed-secret") || !strings.Contains(content, "custom:") || !strings.Contains(content, "retained-secret") {
		t.Fatalf("logout did not preserve provider and unrelated credential: %q", content)
	}
	if strings.Contains(stdout.String()+stderr.String(), "secret") {
		t.Fatalf("credential command output leaked a secret: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestProviderCredentialLoginCancellationDoesNotWrite(t *testing.T) {
	path := providerCredentialTestFile(t, "providers:\n  custom:\n    api_key: retained-secret\n")
	restoreProviderCredentialSeams(t, providerCredentialConfig{definitions: permconfig.ProviderDefinitions{
		"custom": {Auth: permconfig.ProviderAuth{Method: "api_key"}},
	}, authPath: path}, func(string) (string, error) { return "", context.Canceled })

	var stdout, stderr bytes.Buffer
	if err := runProviderCredentialCommand(providerCredentialResolution(providerActionLogin, "custom"), &stdout, &stderr); !errors.Is(err, errProviderCredentialCancelled) {
		t.Fatalf("cancelled login error = %v, want provider credential cancellation", err)
	}
	if got := stderr.String(); got != "Cancelled; no changes made.\n" {
		t.Fatalf("cancellation output = %q, want %q", got, "Cancelled; no changes made.\\n")
	}
	if stdout.Len() != 0 || readProviderCredentialTestFile(t, path) != "providers:\n  custom:\n    api_key: retained-secret\n" {
		t.Fatalf("cancelled login wrote credentials or output: stdout=%q", stdout.String())
	}
}

func TestProviderCredentialOIDCLifecycleUsesConfiguredDefinition(t *testing.T) {
	path := providerCredentialTestFile(t, "providers:\n  custom:\n    api_key: retained-secret\n")
	definition := permconfig.ProviderDefinition{ID: "oidc", BaseURL: "https://gateway.example", Auth: permconfig.ProviderAuth{Method: "oidc"}}
	oldLoad, oldOpen := loadProviderCredentialConfig, openProviderOIDCRuntime
	t.Cleanup(func() { loadProviderCredentialConfig, openProviderOIDCRuntime = oldLoad, oldOpen })
	loadProviderCredentialConfig = func() (providerCredentialConfig, error) {
		return providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"oidc": definition}, authPath: path}, nil
	}
	fake := &fakeProviderOIDCRuntime{}
	var got permconfig.ProviderDefinition
	var gotNoBrowser bool
	openProviderOIDCRuntime = func(_ context.Context, def permconfig.ProviderDefinition, noBrowser bool, _ io.Writer) (nativeEndpointRuntime, error) {
		got, gotNoBrowser = def, noBrowser
		return fake, nil
	}

	res := providerCredentialResolution(providerActionLogin, "oidc")
	res.remaining = []string{"--no-browser"}
	var stdout, stderr bytes.Buffer
	if err := runProviderCredentialCommand(res, &stdout, &stderr); err != nil {
		t.Fatalf("OIDC login: %v", err)
	}
	if got != definition || !gotNoBrowser || fake.logins != 1 || fake.logouts != 0 || fake.closes != 1 {
		t.Fatalf("OIDC handoff definition=%#v noBrowser=%t calls=%#v", got, gotNoBrowser, fake)
	}
	if content := readProviderCredentialTestFile(t, path); !strings.Contains(content, "retained-secret") {
		t.Fatalf("OIDC lifecycle touched API-key custody: %q", content)
	}

	if err := runProviderCredentialCommand(providerCredentialResolution(providerActionLogout, "oidc"), &stdout, &stderr); err != nil {
		t.Fatalf("OIDC logout: %v", err)
	}
	if fake.logouts != 1 {
		t.Fatalf("OIDC logout calls = %d, want 1", fake.logouts)
	}
}

func TestProviderCredentialOIDCCancellationPreservesCLIContract(t *testing.T) {
	oldLoad, oldOpen := loadProviderCredentialConfig, openProviderOIDCRuntime
	t.Cleanup(func() { loadProviderCredentialConfig, openProviderOIDCRuntime = oldLoad, oldOpen })
	loadProviderCredentialConfig = func() (providerCredentialConfig, error) {
		return providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"oidc": {Auth: permconfig.ProviderAuth{Method: "oidc"}}}}, nil
	}
	openProviderOIDCRuntime = func(context.Context, permconfig.ProviderDefinition, bool, io.Writer) (nativeEndpointRuntime, error) {
		return &fakeProviderOIDCRuntime{loginErr: context.Canceled}, nil
	}
	var stdout, stderr bytes.Buffer
	err := runProviderCredentialCommand(providerCredentialResolution(providerActionLogin, "oidc"), &stdout, &stderr)
	if !errors.Is(err, errProviderCredentialCancelled) || stdout.Len() != 0 || stderr.String() != "Cancelled; no changes made.\n" {
		t.Fatalf("OIDC cancellation err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}

func TestProviderCredentialToolHiveHandoff(t *testing.T) {
	old := executeToolHiveLogin
	t.Cleanup(func() { executeToolHiveLogin = old })
	var noBrowser bool
	executeToolHiveLogin = func(_ context.Context, got bool) error { noBrowser = got; return nil }
	res := providerCredentialResolution(providerActionLogin, toolHiveEndpointID)
	res.remaining = []string{"--no-browser"}
	var stderr bytes.Buffer
	if err := runProviderCredentialCommand(res, &bytes.Buffer{}, &stderr); err != nil || !noBrowser {
		t.Fatalf("ToolHive handoff err=%v noBrowser=%t", err, noBrowser)
	}
	if err := runProviderCredentialCommand(providerCredentialResolution(providerActionLogout, toolHiveEndpointID), &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("ToolHive logout must remain externally owned")
	}
}

func TestProviderCredentialRejectsUnavailableProviderWithoutMutation(t *testing.T) {
	path := providerCredentialTestFile(t, "providers:\n  custom:\n    api_key: retained-secret\n")
	oldLoad, oldRead, oldUpdate := loadProviderCredentialConfig, readProviderAPIKey, updateProviderAPIKey
	t.Cleanup(func() {
		loadProviderCredentialConfig, readProviderAPIKey, updateProviderAPIKey = oldLoad, oldRead, oldUpdate
	})
	loadProviderCredentialConfig = func() (providerCredentialConfig, error) {
		return providerCredentialConfig{definitions: permconfig.ProviderDefinitions{
			"custom": {Auth: permconfig.ProviderAuth{Method: "api_key"}},
			"oidc":   {Auth: permconfig.ProviderAuth{Method: "oidc"}},
			"none":   {Auth: permconfig.ProviderAuth{Method: "none"}},
		}, authPath: path}, nil
	}
	readProviderAPIKey = func(string) (string, error) { t.Fatal("secret input must not be requested"); return "", nil }
	updates := 0
	updateProviderAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		updates++
		return authfile.CommitNotApplied, errors.New("must not update")
	}

	for _, res := range []invocationResolution{
		providerCredentialResolution(providerActionLogin, "missing"),
		providerCredentialResolution(providerActionLogout, "none"),
	} {
		if err := runProviderCredentialCommand(res, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("%s %s succeeded", res.llmAction, res.llmEndpoint)
		}
	}
	if updates != 0 || readProviderCredentialTestFile(t, path) != "providers:\n  custom:\n    api_key: retained-secret\n" {
		t.Fatal("unavailable provider command mutated credentials")
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
	return invocationResolution{mode: modeProviderCredential, llmAction: action, llmEndpoint: provider}
}

func restoreProviderCredentialSeams(t *testing.T, cfg providerCredentialConfig, input func(string) (string, error)) {
	t.Helper()
	oldLoad, oldRead, oldUpdate := loadProviderCredentialConfig, readProviderAPIKey, updateProviderAPIKey
	t.Cleanup(func() {
		loadProviderCredentialConfig, readProviderAPIKey, updateProviderAPIKey = oldLoad, oldRead, oldUpdate
	})
	loadProviderCredentialConfig = func() (providerCredentialConfig, error) { return cfg, nil }
	if input != nil {
		readProviderAPIKey = input
	}
	updateProviderAPIKey = authfile.UpdateAPIKey
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

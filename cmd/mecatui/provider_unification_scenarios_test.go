package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/app"
)

func TestProviderUnification_Scenario2_ProviderCommandGrammar(t *testing.T) {
	for _, args := range [][]string{
		{"mecatui", "providers"}, {"mecatui", "providers", "status"},
	} {
		if got := resolveInvocation(args); got.err != nil {
			t.Fatalf("resolve %v: %v", args, got.err)
		}
	}
	if got := resolveInvocation([]string{"mecatui", "llm", "status"}); got.err == nil {
		t.Fatal("legacy llm command was accepted")
	}
}

func TestProviderUnification_Scenario2_LoginUsesSelectedAuthentication(t *testing.T) {
	t.Run("API key", func(t *testing.T) {
		path := providerCredentialTestFile(t, "providers:\n  custom:\n    api_key: old-secret\n")
		definition := permconfig.ProviderDefinition{Auth: permconfig.ProviderAuth{Method: "api_key"}}
		restoreProviderCredentialSeams(t, providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": definition}, authPath: path}, func(string) (string, error) { return "replacement-secret", nil })

		if err := runProviderCredentialCommand(providerCredentialResolution(providerActionLogin, "custom"), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if got := readProviderCredentialTestFile(t, path); strings.Contains(got, "old-secret") || !strings.Contains(got, "replacement-secret") {
			t.Fatalf("API-key login did not replace local credential: %q", got)
		}
		if got := definition.Auth.Method; got != "api_key" {
			t.Fatalf("provider definition changed to auth.method %q", got)
		}
	})
	t.Run("OIDC", func(t *testing.T) {
		oldLoad, oldOpen := loadProviderCredentialConfig, openProviderOIDCRuntime
		t.Cleanup(func() { loadProviderCredentialConfig, openProviderOIDCRuntime = oldLoad, oldOpen })
		definition := permconfig.ProviderDefinition{ID: "oidc", BaseURL: "https://gateway.example", Auth: permconfig.ProviderAuth{Method: providerAuthOIDC}}
		loadProviderCredentialConfig = func() (providerCredentialConfig, error) {
			return providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"oidc": definition}}, nil
		}
		fake := &fakeProviderOIDCRuntime{}
		var opened permconfig.ProviderDefinition
		openProviderOIDCRuntime = func(_ context.Context, got permconfig.ProviderDefinition, _ bool, _ io.Writer) (nativeEndpointRuntime, error) {
			opened = got
			return fake, nil
		}

		if err := runProviderCredentialCommand(providerCredentialResolution(providerActionLogin, "oidc"), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if fake.logins != 1 || opened != definition {
			t.Fatalf("OIDC login calls=%d definition=%#v, want configured definition %#v", fake.logins, opened, definition)
		}
	})
}

func TestProviderUnification_Scenario2_LogoutPreservesProvider(t *testing.T) {
	t.Run("API key", func(t *testing.T) {
		path := providerCredentialTestFile(t, "providers:\n  custom:\n    api_key: managed-secret\n  other:\n    api_key: retained-secret\n")
		definition := permconfig.ProviderDefinition{Auth: permconfig.ProviderAuth{Method: "api_key"}}
		restoreProviderCredentialSeams(t, providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": definition}, authPath: path}, nil)

		if err := runProviderCredentialCommand(providerCredentialResolution(providerActionLogout, "custom"), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if got := readProviderCredentialTestFile(t, path); strings.Contains(got, "managed-secret") || !strings.Contains(got, "custom:") || !strings.Contains(got, "retained-secret") {
			t.Fatalf("API-key logout changed more than the local credential: %q", got)
		}
		if definition.Auth.Method != "api_key" {
			t.Fatal("API-key logout changed the provider definition")
		}
	})
	t.Run("OIDC", func(t *testing.T) {
		oldLoad, oldOpen := loadProviderCredentialConfig, openProviderOIDCRuntime
		t.Cleanup(func() { loadProviderCredentialConfig, openProviderOIDCRuntime = oldLoad, oldOpen })
		definition := permconfig.ProviderDefinition{Auth: permconfig.ProviderAuth{Method: providerAuthOIDC}}
		loadProviderCredentialConfig = func() (providerCredentialConfig, error) {
			return providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"oidc": definition}}, nil
		}
		fake := &fakeProviderOIDCRuntime{}
		openProviderOIDCRuntime = func(context.Context, permconfig.ProviderDefinition, bool, io.Writer) (nativeEndpointRuntime, error) {
			return fake, nil
		}

		if err := runProviderCredentialCommand(providerCredentialResolution(providerActionLogout, "oidc"), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if fake.logouts != 1 || definition.Auth.Method != providerAuthOIDC {
			t.Fatalf("OIDC logout calls=%d method=%q", fake.logouts, definition.Auth.Method)
		}
	})
}

func TestProviderUnification_Scenario2_RemoveDefinitionAndCredentials(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "providers")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(dir, "settings.yaml")
	if err := os.WriteFile(settings, []byte("models:\n  default_provider: custom\n  default: custom-model\nproviders:\n  custom:\n    base_url: https://custom.example\n    default_model: custom-model\n    api_flavor: openai-responses\n    auth: {method: api_key}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	authPath := providerCredentialTestFile(t, "providers:\n  custom:\n    api_key: managed-secret\n")
	restoreProviderRemoveSeams(t, providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: "api_key"}}}, authPath: authPath})
	providerSettingsPath = func() string { return settings }
	confirmations := 0
	readProviderRemovalConfirmation = func(string) (bool, error) { confirmations++; return true, nil }
	updateProviderMap = permconfig.UpdateProviderMap
	updateProviderAPIKey = authfile.UpdateAPIKey

	var stdout bytes.Buffer
	if err := runProviderRemoveCommand(providerRemoveResolution("custom"), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if confirmations != 1 || stdout.String() != "Removed custom provider \"custom\" and its locally managed credentials.\n" {
		t.Fatalf("removal confirmation/output = %d/%q", confirmations, stdout.String())
	}
	if got := readProviderCredentialTestFile(t, authPath); strings.Contains(got, "managed-secret") {
		t.Fatalf("credential remained after confirmed removal: %q", got)
	}
	data, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "custom:\n    base_url") || !strings.Contains(string(data), "default_provider: custom") {
		t.Fatalf("definition/default after removal: %s", data)
	}
	if _, _, err := app.ResolveDeploymentDefault(context.Background(), app.Config{DefaultProvider: "custom", DefaultModel: "custom-model"}); err == nil {
		t.Fatal("removed selected default remained valid at startup")
	}
	if err := runProviderRemoveCommand(providerRemoveResolution("openai"), &bytes.Buffer{}, &bytes.Buffer{}); err == nil || confirmations != 1 {
		t.Fatalf("stock removal err=%v confirmations=%d", err, confirmations)
	}
}

func TestProviderUnification_Scenario2_AddChainsLoginUnlessOptedOut(t *testing.T) {
	t.Run("default chains login", func(t *testing.T) {
		restoreProviderAddInput(t, "https://gateway.example", "1", "model-1", "1")
		restoreProviderAddWriter(t, func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
			return authfile.CommitDurable, nil
		})
		oldLoad, oldRead, oldUpdate := loadProviderCredentialConfig, readProviderAPIKey, updateProviderAPIKey
		t.Cleanup(func() {
			loadProviderCredentialConfig, readProviderAPIKey, updateProviderAPIKey = oldLoad, oldRead, oldUpdate
		})
		loadProviderCredentialConfig = func() (providerCredentialConfig, error) {
			return providerCredentialConfig{definitions: permconfig.ProviderDefinitions{"custom": {Auth: permconfig.ProviderAuth{Method: "api_key"}}}, authPath: "/safe/auth.yaml"}, nil
		}
		readProviderAPIKey = func(string) (string, error) { return "secret", nil }
		logins := 0
		updateProviderAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
			logins++
			return authfile.CommitDurable, nil
		}

		if err := runProviderAddCommand(invocationResolution{mode: modeProviderAdd, llmEndpoint: "custom"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil || logins != 1 {
			t.Fatalf("add login err=%v calls=%d", err, logins)
		}
	})
	t.Run("--no-login prints next command", func(t *testing.T) {
		restoreProviderAddInput(t, "https://gateway.example", "1", "model-1", "1")
		writes := 0
		restoreProviderAddWriter(t, func(context.Context, string, permconfig.ProviderMapUpdate) (authfile.CommitState, error) {
			writes++
			return authfile.CommitDurable, nil
		})
		var stdout bytes.Buffer
		if err := runProviderAddCommand(invocationResolution{mode: modeProviderAdd, llmEndpoint: "custom", remaining: []string{"--no-login"}}, &stdout, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if writes != 1 || stdout.String() != "Provider definition saved for \"custom\"\nNext login command: mecatui providers login custom\n" {
			t.Fatalf("--no-login writes=%d output=%q", writes, stdout.String())
		}
	})
}

func TestProviderUnification_Scenario2_ToolHiveLifecycleIsolation(t *testing.T) {
	old := executeToolHiveLogin
	t.Cleanup(func() { executeToolHiveLogin = old })
	logins := 0
	executeToolHiveLogin = func(context.Context, bool) error { logins++; return nil }
	if err := runProviderCredentialCommand(providerCredentialResolution(providerActionLogin, toolHiveEndpointID), &bytes.Buffer{}, &bytes.Buffer{}); err != nil || logins != 1 {
		t.Fatalf("ToolHive login err=%v calls=%d", err, logins)
	}
	if err := runProviderCredentialCommand(providerCredentialResolution(providerActionLogout, toolHiveEndpointID), &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "ToolHive owns this provider lifecycle") {
		t.Fatalf("ToolHive logout err=%v", err)
	}
	toolHive := toolHiveProviderStatus()
	if toolHive.Class != "external" || toolHive.Auth != "managed externally" || toolHive.Next != "use `thv llm` tooling" {
		t.Fatalf("ToolHive status = %#v", toolHive)
	}
}

func TestProviderUnification_Scenario3_ProviderHelpHierarchy(t *testing.T) {
	got := resolveInvocation([]string{"mecatui", "providers", "status", "--help"})
	if got.err != nil || got.mode != modeProviderStatus {
		t.Fatalf("status help = %+v", got)
	}
}

func TestProviderUnification_Scenario4_APIKeyFileFlagIsTheOnlySpelling(t *testing.T) {
	if got := resolveInvocation([]string{"mecatui", "--api-key-file", "auth.yaml"}); got.err != nil {
		t.Fatalf("--api-key-file did not remain a bare invocation: %v", got.err)
	}
}

func TestProviderUnification_Scenario5_ChangedTargetIsTruthful(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := authfile.UpdateAPIKey(ctx, t.TempDir()+"/auth.yaml", authfile.APIKeyUpdate{Provider: "openai", APIKey: stringPtr("secret")})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write = %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("write error disclosed secret")
	}
}

func stringPtr(v string) *string { return &v }

func TestProviderUnification_Scenario3_AC31_HelpPathsAreSuccessfulAndDedicated(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"providers", []string{"mecatui", "providers", "--help"}, "Usage: mecatui providers [status"},
		{"help providers", []string{"mecatui", "help", "providers"}, "Usage: mecatui providers [status"},
		{"status", []string{"mecatui", "providers", "status", "--help"}, "Usage: mecatui providers status [PROVIDER]"},
		{"setup", []string{"mecatui", "providers", "setup", "--help"}, "Usage: mecatui providers setup [PROVIDER]"},
		{"add", []string{"mecatui", "providers", "add", "--help"}, "Usage: mecatui providers add PROVIDER"},
		{"login", []string{"mecatui", "providers", "login", "--help"}, "Usage: mecatui providers login PROVIDER"},
		{"logout", []string{"mecatui", "providers", "logout", "--help"}, "Usage: mecatui providers logout PROVIDER"},
		{"set default", []string{"mecatui", "providers", "set-default", "--help"}, "Usage: mecatui providers set-default PROVIDER"},
		{"remove", []string{"mecatui", "providers", "remove", "--help"}, "Usage: mecatui providers remove PROVIDER"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := resolveInvocation(tt.args)
			if res.err != nil {
				t.Fatalf("resolve %v: %v", tt.args, res.err)
			}
			var stdout, stderr bytes.Buffer
			if err := runProviderCommandForHelpTest(res, &stdout, &stderr); !errors.Is(err, flag.ErrHelp) {
				t.Fatalf("help error = %v, want flag.ErrHelp", err)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), tt.want) {
				t.Fatalf("help output stdout=%q stderr=%q, want dedicated %q help", stdout.String(), stderr.String(), tt.want)
			}
		})
	}
}

func TestProviderUnification_Scenario3_AC32_StatusIsPassiveAndNeverPrintsSecrets(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("OPENAI_API_KEY", "environment-secret")
	for _, name := range []string{"ANTHROPIC_API_KEY", "OPENROUTER_API_KEY", "OPENCODE_API_KEY"} {
		t.Setenv(name, "")
	}
	authPath := filepath.Join(t.TempDir(), "provider-keys.yaml")
	auth := []byte("providers:\n  openai:\n    api_key: file-secret\n")
	if err := os.WriteFile(authPath, auth, 0o600); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(configHome, "mecatl", "settings.yaml")
	settings := []byte("credential_store:\n  api_key:\n    file: " + authPath + "\n")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, settings, 0o600); err != nil {
		t.Fatal(err)
	}

	res := resolveInvocation([]string{"mecatui", "providers", "status"})
	var stdout, stderr bytes.Buffer
	if res.err != nil || runProviderStatusCommand(res, &stdout, &stderr) != nil {
		t.Fatalf("status failed: resolution=%+v stderr=%q", res, stderr.String())
	}
	for _, secret := range []string{"environment-secret", "file-secret"} {
		if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
			t.Fatalf("status exposed %q: stdout=%q stderr=%q", secret, stdout.String(), stderr.String())
		}
	}
	if got, err := os.ReadFile(authPath); err != nil || !bytes.Equal(got, auth) {
		t.Fatalf("status changed credentials: got=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(settingsPath); err != nil || !bytes.Equal(got, settings) {
		t.Fatalf("status changed settings: got=%q err=%v", got, err)
	}
}

func TestProviderUnification_Scenario3_AC33_SetupMenuCancelsBeforeMutation(t *testing.T) {
	oldStatuses, oldChoice, oldCredentials, oldKey, oldUpdate := loadProviderStatuses, readProviderSetupField, loadProviderCredentialConfig, readProviderAPIKey, updateProviderAPIKey
	t.Cleanup(func() {
		loadProviderStatuses, readProviderSetupField = oldStatuses, oldChoice
		loadProviderCredentialConfig, readProviderAPIKey, updateProviderAPIKey = oldCredentials, oldKey, oldUpdate
	})
	loadProviderStatuses = func() ([]providerStatus, error) {
		return []providerStatus{{Name: "openai", Class: providerClassBuiltin}}, nil
	}
	readProviderSetupField = func(string) (string, error) { return "1", nil }
	loadProviderCredentialConfig = func() (providerCredentialConfig, error) {
		return providerCredentialConfig{authPath: "/safe/auth.yaml"}, nil
	}
	readProviderAPIKey = func(string) (string, error) { return "", context.Canceled }
	updateProviderAPIKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		t.Fatal("cancelled setup must not write credentials")
		return authfile.CommitNotApplied, nil
	}

	var stdout, stderr bytes.Buffer
	err := runProviderSetupCommand(invocationResolution{mode: modeProviderSetup}, &stdout, &stderr)
	if !errors.Is(err, errProviderCredentialCancelled) || !strings.Contains(stdout.String(), "1. openai (API key)") || stderr.String() != "Cancelled; no changes made.\n" {
		t.Fatalf("setup cancellation err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}

func TestProviderUnification_Scenario3_AC34_NoProviderRecoveryIsLocalOnly(t *testing.T) {
	local := config{transportMode: modeLocal, workspace: t.TempDir(), mode: "default"}
	if err := local.validate(); err == nil || !strings.Contains(err.Error(), "mecatui providers setup") || !strings.Contains(err.Error(), "mecatui connect ADDRESS") || !strings.Contains(err.Error(), "remote mecated's provider configuration is managed by its operator") {
		t.Fatalf("local no-provider recovery = %v", err)
	}
	remote := config{transportMode: modeConnect, connectAddress: "mecated.example:443", mode: "default"}
	if err := remote.validate(); err != nil {
		t.Fatalf("remote connect must not require local provider setup: %v", err)
	}
}

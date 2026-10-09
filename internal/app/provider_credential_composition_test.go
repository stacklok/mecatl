package app_test

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

type credentialStoreDiagnostics struct {
	mu    sync.Mutex
	lines []string
}

func (d *credentialStoreDiagnostics) Log(_ context.Context, _ port.Level, message string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	line := message
	for _, arg := range args {
		if value, ok := arg.(string); ok {
			line += " " + value
		}
	}
	d.lines = append(d.lines, line)
}

func (d *credentialStoreDiagnostics) With(...any) port.Diagnostics { return d }

func (d *credentialStoreDiagnostics) contains(s string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.Contains(strings.Join(d.lines, "\n"), s)
}

func TestBuildLoadsExaCredentialStoreAPIKeyFile(t *testing.T) {
	xdgConfigHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgConfigHome)
	t.Setenv("EXA_API_KEY", "")

	credentialFile := filepath.Join(xdgConfigHome, "exa-auth.yaml")
	if err := os.WriteFile(credentialFile, []byte("providers:\n  exa:\n    api_key: paid-exa-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	settingsDir := filepath.Join(xdgConfigHome, "mecatl")
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "auth.yaml"), []byte("providers:\n  exa:\n    api_key: ambient-exa-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.yaml"), []byte("credential_store:\n  api_key:\n    file: "+credentialFile+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	flags := cliconfig.RegisterProviderFlags(flag.NewFlagSet("test", flag.ContinueOnError), cliconfig.ProviderFlagHelp{})
	loader := cliconfig.NewProviderCredentialResolver(flags, flags.Resolve())
	diagnostics := &credentialStoreDiagnostics{}
	built, err := app.Build(t.Context(), app.Config{
		Workspace:                t.TempDir(),
		UserModelDir:             t.TempDir(),
		Model:                    "mock",
		MockProvider:             mockllm.New(),
		NoSoul:                   true,
		PermissionsConventional:  true,
		ProviderCredentialLoader: loader,
		Diagnostics:              diagnostics,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()

	if !diagnostics.contains("WebSearch ENABLED with Exa backend") || !diagnostics.contains("authenticated/paid") {
		t.Fatal("credential_store.api_key.file did not produce a paid Exa search provider")
	}
	credentials, _, err := loader.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.ExaAPIKey != "paid-exa-key" {
		t.Fatal("credential_store.api_key.file did not override conventional auth.yaml")
	}
}

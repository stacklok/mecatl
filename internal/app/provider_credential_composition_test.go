package app_test

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
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

func TestBuildLoadsSearchCredentialStoreAPIKeyFile(t *testing.T) {
	const exaKey = "paid-exa-key"
	const braveKey = "paid-brave-key"
	const webSearchKey = "generic-websearch-key"
	var receivedWebSearchKey string
	var requestMu sync.Mutex
	searchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestMu.Lock()
		receivedWebSearchKey = r.Header.Get("X-Search-Key")
		requestMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer searchServer.Close()

	xdgConfigHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgConfigHome)
	t.Setenv("EXA_API_KEY", "")
	t.Setenv("BRAVE_API_KEY", "")
	t.Setenv("WEBSEARCH_API_KEY", "")

	credentialFile := filepath.Join(xdgConfigHome, "search-auth.yaml")
	if err := os.WriteFile(credentialFile, []byte("search:\n  exa:\n    api_key: "+exaKey+"\n  brave:\n    api_key: "+braveKey+"\n  websearch:\n    api_key: "+webSearchKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	settingsDir := filepath.Join(xdgConfigHome, "mecatl")
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "auth.yaml"), []byte("providers:\n  poisoned:\n    api_key: should-not-be-read\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(settingsDir, "settings.yaml")
	settingsBody := "credential_store:\n  api_key:\n    file: " + credentialFile + "\nwebsearch:\n  url: " + searchServer.URL + "\n  auth_header: X-Search-Key\n"
	if err := os.WriteFile(settings, []byte(settingsBody), 0o600); err != nil {
		t.Fatal(err)
	}

	flags := cliconfig.RegisterProviderFlags(flag.NewFlagSet("test", flag.ContinueOnError), cliconfig.ProviderFlagHelp{})
	keys := flags.Resolve()
	loader := cliconfig.NewProviderCredentialResolver(cliconfig.NewCredentialSource(flags, keys), keys)
	diagnostics := &credentialStoreDiagnostics{}
	built, err := app.Build(t.Context(), app.Config{
		Workspace:    t.TempDir(),
		UserModelDir: t.TempDir(),
		Model:        "mock",
		MockProvider: mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("search", "WebSearch", json.RawMessage(`{"query":"test query"}`))),
			mockllm.TextTurn("done"),
		),
		NoSoul:                   true,
		PermissionsConventional:  true,
		PermissionConfigs:        []string{settings},
		ProviderCredentialLoader: loader,
		Diagnostics:              diagnostics,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()

	if _, _, err := runAuthFileMCPTool(t.Context(), built, "search"); err != nil {
		t.Fatalf("run generic web search: %v", err)
	}
	requestMu.Lock()
	gotWebSearchKey := receivedWebSearchKey
	requestMu.Unlock()
	if gotWebSearchKey != webSearchKey {
		t.Fatal("settings-selected credential file did not deliver the generic web search key")
	}
	if diagnostics.contains(exaKey) || diagnostics.contains(braveKey) || diagnostics.contains(webSearchKey) {
		t.Fatal("credential_store.api_key.file leaked a search credential to diagnostics")
	}
	credentials, _, err := loader.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.ExaAPIKey != exaKey || credentials.BraveAPIKey != braveKey || credentials.WebSearchAPIKey != webSearchKey {
		t.Fatal("credential_store.api_key.file did not override the poisoned conventional auth.yaml")
	}
}

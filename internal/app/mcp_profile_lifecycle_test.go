package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

type countingMCPProfileCloser struct {
	calls   atomic.Int32
	onClose func()
}

func (c *countingMCPProfileCloser) Close() error {
	c.calls.Add(1)
	if c.onClose != nil {
		c.onClose()
	}
	return nil
}

func TestBuildOwnsMCPProfileLifecycleExactlyOnce(t *testing.T) {
	closer := new(countingMCPProfileCloser)
	built, err := buildIsolated(t, context.Background(), Config{
		Workspace: t.TempDir(), Model: "mock", MockProvider: mockllm.New(),
		MCPProfileLifecycle: closer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if closer.calls.Load() != 0 {
		t.Fatal("profile lifecycle closed before Built.Close")
	}
	built.Close()
	built.Close()
	if got := closer.calls.Load(); got != 1 {
		t.Fatalf("profile lifecycle Close calls = %d, want 1", got)
	}
}

type capturingMCPProfileLoader struct {
	calls    int
	operator *permconfig.MCPSection
	closer   *countingMCPProfileCloser
}

func (l *capturingMCPProfileLoader) Load(operator *permconfig.MCPSection) ([]mcp.ServerConfig, interface{ Close() error }, error) {
	l.calls++
	l.operator = operator
	return nil, l.closer, nil
}

func TestBuildUsesExistingOperatorResolverForMCPProfiles(t *testing.T) {
	settings := t.TempDir() + "/settings.yaml"
	if err := os.WriteFile(settings, []byte("mcp:\n  servers:\n    - name: public\n      url: https://mcp.example.com/mcp\n      auth:\n        mode: none\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	closer := new(countingMCPProfileCloser)
	loader := &capturingMCPProfileLoader{closer: closer}
	built, err := buildIsolated(t, context.Background(), Config{
		Workspace: t.TempDir(), Model: "mock", MockProvider: mockllm.New(),
		PermissionConfigs: []string{settings}, MCPProfileLoader: loader,
	})
	if err != nil {
		t.Fatal(err)
	}
	if loader.calls != 1 || loader.operator == nil || len(loader.operator.Servers) != 1 || loader.operator.Servers[0].Name != "public" {
		t.Fatalf("loader calls=%d operator=%#v", loader.calls, loader.operator)
	}
	built.Close()
	if closer.calls.Load() != 1 {
		t.Fatalf("profile lifecycle close calls = %d", closer.calls.Load())
	}
}

func TestBuildClosesMCPManagerBeforeProfileSources(t *testing.T) {
	url, deletes := newMCPTestServerCounting(t)
	managerClosedFirst := false
	closer := &countingMCPProfileCloser{onClose: func() {
		managerClosedFirst = atomic.LoadInt32(deletes) > 0
	}}
	built, err := buildIsolated(t, context.Background(), Config{
		Workspace: t.TempDir(), Model: "mock", MockProvider: mockllm.New(),
		MCPServers: []mcp.ServerConfig{{Name: "ordered", URL: url}}, MCPProfileLifecycle: closer,
	})
	if err != nil {
		t.Fatal(err)
	}
	built.Close()
	if !managerClosedFirst {
		t.Fatal("profile source closed before the global MCP manager terminated its session")
	}
}

type missingCredentialReader struct{}

func (missingCredentialReader) Get(context.Context, []byte) (credentialstore.Record, error) {
	return credentialstore.Record{}, credentialstore.ErrNotFound
}
func (missingCredentialReader) Capabilities() credentialstore.Capabilities {
	return credentialstore.Capabilities{}
}
func (missingCredentialReader) Close() error { return nil }

func TestBuildMCPLoginRequiredDiagnosticsAreModeSpecificAndRedacted(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer scope="read"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(authServer.Close)
	url := authServer.URL
	backend := credentialstore.NewMemoryBackend()
	local, err := backend.Open("login-required")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	healthyURL := newMCPTestServer(t)
	base := mcp.OAuthOptions{
		Subject: mcp.OAuthSubject{Profile: "work", Principal: "operator"}, Issuer: url,
		Client:      mcp.OAuthClientConfig{ClientIDMetadataDocumentURL: "https://client.example/metadata.json"},
		RedirectURL: url + "/callback", AllowedScopes: []string{"read"},
		Network: mcp.OAuthNetworkPolicy{AdditionalOrigins: []string{"https://client.example"}, PrivateOrigins: []string{url}},
	}
	mcp.AllowOAuthLoopbackForTest(t, &base)
	for _, test := range []struct {
		name, message, remedy string
		configure             func(*mcp.OAuthOptions)
	}{
		{name: "local", message: "MCP OAuth login required", remedy: "mecated mcp login local", configure: func(o *mcp.OAuthOptions) { o.CredentialStore = local }},
		{name: "environment", message: "MCP OAuth environment credential unavailable", remedy: "preprovision the environment credential and restart", configure: func(o *mcp.OAuthOptions) { o.CredentialReader = missingCredentialReader{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			diag := &kvDiag{}
			oauth := base
			test.configure(&oauth)
			built, err := buildIsolated(t, context.Background(), Config{
				Workspace: t.TempDir(), Model: "mock", MockProvider: mockllm.New(), Diagnostics: diag,
				MCPServers: []mcp.ServerConfig{
					{Name: "healthy", URL: healthyURL},
					{Name: test.name, URL: url, OAuth: &oauth},
				},
			})
			if err != nil {
				t.Fatalf("Build failed instead of degrading: %v", err)
			}
			defer built.Close()
			if built.Service == nil {
				t.Fatal("Build returned an unusable service")
			}
			joined := fmt.Sprint(diag.msgs, diag.args)
			if !strings.Contains(joined, test.message) || !strings.Contains(joined, test.remedy) {
				t.Fatalf("diagnostics = %s, want remedy %q", joined, test.remedy)
			}
			connected := false
			for i, msg := range diag.msgs {
				if msg != "MCP servers connected" {
					continue
				}
				connected = true
				for j := 0; j+1 < len(diag.args[i]); j += 2 {
					if diag.args[i][j] == "servers" && diag.args[i][j+1] != 1 {
						t.Fatalf("connected servers = %v, want 1", diag.args[i][j+1])
					}
				}
			}
			if !connected {
				t.Fatal("missing MCP connected diagnostic")
			}
			for _, canary := range []string{url, "MECATL_SECRET_REF", "token-canary", "adapter-error-canary"} {
				if strings.Contains(joined, canary) {
					t.Fatalf("diagnostics leaked canary %q: %s", canary, joined)
				}
			}
		})
	}
}

func TestConnectMCPOverLimitGracefullyDegrades(t *testing.T) {
	configs := make([]mcp.ServerConfig, maxMCPReconcileServers+1)
	for i := range configs {
		configs[i] = mcp.ServerConfig{Name: fmt.Sprintf("over-limit-%d", i), URL: "not-used"}
	}
	diag := &kvDiag{}
	mgr, provider, inventory, _, _, revision, _, closeRuntime := connectMCP(context.Background(), Config{MCPServers: configs, Diagnostics: diag})
	defer closeRuntime()

	if mgr != nil || provider == nil || revision != 0 {
		t.Fatalf("over-limit startup = (manager=%v, provider=%v, revision=%d), want no manager with a usable degraded provider", mgr, provider, revision)
	}
	if len(inventory) != 1 || len(inventory[0].Servers) != len(configs) {
		t.Fatalf("over-limit startup inventory = %+v", inventory)
	}
	joined := fmt.Sprint(diag.msgs, diag.args)
	if !strings.Contains(joined, "MCP manager construction failed; continuing without MCP tools") {
		t.Fatalf("over-limit startup diagnostics = %s", joined)
	}
}

func TestLogMCPReconcileConnectErrorReportsEveryFailedProfile(t *testing.T) {
	diag := &kvDiag{}
	configs := []mcp.ServerConfig{
		{Name: "login"},
		{Name: "environment", OAuth: &mcp.OAuthOptions{CredentialReader: missingCredentialReader{}}},
	}
	logMCPReconcileConnectError(context.Background(), Config{Diagnostics: diag}, configs, map[string]error{
		"login":       mcp.ErrOAuthLoginRequired,
		"environment": mcp.ErrOAuthUnavailable,
	}, errors.New("partial MCP connection failure"))

	joined := fmt.Sprint(diag.msgs, diag.args)
	for _, want := range []string{
		"MCP OAuth login required", "mecated mcp login login",
		"MCP OAuth environment credential unavailable", "preprovision the environment credential and restart",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("diagnostics = %s, missing %q", joined, want)
		}
	}
}

func TestBuildFailureAfterProfileLoadClosesReturnedLifecycleOnce(t *testing.T) {
	closer := new(countingMCPProfileCloser)
	loader := &capturingMCPProfileLoader{closer: closer}
	if _, err := buildIsolated(t, context.Background(), Config{MCPProfileLoader: loader}); err == nil {
		t.Fatal("Build without provider succeeded")
	}
	if loader.calls != 1 {
		t.Fatalf("profile loader calls = %d, want 1", loader.calls)
	}
	if got := closer.calls.Load(); got != 1 {
		t.Fatalf("returned lifecycle Close calls = %d, want 1", got)
	}
}

func TestBuildFailureClosesMCPProfileLifecycle(t *testing.T) {
	closer := new(countingMCPProfileCloser)
	if _, err := buildIsolated(t, context.Background(), Config{MCPProfileLifecycle: closer}); err == nil {
		t.Fatal("Build without provider succeeded")
	}
	if got := closer.calls.Load(); got != 1 {
		t.Fatalf("profile lifecycle Close calls = %d, want 1", got)
	}
}

// TestBuildWarnsWhenOperatorMCPConfiguredWithoutLoader pins the WARN for an
// otherwise-silent drop: an operator-tier mcp.servers block with
// no MCPProfileLoader wired must surface a settings-only diagnostic so any
// consumer omitting the loader sees the ignored servers.
func TestBuildWarnsWhenOperatorMCPConfiguredWithoutLoader(t *testing.T) {
	settings := t.TempDir() + "/settings.yaml"
	if err := os.WriteFile(settings, []byte("mcp:\n  servers:\n    - name: public\n      url: https://mcp.example.com/mcp\n      auth:\n        mode: none\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diag := &kvDiag{}
	built, err := buildIsolated(t, context.Background(), Config{
		Workspace: t.TempDir(), Model: "mock", MockProvider: mockllm.New(),
		PermissionConfigs: []string{settings}, Diagnostics: diag,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	want := "operator-tier mcp.servers configured"
	found := false
	for i, msg := range diag.msgs {
		if strings.Contains(msg, want) {
			found = true
			if diag.lvl[i] != port.LevelWarn {
				t.Fatalf("log level = %v, want WARN", diag.lvl[i])
			}
			break
		}
	}
	if !found {
		t.Fatalf("diagnostics = %v, want a WARN containing %q", diag.msgs, want)
	}
}

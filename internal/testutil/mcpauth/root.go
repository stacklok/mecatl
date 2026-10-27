// Package mcpauth exercises static credentials through command-root factories.
package mcpauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

const missingLegacySource = "missing legacy"

// CheckRoot uses the real root's flag parsing and factory, not hand-wired loaders.
func CheckRoot(t *testing.T, factory func(*testing.T, string, port.Diagnostics) app.Config) {
	t.Helper()
	t.Run("CLI file snapshot", func(t *testing.T) {
		checkCLIAuthFileSnapshot(t, factory)
	})
	for _, source := range []string{"file", "environment", "environment over file", "legacy environment", missingLegacySource} {
		t.Run(source, func(t *testing.T) {
			checkRootScenario(t, factory, source)
		})
	}
}

func checkCLIAuthFileSnapshot(t *testing.T, factory func(*testing.T, string, port.Diagnostics) app.Config) {
	t.Helper()
	resetRootEnvironment(t)
	path := writeRootAuthFile(t, "selected-file-token")
	cfg := factory(t, path, port.NopDiagnostics{})
	// Settings cannot displace the actual root's explicit CLI file.
	cfg.ProviderCredentialLoader.(interface{ SetAPIKeyFile(string) }).SetAPIKeyFile(filepath.Join(t.TempDir(), "not-selected"))
	if err := os.WriteFile(path, []byte("mcp: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	operator := &permconfig.MCPSection{Mode: "global", Servers: []permconfig.MCPServerProfile{{Name: "server", URL: "https://mcp.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "static_bearer", StaticBearer: &permconfig.MCPStaticBearerProfile{TokenRef: "root_fixture"}}}}}
	for _, authority := range []bool{false, true} {
		servers, lifecycle, err := loadRootProfiles(cfg, operator, authority)
		if err != nil {
			t.Fatal(err)
		}
		_ = lifecycle.Close()
		if len(servers) != 1 || servers[0].Headers["Authorization"] != "Bearer selected-file-token" {
			t.Fatalf("root lost CLI snapshot (authority=%v)", authority)
		}
	}
}

func checkRootScenario(t *testing.T, factory func(*testing.T, string, port.Diagnostics) app.Config, source string) {
	resetRootEnvironment(t)
	token, fileToken, selector := rootScenarioConfig(t, source)
	path := writeRootAuthFile(t, fileToken)
	var logs bytes.Buffer
	diag := slogdiag.NewText(&logs)
	cfg := factory(t, path, diag)
	var calls, requests atomic.Int32
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "root-fixture", Version: "1"}, nil)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "ready"}, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
		calls.Add(1)
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "ready"}}}, nil, nil
	})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("destination received altered or wrong-source bearer")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer destination.Close()
	operator := &permconfig.MCPSection{Mode: "global", Servers: []permconfig.MCPServerProfile{{Name: "protected", URL: destination.URL, Auth: permconfig.MCPAuthProfile{Mode: "static_bearer", StaticBearer: selector}}}}
	runRootLoaders(t, cfg, path, operator, source, diag)
	verifyRootLoads(t, source, calls.Load(), requests.Load())
	verifyRootDiagnostics(t, source, logs.String())
	verifyRootFreshStartup(t, factory, path, diag, operator, source, &logs)
}

func resetRootEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, name := range []string{"MECATL_MCP_TOKEN_ROOT_FIXTURE", "MECATL_ROOT_LEGACY"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

func rootScenarioConfig(t *testing.T, source string) (string, string, *permconfig.MCPStaticBearerProfile) {
	t.Helper()
	// #nosec G101 -- Offline test sentinel verifies bearer preservation.
	const token = " \troot-canary\t opaque token"
	fileToken := token
	selector := &permconfig.MCPStaticBearerProfile{TokenRef: "root_fixture"}
	switch source {
	case "environment":
		t.Setenv("MECATL_MCP_TOKEN_ROOT_FIXTURE", token)
		fileToken = ""
	case "environment over file":
		t.Setenv("MECATL_MCP_TOKEN_ROOT_FIXTURE", token)
		// #nosec G101 -- Offline test sentinel must lose to the environment.
		fileToken = "unused-file-secret"
	case "legacy environment", missingLegacySource:
		// #nosec G101 -- Offline test names the deprecated environment selector.
		selector = &permconfig.MCPStaticBearerProfile{TokenEnv: "MECATL_ROOT_LEGACY"}
		// #nosec G101 -- Offline test sentinel is not a credential.
		fileToken = "unused-file-secret"
		if source == "legacy environment" {
			t.Setenv("MECATL_ROOT_LEGACY", token)
		}
	}
	return token, fileToken, selector
}

func writeRootAuthFile(t *testing.T, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.yaml")
	body := "mcp: {}\n"
	if token != "" {
		encoded, _ := json.Marshal(token)
		body = "mcp:\n  root_fixture:\n    token: " + string(encoded) + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runRootLoaders(t *testing.T, cfg app.Config, path string, operator *permconfig.MCPSection, source string, diag port.Diagnostics) {
	t.Helper()
	for _, authority := range []bool{false, true} {
		servers, lifecycle, err := loadRootProfiles(cfg, operator, authority)
		if source == missingLegacySource {
			if !errors.Is(err, cliconfig.ErrMCPProfileSecret) {
				t.Fatalf("missing legacy error: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		manager, err := mcp.NewManager(ctx, servers, func(_ mcp.ServerConfig, err error) { t.Errorf("connect: %v", err) }, diag)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		result, err := manager.CallTool(ctx, "protected", "ready", json.RawMessage(`{}`))
		if err != nil || result.IsError {
			t.Errorf("root tool call failed: %v", err)
		}
		_ = manager.Close()
		_ = lifecycle.Close()
		cancel()
		// Both loader paths must keep the factory's startup snapshot.
		t.Setenv("MECATL_MCP_TOKEN_ROOT_FIXTURE", "rotated-env")
		t.Setenv("MECATL_ROOT_LEGACY", "rotated-legacy")
		if err := os.WriteFile(path, []byte("mcp:\n  root_fixture:\n    token: rotated-file\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func loadRootProfiles(cfg app.Config, operator *permconfig.MCPSection, authority bool) ([]mcp.ServerConfig, interface{ Close() error }, error) {
	if !authority {
		return cfg.MCPProfileLoader.Load(operator)
	}
	result, err := cfg.MCPAuthorityLoader.LoadAuthority(operator, cfg.MCPAuthorityDefault, cfg.MCPBrokerSupported)
	if err != nil {
		return nil, nil, err
	}
	servers, lifecycle, ok := result.Global()
	if !ok {
		return nil, nil, errors.New("explicit global authority lost")
	}
	return servers, lifecycle, nil
}

func verifyRootLoads(t *testing.T, source string, calls, requests int32) {
	t.Helper()
	if source == missingLegacySource {
		if requests != 0 {
			t.Fatal("missing secret reached transport")
		}
	} else if calls != 2 {
		t.Fatal("both real loader paths must deliver tool success")
	}
}

func verifyRootDiagnostics(t *testing.T, source, logs string) {
	t.Helper()
	wantWarnings := 0
	if strings.Contains(source, "legacy") {
		wantWarnings = 1
	}
	if strings.Count(logs, "deprecated") != wantWarnings {
		t.Fatal("root warning missing or duplicated")
	}
	if strings.Contains(logs, "root-canary") || strings.Contains(logs, "MECATL_ROOT_LEGACY") {
		t.Fatal("root diagnostics exposed credential")
	}
}

func verifyRootFreshStartup(t *testing.T, factory func(*testing.T, string, port.Diagnostics) app.Config, path string, diag port.Diagnostics, operator *permconfig.MCPSection, source string, logs *bytes.Buffer) {
	t.Helper()
	wantFresh := rootFreshAuthorization(t, source)
	fresh := factory(t, path, diag)
	configs, closer, err := fresh.MCPProfileLoader.Load(operator)
	if closer != nil {
		_ = closer.Close()
	}
	if source == missingLegacySource {
		if !errors.Is(err, cliconfig.ErrMCPProfileSecret) {
			t.Fatal("new startup lost missing-secret failure")
		}
	} else if err != nil || len(configs) != 1 || configs[0].Headers["Authorization"] != wantFresh {
		t.Fatal("new startup did not capture the rotated source")
	}
	wantWarnings := 0
	if strings.Contains(source, "legacy") {
		wantWarnings = 1
	}
	if strings.Count(logs.String(), "deprecated") != 2*wantWarnings {
		t.Fatal("new startup warning count changed")
	}
}

func rootFreshAuthorization(t *testing.T, source string) string {
	t.Helper()
	switch source {
	case "file":
		if err := os.Unsetenv("MECATL_MCP_TOKEN_ROOT_FIXTURE"); err != nil {
			t.Fatal(err)
		}
		return "Bearer rotated-file"
	case "legacy environment":
		return "Bearer rotated-legacy"
	default:
		return "Bearer rotated-env"
	}
}

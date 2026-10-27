package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestMecak8sMCPAuthFileComposition(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "selected.yaml")
	if err := os.WriteFile(path, []byte("mcp:\n  exact:\n    token: selected-file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseFlags([]string{"--api-key-file", path})
	if err != nil {
		t.Fatal(err)
	}
	ac := appConfig(cfg, port.NopDiagnostics{}, observability{})
	ac.ProviderCredentialLoader.(interface{ SetAPIKeyFile(string) }).SetAPIKeyFile(filepath.Join(t.TempDir(), "not-selected"))
	if err := os.WriteFile(path, []byte("mcp: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Explicit global mode is required for static bearer on the broker-default root.
	operator := &permconfig.MCPSection{Mode: "global", Servers: []permconfig.MCPServerProfile{{Name: "server", URL: "https://mcp.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "static_bearer", StaticBearer: &permconfig.MCPStaticBearerProfile{TokenRef: "exact"}}}}}
	servers, lifecycle, err := ac.MCPProfileLoader.Load(operator)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()
	if len(servers) != 1 || servers[0].Headers["Authorization"] != "Bearer selected-file-token" {
		t.Fatal("direct loader lost the root credential snapshot")
	}
	authority, err := ac.MCPAuthorityLoader.LoadAuthority(operator, ac.MCPAuthorityDefault, ac.MCPBrokerSupported)
	if err != nil {
		t.Fatal(err)
	}
	servers, lifecycle, ok := authority.Global()
	if !ok {
		t.Fatal("expected explicit global authority")
	}
	defer lifecycle.Close()
	if len(servers) != 1 || servers[0].Headers["Authorization"] != "Bearer selected-file-token" {
		t.Fatal("authority loader lost the root credential snapshot")
	}
}

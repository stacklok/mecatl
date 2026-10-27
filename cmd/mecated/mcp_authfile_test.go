package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestMecatedMCPAuthFileComposition(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "selected.yaml")
	if err := os.WriteFile(path, []byte("mcp:\n  exact:\n    token: selected-file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseFlagsMode(modeServe, []string{"--api-key-file", path})
	if err != nil {
		t.Fatal(err)
	}
	ac := appConfig(cfg, nil, nil, nil, nil, port.NopDiagnostics{})
	// A settings-selected source cannot displace the root's explicit CLI file.
	ac.ProviderCredentialLoader.(interface{ SetAPIKeyFile(string) }).SetAPIKeyFile(filepath.Join(t.TempDir(), "not-selected"))
	if err := os.WriteFile(path, []byte("mcp: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
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
		t.Fatal("expected global authority")
	}
	defer lifecycle.Close()
	if len(servers) != 1 || servers[0].Headers["Authorization"] != "Bearer selected-file-token" {
		t.Fatal("authority loader lost the root credential snapshot")
	}
}

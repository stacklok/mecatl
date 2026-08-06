package main

import (
	"testing"

	"github.com/stacklok/mecatl/engine/port"
)

// TestParseFlagsMCPServer covers the factory MCP wiring (issue #341) on
// mecak8s: the shared --mcp-server flag (cliconfig.MCPServerList) is
// registered, the MCP_<NAME>_TOKEN convention resolves a bearer header,
// repeats accumulate in order, a malformed value is a parse error — and
// appConfig threads the list onto app.Config.MCPServers.
func TestParseFlagsMCPServer(t *testing.T) {
	t.Run("name=URL with token env maps onto app.Config.MCPServers", func(t *testing.T) {
		t.Setenv("MCP_VMCP_TOKEN", "per-run-token")
		cfg, err := parseFlags([]string{
			"--mcp-server", "vmcp=https://vmcp.internal/mcp",
			"--mcp-server", "tequitl=https://tequitl.internal/mcp",
		})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		ac := appConfig(cfg, port.NopDiagnostics{}, observability{})
		if len(ac.MCPServers) != 2 {
			t.Fatalf("app.Config.MCPServers len = %d, want 2", len(ac.MCPServers))
		}
		if ac.MCPServers[0].Name != "vmcp" || ac.MCPServers[1].Name != "tequitl" {
			t.Errorf("order not preserved: %q then %q", ac.MCPServers[0].Name, ac.MCPServers[1].Name)
		}
		if auth := ac.MCPServers[0].Headers["Authorization"]; auth != "Bearer per-run-token" {
			t.Errorf("vmcp Authorization = %q, want the MCP_VMCP_TOKEN bearer", auth)
		}
		if ac.MCPServers[1].Headers != nil {
			t.Errorf("tequitl Headers = %v, want nil (token optional)", ac.MCPServers[1].Headers)
		}
	})

	t.Run("bad shape is a parse error", func(t *testing.T) {
		if _, err := parseFlags([]string{"--mcp-server", "noequals"}); err == nil {
			t.Fatal("parseFlags with a malformed --mcp-server: want error, got nil")
		}
	})

	t.Run("unset leaves MCPServers empty", func(t *testing.T) {
		cfg, err := parseFlags(nil)
		if err != nil {
			t.Fatalf("parseFlags(nil): %v", err)
		}
		if got := appConfig(cfg, port.NopDiagnostics{}, observability{}).MCPServers; len(got) != 0 {
			t.Errorf("MCPServers = %v, want empty", got)
		}
	})
}

// TestParseFlagsMCPServerInsecureHTTP covers the issue-#358 per-server opt-in
// end-to-end through mecak8s's parseFlags (which runs the post-parse
// Finalize): order-independent relaxation of the token-bearing http scheme
// gate, PER NAME — a relaxation for one server must not relax another.
func TestParseFlagsMCPServerInsecureHTTP(t *testing.T) {
	t.Run("relaxation is order-independent", func(t *testing.T) {
		t.Setenv("MCP_TEQUITL_TOKEN", "per-run-token")
		for name, argv := range map[string][]string{
			"before": {"--mcp-server-insecure-http", "tequitl", "--mcp-server", "tequitl=http://tequitl.internal/mcp"},
			"after":  {"--mcp-server", "tequitl=http://tequitl.internal/mcp", "--mcp-server-insecure-http", "tequitl"},
		} {
			cfg, err := parseFlags(argv)
			if err != nil {
				t.Fatalf("parseFlags (%s): %v", name, err)
			}
			ac := appConfig(cfg, port.NopDiagnostics{}, observability{})
			if len(ac.MCPServers) != 1 || ac.MCPServers[0].Headers["Authorization"] != "Bearer per-run-token" {
				t.Errorf("(%s) MCPServers = %+v, want the bearer attached under the relaxation", name, ac.MCPServers)
			}
		}
	})

	t.Run("per-name scope: relaxing tequitl does not relax vmcp", func(t *testing.T) {
		t.Setenv("MCP_TEQUITL_TOKEN", "tok-a")
		t.Setenv("MCP_VMCP_TOKEN", "tok-b")
		_, err := parseFlags([]string{
			"--mcp-server", "tequitl=http://tequitl.internal/mcp",
			"--mcp-server", "vmcp=http://vmcp.internal/mcp",
			"--mcp-server-insecure-http", "tequitl",
		})
		if err == nil {
			t.Fatal("unrelaxed vmcp (token + http off-host): want error, got nil")
		}
	})

	t.Run("default posture unchanged: no relaxation still fails", func(t *testing.T) {
		t.Setenv("MCP_VMCP_TOKEN", "per-run-token")
		if _, err := parseFlags([]string{"--mcp-server", "vmcp=http://vmcp.internal/mcp"}); err == nil {
			t.Fatal("token-bearing http off-host without the opt-in: want error, got nil")
		}
	})
}

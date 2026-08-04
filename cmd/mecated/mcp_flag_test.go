package main

import (
	"testing"
)

// TestParseFlagsMCPServer pins mecated's --mcp-server behavior ACROSS the
// issue-#341 extraction into cliconfig.MCPServerList: name=URL parses, the
// MCP_<NAME>_TOKEN env resolves to a bearer header, a missing token leaves
// Headers nil, and a malformed value is a parse error — byte-for-byte the
// semantics the inline mcpServerList type had.
func TestParseFlagsMCPServer(t *testing.T) {
	t.Run("name=URL with token env", func(t *testing.T) {
		t.Setenv("MCP_GITHUB_TOKEN", "ghp-token")
		cfg, err := parseFlags([]string{
			"--mcp-server", "github=https://mcp.github.internal/v1",
			"--mcp-server", "slack=https://mcp.slack.internal/v1",
		})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		servers := cfg.mcpServers.Servers()
		if len(servers) != 2 {
			t.Fatalf("mcpServers len = %d, want 2", len(servers))
		}
		if servers[0].Name != "github" || servers[0].URL != "https://mcp.github.internal/v1" {
			t.Errorf("first server = %+v", servers[0])
		}
		if auth := servers[0].Headers["Authorization"]; auth != "Bearer ghp-token" {
			t.Errorf("github Authorization = %q, want the MCP_GITHUB_TOKEN bearer", auth)
		}
		if servers[1].Headers != nil {
			t.Errorf("slack Headers = %v, want nil (token optional)", servers[1].Headers)
		}
	})

	t.Run("bad shape is a parse error", func(t *testing.T) {
		if _, err := parseFlags([]string{"--mcp-server", "noequals"}); err == nil {
			t.Fatal("parseFlags with a malformed --mcp-server: want error, got nil")
		}
	})
}

// TestParseFlagsMCPServerInsecureHTTP covers the issue-#358 per-server opt-in
// end-to-end through mecated's parseFlags (which runs the post-parse
// Finalize): the relaxation is order-independent, and the default posture is
// unchanged without it.
func TestParseFlagsMCPServerInsecureHTTP(t *testing.T) {
	t.Run("relaxation is order-independent", func(t *testing.T) {
		t.Setenv("MCP_GITHUB_TOKEN", "ghp-token")
		for name, argv := range map[string][]string{
			"before": {"--mcp-server-insecure-http", "github", "--mcp-server", "github=http://mcp.github.internal/v1"},
			"after":  {"--mcp-server", "github=http://mcp.github.internal/v1", "--mcp-server-insecure-http", "github"},
		} {
			cfg, err := parseFlags(argv)
			if err != nil {
				t.Fatalf("parseFlags (%s): %v", name, err)
			}
			servers := cfg.mcpServers.Servers()
			if len(servers) != 1 || servers[0].Headers["Authorization"] != "Bearer ghp-token" {
				t.Errorf("(%s) servers = %+v, want the bearer attached under the relaxation", name, servers)
			}
		}
	})

	t.Run("default posture unchanged: no relaxation still fails", func(t *testing.T) {
		t.Setenv("MCP_GITHUB_TOKEN", "ghp-token")
		if _, err := parseFlags([]string{"--mcp-server", "github=http://mcp.github.internal/v1"}); err == nil {
			t.Fatal("token-bearing http off-host without the opt-in: want error, got nil")
		}
	})

	t.Run("stale relaxation (unknown name) is a loud error", func(t *testing.T) {
		if _, err := parseFlags([]string{"--mcp-server-insecure-http", "ghost"}); err == nil {
			t.Fatal("relaxation for an unregistered name: want error, got nil")
		}
	})
}

package permconfig

import (
	"strings"
	"testing"
)

func TestDirectMCPOnboarding_Scenario3_AtomicNarrowMutation(t *testing.T) {
	before := []byte("# preserve me\nmodels:\n  default: test\n")
	added, err := AddDirectMCPServer(before, "Calendar", "https://mcp.example/mcp", "https://issuer.example", "/tmp/credentials", "MECATL_MCP_CREDENTIAL_KEY")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(added), "# preserve me") || !strings.Contains(string(added), "name: 'Calendar'") {
		t.Fatalf("unrelated comment or server missing:\n%s", added)
	}
	removed, err := RemoveDirectMCPServer(added, "calendar")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(removed), "# preserve me") || strings.Contains(string(removed), "Calendar") {
		t.Fatalf("remove changed unrelated content or retained server:\n%s", removed)
	}
}

func TestDirectMCPMutationRejectsDuplicateFoldedName(t *testing.T) {
	data := []byte("mcp:\n  servers:\n    - name: Calendar\n      url: https://mcp.example\n      auth: {mode: none}\n")
	if _, err := AddDirectMCPServer(data, "calendar", "https://other.example", "https://issuer.example", "/tmp/credentials", "MECATL_MCP_CREDENTIAL_KEY"); err == nil {
		t.Fatal("duplicate folded name was accepted")
	}
}

func TestAddDirectMCPServerWithKeyNestsModeUnderKey(t *testing.T) {
	added, err := AddDirectMCPServerWithKey(nil, "gateway", "https://mcp.example/mcp", "https://issuer.example", "/tmp/mcp-credentials", "keyring", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parseYAML(added)
	if err != nil {
		t.Fatalf("generated document did not parse against the real schema:\n%s\nerr: %v", added, err)
	}
	local := cfg.MCP.Servers[0].Auth.OAuth.Credentials.Local
	if local.Key == nil {
		t.Fatalf("local.key was not populated (mode ended up a sibling, not nested):\n%s", added)
	}
	if local.Key.Mode != "keyring" {
		t.Fatalf("local.key.mode = %q, want keyring:\n%s", local.Key.Mode, added)
	}
	if local.Key.File != nil {
		t.Fatalf("keyring mode unexpectedly carried a file block:\n%s", added)
	}
}

// TestAddDirectMCPServerWithKeyHandlesEmptyFileStub covers the shape
// cmd/mecated's readMCPSettings actually produces for a not-yet-created
// settings file ("{}\n"), which is not "empty" by a raw byte/whitespace
// check — a nil/blank-only input isn't the only "no settings yet" shape this
// function has to handle.
func TestAddDirectMCPServerWithKeyHandlesEmptyFileStub(t *testing.T) {
	added, err := AddDirectMCPServerWithKey([]byte("{}\n"), "gateway", "https://mcp.example/mcp", "https://issuer.example", "/tmp/mcp-credentials", "keyring", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseYAML(added); err != nil {
		t.Fatalf("generated document did not parse against the real schema:\n%s\nerr: %v", added, err)
	}
}

func TestAddDirectMCPServerWithKeyFileModeNestsPath(t *testing.T) {
	added, err := AddDirectMCPServerWithKey(nil, "gateway", "https://mcp.example/mcp", "https://issuer.example", "/tmp/mcp-credentials", "file", "/tmp/mcp-credential-key")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parseYAML(added)
	if err != nil {
		t.Fatalf("generated document did not parse against the real schema:\n%s\nerr: %v", added, err)
	}
	local := cfg.MCP.Servers[0].Auth.OAuth.Credentials.Local
	if local.Key == nil || local.Key.Mode != "file" {
		t.Fatalf("local.key = %#v, want mode file:\n%s", local.Key, added)
	}
	if local.Key.File == nil || local.Key.File.Path != "/tmp/mcp-credential-key" {
		t.Fatalf("local.key.file = %#v, want path /tmp/mcp-credential-key:\n%s", local.Key.File, added)
	}
}

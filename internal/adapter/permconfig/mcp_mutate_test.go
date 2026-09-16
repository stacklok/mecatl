package permconfig

import (
	"strings"
	"testing"
)

func TestDirectMCPMutationPreservesUnrelatedContent(t *testing.T) {
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

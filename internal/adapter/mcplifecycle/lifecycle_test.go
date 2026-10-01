package mcplifecycle

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestAC08DirectMCPLifecycleListIsNonSecretProjection(t *testing.T) {
	got := List(permconfig.Config{MCP: &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{
		{Name: "plain", URL: "https://mcp.example/plain", Auth: permconfig.MCPAuthProfile{Mode: "none"}},
	}}})
	if len(got) != 1 || got[0].Name != "plain" || got[0].CredentialStatus != "ready" {
		t.Fatalf("List() = %#v", got)
	}
}

func TestAC08DirectMCPLifecycleAddRejectsBrokerBeforeDiscovery(t *testing.T) {
	_, err := Add(t.Context(), AddRequest{
		Name: "calendar", URL: "https://mcp.example/calendar",
		Settings: []byte("mcp:\n  mode: broker\n"),
	})
	if err == nil || err.Error() != "MCP direct onboarding is unavailable when mcp.mode is broker" {
		t.Fatalf("Add() error = %v, want broker rejection", err)
	}
}

// TestDirectMCPLifecycleAddRejectsIllegalNameBeforeDiscovery uses an
// unroutable URL: reaching discovery would surface a network error instead.
func TestDirectMCPLifecycleAddRejectsIllegalNameBeforeDiscovery(t *testing.T) {
	for name, suggestion := range map[string]string{
		"connector-gateway": `(try "connector_gateway")`,
		"a__b":              `(try "a_b")`,
		"-my.svc-":          `(try "my_svc")`,
		"ı":                 "",
		"":                  "",
	} {
		var stages []string
		_, err := Add(t.Context(), AddRequest{
			Name: name, URL: "https://mcp.invalid/mcp",
			Settings: []byte("mcp:\n  mode: global\n"),
			Progress: func(stage string) { stages = append(stages, stage) },
		})
		if err == nil || !strings.Contains(err.Error(), "invalid MCP server name") {
			t.Fatalf("Add(%q) error = %v, want name rejection", name, err)
		}
		if got := strings.Contains(err.Error(), "(try "); got != (suggestion != "") || !strings.HasSuffix(err.Error(), suggestion) {
			t.Fatalf("Add(%q) error = %q, want suggestion %q", name, err, suggestion)
		}
		if len(stages) != 0 {
			t.Fatalf("Add(%q) reached stages %v before rejecting the name", name, stages)
		}
	}
}

func TestAC08DirectMCPLifecycleRemoveDelegatesNarrowMutation(t *testing.T) {
	before := []byte("mcp:\n  servers:\n    - name: calendar\n      url: https://mcp.example/calendar\n      auth: {mode: none}\n")
	got, err := Remove(before, "calendar")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == string(before) || string(got) == "" {
		t.Fatalf("Remove() did not change settings: %q", got)
	}
}

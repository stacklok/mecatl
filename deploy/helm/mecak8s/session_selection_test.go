package mecak8s_test

import (
	"strings"
	"testing"
)

func TestBrokerPathRetirementHelmSelection(t *testing.T) {
	base := []string{"template", "poc", ".", "--set", "mockProvider=true,redis.local.enabled=true"}
	zero, err := helm(t, base...)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(zero, "name: poc-mecak8s-broker") || strings.Contains(zero, "--mcp-broker-") || !strings.Contains(zero, "mode: global") {
		t.Fatal("zero-MCP render selected a broker")
	}
	selected := append(append([]string(nil), base...), "--set", "mcp.mode=broker,broker.sessionAPI.mode=OWNERLESS,broker.sessionAPI.deployment=kind-poc-a,broker.credentialStore.redis.address=redis.example.com:6379,broker.credentialStore.redis.credentialsSecret=broker-redis")
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"empty", nil},
		{"anonymous", []string{"--set-json", `mcp.servers=[{"name":"fixture","url":"https://fixture.example.com/mcp","auth":{"mode":"none"}}]`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered, err := helm(t, append(append([]string(nil), selected...), tc.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			for _, required := range []string{"mode: broker", `"session_api":`, `"mode": "OWNERLESS"`, `"deployment": "kind-poc-a"`, "--mcp-broker-address=", `"protected_storage":`} {
				if !strings.Contains(rendered, required) {
					t.Fatalf("render missing %s", required)
				}
			}
			for _, retired := range []string{"mcp-broker-session-api", "max_handles", "max_receipts", "handle_idle_timeout", "credential-encryption", "--mcp-server="} {
				if strings.Contains(rendered, retired) {
					t.Fatalf("render retained %s", retired)
				}
			}
		})
	}
	for _, setting := range []string{"broker.sessionAPI.mode=", "broker.sessionAPI.deployment=", "broker.credentialStore.redis.address=", "mcp.mode=global,mcp.broker.callbackURL=https://fixture.example.com/callback"} {
		if _, err := helm(t, append(append([]string(nil), selected...), "--set", setting)...); err == nil {
			t.Fatalf("invalid selection admitted: %s", setting)
		}
	}
}

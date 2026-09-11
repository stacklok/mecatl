package app

import (
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestBrokerMCPStatus_Scenario1_Capabilities(t *testing.T) {
	for _, broker := range []bool{false, true} {
		for _, owned := range []bool{false, true} {
			cfg := Config{Workspace: t.TempDir(), StoreDir: t.TempDir(), UseMock: true, NoSoul: true, OwnershipEnforced: owned}
			if broker {
				cfg.MCPAuthority = mcpauthority.NewBroker(mcpauthority.BrokerConfig{CallbackURL: "https://broker.example/oauth/callback", Routes: []permconfig.MCPServerProfile{protectedToolHiveRoute("calendar")}})
			}
			built, err := Build(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(built.Close)
			for _, principal := range []*session.Principal{nil, {Issuer: "https://identity.example", Subject: "owner", GrantType: session.GrantTypeUser}} {
				ctx := session.WithPrincipal(t.Context(), principal)
				caps := built.Service.CompatibilityInfo(ctx).GetCapabilities()
				if got, want := caps.GetMcpConnectorStatus(), broker && owned && principal != nil; got != want {
					t.Fatalf("broker=%v owned=%v principal=%v: capability=%v want %v", broker, owned, principal != nil, got, want)
				}
				if broker && caps.GetMcp() {
					t.Fatal("broker enabled direct MCP")
				}
				if broker && owned && principal != nil {
					sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
					if err != nil {
						t.Fatal(err)
					}
					inventory, err := built.Service.ListSessionMcpConnectors(ctx, sess.ID)
					if err != nil || inventory.Availability != "available" || len(inventory.Connectors) != 1 {
						t.Fatalf("real bundled inspection: %+v %v", inventory, err)
					}
				}
			}
		}
	}
}

func TestBrokerMCPStatus_Scenario1_DirectComposition(t *testing.T) {
	mcpURL, _ := authorityVerticalMCPServer(t)
	built, err := Build(t.Context(), Config{
		Workspace: t.TempDir(), StoreDir: t.TempDir(), NoSoul: true, UseMock: true,
		MCPServers: []mcp.ServerConfig{{Name: "direct", URL: mcpURL}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(built.Close)

	caps := built.Service.CompatibilityInfo(t.Context()).GetCapabilities()
	if !caps.GetMcp() || caps.GetMcpConnectorStatus() {
		t.Fatalf("direct composition capabilities = mcp=%v broker=%v, want direct only", caps.GetMcp(), caps.GetMcpConnectorStatus())
	}
}

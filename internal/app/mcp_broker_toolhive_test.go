package app

import (
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestToolHiveBrokerConfigCopiesParsedOperatorValues(t *testing.T) {
	routes := []permconfig.MCPServerProfile{{
		Name: "protected", URL: "https://mcp.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{
			Issuer: "https://issuer.example", Scopes: []string{"read"},
			Client: permconfig.MCPOAuthClientProfile{Preregistered: &permconfig.MCPPreregisteredClientProfile{ID: "client", SecretEnv: "MECATL_CLIENT_SECRET"}},
			Tools:  []permconfig.MCPStaticToolProfile{{Name: "reviewed", Description: "comparison only", InputSchema: []byte(`{"type":"object"}`), ReadOnly: true}},
		}},
	}}
	config := toolHiveBrokerConfig(routes, "https://broker.example/callback", []string{"Read"})
	routes[0].Auth.OAuth.Scopes[0] = "changed"
	routes[0].Auth.OAuth.Tools[0].InputSchema[0] = '['

	profile := config.Profiles[0]
	if profile.OAuth.Scopes[0] != "read" || string(profile.Static[0].Schema) != `{"type":"object"}` {
		t.Fatalf("adapter construction values alias permconfig input: %#v", profile)
	}
	if config.CallbackURL != "https://broker.example/callback" || config.Occupied[0] != "Read" {
		t.Fatalf("adapter construction boundary = %#v", config)
	}
}

func TestToolHiveBrokerConfigPreservesStaticOIDCClientVariants(t *testing.T) {
	for _, test := range []struct {
		name   string
		client permconfig.MCPOAuthClientProfile
		wantID string
	}{
		{name: "preregistered", client: permconfig.MCPOAuthClientProfile{Mode: "preregistered", Preregistered: &permconfig.MCPPreregisteredClientProfile{ID: "registered", SecretEnv: "MECATL_CLIENT_SECRET"}}, wantID: "registered"},
		{name: "cimd", client: permconfig.MCPOAuthClientProfile{Mode: "cimd", CIMD: &permconfig.MCPCIMDClientProfile{DocumentURL: "https://client.example/oauth-client.json"}}, wantID: "https://client.example/oauth-client.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			routes := []permconfig.MCPServerProfile{{Name: "protected", URL: "https://mcp.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{
				Issuer: "https://issuer.example", Client: test.client, Scopes: []string{"read"},
				Tools: []permconfig.MCPStaticToolProfile{{Name: "read", InputSchema: []byte(`{"type":"object"}`)}},
			}}}}
			profile := toolHiveBrokerConfig(routes, "https://broker.example/callback", nil).Profiles[0]
			if profile.OAuth.Issuer != "https://issuer.example" || profile.OAuth.ClientID != test.wantID || len(profile.Static) != 1 {
				t.Fatalf("adapter profile = %#v", profile)
			}
		})
	}
}

func TestToolHiveBrokerConfigBuildsProtectedProviderMapping(t *testing.T) {
	routes := []permconfig.MCPServerProfile{protectedToolHiveRoute("GitHub_Cloud")}
	config := toolHiveBrokerConfig(routes, "https://broker.example/oauth/callback", nil)
	if got := config.Profiles[0].Name; got != "GitHub_Cloud" {
		t.Fatalf("configured profile = %q", got)
	}

	process, err := mcpbroker.NewToolHiveProcess(t.Context(), config)
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })

	construction := reflect.ValueOf(process).Elem().FieldByName("construction")
	upstreams := construction.FieldByName("upstreams")
	if got := upstreams.Index(0).FieldByName("Name").String(); got != "github-cloud" {
		t.Fatalf("constructed upstream = %q", got)
	}
	backend := construction.FieldByName("backends").Index(0)
	provider := backend.FieldByName("AuthConfig").Elem().FieldByName("UpstreamInject").Elem().FieldByName("ProviderName").String()
	if provider != "github-cloud" {
		t.Fatalf("backend provider = %q", provider)
	}
}

func protectedToolHiveRoute(name string) permconfig.MCPServerProfile {
	return permconfig.MCPServerProfile{Name: name, URL: "https://mcp.example/" + name, Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{
		Upstream: &permconfig.MCPOAuthUpstreamProfile{Mode: "oauth2", OAuth2: &permconfig.MCPOAuth2UpstreamProfile{
			AuthorizationEndpoint: "https://issuer.example/authorize", TokenEndpoint: "https://issuer.example/token",
		}},
		Client: permconfig.MCPOAuthClientProfile{Mode: "preregistered", Preregistered: &permconfig.MCPPreregisteredClientProfile{ID: name + "-client"}},
	}}}
}

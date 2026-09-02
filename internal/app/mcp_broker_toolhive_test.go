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

func TestToolHiveBrokerConfigBuildsOrderedProtectedProviderMappings(t *testing.T) {
	routes := []permconfig.MCPServerProfile{
		protectedToolHiveRoute("GitHub_Cloud"),
		protectedToolHiveRoute("Calendar_API"),
	}
	config := toolHiveBrokerConfig(routes, "https://broker.example/oauth/callback", nil)
	if got := []string{config.Profiles[0].Name, config.Profiles[1].Name}; !reflect.DeepEqual(got, []string{"GitHub_Cloud", "Calendar_API"}) {
		t.Fatalf("configured profile order = %v", got)
	}

	process, err := mcpbroker.NewToolHiveProcess(t.Context(), config)
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })

	construction := reflect.ValueOf(process).Elem().FieldByName("construction")
	upstreams := construction.FieldByName("upstreams")
	if got := []string{upstreams.Index(0).FieldByName("Name").String(), upstreams.Index(1).FieldByName("Name").String()}; !reflect.DeepEqual(got, []string{"github-cloud", "calendar-api"}) {
		t.Fatalf("constructed upstream order = %v", got)
	}
	backends := construction.FieldByName("backends")
	for _, want := range []struct{ backend, provider string }{{"GitHub_Cloud", "github-cloud"}, {"Calendar_API", "calendar-api"}} {
		found := false
		for i := range backends.Len() {
			backend := backends.Index(i)
			if backend.FieldByName("ID").String() != want.backend {
				continue
			}
			provider := backend.FieldByName("AuthConfig").Elem().FieldByName("UpstreamInject").Elem().FieldByName("ProviderName").String()
			if provider != want.provider {
				t.Fatalf("backend %q provider = %q, want %q", want.backend, provider, want.provider)
			}
			found = true
		}
		if !found {
			t.Errorf("configured backend %q was not constructed", want.backend)
		}
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

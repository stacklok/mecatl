package mcpauthority

import (
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestResultAccessorsFailClosed(t *testing.T) {
	var nilResult *Result
	if nilResult.Mode() != "" {
		t.Fatalf("nil mode = %q", nilResult.Mode())
	}
	if _, _, ok := nilResult.Global(); ok {
		t.Fatal("nil result exposed global payload")
	}
	if _, ok := nilResult.Broker(); ok {
		t.Fatal("nil result exposed broker payload")
	}

	global := NewGlobal(nil, nil)
	if global.Mode() != Global {
		t.Fatalf("global mode = %q", global.Mode())
	}
	if _, ok := global.Broker(); ok {
		t.Fatal("global result exposed broker payload")
	}
	broker := NewBroker(BrokerConfig{})
	if broker.Mode() != Broker {
		t.Fatalf("broker mode = %q", broker.Mode())
	}
	if _, _, ok := broker.Global(); ok {
		t.Fatal("broker result exposed global payload")
	}

	malformed := &Result{mode: Global, global: &globalConfig{}, broker: &BrokerConfig{}}
	if malformed.Mode() != "" {
		t.Fatalf("malformed mode = %q", malformed.Mode())
	}
}

func TestResultOwnsImmutableCopies(t *testing.T) {
	servers := []mcp.ServerConfig{{
		Name: "global", Headers: map[string]string{"Authorization": "Bearer token"},
		OAuth: &mcp.OAuthOptions{
			AllowedScopes: []string{"read"},
			Network:       mcp.OAuthNetworkPolicy{AdditionalOrigins: []string{"https://issuer.example"}},
		},
	}}
	global := NewGlobal(servers, nil)
	servers[0].Name = "mutated"
	servers[0].Headers["Authorization"] = "mutated"
	servers[0].OAuth.AllowedScopes[0] = "mutated"
	servers[0].OAuth.Network.AdditionalOrigins[0] = "https://mutated.example"
	gotServers, _, ok := global.Global()
	if !ok || gotServers[0].Name != "global" || gotServers[0].Headers["Authorization"] != "Bearer token" || gotServers[0].OAuth.AllowedScopes[0] != "read" || gotServers[0].OAuth.Network.AdditionalOrigins[0] != "https://issuer.example" {
		t.Fatalf("global aliases input: %#v", gotServers)
	}
	gotServers[0].Headers["Authorization"] = "returned mutation"
	gotServers[0].OAuth.AllowedScopes[0] = "returned mutation"
	againServers, _, _ := global.Global()
	if againServers[0].Headers["Authorization"] != "Bearer token" || againServers[0].OAuth.AllowedScopes[0] != "read" {
		t.Fatalf("global aliases accessor result: %#v", againServers)
	}

	routes := []permconfig.MCPServerProfile{{Name: "broker", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{
		Scopes: []string{"read"},
		Tools: []permconfig.MCPStaticToolProfile{{
			Name: "reviewed", InputSchema: []byte(`{"type":"object","properties":{"title":{"type":"string"}}}`),
		}},
		Upstream: &permconfig.MCPOAuthUpstreamProfile{Mode: "oauth2", OAuth2: &permconfig.MCPOAuth2UpstreamProfile{
			AuthorizationEndpoint: "https://auth.example/authorize", TokenEndpoint: "https://auth.example/token",
		}},
		Network: &permconfig.MCPOAuthNetworkProfile{AdditionalOrigins: []string{"https://issuer.example"}},
	}}}}
	broker := NewBroker(BrokerConfig{Routes: routes, CallbackURL: "https://agent.example/callback"})
	routes[0].Name = "mutated"
	routes[0].Auth.OAuth.Scopes[0] = "mutated"
	routes[0].Auth.OAuth.Tools[0].Name = "mutated"
	routes[0].Auth.OAuth.Tools[0].InputSchema[0] = '['
	got, ok := broker.Broker()
	if !ok || got.Routes[0].Name != "broker" || got.Routes[0].Auth.OAuth.Scopes[0] != "read" || got.Routes[0].Auth.OAuth.Tools[0].Name != "reviewed" || string(got.Routes[0].Auth.OAuth.Tools[0].InputSchema) != `{"type":"object","properties":{"title":{"type":"string"}}}` {
		t.Fatalf("broker aliases input: %#v", got)
	}
	got.Routes[0].Auth.OAuth.Upstream.OAuth2.TokenEndpoint = "https://mutated.example/token"
	got.Routes[0].Auth.OAuth.Tools[0].Name = "returned mutation"
	got.Routes[0].Auth.OAuth.Tools[0].InputSchema[0] = '['
	again, _ := broker.Broker()
	if again.Routes[0].Auth.OAuth.Upstream.OAuth2.TokenEndpoint != "https://auth.example/token" || again.Routes[0].Auth.OAuth.Tools[0].Name != "reviewed" || string(again.Routes[0].Auth.OAuth.Tools[0].InputSchema) != `{"type":"object","properties":{"title":{"type":"string"}}}` {
		t.Fatalf("broker aliases accessor result: %#v", again)
	}
}

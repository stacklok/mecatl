package cliconfig

import (
	"encoding/base64"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestMCPAuthorityRootDefaultsAndExplicitSelection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		root      mcpauthority.Mode
		explicit  string
		supported bool
		want      mcpauthority.Mode
		wantErr   bool
	}{
		{name: "kubernetes default", root: mcpauthority.Broker, supported: true, want: mcpauthority.Broker},
		{name: "server default", root: mcpauthority.Global, want: mcpauthority.Global},
		{name: "explicit global", root: mcpauthority.Broker, explicit: "global", supported: true, want: mcpauthority.Global},
		{name: "unsupported broker", root: mcpauthority.Global, explicit: "broker", wantErr: true},
		{name: "invalid root default", root: "other", wantErr: true},
		{name: "invalid explicit mode", root: mcpauthority.Global, explicit: "other", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveMCPAuthority(MCPAuthorityOptions{Operator: &permconfig.MCPSection{Mode: tc.explicit}, DefaultMode: tc.root, BrokerSupported: tc.supported})
			if tc.wantErr {
				if !errors.Is(err, ErrMCPProfileInvalid) {
					t.Fatalf("error = %v, want invalid profile", err)
				}
				return
			}
			if err != nil || got.Mode() != tc.want {
				t.Fatalf("authority = %#v mode %q, error %v", got, got.Mode(), err)
			}
		})
	}
}

func TestMCPAuthorityBrokerIsExclusiveAndRejectsLegacy(t *testing.T) {
	route := permconfig.MCPServerProfile{Name: "public", URL: "https://public.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "none"}}
	got, err := ResolveMCPAuthority(MCPAuthorityOptions{Operator: &permconfig.MCPSection{Mode: "broker", Servers: []permconfig.MCPServerProfile{route}}, DefaultMode: mcpauthority.Global, BrokerSupported: true})
	if err != nil {
		t.Fatal(err)
	}
	broker, ok := got.Broker()
	if !ok || len(broker.Routes) != 1 {
		t.Fatalf("broker payload = %#v, %t", broker, ok)
	}
	if _, _, ok := got.Global(); ok {
		t.Fatal("broker authority exposed global payload")
	}
	legacy := &MCPServerList{entries: []mcpServerEntry{{}}}
	if _, err := ResolveMCPAuthority(MCPAuthorityOptions{Operator: &permconfig.MCPSection{Mode: "broker"}, Legacy: legacy, DefaultMode: mcpauthority.Global, BrokerSupported: true}); !errors.Is(err, ErrMCPProfileInvalid) {
		t.Fatalf("legacy conflict error = %v", err)
	}
	static := permconfig.MCPServerProfile{Name: "static", URL: "https://static.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "static_bearer", StaticBearer: &permconfig.MCPStaticBearerProfile{TokenEnv: "MECATL_TOKEN"}}}
	for name, routes := range map[string][]permconfig.MCPServerProfile{
		"static bearer":    {static},
		"two OAuth routes": {brokerOAuthRoute(), brokerOAuthRoute()},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ResolveMCPAuthority(MCPAuthorityOptions{Operator: &permconfig.MCPSection{Mode: "broker", Broker: permconfig.MCPBrokerProfile{CallbackURL: "https://agent.example/callback"}, Servers: routes}, DefaultMode: mcpauthority.Global, BrokerSupported: true})
			if !errors.Is(err, ErrMCPProfileInvalid) {
				t.Fatalf("error = %v, want invalid profile", err)
			}
		})
	}
}

func TestMCPAuthorityBrokerCallbackRules(t *testing.T) {
	base := brokerOAuthRoute()
	for _, tc := range []struct {
		name, callback, wantCallback string
		ok                           bool
	}{
		{name: "required", ok: false},
		{name: "https", callback: "https://agent.example/callback", wantCallback: "https://agent.example/callback", ok: true},
		{name: "pathless root", callback: "https://agent.example", wantCallback: "https://agent.example/", ok: true},
		{name: "slash root", callback: "https://agent.example/", wantCallback: "https://agent.example/", ok: true},
		{name: "http", callback: "http://agent.example/callback", ok: false},
		{name: "userinfo", callback: "https://user@agent.example/callback", ok: false},
		{name: "query", callback: "https://agent.example/callback?code=x", ok: false},
		{name: "fragment", callback: "https://agent.example/callback#x", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			section := &permconfig.MCPSection{Mode: "broker", Broker: permconfig.MCPBrokerProfile{CallbackURL: tc.callback}, Servers: []permconfig.MCPServerProfile{base}}
			got, err := ResolveMCPAuthority(MCPAuthorityOptions{Operator: section, DefaultMode: mcpauthority.Global, BrokerSupported: true})
			if (err == nil) != tc.ok {
				t.Fatalf("error = %v, want success %t", err, tc.ok)
			}
			if tc.ok {
				broker, ok := got.Broker()
				if !ok || broker.CallbackURL != tc.wantCallback {
					t.Fatalf("broker callback = %#v, selected %t; want %q", broker, ok, tc.wantCallback)
				}
			}
		})
	}
	if _, err := ResolveMCPAuthority(MCPAuthorityOptions{Operator: &permconfig.MCPSection{Mode: "broker", Broker: permconfig.MCPBrokerProfile{CallbackURL: "https://agent.example/callback"}}, DefaultMode: mcpauthority.Global, BrokerSupported: true}); !errors.Is(err, ErrMCPProfileInvalid) {
		t.Fatalf("inert broker callback error = %v", err)
	}
	if _, err := ResolveMCPAuthority(MCPAuthorityOptions{Operator: &permconfig.MCPSection{Mode: "global", Broker: permconfig.MCPBrokerProfile{CallbackURL: "https://agent.example/callback"}}, DefaultMode: mcpauthority.Global}); !errors.Is(err, ErrMCPProfileInvalid) {
		t.Fatalf("global callback error = %v", err)
	}
}

func TestMCPAuthorityPreservesGlobalProfileResolution(t *testing.T) {
	oauth := environmentOAuth()
	oauth.Network.PrivateOrigins = []string{"https://issuer.example"}
	oauth.Network.MaxRedirects = 3
	section := &permconfig.MCPSection{Mode: "global", Servers: []permconfig.MCPServerProfile{
		{Name: "none", URL: "http://public.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "none"}},
		{Name: "static", URL: "https://static.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "static_bearer", StaticBearer: &permconfig.MCPStaticBearerProfile{TokenEnv: "MECATL_STATIC"}}},
		{Name: "oauth", URL: "https://oauth.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: oauth}},
	}}
	credential := base64.StdEncoding.EncodeToString([]byte("credential"))
	got, err := ResolveMCPAuthority(MCPAuthorityOptions{Operator: section, DefaultMode: mcpauthority.Broker, LookupEnv: func(name string) (string, bool) {
		values := map[string]string{"MECATL_STATIC": "token", "MECATL_ENV_CREDENTIAL": credential}
		value, ok := values[name]
		return value, ok
	}})
	if err != nil {
		t.Fatal(err)
	}
	servers, lifecycle, ok := got.Global()
	if !ok || len(servers) != 3 || lifecycle == nil || servers[1].Headers["Authorization"] != "Bearer token" || servers[2].OAuth == nil {
		t.Fatalf("global result = %#v, lifecycle %T, selected %t", servers, lifecycle, ok)
	}
	if network := servers[2].OAuth.Network; len(network.AdditionalOrigins) != 1 || network.AdditionalOrigins[0] != "https://client.example" || len(network.PrivateOrigins) != 1 || network.PrivateOrigins[0] != "https://issuer.example" || network.MaxRedirects != 3 {
		t.Fatalf("global OAuth network = %#v", network)
	}
	if _, ok := got.Broker(); ok {
		t.Fatal("global authority exposed broker payload")
	}
	if err := lifecycle.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMCPAuthorityModeSpecificOAuth(t *testing.T) {
	broker := brokerOAuthRoute()
	section := &permconfig.MCPSection{Mode: "broker", Broker: permconfig.MCPBrokerProfile{CallbackURL: "https://agent.example/callback"}, Servers: []permconfig.MCPServerProfile{broker}}
	if _, err := ResolveMCPAuthority(MCPAuthorityOptions{Operator: section, DefaultMode: mcpauthority.Global, BrokerSupported: true}); err != nil {
		t.Fatal(err)
	}
	section.Servers[0].Auth.OAuth.Profile = "global-only"
	if _, err := ResolveMCPAuthority(MCPAuthorityOptions{Operator: section, DefaultMode: mcpauthority.Global, BrokerSupported: true}); !errors.Is(err, ErrMCPProfileInvalid) {
		t.Fatalf("broker global-field error = %v", err)
	}
	section.Servers[0] = brokerOAuthRoute()
	section.Servers[0].Auth.OAuth.Upstream = &permconfig.MCPOAuthUpstreamProfile{Mode: "oauth2", OAuth2: &permconfig.MCPOAuth2UpstreamProfile{AuthorizationEndpoint: "https://auth.example/authorize", TokenEndpoint: "https://auth.example/token"}}
	section.Servers[0].Auth.OAuth.Issuer = ""
	if _, err := ResolveMCPAuthority(MCPAuthorityOptions{Operator: section, DefaultMode: mcpauthority.Global, BrokerSupported: true}); err != nil {
		t.Fatalf("broker OAuth2: %v", err)
	}
	section.Servers[0].Auth.OAuth.Network.AdditionalOrigins = []string{"https://extra.example"}
	if _, err := ResolveMCPAuthority(MCPAuthorityOptions{Operator: section, DefaultMode: mcpauthority.Global, BrokerSupported: true}); !errors.Is(err, ErrMCPProfileInvalid) {
		t.Fatalf("unsupported network controls error = %v", err)
	}
	global := &permconfig.MCPSection{Mode: "global", Servers: []permconfig.MCPServerProfile{broker}}
	global.Servers[0].Auth.OAuth.Upstream = &permconfig.MCPOAuthUpstreamProfile{Mode: "oidc"}
	if _, err := ResolveMCPAuthority(MCPAuthorityOptions{Operator: global, DefaultMode: mcpauthority.Global}); !errors.Is(err, ErrMCPProfileInvalid) {
		t.Fatalf("global upstream selector error = %v", err)
	}
}

func TestMCPAuthorityBrokerRejectsUnsupportedNetworkControlsBeforeConstruction(t *testing.T) {
	for _, upstream := range []struct {
		name  string
		apply func(*permconfig.MCPOAuthProfile)
	}{
		{name: "oidc", apply: func(_ *permconfig.MCPOAuthProfile) {}},
		{name: "oauth2", apply: func(oauth *permconfig.MCPOAuthProfile) {
			oauth.Upstream = &permconfig.MCPOAuthUpstreamProfile{Mode: "oauth2", OAuth2: &permconfig.MCPOAuth2UpstreamProfile{AuthorizationEndpoint: "https://auth.example/authorize", TokenEndpoint: "https://auth.example/token"}}
			oauth.Issuer = ""
		}},
	} {
		for _, control := range []struct {
			name  string
			apply func(*permconfig.MCPOAuthNetworkProfile)
		}{
			{name: "additional_origins", apply: func(network *permconfig.MCPOAuthNetworkProfile) {
				network.AdditionalOrigins = []string{"https://extra.example"}
			}},
			{name: "private_origins", apply: func(network *permconfig.MCPOAuthNetworkProfile) {
				network.PrivateOrigins = []string{"https://issuer.example"}
			}},
			{name: "max_redirects", apply: func(network *permconfig.MCPOAuthNetworkProfile) { network.MaxRedirects = 1 }},
		} {
			t.Run(upstream.name+"/"+control.name, func(t *testing.T) {
				route := brokerOAuthRoute()
				upstream.apply(route.Auth.OAuth)
				control.apply(route.Auth.OAuth.Network)
				section := &permconfig.MCPSection{Mode: "broker", Broker: permconfig.MCPBrokerProfile{CallbackURL: "https://agent.example/callback"}, Servers: []permconfig.MCPServerProfile{route}}

				got, err := ResolveMCPAuthority(MCPAuthorityOptions{Operator: section, DefaultMode: mcpauthority.Global, BrokerSupported: true})
				if !errors.Is(err, ErrMCPProfileInvalid) {
					t.Fatalf("ResolveMCPAuthority error = %v, want invalid profile", err)
				}
				if got != nil {
					t.Fatalf("ResolveMCPAuthority authority = %#v, want no broker construction input", got)
				}
			})
		}
	}
}

func brokerOAuthRoute() permconfig.MCPServerProfile {
	return permconfig.MCPServerProfile{Name: "protected", URL: "https://mcp.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{
		Issuer: "https://issuer.example",
		Client: permconfig.MCPOAuthClientProfile{Mode: "cimd", CIMD: &permconfig.MCPCIMDClientProfile{DocumentURL: "https://issuer.example/client.json"}},
		Scopes: []string{"read"}, Network: &permconfig.MCPOAuthNetworkProfile{},
	}}}
}

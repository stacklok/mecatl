package cliconfig

import (
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestDirectMCPDCR_Scenario1_AuthoritySeparatedProfile(t *testing.T) {
	const template = `mcp:
  mode: %s
  servers:
    - name: connector
      url: https://connector-gateway.stacklok.dev/gw/mcp
      auth:
        mode: oauth
        oauth:
          profile: connector
          principal: local-user
          issuer: https://connector-gateway.stacklok.dev
          client: {mode: dcr, dcr: {}}
          %s
          credentials:
            mode: local
            local: {root: %q, key_env: MECATL_MCP_CREDENTIAL_KEY}
          network: {additional_origins: [], private_origins: [], max_redirects: 0}
`
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	lookup := func(name string) (string, bool) { return key, name == "MECATL_MCP_CREDENTIAL_KEY" }
	parse := func(t *testing.T, mode, refresh string) *permconfig.MCPSection {
		t.Helper()
		var cfg permconfig.Config
		if err := yaml.Unmarshal([]byte(fmt.Sprintf(template, mode, refresh, filepath.Join(t.TempDir(), "credentials"))), &cfg); err != nil {
			t.Fatalf("parse direct DCR profile: %v", err)
		}
		return cfg.MCP
	}

	for _, tc := range []struct {
		name, refresh string
		wantRefresh   bool
		wantScopes    string
	}{
		{name: "durable defaults", wantRefresh: true, wantScopes: "openid,offline_access"},
		{name: "explicit no refresh", refresh: "request_refresh_token: false", wantScopes: "openid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveMCPAuthority(MCPAuthorityOptions{Operator: parse(t, "global", tc.refresh), DefaultMode: mcpauthority.Global, LookupEnv: lookup})
			if err != nil {
				t.Fatal(err)
			}
			servers, lifecycle, ok := got.Global()
			if !ok || len(servers) != 1 || servers[0].OAuth == nil {
				t.Fatalf("global DCR result = %#v, selected %t", servers, ok)
			}
			defer lifecycle.Close()
			oauth := servers[0].OAuth
			if oauth.Client.DCR == nil || oauth.Client.Preregistered != nil || oauth.Client.ClientIDMetadataDocumentURL != "" {
				t.Fatalf("resolved client = %#v, want DCR only", oauth.Client)
			}
			if oauth.RequestRefreshToken != tc.wantRefresh || strings.Join(oauth.AllowedScopes, ",") != tc.wantScopes {
				t.Fatalf("refresh/scopes = %t/%v, want %t/%s", oauth.RequestRefreshToken, oauth.AllowedScopes, tc.wantRefresh, tc.wantScopes)
			}
		})
	}

	for _, tc := range []struct{ name, mode, replacement string }{
		{name: "broker authority", mode: "broker"},
		{name: "upstream", mode: "global", replacement: "issuer: https://connector-gateway.stacklok.dev\n          upstream: {mode: oidc}"},
		{name: "broker discovery payload", mode: "global", replacement: "client: {mode: dcr, dcr: {discovery_url: https://connector-gateway.stacklok.dev/.well-known/oauth-authorization-server}}"},
		{name: "mixed client forms", mode: "global", replacement: "client: {mode: dcr, dcr: {}, cimd: {document_url: https://client.example/metadata.json}}"},
		{name: "static credential payload", mode: "global", replacement: "static_bearer: {token_env: MECATL_TOKEN}"},
		{name: "environment credentials", mode: "global", replacement: "credentials: {mode: environment, environment: {credential_env: MECATL_CREDENTIAL}}"},
		{name: "inconsistent scopes", mode: "global", replacement: "request_refresh_token: false\n          scopes: [openid, offline_access]"},
	} {
		t.Run("reject "+tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "credentials")
			body := fmt.Sprintf(template, tc.mode, "", root)
			if tc.replacement != "" {
				switch tc.name {
				case "upstream":
					body = strings.Replace(body, "issuer: https://connector-gateway.stacklok.dev", tc.replacement, 1)
				case "broker discovery payload":
					body = strings.Replace(body, "client: {mode: dcr, dcr: {}}", tc.replacement, 1)
				case "mixed client forms":
					body = strings.Replace(body, "client: {mode: dcr, dcr: {}}", tc.replacement, 1)
				case "static credential payload":
					body = strings.Replace(body, "        oauth:\n", "        "+tc.replacement+"\n        oauth:\n", 1)
				case "environment credentials":
					body = strings.Replace(body, "credentials:\n            mode: local\n            local: {root: "+fmt.Sprintf("%q", root)+", key_env: MECATL_MCP_CREDENTIAL_KEY}", tc.replacement, 1)
				case "inconsistent scopes":
					body = strings.Replace(body, "          \n", "          "+tc.replacement+"\n", 1)
				}
			}
			var cfg permconfig.Config
			parseErr := yaml.Unmarshal([]byte(body), &cfg)
			if parseErr == nil {
				_, parseErr = ResolveMCPAuthority(MCPAuthorityOptions{Operator: cfg.MCP, DefaultMode: mcpauthority.Global, BrokerSupported: true, LookupEnv: lookup})
			}
			if parseErr == nil {
				t.Fatal("invalid authority-separated DCR profile was admitted")
			}
		})
	}

	brokerProfiles := parse(t, "broker", "")
	if _, err := LoadMCPProfiles(MCPProfileLoadOptions{Operator: brokerProfiles, LookupEnv: lookup}); !errors.Is(err, ErrMCPProfileInvalid) {
		t.Fatalf("direct loader accepted broker authority: %v", err)
	}

	_ = mcp.OAuthDCRConfig{}
}

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
	if _, err := ResolveMCPAuthority(MCPAuthorityOptions{Operator: &permconfig.MCPSection{Mode: "broker", Servers: []permconfig.MCPServerProfile{static}}, DefaultMode: mcpauthority.Global, BrokerSupported: true}); !errors.Is(err, ErrMCPProfileInvalid) {
		t.Fatalf("static bearer error = %v, want invalid profile", err)
	}
}

func TestADR_0298_BrokerAuthorityAdmitsMultipleOAuthProfilesInOrder(t *testing.T) {
	first := brokerOAuthRoute()
	first.Name = "github"
	second := brokerOAuthRoute()
	second.Name = "calendar"
	section := &permconfig.MCPSection{
		Mode:    "broker",
		Broker:  permconfig.MCPBrokerProfile{CallbackURL: "https://agent.example/callback"},
		Servers: []permconfig.MCPServerProfile{first, second},
	}

	got, err := ResolveMCPAuthority(MCPAuthorityOptions{Operator: section, DefaultMode: mcpauthority.Global, BrokerSupported: true})
	if err != nil {
		t.Fatalf("ResolveMCPAuthority: %v", err)
	}
	broker, ok := got.Broker()
	if !ok || len(broker.Routes) != 2 || broker.Routes[0].Name != "github" || broker.Routes[1].Name != "calendar" {
		t.Fatalf("broker routes = %#v, selected %t", broker.Routes, ok)
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

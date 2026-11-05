package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func unusedBrokerCaller(context.Context, mcpbroker.SessionRef, string, session.ToolCall) (session.ToolResult, error) {
	return session.ToolResult{}, nil
}

func TestConfiguredMCPBrokerSelectsConfiguredPath(t *testing.T) {
	for name, configured := range map[string]ConfiguredMCPBroker{
		"discovered": {MCPBrokerDiscovered: []mcpbroker.ToolDefinition{{Backend: "b", Name: "mcp__b__t", Schema: json.RawMessage(`{"type":"object"}`)}}},
		"caller":     {MCPBrokerCaller: unusedBrokerCaller},
		"authorized caller": {MCPBrokerAuthorizedCaller: func(context.Context, mcpbroker.SessionRef, string, session.ToolCall, oauth2.TokenSource) (session.ToolResult, error) {
			return session.ToolResult{}, nil
		}},
		"query caller": {MCPBrokerQueryCaller: func(context.Context, mcpbroker.SessionRef, string, session.ToolCall, oauth2.TokenSource, string) (session.ToolResult, error) {
			return session.ToolResult{}, nil
		}},
		"options": {MCPBrokerOptions: []mcpbroker.Option{mcpbroker.WithOAuthLimits(0, 0)}},
	} {
		if !configured.selectsConfiguredMCPBroker() {
			t.Errorf("%s alone did not select the configured MCP broker path", name)
		}
	}
	if (ConfiguredMCPBroker{}).selectsConfiguredMCPBroker() {
		t.Error("empty ConfiguredMCPBroker selected the configured MCP broker path")
	}
}

const (
	brokerTestCallbackURL  = "https://broker.example/oauth/callback"
	brokerTestCallbackPath = "/oauth/callback"
	brokerAuthorizePath    = "/v1/mcp/broker/oauth/authorize"
)

// mountedBrokerRoutes mounts built's broker surface and reports whether the
// bundled authorization route and the callback route were registered.
func mountedBrokerRoutes(t *testing.T, built *Built) (authorize, callback bool) {
	t.Helper()
	mux := http.NewServeMux()
	if err := built.MountMCPBrokerHandlers(mux); err != nil {
		t.Fatalf("MountMCPBrokerHandlers: %v", err)
	}
	registered := func(path string) bool {
		_, pattern := mux.Handler(&http.Request{Method: http.MethodGet, URL: &url.URL{Path: path}})
		return pattern != ""
	}
	return registered(brokerAuthorizePath), registered(brokerTestCallbackPath)
}

// The two tests below pin that enabling the identity broker leaves the MCP
// broker path unchanged: only ConfiguredMCPBroker selects it. #2536 passes the
// identity broker into buildMCPBroker on both paths; both tests must keep
// passing with it.

func TestBuildKeepsBundledMCPBrokerWithIdentityBroker(t *testing.T) {
	built, err := buildIsolated(t, t.Context(), Config{
		Workspace: t.TempDir(), UseMock: true, NoSoul: true,
		IdentityBroker: true, identityBrokerCore: newClosableCore(t),
		MCPAuthority: mcpauthority.NewBroker(mcpauthority.BrokerConfig{CallbackURL: brokerTestCallbackURL, Routes: []permconfig.MCPServerProfile{protectedToolHiveRoute("calendar")}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(built.Close)
	if built.MCPBroker == nil || built.IdentityBroker == nil {
		t.Fatalf("broker=%v identity broker=%v, want both", built.MCPBroker != nil, built.IdentityBroker != nil)
	}
	if built.MCPBrokerCallbackPath != brokerTestCallbackPath {
		t.Fatalf("callback path = %q, want %q", built.MCPBrokerCallbackPath, brokerTestCallbackPath)
	}
	if authorize, callback := mountedBrokerRoutes(t, built); !authorize || !callback {
		t.Fatalf("bundled routes: authorize=%v callback=%v, want both", authorize, callback)
	}
	built.Close()
	if _, _, err := built.MCPBroker.AttachSession(context.Background(), "after-shutdown"); err == nil {
		t.Fatal("bundled broker accepted attachment after Built.Close")
	}
}

func TestBuildKeepsConfiguredMCPBrokerWithIdentityBroker(t *testing.T) {
	cfg := Config{
		Workspace: t.TempDir(), UseMock: true, NoSoul: true,
		IdentityBroker: true, identityBrokerCore: newClosableCore(t),
		MCPAuthority: mcpauthority.NewBroker(mcpauthority.BrokerConfig{CallbackURL: brokerTestCallbackURL}),
	}
	cfg.MCPBrokerCaller = unusedBrokerCaller
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(built.Close)
	if built.MCPBroker == nil || built.IdentityBroker == nil {
		t.Fatalf("broker=%v identity broker=%v, want both", built.MCPBroker != nil, built.IdentityBroker != nil)
	}
	if authorize, callback := mountedBrokerRoutes(t, built); authorize || !callback {
		t.Fatalf("configured routes: authorize=%v callback=%v, want callback only", authorize, callback)
	}
}

// TestConfiguredMCPBrokerWithoutBrokerAuthorityBuildsNoBroker pins that
// ConfiguredMCPBroker only chooses a path: without broker MCP authority, Build
// constructs no broker at all.
func TestConfiguredMCPBrokerWithoutBrokerAuthorityBuildsNoBroker(t *testing.T) {
	cfg := Config{Workspace: t.TempDir(), UseMock: true, NoSoul: true}
	cfg.MCPBrokerCaller = unusedBrokerCaller
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(built.Close)
	if built.MCPBroker != nil || built.MCPBrokerHandlers != nil {
		t.Fatal("ConfiguredMCPBroker built a broker without broker MCP authority")
	}
}

func TestBuildMCPBrokerFailuresKeepTheirErrors(t *testing.T) {
	configured := Config{}
	configured.MCPBrokerCaller = unusedBrokerCaller
	collision := configured
	collision.MCPBrokerDiscovered = []mcpbroker.ToolDefinition{{Backend: "calendar", Name: "Read", Schema: json.RawMessage(`{"type":"object"}`)}}
	for _, tc := range []struct {
		name        string
		cfg         Config
		declaration mcpauthority.BrokerConfig
		occupied    []string
		want        string
	}{
		{name: "configured catalogue", cfg: collision, declaration: mcpauthority.BrokerConfig{Routes: []permconfig.MCPServerProfile{{Name: "calendar", Auth: permconfig.MCPAuthProfile{Mode: "none"}}}}, occupied: []string{"Read"}, want: "build MCP broker catalogue: "},
		{name: "configured callback", cfg: configured, declaration: mcpauthority.BrokerConfig{CallbackURL: brokerTestCallbackURL + "?x=1"}, want: "build MCP broker handlers: "},
		{name: "bundled callback", declaration: mcpauthority.BrokerConfig{CallbackURL: brokerTestCallbackURL + "?x=1"}, want: "build bundled MCP broker: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parts, err := buildMCPBroker(t.Context(), tc.cfg, tc.declaration, tc.occupied)
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				parts.close()
				t.Fatalf("buildMCPBroker error = %v, want prefix %q", err, tc.want)
			}
			if parts.service != nil || parts.closer != nil {
				t.Fatalf("failed construction returned broker parts: %+v", parts)
			}
		})
	}
}

// TestBuildMCPBrokerHandsOffPathSpecificParts pins what each path hands to
// the server: only the bundled process inspects connectors and may require
// workspace enrollment, and a broker without an HTTP surface hands over nil
// handlers, which the command roots treat as nothing to mount.
func TestBuildMCPBrokerHandsOffPathSpecificParts(t *testing.T) {
	configured := Config{}
	configured.MCPBrokerCaller = unusedBrokerCaller
	protected := mcpauthority.BrokerConfig{CallbackURL: brokerTestCallbackURL, Routes: []permconfig.MCPServerProfile{protectedToolHiveRoute("calendar")}}
	for _, tc := range []struct {
		name                       string
		cfg                        Config
		declaration                mcpauthority.BrokerConfig
		inspector, enrollment, web bool
	}{
		{name: "bundled without surface", inspector: true},
		{name: "bundled protected", declaration: protected, inspector: true, enrollment: true, web: true},
		{name: "configured without callback", cfg: configured},
		{name: "configured with callback", cfg: configured, declaration: mcpauthority.BrokerConfig{CallbackURL: brokerTestCallbackURL}, web: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parts, err := buildMCPBroker(t.Context(), tc.cfg, tc.declaration, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(parts.close)
			if parts.service == nil {
				t.Fatal("no broker service")
			}
			if got := parts.inspector != nil; got != tc.inspector {
				t.Errorf("connector inspector = %v, want %v", got, tc.inspector)
			}
			if parts.workspaceEnrollment != tc.enrollment {
				t.Errorf("workspace enrollment = %v, want %v", parts.workspaceEnrollment, tc.enrollment)
			}
			if got := parts.handlers != nil; got != tc.web {
				t.Errorf("HTTP handlers = %v, want %v", got, tc.web)
			}
		})
	}
}

// TestBundledMCPBrokerClosesUnusedAuthRedisClient covers an anonymous-only
// bundled broker: the process never uses the auth Redis client, so closing
// the broker must still close it.
func TestBundledMCPBrokerClosesUnusedAuthRedisClient(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	parts, err := buildMCPBroker(t.Context(), Config{RedisURL: mr.Addr(), RedisAllowPlaintext: true}, mcpauthority.BrokerConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mr.CurrentConnectionCount() == 0 {
		parts.close()
		t.Fatal("auth Redis client never connected; the test cannot observe its close")
	}
	parts.close()
	deadline := time.Now().Add(5 * time.Second)
	for mr.CurrentConnectionCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("auth Redis client still holds %d connections after close", mr.CurrentConnectionCount())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestToolHiveBrokerConfigCopiesParsedOperatorValues(t *testing.T) {
	routes := []permconfig.MCPServerProfile{{
		Name: "protected", URL: "https://mcp.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{
			Issuer: "https://issuer.example", Scopes: []string{"read"},
			Client: permconfig.MCPOAuthClientProfile{Preregistered: &permconfig.MCPPreregisteredClientProfile{ID: "client", SecretEnv: "MECATL_CLIENT_SECRET"}},
			Tools:  []permconfig.MCPStaticToolProfile{{Name: "reviewed", Description: "comparison only", InputSchema: []byte(`{"type":"object"}`), ReadOnly: true}},
		}},
	}}
	config := toolHiveBrokerConfig(routes, "https://broker.example/callback", []string{"Read"}, nil, nil)
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
			profile := toolHiveBrokerConfig(routes, "https://broker.example/callback", nil, nil, nil).Profiles[0]
			if profile.OAuth.Issuer != "https://issuer.example" || profile.OAuth.ClientID != test.wantID || len(profile.Static) != 1 {
				t.Fatalf("adapter profile = %#v", profile)
			}
		})
	}
}

func TestToolHiveConversionCarriesDCRConfig(t *testing.T) {
	routes := []permconfig.MCPServerProfile{{Name: "protected", URL: "https://mcp.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{
		Upstream: &permconfig.MCPOAuthUpstreamProfile{Mode: "oauth2", OAuth2: &permconfig.MCPOAuth2UpstreamProfile{AuthorizationEndpoint: "https://auth.example/authorize", TokenEndpoint: "https://auth.example/token"}},
		Client:   permconfig.MCPOAuthClientProfile{Mode: "dcr", DCR: &permconfig.MCPDCRClientProfile{DiscoveryURL: "https://auth.example/.well-known/oauth-authorization-server"}}, Scopes: []string{"read"},
	}}}}
	profile := toolHiveBrokerConfig(routes, "https://broker.example", nil, nil, nil).Profiles[0]
	if profile.OAuth.ClientID != "" || profile.OAuth.DCRDiscoveryURL != "https://auth.example/.well-known/oauth-authorization-server" {
		t.Fatalf("DCR adapter profile = %#v", profile.OAuth)
	}
}

func TestDCRRequiresExplicitOAuth2Upstream(t *testing.T) {
	_, err := mcpbroker.NewToolHiveProcess(t.Context(), mcpbroker.ToolHiveConfig{CallbackURL: "https://broker.example", Profiles: []mcpbroker.ToolHiveProfile{{Name: "protected", URL: "https://mcp.example/mcp", Auth: "oauth", OAuth: &mcpbroker.ToolHiveOAuth{DCRDiscoveryURL: "https://auth.example/discovery"}}}})
	if err == nil {
		t.Fatal("DCR without explicit OAuth2 endpoints constructed successfully")
	}
}

func TestToolHiveBrokerConfigProjectsRefreshTokenRequest(t *testing.T) {
	for _, requestRefreshToken := range []bool{true, false} {
		t.Run("request_refresh_token="+strconv.FormatBool(requestRefreshToken), func(t *testing.T) {
			routes := []permconfig.MCPServerProfile{{
				Name: "protected", URL: "https://mcp.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{
					Issuer: "https://issuer.example", Scopes: []string{"openid", "profile"}, RequestRefreshToken: requestRefreshToken,
					Client: permconfig.MCPOAuthClientProfile{Preregistered: &permconfig.MCPPreregisteredClientProfile{ID: "client"}},
				}},
			}}
			oauth := toolHiveBrokerConfig(routes, "https://broker.example/callback", nil, nil, nil).Profiles[0].OAuth
			if oauth.RequestRefreshToken != requestRefreshToken || !reflect.DeepEqual(oauth.Scopes, []string{"openid", "profile"}) {
				t.Fatalf("adapter OAuth = %#v", oauth)
			}
		})
	}
}

func TestToolHiveBrokerConfigBuildsEveryProtectedProviderMapping(t *testing.T) {
	routes := []permconfig.MCPServerProfile{protectedToolHiveRoute("GitHub_Cloud"), protectedToolHiveRoute("Calendar")}
	config := toolHiveBrokerConfig(routes, "https://broker.example/oauth/callback", nil, nil, nil)
	if got := config.Profiles; len(got) != 2 || got[0].Name != "GitHub_Cloud" || got[1].Name != "Calendar" {
		t.Fatalf("configured profiles = %#v", got)
	}

	process, err := mcpbroker.NewToolHiveProcess(t.Context(), config)
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })

	construction := reflect.ValueOf(process).Elem().FieldByName("construction")
	upstreams := construction.FieldByName("upstreams")
	backends := construction.FieldByName("backends")
	for i, want := range []string{"github-cloud", "calendar"} {
		if got := upstreams.Index(i).FieldByName("Name").String(); got != want {
			t.Fatalf("constructed upstream %d = %q, want %q", i, got, want)
		}
		provider := backends.Index(i).FieldByName("AuthConfig").Elem().FieldByName("UpstreamInject").Elem().FieldByName("ProviderName").String()
		if provider != want {
			t.Fatalf("backend %d provider = %q, want %q", i, provider, want)
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

// TestBuildToolHiveAuthRedisClientUsesRedisWhenConfigured proves the
// embedded ToolHive auth server is handed a live client to the operator's
// configured Redis instance — the same managed instance mecatl's own session
// store uses — rather than always falling back to storage.NewMemoryStorage()
// (composition itself never touches the vendored toolhive storage package;
// see TestToolHiveImportsStayBehindApprovedAdapterLeaves).
func TestBuildToolHiveAuthRedisClientUsesRedisWhenConfigured(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)

	client, closeClient, err := buildToolHiveAuthRedisClient(Config{RedisURL: mr.Addr(), RedisAllowPlaintext: true})
	if err != nil {
		t.Fatalf("buildToolHiveAuthRedisClient: %v", err)
	}
	t.Cleanup(closeClient)
	if client == nil {
		t.Fatal("buildToolHiveAuthRedisClient returned a nil client with Redis configured")
	}
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Fatalf("Ping: %v (proves the client round-trips through the real Redis connection)", err)
	}
}

// TestBuildToolHiveAuthRedisClientFallsBackWithoutRedis proves the "drop-in,
// same interface" property when Redis isn't configured: no client is minted
// here at all, leaving mcpbroker.ToolHiveConfig.AuthRedisClient nil so
// newToolHiveProcess's own fallback selects storage.NewMemoryStorage() —
// unchanged behavior for non-HA/dev deployments.
func TestBuildToolHiveAuthRedisClientFallsBackWithoutRedis(t *testing.T) {
	client, closeClient, err := buildToolHiveAuthRedisClient(Config{})
	if err != nil {
		t.Fatalf("buildToolHiveAuthRedisClient: %v", err)
	}
	closeClient()
	if client != nil {
		t.Fatalf("buildToolHiveAuthRedisClient client = %#v, want nil", client)
	}
}

func TestToolHiveBrokerConfigMapsClientSecretSources(t *testing.T) {
	route := func(name string, client permconfig.MCPPreregisteredClientProfile) permconfig.MCPServerProfile {
		return permconfig.MCPServerProfile{Name: name, URL: "https://mcp.example/" + name, Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{
			Issuer: "https://issuer.example", Scopes: []string{"read"},
			Client: permconfig.MCPOAuthClientProfile{Mode: "preregistered", Preregistered: &client},
		}}}
	}
	config := toolHiveBrokerConfig([]permconfig.MCPServerProfile{
		route("envroute", permconfig.MCPPreregisteredClientProfile{ID: "a", SecretEnv: "MECATL_CLIENT_SECRET"}),
		route("fileroute", permconfig.MCPPreregisteredClientProfile{ID: "b", SecretFile: "/run/secrets/client"}),
	}, "https://broker.example/callback", nil, nil, nil)
	if o := config.Profiles[0].OAuth; o.ClientSecretEnv != "MECATL_CLIENT_SECRET" || o.ClientSecretFile != "" {
		t.Fatalf("env route = %#v", o)
	}
	if o := config.Profiles[1].OAuth; o.ClientSecretFile != "/run/secrets/client" || o.ClientSecretEnv != "" {
		t.Fatalf("file route = %#v", o)
	}
}

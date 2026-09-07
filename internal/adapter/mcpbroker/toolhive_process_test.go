package mcpbroker

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	jwt "github.com/golang-jwt/jwt/v5"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ory/fosite"
	"github.com/redis/go-redis/v9"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// registerClientSpy wraps storage.NewMemoryStorage(), recording whether
// RegisterClient was called on IT, so a test can prove newToolHiveProcess used
// the config.AuthStorage it was given rather than silently falling back to a
// fresh storage.NewMemoryStorage().
type registerClientSpy struct {
	*storage.MemoryStorage
	registered atomic.Bool
	client     fosite.Client
}

func newRegisterClientSpy() *registerClientSpy {
	return &registerClientSpy{MemoryStorage: storage.NewMemoryStorage()}
}

func (s *registerClientSpy) RegisterClient(ctx context.Context, client fosite.Client) error {
	s.client = client
	s.registered.Store(true)
	return s.MemoryStorage.RegisterClient(ctx, client)
}

func TestNewToolHiveProcessUsesConfiguredAuthStorage(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	profile := protectedToolHiveProfile("private")
	profile.Static = []StaticTool{{Name: "echo", Description: "echo", Schema: json.RawMessage(`{"type":"object"}`)}}
	spy := newRegisterClientSpy()
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
		CallbackURL: "https://broker.example/callback", Profiles: []ToolHiveProfile{profile}, AuthStorage: spy,
	})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	if !spy.registered.Load() {
		t.Fatal("NewToolHiveProcess did not register the client on the configured AuthStorage")
	}
}

func TestInvariant_singleton_broker_named_proofs_use_production_paths(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	profile := protectedToolHiveProfile("proof")
	profile.Static = []StaticTool{{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)}}
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
		CallbackURL: "https://broker.example/callback", Profiles: []ToolHiveProfile{profile},
	})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	mux := http.NewServeMux()
	if err := process.Handlers.Mount(mux, "/callback"); err != nil {
		t.Fatalf("mount production ToolHive callback bundle: %v", err)
	}
	// The fixture reaches the mounted fixed bundle, not an attachment or a
	// hand-written callback. The protected resource endpoint is supplied by the
	// embedded ToolHive process and must remain live with its authorization routes.
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, toolHiveBasePath+"/.well-known/oauth-protected-resource", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("embedded protected-resource handler = %d, want 200", response.Code)
	}
	attachment, _, err := process.Runtime.AttachSession(t.Context(), "production-proof")
	if err != nil || attachment.Binding() == "" || len(attachment.Tools()) != 1 {
		t.Fatalf("production runtime attachment = %#v, %v", attachment, err)
	}
}

func TestToolHiveProtectedClientIsConfidential(t *testing.T) {
	assertToolHiveProtectedClientIsConfidential(t)
}

func assertToolHiveProtectedClientIsConfidential(t *testing.T) {
	t.Helper()
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	profile := protectedToolHiveProfile("private")
	profile.Static = []StaticTool{{Name: "echo", Description: "echo", Schema: json.RawMessage(`{"type":"object"}`)}}
	spy := newRegisterClientSpy()
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
		CallbackURL: "https://broker.example/callback", Profiles: []ToolHiveProfile{profile}, AuthStorage: spy,
	})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })

	client := spy.client
	if client == nil {
		t.Fatal("registered client is nil")
	}
	if client.IsPublic() {
		t.Fatal("protected ToolHive client is public")
	}
	openid, ok := client.(fosite.OpenIDConnectClient)
	if !ok {
		t.Fatalf("registered client type %T does not expose token-endpoint authentication", client)
	}
	if got := openid.GetTokenEndpointAuthMethod(); got != oauthproto.TokenEndpointAuthMethodClientSecretBasic {
		t.Fatalf("token endpoint auth method = %q, want %q", got, oauthproto.TokenEndpointAuthMethodClientSecretBasic)
	}
	secret := process.protectedTarget.clientSecret
	if secret == "" {
		t.Fatal("protected target client secret is empty")
	}
	if string(client.GetHashedSecret()) == secret {
		t.Fatal("registered client stores the raw secret")
	}
	if err := registration.SHA256Hasher.Compare(t.Context(), client.GetHashedSecret(), []byte(secret)); err != nil {
		t.Fatalf("registered secret hash does not verify: %v", err)
	}
	if err := registration.SHA256Hasher.Compare(t.Context(), client.GetHashedSecret(), []byte("wrong-secret")); err == nil {
		t.Fatal("registered secret hash accepted a different secret")
	}
	transaction := &authorizationTransaction{route: process.protectedTarget}
	oauthConfig := transaction.oauthConfig(secret)
	if oauthConfig.Endpoint.AuthStyle != oauth2.AuthStyleInHeader || oauthConfig.ClientSecret != secret {
		t.Fatalf("broker exchange config = %#v, want private secret with HTTP Basic", oauthConfig)
	}
}

func TestADR_0299_BrokerClientSecretNeverCrossesPublicBoundary(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	profile := protectedToolHiveProfile("private")
	profile.Static = []StaticTool{{Name: "echo", Description: "echo", Schema: json.RawMessage(`{"type":"object"}`)}}
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
		CallbackURL: "https://broker.example/callback", Profiles: []ToolHiveProfile{profile},
	})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	secret := process.protectedTarget.clientSecret

	attachment, _, err := process.Runtime.AttachSession(t.Context(), "secret-boundary")
	if err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	t.Cleanup(func() { _, _ = attachment.Close(context.Background()) })
	presentation, err := attachment.(contract.WorkspaceEnrollmentAttachment).BeginWorkspaceEnrollment(t.Context())
	if err != nil {
		t.Fatalf("BeginWorkspaceEnrollment: %v", err)
	}
	if strings.Contains(presentation.URL, secret) {
		t.Fatal("workspace enrollment presentation exposes the broker client secret")
	}
	for _, spec := range attachment.(*Attachment).Tools() {
		if strings.Contains(spec.Spec().Description, secret) || strings.Contains(string(spec.Spec().Schema), secret) {
			t.Fatal("model-facing tool specification exposes the broker client secret")
		}
	}
}

func TestNewToolHiveProcessFallsBackToMemoryStorageWhenUnconfigured(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	profile := protectedToolHiveProfile("private")
	profile.Static = []StaticTool{{Name: "echo", Description: "echo", Schema: json.RawMessage(`{"type":"object"}`)}}
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
		CallbackURL: "https://broker.example/callback", Profiles: []ToolHiveProfile{profile},
	})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
}

func TestAnonymousOnlyProcessPublishesNoVMCPRoute(t *testing.T) {
	var requests atomic.Int32
	anonymous := toolHiveDiscoveryServer(t, "get_status", &requests)
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
		Profiles: []ToolHiveProfile{{Name: "status", URL: anonymous.URL, Auth: authNone}},
	})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	if process.Handlers.VMCP != nil {
		t.Fatal("an all-auth:none process published the vMCP handler with no protected bundle to guard it")
	}
	if !process.Handlers.Empty() {
		t.Fatalf("an all-auth:none process published unexpected handlers: %+v", process.Handlers)
	}
}

func TestProtectedBrokerVMCPEndpointRejectsAnonymousOverNetwork(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	var requests atomic.Int32
	anonymous := toolHiveDiscoveryServer(t, "get_status", &requests)
	protected := protectedToolHiveProfile("private")
	protected.Static = []StaticTool{{Name: "echo", Description: "echo", Schema: json.RawMessage(`{"type":"object"}`)}}
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
		CallbackURL: "https://broker.example/callback",
		Profiles:    []ToolHiveProfile{protected, {Name: "status", URL: anonymous.URL, Auth: authNone}},
	})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	if process.Handlers.VMCP == nil {
		t.Fatal("a process with a protected upstream must publish the vMCP handler")
	}

	mux := http.NewServeMux()
	if err := process.Handlers.Mount(mux, "/callback"); err != nil {
		t.Fatalf("mount handlers: %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"anon","version":"1.0"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"private.echo","arguments":{}}}`,
	} {
		response, err := http.Post(server.URL+toolHiveMCPPath, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("anonymous POST %s: %v", body, err)
		}
		payload, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous %s: status = %d, body = %s, want %d", body, response.StatusCode, payload, http.StatusUnauthorized)
		}
	}
}

func TestToolHiveConstructionPreservesProtectedMapping(t *testing.T) {
	profiles := []ToolHiveProfile{
		protectedToolHiveProfile("GitHub_Cloud"),
		{Name: "public", URL: "https://public.example/mcp", Auth: authNone},
	}

	construction, err := compileToolHiveConstruction(profiles, "https://broker.example/v1/mcp/broker")
	if err != nil {
		t.Fatalf("compileToolHiveConstruction: %v", err)
	}
	if got := construction.upstreams[0].Name; got != "github-cloud" {
		t.Fatalf("protected upstream = %q", got)
	}
	if got := construction.protectedBackends; !reflect.DeepEqual(got, []string{"GitHub_Cloud"}) {
		t.Fatalf("protected backends = %v", got)
	}
	if got := construction.providerByBackend["GitHub_Cloud"]; got != "github-cloud" {
		t.Fatalf("GitHub provider = %q", got)
	}
	if construction.backends[0].AuthConfig.UpstreamInject.ProviderName != "github-cloud" {
		t.Fatalf("backend provider mapping = %#v", construction.backends)
	}
}

func TestToolHiveConstructionMapsRefreshTokenRequest(t *testing.T) {
	for _, upstreamType := range []string{"oidc", "oauth2"} {
		for _, requestRefreshToken := range []bool{true, false} {
			t.Run(upstreamType+"/request_refresh_token="+strconv.FormatBool(requestRefreshToken), func(t *testing.T) {
				profile := protectedToolHiveProfile("private")
				profile.OAuth.RequestRefreshToken = requestRefreshToken
				if upstreamType == "oidc" {
					profile.OAuth.Issuer = "https://issuer.example"
					profile.OAuth.AuthorizationEndpoint = ""
					profile.OAuth.TokenEndpoint = ""
				}

				construction, err := compileToolHiveConstruction([]ToolHiveProfile{profile}, "https://broker.example")
				if err != nil {
					t.Fatalf("compileToolHiveConstruction: %v", err)
				}
				var scopes []string
				var params map[string]string
				if upstreamType == "oidc" {
					scopes = construction.upstreams[0].OIDCConfig.Scopes
					params = construction.upstreams[0].OIDCConfig.AdditionalAuthorizationParams
				} else {
					scopes = construction.upstreams[0].OAuth2Config.Scopes
					params = construction.upstreams[0].OAuth2Config.AdditionalAuthorizationParams
				}
				if !reflect.DeepEqual(scopes, []string{"openid"}) {
					t.Fatalf("scopes = %v", scopes)
				}
				var want map[string]string
				if requestRefreshToken {
					want = map[string]string{"access_type": "offline"}
				}
				if !reflect.DeepEqual(params, want) {
					t.Fatalf("AdditionalAuthorizationParams = %#v, want %#v", params, want)
				}
			})
		}
	}
}

func TestToolHiveConstructionRejectsInvalidProfiles(t *testing.T) {
	tests := []struct {
		name     string
		profiles []ToolHiveProfile
	}{
		{"duplicate backend", []ToolHiveProfile{{Name: "same", URL: "https://one.example/mcp", Auth: authNone}, {Name: "SAME", URL: "https://two.example/mcp", Auth: authNone}}},
		{"dotted backend", []ToolHiveProfile{{Name: "git.hub", URL: "https://git.example/mcp", Auth: authNone}}},
		{"missing oauth", []ToolHiveProfile{{Name: "private", URL: "https://private.example/mcp", Auth: authOAuth}}},
		{"missing client", []ToolHiveProfile{{Name: "private", URL: "https://private.example/mcp", Auth: authOAuth, OAuth: &ToolHiveOAuth{Issuer: "https://issuer.example"}}}},
		{"partial endpoints", []ToolHiveProfile{{Name: "private", URL: "https://private.example/mcp", Auth: authOAuth, OAuth: &ToolHiveOAuth{ClientID: "client", AuthorizationEndpoint: "https://issuer.example/authorize"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := compileToolHiveConstruction(test.profiles, "https://broker.example/v1/mcp/broker"); !errors.Is(err, ErrInvalidCatalogue) {
				t.Fatalf("compileToolHiveConstruction error = %v, want ErrInvalidCatalogue", err)
			}
		})
	}
}

func TestADR_0298_ToolHiveConstructionMapsEveryProtectedProfileInOrder(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	first := protectedToolHiveProfile("GitHub_Cloud")
	first.Static = []StaticTool{{Name: "reviewed", Schema: json.RawMessage(`{"type":"object"}`)}}
	profiles := []ToolHiveProfile{
		first,
		{Name: "public", URL: "https://public.example/mcp", Auth: authNone},
		protectedToolHiveProfile("Calendar"),
	}

	construction, err := compileToolHiveConstruction(profiles, "https://broker.example/v1/mcp/broker")
	if err != nil {
		t.Fatalf("compileToolHiveConstruction: %v", err)
	}
	if got := construction.upstreams; len(got) != 2 || got[0].Name != "github-cloud" || got[1].Name != "calendar" {
		t.Fatalf("protected upstreams = %#v", got)
	}
	if got := construction.protectedBackends; !reflect.DeepEqual(got, []string{"GitHub_Cloud", "Calendar"}) {
		t.Fatalf("protected backends = %v", got)
	}
	for index, want := range []string{"github-cloud", "calendar"} {
		backend := construction.backends[index*2]
		if got := backend.AuthConfig.UpstreamInject.ProviderName; got != want {
			t.Fatalf("backend %q provider = %q, want %q", backend.Name, got, want)
		}
		if got := construction.providerByBackend[backend.Name]; got != want {
			t.Fatalf("providerByBackend[%q] = %q, want %q", backend.Name, got, want)
		}
	}

	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{CallbackURL: "https://broker.example/callback", Profiles: []ToolHiveProfile{first, profiles[2]}})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	attachment, _, err := process.Runtime.AttachSession(t.Context(), "multi-profile")
	if err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	t.Cleanup(func() { _, _ = attachment.Close(context.Background()) })
	enroller, ok := attachment.(contract.WorkspaceEnrollmentAttachment)
	if !ok {
		t.Fatal("Attachment does not implement WorkspaceEnrollmentAttachment")
	}
	presentation, err := enroller.BeginWorkspaceEnrollment(t.Context())
	if err != nil {
		t.Fatalf("BeginWorkspaceEnrollment: %v", err)
	}
	if got := presentation.Ref.RequiredServices; got != 2 {
		t.Fatalf("RequiredServices = %d, want 2 protected profiles including static declarations", got)
	}
}

func TestADR_0298_ToolHiveConstructionRejectsCollidingProviderKeys(t *testing.T) {
	_, err := compileToolHiveConstruction([]ToolHiveProfile{protectedToolHiveProfile("foo_bar"), protectedToolHiveProfile("foo-bar")}, "https://broker.example/v1/mcp/broker")
	if !errors.Is(err, ErrInvalidCatalogue) || !strings.Contains(err.Error(), `map to provider "foo-bar"`) {
		t.Fatalf("compileToolHiveConstruction error = %v, want colliding provider-key rejection", err)
	}
}

func TestStaticProtectedRoutesRejectInvalidDeclarationsAndCollisions(t *testing.T) {
	valid := StaticTool{Name: "read", Schema: json.RawMessage(`{"type":"object"}`)}
	for _, test := range []struct {
		name     string
		tools    []StaticTool
		occupied []string
	}{
		{name: "invalid schema", tools: []StaticTool{{Name: "read", Schema: json.RawMessage(`[]`)}}},
		{name: "duplicate declaration", tools: []StaticTool{valid, valid}},
		{name: "occupied name", tools: []StaticTool{valid}, occupied: []string{"mcp__private__read"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := protectedToolHiveProfile("private")
			profile.Static = test.tools
			construction, err := compileToolHiveConstruction([]ToolHiveProfile{profile}, "https://broker.example/v1/mcp/broker")
			if err != nil {
				t.Fatalf("compileToolHiveConstruction: %v", err)
			}
			if _, err := compileStaticProtectedRoutes(construction, &oauthRoute{}, nil, test.occupied); !errors.Is(err, ErrInvalidCatalogue) {
				t.Fatalf("compileStaticProtectedRoutes error = %v, want ErrInvalidCatalogue", err)
			}
		})
	}
}

// admitStaticForGenericAuthorizationTest reconstructs a generic per-tool OAuth
// route for Runtime-only tests. NewToolHiveProcess itself publishes broker routes.
func admitStaticForGenericAuthorizationTest(t *testing.T, process *Process) {
	t.Helper()
	routes, err := compileStaticProtectedRoutes(process.construction, process.protectedTarget, nil, process.occupied)
	if err != nil {
		t.Fatalf("compile generic static routes: %v", err)
	}
	for i := range routes {
		routes[i].broker = false
	}
	process.Runtime.catalogue = &Catalogue{routes: routes}
}

func TestToolHiveProcessAllowsUnambiguousUnderscoreRoutingKeys(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	var anonymousRequests atomic.Int32
	anonymous := toolHiveDiscoveryServer(t, "enterprise_create_issue", &anonymousRequests)
	protected := protectedToolHiveProfile("github_enterprise")
	protected.Static = []StaticTool{{Name: "create_issue", Schema: json.RawMessage(`{"type":"object"}`)}}

	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
		CallbackURL: "https://broker.example/callback",
		Profiles: []ToolHiveProfile{
			{Name: "github", URL: anonymous.URL, Auth: authNone},
			protected,
		},
	})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	if anonymousRequests.Load() == 0 {
		t.Fatal("anonymous route was not discovered")
	}
	anonymousName, err := toolHiveAdvertisedToolName("github", "mcp__github__enterprise_create_issue")
	if err != nil {
		t.Fatalf("anonymous advertised name: %v", err)
	}
	protectedName, err := toolHiveAdvertisedToolName("github_enterprise", "mcp__github_enterprise__create_issue")
	if err != nil {
		t.Fatalf("protected advertised name: %v", err)
	}
	if anonymousName != "github.enterprise_create_issue" || protectedName != "github_enterprise.create_issue" || anonymousName == protectedName {
		t.Fatalf("advertised names = %q and %q", anonymousName, protectedName)
	}
}

func TestGenericStaticProtectedOIDCAndCIMDUseToolHiveTarget(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	issuer := toolHiveOIDCIssuer(t)
	for _, test := range []struct {
		name     string
		clientID string
		secret   string
	}{
		{name: "OIDC", clientID: "registered-client", secret: "MECATL_TEST_CLIENT_SECRET"},
		{name: "CIMD", clientID: "https://client.example/oauth-client.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := protectedToolHiveProfile("private")
			profile.OAuth.Issuer = issuer.URL
			profile.OAuth.AuthorizationEndpoint = ""
			profile.OAuth.TokenEndpoint = ""
			profile.OAuth.ClientID = test.clientID
			profile.OAuth.ClientSecretEnv = test.secret
			profile.Static = []StaticTool{{Name: "read", Description: "read", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}}

			process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
				CallbackURL: "https://broker.example/callback", Profiles: []ToolHiveProfile{profile},
			})
			if err != nil {
				t.Fatalf("NewToolHiveProcess: %v", err)
			}
			t.Cleanup(func() { _ = process.Close() })
			admitStaticForGenericAuthorizationTest(t, process)
			if len(process.Runtime.catalogue.routes) != 1 {
				t.Fatalf("routes = %#v", process.Runtime.catalogue.routes)
			}
			route := process.Runtime.catalogue.routes[0]
			if route.oauth == nil || route.oauth != process.protectedTarget {
				t.Fatal("static route did not retain the shared ToolHive protected target")
			}
			if route.oauth.authorizationEndpoint != "https://broker.example"+toolHiveBasePath+"/oauth/authorize" ||
				route.oauth.tokenEndpoint != "https://broker.example"+toolHiveBasePath+"/oauth/token" {
				t.Fatalf("protected target = %#v", route.oauth)
			}
			if !reflect.DeepEqual(process.construction.protectedBackends, []string{"private"}) || len(process.construction.upstreams) != 1 {
				t.Fatalf("construction = %#v", process.construction)
			}
			attached, _, err := process.Runtime.AttachSession(t.Context(), session.SessionID("static-"+strings.ToLower(test.name)))
			if err != nil {
				t.Fatalf("AttachSession: %v", err)
			}
			t.Cleanup(func() { _, _ = attached.Close(context.Background()) })
			wrapped := toolByName(t, attached.(*Attachment), "mcp__private__read")
			authorization, required, err := wrapped.(tool.AuthorizationRequester).RequestAuthorization(t.Context(), session.NewToolCall("call-1", wrapped.Spec().Name, json.RawMessage(`{}`)))
			if err != nil || !required {
				t.Fatalf("RequestAuthorization = (%+v, %v, %v)", authorization, required, err)
			}
			presentation, err := attached.PresentAuthorization(t.Context(), authorization)
			if err != nil || !strings.HasPrefix(presentation, route.oauth.authorizationEndpoint+"?") {
				t.Fatalf("embedded target presentation = %q, %v", presentation, err)
			}
		})
	}
}

func TestToolHiveStaticToolAuthorizationStartsBundle(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	first := protectedToolHiveProfile("github")
	first.Static = []StaticTool{{Name: "get_me", Schema: json.RawMessage(`{"type":"object"}`)}}
	second := protectedToolHiveProfile("calendar")
	second.Static = []StaticTool{{Name: "list_events", Schema: json.RawMessage(`{"type":"object"}`)}}
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{CallbackURL: "https://broker.example/callback", Profiles: []ToolHiveProfile{first, second}})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	attached, _, err := process.Runtime.AttachSession(t.Context(), "bundle-session")
	if err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	t.Cleanup(func() { _, _ = attached.Close(context.Background()) })
	attachment := attached.(*Attachment)
	wrapped := toolByName(t, attachment, "mcp__github__get_me")
	requester, ok := wrapped.(tool.AuthorizationRequester)
	if !ok {
		t.Fatal("visible static tool lacks authorization requester")
	}
	call := session.NewToolCall("call-1", wrapped.Spec().Name, json.RawMessage(`{}`))
	authorization, required, err := requester.RequestAuthorization(t.Context(), call)
	if err != nil || !required {
		t.Fatalf("RequestAuthorization = (%+v, %v, %v)", authorization, required, err)
	}
	attachment.logical.mu.RLock()
	transaction := attachment.logical.authorizations[authorizationIdentity{id: authorization.ID, binding: authorization.Binding}]
	attachment.logical.mu.RUnlock()
	if transaction == nil || !reflect.DeepEqual(transaction.bundleBackends, []string{"github", "calendar"}) {
		t.Fatalf("transaction = %#v, want ToolHive bundle", transaction)
	}
	presentation, err := attachment.PresentAuthorization(t.Context(), authorization)
	if err != nil || !strings.HasPrefix(presentation, process.protectedTarget.authorizationEndpoint+"?") {
		t.Fatalf("PresentAuthorization = %q, %v", presentation, err)
	}
	var brokerCalls int
	process.Runtime.authorizedCaller = func(_ context.Context, _ SessionRef, backend string, call session.ToolCall, source oauth2.TokenSource) (session.ToolResult, error) {
		token, err := source.Token()
		if err != nil || token.AccessToken != "bundle-token" || backend != "calendar" {
			return session.ToolResult{}, errors.New("static route did not use the bundle credential")
		}
		brokerCalls++
		return session.NewToolResult(call.ID, "connected"), nil
	}
	attachment.logical.mu.Lock()
	delete(attachment.logical.authorizations, transaction.identity)
	attachment.logical.brokerCredential = &oauthGrant{token: &oauth2.Token{AccessToken: "bundle-token", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}, executed: make(map[session.ToolCallID][32]byte)}
	attachment.logical.mu.Unlock()
	secondTool := toolByName(t, attachment, "mcp__calendar__list_events")
	secondCall := session.NewToolCall("call-2", secondTool.Spec().Name, json.RawMessage(`{}`))
	if _, required, err := secondTool.(tool.AuthorizationRequester).RequestAuthorization(t.Context(), secondCall); err != nil || required {
		t.Fatalf("second RequestAuthorization = (%v, %v), want satisfied bundle", required, err)
	}
	result, err := secondTool.Execute(t.Context(), secondCall, tool.Environment{})
	if err != nil || result.Content != "connected" || brokerCalls != 1 {
		t.Fatalf("bundle route execution = (%+v, %v), calls=%d", result, err, brokerCalls)
	}
}

func TestADR_0298_StaticProtectedToolsAreVisibleBeforeEnrollment(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	var anonymousRequests, protectedRequests atomic.Int32
	anonymous := toolHiveDiscoveryServer(t, "status", &anonymousRequests)
	protected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		protectedRequests.Add(1)
		http.Error(w, "authorization required", http.StatusUnauthorized)
	}))
	t.Cleanup(protected.Close)
	private := protectedToolHiveProfile("GitHub_API")
	private.URL = protected.URL
	private.Static = []StaticTool{{Name: "reviewed", Description: "comparison only", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}}

	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{CallbackURL: "https://broker.example/callback", Profiles: []ToolHiveProfile{
		{Name: "public", URL: anonymous.URL, Auth: authNone}, private,
	}})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	if anonymousRequests.Load() == 0 {
		t.Fatal("anonymous upstream was not discovered")
	}
	if got := protectedRequests.Load(); got != 0 {
		t.Fatalf("protected startup requests = %d, want 0", got)
	}
	if got := process.Runtime.catalogue.Specs(); len(got) != 2 || got[0].Name != "mcp__GitHub_API__reviewed" || got[1].Name != "mcp__public__status" {
		t.Fatalf("startup catalogue = %#v, want anonymous and declared protected tools", got)
	}
	attachment, _, err := process.Runtime.AttachSession(t.Context(), "static-session")
	if err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	t.Cleanup(func() { _, _ = attachment.Close(context.Background()) })
	if got := toolNames(attachment.(*Attachment).Tools()); !reflect.DeepEqual(got, []string{"mcp__GitHub_API__reviewed", "mcp__public__status"}) {
		t.Fatalf("pre-enrollment attachment tools = %v, want anonymous and declared protected tools", got)
	}
	route, ok := attachment.(*Attachment).lookupRoute("mcp__GitHub_API__reviewed")
	if !ok || route.oauth != process.protectedTarget || !route.broker {
		t.Fatalf("declared route = %#v, want ToolHive OAuth and broker execution", route)
	}
	enroller := attachment.(contract.WorkspaceEnrollmentAttachment)
	presentation, err := enroller.BeginWorkspaceEnrollment(t.Context())
	if err != nil || presentation.Ref.RequiredServices != 1 {
		t.Fatalf("BeginWorkspaceEnrollment = (%+v, %v)", presentation, err)
	}
}

func TestADR_0298_ToolHiveEnrollmentUsesRealIdentityMiddleware(t *testing.T) {
	for _, test := range []struct {
		name string
		mode string
	}{
		{name: "OIDC discovery", mode: "oidc"},
		{name: "CIMD document", mode: "cimd"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("MECATL_TEST_CLIENT_SECRET", "client-secret")
			mux := http.NewServeMux()
			gateway := httptest.NewUnstartedServer(mux)
			gateway.StartTLS()
			t.Cleanup(gateway.Close)
			roots := x509.NewCertPool()
			roots.AddCert(gateway.Certificate())

			var discoveryRequests, metadataRequests atomic.Int32
			var oidcNonce atomic.Value
			oidcKey, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatalf("generate OIDC key: %v", err)
			}
			var upstreamOAuth *httptest.Server
			upstreamOAuth = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/.well-known/openid-configuration":
					discoveryRequests.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"issuer": upstreamOAuth.URL, "authorization_endpoint": upstreamOAuth.URL + "/authorize",
						"token_endpoint": upstreamOAuth.URL + "/token", "jwks_uri": upstreamOAuth.URL + "/jwks",
						"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
						"id_token_signing_alg_values_supported": []string{"RS256"},
					})
				case "/client-metadata.json":
					metadataRequests.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"client_id": upstreamOAuth.URL + "/client-metadata.json", "client_name": "mecatl test client",
						"redirect_uris": []string{gateway.URL + toolHiveBasePath + "/oauth/callback"},
						"grant_types":   []string{"authorization_code"}, "response_types": []string{"code"},
						"token_endpoint_auth_method": "none",
					})
				case "/authorize":
					if test.mode == "oidc" {
						oidcNonce.Store(request.URL.Query().Get("nonce"))
					}
					if test.mode == "cimd" {
						metadataURL := upstreamOAuth.URL + "/client-metadata.json"
						if request.URL.Query().Get("client_id") != metadataURL {
							http.Error(w, "unexpected CIMD client", http.StatusBadRequest)
							return
						}
						metadataClient := &http.Client{Timeout: 2 * time.Second}
						metadataRequest, err := http.NewRequestWithContext(request.Context(), http.MethodGet, metadataURL, nil)
						if err != nil {
							http.Error(w, "invalid metadata request", http.StatusInternalServerError)
							return
						}
						metadataResponse, err := metadataClient.Do(metadataRequest)
						if err != nil {
							http.Error(w, "metadata unavailable", http.StatusBadGateway)
							return
						}
						var document struct {
							ClientID     string   `json:"client_id"`
							RedirectURIs []string `json:"redirect_uris"`
						}
						decodeErr := json.NewDecoder(metadataResponse.Body).Decode(&document)
						_ = metadataResponse.Body.Close()
						if decodeErr != nil || document.ClientID != metadataURL || len(document.RedirectURIs) != 1 || document.RedirectURIs[0] != request.URL.Query().Get("redirect_uri") {
							http.Error(w, "invalid client metadata", http.StatusBadRequest)
							return
						}
					}
					redirect := request.URL.Query().Get("redirect_uri")
					state := request.URL.Query().Get("state")
					http.Redirect(w, request, redirect+"?"+url.Values{"code": {"upstream-code"}, "state": {state}}.Encode(), http.StatusFound)
				case "/token":
					response := map[string]any{"access_token": "upstream-token", "token_type": "Bearer", "expires_in": 3600}
					if test.mode == "oidc" {
						now := time.Now()
						nonce, _ := oidcNonce.Load().(string)
						idToken := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
							"iss": upstreamOAuth.URL, "sub": "test-user", "aud": "registered-client", "nonce": nonce,
							"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
						})
						idToken.Header["kid"] = "test-key"
						signed, err := idToken.SignedString(oidcKey)
						if err != nil {
							http.Error(w, "sign ID token", http.StatusInternalServerError)
							return
						}
						response["id_token"] = signed
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(response)
				case "/jwks":
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
						"kty": "RSA", "use": "sig", "kid": "test-key", "alg": "RS256",
						"n": base64.RawURLEncoding.EncodeToString(oidcKey.N.Bytes()),
						"e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
					}}})
				default:
					http.NotFound(w, request)
				}
			}))
			t.Cleanup(upstreamOAuth.Close)

			var calls atomic.Int32
			upstreamMCP := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "protected", Version: "v1"}, nil)
			mcpsdk.AddTool(upstreamMCP, &mcpsdk.Tool{Name: "create"}, func(_ context.Context, _ *mcpsdk.CallToolRequest, input struct {
				Title string `json:"title"`
			}) (*mcpsdk.CallToolResult, any, error) {
				calls.Add(1)
				return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "created:" + input.Title}}}, nil, nil
			})
			upstreamHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return upstreamMCP }, &mcpsdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
			protectedMCP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.Header.Get("Authorization") != "Bearer upstream-token" {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				upstreamHandler.ServeHTTP(w, request)
			}))
			t.Cleanup(protectedMCP.Close)

			oauth := &ToolHiveOAuth{Scopes: []string{"openid"}}
			if test.mode == "oidc" {
				oauth.Issuer, oauth.ClientID, oauth.ClientSecretEnv = upstreamOAuth.URL, "registered-client", "MECATL_TEST_CLIENT_SECRET"
			} else {
				oauth.AuthorizationEndpoint, oauth.TokenEndpoint = upstreamOAuth.URL+"/authorize", upstreamOAuth.URL+"/token"
				oauth.ClientID = upstreamOAuth.URL + "/client-metadata.json"
			}
			profile := ToolHiveProfile{Name: "private", URL: protectedMCP.URL, Auth: authOAuth, OAuth: oauth,
				Static: []StaticTool{{Name: "create", Schema: json.RawMessage(`{"type":"object"}`)}}}
			process, err := newToolHiveProcess(t.Context(), ToolHiveConfig{CallbackURL: gateway.URL + "/callback", Profiles: []ToolHiveProfile{profile}}, toolHiveProcessOptions{
				runtimeOptions: []Option{WithOAuthLoopbackForTest(t, roots)}, brokerHTTPClient: gateway.Client(),
			})
			if err != nil {
				t.Fatalf("newToolHiveProcess: %v", err)
			}
			t.Cleanup(func() { _ = process.Close() })
			if err := process.Handlers.Mount(mux, "/callback"); err != nil {
				t.Fatalf("mount handlers: %v", err)
			}
			attached, _, err := process.Runtime.AttachSession(t.Context(), session.SessionID("real-flow-"+test.mode))
			if err != nil {
				t.Fatalf("AttachSession: %v", err)
			}
			t.Cleanup(func() { _, _ = attached.Close(context.Background()) })
			enroller := attached.(contract.WorkspaceEnrollmentAttachment)
			presentation, err := enroller.BeginWorkspaceEnrollment(t.Context())
			if err != nil || !presentation.Valid() {
				t.Fatalf("BeginWorkspaceEnrollment = (%+v, %v)", presentation, err)
			}
			presentedURL, err := url.Parse(presentation.URL)
			if err != nil || presentedURL.Query().Get("resource") != process.protectedTarget.resource || process.protectedTarget.resource == "" {
				t.Fatalf("presentation resource = %q, want %q (err=%v)", presentedURL.Query().Get("resource"), process.protectedTarget.resource, err)
			}
			if got, want := toolNames(attached.(*Attachment).Tools()), []string{"mcp__private__create"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("pre-enrollment tools = %v, want %v", got, want)
			}
			flowCtx, cancelFlow := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancelFlow()
			request, err := http.NewRequestWithContext(flowCtx, http.MethodGet, presentation.URL, nil)
			if err != nil {
				t.Fatalf("build authorization request: %v", err)
			}
			client := gateway.Client()
			client.Timeout = 5 * time.Second
			response, err := client.Do(request)
			if err != nil {
				t.Fatalf("complete authorization: %v", err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("callback status = %d", response.StatusCode)
			}
			connected, err := enroller.ObserveWorkspaceEnrollment(t.Context(), presentation.Ref)
			if err != nil || connected.Status != contract.WorkspaceEnrollmentConnected {
				t.Fatalf("ObserveWorkspaceEnrollment = (%+v, %v)", connected, err)
			}
			wrapped := toolByName(t, attached.(*Attachment), "mcp__private__create")
			if _, asksAgain := wrapped.(tool.AuthorizationRequester); asksAgain {
				t.Fatal("connected ToolHive wrapper exposes a second authorization flow")
			}
			call := session.NewToolCall("call-1", wrapped.Spec().Name, json.RawMessage(`{"title":"one"}`))
			result, err := wrapped.Execute(t.Context(), call, tool.Environment{})
			if err != nil || result.IsError || !strings.HasPrefix(result.Content, "created:one") || calls.Load() != 1 {
				t.Fatalf("protected result = (%+v, %v), calls=%d", result, err, calls.Load())
			}
			if test.mode == "oidc" && discoveryRequests.Load() == 0 {
				t.Fatal("OIDC discovery was not requested")
			}
			if test.mode == "cimd" && metadataRequests.Load() == 0 {
				t.Fatal("CIMD metadata document was not requested")
			}
		})
	}
}

func TestGenericStaticOAuth2AuthorizationCallbackAndExactExecution(t *testing.T) {
	const upstreamToken = "upstream-token"
	upstreamOAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/authorize":
			redirect := request.URL.Query().Get("redirect_uri")
			state := request.URL.Query().Get("state")
			http.Redirect(w, request, redirect+"?"+url.Values{"code": {"upstream-code"}, "state": {state}}.Encode(), http.StatusFound)
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"` + upstreamToken + `","token_type":"Bearer","expires_in":3600}`))
		default:
			http.NotFound(w, request)
		}
	}))
	t.Cleanup(upstreamOAuth.Close)

	var calls atomic.Int32
	upstreamMCP := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "protected", Version: "v1"}, nil)
	mcpsdk.AddTool(upstreamMCP, &mcpsdk.Tool{Name: "create", Description: "create"}, func(_ context.Context, _ *mcpsdk.CallToolRequest, input struct {
		Title string `json:"title"`
	}) (*mcpsdk.CallToolResult, any, error) {
		calls.Add(1)
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "created:" + input.Title}}}, nil, nil
	})
	upstreamHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return upstreamMCP }, &mcpsdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	protectedMCP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+upstreamToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		upstreamHandler.ServeHTTP(w, request)
	}))
	t.Cleanup(protectedMCP.Close)

	mux := http.NewServeMux()
	gateway := httptest.NewUnstartedServer(mux)
	gateway.StartTLS()
	t.Cleanup(gateway.Close)
	roots := x509.NewCertPool()
	roots.AddCert(gateway.Certificate())

	profile := ToolHiveProfile{Name: "private", URL: protectedMCP.URL, Auth: authOAuth, OAuth: &ToolHiveOAuth{
		AuthorizationEndpoint: upstreamOAuth.URL + "/authorize", TokenEndpoint: upstreamOAuth.URL + "/token",
		ClientID: "public-client", Scopes: []string{"read"},
	}, Static: []StaticTool{{Name: "create", Description: "create", Schema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}}}`)}}}
	process, err := newToolHiveProcess(t.Context(), ToolHiveConfig{
		CallbackURL: gateway.URL + "/callback", Profiles: []ToolHiveProfile{profile},
	}, toolHiveProcessOptions{
		runtimeOptions: []Option{WithOAuthLoopbackForTest(t, roots)}, brokerHTTPClient: gateway.Client(),
	})
	if err != nil {
		t.Fatalf("newToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	admitStaticForGenericAuthorizationTest(t, process)
	if err := process.Handlers.Mount(mux, "/callback"); err != nil {
		t.Fatalf("mount handlers: %v", err)
	}

	attached, _, err := process.Runtime.AttachSession(t.Context(), "process-static-cimd")
	if err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	t.Cleanup(func() { _, _ = attached.Close(context.Background()) })
	wrapped := toolByName(t, attached.(*Attachment), "mcp__private__create")
	requester := wrapped.(tool.AuthorizationRequester)
	call := session.NewToolCall("call-1", wrapped.Spec().Name, json.RawMessage(`{"title":"one"}`))
	if _, err := wrapped.Execute(t.Context(), call, tool.Environment{}); !errors.Is(err, contract.ErrAuthorizationNotFound) || calls.Load() != 0 {
		t.Fatalf("pre-authorization execution = %v, calls=%d", err, calls.Load())
	}
	authorization, required, err := requester.RequestAuthorization(t.Context(), call)
	if err != nil || !required {
		t.Fatalf("RequestAuthorization = (%+v, %v, %v)", authorization, required, err)
	}
	wrongBinding := authorization
	wrongBinding.Binding = session.AuthorizationBinding("wrong-binding")
	if _, err := attached.PresentAuthorization(t.Context(), wrongBinding); !errors.Is(err, contract.ErrAuthorizationNotFound) {
		t.Fatalf("mismatched authorization binding error = %v", err)
	}
	presentation, err := attached.PresentAuthorization(t.Context(), authorization)
	if err != nil {
		t.Fatalf("PresentAuthorization: %v", err)
	}
	redirectCtx, cancelRedirect := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelRedirect()
	request, err := http.NewRequestWithContext(redirectCtx, http.MethodGet, presentation, nil)
	if err != nil {
		t.Fatalf("build authorization request: %v", err)
	}
	redirectClient := gateway.Client()
	redirectClient.Timeout = 5 * time.Second
	response, err := redirectClient.Do(request)
	if err != nil {
		t.Fatalf("complete embedded authorization: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d url=%s body=%q", response.StatusCode, response.Request.URL, body)
	}
	if _, err := attached.PresentAuthorization(t.Context(), authorization); !errors.Is(err, contract.ErrAuthorizationNotFound) {
		t.Fatalf("consumed authorization presentation error = %v", err)
	}
	if got, stillRequired, err := requester.RequestAuthorization(t.Context(), call); err != nil || stillRequired || got != (session.ExternalAuthorization{}) {
		t.Fatalf("post-callback RequestAuthorization = (%+v, %v, %v)", got, stillRequired, err)
	}
	mismatch := session.NewToolCall("call-2", wrapped.Spec().Name, json.RawMessage(`{"title":"two"}`))
	if _, err := wrapped.Execute(t.Context(), mismatch, tool.Environment{}); err == nil || calls.Load() != 0 {
		t.Fatalf("post-grant mismatched execution = %v, calls=%d", err, calls.Load())
	}
	result, err := wrapped.Execute(t.Context(), call, tool.Environment{})
	if err != nil || result.IsError || !strings.HasPrefix(result.Content, "created:one") || calls.Load() != 1 {
		t.Fatalf("protected result = (%+v, %v), calls=%d", result, err, calls.Load())
	}
	if _, err := wrapped.Execute(t.Context(), call, tool.Environment{}); err == nil || calls.Load() != 1 {
		t.Fatalf("consumed call replay = %v, calls=%d", err, calls.Load())
	}
}

func TestNewToolHiveProcessWiresDefaultProtectedTransport(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "protected", Version: "v1"}, nil)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "private.echo", Description: "echoes input"}, func(_ context.Context, _ *mcpsdk.CallToolRequest, input struct {
		Text string `json:"text"`
	}) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "protected:" + input.Text}}}, nil, nil
	})
	var requests atomic.Int32
	var requestedPath atomic.Value
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		requestedPath.Store(request.URL.Path)
		if request.URL.Path != toolHiveMCPPath {
			http.Error(w, "unexpected broker path", http.StatusNotFound)
			return
		}
		if request.Header.Get("Authorization") != "Bearer broker-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, request)
	}))
	t.Cleanup(local.Close)
	localURL, err := url.Parse(local.URL)
	if err != nil {
		t.Fatalf("parse local URL: %v", err)
	}
	originalTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "broker.example" {
			return originalTransport.RoundTrip(request)
		}
		clone := request.Clone(request.Context())
		clonedURL := *request.URL
		clonedURL.Scheme, clonedURL.Host = localURL.Scheme, localURL.Host
		clone.URL = &clonedURL
		clone.Host = localURL.Host
		return originalTransport.RoundTrip(clone)
	})
	defer func() { http.DefaultTransport = originalTransport }()

	profile := protectedToolHiveProfile("private")
	profile.Static = []StaticTool{{Name: "echo", Description: "echo", Schema: json.RawMessage(`{"type":"object"}`)}}
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
		CallbackURL: "https://broker.example/callback", Profiles: []ToolHiveProfile{profile},
	})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	result, err := process.Runtime.authorizedCaller(t.Context(), SessionRef{}, "private",
		session.NewToolCall("call-1", "mcp__private__echo", json.RawMessage(`{"text":"hello"}`)),
		oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "broker-token", TokenType: "Bearer"}))
	if err != nil {
		t.Fatalf("exported-constructor protected call: %v", err)
	}
	path, _ := requestedPath.Load().(string)
	if requests.Load() == 0 || path != toolHiveMCPPath || result.CallID != "call-1" || !strings.HasPrefix(result.Content, "protected:hello") || result.IsError {
		t.Fatalf("requests=%d path=%q result=%#v", requests.Load(), path, result)
	}
}

func TestStaticProtectedRouteCallbackResumesExactCall(t *testing.T) {
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"static-token","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(tokenServer.Close)

	profile := protectedToolHiveProfile("private")
	profile.OAuth.TokenEndpoint = tokenServer.URL
	profile.Static = []StaticTool{{Name: "create", Description: "create item", Schema: json.RawMessage(`{"type":"object"}`)}}
	target := &oauthRoute{
		authorizationEndpoint: "https://accounts.example/authorize", tokenEndpoint: tokenServer.URL,
		callbackURL: "https://client.example/oauth/callback", clientID: "client-id", secretEnv: "MECATL_TEST_CLIENT_SECRET", scopes: []string{"openid"},
	}
	construction, err := compileToolHiveConstruction([]ToolHiveProfile{profile}, "https://broker.example"+toolHiveBasePath)
	if err != nil {
		t.Fatalf("compileToolHiveConstruction: %v", err)
	}
	routes, err := compileStaticProtectedRoutes(construction, target, nil, nil)
	if err != nil {
		t.Fatalf("compileStaticProtectedRoutes: %v", err)
	}
	routes[0].broker = false
	roots := x509.NewCertPool()
	roots.AddCert(tokenServer.Certificate())
	calls := 0
	runtime, err := New(&Catalogue{routes: routes}, func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error) {
		return session.ToolResult{}, errors.New("anonymous caller used for protected route")
	}, WithAuthorizedCaller(func(_ context.Context, _ SessionRef, backend string, call session.ToolCall, source oauth2.TokenSource) (session.ToolResult, error) {
		token, tokenErr := source.Token()
		if tokenErr != nil {
			return session.ToolResult{}, tokenErr
		}
		if backend != "private" || token.AccessToken != "static-token" {
			return session.ToolResult{}, errors.New("wrong protected execution custody")
		}
		calls++
		return session.NewToolResult(call.ID, "created"), nil
	}), WithOAuthLoopbackForTest(t, roots), WithOAuthSecretResolver(func(context.Context, string) (string, error) {
		return "client-secret", nil
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	attachment, _ := attach(t, runtime, "static-callback")
	wrapped := toolByName(t, attachment, "mcp__private__create")
	requester := wrapped.(tool.AuthorizationRequester)
	call := session.NewToolCall("call-1", wrapped.Spec().Name, json.RawMessage(`{"title":"one"}`))
	authorization, required, err := requester.RequestAuthorization(t.Context(), call)
	if err != nil || !required {
		t.Fatalf("RequestAuthorization = (%+v, %v, %v)", authorization, required, err)
	}
	mismatch := session.NewToolCall("call-2", wrapped.Spec().Name, json.RawMessage(`{"title":"two"}`))
	if _, _, err := requester.RequestAuthorization(t.Context(), mismatch); err == nil || !strings.Contains(err.Error(), "different pending authorization") {
		t.Fatalf("mismatched call authorization error = %v", err)
	}
	presentation, err := attachment.PresentAuthorization(t.Context(), authorization)
	if err != nil {
		t.Fatalf("PresentAuthorization: %v", err)
	}
	presentationURL, err := url.Parse(presentation)
	if err != nil {
		t.Fatalf("parse presentation URL: %v", err)
	}
	state := presentationURL.Query().Get("state")
	if got := callback(t, runtime, "authorization-code", state).Code; got != http.StatusOK {
		t.Fatalf("callback status = %d", got)
	}
	if got, required, err := requester.RequestAuthorization(t.Context(), call); err != nil || required || got != (session.ExternalAuthorization{}) {
		t.Fatalf("post-callback authorization = (%+v, %v, %v)", got, required, err)
	}
	result, err := wrapped.Execute(t.Context(), call, tool.Environment{})
	if err != nil || result.Content != "created" || calls != 1 {
		t.Fatalf("resumed call = (%+v, %v), calls=%d", result, err, calls)
	}
}

func TestToolHiveProtectedCallerRejectsCrossBackendCapabilityDrift(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "broker", Version: "v1"}, nil)
	var calls atomic.Int32
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "github.enterprise_create_issue"}, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
		calls.Add(1)
		return &mcpsdk.CallToolResult{}, nil, nil
	})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)

	wanted, err := toolHiveAdvertisedToolName("github_enterprise", "mcp__github_enterprise__create_issue")
	if err != nil || wanted != "github_enterprise.create_issue" {
		t.Fatalf("protected advertised name = %q, %v", wanted, err)
	}
	caller := toolHiveProtectedCaller(httpServer.URL, nil)
	_, err = caller(t.Context(), SessionRef{}, "github_enterprise",
		session.NewToolCall("call-1", "mcp__github_enterprise__create_issue", json.RawMessage(`{}`)),
		oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "session-bearer", TokenType: "Bearer"}))
	if err == nil || calls.Load() != 0 {
		t.Fatalf("cross-backend capability drift = %v, calls=%d", err, calls.Load())
	}
}

func TestToolHiveProtectedCallerInjectsSessionBearer(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "protected", Version: "v1"}, nil)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "private.echo", Description: "echoes input"}, func(_ context.Context, _ *mcpsdk.CallToolRequest, input struct {
		Text string `json:"text"`
	}) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "protected:" + input.Text}}}, nil, nil
	})
	var requests, rejected atomic.Int32
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Header.Get("Authorization") != "Bearer session-bearer" {
			rejected.Add(1)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, request)
	}))
	t.Cleanup(httpServer.Close)

	caller := toolHiveProtectedCaller(httpServer.URL, nil)
	result, err := caller(t.Context(), SessionRef{}, "private", session.NewToolCall("call-1", "mcp__private__echo", json.RawMessage(`{"text":"hello"}`)), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "session-bearer", TokenType: "Bearer"}))
	if err != nil {
		t.Fatalf("protected caller: %v", err)
	}
	if rejected.Load() != 0 || requests.Load() == 0 {
		t.Fatalf("requests = %d, rejected = %d", requests.Load(), rejected.Load())
	}
	if result.CallID != "call-1" || result.Content != "protected:hello" || result.IsError {
		t.Fatalf("result = %#v", result)
	}
}

func TestProcessRollbackDrainsRuntimeThenResourcesAndCancelsContext(t *testing.T) {
	runtime, err := New(&Catalogue{}, func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error) {
		return session.ToolResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var order []string
	process := &Process{Runtime: runtime, ctx: ctx, resources: []ownedResource{
		{name: "context", close: func() error { order = append(order, "context"); cancel(); return nil }},
		{name: "auth", close: func() error { order = append(order, "auth"); return nil }},
		{name: "vmcp", close: func() error { order = append(order, "vmcp"); return nil }},
	}}
	process.rollback()
	if got := strings.Join(order, ","); got != "vmcp,auth,context" {
		t.Fatalf("resource rollback order = %q", got)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("process context remained live after rollback")
	}
	if _, _, err := runtime.AttachSession(t.Context(), "after-rollback"); !errors.Is(err, contract.ErrStateUnavailable) {
		t.Fatalf("runtime after rollback = %v, want contract.ErrStateUnavailable", err)
	}
}

func TestProcessCloseDrainsRuntimeThenResourcesCancelsContextAndIsIdempotent(t *testing.T) {
	runtime, err := New(&Catalogue{}, func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error) {
		return session.ToolResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var order []string
	process := &Process{Runtime: runtime, ctx: ctx, resources: []ownedResource{
		{name: "context", close: func() error { order = append(order, "context"); cancel(); return nil }},
		{name: "auth", close: func() error { order = append(order, "auth"); return nil }},
		{name: "vmcp", close: func() error {
			if _, _, attachErr := runtime.AttachSession(t.Context(), "during-close"); !errors.Is(attachErr, contract.ErrStateUnavailable) {
				t.Errorf("runtime was not closed before resources: %v", attachErr)
			}
			order = append(order, "vmcp")
			return nil
		}},
	}}
	if err := process.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := process.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := strings.Join(order, ","); got != "vmcp,auth,context" {
		t.Fatalf("Close resource order = %q", got)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("process context remained live after Close")
	}
	if _, _, err := runtime.AttachSession(t.Context(), "after-close"); !errors.Is(err, contract.ErrStateUnavailable) {
		t.Fatalf("runtime after Close = %v, want contract.ErrStateUnavailable", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func toolHiveOIDCIssuer(t *testing.T) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize",
				"token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/jwks",
				"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/jwks":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"keys":[]}`))
		default:
			http.NotFound(w, request)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestADR_0314_ToolHiveConstructionCarriesDCRConfig(t *testing.T) {
	profile := protectedToolHiveProfile("private")
	profile.OAuth.ClientID = ""
	profile.OAuth.ClientSecretEnv = ""
	profile.OAuth.DCRDiscoveryURL = "https://auth.example/.well-known/oauth-authorization-server"
	construction, err := compileToolHiveConstruction([]ToolHiveProfile{profile}, "https://broker.example")
	if err != nil {
		t.Fatalf("compile DCR profile: %v", err)
	}
	upstream := construction.upstreams[0].OAuth2Config
	if upstream == nil || upstream.ClientID != "" || upstream.DCRConfig == nil || upstream.DCRConfig.DiscoveryURL != profile.OAuth.DCRDiscoveryURL || upstream.AllowPrivateIPs || upstream.InsecureAllowHTTP {
		t.Fatalf("DCR upstream = %#v", upstream)
	}
}

func TestADR_0314_DCRRequiresExplicitOAuth2Upstream(t *testing.T) {
	profile := protectedToolHiveProfile("private")
	profile.OAuth.ClientID = ""
	profile.OAuth.ClientSecretEnv = ""
	profile.OAuth.AuthorizationEndpoint = ""
	profile.OAuth.TokenEndpoint = ""
	profile.OAuth.DCRDiscoveryURL = "https://auth.example/discovery"
	if _, err := compileToolHiveConstruction([]ToolHiveProfile{profile}, "https://broker.example"); err == nil {
		t.Fatal("DCR without explicit OAuth2 endpoints compiled successfully")
	}
}

func TestMcpBrokerDCRClient_Scenario2_RegistersAndEnrolls(t *testing.T) {
	fixture := newToolHiveDCRFixture(t, false)
	process := fixture.newProcess(t)
	t.Cleanup(func() { _ = process.Close() })

	if fixture.metadataRequests.Load() != 1 || fixture.registrationRequests.Load() != 1 {
		t.Fatalf("DCR requests: metadata=%d registrations=%d, want 1 each", fixture.metadataRequests.Load(), fixture.registrationRequests.Load())
	}
	fixture.assertRegistration(t)
	fixture.enrollAndCall(t, process, "dcr-first")
}

func TestMcpBrokerDCRClient_Scenario2_ReusesCachedRegistration(t *testing.T) {
	fixture := newToolHiveDCRFixture(t, false)
	redisServer := miniredis.RunT(t)
	newRedisClient := func() *redis.Client {
		return redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	}
	firstClient, secondClient := newRedisClient(), newRedisClient()

	newRedisProcess := func(client *redis.Client) *Process {
		t.Helper()
		config := fixture.config()
		config.AuthStorage = nil
		config.AuthRedisClient = client
		process, err := newToolHiveProcess(t.Context(), config, fixture.options())
		if err != nil {
			t.Fatalf("newToolHiveProcess: %v", err)
		}
		fixture.handlers.Store(process.Handlers)
		return process
	}

	first := newRedisProcess(firstClient)
	fixture.enrollAndCall(t, first, "dcr-first")
	if err := first.Close(); err != nil {
		t.Fatalf("close first Process: %v", err)
	}
	if fixture.metadataRequests.Load() != 1 || fixture.registrationRequests.Load() != 1 {
		t.Fatalf("DCR requests after first Process close: metadata=%d registrations=%d, want 1 each", fixture.metadataRequests.Load(), fixture.registrationRequests.Load())
	}

	second := newRedisProcess(secondClient)
	t.Cleanup(func() { _ = second.Close() })
	fixture.enrollAndCall(t, second, "dcr-second")
	if fixture.metadataRequests.Load() != 1 || fixture.registrationRequests.Load() != 1 {
		t.Fatalf("DCR requests after second Process: metadata=%d registrations=%d, want 1 each", fixture.metadataRequests.Load(), fixture.registrationRequests.Load())
	}
	if fixture.protectedCalls.Load() != 2 {
		t.Fatalf("protected calls = %d, want 2", fixture.protectedCalls.Load())
	}
}

func TestADR_0314_RegistrationFailureNeverFallsBackUnauthenticated(t *testing.T) {
	fixture := newToolHiveDCRFixture(t, true)
	process, err := newToolHiveProcess(t.Context(), fixture.config(), fixture.options())
	if err == nil {
		if process != nil {
			_ = process.Close()
		}
		t.Fatal("Process constructed after DCR registration rejection")
	}
	if process != nil {
		t.Fatalf("failed DCR construction returned Process %#v", process)
	}
	if fixture.registrationRequests.Load() != 1 {
		t.Fatalf("DCR registrations = %d, want 1", fixture.registrationRequests.Load())
	}
	if fixture.protectedCalls.Load() != 0 {
		t.Fatalf("protected upstream received %d unauthenticated fallback calls", fixture.protectedCalls.Load())
	}
}

type toolHiveDCRFixture struct {
	t                    *testing.T
	oauth                *httptest.Server
	protected            *httptest.Server
	gatewayMux           *http.ServeMux
	gateway              *httptest.Server
	storage              *nonClosingMemoryStorage
	metadataRequests     atomic.Int32
	registrationRequests atomic.Int32
	protectedCalls       atomic.Int32
	handlers             atomic.Value
	registration         struct {
		sync.Mutex
		redirectURIs  []string
		grantTypes    []string
		responseTypes []string
		authMethod    string
	}
	rejectRegistration bool
}

func newToolHiveDCRFixture(t *testing.T, rejectRegistration bool) *toolHiveDCRFixture {
	t.Helper()
	fixture := &toolHiveDCRFixture{t: t, rejectRegistration: rejectRegistration, storage: &nonClosingMemoryStorage{MemoryStorage: storage.NewMemoryStorage()}, gatewayMux: http.NewServeMux()}
	fixture.gatewayMux.HandleFunc("/", fixture.serveGateway)
	fixture.gateway = httptest.NewUnstartedServer(fixture.gatewayMux)
	fixture.gateway.StartTLS()
	t.Cleanup(fixture.gateway.Close)
	t.Cleanup(func() { _ = fixture.storage.MemoryStorage.Close() })

	fixture.oauth = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/.well-known/oauth-authorization-server":
			fixture.metadataRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": fixture.oauth.URL, "authorization_endpoint": fixture.oauth.URL + "/authorize", "token_endpoint": fixture.oauth.URL + "/token", "registration_endpoint": fixture.oauth.URL + "/register", "token_endpoint_auth_methods_supported": []string{"client_secret_basic"}})
		case "/register":
			fixture.registrationRequests.Add(1)
			var body struct {
				RedirectURIs  []string `json:"redirect_uris"`
				GrantTypes    []string `json:"grant_types"`
				ResponseTypes []string `json:"response_types"`
				AuthMethod    string   `json:"token_endpoint_auth_method"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			fixture.registration.Lock()
			fixture.registration.redirectURIs, fixture.registration.grantTypes, fixture.registration.responseTypes, fixture.registration.authMethod = body.RedirectURIs, body.GrantTypes, body.ResponseTypes, body.AuthMethod
			fixture.registration.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if fixture.rejectRegistration {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_client_metadata"}`))
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "dcr-client", "client_secret": "dcr-secret", "token_endpoint_auth_method": "client_secret_basic"})
		case "/authorize":
			redirect := request.URL.Query().Get("redirect_uri")
			state := request.URL.Query().Get("state")
			http.Redirect(w, request, redirect+"?"+url.Values{"code": {"upstream-code"}, "state": {state}}.Encode(), http.StatusFound)
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			clientID, secret, ok := request.BasicAuth()
			if !ok || clientID != "dcr-client" || secret != "dcr-secret" {
				http.Error(w, "invalid DCR client authentication", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"dcr-upstream-token","token_type":"Bearer","expires_in":3600}`))
		default:
			http.NotFound(w, request)
		}
	}))
	t.Cleanup(fixture.oauth.Close)

	upstream := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "protected", Version: "v1"}, nil)
	mcpsdk.AddTool(upstream, &mcpsdk.Tool{Name: "create"}, func(_ context.Context, _ *mcpsdk.CallToolRequest, input struct {
		Title string `json:"title"`
	}) (*mcpsdk.CallToolResult, any, error) {
		fixture.protectedCalls.Add(1)
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "created:" + input.Title}}}, nil, nil
	})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return upstream }, &mcpsdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	fixture.protected = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer dcr-upstream-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, request)
	}))
	t.Cleanup(fixture.protected.Close)
	return fixture
}

func (f *toolHiveDCRFixture) config() ToolHiveConfig {
	return ToolHiveConfig{CallbackURL: f.gateway.URL + "/callback", AuthStorage: f.storage, Profiles: []ToolHiveProfile{{Name: "private", URL: f.protected.URL, Auth: authOAuth, OAuth: &ToolHiveOAuth{AuthorizationEndpoint: f.oauth.URL + "/authorize", TokenEndpoint: f.oauth.URL + "/token", DCRDiscoveryURL: f.oauth.URL + "/.well-known/oauth-authorization-server", Scopes: []string{"read"}}, Static: []StaticTool{{Name: "create", Schema: json.RawMessage(`{"type":"object"}`)}}}}}
}

func (f *toolHiveDCRFixture) options() toolHiveProcessOptions {
	roots := x509.NewCertPool()
	roots.AddCert(f.gateway.Certificate())
	return toolHiveProcessOptions{runtimeOptions: []Option{WithOAuthLoopbackForTest(f.t, roots)}, brokerHTTPClient: f.gateway.Client(), allowLoopbackUpstreamsForTest: true}
}

func (f *toolHiveDCRFixture) newProcess(t *testing.T) *Process {
	t.Helper()
	process, err := newToolHiveProcess(t.Context(), f.config(), f.options())
	if err != nil {
		t.Fatalf("newToolHiveProcess: %v", err)
	}
	f.handlers.Store(process.Handlers)
	return process
}

func (f *toolHiveDCRFixture) serveGateway(w http.ResponseWriter, request *http.Request) {
	value := f.handlers.Load()
	if value == nil {
		http.NotFound(w, request)
		return
	}
	handlers := value.(HandlerBundle)
	var handler http.Handler
	switch request.URL.Path {
	case toolHiveBasePath + "/oauth/authorize":
		handler = handlers.Authorization
	case toolHiveBasePath + "/oauth/token":
		handler = handlers.Token
	case toolHiveBasePath + "/oauth/callback":
		handler = handlers.UpstreamCallback
	case toolHiveMCPPath:
		handler = handlers.VMCP
	case "/callback":
		handler = handlers.Callback
	}
	if handler == nil {
		http.NotFound(w, request)
		return
	}
	handler.ServeHTTP(w, request)
}

func (f *toolHiveDCRFixture) assertRegistration(t *testing.T) {
	t.Helper()
	f.registration.Lock()
	defer f.registration.Unlock()
	if !reflect.DeepEqual(f.registration.redirectURIs, []string{f.gateway.URL + toolHiveBasePath + "/oauth/callback"}) || !reflect.DeepEqual(f.registration.grantTypes, []string{"authorization_code", "refresh_token"}) || !reflect.DeepEqual(f.registration.responseTypes, []string{"code"}) || f.registration.authMethod != "client_secret_basic" {
		t.Fatalf("DCR registration = redirect=%v grants=%v response_types=%v auth_method=%q", f.registration.redirectURIs, f.registration.grantTypes, f.registration.responseTypes, f.registration.authMethod)
	}
}

func (f *toolHiveDCRFixture) enrollAndCall(t *testing.T, process *Process, id session.SessionID) {
	t.Helper()
	attached, _, err := process.Runtime.AttachSession(t.Context(), id)
	if err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	t.Cleanup(func() { _, _ = attached.Close(context.Background()) })
	enroller := attached.(contract.WorkspaceEnrollmentAttachment)
	presentation, err := enroller.BeginWorkspaceEnrollment(t.Context())
	if err != nil || !presentation.Valid() {
		t.Fatalf("BeginWorkspaceEnrollment = (%+v, %v)", presentation, err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, presentation.URL, nil)
	if err != nil {
		t.Fatalf("build authorization request: %v", err)
	}
	response, err := f.gateway.Client().Do(request)
	if err != nil {
		t.Fatalf("complete authorization: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d url=%s body=%s", response.StatusCode, response.Request.URL, body)
	}
	connected, err := enroller.ObserveWorkspaceEnrollment(t.Context(), presentation.Ref)
	if err != nil || connected.Status != contract.WorkspaceEnrollmentConnected {
		t.Fatalf("ObserveWorkspaceEnrollment = (%+v, %v)", connected, err)
	}
	wrapped := toolByName(t, attached.(*Attachment), "mcp__private__create")
	result, err := wrapped.Execute(t.Context(), session.NewToolCall(session.ToolCallID("call-"+string(id)), wrapped.Spec().Name, json.RawMessage(`{"title":"one"}`)), tool.Environment{})
	if err != nil || result.IsError || !strings.HasPrefix(result.Content, "created:one") {
		t.Fatalf("protected result = (%+v, %v)", result, err)
	}
}

type nonClosingMemoryStorage struct{ *storage.MemoryStorage }

func (*nonClosingMemoryStorage) Close() error { return nil }

func protectedToolHiveProfile(name string) ToolHiveProfile {
	return ToolHiveProfile{Name: name, URL: "https://" + strings.ToLower(strings.Trim(name, "_")) + ".example/mcp", Auth: authOAuth, OAuth: &ToolHiveOAuth{
		AuthorizationEndpoint: "https://issuer.example/authorize",
		TokenEndpoint:         "https://issuer.example/token",
		ClientID:              name + "-client",
		ClientSecretEnv:       "MECATL_TEST_CLIENT_SECRET",
		Scopes:                []string{"openid"},
	}}
}

func toolHiveDiscoveryServer(t *testing.T, toolName string, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	upstream := mcpsdk.NewServer(&mcpsdk.Implementation{Name: toolName, Version: "test"}, nil)
	mcpsdk.AddTool(upstream, &mcpsdk.Tool{Name: toolName}, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, struct{}, error) {
		return &mcpsdk.CallToolResult{}, struct{}{}, nil
	})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return upstream }, &mcpsdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

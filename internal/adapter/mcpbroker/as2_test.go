package mcpbroker

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ory/fosite"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
	"github.com/stacklok/mecatl/internal/testutil/oauthbrowser"
)

const (
	as2TestCallbackURL = "https://broker.example/callback"
	as2TestIssuer      = "https://broker.example" + toolHiveBasePath
	as2TestResource    = as2TestIssuer + "/mcp"
	clientAssertionTyp = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	idJAGType          = "oauth-id-jag+jwt"
	mcpInitializeBody  = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"as2-test","version":"1.0"}}}`
)

// as2BrokerClient is the identity broker's client key, its assertion signer,
// and the broker client configuration carrying its public key set.
type as2BrokerClient struct {
	key    *ecdsa.PrivateKey
	signer *identitybroker.ClientAssertionSigner
	config *ToolHiveBrokerClient
}

func newAS2BrokerClient(t *testing.T) as2BrokerClient {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := identitybroker.NewClientAssertionSigner(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	keySet, err := json.Marshal(signer.JWKS())
	if err != nil {
		t.Fatal(err)
	}
	return as2BrokerClient{key: key, signer: signer, config: &ToolHiveBrokerClient{ID: identitybroker.BrokerClientID, KeySet: keySet}}
}

// assertion is a fresh client assertion from the broker's own signer.
func (c as2BrokerClient) assertion(t *testing.T, audience string) string {
	t.Helper()
	token, err := c.signer.ClientAssertion(audience, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return token.Value()
}

// signAssertion signs claims as a client assertion with key under the broker
// key's kid, for assertions the broker's signer never makes.
func (c as2BrokerClient) signAssertion(t *testing.T, key *ecdsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["typ"] = "client-authentication+jwt"
	token.Header["kid"] = c.signer.JWKS().Keys[0].KeyID
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

// postAS2Token posts form to AS-2's token endpoint in process and returns the
// status and decoded JSON body.
func postAS2Token(t *testing.T, as2 *AS2, form url.Values) (int, map[string]any) {
	t.Helper()
	client := as2.InProcessClient()
	client.Timeout = 5 * time.Second
	resp, err := client.PostForm(as2.TokenEndpoint, form)
	if err != nil {
		t.Fatalf("AS-2 token request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &body)
	return resp.StatusCode, body
}

// as2TestAct is the act claim of the ID-JAGs the tests mint.
var as2TestAct = map[string]any{"sub": "spiffe://agents.mecatl.invalid/agent/reviewer"}

// mintAS2IDJAG is an ID-JAG for Alice from the harness's AS-1, for the broker
// client at as2.
func mintAS2IDJAG(h *identityharness.Harness, as2 *AS2) string {
	return h.AS1.MintAccessToken(identityharness.AccessTokenSpec{
		User: identityharness.Alice, ClientID: identitybroker.BrokerClientID, Audience: as2.Issuer,
		Type: idJAGType, Lifetime: 5 * time.Minute,
		Claims: map[string]any{"resource": as2.Resource, "act": as2TestAct},
	})
}

// unverifiedClaims decodes a JWT's claims without verifying it.
func unverifiedClaims(t *testing.T, token string) jwt.MapClaims {
	t.Helper()
	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, claims); err != nil {
		t.Fatalf("parse token: %v", err)
	}
	return claims
}

func staticProtectedProfile(name string) ToolHiveProfile {
	profile := protectedToolHiveProfile(name)
	profile.Static = []StaticTool{{Name: "echo", Description: "echo", Schema: json.RawMessage(`{"type":"object"}`)}}
	return profile
}

func TestEmbeddedAuthServerIsUnchangedWithoutBrokerClient(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	spy := newRegisterClientSpy()
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
		CallbackURL: as2TestCallbackURL, Profiles: []ToolHiveProfile{staticProtectedProfile("private")}, AuthStorage: spy,
	})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	if process.AS2 != nil {
		t.Fatalf("AS-2 = %+v without a broker client", process.AS2)
	}
	if got := []string(spy.client.GetAudience()); !reflect.DeepEqual(got, []string{as2TestIssuer}) {
		t.Fatalf("login client audience = %v, want the issuer", got)
	}
	if process.protectedTarget.resource != as2TestIssuer {
		t.Fatalf("login resource = %q, want the issuer", process.protectedTarget.resource)
	}
	if _, err := spy.GetClient(t.Context(), identitybroker.BrokerClientID); err == nil {
		t.Fatal("the broker client is registered without a broker client configured")
	}
}

func TestAS2NeedsAProtectedUpstream(t *testing.T) {
	var requests atomic.Int32
	anonymous := toolHiveDiscoveryServer(t, "get_status", &requests)
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
		Profiles:     []ToolHiveProfile{{Name: "status", URL: anonymous.URL, Auth: authNone}},
		BrokerClient: newAS2BrokerClient(t).config,
	})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	if process.AS2 != nil {
		t.Fatalf("AS-2 = %+v without a protected upstream", process.AS2)
	}
}

func TestAS2RegistersTheBrokerClientWithTheVMCPResource(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	// The broker client's key set file lives in TMPDIR only during construction.
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	spy := newRegisterClientSpy()
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
		CallbackURL: as2TestCallbackURL, Profiles: []ToolHiveProfile{staticProtectedProfile("private")}, AuthStorage: spy,
		BrokerClient: newAS2BrokerClient(t).config,
	})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	if left, err := os.ReadDir(tmp); err != nil || len(left) != 0 {
		t.Fatalf("TMPDIR after construction = %v, %v; want empty", left, err)
	}
	as2 := process.AS2
	if as2 == nil || as2.Issuer != as2TestIssuer || as2.TokenEndpoint != as2TestIssuer+"/oauth/token" || as2.Resource != as2TestResource {
		t.Fatalf("AS-2 = %+v", as2)
	}
	if got := []string(spy.client.GetAudience()); !reflect.DeepEqual(got, []string{as2TestResource}) {
		t.Fatalf("login client audience = %v, want the vMCP resource", got)
	}
	if process.protectedTarget.resource != as2TestResource {
		t.Fatalf("login resource = %q, want the vMCP resource", process.protectedTarget.resource)
	}
	registered, err := spy.GetClient(t.Context(), identitybroker.BrokerClientID)
	if err != nil {
		t.Fatalf("broker client not registered: %v", err)
	}
	if got, want := []string(registered.GetGrantTypes()), []string{oauthproto.GrantTypeTokenExchange, oauthproto.GrantTypeJWTBearer}; !reflect.DeepEqual(got, want) {
		t.Errorf("broker client grant types = %v, want %v", got, want)
	}
	if got := []string(registered.GetScopes()); !reflect.DeepEqual(got, []string{"openid"}) {
		t.Errorf("broker client scopes = %v, want [openid]", got)
	}
	if got := []string(registered.GetAudience()); !reflect.DeepEqual(got, []string{as2TestResource}) {
		t.Errorf("broker client audiences = %v, want the vMCP resource", got)
	}
	if oidc, ok := registered.(fosite.OpenIDConnectClient); !ok || oidc.GetTokenEndpointAuthMethod() != oauthproto.TokenEndpointAuthMethodPrivateKeyJWT {
		t.Errorf("broker client does not authenticate with private_key_jwt")
	}
	// The in-process client serves the token endpoint and nothing else.
	client := as2.InProcessClient()
	for _, other := range []string{as2TestIssuer + "/.well-known/jwks.json", "https://other.example/v1/mcp/broker/oauth/token", as2TestResource} {
		if resp, err := client.Get(other); err == nil {
			_ = resp.Body.Close()
			t.Errorf("in-process client served %s", other)
		}
	}
}

func TestAS2RefusesAnInvalidBrokerClientBeforeConstruction(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	valid := newAS2BrokerClient(t).config
	for name, client := range map[string]*ToolHiveBrokerClient{
		"empty ID":       {KeySet: valid.KeySet},
		"no key set":     {ID: valid.ID},
		"not a JWK Set":  {ID: valid.ID, KeySet: []byte(`[]`)},
		"empty JWK Set":  {ID: valid.ID, KeySet: []byte(`{"keys":[]}`)},
		"malformed JSON": {ID: valid.ID, KeySet: []byte(`{"keys":`)},
	} {
		t.Run(name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			var requests atomic.Int32
			anonymous := toolHiveDiscoveryServer(t, "get_status", &requests)
			spy := newRegisterClientSpy()
			process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
				CallbackURL: as2TestCallbackURL, AuthStorage: spy, BrokerClient: client,
				Profiles: []ToolHiveProfile{staticProtectedProfile("private"), {Name: "status", URL: anonymous.URL, Auth: authNone}},
			})
			if err == nil {
				_ = process.Close()
			}
			if !errors.Is(err, ErrInvalidCatalogue) {
				t.Fatalf("NewToolHiveProcess = %v, want ErrInvalidCatalogue", err)
			}
			if requests.Load() != 0 || spy.registered.Load() {
				t.Errorf("construction ran before refusing: %d discovery requests, storage written %v", requests.Load(), spy.registered.Load())
			}
			if left, err := os.ReadDir(tmp); err != nil || len(left) != 0 {
				t.Errorf("TMPDIR = %v, %v; want empty", left, err)
			}
		})
	}
}

// closeSpy is a registerClientSpy that records its Close.
type closeSpy struct {
	*registerClientSpy
	closed atomic.Bool
}

func (s *closeSpy) Close() error {
	s.closed.Store(true)
	return s.registerClientSpy.Close()
}

func TestAS2RemovesTheKeySetFileWhenConstructionFails(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	valid := newAS2BrokerClient(t).config
	for name, test := range map[string]struct {
		client *ToolHiveBrokerClient
		trust  *as2Trust
	}{
		"key set ToolHive rejects": {client: &ToolHiveBrokerClient{ID: valid.ID, KeySet: []byte(`{"keys":[{"kty":"none"}]}`)}},
		"invalid ID-JAG trust":     {client: valid, trust: &as2Trust{}},
	} {
		t.Run(name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			spy := &closeSpy{registerClientSpy: newRegisterClientSpy()}
			process, err := newToolHiveProcess(t.Context(), ToolHiveConfig{
				CallbackURL: as2TestCallbackURL, Profiles: []ToolHiveProfile{staticProtectedProfile("private")}, AuthStorage: spy,
				BrokerClient: test.client,
			}, toolHiveProcessOptions{as2TrustForTest: test.trust})
			if err == nil {
				_ = process.Close()
				t.Fatal("NewToolHiveProcess succeeded, want a construction failure")
			}
			if !strings.Contains(err.Error(), "create embedded auth server") {
				t.Fatalf("NewToolHiveProcess = %v, want an embedded auth server failure", err)
			}
			if left, err := os.ReadDir(tmp); err != nil || len(left) != 0 {
				t.Errorf("TMPDIR after a failed construction = %v, %v; want empty", left, err)
			}
			if !spy.closed.Load() {
				t.Error("the configured auth storage was not closed after a failed construction")
			}
		})
	}
}

var errRegisterForTest = errors.New("register client refused for test")

// localStoreSpy wraps an auth store construction creates itself, recording its
// Close and optionally refusing RegisterClient.
type localStoreSpy struct {
	storage.Storage
	refuseRegister bool
	closed         atomic.Bool
}

func (s *localStoreSpy) RegisterClient(ctx context.Context, client fosite.Client) error {
	if s.refuseRegister {
		return errRegisterForTest
	}
	return s.Storage.RegisterClient(ctx, client)
}

func (s *localStoreSpy) Close() error {
	s.closed.Store(true)
	return s.Storage.Close()
}

func TestToolHiveClosesALocallyCreatedAuthStoreOnEarlyFailure(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	valid := newAS2BrokerClient(t).config
	for name, test := range map[string]struct {
		tmpdirNotADirectory bool
		refuseRegister      bool
		want                string
	}{
		"key set file write fails": {tmpdirNotADirectory: true, want: "mcpbroker: create embedded auth server: jwksfile:"},
		"register client fails":    {refuseRegister: true, want: errRegisterForTest.Error()},
	} {
		t.Run(name, func(t *testing.T) {
			tmp := t.TempDir()
			if test.tmpdirNotADirectory {
				tmp = filepath.Join(tmp, "file")
				if err := os.WriteFile(tmp, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("TMPDIR", tmp)
			var stores []*localStoreSpy
			process, err := newToolHiveProcess(t.Context(), ToolHiveConfig{
				CallbackURL: as2TestCallbackURL, Profiles: []ToolHiveProfile{staticProtectedProfile("private")}, BrokerClient: valid,
			}, toolHiveProcessOptions{localAuthStoreForTest: func(store storage.Storage) storage.Storage {
				spy := &localStoreSpy{Storage: store, refuseRegister: test.refuseRegister}
				stores = append(stores, spy)
				return spy
			}})
			if err == nil {
				_ = process.Close()
				t.Fatal("NewToolHiveProcess succeeded, want a construction failure")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewToolHiveProcess = %v, want %q", err, test.want)
			}
			for i, store := range stores {
				if !store.closed.Load() {
					t.Errorf("locally created auth store %d was not closed after a failed construction", i)
				}
			}
			if !test.tmpdirNotADirectory {
				if len(stores) != 1 {
					t.Fatalf("created %d local auth stores, want 1", len(stores))
				}
				if left, err := os.ReadDir(tmp); err != nil || len(left) != 0 {
					t.Errorf("TMPDIR after a failed construction = %v, %v; want empty", left, err)
				}
			}
		})
	}
}

// TestAS2GatewayLoginUsesTheVMCPResource logs a user in at the gateway with
// AS-2 set up: the token carries the vMCP resource and the vMCP accepts it,
// a token for the issuer is refused, and the broker client cannot exchange
// the login token.
func TestAS2GatewayLoginUsesTheVMCPResource(t *testing.T) {
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
	upstreamMCP := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "protected", Version: "v1"}, nil)
	mcpsdk.AddTool(upstreamMCP, &mcpsdk.Tool{Name: "create", Description: "create"}, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "created"}}}, nil, nil
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
	// The broker's calls to the vMCP carry the gateway login token.
	var bearerMu sync.Mutex
	var bearer string
	brokerClient := gateway.Client()
	base := brokerClient.Transport
	brokerClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == toolHiveMCPPath {
			bearerMu.Lock()
			bearer, _ = strings.CutPrefix(request.Header.Get("Authorization"), "Bearer ")
			bearerMu.Unlock()
		}
		return base.RoundTrip(request)
	})

	broker := newAS2BrokerClient(t)
	profile := ToolHiveProfile{Name: "private", URL: protectedMCP.URL, Auth: authOAuth, OAuth: &ToolHiveOAuth{
		AuthorizationEndpoint: upstreamOAuth.URL + "/authorize", TokenEndpoint: upstreamOAuth.URL + "/token",
		ClientID: "public-client", Scopes: []string{"read"},
	}, Static: []StaticTool{{Name: "create", Description: "create", Schema: json.RawMessage(`{"type":"object"}`)}}}
	process, err := newToolHiveProcess(t.Context(), ToolHiveConfig{
		CallbackURL: gateway.URL + "/callback", Profiles: []ToolHiveProfile{profile}, BrokerClient: broker.config,
	}, toolHiveProcessOptions{
		runtimeOptions: []Option{WithOAuthLoopbackForTest(t, roots)}, brokerHTTPClient: brokerClient,
	})
	if err != nil {
		t.Fatalf("newToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	admitStaticForGenericAuthorizationTest(t, process)
	if err := process.Handlers.Mount(mux, "/callback"); err != nil {
		t.Fatalf("mount handlers: %v", err)
	}
	as2 := process.AS2
	if as2 == nil || as2.Resource != as2.Issuer+"/mcp" {
		t.Fatalf("AS-2 = %+v", as2)
	}

	attached, _, err := process.Runtime.AttachSession(t.Context(), "as2-login")
	if err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	t.Cleanup(func() { _, _ = attached.Close(context.Background()) })
	wrapped := toolByName(t, attached.(*Attachment), "mcp__private__create")
	call := session.NewToolCall("call-1", wrapped.Spec().Name, json.RawMessage(`{}`))
	authorization, required, err := wrapped.(tool.AuthorizationRequester).RequestAuthorization(t.Context(), call)
	if err != nil || !required {
		t.Fatalf("RequestAuthorization = (%+v, %v, %v)", authorization, required, err)
	}
	presentation, err := attached.PresentAuthorization(t.Context(), authorization)
	if err != nil {
		t.Fatalf("PresentAuthorization: %v", err)
	}
	if got := mustParseURL(t, presentation).Query().Get("resource"); got != as2.Resource {
		t.Fatalf("authorization resource = %q, want %q", got, as2.Resource)
	}
	flowCtx, cancelFlow := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelFlow()
	request, err := http.NewRequestWithContext(flowCtx, http.MethodGet, presentation, nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := gateway.Client()
	browser.Timeout = 5 * time.Second
	response, err := oauthbrowser.Approve(browser, request)
	if err != nil {
		t.Fatalf("complete authorization: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d", response.StatusCode)
	}
	result, err := wrapped.Execute(t.Context(), call, tool.Environment{})
	if err != nil || result.IsError {
		t.Fatalf("protected call through the vMCP = (%+v, %v)", result, err)
	}
	bearerMu.Lock()
	login := bearer
	bearerMu.Unlock()
	if login == "" {
		t.Fatal("the broker sent no gateway login token to the vMCP")
	}
	claims := unverifiedClaims(t, login)
	if aud, _ := claims.GetAudience(); !reflect.DeepEqual([]string(aud), []string{as2.Resource}) {
		t.Fatalf("login token aud = %v, want [%s]", aud, as2.Resource)
	}

	// vmcpStatus is the status of an MCP initialize at the vMCP with token. A
	// session it opens is ended, so the vMCP lets its backend go.
	vmcpStatus := func(token string) int {
		t.Helper()
		send := func(method, body, sessionID string) *http.Response {
			req, err := http.NewRequestWithContext(t.Context(), method, gateway.URL+toolHiveMCPPath, strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			if sessionID != "" {
				req.Header.Set("Mcp-Session-Id", sessionID)
			}
			resp, err := gateway.Client().Do(req)
			if err != nil {
				t.Fatalf("vMCP %s: %v", method, err)
			}
			_ = resp.Body.Close()
			return resp
		}
		resp := send(http.MethodPost, mcpInitializeBody, "")
		if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
			send(http.MethodDelete, "", id)
		}
		return resp.StatusCode
	}
	if status := vmcpStatus(login); status != http.StatusOK {
		t.Fatalf("vMCP initialize with the login token: status %d", status)
	}
	// A token issued before AS-2 was set up, for the issuer, is refused.
	signing, err := process.auth.KeyProvider().SigningKey(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	forged := jwt.MapClaims{}
	for name, value := range claims {
		forged[name] = value
	}
	forged["aud"] = []string{as2.Issuer}
	forgedToken := jwt.NewWithClaims(jwt.GetSigningMethod(signing.Algorithm), forged)
	forgedToken.Header["kid"] = signing.KeyID
	issuerToken, err := forgedToken.SignedString(signing.Key)
	if err != nil {
		t.Fatalf("sign issuer-audience token: %v", err)
	}
	if status := vmcpStatus(issuerToken); status != http.StatusUnauthorized {
		t.Fatalf("vMCP initialize with an issuer-audience token: status %d, want 401", status)
	}

	// The broker client cannot exchange the user's login token.
	status, body := postAS2Token(t, as2, url.Values{
		"grant_type":            {oauthproto.GrantTypeTokenExchange},
		"subject_token":         {login},
		"subject_token_type":    {"urn:ietf:params:oauth:token-type:access_token"},
		"resource":              {as2.Resource},
		"client_assertion_type": {clientAssertionTyp},
		"client_assertion":      {broker.assertion(t, as2.Issuer)},
	})
	if status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("broker client exchanging a login token: status %d, body %v; want 400 invalid_grant", status, body)
	}
}

// TestAS2RedeemsAHarnessIDJAG configures AS-2 to trust the harness's AS-1
// through the test-only seam and redeems ID-JAGs it signs, then checks the
// broker client's assertions.
func TestAS2RedeemsAHarnessIDJAG(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	h := identityharness.New(t, identityharness.Options{})
	broker := newAS2BrokerClient(t)
	process, err := newToolHiveProcess(t.Context(), ToolHiveConfig{
		CallbackURL: as2TestCallbackURL, Profiles: []ToolHiveProfile{staticProtectedProfile("private")}, BrokerClient: broker.config,
	}, toolHiveProcessOptions{as2TrustForTest: &as2Trust{
		issuer: h.AS1.Issuer(), jwksURL: h.AS1.Issuer() + "/jwks", caFile: h.WriteCACert(t, t.TempDir()),
		subjects:        []string{h.AS1.Subject(identityharness.Alice), h.AS1.Subject(identityharness.Bob)},
		maxAssertionAge: 10 * time.Minute,
	}})
	if err != nil {
		t.Fatalf("newToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	as2 := process.AS2
	if as2 == nil {
		t.Fatal("AS-2 is not set up")
	}
	idJAG := func() string { return mintAS2IDJAG(h, as2) }
	redeem := func(assertion, clientAssertion string) (int, map[string]any) {
		return postAS2Token(t, as2, url.Values{
			"grant_type":            {oauthproto.GrantTypeJWTBearer},
			"assertion":             {assertion},
			"resource":              {as2.Resource},
			"client_assertion_type": {clientAssertionTyp},
			"client_assertion":      {clientAssertion},
		})
	}

	first := idJAG()
	status, body := redeem(first, broker.assertion(t, as2.Issuer))
	if status != http.StatusOK {
		t.Fatalf("ID-JAG redemption: status %d, body %v", status, body)
	}
	if _, ok := body["refresh_token"]; ok {
		t.Fatal("the gateway token came with a refresh token")
	}
	gatewayToken, _ := body["access_token"].(string)
	claims := unverifiedClaims(t, gatewayToken)
	if claims["client_id"] != identitybroker.BrokerClientID {
		t.Errorf("gateway token client_id = %v, want %s", claims["client_id"], identitybroker.BrokerClientID)
	}
	if !reflect.DeepEqual(claims["act"], as2TestAct) {
		t.Errorf("gateway token act = %v, want %v", claims["act"], as2TestAct)
	}
	if sub, _ := claims.GetSubject(); sub != h.AS1.Issuer()+"#"+h.AS1.Subject(identityharness.Alice) {
		t.Errorf("gateway token sub = %q, want issuer#sub", sub)
	}
	if aud, _ := claims.GetAudience(); !slices.Equal([]string(aud), []string{as2.Resource}) {
		t.Errorf("gateway token aud = %v, want [%s]", aud, as2.Resource)
	}

	// The vMCP's incoming authentication accepts the gateway token.
	mux := http.NewServeMux()
	if err := process.Handlers.Mount(mux, "/callback"); err != nil {
		t.Fatalf("mount handlers: %v", err)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, as2TestResource, strings.NewReader(mcpInitializeBody))
	req.Header.Set("Authorization", "Bearer "+gatewayToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("vMCP initialize with the gateway token: status %d, body %s", recorder.Code, recorder.Body)
	}

	// An ID-JAG redeems once.
	if status, body := redeem(first, broker.assertion(t, as2.Issuer)); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("second redemption of one ID-JAG: status %d, body %v", status, body)
	}

	// AS-2 accepts the broker's assertions for its issuer or its token
	// endpoint, and refuses every other one with 401 invalid_client. Accepting
	// the token endpoint as aud is ToolHive v0.61.0 behaviour that the design's
	// step (5) does not want (stacklok/toolhive#7039); the broker signs
	// assertions for the issuer only, so this documents current behaviour, not
	// policy.
	now := time.Now()
	claimsAt := func(aud string, iat, exp time.Time) jwt.MapClaims {
		return jwt.MapClaims{"iss": identitybroker.BrokerClientID, "sub": identitybroker.BrokerClientID, "aud": aud,
			"iat": iat.Unix(), "exp": exp.Unix(), "jti": rand.Text()}
	}
	if status, body := redeem(idJAG(), broker.signAssertion(t, broker.key, claimsAt(as2.TokenEndpoint, now, now.Add(time.Minute)))); status != http.StatusOK {
		t.Fatalf("assertion for the token endpoint: status %d, body %v", status, body)
	}
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	reused := broker.assertion(t, as2.Issuer)
	if status, body := redeem(idJAG(), reused); status != http.StatusOK {
		t.Fatalf("first use of an assertion: status %d, body %v", status, body)
	}
	for name, assertion := range map[string]string{
		"another audience": broker.assertion(t, "https://other.example/v1/mcp/broker"),
		"expired":          broker.signAssertion(t, broker.key, claimsAt(as2.Issuer, now.Add(-3*time.Minute), now.Add(-2*time.Minute))),
		"exp too far":      broker.signAssertion(t, broker.key, claimsAt(as2.Issuer, now, now.Add(6*time.Minute))),
		"reused jti":       reused,
		"unregistered key": broker.signAssertion(t, other, claimsAt(as2.Issuer, now, now.Add(time.Minute))),
	} {
		if status, body := redeem(idJAG(), assertion); status != http.StatusUnauthorized || body["error"] != "invalid_client" {
			t.Errorf("%s: status %d, body %v; want 401 invalid_client", name, status, body)
		}
	}
}

func TestAS2TrustsNoIssuerWithoutTheTestSeam(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	h := identityharness.New(t, identityharness.Options{})
	broker := newAS2BrokerClient(t)
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
		CallbackURL: as2TestCallbackURL, Profiles: []ToolHiveProfile{staticProtectedProfile("private")}, BrokerClient: broker.config,
	})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	status, body := postAS2Token(t, process.AS2, url.Values{
		"grant_type":            {oauthproto.GrantTypeJWTBearer},
		"assertion":             {mintAS2IDJAG(h, process.AS2)},
		"resource":              {process.AS2.Resource},
		"client_assertion_type": {clientAssertionTyp},
		"client_assertion":      {broker.assertion(t, process.AS2.Issuer)},
	})
	// With no JWT-bearer handler, no token endpoint handler takes the grant
	// and fosite refuses the request as invalid_request.
	if status != http.StatusBadRequest || body["error"] != "invalid_request" {
		t.Fatalf("production AS-2 redeeming an ID-JAG: status %d, body %v; want 400 invalid_request", status, body)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

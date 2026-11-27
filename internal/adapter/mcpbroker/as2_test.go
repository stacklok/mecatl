package mcpbroker

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
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
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker/internal/as2trust"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server/as1servertest"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/brokerconfig/brokerconfigtest"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/inprocesshttp"
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
	key := brokerconfigtest.NewKey(t)
	signer, err := identitybroker.NewClientAssertionSigner(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	keySetFile := brokerconfigtest.WriteKeySet(t, filepath.Join(t.TempDir(), "broker-client-jwks.json"), key)
	return as2BrokerClient{key: key, signer: signer, config: &ToolHiveBrokerClient{ID: identitybroker.BrokerClientID, KeySetFile: keySetFile}}
}

// writeKeySetFile writes keySet, raw, to a file of t.
func writeKeySetFile(t *testing.T, keySet []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "broker-client-jwks.json")
	if err := os.WriteFile(path, keySet, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// keySetFileUnchanged fails t unless the file at path still holds want: the
// key set file is the operator's, such as a mounted Secret, which AS-2 only
// reads.
func keySetFileUnchanged(t *testing.T, path string, want []byte, when string) {
	t.Helper()
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("key set file %s: %v, changed = %v", when, err, !bytes.Equal(got, want))
	}
}

// readFile is the contents of the file at path.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
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
	status, body, err := tryPostAS2Token(as2, form)
	if err != nil {
		t.Fatalf("AS-2 token request: %v", err)
	}
	return status, body
}

// tryPostAS2Token is postAS2Token returning the request's error, for
// goroutines other than the test's.
func tryPostAS2Token(as2 *AS2, form url.Values) (int, map[string]any, error) {
	client := inprocesshttp.NewClient(as2.Handler(), func(*url.URL) error { return nil })
	client.Timeout = 5 * time.Second
	resp, err := client.PostForm(as2.TokenEndpoint, form)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &body)
	return resp.StatusCode, body, nil
}

// refusalReason is the reason code the identity broker reads from an AS-2
// refusal body: the known code that starts error_description, "unknown" for
// any other text, and empty when there is none.
func refusalReason(body map[string]any) string {
	code, _ := body["error"].(string)
	description, _ := body["error_description"].(string)
	return identitybroker.NewServerRefusal(0, code, description).Reason
}

// trustedAS1 is the real AS-1, with the harness IdP as its upstream, and
// Alice's and Bob's access tokens from it. The tests forge their assertions
// from Alice's token and sign them with AS-1's key.
type trustedAS1 struct {
	a          *as1servertest.AS1
	alice, bob identitybroker.Token
}

// startTrustedAS1 starts the real AS-1 and signs Alice and Bob in.
func startTrustedAS1(t *testing.T) trustedAS1 {
	t.Helper()
	a := as1servertest.Start(t, identityharness.New(t, identityharness.Options{}), nil)
	return trustedAS1{
		a:     a,
		alice: a.SignIn(t, as1servertest.SignIn{User: identityharness.Alice}),
		bob:   a.SignIn(t, as1servertest.SignIn{User: identityharness.Bob}),
	}
}

// act is the act claim of the ID-JAGs the tests mint, in the design's shape
// (credentials-and-lifecycle.md, step (7)): the instance's ag_ identifier,
// and AS-1, which minted act, as its issuer.
func (s trustedAS1) act() map[string]any {
	return map[string]any{"iss": s.a.Issuer(), "sub": "ag_7hq2x9"}
}

// jwt is Alice's access token turned into a JWT AS-1 signs for the broker
// client: aud audience, typ typ (none when empty), a fresh jti, five
// minutes to live, and an ID-JAG's resource, act, and call entry
// (testCallDetails) for as2 unless claims replaces them.
func (s trustedAS1) jwt(t *testing.T, as2 *AS2, audience, typ string, claims map[string]any) string {
	t.Helper()
	if claims == nil {
		claims = map[string]any{"resource": as2.Resource, "act": s.act(), "authorization_details": testCallDetails()}
	}
	now := time.Now()
	return s.a.Forge(t, s.alice, as1servertest.Forgery{
		Claims: func(c jwt.MapClaims) {
			c["aud"], c["client_id"], c["jti"] = audience, identitybroker.BrokerClientID, rand.Text()
			c["iat"], c["exp"] = now.Unix(), now.Add(5*time.Minute).Unix()
			maps.Copy(c, claims)
		},
		Header: func(h map[string]any) {
			if typ == "" {
				delete(h, "typ")
				return
			}
			h["typ"] = typ
		},
	}).Value()
}

// idJAG is an ID-JAG for Alice from AS-1, for the broker client at as2,
// carrying the call entry testCallDetails, as every ID-JAG the broker asks
// for does.
func (s trustedAS1) idJAG(t *testing.T, as2 *AS2) string {
	t.Helper()
	return s.jwt(t, as2, as2.Issuer, idJAGType, nil)
}

// unboundIDJAG is an ID-JAG for Alice from AS-1, for the broker client at
// as2, with no call entry.
func (s trustedAS1) unboundIDJAG(t *testing.T, as2 *AS2) string {
	t.Helper()
	return s.jwt(t, as2, as2.Issuer, idJAGType, map[string]any{"resource": as2.Resource, "act": s.act()})
}

// testCallDetails is the authorization_details claim of the tests' ID-JAGs:
// one tool-call entry for the private backend's echo tool
// (credentials-and-lifecycle.md, step (8)).
func testCallDetails() []any {
	return []any{map[string]any{
		"type": identitybroker.CallEntryType, "server": "private", "tool": "private.echo",
		"arguments_sha256": strings.Repeat("ab", 32), "operation_id": "op_test",
	}}
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
	spy := newRegisterClientSpy()
	client := newAS2BrokerClient(t).config
	keySet := readFile(t, client.KeySetFile)
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
		CallbackURL: as2TestCallbackURL, Profiles: []ToolHiveProfile{staticProtectedProfile("private")}, AuthStorage: spy,
		BrokerClient: client,
	})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() {
		_ = process.Close()
		keySetFileUnchanged(t, client.KeySetFile, keySet, "after Close")
	})
	keySetFileUnchanged(t, client.KeySetFile, keySet, "after construction")
	as2 := process.AS2
	if as2 == nil || as2.Issuer != as2TestIssuer || as2.TokenEndpoint != as2TestIssuer+"/oauth/token" || as2.Resource != as2TestResource {
		t.Fatalf("AS-2 = %+v", as2)
	}
	// AS2Endpoints names the same AS-2 before construction.
	if issuer, token, resource, err := AS2Endpoints(as2TestCallbackURL); err != nil || issuer != as2.Issuer || token != as2.TokenEndpoint || resource != as2.Resource {
		t.Fatalf("AS2Endpoints = %q, %q, %q, %v; want AS-2's %q, %q, and %q", issuer, token, resource, err, as2.Issuer, as2.TokenEndpoint, as2.Resource)
	}
	if _, _, _, err := AS2Endpoints("http://broker.example/callback"); !errors.Is(err, ErrInvalidCatalogue) {
		t.Fatalf("AS2Endpoints for an http callback = %v, want ErrInvalidCatalogue", err)
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
	// AS-2's handler serves a POST to the token endpoint and none of the
	// embedded server's other routes, which the mux serves.
	for _, request := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/v1/mcp/broker/.well-known/jwks.json", http.StatusNotFound},
		{http.MethodGet, "/v1/mcp/broker/.well-known/openid-configuration", http.StatusNotFound},
		{http.MethodGet, "/v1/mcp/broker/oauth/authorize", http.StatusNotFound},
		{http.MethodPost, "/v1/mcp/broker/oauth/consent", http.StatusNotFound},
		{http.MethodPost, "/v1/mcp/broker/oauth/callback", http.StatusNotFound},
		{http.MethodGet, "/v1/mcp/broker/oauth/token", http.StatusMethodNotAllowed},
		{http.MethodPost, "/v1/mcp/broker/oauth/token", http.StatusBadRequest},
	} {
		recorder := httptest.NewRecorder()
		as2.Handler().ServeHTTP(recorder, httptest.NewRequest(request.method, "https://broker.example"+request.path, nil))
		if recorder.Code != request.want {
			t.Errorf("AS-2 handler %s %s = %d, want %d", request.method, request.path, recorder.Code, request.want)
		}
		mounted := httptest.NewRecorder()
		process.Handlers.JWKS.ServeHTTP(mounted, httptest.NewRequest(request.method, "https://broker.example"+request.path, nil))
		if request.path != "/v1/mcp/broker/oauth/token" && mounted.Code == http.StatusNotFound {
			t.Errorf("the embedded server does not serve %s, so the AS-2 handler's 404 proves nothing", request.path)
		}
	}
}

func TestAS2RefusesAnInvalidBrokerClientBeforeConstruction(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	valid := newAS2BrokerClient(t).config
	for name, test := range map[string]struct {
		client *ToolHiveBrokerClient
		want   string
	}{
		"empty ID":              {client: &ToolHiveBrokerClient{KeySetFile: valid.KeySetFile}, want: "needs an ID"},
		"no key set":            {client: &ToolHiveBrokerClient{ID: valid.ID}, want: "needs an ID and a JWK Set file"},
		"relative key set path": {client: &ToolHiveBrokerClient{ID: valid.ID, KeySetFile: "jwks.json"}, want: "is not an absolute path"},
		"absent key set file":   {client: &ToolHiveBrokerClient{ID: valid.ID, KeySetFile: filepath.Join(t.TempDir(), "absent.json")}, want: "no such file"},
		"not a JWK Set":         {client: &ToolHiveBrokerClient{ID: valid.ID, KeySetFile: writeKeySetFile(t, []byte(`[]`))}, want: "is not a JSON JWK Set"},
		"empty JWK Set":         {client: &ToolHiveBrokerClient{ID: valid.ID, KeySetFile: writeKeySetFile(t, []byte(`{"keys":[]}`))}, want: "holds no key"},
		"malformed JSON":        {client: &ToolHiveBrokerClient{ID: valid.ID, KeySetFile: writeKeySetFile(t, []byte(`{"keys":`))}, want: "is not a JSON JWK Set"},
	} {
		client := test.client
		t.Run(name, func(t *testing.T) {
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
			if !errors.Is(err, ErrInvalidCatalogue) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewToolHiveProcess = %v, want ErrInvalidCatalogue containing %q", err, test.want)
			}
			// The file-system cause stays matchable, to tell a missing mount
			// from a malformed key set.
			if name == "absent key set file" && !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("NewToolHiveProcess = %v, want it to match fs.ErrNotExist", err)
			}
			if requests.Load() != 0 || spy.registered.Load() {
				t.Errorf("construction ran before refusing: %d discovery requests, storage written %v", requests.Load(), spy.registered.Load())
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

func TestAS2ClosesTheAuthStorageWhenConstructionFails(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	valid := newAS2BrokerClient(t).config
	for name, test := range map[string]struct {
		client *ToolHiveBrokerClient
		trust  AS2Trust
	}{
		"key set ToolHive rejects": {client: &ToolHiveBrokerClient{ID: valid.ID, KeySetFile: writeKeySetFile(t, []byte(`{"keys":[{"kty":"none"}]}`))}},
		"invalid ID-JAG trust":     {client: valid, trust: NewAS2Trust(as2trust.Trust{})},
	} {
		t.Run(name, func(t *testing.T) {
			keySet := readFile(t, test.client.KeySetFile)
			spy := &closeSpy{registerClientSpy: newRegisterClientSpy()}
			process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
				CallbackURL: as2TestCallbackURL, Profiles: []ToolHiveProfile{staticProtectedProfile("private")}, AuthStorage: spy,
				BrokerClient: test.client, AS2Trust: test.trust,
			})
			if err == nil {
				_ = process.Close()
				t.Fatal("NewToolHiveProcess succeeded, want a construction failure")
			}
			if !strings.Contains(err.Error(), "create embedded auth server") {
				t.Fatalf("NewToolHiveProcess = %v, want an embedded auth server failure", err)
			}
			if !spy.closed.Load() {
				t.Error("the configured auth storage was not closed after a failed construction")
			}
			keySetFileUnchanged(t, test.client.KeySetFile, keySet, "after a failed construction")
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
		refuseRegister bool
		want           string
	}{
		"register client fails": {refuseRegister: true, want: errRegisterForTest.Error()},
	} {
		t.Run(name, func(t *testing.T) {
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
			if len(stores) != 1 {
				t.Fatalf("created %d local auth stores, want 1", len(stores))
			}
		})
	}
}

// TestToolHiveProcessCloseClosesTheAuthStore: ToolHive borrows the auth store,
// so Process.Close closes it, once.
func TestToolHiveProcessCloseClosesTheAuthStore(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	spy := &closeSpy{registerClientSpy: newRegisterClientSpy()}
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
		CallbackURL: as2TestCallbackURL, Profiles: []ToolHiveProfile{staticProtectedProfile("private")}, AuthStorage: spy,
	})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	if spy.closed.Load() {
		t.Fatal("the auth store was closed before the process")
	}
	// Closing the memory store a second time would panic.
	for range 2 {
		_ = process.Close()
	}
	if !spy.closed.Load() {
		t.Fatal("Process.Close did not close the auth store")
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
		t.Fatalf("NewToolHiveProcess: %v", err)
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

// newTrustingAS2 is AS-2 configured to trust the real AS-1, for Alice and
// Bob, through the test-only seam, ToolHiveConfig.AS2Trust. It is this
// package's counterpart of mcpbrokertest.StartAS2, which these tests cannot
// call: mcpbrokertest imports this package, and the tests here use its
// unexported helpers. Both build the trust with as2trust.Trust.WithDefaults.
func newTrustingAS2(t *testing.T) (trustedAS1, as2BrokerClient, *Process) {
	t.Helper()
	as1 := startTrustedAS1(t)
	broker := newAS2BrokerClient(t)
	return as1, broker, startTrustingAS2(t, as1, broker.config)
}

// startTrustingAS2 sets AS-2 up trusting as1, with client as its broker
// client.
func startTrustingAS2(t *testing.T, as1 trustedAS1, client *ToolHiveBrokerClient) *Process {
	t.Helper()
	return startTrustingAS2With(t, as1, client, nil)
}

// startTrustingAS2With is startTrustingAS2 with edit, when set, changing the
// configuration first.
func startTrustingAS2With(t *testing.T, as1 trustedAS1, client *ToolHiveBrokerClient, edit func(*ToolHiveConfig)) *Process {
	t.Helper()
	return startTrustingAS2WithOptions(t, as1, client, edit, toolHiveProcessOptions{})
}

// trustingAS2Config is the configuration of AS-2 trusting as1, for Alice
// and Bob, with client as its broker client. It sets an environment
// variable, so t must not be parallel.
func trustingAS2Config(t *testing.T, as1 trustedAS1, client *ToolHiveBrokerClient) ToolHiveConfig {
	t.Helper()
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	return ToolHiveConfig{
		CallbackURL: as2TestCallbackURL, Profiles: []ToolHiveProfile{staticProtectedProfile("private")}, BrokerClient: client,
		AS2Trust: NewAS2Trust(as2trust.Trust{
			Issuer: as1.a.Issuer(), JWKSURL: as1.a.JWKSURL(), CAFile: as1.a.Harness.WriteCACert(t, t.TempDir()),
			AllowPrivateAddresses: true,
			Subjects:              []string{as1servertest.Subject(t, as1.alice), as1servertest.Subject(t, as1.bob)},
		}.WithDefaults()),
	}
}

// startTrustingAS2WithOptions is startTrustingAS2With built with options.
func startTrustingAS2WithOptions(t *testing.T, as1 trustedAS1, client *ToolHiveBrokerClient, edit func(*ToolHiveConfig), options toolHiveProcessOptions) *Process {
	t.Helper()
	config := trustingAS2Config(t, as1, client)
	if edit != nil {
		edit(&config)
	}
	process, err := newToolHiveProcess(t.Context(), config, options)
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	if process.AS2 == nil {
		t.Fatal("AS-2 is not set up")
	}
	return process
}

// TestAS2BrokerClientKeySetRotation: with the old and new broker client keys
// in the key set file, AS-2 accepts client assertions signed by either;
// rebuilt from a file holding only the new key, it accepts only the new
// key's.
func TestAS2BrokerClientKeySetRotation(t *testing.T) {
	as1 := startTrustedAS1(t)
	oldKey, newKey := brokerconfigtest.NewKey(t), brokerconfigtest.NewKey(t)
	start := func(keys ...*ecdsa.PrivateKey) *AS2 {
		file := brokerconfigtest.WriteKeySet(t, filepath.Join(t.TempDir(), "broker-client-jwks.json"), keys...)
		return startTrustingAS2(t, as1, &ToolHiveBrokerClient{ID: identitybroker.BrokerClientID, KeySetFile: file}).AS2
	}
	// redeem redeems a fresh ID-JAG at as2, authenticated with an assertion
	// key signs.
	redeem := func(as2 *AS2, key *ecdsa.PrivateKey) (int, map[string]any) {
		t.Helper()
		signer, err := identitybroker.NewClientAssertionSigner(key, 0)
		if err != nil {
			t.Fatal(err)
		}
		assertion, err := signer.ClientAssertion(as2.Issuer, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return postAS2Token(t, as2, url.Values{
			"grant_type": {oauthproto.GrantTypeJWTBearer}, "assertion": {as1.idJAG(t, as2)}, "resource": {as2.Resource},
			"client_assertion_type": {clientAssertionTyp}, "client_assertion": {assertion.Value()},
		})
	}

	during := start(oldKey, newKey)
	for name, key := range map[string]*ecdsa.PrivateKey{"old": oldKey, "new": newKey} {
		if status, body := redeem(during, key); status != http.StatusOK {
			t.Errorf("during the rotation, the %s key's assertion: status %d, body %v; want 200", name, status, body)
		}
	}
	after := start(newKey)
	if status, body := redeem(after, newKey); status != http.StatusOK {
		t.Errorf("after the rotation, the new key's assertion: status %d, body %v; want 200", status, body)
	}
	if status, body := redeem(after, oldKey); status != http.StatusUnauthorized || body["error"] != "invalid_client" {
		t.Errorf("after the rotation, the old key's assertion: status %d, body %v; want 401 invalid_client", status, body)
	}
}

// TestAS2RedeemsAnAS1IDJAG configures AS-2 to trust the real AS-1 through
// the test-only seam and redeems ID-JAGs signed with AS-1's key, then checks
// the broker client's assertions.
func TestAS2RedeemsAnAS1IDJAG(t *testing.T) {
	as1, broker, process := newTrustingAS2(t)
	as2 := process.AS2
	idJAG := func() string { return as1.idJAG(t, as2) }
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
	parsed, _, err := jwt.NewParser().ParseUnverified(gatewayToken, jwt.MapClaims{})
	if err != nil {
		t.Fatalf("parse the gateway token: %v", err)
	}
	claims, header := parsed.Claims.(jwt.MapClaims), parsed.Header
	if header["typ"] != "at+jwt" {
		t.Errorf("gateway token typ = %v, want at+jwt", header["typ"])
	}
	if claims["client_id"] != identitybroker.BrokerClientID {
		t.Errorf("gateway token client_id = %v, want %s", claims["client_id"], identitybroker.BrokerClientID)
	}
	if want := as1.act(); !reflect.DeepEqual(claims["act"], want) {
		t.Errorf("gateway token act = %v, want %v", claims["act"], want)
	}
	if sub, _ := claims.GetSubject(); sub != as1.a.Issuer()+"#"+as1servertest.Subject(t, as1.alice) {
		t.Errorf("gateway token sub = %q, want issuer#sub", sub)
	}
	if aud, _ := claims.GetAudience(); !slices.Equal([]string(aud), []string{as2.Resource}) {
		t.Errorf("gateway token aud = %v, want [%s]", aud, as2.Resource)
	}
	// AS-2 copies the ID-JAG's call entry into the gateway token and returns
	// it in the token response (step (8)).
	if want := testCallDetails(); !reflect.DeepEqual(claims["authorization_details"], want) || !reflect.DeepEqual(body["authorization_details"], want) {
		t.Errorf("gateway token authorization_details = %v, response's %v; want %v in both", claims["authorization_details"], body["authorization_details"], want)
	}
	// It lives for the broker client's gateway-token lifetime.
	if iat, exp := claims["iat"].(float64), claims["exp"].(float64); time.Duration(exp-iat)*time.Second != DefaultGatewayTokenLifetime {
		t.Errorf("gateway token lives %v, want %v", time.Duration(exp-iat)*time.Second, DefaultGatewayTokenLifetime)
	}

	// The vMCP's incoming authentication accepts the gateway token.
	mux := http.NewServeMux()
	if err := process.Handlers.Mount(mux, "/callback"); err != nil {
		t.Fatalf("mount handlers: %v", err)
	}
	vmcpInitialize := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, as2TestResource, strings.NewReader(mcpInitializeBody))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, req)
		return recorder
	}
	if recorder := vmcpInitialize(gatewayToken); recorder.Code != http.StatusOK {
		t.Fatalf("vMCP initialize with the gateway token: status %d, body %s", recorder.Code, recorder.Body)
	}

	// The vMCP rejects a gateway token from another issuer, for another
	// audience, with the wrong typ, or expired (implementation.md,
	// "Credential verification"), as invalid_token, which the broker retries
	// once with a new gateway token (credentials-and-lifecycle.md, step (8)).
	// Each variant is the gateway token with one change, signed again; the
	// unchanged copy, signed the same way, is accepted, so each rejection is
	// the change's.
	signing, err := process.auth.KeyProvider().SigningKey(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// resign is the gateway token after edit, signed by key under kid with
	// method.
	resign := func(method jwt.SigningMethod, key any, kid string, edit func(jwt.MapClaims, map[string]any)) string {
		t.Helper()
		token := jwt.NewWithClaims(method, maps.Clone(claims))
		token.Header = maps.Clone(header)
		token.Header["alg"], token.Header["kid"] = method.Alg(), kid
		edit(token.Claims.(jwt.MapClaims), token.Header)
		signed, err := token.SignedString(key)
		if err != nil {
			t.Fatalf("sign a gateway token variant: %v", err)
		}
		return signed
	}
	asAS2 := func(edit func(jwt.MapClaims, map[string]any)) string {
		return resign(jwt.GetSigningMethod(signing.Algorithm), signing.Key, signing.KeyID, edit)
	}
	if recorder := vmcpInitialize(asAS2(func(jwt.MapClaims, map[string]any) {})); recorder.Code != http.StatusOK {
		t.Fatalf("vMCP initialize with the gateway token signed again: status %d, body %s", recorder.Code, recorder.Body)
	}
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherIssuer := func(c jwt.MapClaims, _ map[string]any) { c["iss"] = "https://other-as2.example" }
	for name, token := range map[string]string{
		"another issuer, signed by another key":           resign(jwt.SigningMethodES256, otherKey, "other-as2-key", otherIssuer),
		"another issuer, signed by AS-2's key":            asAS2(otherIssuer),
		"AS-2's issuer and key ID, signed by another key": resign(jwt.SigningMethodES256, otherKey, signing.KeyID, func(jwt.MapClaims, map[string]any) {}),
		"another audience":                                asAS2(func(c jwt.MapClaims, _ map[string]any) { c["aud"] = []string{"https://other.example/mcp"} }),
		"typ ID-JAG":                                      asAS2(func(_ jwt.MapClaims, h map[string]any) { h["typ"] = idJAGType }),
		"expired": asAS2(func(c jwt.MapClaims, _ map[string]any) {
			c["iat"], c["exp"] = time.Now().Add(-10*time.Minute).Unix(), time.Now().Add(-5*time.Minute).Unix()
		}),
	} {
		recorder := vmcpInitialize(token)
		if challenge := recorder.Header().Get("WWW-Authenticate"); recorder.Code != http.StatusUnauthorized || !strings.Contains(challenge, `error="invalid_token"`) {
			t.Errorf("vMCP initialize with a gateway token, %s: status %d, WWW-Authenticate %q; want 401 invalid_token", name, recorder.Code, challenge)
		}
	}
	// The vMCP accepts these, which the design refuses: see the shortcut on
	// the vMCP's incoming authentication in newToolHiveProcess. Flip them to
	// 401 when Mecatl turns on stacklok/toolhive#7345's check.
	for name, edit := range map[string]func(jwt.MapClaims, map[string]any){
		"typ JWT":    func(_ jwt.MapClaims, h map[string]any) { h["typ"] = "JWT" },
		"typ absent": func(_ jwt.MapClaims, h map[string]any) { delete(h, "typ") },
	} {
		if recorder := vmcpInitialize(asAS2(edit)); recorder.Code != http.StatusOK {
			t.Errorf("vMCP initialize with a gateway token, %s: status %d; if the vMCP now refuses it, make this a 401 and drop the shortcut", name, recorder.Code)
		}
	}

	// An ID-JAG redeems once.
	if status, body := redeem(first, broker.assertion(t, as2.Issuer)); status != http.StatusBadRequest || body["error"] != "invalid_grant" || refusalReason(body) != "invalid_assertion" {
		t.Fatalf("second redemption of one ID-JAG: status %d, body %v", status, body)
	}

	// Without a resource parameter, AS-2 uses the ID-JAG's resource claim
	// (credentials-and-lifecycle.md, step (8)).
	status, body = as2Redemption(t, as2, broker, idJAG(), nil, true)
	if status != http.StatusOK {
		t.Fatalf("ID-JAG redemption without a resource: status %d, body %v", status, body)
	}
	defaulted, _ := body["access_token"].(string)
	if aud, _ := unverifiedClaims(t, defaulted).GetAudience(); !slices.Equal([]string(aud), []string{as2.Resource}) {
		t.Errorf("gateway token aud without a resource = %v, want [%s]", aud, as2.Resource)
	}

	// AS-2 accepts the broker's assertions only when aud is its issuer alone
	// (design step (5), draft-ietf-oauth-rfc7523bis), and refuses every other one with
	// 401 invalid_client, the token endpoint and an array that adds another
	// value included.
	now := time.Now()
	claimsAt := func(aud any, iat, exp time.Time) jwt.MapClaims {
		return jwt.MapClaims{"iss": identitybroker.BrokerClientID, "sub": identitybroker.BrokerClientID, "aud": aud,
			"iat": iat.Unix(), "exp": exp.Unix(), "jti": rand.Text()}
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
		"another audience":   broker.assertion(t, "https://other.example/v1/mcp/broker"),
		"the token endpoint": broker.signAssertion(t, broker.key, claimsAt(as2.TokenEndpoint, now, now.Add(time.Minute))),
		"the issuer and another audience": broker.signAssertion(t, broker.key,
			claimsAt([]string{as2.Issuer, "https://other.example/v1/mcp/broker"}, now, now.Add(time.Minute))),
		"expired": broker.signAssertion(t, broker.key, claimsAt(as2.Issuer, now.Add(-3*time.Minute), now.Add(-2*time.Minute))),
		// ToolHive's bound is 5 minutes ahead plus a 60-second skew.
		"exp too far":      broker.signAssertion(t, broker.key, claimsAt(as2.Issuer, now, now.Add(10*time.Minute))),
		"reused jti":       reused,
		"unregistered key": broker.signAssertion(t, other, claimsAt(as2.Issuer, now, now.Add(time.Minute))),
	} {
		if status, body := redeem(idJAG(), assertion); status != http.StatusUnauthorized || body["error"] != "invalid_client" {
			t.Errorf("%s: status %d, body %v; want 401 invalid_client", name, status, body)
		}
	}
}

// as2Redemption posts a JWT-bearer redemption of assertion to as2. A nil
// resource leaves the resource parameter out; clientAuth adds the broker
// client's assertion.
func as2Redemption(t *testing.T, as2 *AS2, broker as2BrokerClient, assertion string, resource *string, clientAuth bool) (int, map[string]any) {
	t.Helper()
	form := url.Values{"grant_type": {oauthproto.GrantTypeJWTBearer}, "assertion": {assertion}}
	if resource != nil {
		form.Set("resource", *resource)
	}
	if clientAuth {
		form.Set("client_assertion_type", clientAssertionTyp)
		form.Set("client_assertion", broker.assertion(t, as2.Issuer))
	}
	return postAS2Token(t, as2, form)
}

// TestAS2RefusesRedemptionsOutsideItsConfiguration shows that AS-2 refuses
// what its configuration leaves out, not only that it redeems what it allows:
// a resource, requested or named by the ID-JAG's claim, other than the
// vMCP's, an ID-JAG addressed to anything but AS-2's
// issuer, and an AS-1 access token presented as an assertion. These are the
// checks that keep an AS-1 access token from being redeemed once #2533 trusts
// AS-1 in production, and that a ToolHive bump must not loosen.
func TestAS2RefusesRedemptionsOutsideItsConfiguration(t *testing.T) {
	as1, broker, process := newTrustingAS2(t)
	as2 := process.AS2
	idJAG := func(audience string) string { return as1.jwt(t, as2, audience, idJAGType, nil) }
	resource := func(value string) *string { return &value }

	if status, body := as2Redemption(t, as2, broker, idJAG(as2.Issuer), resource(as2.Resource), true); status != http.StatusOK {
		t.Fatalf("valid ID-JAG: status %d, body %v", status, body)
	}
	accessToken := as1.jwt(t, as2, as2.Issuer, "at+jwt", nil)
	otherClaim := as1.jwt(t, as2, as2.Issuer, idJAGType, map[string]any{"resource": "https://other.example/mcp", "act": as1.act()})
	twoAudiences := as1.jwt(t, as2, as2.Issuer, idJAGType, map[string]any{
		"aud": []string{as2.Issuer, "https://other.example/v1/mcp/broker"}, "resource": as2.Resource, "act": as1.act(),
	})
	// withClaims is an ID-JAG for AS-2 with claims changed.
	withClaims := func(claims map[string]any) string {
		all := map[string]any{"resource": as2.Resource, "act": as1.act()}
		maps.Copy(all, claims)
		return as1.jwt(t, as2, as2.Issuer, idJAGType, all)
	}
	now := time.Now()
	unbound := withClaims(map[string]any{"sub": "not-a-bound-subject"})
	// Plain RFC 7523 assertions from AS-1, for a bound subject: AS-2 trusts
	// AS-1 for ID-JAGs only, so it refuses them, and ToolHive's plain path,
	// which would otherwise redeem them without client authentication, never
	// sees them (credentials-and-lifecycle.md, step (8)).
	plainJWT := func() string { return as1.jwt(t, as2, as2.Issuer, "JWT", nil) }
	plainNoTyp := func() string { return as1.jwt(t, as2, as2.Issuer, "", nil) }
	// ToolHive's hints for the refusals below, as of v0.69.0. The generic
	// hint is the one ToolHive gives every assertion it cannot validate.
	const (
		hintResource    = "The requested resource is invalid"
		hintUngoverned  = "resource claim names a resource this server does not govern"
		hintUnverified  = "The ID-JAG assertion is invalid or could not be verified"
		hintOneAudience = "aud must name exactly one audience"
		hintLifetime    = "exceeds the configured maximum age"
		hintOtherClient = "was not issued to the requesting client"
		hintTyp         = "JOSE typ header is not supported"
		hintPlain       = "issuer is not enabled for this grant"
	)
	// otherSignature is an ID-JAG that is valid but for its signature: AS-1's
	// issuer and key ID, signed with another key.
	otherSignature := func() string {
		valid, _, err := jwt.NewParser().ParseUnverified(idJAG(as2.Issuer), jwt.MapClaims{})
		if err != nil {
			t.Fatal(err)
		}
		otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		forged := jwt.NewWithClaims(jwt.SigningMethodES256, valid.Claims)
		forged.Header = maps.Clone(valid.Header)
		forged.Header["alg"] = jwt.SigningMethodES256.Alg()
		signed, err := forged.SignedString(otherKey)
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
	for _, test := range []struct {
		name       string
		assertion  string
		resource   *string
		clientAuth bool
		wantError  string
		// wantReason is the reason code AS-2 puts first in error_description
		// (step (8)'s failure rows), as the broker reads it: "unknown" for
		// invalid_target, which carries ToolHive's prose and no reason.
		wantReason string
		// wantHint is text error_description must contain: many refusals
		// share invalid_assertion, and ToolHive tells their causes apart
		// only in the hint after it.
		wantHint string
	}{
		{"resource is AS-2's issuer", idJAG(as2.Issuer), resource(as2.Issuer), true, "invalid_target", "unknown", hintResource},
		{"resource is another server", idJAG(as2.Issuer), resource("https://other.example/mcp"), true, "invalid_target", "unknown", hintResource},
		// An ID-JAG whose resource claim names a resource AS-2 does not
		// govern is refused, whether or not the request names the vMCP's.
		// ToolHive checks the claim itself before comparing it with the
		// request or with policy (ID-JAG draft, section 4.4.1), so step (8)'s
		// invalid_target and resource_denied rows apply only to a claim AS-2
		// governs.
		{"resource left out, claim names another server", otherClaim, nil, true, "invalid_grant", "invalid_assertion", hintUngoverned},
		{"resource is the vMCP, claim names another server", otherClaim, resource(as2.Resource), true, "invalid_grant", "invalid_assertion", hintUngoverned},
		{"ID-JAG for the vMCP resource", idJAG(as2.Resource), resource(as2.Resource), true, "invalid_grant", "invalid_assertion", hintUnverified},
		{"ID-JAG for the token endpoint", idJAG(as2.TokenEndpoint), resource(as2.Resource), true, "invalid_grant", "invalid_assertion", hintUnverified},
		{"ID-JAG for AS-2's issuer and another audience", twoAudiences, resource(as2.Resource), true, "invalid_grant", "invalid_assertion", hintOneAudience},
		{"ID-JAG for a subject AS-2 cannot map", unbound, resource(as2.Resource), true, "access_denied", "subject_unknown", ""},
		{"expired ID-JAG", withClaims(map[string]any{"iat": now.Add(-3 * time.Minute).Unix(), "exp": now.Add(-2 * time.Minute).Unix()}), resource(as2.Resource), true, "invalid_grant", "invalid_assertion", hintUnverified},
		// AS-2 limits an ID-JAG's lifetime, exp - iat, not its age: it does
		// not compare iat with the current time.
		{"ID-JAG with a lifetime over the maximum", withClaims(map[string]any{"iat": now.Unix(), "exp": now.Add(as2trust.DefaultMaxAssertionAge + time.Minute).Unix()}), resource(as2.Resource), true, "invalid_grant", "invalid_assertion", hintLifetime},
		{"ID-JAG for another client", withClaims(map[string]any{"client_id": "other-client"}), resource(as2.Resource), true, "invalid_grant", "invalid_assertion", hintOtherClient},
		{"ID-JAG naming an issuer AS-2 does not trust", withClaims(map[string]any{"iss": "https://untrusted-as1.example"}), resource(as2.Resource), true, "invalid_grant", "invalid_assertion", hintUnverified},
		{"ID-JAG with AS-1's issuer and key ID, signed by another key", otherSignature(), resource(as2.Resource), true, "invalid_grant", "invalid_assertion", hintUnverified},
		{"AS-1 access token", accessToken, resource(as2.Resource), true, "invalid_grant", "invalid_assertion", hintTyp},
		{"AS-1 access token without client authentication", accessToken, resource(as2.Resource), false, "invalid_grant", "invalid_assertion", hintTyp},
		{"plain JWT, typ JWT", plainJWT(), resource(as2.Resource), true, "invalid_grant", "invalid_assertion", hintPlain},
		{"plain JWT, typ JWT, without client authentication", plainJWT(), resource(as2.Resource), false, "invalid_grant", "invalid_assertion", hintPlain},
		{"plain JWT, no typ", plainNoTyp(), resource(as2.Resource), true, "invalid_grant", "invalid_assertion", hintPlain},
		{"plain JWT, no typ, without client authentication", plainNoTyp(), resource(as2.Resource), false, "invalid_grant", "invalid_assertion", hintPlain},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, body := as2Redemption(t, as2, broker, test.assertion, test.resource, test.clientAuth)
			if _, issued := body["access_token"]; issued || status != http.StatusBadRequest || body["error"] != test.wantError {
				t.Fatalf("status %d, body %v; want 400 %s and no token", status, body, test.wantError)
			}
			if got := refusalReason(body); got != test.wantReason {
				t.Fatalf("reason %q, body %v; want %q", got, body, test.wantReason)
			}
			if description, _ := body["error_description"].(string); !strings.Contains(description, test.wantHint) {
				t.Fatalf("error_description %q; want it to contain %q", description, test.wantHint)
			}
		})
	}

	// A valid ID-JAG redeemed without client authentication is refused.
	status, body := as2Redemption(t, as2, broker, idJAG(as2.Issuer), resource(as2.Resource), false)
	if _, issued := body["access_token"]; issued || status != http.StatusUnauthorized || body["error"] != "invalid_client" {
		t.Fatalf("ID-JAG without client authentication: status %d, body %v; want 401 invalid_client and no token", status, body)
	}
}

// TestAS2RedeemsAnIDJAGOnceUnderConcurrentRedemptions sends one ID-JAG to
// AS-2 several times at once: exactly one redemption gets a gateway token,
// and every other one is refused as a replay (step (8) check 5).
func TestAS2RedeemsAnIDJAGOnceUnderConcurrentRedemptions(t *testing.T) {
	as1, broker, process := newTrustingAS2(t)
	as2 := process.AS2
	idJAG := as1.idJAG(t, as2)
	const redemptions = 8
	forms := make([]url.Values, redemptions)
	for i := range forms {
		forms[i] = url.Values{
			"grant_type": {oauthproto.GrantTypeJWTBearer}, "assertion": {idJAG}, "resource": {as2.Resource},
			"client_assertion_type": {clientAssertionTyp}, "client_assertion": {broker.assertion(t, as2.Issuer)},
		}
	}
	type answer struct {
		status int
		body   map[string]any
		err    error
	}
	answers := make([]answer, redemptions)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range forms {
		wg.Go(func() {
			<-start
			answers[i].status, answers[i].body, answers[i].err = tryPostAS2Token(as2, forms[i])
		})
	}
	close(start)
	wg.Wait()

	issued := 0
	for _, a := range answers {
		switch {
		case a.err != nil:
			t.Errorf("concurrent redemption: %v", a.err)
		case a.status == http.StatusOK:
			if token, _ := a.body["access_token"].(string); token == "" {
				t.Errorf("concurrent redemption: status 200 with no gateway token, body %v", a.body)
				continue
			}
			issued++
		default:
			description, _ := a.body["error_description"].(string)
			if _, token := a.body["access_token"]; token || a.status != http.StatusBadRequest || a.body["error"] != "invalid_grant" ||
				refusalReason(a.body) != "invalid_assertion" || !strings.Contains(description, "has already been used") {
				t.Errorf("concurrent redemption: status %d, body %v; want 200 with a token, or 400 invalid_grant refusing a replay and no token", a.status, a.body)
			}
		}
	}
	if issued != 1 {
		t.Fatalf("%d of %d concurrent redemptions of one ID-JAG got a gateway token, want 1", issued, redemptions)
	}
}

// TestAS2FetchesAS1KeysFromAPrivateAddressOnlyWhenAllowed: a trust whose key
// set URL names a loopback address is refused when AS-2 is built unless the
// trust allows private addresses. The other AS-2 tests allow it for their
// loopback AS-1.
func TestAS2FetchesAS1KeysFromAPrivateAddressOnlyWhenAllowed(t *testing.T) {
	config := trustingAS2Config(t, startTrustedAS1(t), newAS2BrokerClient(t).config)
	config.AS2Trust.t.AllowPrivateAddresses = false
	process, err := NewToolHiveProcess(t.Context(), config)
	if err == nil {
		_ = process.Close()
		t.Fatal("AS-2 trusting a loopback AS-1 without private addresses allowed was built")
	}
	if !strings.Contains(err.Error(), "private or loopback") {
		t.Fatalf("NewToolHiveProcess: %v; want a refusal of the loopback key set URL", err)
	}
}

// errConsumeForTest is the replay store's failure in
// TestAS2AnswersAReplayStoreOutageWithServerError.
var errConsumeForTest = errors.New("replay store unavailable for test")

// failingReplayStore is the in-memory auth store whose replay store for
// assertions, which AS-2 consults at each redemption, is unavailable while
// failing is set. It embeds the concrete store so ToolHive still finds the
// store's other interfaces.
type failingReplayStore struct {
	*storage.MemoryStorage
	failing *atomic.Bool
}

func (s failingReplayStore) ConsumeAssertionJWT(ctx context.Context, purpose, issuer, jti string, exp time.Time) error {
	if s.failing.Load() {
		return errConsumeForTest
	}
	return s.MemoryStorage.ConsumeAssertionJWT(ctx, purpose, issuer, jti, exp)
}

// TestAS2AnswersAReplayStoreOutageWithServerError: when AS-2 cannot record a
// redeemed ID-JAG, it refuses the redemption as server_error, which the
// broker treats as an unavailable server, never as a refused grant or a
// reason to prompt, and a gateway token issued before the outage still works
// at the vMCP (implementation.md, "Credential verification").
func TestAS2AnswersAReplayStoreOutageWithServerError(t *testing.T) {
	as1 := startTrustedAS1(t)
	broker := newAS2BrokerClient(t)
	var failing atomic.Bool
	process := startTrustingAS2WithOptions(t, as1, broker.config, nil, toolHiveProcessOptions{
		localAuthStoreForTest: func(store storage.Storage) storage.Storage {
			memory, ok := store.(*storage.MemoryStorage)
			if !ok {
				t.Fatalf("auth store %T, want the in-memory store", store)
			}
			return failingReplayStore{MemoryStorage: memory, failing: &failing}
		},
	})
	as2 := process.AS2
	vmcp := as2.Resource
	status, body := as2Redemption(t, as2, broker, as1.idJAG(t, as2), &vmcp, true)
	if status != http.StatusOK {
		t.Fatalf("redemption before the outage: status %d, body %v", status, body)
	}
	issued, _ := body["access_token"].(string)

	failing.Store(true)
	status, body = as2Redemption(t, as2, broker, as1.idJAG(t, as2), &vmcp, true)
	if _, issued := body["access_token"]; issued || status != http.StatusInternalServerError || body["error"] != "server_error" {
		t.Fatalf("redemption during a replay store outage: status %d, body %v; want 500 server_error and no token", status, body)
	}

	mux := http.NewServeMux()
	if err := process.Handlers.Mount(mux, "/callback"); err != nil {
		t.Fatalf("mount handlers: %v", err)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, as2TestResource, strings.NewReader(mcpInitializeBody))
	req.Header.Set("Authorization", "Bearer "+issued)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("vMCP initialize during the outage with a gateway token issued before it: status %d, body %s", recorder.Code, recorder.Body)
	}
}

// TestAS2LetsTheBrokerClientExchangeItsGatewayToken records the shortcut on
// as2DelegateClient: the broker client exchanges a gateway token without a
// call entry that AS-2 issued to it, which the design refuses. #2533 flips
// that case to a refusal once AS-2 marks gateway tokens
// (stacklok/toolhive#7247). A call-bound gateway token, the only kind the
// broker obtains, is refused already (stacklok/toolhive#7282): exchanging it
// would mint a fresh token with a new jti and get around its single use.
func TestAS2LetsTheBrokerClientExchangeItsGatewayToken(t *testing.T) {
	as1, broker, process := newTrustingAS2(t)
	as2 := process.AS2
	vmcp := as2.Resource
	exchange := func(idJAG string) (int, map[string]any) {
		t.Helper()
		status, body := as2Redemption(t, as2, broker, idJAG, &vmcp, true)
		if status != http.StatusOK {
			t.Fatalf("ID-JAG redemption: status %d, body %v", status, body)
		}
		gatewayToken, _ := body["access_token"].(string)
		return postAS2Token(t, as2, url.Values{
			"grant_type":            {oauthproto.GrantTypeTokenExchange},
			"subject_token":         {gatewayToken},
			"subject_token_type":    {"urn:ietf:params:oauth:token-type:access_token"},
			"resource":              {as2.Resource},
			"client_assertion_type": {clientAssertionTyp},
			"client_assertion":      {broker.assertion(t, as2.Issuer)},
		})
	}
	if status, body := exchange(as1.idJAG(t, as2)); status != http.StatusBadRequest || body["access_token"] != nil {
		t.Fatalf("broker client exchanging its call-bound gateway token: status %d, body %v; want a refusal", status, body)
	}
	if status, body := exchange(as1.unboundIDJAG(t, as2)); status != http.StatusOK {
		t.Fatalf("broker client exchanging its gateway token without a call entry: status %d, body %v; if AS-2 now refuses it, make this a refusal and update #2533", status, body)
	}
}

func TestAS2TrustsNoIssuerWithoutTheTestSeam(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	as1 := startTrustedAS1(t)
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
		"assertion":             {as1.idJAG(t, process.AS2)},
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

// TestAS2BindsGatewayTokensToTheirCall: AS-2 issues the broker client's
// gateway tokens for the configured lifetime, and the vMCP accepts a
// call-bound one and refuses one of the broker client without a call entry
// (credentials-and-lifecycle.md, steps (8) and (9)).
func TestAS2BindsGatewayTokensToTheirCall(t *testing.T) {
	as1 := startTrustedAS1(t)
	broker := newAS2BrokerClient(t)
	process := startTrustingAS2With(t, as1, broker.config, func(c *ToolHiveConfig) { c.GatewayTokenLifetime = 7 * time.Second })
	as2 := process.AS2
	// redeem redeems idJAG, or a fresh call-bound one when it is empty.
	redeem := func(idJAG string) string {
		t.Helper()
		if idJAG == "" {
			idJAG = as1.idJAG(t, as2)
		}
		status, body := postAS2Token(t, as2, url.Values{
			"grant_type": {oauthproto.GrantTypeJWTBearer}, "assertion": {idJAG}, "resource": {as2.Resource},
			"client_assertion_type": {clientAssertionTyp}, "client_assertion": {broker.assertion(t, as2.Issuer)},
		})
		token, _ := body["access_token"].(string)
		if status != http.StatusOK || token == "" {
			t.Fatalf("ID-JAG redemption: status %d, body %v", status, body)
		}
		return token
	}

	bound := unverifiedClaims(t, redeem(""))
	if iat, exp := bound["iat"].(float64), bound["exp"].(float64); time.Duration(exp-iat)*time.Second != 7*time.Second {
		t.Fatalf("the gateway token lives %vs, want the configured 7s", exp-iat)
	}
	if status := vmcpInitialize(t, process, redeem("")); status != http.StatusOK {
		t.Fatalf("vMCP initialize with a call-bound gateway token: status %d, want 200", status)
	}
	if status := vmcpInitialize(t, process, redeem(as1.unboundIDJAG(t, as2))); status != http.StatusForbidden {
		t.Fatalf("vMCP initialize with a gateway token without a call entry: status %d, want 403 entry_required", status)
	}
}

// TestToolHiveProcessRefusesGatewayTokenBoundsOutOfRange: the gateway token
// lifetime is whole seconds from 2s to 5m, and needs AS-2 to trust an ID-JAG
// issuer, since ToolHive applies it only to redeemed tokens.
func TestToolHiveProcessRefusesGatewayTokenBoundsOutOfRange(t *testing.T) {
	trust := NewAS2Trust(as2trust.Trust{Issuer: "https://as1.example"})
	for name, c := range map[string]struct {
		lifetime time.Duration
		trust    AS2Trust
		want     string
	}{
		"a lifetime under 2s":          {time.Second, trust, "whole seconds from 2s to 5m0s"},
		"a lifetime over 5m":           {6 * time.Minute, trust, "whole seconds from 2s to 5m0s"},
		"a lifetime of part a second":  {2500 * time.Millisecond, trust, "whole seconds from 2s to 5m0s"},
		"a lifetime AS-2 cannot apply": {30 * time.Second, AS2Trust{}, "needs AS-2 to trust an ID-JAG issuer"},
	} {
		t.Run(name, func(t *testing.T) {
			config := ToolHiveConfig{
				CallbackURL: as2TestCallbackURL, Profiles: []ToolHiveProfile{staticProtectedProfile("private")},
				GatewayTokenLifetime: c.lifetime, AS2Trust: c.trust,
			}
			if _, err := NewToolHiveProcess(t.Context(), config); !errors.Is(err, ErrInvalidCatalogue) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("NewToolHiveProcess = %v, want ErrInvalidCatalogue: %s", err, c.want)
			}
		})
	}
}

// TestGatewayCallBindingDefaults pins the vMCP's call binding in production:
// every token of the broker client must carry a call entry, a call stays
// open for at most 30 minutes, and only that client's tokens from AS-2 may
// send their call's messages for five minutes after exp.
func TestGatewayCallBindingDefaults(t *testing.T) {
	const issuer = as2TestIssuer
	binding, err := gatewayCallBinding(issuer, ToolHiveBrokerClient{ID: identitybroker.BrokerClientID}, AS2Trust{}.expiredTokenGrace(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !binding.ClientsListed() || binding.MaxCallDuration() != 30*time.Minute || !binding.AdmitsExpiredTokens() {
		t.Fatalf("binding lists clients %v, max call %v, admits expired tokens %v", binding.ClientsListed(), binding.MaxCallDuration(), binding.AdmitsExpiredTokens())
	}
	exp := time.Now()
	for _, c := range []struct {
		name   string
		claims map[string]any
		at     time.Time
		want   bool
	}{
		{"the broker client within five minutes", map[string]any{"iss": issuer, "client_id": identitybroker.BrokerClientID}, exp.Add(5*time.Minute - time.Second), true},
		{"the broker client after five minutes", map[string]any{"iss": issuer, "client_id": identitybroker.BrokerClientID}, exp.Add(5*time.Minute + time.Second), false},
		{"another client", map[string]any{"iss": issuer, "client_id": "other"}, exp.Add(time.Second), false},
		{"another issuer", map[string]any{"iss": "https://other.example", "client_id": identitybroker.BrokerClientID}, exp.Add(time.Second), false},
	} {
		if got := binding.WithinExpiredTokenGrace(c.claims, exp, c.at); got != c.want {
			t.Errorf("%s: within the grace = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestAS2RefusesClaimsOutsideTheCallType: AS-2 registers ToolHive's call
// type and nothing else, so it refuses an ID-JAG whose authorization_details
// names another type, holds a second call entry, or is not an array
// (credentials-and-lifecycle.md, step (8), check 8).
func TestAS2RefusesClaimsOutsideTheCallType(t *testing.T) {
	as1, broker, process := newTrustingAS2(t)
	as2 := process.AS2
	vmcp := as2.Resource
	entry := testCallDetails()[0]
	for name, c := range map[string]struct {
		claim        any
		code, reason string
	}{
		"another type":       {[]any{map[string]any{"type": "https://example.com/other"}}, "invalid_authorization_details", "unknown_type"},
		"a second entry":     {[]any{entry, entry}, "invalid_authorization_details", "single_entry_required"},
		"an entry, no array": {entry, "invalid_grant", "invalid_assertion"},
	} {
		t.Run(name, func(t *testing.T) {
			idJAG := as1.jwt(t, as2, as2.Issuer, idJAGType, map[string]any{"resource": as2.Resource, "act": as1.act(), "authorization_details": c.claim})
			status, body := as2Redemption(t, as2, broker, idJAG, &vmcp, true)
			description, _ := body["error_description"].(string)
			if status != http.StatusBadRequest || body["error"] != c.code || !strings.HasPrefix(description, c.reason) || body["access_token"] != nil {
				t.Fatalf("redemption = %d %v, want 400 %s %s", status, body, c.code, c.reason)
			}
		})
	}
}

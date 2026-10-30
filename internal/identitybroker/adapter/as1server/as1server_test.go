package as1server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stacklok/toolhive/pkg/authserver/storage"

	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

const (
	testAudience   = identityharness.DefaultAudience
	otherAudience  = "https://other.test/api"
	testClientID   = "cli"
	testRedirect   = "http://127.0.0.1:18473/oauth/callback"
	testIssuerPath = "/as1"
)

type fixture struct {
	h      *identityharness.Harness
	server *Server
	issuer string
}

// startAS1 hosts a real AS-1 on loopback TLS with the harness IdP as its
// upstream. The listener exists before the server so the issuer names it.
func startAS1(t *testing.T, h *identityharness.Harness, edit func(*Config)) *fixture {
	t.Helper()
	return startAS1At(t, h, testIssuerPath, edit)
}

// startAS1At is startAS1 with the issuer path issuerPath.
func startAS1At(t *testing.T, h *identityharness.Harness, issuerPath string, edit func(*Config)) *fixture {
	t.Helper()
	return startAS1With(t, h, issuerPath, edit, options{}, nil)
}

// startAS1With is startAS1At with opts, serving server.Handler() through
// wrap when it is set.
func startAS1With(t *testing.T, h *identityharness.Harness, issuerPath string, edit func(*Config), opts options, wrap func(http.Handler) http.Handler) *fixture {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	issuer := "https://" + ln.Addr().String() + issuerPath
	server := newTestServerWith(t, h, issuer, edit, opts)
	handler := server.Handler()
	if wrap != nil {
		handler = wrap(handler)
	}
	ts := &httptest.Server{Listener: ln, Config: &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return &fixture{h: h, server: server, issuer: issuer}
}

// testConfig configures AS-1 for issuer with the harness IdP, which it
// trusts and admits on loopback, as its upstream, and registers it there.
// Registering again for the same issuer replaces the upstream client secret.
func testConfig(t *testing.T, h *identityharness.Harness, issuer string) Config {
	t.Helper()
	dir := t.TempDir()
	clientID := "as1-" + strings.NewReplacer(":", "-", "/", "-", ".", "-").Replace(strings.TrimPrefix(issuer, "https://"))
	secret := rand.Text()
	secretFile := filepath.Join(dir, "upstream-client-secret")
	if err := os.WriteFile(secretFile, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	h.IdP.RegisterClient(clientID, secret, issuer+"/oauth/callback")
	return Config{
		Issuer: issuer, Audience: testAudience,
		PublicClients: []PublicClient{{ID: testClientID, RedirectURIs: []string{testRedirect}}},
		Upstream: UpstreamConfig{
			Issuer: h.IdP.Issuer(), ClientID: clientID, ClientSecretFile: secretFile,
			CAFile: h.WriteCACert(t, dir), AllowPrivateAddresses: true,
		},
	}
}

// newTestServer builds AS-1 for issuer from testConfig after edit.
func newTestServer(t *testing.T, h *identityharness.Harness, issuer string, edit func(*Config)) *Server {
	t.Helper()
	return newTestServerWith(t, h, issuer, edit, options{})
}

// newTestServerWith is newTestServer with opts, to which it adds the other
// test audience.
func newTestServerWith(t *testing.T, h *identityharness.Harness, issuer string, edit func(*Config), opts options) *Server {
	t.Helper()
	cfg := testConfig(t, h, issuer)
	if edit != nil {
		edit(&cfg)
	}
	opts.extraAudiences = append(opts.extraAudiences, otherAudience)
	server, err := newServer(t.Context(), cfg, opts)
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return server
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
	status       int
}

// signIn signs the default harness user in as the test client, with PKCE,
// asking for a token for resource.
func (f *fixture) signIn(t *testing.T, resource string) tokenResponse {
	t.Helper()
	return f.signInAs(t, PublicClient{ID: testClientID, RedirectURIs: []string{testRedirect}}, resource)
}

// signInAs is signIn as client, at its first redirect URI.
func (f *fixture) signInAs(t *testing.T, client PublicClient, resource string) tokenResponse {
	t.Helper()
	q, verifier := authorizationRequest(client, resource)
	state := q.Get("state")
	back, err := f.h.AuthorizeWithConsent(t.Context(), f.issuer+"/oauth/authorize?"+q.Encode())
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if back.Get("error") != "" || back.Get("state") != state || back.Get("code") == "" {
		t.Fatalf("authorize redirect: error=%q description=%q state match=%v", back.Get("error"), back.Get("error_description"), back.Get("state") == state)
	}
	tokens := f.token(t, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {client.ID},
		"code":          {back.Get("code")},
		"redirect_uri":  {client.RedirectURIs[0]},
		"code_verifier": {verifier},
		"resource":      {resource},
	})
	if tokens.status != http.StatusOK || tokens.AccessToken == "" {
		t.Fatalf("code exchange: status %d error %q", tokens.status, tokens.Error)
	}
	return tokens
}

// authorizationRequest is the query of client's PKCE authorization request
// for resource, and its code verifier.
func authorizationRequest(client PublicClient, resource string) (url.Values, string) {
	verifier := rand.Text() + rand.Text()
	sum := sha256.Sum256([]byte(verifier))
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {client.ID},
		"redirect_uri":          {client.RedirectURIs[0]},
		"scope":                 {"openid offline_access"},
		"state":                 {rand.Text()},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		"resource":              {resource},
	}, verifier
}

// startAuthorization sends the test client's authorization request without
// following redirects and returns the response status, its Location (nil
// without one), and its body.
func (f *fixture) startAuthorization(t *testing.T) (int, *url.URL, string) {
	t.Helper()
	q, _ := authorizationRequest(PublicClient{ID: testClientID, RedirectURIs: []string{testRedirect}}, testAudience)
	client := f.h.HTTPClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Get(f.issuer + "/oauth/authorize?" + q.Encode())
	if err != nil {
		t.Errorf("authorize: %v", err)
		return 0, nil, ""
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	location, _ := resp.Location()
	return resp.StatusCode, location, string(body)
}

func (f *fixture) refresh(t *testing.T, refreshToken, resource string) tokenResponse {
	t.Helper()
	return f.token(t, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {testClientID},
		"refresh_token": {refreshToken},
		"resource":      {resource},
	})
}

func (f *fixture) token(t *testing.T, form url.Values) tokenResponse {
	t.Helper()
	resp, err := f.h.HTTPClient().PostForm(f.issuer+"/oauth/token", form)
	if err != nil {
		t.Fatalf("token request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("token response: %v", err)
	}
	out.status = resp.StatusCode
	return out
}

// parse decodes an access token without checking its signature.
func parse(t *testing.T, token string) (map[string]any, jwt.MapClaims) {
	t.Helper()
	claims := jwt.MapClaims{}
	parsed, _, err := jwt.NewParser().ParseUnverified(token, claims)
	if err != nil {
		t.Fatalf("parse access token: %v", err)
	}
	return parsed.Header, claims
}

// jwks fetches server's key set through its in-process client.
func jwks(t *testing.T, server *Server) map[string]*ecdsa.PublicKey {
	t.Helper()
	resp, err := server.InProcessClient().Get(server.Issuer() + "/.well-known/jwks.json")
	if err != nil {
		t.Fatalf("JWKS: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("JWKS: status %d", resp.StatusCode)
	}
	var set struct {
		Keys []struct{ Kid, Kty, Crv, X, Y string } `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		t.Fatalf("JWKS: %v", err)
	}
	keys := make(map[string]*ecdsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		if k.Kty != "EC" || k.Crv != "P-256" {
			t.Fatalf("JWKS: unexpected key type %s %s", k.Kty, k.Crv)
		}
		x, xErr := base64.RawURLEncoding.DecodeString(k.X)
		y, yErr := base64.RawURLEncoding.DecodeString(k.Y)
		if xErr != nil || yErr != nil {
			t.Fatal("JWKS: malformed key")
		}
		key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), slices.Concat([]byte{4}, x, y))
		if err != nil {
			t.Fatalf("JWKS: %v", err)
		}
		keys[k.Kid] = key
	}
	if len(keys) == 0 {
		t.Fatal("JWKS: no keys")
	}
	return keys
}

// verify checks token's signature against keys.
func verify(token string, keys map[string]*ecdsa.PublicKey) error {
	_, err := jwt.Parse(token, func(tok *jwt.Token) (any, error) {
		kid, _ := tok.Header["kid"].(string)
		key, ok := keys[kid]
		if !ok {
			return nil, errors.New("unknown kid")
		}
		return key, nil
	}, jwt.WithValidMethods([]string{"ES256"}))
	return err
}

func TestSignInIssuesAudienceTokenWithCompanyIdentity(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	f := startAS1(t, h, nil)

	tokens := f.signIn(t, testAudience)

	header, claims := parse(t, tokens.AccessToken)
	if header["typ"] != "at+jwt" {
		t.Errorf("typ = %v, want at+jwt", header["typ"])
	}
	if aud, _ := claims.GetAudience(); !slices.Equal(aud, jwt.ClaimStrings{testAudience}) {
		t.Errorf("aud = %v, want [%s]", aud, testAudience)
	}
	if iss, _ := claims.GetIssuer(); iss != f.issuer {
		t.Errorf("iss = %q, want %q", iss, f.issuer)
	}
	if claims["client_id"] != testClientID {
		t.Errorf("client_id = %v, want %s", claims["client_id"], testClientID)
	}
	if jti, _ := claims["jti"].(string); jti == "" {
		t.Error("jti missing")
	}
	if sub, _ := claims.GetSubject(); sub == "" {
		t.Error("sub missing")
	}
	wantSubID := map[string]any{"format": "iss_sub", "iss": h.IdP.Issuer(), "sub": identityharness.Alice}
	if subID, _ := claims["sub_id"].(map[string]any); !mapsEqual(subID, wantSubID) {
		t.Errorf("sub_id = %v, want %v", claims["sub_id"], wantSubID)
	}
	if tokens.RefreshToken == "" {
		t.Error("no refresh token for offline_access")
	}
	if got := h.IdP.RefreshTokensIssued(identityharness.Alice); got != 1 {
		t.Errorf("upstream refresh tokens issued = %d, want 1 (offline_access with prompt=consent)", got)
	}
	if err := verify(tokens.AccessToken, jwks(t, f.server)); err != nil {
		t.Errorf("access token does not verify against AS-1's JWKS: %v", err)
	}
}

func mapsEqual(got, want map[string]any) bool {
	if len(got) != len(want) {
		return false
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

func TestEveryPublicClientCanSignIn(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	second := PublicClient{ID: "studio", RedirectURIs: []string{"http://127.0.0.1:18474/callback"}}
	f := startAS1(t, h, func(c *Config) { c.PublicClients = append(c.PublicClients, second) })

	for _, client := range []PublicClient{{ID: testClientID, RedirectURIs: []string{testRedirect}}, second} {
		_, claims := parse(t, f.signInAs(t, client, testAudience).AccessToken)
		if claims["client_id"] != client.ID {
			t.Errorf("client_id = %v, want %s", claims["client_id"], client.ID)
		}
	}
}

// TestSubjectClaimReachesTheUpstreamSubject: the harness IdP's ID tokens
// carry an oid claim that differs from sub, and the subject claim picks which
// one AS-1 keys the user on, which ToolHive's upstream_subject claim carries.
func TestSubjectClaimReachesTheUpstreamSubject(t *testing.T) {
	for name, c := range map[string]struct {
		claim, wantClaim, wantValue string
	}{
		"default": {claim: "", wantClaim: "sub", wantValue: identityharness.Alice},
		"oid":     {claim: "oid", wantClaim: "oid", wantValue: identityharness.OID(identityharness.Alice)},
	} {
		t.Run(name, func(t *testing.T) {
			h := identityharness.New(t, identityharness.Options{})
			f := startAS1(t, h, func(cfg *Config) { cfg.Upstream.SubjectClaim = c.claim })

			_, claims := parse(t, f.signIn(t, testAudience).AccessToken)

			want := map[string]any{"iss": h.IdP.Issuer(), "claim": c.wantClaim, "value": c.wantValue}
			if got, _ := claims["https://toolhive.dev/upstream_subject"].(map[string]any); !mapsEqual(got, want) {
				t.Errorf("upstream_subject = %v, want %v", claims["https://toolhive.dev/upstream_subject"], want)
			}
		})
	}
}

// TestRootIssuerServesAtTheRoot hosts AS-1 at an issuer without a path: its
// routes sit at the root, sign-in works, and the in-process client reaches it.
func TestRootIssuerServesAtTheRoot(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	f := startAS1At(t, h, "", nil)

	wantRoutes := []string{
		"/oauth/authorize", "/oauth/consent", "/oauth/token", "/oauth/callback",
		"/.well-known/openid-configuration", "/.well-known/oauth-authorization-server", "/.well-known/jwks.json",
	}
	if got := f.server.Routes(); !slices.Equal(got, wantRoutes) {
		t.Errorf("Routes() = %v, want %v", got, wantRoutes)
	}
	tokens := f.signIn(t, testAudience)
	if _, claims := parse(t, tokens.AccessToken); claims["iss"] != f.issuer {
		t.Errorf("iss = %v, want %s", claims["iss"], f.issuer)
	}
	if err := verify(tokens.AccessToken, jwks(t, f.server)); err != nil {
		t.Errorf("access token does not verify against the in-process JWKS: %v", err)
	}

	issuer := "https://as1.mecatl.test"
	client := newTestServer(t, h, issuer, nil).InProcessClient()
	resp, err := client.Get(issuer + "/.well-known/openid-configuration")
	if err != nil {
		t.Fatalf("in-process discovery: %v", err)
	}
	var discovery struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	err = json.NewDecoder(resp.Body).Decode(&discovery)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || discovery.Issuer != issuer || discovery.JWKSURI != issuer+"/.well-known/jwks.json" {
		t.Fatalf("in-process discovery: status %d, issuer %q, jwks_uri %q, err %v", resp.StatusCode, discovery.Issuer, discovery.JWKSURI, err)
	}
	if resp, err := client.Get("https://other.mecatl.test/.well-known/jwks.json"); err == nil {
		_ = resp.Body.Close()
		t.Error("in-process client served another host")
	}
}

// TestUpstreamCATrustIsSeparateFromPrivateAddresses: trusting a CA for the
// upstream does not switch off its SSRF protection, and only the CA file makes
// the harness IdP's certificate trusted. ToolHive admits a loopback issuer
// host whatever AllowPrivateAddresses says, so the private-address half is
// checked on the configuration AS-1 hands ToolHive.
func TestUpstreamCATrustIsSeparateFromPrivateAddresses(t *testing.T) {
	caOnly := Config{Issuer: "https://as1.mecatl.test", Upstream: UpstreamConfig{CAFile: "/ca.pem"}}.upstreamRunConfig()
	if caOnly.CAFilePath != "/ca.pem" || caOnly.AllowPrivateIPs {
		t.Errorf("CA file alone: CAFilePath %q AllowPrivateIPs %v, want the CA and no private addresses", caOnly.CAFilePath, caOnly.AllowPrivateIPs)
	}
	privateOnly := Config{Upstream: UpstreamConfig{AllowPrivateAddresses: true}}.upstreamRunConfig()
	if privateOnly.CAFilePath != "" || !privateOnly.AllowPrivateIPs {
		t.Errorf("private addresses alone: CAFilePath %q AllowPrivateIPs %v", privateOnly.CAFilePath, privateOnly.AllowPrivateIPs)
	}

	h := identityharness.New(t, identityharness.Options{})
	cfg := testConfig(t, h, "https://as1.mecatl.test"+testIssuerPath)
	cfg.Upstream.CAFile = ""
	if server, err := New(t.Context(), cfg); err == nil {
		_ = server.Close()
		t.Fatal("New trusted the harness IdP without its CA file")
	}
	cfg.Upstream.CAFile, cfg.Upstream.AllowPrivateAddresses = h.WriteCACert(t, t.TempDir()), false
	server, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatalf("New with the CA file and a loopback upstream: %v", err)
	}
	_ = server.Close()
}

func TestTokenForAnotherAudienceCarriesNoSubjectIdentifier(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	f := startAS1(t, h, nil)

	_, claims := parse(t, f.signIn(t, otherAudience).AccessToken)

	if aud, _ := claims.GetAudience(); !slices.Equal(aud, jwt.ClaimStrings{otherAudience}) {
		t.Fatalf("aud = %v, want [%s]", aud, otherAudience)
	}
	if _, ok := claims["sub_id"]; ok {
		t.Errorf("sub_id present for audience %s: %v", otherAudience, claims["sub_id"])
	}
}

func TestRepeatedSignInKeepsSubject(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	f := startAS1(t, h, nil)

	_, first := parse(t, f.signIn(t, testAudience).AccessToken)
	_, second := parse(t, f.signIn(t, testAudience).AccessToken)

	firstSub, _ := first.GetSubject()
	secondSub, _ := second.GetSubject()
	if firstSub == "" || firstSub != secondSub {
		t.Errorf("sub changed across sign-ins: %q then %q", firstSub, secondSub)
	}
	if first["jti"] == second["jti"] {
		t.Error("two sign-ins share a jti")
	}
}

func TestRefreshRotatesAndChecksResource(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	f := startAS1(t, h, nil)
	signedIn := f.signIn(t, testAudience)
	_, signedInClaims := parse(t, signedIn.AccessToken)

	if wrong := f.refresh(t, signedIn.RefreshToken, "https://elsewhere.test/api"); wrong.status == http.StatusOK {
		t.Fatal("refresh for a resource that is not an allowed audience succeeded")
	}

	refreshed := f.refresh(t, signedIn.RefreshToken, testAudience)
	if refreshed.status != http.StatusOK {
		t.Fatalf("refresh: status %d error %q", refreshed.status, refreshed.Error)
	}
	if refreshed.RefreshToken == "" || refreshed.RefreshToken == signedIn.RefreshToken {
		t.Error("refresh token did not rotate")
	}
	_, claims := parse(t, refreshed.AccessToken)
	if aud, _ := claims.GetAudience(); !slices.Equal(aud, jwt.ClaimStrings{testAudience}) {
		t.Errorf("refreshed aud = %v", aud)
	}
	if claims["sub"] != signedInClaims["sub"] || claims["sub_id"] == nil {
		t.Errorf("refreshed token lost the identity: sub %v sub_id %v", claims["sub"], claims["sub_id"])
	}

	if reused := f.refresh(t, signedIn.RefreshToken, testAudience); reused.status == http.StatusOK {
		t.Error("a rotated refresh token was accepted again")
	}
}

func TestSigningKeyFileSurvivesRestart(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	keyFile := h.WriteSigningKey(t, t.TempDir())
	withKey := func(cfg *Config) { cfg.SigningKeyFile = keyFile }
	before := startAS1(t, h, withKey)
	token := before.signIn(t, testAudience).AccessToken

	// The restarted instance has the same issuer and key file, and is never
	// served: its JWKS comes through the in-process client.
	after := newTestServer(t, h, before.issuer, withKey)

	if err := verify(token, jwks(t, after)); err != nil {
		t.Errorf("token from the first instance does not verify against the restarted instance's JWKS: %v", err)
	}
}

func TestGeneratedSigningKeysDifferPerInstance(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	f := startAS1(t, h, nil)
	token := f.signIn(t, testAudience).AccessToken
	other := newTestServer(t, h, f.issuer, nil)

	first, second := jwks(t, f.server), jwks(t, other)
	for kid := range first {
		if _, shared := second[kid]; shared {
			t.Errorf("generated key %q shared by two instances", kid)
		}
	}
	if err := verify(token, second); err == nil {
		t.Error("token verified against another instance's generated keys")
	}
}

func TestServesOnlySignInRoutes(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	f := startAS1(t, h, nil)
	client := h.HTTPClient()
	origin := strings.TrimSuffix(f.issuer, testIssuerPath)

	for _, path := range []string{
		testIssuerPath + "/.well-known/openid-configuration",
		testIssuerPath + "/.well-known/oauth-authorization-server",
		testIssuerPath + "/.well-known/jwks.json",
	} {
		resp, err := client.Get(origin + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200", path, resp.StatusCode)
		}
	}
	for _, path := range []string{
		testIssuerPath + "/oauth/register",
		testIssuerPath + "/oauth/device_authorization",
		testIssuerPath + "/oauth/device",
		testIssuerPath + "/oauth/device/confirm",
		"/oauth/token",
		"/.well-known/openid-configuration",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			req, err := http.NewRequestWithContext(t.Context(), method, origin+path, strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s %s: status %d, want 404", method, path, resp.StatusCode)
			}
		}
	}

	wantRoutes := []string{
		testIssuerPath + "/oauth/authorize",
		testIssuerPath + "/oauth/consent",
		testIssuerPath + "/oauth/token",
		testIssuerPath + "/oauth/callback",
		testIssuerPath + "/.well-known/openid-configuration",
		testIssuerPath + "/.well-known/oauth-authorization-server",
		testIssuerPath + "/.well-known/jwks.json",
	}
	if got := f.server.Routes(); !slices.Equal(got, wantRoutes) {
		t.Errorf("Routes() = %v, want %v", got, wantRoutes)
	}
}

// TestDiscoveryOmitsUnroutedEndpoints: both discovery documents keep the
// endpoints AS-1 serves and drop every other endpoint ToolHive advertises,
// with a Content-Length that matches the rewritten body.
func TestDiscoveryOmitsUnroutedEndpoints(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	f := startAS1(t, h, nil)
	for _, doc := range []string{"/.well-known/openid-configuration", "/.well-known/oauth-authorization-server"} {
		resp, err := h.HTTPClient().Get(f.issuer + doc)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status %d: %v", doc, resp.StatusCode, err)
		}
		if resp.Header.Get("Content-Length") != strconv.Itoa(len(body)) || resp.Header.Get("Content-Type") != "application/json" {
			t.Errorf("GET %s: Content-Length %q Content-Type %q for a %d-byte body", doc, resp.Header.Get("Content-Length"), resp.Header.Get("Content-Type"), len(body))
		}
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("GET %s: %v", doc, err)
		}
		for key, want := range map[string]string{
			"authorization_endpoint": f.issuer + "/oauth/authorize",
			"token_endpoint":         f.issuer + "/oauth/token",
			"jwks_uri":               f.issuer + "/.well-known/jwks.json",
			"issuer":                 f.issuer,
		} {
			if got[key] != want {
				t.Errorf("GET %s: %s = %v, want %s", doc, key, got[key], want)
			}
		}
		for key := range got {
			if strings.HasSuffix(key, "_endpoint") && key != "authorization_endpoint" && key != "token_endpoint" {
				t.Errorf("GET %s advertises %s = %v, which AS-1 does not serve", doc, key, got[key])
			}
		}
	}

	// Endpoints ToolHive advertises in other configurations, such as the
	// device flow, are dropped the same way.
	routed := map[string]bool{"https://as1.test/oauth/token": true}
	upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "999")
		_, _ = w.Write([]byte(`{"token_endpoint":"https://as1.test/oauth/token","device_authorization_endpoint":"https://as1.test/oauth/device_authorization",` +
			`"revocation_endpoint":"https://elsewhere.test/oauth/token","odd_endpoint":7,"scopes_supported":["openid"]}`))
	})
	rec := httptest.NewRecorder()
	withoutUnroutedEndpoints(upstream, routed).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	want := `{"scopes_supported":["openid"],"token_endpoint":"https://as1.test/oauth/token"}`
	if rec.Body.String() != want || rec.Header().Get("Content-Length") != strconv.Itoa(len(want)) {
		t.Errorf("rewritten document = %s (Content-Length %s), want %s", rec.Body, rec.Header().Get("Content-Length"), want)
	}
}

// TestPendingSignInsAreCapped: once the cap of pending sign-ins is reached a
// new authorization request gets a clean server_error redirect, a sign-in
// already in progress still completes, and finished sign-ins free capacity.
func TestPendingSignInsAreCapped(t *testing.T) {
	const limit = 3
	h := identityharness.New(t, identityharness.Options{})
	var f *fixture
	var filled atomic.Bool
	// When the sign-in in progress approves its consent page, it holds one
	// pending authorization: fill the rest and check the next is refused.
	fillAtConsent := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/oauth/consent") && filled.CompareAndSwap(false, true) {
				for range limit - 1 {
					if status, _, _ := f.startAuthorization(t); status != http.StatusOK {
						t.Errorf("authorization below the cap: status %d, want the consent page", status)
					}
				}
				checkRefused(t, f)
			}
			next.ServeHTTP(w, r)
		})
	}
	f = startAS1With(t, h, testIssuerPath, nil, options{maxPendingAuthorizations: limit}, fillAtConsent)

	f.signIn(t, testAudience)
	if !filled.Load() {
		t.Fatal("the sign-in never posted its consent page")
	}
	// The completed sign-in freed its entry; the next one takes it.
	if status, _, _ := f.startAuthorization(t); status != http.StatusOK {
		t.Errorf("authorization after a sign-in completed: status %d, want the consent page", status)
	}
	checkRefused(t, f)
}

// checkRefused checks that a new authorization request at f gets ToolHive's
// server_error redirect to the client, without a 500 or the storage error.
func checkRefused(t *testing.T, f *fixture) {
	t.Helper()
	status, location, body := f.startAuthorization(t)
	if status != http.StatusFound && status != http.StatusSeeOther {
		t.Errorf("authorization at the cap: status %d, want a redirect to the client", status)
		return
	}
	if location == nil || !strings.HasPrefix(location.String(), testRedirect+"?") || location.Query().Get("error") != "server_error" {
		t.Errorf("authorization at the cap redirected to %v, want server_error at the client", location)
	}
	if strings.Contains(location.String()+body, errTooManyPendingAuthorizations.Error()) {
		t.Errorf("authorization at the cap leaked the storage error: %v %s", location, body)
	}
}

// TestPendingCapReservesRoomPastConsent: new sign-ins stop at the cap, and
// those past consent may use a further tenth.
func TestPendingCapReservesRoomPastConsent(t *testing.T) {
	store := &cappedStorage{MemoryStorage: storage.NewMemoryStorage(), limit: 10}
	t.Cleanup(func() { _ = store.Close() })
	pending := func(stage string) *storage.PendingAuthorization {
		return &storage.PendingAuthorization{ConsentStage: stage, CreatedAt: time.Now()}
	}
	for i := range 10 {
		if err := store.StorePendingAuthorization(t.Context(), rand.Text(), pending(storage.ConsentStageAwaiting)); err != nil {
			t.Fatalf("new sign-in %d below the cap: %v", i, err)
		}
	}
	if err := store.StorePendingAuthorization(t.Context(), rand.Text(), pending(storage.ConsentStageAwaiting)); !errors.Is(err, errTooManyPendingAuthorizations) {
		t.Fatalf("new sign-in at the cap: %v, want refused", err)
	}
	if err := store.StorePendingAuthorization(t.Context(), rand.Text(), pending(storage.ConsentStageApproved)); err != nil {
		t.Fatalf("sign-in past consent at the cap: %v", err)
	}
	if err := store.StorePendingAuthorization(t.Context(), rand.Text(), pending(storage.ConsentStageApproved)); !errors.Is(err, errTooManyPendingAuthorizations) {
		t.Fatalf("sign-in past consent beyond the reserve: %v, want refused", err)
	}
}

func TestInProcessClientServesOnlyTheIssuer(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	issuer := "https://as1.mecatl.test" + testIssuerPath
	server := newTestServer(t, h, issuer, nil)
	client := server.InProcessClient()

	resp, err := client.Get(issuer + "/.well-known/openid-configuration")
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	var discovery struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	err = json.NewDecoder(resp.Body).Decode(&discovery)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("discovery: status %d: %v", resp.StatusCode, err)
	}
	if discovery.Issuer != issuer || discovery.JWKSURI != issuer+"/.well-known/jwks.json" {
		t.Errorf("discovery issuer %q jwks_uri %q", discovery.Issuer, discovery.JWKSURI)
	}
	jwks(t, server)

	for _, refused := range []string{
		"http://as1.mecatl.test" + testIssuerPath + "/.well-known/jwks.json",
		"https://other.mecatl.test" + testIssuerPath + "/.well-known/jwks.json",
		"https://as1.mecatl.test:8443" + testIssuerPath + "/.well-known/jwks.json",
		"https://as1.mecatl.test/.well-known/openid-configuration",
		"https://as1.mecatl.test" + testIssuerPath + "x/.well-known/jwks.json",
		"https://as1.mecatl.test" + testIssuerPath + "/../.well-known/jwks.json",
	} {
		resp, err := client.Get(refused)
		if err == nil {
			_ = resp.Body.Close()
			t.Errorf("in-process client served %s: status %d", refused, resp.StatusCode)
		}
	}
}

func TestConfigValidation(t *testing.T) {
	valid := Config{
		Issuer: "https://as1.mecatl.test/as1", Audience: testAudience,
		Upstream: UpstreamConfig{Issuer: "https://idp.example", ClientID: "mecatl-as1"},
	}
	client := PublicClient{ID: testClientID, RedirectURIs: []string{testRedirect}}
	for name, edit := range map[string]func(*Config){
		"empty issuer":            func(c *Config) { c.Issuer = "" },
		"http issuer":             func(c *Config) { c.Issuer = "http://as1.mecatl.test/as1" },
		"issuer with query":       func(c *Config) { c.Issuer = "https://as1.mecatl.test/as1?x=1" },
		"issuer with fragment":    func(c *Config) { c.Issuer = "https://as1.mecatl.test/as1#x" },
		"issuer with userinfo":    func(c *Config) { c.Issuer = "https://u@as1.mecatl.test/as1" },
		"issuer trailing slash":   func(c *Config) { c.Issuer = "https://as1.mecatl.test/as1/" },
		"issuer unclean path":     func(c *Config) { c.Issuer = "https://as1.mecatl.test/a/../as1" },
		"empty audience":          func(c *Config) { c.Audience = "" },
		"no public clients":       func(c *Config) { c.PublicClients = nil },
		"empty client ID":         func(c *Config) { c.PublicClients[0].ID = "" },
		"duplicate client IDs":    func(c *Config) { c.PublicClients = append(c.PublicClients, client) },
		"no redirect URIs":        func(c *Config) { c.PublicClients[0].RedirectURIs = nil },
		"relative redirect URI":   func(c *Config) { c.PublicClients[0].RedirectURIs = []string{"/oauth/callback"} },
		"redirect URI fragment":   func(c *Config) { c.PublicClients[0].RedirectURIs = []string{testRedirect + "#x"} },
		"empty upstream issuer":   func(c *Config) { c.Upstream.Issuer = "" },
		"http upstream issuer":    func(c *Config) { c.Upstream.Issuer = "http://idp.example" },
		"empty upstream client":   func(c *Config) { c.Upstream.ClientID = "" },
		"upstream issuer query":   func(c *Config) { c.Upstream.Issuer = "https://idp.example?x=1" },
		"upstream issuer no host": func(c *Config) { c.Upstream.Issuer = "https:///tenant" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			cfg.PublicClients = []PublicClient{client}
			edit(&cfg)
			server, err := New(t.Context(), cfg)
			if server != nil {
				_ = server.Close()
			}
			if !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("New: %v, want ErrInvalidConfig", err)
			}
		})
	}
}

package as1server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	issuer := "https://" + ln.Addr().String() + issuerPath
	server := newTestServer(t, h, issuer, edit)
	ts := &httptest.Server{Listener: ln, Config: &http.Server{Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}}
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
	cfg := testConfig(t, h, issuer)
	if edit != nil {
		edit(&cfg)
	}
	server, err := newServer(t.Context(), cfg, options{extraAudiences: []string{otherAudience}})
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
	verifier := rand.Text() + rand.Text()
	sum := sha256.Sum256([]byte(verifier))
	state := rand.Text()
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {client.ID},
		"redirect_uri":          {client.RedirectURIs[0]},
		"scope":                 {"openid offline_access"},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		"resource":              {resource},
	}
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

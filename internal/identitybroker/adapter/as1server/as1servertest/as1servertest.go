// Package as1servertest runs the real AS-1 (package as1server) for tests of
// the identity broker and the code that wires it: in process, on loopback
// TLS, with the identityharness IdP as its upstream and the broker's delegate
// client keyed by a fresh broker key. It signs users in as the TUI would, and
// puts a FaultTransport between the broker's authserverclient.Client and
// AS-1, which injects transport faults and arbitrary refusals and records
// what reached the token endpoint.
//
// AS-1 has no clock: it stamps expiries with wall time, so tests that need a
// later time move the broker's clock, not AS-1's.
//
// The package is test code; production packages never import it.
package as1servertest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/authserverclient"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

const (
	// Audience is the Mecatl API identifier AS-1 issues access tokens for.
	Audience = identityharness.DefaultAudience
	// PublicClientID is the public sign-in client Start registers and lists
	// as the delegate's only allowed source client.
	PublicClientID = "cli"
	// RedirectURI is PublicClientID's only redirect URI. Sign-in never
	// contacts it.
	RedirectURI = "http://127.0.0.1:18473/oauth/callback"
	// IssuerPath is the path of AS-1's issuer URL.
	IssuerPath = "/as1"
	// IDJAGMaxLifetime is the delegate's ID-JAG grant lifetime cap unless the
	// edit hook changes it: the expiry AS-1 grants an exchange that asks for
	// none.
	IDJAGMaxLifetime = time.Hour
	// DefaultScope is the scope SignIn asks for when SignIn.Scope is empty.
	DefaultScope = "openid offline_access"
)

// PublicClient returns the public client Start registers, PublicClientID at
// RedirectURI.
func PublicClient() as1server.PublicClient {
	return as1server.PublicClient{ID: PublicClientID, RedirectURIs: []string{RedirectURI}}
}

// AS1 is a running real AS-1 and the broker credentials registered at it.
type AS1 struct {
	// Harness is the harness whose IdP AS-1 signs users in at.
	Harness *identityharness.Harness
	// Server is AS-1.
	Server *as1server.Server
	// Key is the broker client key registered for the delegate client, and
	// Signer mints client assertions with it.
	Key    *ecdsa.PrivateKey
	Signer *identitybroker.ClientAssertionSigner
	// Faults stands between the broker's client and AS-1.
	Faults *FaultTransport

	signingKeyFile string
}

// Start runs AS-1 for t on loopback TLS with h's IdP as its upstream: issuer
// https://127.0.0.1:port/as1, audience Audience, public client PublicClient,
// a signing key Start writes to a file, and the delegate client
// identitybroker.BrokerClientID keyed by a fresh broker key, allowing
// PublicClientID with IDJAGMaxLifetime. edit, when set, changes the Config
// and the DelegateClient before AS-1 is built. AS-1 stops when t ends.
func Start(t testing.TB, h *identityharness.Harness, edit func(*as1server.Config, *as1server.DelegateClient)) *AS1 {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("as1servertest: broker key: %v", err)
	}
	signer, err := identitybroker.NewClientAssertionSigner(key, 0)
	if err != nil {
		t.Fatalf("as1servertest: assertion signer: %v", err)
	}
	keySet, err := json.Marshal(signer.JWKS())
	if err != nil {
		t.Fatalf("as1servertest: broker key set: %v", err)
	}
	// The listener exists before the server so the issuer names it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("as1servertest: listen: %v", err)
	}
	issuer := "https://" + ln.Addr().String() + IssuerPath
	cfg := config(t, h, issuer)
	delegate := as1server.DelegateClient{
		ID:                   identitybroker.BrokerClientID,
		KeySet:               keySet,
		AllowedSourceClients: []string{PublicClientID},
		IDJAGMaxLifetime:     IDJAGMaxLifetime,
	}
	if edit != nil {
		edit(&cfg, &delegate)
	}
	cfg.Delegate = &delegate
	server, err := as1server.New(t.Context(), cfg)
	if err != nil {
		_ = ln.Close()
		t.Fatalf("as1servertest: as1server.New: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("as1servertest: Close: %v", err)
		}
	})
	ts := &httptest.Server{Listener: ln, Config: &http.Server{Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return &AS1{
		Harness: h, Server: server, Key: key, Signer: signer,
		Faults:         newFaultTransport(server),
		signingKeyFile: cfg.SigningKeyFile,
	}
}

// config configures AS-1 for issuer with h's IdP, which it trusts and admits
// on loopback, as its upstream, and registers it there.
func config(t testing.TB, h *identityharness.Harness, issuer string) as1server.Config {
	t.Helper()
	dir := t.TempDir()
	clientID := "as1-" + strings.NewReplacer(":", "-", "/", "-", ".", "-").Replace(strings.TrimPrefix(issuer, "https://"))
	return as1server.Config{
		Issuer:         issuer,
		Audience:       Audience,
		SigningKeyFile: h.WriteSigningKey(t, dir),
		PublicClients:  []as1server.PublicClient{PublicClient()},
		Upstream: as1server.UpstreamConfig{
			Issuer: h.IdP.Issuer(), ClientID: clientID, ClientSecretFile: h.RegisterAS1Upstream(t, dir, clientID, issuer),
			CAFile: h.WriteCACert(t, dir), AllowPrivateAddresses: true,
		},
	}
}

// Issuer is AS-1's issuer URL.
func (a *AS1) Issuer() string { return a.Server.Issuer() }

// HTTPClient returns a client that reaches AS-1 in process through Faults
// and does not follow redirects, for wiring that takes an *http.Client.
func (a *AS1) HTTPClient() *http.Client {
	return &http.Client{
		Transport:     a.Faults,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Client returns the broker's authorization-server client for AS-1: it
// authenticates as the delegate with Signer on the wall clock and reaches
// AS-1 through HTTPClient.
func (a *AS1) Client(t testing.TB) *authserverclient.Client {
	t.Helper()
	c, err := authserverclient.New(authserverclient.Config{
		Issuer:        a.Server.Issuer(),
		TokenEndpoint: a.Server.TokenEndpoint(),
		Assertions:    a.Signer,
		HTTPClient:    a.HTTPClient(),
	})
	if err != nil {
		t.Fatalf("as1servertest: authserverclient.New: %v", err)
	}
	return c
}

// SignIn selects who signs in, with which client, and what they ask for.
// The zero value signs the IdP's default user in as PublicClient with
// DefaultScope for Audience.
type SignIn struct {
	// User is the company identity the IdP signs in. Empty selects the IdP's
	// default user.
	User string
	// Client is the public client, signing in at its first redirect URI.
	// Zero selects PublicClient.
	Client as1server.PublicClient
	// Scope replaces DefaultScope, such as "openid" for a token without
	// offline_access.
	Scope string
	// Resource replaces Audience as the requested resource.
	Resource string
}

// SignIn signs a user in at AS-1 as req selects, playing the browser with
// PKCE and approving consent, and returns their access token. The sign-in
// does not pass through Faults.
func (a *AS1) SignIn(t testing.TB, req SignIn) identitybroker.Token {
	t.Helper()
	client := req.Client
	if client.ID == "" {
		client = PublicClient()
	}
	scope, resource := req.Scope, req.Resource
	if scope == "" {
		scope = DefaultScope
	}
	if resource == "" {
		resource = Audience
	}
	verifier := rand.Text() + rand.Text()
	sum := sha256.Sum256([]byte(verifier))
	state := rand.Text()
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {client.ID},
		"redirect_uri":          {client.RedirectURIs[0]},
		"scope":                 {scope},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		"resource":              {resource},
	}
	if req.User != "" {
		// AS-1 does not forward login_hint to the IdP.
		a.Harness.IdP.SignInNextAs(req.User)
	}
	back, err := a.Harness.AuthorizeWithConsent(t.Context(), a.Issuer()+"/oauth/authorize?"+q.Encode())
	if err != nil {
		t.Fatalf("as1servertest: authorize: %v", err)
	}
	if back.Get("error") != "" || back.Get("state") != state || back.Get("code") == "" {
		t.Fatalf("as1servertest: authorize redirect: error=%q description=%q state match=%v",
			back.Get("error"), back.Get("error_description"), back.Get("state") == state)
	}
	resp, err := a.Harness.HTTPClient().PostForm(a.Server.TokenEndpoint(), url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {client.ID},
		"code":          {back.Get("code")},
		"redirect_uri":  {client.RedirectURIs[0]},
		"code_verifier": {verifier},
		"resource":      {resource},
	})
	if err != nil {
		t.Fatalf("as1servertest: code exchange: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var tokens struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
		t.Fatalf("as1servertest: code exchange response: %v", err)
	}
	if resp.StatusCode != http.StatusOK || tokens.AccessToken == "" {
		t.Fatalf("as1servertest: code exchange: status %d error %q", resp.StatusCode, tokens.Error)
	}
	return identitybroker.NewToken(tokens.AccessToken)
}

// ExpiredAccessToken returns accessToken, an access token AS-1 issued, with
// its iat and exp moved an hour into the past and signed again with AS-1's
// signing key, so AS-1 verifies it and refuses it only for its expiry. It
// needs Config.SigningKeyFile to hold a key identityharness's WriteSigningKey
// wrote, as Start's does.
func (a *AS1) ExpiredAccessToken(t testing.TB, accessToken identitybroker.Token) identitybroker.Token {
	t.Helper()
	if a.signingKeyFile == "" {
		t.Fatal("as1servertest: AS-1 has no signing key file")
	}
	return identitybroker.NewToken(identityharness.ResignAccessToken(t, a.signingKeyFile, accessToken.Value(), func(claims jwt.MapClaims) {
		iat, errIat := claims.GetIssuedAt()
		exp, errExp := claims.GetExpirationTime()
		if errIat != nil || errExp != nil || iat == nil || exp == nil {
			t.Fatal("as1servertest: the access token lacks iat or exp")
		}
		end := time.Now().Add(-time.Hour)
		claims["iat"], claims["exp"] = end.Add(-exp.Sub(iat.Time)).Unix(), end.Unix()
	}))
}

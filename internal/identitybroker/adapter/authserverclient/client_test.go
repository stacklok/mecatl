package authserverclient_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/authserverclient"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

const (
	grantTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	tokenTypeAccess    = "urn:ietf:params:oauth:token-type:access_token"
	tokenTypeRefresh   = "urn:ietf:params:oauth:token-type:refresh_token"
)

// testClock is a settable clock safe to read from server goroutines.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// recorder records every assertion it mints; with replay set it returns the
// first one again instead of minting.
type recorder struct {
	signer *identitybroker.ClientAssertionSigner
	replay bool

	mu     sync.Mutex
	minted []identitybroker.Token
}

func (r *recorder) ClientAssertion(audience string, now time.Time) (identitybroker.Token, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.replay && len(r.minted) > 0 {
		return r.minted[0], nil
	}
	a, err := r.signer.ClientAssertion(audience, now)
	if err == nil {
		r.minted = append(r.minted, a)
	}
	return a, err
}

func (r *recorder) all() []identitybroker.Token {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]identitybroker.Token(nil), r.minted...)
}

type fixture struct {
	h      *identityharness.Harness
	clock  *testClock
	signer *identitybroker.ClientAssertionSigner
}

type fixtureOptions struct {
	// unregistered leaves the broker client key out of AS-1's registration.
	unregistered bool
	// maxLifetime is AS-1's largest accepted exp - iat.
	maxLifetime time.Duration
}

func newFixture(t *testing.T, opts fixtureOptions) *fixture {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := identitybroker.NewClientAssertionSigner(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Unix(1_800_000_000, 0)}
	hopts := identityharness.Options{Now: clock.Now, ClientAssertionMaxLifetime: opts.maxLifetime}
	if !opts.unregistered {
		// Register the key as a server would: from the published JWKS.
		hopts.BrokerKeys = []identityharness.BrokerKey{jwksKey(t, signer.JWKS())}
	}
	return &fixture{h: identityharness.New(t, hopts), clock: clock, signer: signer}
}

func jwksKey(t *testing.T, set identitybroker.JWKSet) identityharness.BrokerKey {
	t.Helper()
	if len(set.Keys) != 1 {
		t.Fatalf("JWKS has %d keys, want 1", len(set.Keys))
	}
	k := set.Keys[0]
	x, xErr := base64.RawURLEncoding.DecodeString(k.X)
	y, yErr := base64.RawURLEncoding.DecodeString(k.Y)
	if xErr != nil || yErr != nil {
		t.Fatalf("JWK coordinates: %v, %v", xErr, yErr)
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
	if err != nil {
		t.Fatalf("JWK key: %v", err)
	}
	return identityharness.BrokerKey{KeyID: k.KeyID, Public: pub}
}

// client returns a client for AS-1 whose assertions r records, after edit
// changes its configuration.
func (f *fixture) client(t *testing.T, r *recorder, edit func(*authserverclient.Config)) *authserverclient.Client {
	t.Helper()
	r.signer = f.signer
	cfg := authserverclient.Config{
		Issuer:             f.h.AS1.Issuer(),
		TokenEndpoint:      f.h.AS1.TokenEndpoint(),
		RevocationEndpoint: f.h.AS1.RevocationEndpoint(),
		Assertions:         r,
		HTTPClient:         f.h.HTTPClient(),
		Now:                f.clock.Now,
	}
	if edit != nil {
		edit(&cfg)
	}
	c, err := authserverclient.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// sessionOpenForm is a session-open exchange (design step (3)): one
// exchange-grant entry listing only id-jag, without client authentication.
func (*fixture) sessionOpenForm(t *testing.T, accessToken string) url.Values {
	t.Helper()
	details, err := json.Marshal([]identityharness.ExchangeGrant{{
		Type: identityharness.ExchangeGrantType, IssuableTokenTypes: []string{identityharness.TokenTypeIDJAG},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return url.Values{
		"grant_type":            {grantTokenExchange},
		"subject_token":         {accessToken},
		"subject_token_type":    {tokenTypeAccess},
		"requested_token_type":  {tokenTypeRefresh},
		"authorization_details": {string(details)},
	}
}

func (f *fixture) aliceToken() string {
	return f.h.AS1.MintAccessToken(identityharness.AccessTokenSpec{User: identityharness.Alice})
}

type sessionOpenResult struct {
	RefreshToken    string `json:"access_token"`
	IssuedTokenType string `json:"issued_token_type"`
}

// assertNoSecrets fails t if err's message carries any of secrets.
func assertNoSecrets(t *testing.T, err error, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if s != "" && strings.Contains(err.Error(), s) {
			t.Fatalf("error %q discloses a secret value", err)
		}
	}
}

func values(tokens []identitybroker.Token) []string {
	out := make([]string, len(tokens))
	for i, tok := range tokens {
		out[i] = tok.Value()
	}
	return out
}

func claims(t *testing.T, a identitybroker.Token) map[string]any { return segment(t, a, 1) }

// segment returns the JSON members of a compact JWS's header (0) or claims (1).
func segment(t *testing.T, a identitybroker.Token, i int) map[string]any {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(strings.Split(a.Value(), ".")[i])
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSessionOpenAuthenticatesWithTheBrokerKey(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	c := f.client(t, &recorder{}, nil)
	var res sessionOpenResult
	if err := c.Token(t.Context(), f.sessionOpenForm(t, f.aliceToken()), &res); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if res.RefreshToken == "" || res.IssuedTokenType != tokenTypeRefresh {
		t.Fatalf("result = %+v, want a grant refresh token", res)
	}
	grant, ok := f.h.AS1.Grant(res.RefreshToken)
	if !ok || grant.ClientID != identityharness.BrokerClientID || grant.Output != identityharness.TokenTypeIDJAG || !grant.Active {
		t.Fatalf("grant = %+v, %v; want an active id-jag grant to %s", grant, ok, identityharness.BrokerClientID)
	}
	// Check what AS-1 received, not the signer's output.
	received := f.h.AS1.ReceivedClientAssertions()
	if len(received) != 1 {
		t.Fatalf("AS-1 received %d assertions, want 1", len(received))
	}
	sent := identitybroker.NewToken(received[0])
	wantKID := f.signer.JWKS().Keys[0].KeyID
	if header := segment(t, sent, 0); len(header) != 3 || header["typ"] != "client-authentication+jwt" ||
		header["alg"] != "ES256" || header["kid"] != wantKID {
		t.Fatalf("assertion header = %v, want exactly typ client-authentication+jwt, alg ES256, and kid %q", header, wantKID)
	}
	got := claims(t, sent)
	jti, _ := got["jti"].(string)
	if got["iss"] != "mecatl" || got["sub"] != "mecatl" || got["aud"] != f.h.AS1.Issuer() ||
		got["exp"].(float64)-got["iat"].(float64) != 60 || jti == "" {
		t.Fatalf("assertion claims = %v, want iss and sub mecatl, aud AS-1's issuer, a 60s lifetime, and a jti string", got)
	}
}

func TestRevocationAuthenticates(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	c := f.client(t, &recorder{}, nil)
	var res sessionOpenResult
	if err := c.Token(t.Context(), f.sessionOpenForm(t, f.aliceToken()), &res); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if err := c.Revoke(t.Context(), url.Values{"token": {res.RefreshToken}, "token_type_hint": {"refresh_token"}}); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if grant, _ := f.h.AS1.Grant(res.RefreshToken); grant.Active || grant.Reason != "revoked" {
		t.Fatalf("grant = %+v after revocation, want revoked", grant)
	}
}

func TestClientAuthenticationRefusals(t *testing.T) {
	tests := []struct {
		name string
		opts fixtureOptions
		// edit changes the client; replay sends the first assertion twice.
		edit   func(*fixture, *authserverclient.Config)
		replay bool
	}{
		{name: "wrong audience: the token endpoint", edit: func(f *fixture, c *authserverclient.Config) { c.Issuer = f.h.AS1.TokenEndpoint() }},
		{name: "expired, past the skew", edit: func(f *fixture, c *authserverclient.Config) {
			c.Now = func() time.Time {
				return f.clock.Now().Add(-identitybroker.MaxClientAssertionLifetime - identityharness.ClientAssertionClockSkew - time.Second)
			}
		}},
		{name: "over-long", opts: fixtureOptions{maxLifetime: 30 * time.Second}},
		{name: "reused jti", replay: true},
		{name: "unregistered key", opts: fixtureOptions{unregistered: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tt.opts)
			r := &recorder{replay: tt.replay}
			c := f.client(t, r, func(cfg *authserverclient.Config) {
				if tt.edit != nil {
					tt.edit(f, cfg)
				}
			})
			token := f.aliceToken()
			if tt.replay {
				// The first use authenticates; its replay must not.
				if err := c.Revoke(t.Context(), url.Values{"token": {"unknown"}}); err != nil {
					t.Fatalf("first use: %v", err)
				}
			}
			err := c.Token(t.Context(), f.sessionOpenForm(t, token), &sessionOpenResult{})
			var oe *identitybroker.ServerRefusal
			if !errors.As(err, &oe) || oe.Status != http.StatusUnauthorized || oe.Code != "invalid_client" {
				t.Fatalf("Token = %v, want 401 invalid_client", err)
			}
			if !identitybroker.IsConfigurationFault(err) || identitybroker.IsUnavailable(err) {
				t.Fatalf("%v: configuration fault %v, unavailable %v; want a configuration fault only", err,
					identitybroker.IsConfigurationFault(err), identitybroker.IsUnavailable(err))
			}
			assertNoSecrets(t, err, append(values(r.all()), token)...)
		})
	}
}

// TestLeakedAccessTokenWithoutTheBrokerKeyIsRefused presents a stolen AS-1
// access token at the session-open exchange with no client authentication,
// and claiming to be the broker.
func TestLeakedAccessTokenWithoutTheBrokerKeyIsRefused(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	for name, edit := range map[string]func(url.Values){
		"no client":   func(url.Values) {},
		"as mecatl":   func(form url.Values) { form.Set("client_id", identityharness.BrokerClientID) },
		"public user": func(form url.Values) { form.Set("client_id", identityharness.DefaultClientID) },
	} {
		t.Run(name, func(t *testing.T) {
			form := f.sessionOpenForm(t, f.aliceToken())
			edit(form)
			resp, err := f.h.HTTPClient().PostForm(f.h.AS1.TokenEndpoint(), form)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			var body struct {
				Error string `json:"error"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&body)
			if resp.StatusCode == http.StatusOK {
				t.Fatalf("the leaked token opened a session")
			}
			if name != "public user" && (resp.StatusCode != http.StatusUnauthorized || body.Error != "invalid_client") {
				t.Fatalf("status %d %q, want 401 invalid_client", resp.StatusCode, body.Error)
			}
		})
	}
	if f.h.AS1.GrantsIssued() != 0 {
		t.Fatal("AS-1 issued a grant for a leaked access token")
	}
}

// TestResendAfterProcessedRequestGetsTokenUsed: after a lost session-open
// response from a request AS-1 processed, the one resend of the same request
// is refused with token_used, and a retry with a fresh access token converts.
// Every attempt carries a new assertion and jti.
func TestResendAfterProcessedRequestGetsTokenUsed(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	r := &recorder{}
	c := f.client(t, r, nil)
	form := f.sessionOpenForm(t, f.aliceToken())
	f.h.AS1.LoseNextExchangeResponse()
	err := c.Token(t.Context(), form, &sessionOpenResult{})
	if !errors.Is(err, identitybroker.ErrNoResponse) || !identitybroker.IsUnavailable(err) || identitybroker.IsConfigurationFault(err) {
		t.Fatalf("lost response: %v, want ErrNoResponse classified unavailable", err)
	}
	assertNoSecrets(t, err, append(values(r.all()), form.Get("subject_token"))...)

	err = c.Token(t.Context(), form, &sessionOpenResult{})
	var oe *identitybroker.ServerRefusal
	if !errors.As(err, &oe) || *oe != (identitybroker.ServerRefusal{Status: http.StatusBadRequest, Code: "invalid_request", Reason: "token_used"}) {
		t.Fatalf("resend with the same access token = %v, want 400 invalid_request token_used", err)
	}
	if identitybroker.IsConfigurationFault(err) || identitybroker.IsUnavailable(err) {
		t.Fatalf("%v is classified; token_used is for the caller to map", err)
	}

	var res sessionOpenResult
	if err := c.Token(t.Context(), f.sessionOpenForm(t, f.aliceToken()), &res); err != nil {
		t.Fatalf("retry with a fresh access token: %v", err)
	}
	minted := r.all()
	if len(minted) != 3 {
		t.Fatalf("minted %d assertions, want one per attempt", len(minted))
	}
	jtis := map[any]bool{}
	for _, a := range minted {
		jtis[claims(t, a)["jti"]] = true
	}
	if len(jtis) != 3 {
		t.Fatal("the attempts did not each send a new assertion with a new jti")
	}
	if grant, ok := f.h.AS1.Grant(res.RefreshToken); !ok || !grant.Active || f.h.AS1.GrantsIssued() != 2 {
		t.Fatalf("retry grant = %+v, %v, %d issued; want an active grant beside the orphan", grant, ok, f.h.AS1.GrantsIssued())
	}
}

// TestResendAfterUnprocessedRequestSucceeds: a session open lost before AS-1
// processed it is resent once with the same access token and a new
// assertion, and converts.
func TestResendAfterUnprocessedRequestSucceeds(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	r := &recorder{}
	c := f.client(t, r, nil)
	form := f.sessionOpenForm(t, f.aliceToken())
	f.h.AS1.DropNextExchange()
	err := c.Token(t.Context(), form, &sessionOpenResult{})
	if !errors.Is(err, identitybroker.ErrNoResponse) || !identitybroker.IsUnavailable(err) {
		t.Fatalf("dropped request: %v, want ErrNoResponse classified unavailable", err)
	}
	var res sessionOpenResult
	if err := c.Token(t.Context(), form, &res); err != nil {
		t.Fatalf("resend with the same access token: %v", err)
	}
	minted := r.all()
	if len(minted) != 2 || claims(t, minted[0])["jti"] == claims(t, minted[1])["jti"] {
		t.Fatal("the resend did not send a new assertion with a new jti")
	}
	if grant, ok := f.h.AS1.Grant(res.RefreshToken); !ok || !grant.Active || f.h.AS1.GrantsIssued() != 1 {
		t.Fatalf("resend grant = %+v, %v, %d issued; want one active grant", grant, ok, f.h.AS1.GrantsIssued())
	}
}

// TestAssertionReplayedAfterExpiryWithinSkewIsRefused replays a used
// assertion after its exp but within the clock skew AS-1 allows, where only
// the retained jti refuses it: a fresh assertion equally far past its exp is
// accepted.
func TestAssertionReplayedAfterExpiryWithinSkewIsRefused(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	replayed := f.client(t, &recorder{replay: true}, nil)
	if err := replayed.Revoke(t.Context(), url.Values{"token": {"unknown"}}); err != nil {
		t.Fatalf("first use: %v", err)
	}
	pastExp := identitybroker.MaxClientAssertionLifetime + identityharness.ClientAssertionClockSkew/2
	f.clock.Advance(pastExp)

	// Positive control: a fresh jti, the same distance past exp.
	fresh := f.client(t, &recorder{}, func(c *authserverclient.Config) {
		c.Now = func() time.Time { return f.clock.Now().Add(-pastExp) }
	})
	if err := fresh.Revoke(t.Context(), url.Values{"token": {"unknown"}}); err != nil {
		t.Fatalf("fresh assertion past exp within the skew: %v, want accepted", err)
	}

	err := replayed.Revoke(t.Context(), url.Values{"token": {"unknown"}})
	var refusal *identitybroker.ServerRefusal
	if !errors.As(err, &refusal) || refusal.Code != "invalid_client" || !identitybroker.IsConfigurationFault(err) {
		t.Fatalf("replay = %v, want invalid_client", err)
	}
}

func TestServerErrorsAreClassified(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	c := f.client(t, &recorder{}, nil)

	f.h.AS1.FailNextExchange(identityharness.Fault{Status: http.StatusServiceUnavailable, Error: "temporarily_unavailable"})
	err := c.Token(t.Context(), f.sessionOpenForm(t, f.aliceToken()), &sessionOpenResult{})
	if !identitybroker.IsUnavailable(err) || identitybroker.IsConfigurationFault(err) {
		t.Fatalf("503 = %v, want unavailable only", err)
	}

	f.h.AS1.FailNextExchange(identityharness.Fault{Error: "invalid_request", Reason: "revoked"})
	err = c.Token(t.Context(), f.sessionOpenForm(t, f.aliceToken()), &sessionOpenResult{})
	var oe *identitybroker.ServerRefusal
	if !errors.As(err, &oe) || *oe != (identitybroker.ServerRefusal{Status: http.StatusBadRequest, Code: "invalid_request", Reason: "revoked"}) {
		t.Fatalf("refusal = %#v, want 400 invalid_request revoked", err)
	}
	if identitybroker.IsConfigurationFault(err) || identitybroker.IsUnavailable(err) {
		t.Fatalf("%v is classified; a reason code is for the caller to map", err)
	}

	// A real refusal from AS-1's own checks: a token that is not AS-1's gets
	// invalid_request with no reason. The client stays generic and reports a
	// configuration fault; session open maps it to subject_invalid first.
	err = c.Token(t.Context(), f.sessionOpenForm(t, "not-a-token"), &sessionOpenResult{})
	if !errors.As(err, &oe) || *oe != (identitybroker.ServerRefusal{Status: http.StatusBadRequest, Code: "invalid_request"}) {
		t.Fatalf("refusal = %v, want 400 invalid_request with no reason", err)
	}
	if !identitybroker.IsConfigurationFault(err) || identitybroker.IsUnavailable(err) {
		t.Fatalf("%v: want a configuration fault only", err)
	}

	for _, tt := range []struct {
		fault               identityharness.Fault
		config, unavailable bool
	}{
		{identityharness.Fault{Error: "invalid_request"}, true, false},
		{identityharness.Fault{Status: http.StatusUnauthorized, Error: "invalid_client", Reason: "denied"}, true, false},
		{identityharness.Fault{Error: "unauthorized_client", Reason: "denied"}, true, false},
		{identityharness.Fault{Error: "invalid_request", Reason: "parent_revoked"}, false, false},
		{identityharness.Fault{Error: "invalid_request", Reason: "not_issuable"}, false, false},
		{identityharness.Fault{Error: "invalid_request", Reason: "superseded"}, true, false},
		{identityharness.Fault{Status: http.StatusServiceUnavailable, Error: "invalid_client"}, false, true},
	} {
		f.h.AS1.FailNextExchange(tt.fault)
		err = c.Token(t.Context(), f.sessionOpenForm(t, f.aliceToken()), &sessionOpenResult{})
		if !errors.As(err, &oe) || identitybroker.IsConfigurationFault(err) != tt.config || identitybroker.IsUnavailable(err) != tt.unavailable {
			t.Errorf("%+v: %v; want configuration fault %v, unavailable %v", tt.fault, err, tt.config, tt.unavailable)
		}
	}
}

// TestRedirectIsNotFollowed: a token endpoint redirecting elsewhere gets a
// refusal with the 3xx status, a configuration fault, and the target
// receives nothing.
func TestRedirectIsNotFollowed(t *testing.T) {
	var reached atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Add(1) }))
	t.Cleanup(target.Close)
	redirecting := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/token", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirecting.Close)
	// httptest servers share one certificate, so this client trusts both.
	hc := redirecting.Client()
	c := newStubClient(t, redirecting.URL, hc)
	err := c.Token(t.Context(), url.Values{"grant_type": {"x"}}, &sessionOpenResult{})
	var refusal *identitybroker.ServerRefusal
	if !errors.As(err, &refusal) || refusal.Status != http.StatusTemporaryRedirect || !identitybroker.IsConfigurationFault(err) {
		t.Fatalf("Token = %v, want a 307 refusal classified as a configuration fault", err)
	}
	if n := reached.Load(); n != 0 {
		t.Fatalf("the redirect target received %d requests", n)
	}
	if hc.CheckRedirect != nil {
		t.Fatal("New changed the caller's HTTP client")
	}
}

func TestOversizedResponseIsRefused(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + strings.Repeat("a", 1<<20) + `"}`))
	}))
	t.Cleanup(server.Close)
	c := newStubClient(t, server.URL, server.Client())
	if err := c.Token(t.Context(), url.Values{"grant_type": {"x"}}, &sessionOpenResult{}); !errors.Is(err, authserverclient.ErrResponseTooLarge) {
		t.Fatalf("Token = %v, want ErrResponseTooLarge", err)
	}
}

// newStubClient returns a client for a stand-in server at base.
func newStubClient(t *testing.T, base string, hc *http.Client) *authserverclient.Client {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := identitybroker.NewClientAssertionSigner(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	c, err := authserverclient.New(authserverclient.Config{Issuer: base, TokenEndpoint: base + "/token", Assertions: signer, HTTPClient: hc})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFormMustNotCarryClientCredentials(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	r := &recorder{}
	c := f.client(t, r, nil)
	for _, name := range []string{"client_secret", "client_id", "client_assertion", "client_assertion_type"} {
		if err := c.Revoke(t.Context(), url.Values{"token": {"x"}, name: {"v"}}); err == nil {
			t.Errorf("a form carrying %s was sent", name)
		}
	}
	if len(r.all()) != 0 {
		t.Fatal("assertions were minted for refused forms")
	}
}

func TestNewValidatesConfig(t *testing.T) {
	signer := &recorder{}
	good := authserverclient.Config{Issuer: "https://as.example.com", TokenEndpoint: "https://as.example.com/token", Assertions: signer, HTTPClient: &http.Client{}}
	if _, err := authserverclient.New(good); err != nil {
		t.Fatalf("New(good): %v", err)
	}
	for name, edit := range map[string]func(*authserverclient.Config){
		"no issuer":           func(c *authserverclient.Config) { c.Issuer = "" },
		"no signer":           func(c *authserverclient.Config) { c.Assertions = nil },
		"no HTTP client":      func(c *authserverclient.Config) { c.HTTPClient = nil },
		"http token endpoint": func(c *authserverclient.Config) { c.TokenEndpoint = "http://as.example.com/token" },
		"userinfo":            func(c *authserverclient.Config) { c.TokenEndpoint = "https://u:p@as.example.com/token" },
		"http revocation":     func(c *authserverclient.Config) { c.RevocationEndpoint = "http://as.example.com/revoke" },
	} {
		cfg := good
		edit(&cfg)
		if _, err := authserverclient.New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// echoingTransport fails every request with an error quoting a secret and
// the request body, as a careless custom transport might.
type echoingTransport struct{}

func (echoingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	return nil, errors.New("upstream said: Bearer sk_live_secret_canary; request was " + string(body))
}

// blockingTransport answers nothing until the request is abandoned.
type blockingTransport struct{}

func (blockingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	<-req.Context().Done()
	return nil, req.Context().Err()
}

func TestTransportErrorTextIsNeverReturned(t *testing.T) {
	r := &recorder{}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	r.signer, err = identitybroker.NewClientAssertionSigner(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	c, err := authserverclient.New(authserverclient.Config{
		Issuer: "https://as.example.com", TokenEndpoint: "https://as.example.com/token",
		Assertions: r, HTTPClient: &http.Client{Transport: echoingTransport{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	const subject = "subject-token-canary"
	err = c.Token(t.Context(), url.Values{"subject_token": {subject}}, &sessionOpenResult{})
	if !errors.Is(err, identitybroker.ErrNoResponse) || !strings.HasSuffix(err.Error(), ": other") {
		t.Fatalf("Token = %v, want ErrNoResponse of kind other", err)
	}
	assertNoSecrets(t, err, append(values(r.all()), subject, "sk_live_secret_canary", "upstream said")...)
}

func TestTransportTimeoutIsNamed(t *testing.T) {
	c := newStubClient(t, "https://as.example.com", &http.Client{Transport: blockingTransport{}, Timeout: 50 * time.Millisecond})
	err := c.Token(t.Context(), url.Values{"grant_type": {"x"}}, &sessionOpenResult{})
	if !errors.Is(err, identitybroker.ErrNoResponse) || !identitybroker.IsUnavailable(err) || !strings.HasSuffix(err.Error(), ": timeout") {
		t.Fatalf("Token = %v, want ErrNoResponse of kind timeout", err)
	}
}

func TestTransportFailureAgainstAClosedPort(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	hc, base := server.Client(), server.URL
	server.Close()
	err := newStubClient(t, base, hc).Token(t.Context(), url.Values{"grant_type": {"x"}}, &sessionOpenResult{})
	if !errors.Is(err, identitybroker.ErrNoResponse) || !strings.HasSuffix(err.Error(), ": connection refused") {
		t.Fatalf("Token = %v, want ErrNoResponse of kind connection refused", err)
	}
}

// TestCertificateFailuresAreConfigurationFaults: a server certificate that
// does not verify is a configuration fault, never a retryable outage.
func TestCertificateFailuresAreConfigurationFaults(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	untrusted := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: x509.NewCertPool(), MinVersion: tls.VersionTLS12}}}
	// The httptest certificate names example.com and the loopback addresses,
	// not localhost.
	wrongName := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	for name, tt := range map[string]struct {
		base string
		hc   *http.Client
	}{
		"untrusted CA":      {server.URL, untrusted},
		"hostname mismatch": {wrongName, server.Client()},
	} {
		t.Run(name, func(t *testing.T) {
			err := newStubClient(t, tt.base, tt.hc).Token(t.Context(), url.Values{"grant_type": {"x"}}, &sessionOpenResult{})
			if !errors.Is(err, identitybroker.ErrTransportConfiguration) || errors.Is(err, identitybroker.ErrNoResponse) ||
				!identitybroker.IsConfigurationFault(err) || identitybroker.IsUnavailable(err) {
				t.Fatalf("Token = %v, want ErrTransportConfiguration, a configuration fault only", err)
			}
		})
	}
}

// TestCancelDuringBodyReadReturnsTheContextError: a request abandoned while
// the response body is arriving is the caller's cancellation, not a lost
// response.
func TestCancelDuringBodyReadReturnsTheContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// The response arrives; the caller cancels while its body is read.
	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: cancelingBody{cancel}}, nil
	})}
	err := newStubClient(t, "https://as.example.com", hc).Token(ctx, url.Values{"grant_type": {"x"}}, &sessionOpenResult{})
	if !errors.Is(err, context.Canceled) || errors.Is(err, identitybroker.ErrNoResponse) || identitybroker.IsUnavailable(err) {
		t.Fatalf("Token = %v, want context.Canceled, not a lost response", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// cancelingBody cancels the request's context on its first read and fails
// that read, as a body interrupted by the caller's cancellation does.
type cancelingBody struct{ cancel context.CancelFunc }

func (b cancelingBody) Read([]byte) (int, error) {
	b.cancel()
	return 0, errors.New("read interrupted")
}

func (cancelingBody) Close() error { return nil }

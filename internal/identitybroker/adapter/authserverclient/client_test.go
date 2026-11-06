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

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server/as1servertest"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/authserverclient"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

const (
	grantTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	tokenTypeAccess    = "urn:ietf:params:oauth:token-type:access_token"
	tokenTypeRefresh   = "urn:ietf:params:oauth:token-type:refresh_token"
)

// recorder records every assertion it mints; with replay set it returns the
// first one again instead of minting.
type recorder struct {
	signer authserverclient.Assertions
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

// startAS1 runs the real AS-1 with the harness IdP as its upstream, for the
// session-open tests.
func startAS1(t *testing.T) *as1servertest.AS1 {
	t.Helper()
	return as1servertest.Start(t, identityharness.New(t, identityharness.Options{}), nil)
}

// as1Client returns a client for the real AS-1 a, reaching it through a's
// fault transport, whose assertions r records, after edit changes its
// configuration. r mints with a's registered broker key unless it already
// has a signer.
func as1Client(t *testing.T, a *as1servertest.AS1, r *recorder, edit func(*authserverclient.Config)) *authserverclient.Client {
	t.Helper()
	if r.signer == nil {
		r.signer = a.Signer
	}
	cfg := authserverclient.Config{
		Issuer:        a.Issuer(),
		TokenEndpoint: a.Server.TokenEndpoint(),
		Assertions:    r,
		HTTPClient:    a.HTTPClient(),
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

// aliceSignsIn signs Alice in at the real AS-1 a and returns her access token.
func aliceSignsIn(t *testing.T, a *as1servertest.AS1) string {
	t.Helper()
	return a.SignIn(t, as1servertest.SignIn{User: identityharness.Alice}).Value()
}

// fixture is the harness's AS-1, kept for revocation, which the real AS-1
// does not serve yet: retired by stacklok/toolhive#6966 and
// stacklok/toolhive#6972 (mecatl #2568).
type fixture struct {
	h      *identityharness.Harness
	signer *identitybroker.ClientAssertionSigner
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := identitybroker.NewClientAssertionSigner(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Register the key as a server would: from the published JWKS.
	h := identityharness.New(t, identityharness.Options{BrokerKeys: []identityharness.BrokerKey{jwksKey(t, signer.JWKS())}})
	return &fixture{h: h, signer: signer}
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
func sessionOpenForm(t *testing.T, accessToken string) url.Values {
	t.Helper()
	details, err := json.Marshal([]as1servertest.ExchangeGrant{{
		Type: identitybroker.ExchangeGrantType, IssuableTokenTypes: []string{identitybroker.TokenTypeIDJAG},
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
	a := startAS1(t)
	c := as1Client(t, a, &recorder{}, nil)
	var res sessionOpenResult
	if err := c.Token(t.Context(), sessionOpenForm(t, aliceSignsIn(t, a)), &res); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if res.RefreshToken == "" || res.IssuedTokenType != tokenTypeRefresh {
		t.Fatalf("result refresh token present %v, type %q; want a grant refresh token", res.RefreshToken != "", res.IssuedTokenType)
	}
	if got := a.Faults.Conversions(); got != 1 {
		t.Fatalf("AS-1 converted %d tokens, want 1", got)
	}
	// Check what AS-1 received, not the signer's output.
	received := a.Faults.Requests()
	if len(received) != 1 || received[0].ClientAssertion.IsZero() {
		t.Fatalf("AS-1 received %d requests, want 1 with a client assertion", len(received))
	}
	sent := received[0].ClientAssertion
	wantKID := a.Signer.JWKS().Keys[0].KeyID
	if header := segment(t, sent, 0); len(header) != 3 || header["typ"] != "client-authentication+jwt" ||
		header["alg"] != "ES256" || header["kid"] != wantKID {
		t.Fatalf("assertion header = %v, want exactly typ client-authentication+jwt, alg ES256, and kid %q", header, wantKID)
	}
	got := claims(t, sent)
	jti, _ := got["jti"].(string)
	if got["iss"] != "mecatl" || got["sub"] != "mecatl" || got["aud"] != a.Issuer() ||
		got["exp"].(float64)-got["iat"].(float64) != 60 || jti == "" {
		t.Fatalf("assertion claims = %v, want iss and sub mecatl, aud AS-1's issuer, a 60s lifetime, and a jti string", got)
	}
}

// TestRevocationAuthenticates stays on the harness's AS-1, as the real AS-1
// serves no revocation yet: retired by stacklok/toolhive#6966 and
// stacklok/toolhive#6972 (mecatl #2568).
func TestRevocationAuthenticates(t *testing.T) {
	f := newFixture(t)
	c := f.client(t, &recorder{}, nil)
	var res sessionOpenResult
	if err := c.Token(t.Context(), sessionOpenForm(t, f.aliceToken()), &res); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if err := c.Revoke(t.Context(), url.Values{"token": {res.RefreshToken}, "token_type_hint": {"refresh_token"}}); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if grant, _ := f.h.AS1.Grant(res.RefreshToken); grant.Active || grant.Reason != "revoked" {
		t.Fatalf("grant = %+v after revocation, want revoked", grant)
	}
}

// overLongAssertions mints assertions with the registered broker key that
// live ten minutes, past the five minutes AS-1 accepts; the broker's own
// signer cannot mint them.
type overLongAssertions struct {
	key *ecdsa.PrivateKey
	kid string
}

func (o overLongAssertions) ClientAssertion(audience string, now time.Time) (identitybroker.Token, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": identitybroker.BrokerClientID, "sub": identitybroker.BrokerClientID, "aud": audience,
		"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(), "jti": rand.Text(),
	})
	token.Header["typ"], token.Header["kid"] = "client-authentication+jwt", o.kid
	signed, err := token.SignedString(o.key)
	return identitybroker.NewToken(signed), err
}

func TestClientAuthenticationRefusals(t *testing.T) {
	unregistered, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	unregisteredSigner, err := identitybroker.NewClientAssertionSigner(unregistered, 0)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		// signer replaces the registered broker key's signer.
		signer func(*as1servertest.AS1) authserverclient.Assertions
		// edit changes the client; replay sends the first assertion twice.
		edit   func(*as1servertest.AS1, *authserverclient.Config)
		replay bool
	}{
		// AS-1 also accepts its token endpoint as the audience (RFC 7523).
		{name: "wrong audience", edit: func(_ *as1servertest.AS1, c *authserverclient.Config) { c.Issuer = "https://not-as1.example" }},
		{name: "expired", edit: func(_ *as1servertest.AS1, c *authserverclient.Config) {
			// Expired 2-3s ago (iat truncates to the second): AS-1 (fosite) allows no leeway on assertion expiry.
			c.Now = func() time.Time { return time.Now().Add(-identitybroker.MaxClientAssertionLifetime - 2*time.Second) }
		}},
		{name: "over-long", signer: func(a *as1servertest.AS1) authserverclient.Assertions {
			return overLongAssertions{key: a.Key, kid: a.Signer.JWKS().Keys[0].KeyID}
		}},
		{name: "reused jti", replay: true},
		{name: "unregistered key", signer: func(*as1servertest.AS1) authserverclient.Assertions { return unregisteredSigner }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := startAS1(t)
			r := &recorder{replay: tt.replay}
			if tt.signer != nil {
				r.signer = tt.signer(a)
			}
			c := as1Client(t, a, r, func(cfg *authserverclient.Config) {
				if tt.edit != nil {
					tt.edit(a, cfg)
				}
			})
			if tt.replay {
				// The first use authenticates; its replay must not.
				if err := c.Token(t.Context(), sessionOpenForm(t, aliceSignsIn(t, a)), &sessionOpenResult{}); err != nil {
					t.Fatalf("first use: %v", err)
				}
			}
			token := aliceSignsIn(t, a)
			err := c.Token(t.Context(), sessionOpenForm(t, token), &sessionOpenResult{})
			var oe *identitybroker.ServerRefusal
			if !errors.As(err, &oe) || oe.Status != http.StatusUnauthorized || oe.Code != "invalid_client" {
				t.Fatalf("Token = %v, want 401 invalid_client", err)
			}
			if !identitybroker.IsConfigurationFault(err) || identitybroker.IsUnavailable(err) {
				t.Fatalf("%v: configuration fault %v, unavailable %v; want a configuration fault only", err,
					identitybroker.IsConfigurationFault(err), identitybroker.IsUnavailable(err))
			}
			assertNoSecrets(t, err, append(values(r.all()), token)...)
			if last := a.Faults.Requests(); !last[len(last)-1].Refused() {
				t.Fatal("the refusal is not AS-1's own")
			}
		})
	}
}

// TestLeakedAccessTokenWithoutTheBrokerKeyIsRefused presents a stolen AS-1
// access token at the session-open exchange with no client authentication,
// and claiming to be the broker.
func TestLeakedAccessTokenWithoutTheBrokerKeyIsRefused(t *testing.T) {
	a := startAS1(t)
	// Without a client_id AS-1 answers invalid_request; claiming the broker
	// without its assertion fails client authentication. A zero want only
	// requires a refusal.
	type want struct {
		status int
		code   string
	}
	for name, tt := range map[string]struct {
		edit func(url.Values)
		want want
	}{
		"no client":   {func(url.Values) {}, want{http.StatusBadRequest, "invalid_request"}},
		"as mecatl":   {func(form url.Values) { form.Set("client_id", identitybroker.BrokerClientID) }, want{http.StatusUnauthorized, "invalid_client"}},
		"public user": {func(form url.Values) { form.Set("client_id", as1servertest.PublicClientID) }, want{}},
	} {
		t.Run(name, func(t *testing.T) {
			form := sessionOpenForm(t, aliceSignsIn(t, a))
			tt.edit(form)
			resp, err := a.HTTPClient().PostForm(a.Server.TokenEndpoint(), form)
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
			if tt.want != (want{}) && (resp.StatusCode != tt.want.status || body.Error != tt.want.code) {
				t.Fatalf("status %d %q, want %d %s", resp.StatusCode, body.Error, tt.want.status, tt.want.code)
			}
		})
	}
	if a.Faults.Conversions() != 0 {
		t.Fatal("AS-1 converted a leaked access token")
	}
}

// TestResendAfterProcessedRequestGetsTokenUsed: after a lost session-open
// response from a request AS-1 processed, the one resend of the same request
// is refused with token_used, and a retry with a fresh access token converts.
// Every attempt carries a new assertion and jti.
func TestResendAfterProcessedRequestGetsTokenUsed(t *testing.T) {
	a := startAS1(t)
	r := &recorder{}
	c := as1Client(t, a, r, nil)
	form := sessionOpenForm(t, aliceSignsIn(t, a))
	a.Faults.LoseNextExchangeResponse()
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
	if err := c.Token(t.Context(), sessionOpenForm(t, aliceSignsIn(t, a)), &res); err != nil {
		t.Fatalf("retry with a fresh access token: %v", err)
	}
	minted := r.all()
	if len(minted) != 3 {
		t.Fatalf("minted %d assertions, want one per attempt", len(minted))
	}
	jtis := map[any]bool{}
	for _, m := range minted {
		jtis[claims(t, m)["jti"]] = true
	}
	if len(jtis) != 3 {
		t.Fatal("the attempts did not each send a new assertion with a new jti")
	}
	// The lost response's conversion is an orphan beside the retry's.
	if res.RefreshToken == "" || a.Faults.Conversions() != 2 {
		t.Fatalf("retry refresh token present %v, %d conversions; want the retry's grant beside the orphan", res.RefreshToken != "", a.Faults.Conversions())
	}
}

// TestResendAfterUnprocessedRequestSucceeds: a session open lost before AS-1
// processed it is resent once with the same access token and a new
// assertion, and converts.
func TestResendAfterUnprocessedRequestSucceeds(t *testing.T) {
	a := startAS1(t)
	r := &recorder{}
	c := as1Client(t, a, r, nil)
	form := sessionOpenForm(t, aliceSignsIn(t, a))
	a.Faults.DropNextExchange()
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
	if res.RefreshToken == "" || a.Faults.Conversions() != 1 {
		t.Fatalf("resend refresh token present %v, %d conversions; want the one grant", res.RefreshToken != "", a.Faults.Conversions())
	}
}

func TestServerErrorsAreClassified(t *testing.T) {
	a := startAS1(t)
	c := as1Client(t, a, &recorder{}, nil)

	// The fault transport answers these instead of AS-1, so their subject
	// token is never sent.
	a.Faults.FailNextExchange(as1servertest.Fault{Status: http.StatusServiceUnavailable, Error: "temporarily_unavailable"})
	err := c.Token(t.Context(), sessionOpenForm(t, "never-sent"), &sessionOpenResult{})
	if !identitybroker.IsUnavailable(err) || identitybroker.IsConfigurationFault(err) {
		t.Fatalf("503 = %v, want unavailable only", err)
	}

	a.Faults.FailNextExchange(as1servertest.Fault{Error: "invalid_request", Reason: "revoked"})
	err = c.Token(t.Context(), sessionOpenForm(t, "never-sent"), &sessionOpenResult{})
	var oe *identitybroker.ServerRefusal
	if !errors.As(err, &oe) || *oe != (identitybroker.ServerRefusal{Status: http.StatusBadRequest, Code: "invalid_request", Reason: "revoked"}) {
		t.Fatalf("refusal = %#v, want 400 invalid_request revoked", err)
	}
	if identitybroker.IsConfigurationFault(err) || identitybroker.IsUnavailable(err) {
		t.Fatalf("%v is classified; a reason code is for the caller to map", err)
	}

	// A real refusal from AS-1's own checks: a token that is not AS-1's gets
	// invalid_request with subject_token_invalid, a reason for the caller,
	// which session open maps to subject_invalid.
	err = c.Token(t.Context(), sessionOpenForm(t, "not-a-token"), &sessionOpenResult{})
	if !errors.As(err, &oe) || *oe != (identitybroker.ServerRefusal{Status: http.StatusBadRequest, Code: "invalid_request", Reason: "subject_token_invalid"}) {
		t.Fatalf("refusal = %v, want 400 invalid_request subject_token_invalid", err)
	}
	if identitybroker.IsConfigurationFault(err) || identitybroker.IsUnavailable(err) {
		t.Fatalf("%v is classified; a reason code is for the caller to map", err)
	}

	for _, tt := range []struct {
		fault               as1servertest.Fault
		config, unavailable bool
	}{
		{as1servertest.Fault{Error: "invalid_request"}, true, false},
		{as1servertest.Fault{Status: http.StatusUnauthorized, Error: "invalid_client", Reason: "denied"}, true, false},
		{as1servertest.Fault{Error: "unauthorized_client", Reason: "denied"}, true, false},
		{as1servertest.Fault{Error: "invalid_request", Reason: "parent_revoked"}, false, false},
		{as1servertest.Fault{Error: "invalid_request", Reason: "not_issuable"}, false, false},
		{as1servertest.Fault{Error: "invalid_request", Reason: "superseded"}, true, false},
		{as1servertest.Fault{Error: "invalid_request", Reason: "subject_token_audience"}, true, false},
		{as1servertest.Fault{Error: "invalid_request", Reason: "subject_token_source_client"}, true, false},
		{as1servertest.Fault{Error: "invalid_request", Reason: "grant_invalid"}, true, false},
		{as1servertest.Fault{Status: http.StatusServiceUnavailable, Error: "invalid_client"}, false, true},
	} {
		a.Faults.FailNextExchange(tt.fault)
		err = c.Token(t.Context(), sessionOpenForm(t, "never-sent"), &sessionOpenResult{})
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
	err := c.Token(t.Context(), url.Values{"grant_type": {"x"}}, &sessionOpenResult{})
	if !errors.Is(err, authserverclient.ErrResponseTooLarge) || !errors.Is(err, identitybroker.ErrMalformedResponse) ||
		!errors.Is(err, identitybroker.ErrTransportConfiguration) {
		t.Fatalf("Token = %v, want ErrResponseTooLarge, a malformed response, and a configuration fault", err)
	}
	assertNoSecrets(t, err, strings.Repeat("a", 64))
}

// TestOversizedRefusalKeepsItsStatus: an oversized error body is not read,
// but its status still classifies the refusal.
func TestOversizedRefusalKeepsItsStatus(t *testing.T) {
	body := `{"error":"server_error","error_description":"` + strings.Repeat("b", 1<<20) + `"}`
	for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(server.Close)
			c := newStubClient(t, server.URL, server.Client())
			err := c.Token(t.Context(), url.Values{"grant_type": {"x"}}, &sessionOpenResult{})
			var refusal *identitybroker.ServerRefusal
			if !errors.As(err, &refusal) || refusal.Status != status || refusal.Code != "" || !identitybroker.IsUnavailable(err) {
				t.Fatalf("Token = %v, want an unavailable %d refusal with no code", err, status)
			}
			assertNoSecrets(t, err, strings.Repeat("b", 64))
		})
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

// TestFormMustNotCarryClientCredentials revokes, so it stays on the harness's
// AS-1: retired by stacklok/toolhive#6966 and stacklok/toolhive#6972.
func TestFormMustNotCarryClientCredentials(t *testing.T) {
	f := newFixture(t)
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

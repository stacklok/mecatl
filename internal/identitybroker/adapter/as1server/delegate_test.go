package as1server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/authserverclient"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

const (
	grantTypeTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	tokenTypeAccessToken   = "urn:ietf:params:oauth:token-type:access_token"
	clientAssertionType    = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
)

// exchangeForm is a raw session-open exchange form authenticated with
// assertion for subject, issuing only ID-JAGs.
func exchangeForm(assertion, subject string) url.Values {
	details, _ := json.Marshal([]map[string]any{{
		"type":                 identitybroker.ExchangeGrantType,
		"issuable_token_types": []string{identitybroker.TokenTypeIDJAG},
	}})
	return url.Values{
		"grant_type":            {grantTypeTokenExchange},
		"subject_token":         {subject},
		"subject_token_type":    {tokenTypeAccessToken},
		"requested_token_type":  {identitybroker.TokenTypeRefreshToken},
		"authorization_details": {string(details)},
		"client_assertion_type": {clientAssertionType},
		"client_assertion":      {assertion},
	}
}

// postToken posts form to AS-1's token endpoint in process and returns the
// status and the response's error code.
func postToken(t *testing.T, server *Server, form url.Values) (int, string) {
	t.Helper()
	resp, err := server.InProcessClient().PostForm(server.TokenEndpoint(), form)
	if err != nil {
		t.Fatalf("token POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body.Error
}

// signAssertion signs claims as a client assertion with key, naming kid.
func signAssertion(t *testing.T, key *ecdsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tok.Header["typ"] = "client-authentication+jwt"
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

// delegateMaxLifetime is the ID-JAG grant lifetime the delegate fixtures cap at.
const delegateMaxLifetime = time.Hour

// startDelegateAS1 hosts AS-1 on loopback with a delegate client whose keys are
// a fresh broker key, after edit tweaks the Config and the DelegateClient. It
// returns the fixture, a client-assertion signer for that key, and the private
// key for signing raw assertions.
func startDelegateAS1(t *testing.T, h *identityharness.Harness, edit func(*Config, *DelegateClient)) (*fixture, *identitybroker.ClientAssertionSigner, *ecdsa.PrivateKey) {
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
	f := startAS1(t, h, func(c *Config) {
		d := DelegateClient{
			ID:                   identitybroker.BrokerClientID,
			KeySet:               keySet,
			AllowedSourceClients: []string{testClientID},
			IDJAGMaxLifetime:     delegateMaxLifetime,
		}
		if edit != nil {
			edit(c, &d)
		}
		c.Delegate = &d
	})
	return f, signer, key
}

// delegateClient builds the broker's authorization-server client for f,
// authenticating as the delegate with signer and reaching AS-1 in process.
func delegateClient(t *testing.T, f *fixture, signer *identitybroker.ClientAssertionSigner) *authserverclient.Client {
	t.Helper()
	c, err := authserverclient.New(authserverclient.Config{
		Issuer:        f.server.Issuer(),
		TokenEndpoint: f.server.TokenEndpoint(),
		Assertions:    signer,
		HTTPClient:    f.server.InProcessClient(),
	})
	if err != nil {
		t.Fatalf("authserverclient.New: %v", err)
	}
	return c
}

// idjagGrant is the session-open exchange grant: one entry issuing only ID-JAGs.
func idjagGrant() identitybroker.ExchangeGrant {
	return identitybroker.ExchangeGrant{Type: identitybroker.ExchangeGrantType, IssuableTokenTypes: []string{identitybroker.TokenTypeIDJAG}}
}

func TestDelegateConvertsASignIn(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	f, signer, _ := startDelegateAS1(t, h, nil)
	c := delegateClient(t, f, signer)

	access := f.signIn(t, testAudience).AccessToken
	now := time.Now()
	res, err := c.Exchange(t.Context(), identitybroker.TokenExchange{
		SubjectToken: identitybroker.NewToken(access),
		Grant:        idjagGrant(),
	})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if res.RefreshToken.IsZero() || res.IssuedTokenType != identitybroker.TokenTypeRefreshToken {
		t.Fatalf("result = %+v, want a refresh token", res)
	}
	if len(res.Grants) != 1 {
		t.Fatalf("echoed %d grants, want 1", len(res.Grants))
	}
	exp := res.Grants[0].ExpiresAt
	// The request asks for no expiry, so AS-1 applies the configured cap.
	if exp.Before(now.Add(delegateMaxLifetime-5*time.Second)) || exp.After(now.Add(delegateMaxLifetime+5*time.Second)) {
		t.Errorf("grant expiry %v, want now + %v within 5s", exp, delegateMaxLifetime)
	}
}

func TestDelegateRefusesASpentSubjectToken(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	f, signer, _ := startDelegateAS1(t, h, nil)
	c := delegateClient(t, f, signer)

	req := identitybroker.TokenExchange{SubjectToken: identitybroker.NewToken(f.signIn(t, testAudience).AccessToken), Grant: idjagGrant()}
	if _, err := c.Exchange(t.Context(), req); err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	_, err := c.Exchange(t.Context(), req)
	var refusal *identitybroker.ServerRefusal
	if !errors.As(err, &refusal) || refusal.Reason != "token_used" {
		t.Fatalf("second exchange = %v, want a refusal with token_used", err)
	}
}

func TestDelegateRefusesAnUnlistedSourceClient(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	second := PublicClient{ID: "studio", RedirectURIs: []string{"http://127.0.0.1:18474/callback"}}
	f, signer, _ := startDelegateAS1(t, h, func(c *Config, _ *DelegateClient) {
		// second signs in but stays out of AllowedSourceClients.
		c.PublicClients = append(c.PublicClients, second)
	})
	c := delegateClient(t, f, signer)

	access := f.signInAs(t, second, testAudience).AccessToken
	_, err := c.Exchange(t.Context(), identitybroker.TokenExchange{SubjectToken: identitybroker.NewToken(access), Grant: idjagGrant()})
	var refusal *identitybroker.ServerRefusal
	if !errors.As(err, &refusal) || refusal.Reason != "subject_token_source_client" {
		t.Fatalf("Exchange = %v, want a refusal with subject_token_source_client", err)
	}
}

func TestDelegateRefusesBadAssertions(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	f, signer, key := startDelegateAS1(t, h, nil)
	kid := signer.JWKS().Keys[0].KeyID
	issuer := f.server.Issuer()
	unregistered, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	base := func() jwt.MapClaims {
		n := time.Now()
		return jwt.MapClaims{
			"iss": identitybroker.BrokerClientID, "sub": identitybroker.BrokerClientID, "aud": issuer,
			"iat": n.Unix(), "exp": n.Add(time.Minute).Unix(), "jti": rand.Text(),
		}
	}
	foreignAud := base()
	foreignAud["aud"] = "https://not-as1.example/"
	expired := base()
	n := time.Now()
	expired["iat"], expired["exp"] = n.Add(-10*time.Minute).Unix(), n.Add(-9*time.Minute).Unix()
	tooLong := base()
	tooLong["exp"] = time.Now().Add(10 * time.Minute).Unix()

	// Every failed client assertion is 401 invalid_client (the design's step
	// (5), and ToolHive since v0.61.0).
	wantInvalidClient := func(t *testing.T, assertion string) {
		t.Helper()
		status, errCode := postToken(t, f.server, exchangeForm(assertion, f.signIn(t, testAudience).AccessToken))
		if status != http.StatusUnauthorized || errCode != "invalid_client" {
			t.Fatalf("status %d error %q, want 401 invalid_client", status, errCode)
		}
	}
	for name, assertion := range map[string]func() string{
		"expired":               func() string { return signAssertion(t, key, kid, expired) },
		"exp over five minutes": func() string { return signAssertion(t, key, kid, tooLong) },
		"wrong audience":        func() string { return signAssertion(t, key, kid, foreignAud) },
		"key not registered":    func() string { return signAssertion(t, unregistered, kid, base()) },
	} {
		t.Run(name, func(t *testing.T) { wantInvalidClient(t, assertion()) })
	}

	t.Run("reused jti", func(t *testing.T) {
		assertion := signAssertion(t, key, kid, base())
		if status, errCode := postToken(t, f.server, exchangeForm(assertion, f.signIn(t, testAudience).AccessToken)); status != http.StatusOK {
			t.Fatalf("first use status %d error %q, want 200", status, errCode)
		}
		wantInvalidClient(t, assertion)
	})
}

func TestDelegateCannotObtainAPIAccessTokens(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	f, signer, _ := startDelegateAS1(t, h, nil)

	// A plain RFC 8693 request for an access token, with and without naming
	// AS-1's audience: neither may succeed (stacklok/toolhive#7026).
	for name, audience := range map[string]string{"default audience": "", "AS-1's audience": testAudience} {
		t.Run(name, func(t *testing.T) {
			assertion, err := signer.ClientAssertion(f.server.Issuer(), time.Now())
			if err != nil {
				t.Fatalf("ClientAssertion: %v", err)
			}
			form := exchangeForm(assertion.Value(), f.signIn(t, testAudience).AccessToken)
			form.Del("authorization_details")
			form.Set("requested_token_type", tokenTypeAccessToken)
			if audience != "" {
				form.Set("audience", audience)
			}
			if status, errCode := postToken(t, f.server, form); status == http.StatusOK {
				t.Fatalf("the delegate obtained an access token: status %d error %q", status, errCode)
			}
		})
	}
}

func TestDelegateRequiresOfflineAccess(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	f, signer, _ := startDelegateAS1(t, h, nil)
	c := delegateClient(t, f, signer)

	access := f.signInWithScope(t, PublicClient{ID: testClientID, RedirectURIs: []string{testRedirect}}, testAudience, "openid").AccessToken
	_, err := c.Exchange(t.Context(), identitybroker.TokenExchange{SubjectToken: identitybroker.NewToken(access), Grant: idjagGrant()})
	var refusal *identitybroker.ServerRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Exchange = %v, want a refusal for a subject token without offline_access", err)
	}
}

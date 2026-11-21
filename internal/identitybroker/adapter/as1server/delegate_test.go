package as1server

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/authserverclient"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/brokerconfig/brokerconfigtest"
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
	status, code, _ := postTokenDescribed(t, server, form)
	return status, code
}

// postTokenDescribed is postToken that also returns the error description.
func postTokenDescribed(t *testing.T, server *Server, form url.Values) (status int, code, description string) {
	t.Helper()
	resp, err := server.InProcessClient().PostForm(server.TokenEndpoint(), form)
	if err != nil {
		t.Fatalf("token POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body.Error, body.Description
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

// newDelegate returns the broker's delegate client keyed by a fresh broker
// key, allowing the test client with delegateMaxLifetime, and a
// client-assertion signer for that key and the key itself.
func newDelegate(t *testing.T) (DelegateClient, *identitybroker.ClientAssertionSigner, *ecdsa.PrivateKey) {
	t.Helper()
	key := brokerconfigtest.NewKey(t)
	signer, err := identitybroker.NewClientAssertionSigner(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	return DelegateClient{
		ID:                   identitybroker.BrokerClientID,
		KeySetFile:           brokerconfigtest.WriteKeySet(t, filepath.Join(t.TempDir(), "jwks.json"), key),
		AllowedSourceClients: []string{testClientID},
		IDJAGMaxLifetime:     delegateMaxLifetime,
	}, signer, key
}

// TestDelegateKeySetFileIsCheckedBeforeConstruction: New refuses a delegate
// key set file that is relative, absent, not a JWK Set, or holds no key, in
// validation, before it builds anything, and without the file's contents. An
// absent file's error still matches fs.ErrNotExist.
func TestDelegateKeySetFileIsCheckedBeforeConstruction(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for name, test := range map[string]struct{ file, want string }{
		"relative":       {file: "jwks.json", want: "is not an absolute path"},
		"absent":         {file: filepath.Join(dir, "absent.json"), want: "no such file"},
		"not a JWK Set":  {file: write("array.json", `["secret-shaped"]`), want: "is not a JSON JWK Set"},
		"malformed JSON": {file: write("malformed.json", `{"keys":`), want: "is not a JSON JWK Set"},
		"empty JWK Set":  {file: write("empty.json", `{"keys":[]}`), want: "holds no key"},
	} {
		t.Run(name, func(t *testing.T) {
			d, _, _ := newDelegate(t)
			d.KeySetFile = test.file
			cfg := Config{
				Issuer: "https://as1.mecatl.test/as1", Audience: testAudience,
				PublicClients: []PublicClient{{ID: testClientID, RedirectURIs: []string{testRedirect}}},
				Upstream:      UpstreamConfig{Issuer: "https://idp.example", ClientID: "mecatl-as1"},
				Delegate:      &d,
			}
			// The upstream is never reached: validation fails first.
			server, err := New(t.Context(), cfg)
			if err == nil {
				_ = server.Close()
			}
			if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New = %v, want ErrInvalidConfig containing %q", err, test.want)
			}
			if strings.Contains(err.Error(), "secret-shaped") {
				t.Errorf("New = %v, which contains the file's contents", err)
			}
			if name == "absent" && !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("New = %v, want it to match fs.ErrNotExist", err)
			}
		})
	}
}

// TestDelegateKeySetFileIsLeftAsTheOperatorWroteIt: the key set file is the
// operator's, such as a mounted Secret, so AS-1 only reads it: it is unchanged
// after a failed construction, a construction, and Close.
func TestDelegateKeySetFileIsLeftAsTheOperatorWroteIt(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	d, _, _ := newDelegate(t)
	want, err := os.ReadFile(d.KeySetFile)
	if err != nil {
		t.Fatal(err)
	}
	unchanged := func(when string) {
		t.Helper()
		if got, err := os.ReadFile(d.KeySetFile); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("key set file %s: %v, changed = %v", when, err, !bytes.Equal(got, want))
		}
	}
	cfg := testConfig(t, h, "https://as1.mecatl.test/as1")
	cfg.Delegate = &d
	// Construction fails after validation, at the absent signing key.
	cfg.SigningKeyFile = filepath.Join(t.TempDir(), "absent.pem")
	if server, err := New(t.Context(), cfg); err == nil {
		_ = server.Close()
		t.Fatal("New accepted an absent signing key file")
	}
	unchanged("after a failed construction")
	cfg.SigningKeyFile = ""
	server, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	unchanged("after construction")
	if err := server.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	unchanged("after Close")
}

// TestDelegateKeySetRotation: with the old and new broker client keys in the
// key set file, AS-1 accepts client assertions signed by either; rebuilt from
// a file holding only the new key, it accepts only the new key's.
func TestDelegateKeySetRotation(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	oldKey, newKey := brokerconfigtest.NewKey(t), brokerconfigtest.NewKey(t)
	start := func(keys ...*ecdsa.PrivateKey) *fixture {
		f, _, _ := startDelegateAS1(t, h, func(_ *Config, d *DelegateClient) {
			d.KeySetFile = brokerconfigtest.WriteKeySet(t, filepath.Join(t.TempDir(), "jwks.json"), keys...)
		})
		return f
	}
	// status is the token endpoint's answer to an exchange authenticated
	// with an assertion key signs.
	status := func(f *fixture, key *ecdsa.PrivateKey) (int, string) {
		t.Helper()
		signer, err := identitybroker.NewClientAssertionSigner(key, 0)
		if err != nil {
			t.Fatal(err)
		}
		n := time.Now()
		assertion := signAssertion(t, key, signer.JWKS().Keys[0].KeyID, jwt.MapClaims{
			"iss": identitybroker.BrokerClientID, "sub": identitybroker.BrokerClientID, "aud": f.server.Issuer(),
			"iat": n.Unix(), "exp": n.Add(time.Minute).Unix(), "jti": rand.Text(),
		})
		return postToken(t, f.server, exchangeForm(assertion, f.signIn(t, testAudience).AccessToken))
	}

	during := start(oldKey, newKey)
	for name, key := range map[string]*ecdsa.PrivateKey{"old": oldKey, "new": newKey} {
		if code, errCode := status(during, key); code != http.StatusOK {
			t.Errorf("during the rotation, the %s key's assertion: status %d error %q, want 200", name, code, errCode)
		}
	}
	after := start(newKey)
	if code, errCode := status(after, newKey); code != http.StatusOK {
		t.Errorf("after the rotation, the new key's assertion: status %d error %q, want 200", code, errCode)
	}
	if code, errCode := status(after, oldKey); code != http.StatusUnauthorized || errCode != "invalid_client" {
		t.Errorf("after the rotation, the old key's assertion: status %d error %q, want 401 invalid_client", code, errCode)
	}
}

// startDelegateAS1 hosts AS-1 on loopback with a delegate client whose keys are
// a fresh broker key, after edit tweaks the Config and the DelegateClient. It
// returns the fixture, a client-assertion signer for that key, and the private
// key for signing raw assertions.
func startDelegateAS1(t *testing.T, h *identityharness.Harness, edit func(*Config, *DelegateClient)) (*fixture, *identitybroker.ClientAssertionSigner, *ecdsa.PrivateKey) {
	t.Helper()
	d, signer, key := newDelegate(t)
	f := startAS1(t, h, func(c *Config) {
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

	form := func(t *testing.T) url.Values {
		t.Helper()
		assertion, err := signer.ClientAssertion(f.server.Issuer(), time.Now())
		if err != nil {
			t.Fatalf("ClientAssertion: %v", err)
		}
		return exchangeForm(assertion.Value(), f.signIn(t, testAudience).AccessToken)
	}

	// Control: the same client, assertion, and kind of subject token convert
	// with the session-open form, so the refusals below come from the policy.
	t.Run("control", func(t *testing.T) {
		if status, errCode := postToken(t, f.server, form(t)); status != http.StatusOK {
			t.Fatalf("session-open form status %d error %q, want 200", status, errCode)
		}
	})

	// A plain RFC 8693 request for an access token is refused because the
	// delegate has no output audience (stacklok/toolhive#7026). ToolHive gives
	// this refusal no reason code, so the test pins the status and error code;
	// the free-text description, which names the audience, is only logged.
	// An explicitly requested audience the client may not use is an RFC 8707
	// invalid_target; omitting the audience is an invalid_request.
	for name, tc := range map[string]struct{ audience, code string }{
		"default audience": {"", "invalid_request"},
		"AS-1's audience":  {testAudience, "invalid_target"},
	} {
		t.Run(name, func(t *testing.T) {
			req := form(t)
			req.Del("authorization_details")
			req.Set("requested_token_type", tokenTypeAccessToken)
			if tc.audience != "" {
				req.Set("audience", tc.audience)
			}
			status, code, description := postTokenDescribed(t, f.server, req)
			t.Logf("refusal description: %s", description)
			if status != http.StatusBadRequest || code != tc.code {
				t.Fatalf("status %d error %q description %q, want 400 %s", status, code, description, tc.code)
			}
		})
	}
}

func TestDelegateRefusesAForeignAudienceSubjectToken(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	f, signer, _ := startDelegateAS1(t, h, nil)
	c := delegateClient(t, f, signer)

	// otherAudience is one AS-1 issues for, but not the delegate's
	// subject_token_audiences.
	access := f.signIn(t, otherAudience).AccessToken
	_, err := c.Exchange(t.Context(), identitybroker.TokenExchange{SubjectToken: identitybroker.NewToken(access), Grant: idjagGrant()})
	var refusal *identitybroker.ServerRefusal
	if !errors.As(err, &refusal) || refusal.Reason != "subject_token_audience" {
		t.Fatalf("Exchange = %v, want a refusal with subject_token_audience", err)
	}
}

func TestDelegateRequiresOfflineAccess(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	f, signer, _ := startDelegateAS1(t, h, nil)
	c := delegateClient(t, f, signer)

	access := f.signInWithScope(t, PublicClient{ID: testClientID, RedirectURIs: []string{testRedirect}}, testAudience, "openid").AccessToken
	_, err := c.Exchange(t.Context(), identitybroker.TokenExchange{SubjectToken: identitybroker.NewToken(access), Grant: idjagGrant()})
	var refusal *identitybroker.ServerRefusal
	if !errors.As(err, &refusal) || refusal.Reason != "subject_token_offline_access" {
		t.Fatalf("Exchange = %v, want a refusal with subject_token_offline_access", err)
	}
}

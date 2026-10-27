package identityharness_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// TestClientAssertionDefects: every failed design step (5) check returns 401
// invalid_client, before any session-open check runs.
func TestClientAssertionDefects(t *testing.T) {
	clock := newTestClock()
	h := identityharness.New(t, identityharness.Options{Now: clock.Now})
	b := h.ScriptedBroker()

	// Using a jti once, even for an open AS-1 then refuses, consumes it.
	_, err := b.OpenSession(t.Context(), identityharness.SessionOpenExchange{
		SubjectToken: "not-a-jwt", Session: "A42", Owner: h.Owner(identityharness.Alice),
		Assertion: b.Assertion(identityharness.ClientAssertionSpec{JTI: "ca-reused"}),
	})
	assertReason(t, err, http.StatusBadRequest, "invalid_request", "subject_invalid")

	maxLife := identityharness.DefaultClientAssertionMaxLifetime
	skew := identityharness.ClientAssertionClockSkew
	defects := map[string]string{
		"alg none": handBuilt(t, h, clock, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType),
		"HS256":    handBuilt(t, h, clock, jwt.SigningMethodHS256, []byte("shared-secret-0123456789abcdef")),
	}
	for name, spec := range map[string]identityharness.ClientAssertionSpec{
		"token endpoint aud": {Audience: []string{h.AS1.TokenEndpoint()}},
		"two audiences":      {Audience: []string{h.AS1.Issuer(), h.AS1.TokenEndpoint()}},
		"expired":            {IssuedAt: clock.Now().Add(-2*maxLife - skew)},
		"exp plus skew now":  {IssuedAt: clock.Now().Add(-maxLife - skew), Lifetime: maxLife},
		"over-long":          {Lifetime: maxLife + time.Second},
		"iat in the future":  {IssuedAt: clock.Now().Add(skew + 30*time.Second), Lifetime: 10 * time.Second},
		"no iat":             {OmitIssuedAt: true},
		"no exp":             {OmitExpiry: true},
		"no jti":             {OmitJTI: true},
		"reused jti":         {JTI: "ca-reused"},
		"unregistered key":   {ForeignKey: true},
		"unknown kid":        {KeyID: "other-key"},
		"no kid":             {OmitKeyID: true},
		"other iss":          {Issuer: "rogue"},
		"other sub":          {Subject: "rogue"},
		"other typ":          {Type: "at+jwt"},
	} {
		defects[name] = b.Assertion(spec)
	}
	for name, assertion := range defects {
		t.Run(name, func(t *testing.T) {
			_, err := b.OpenSession(t.Context(), openReq(h, aliceToken(h), "A42", identityharness.Alice))
			if err != nil {
				t.Fatalf("control open: %v", err)
			}
			req := openReq(h, aliceToken(h), "A42", identityharness.Alice)
			req.Assertion = assertion
			_, err = b.OpenSession(t.Context(), req)
			assertReason(t, err, http.StatusUnauthorized, "invalid_client", "")
		})
	}

	valid := func() string { return b.Assertion(identityharness.ClientAssertionSpec{}) }
	for name, form := range map[string]url.Values{
		"client_secret alone":       {"client_id": {"mecatl"}, "client_secret": {"s3cret"}},
		"client_secret with jwt":    {"client_secret": {"s3cret"}, "client_assertion_type": {jwtBearer}, "client_assertion": {valid()}},
		"no assertion":              {"client_id": {"mecatl"}},
		"wrong assertion type":      {"client_assertion_type": {"urn:example:other"}, "client_assertion": {valid()}},
		"assertion for other id":    {"client_id": {identityharness.DefaultClientID}, "client_assertion_type": {jwtBearer}, "client_assertion": {valid()}},
		"assertion type, no assert": {"client_assertion_type": {jwtBearer}},
	} {
		t.Run(name, func(t *testing.T) {
			form.Set("grant_type", grantTokenExchange)
			_, err := postToken(t, h, form)
			assertOAuthError(t, err, "invalid_client")
		})
	}

	t.Run("basic authentication", func(t *testing.T) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.AS1.TokenEndpoint(), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.SetBasicAuth("mecatl", "s3cret")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := h.HTTPClient().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Basic authentication: status %d, want 401", resp.StatusCode)
		}
	})
}

func TestClientAssertionAccepted(t *testing.T) {
	brokerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clock := newTestClock()
	h := identityharness.New(t, identityharness.Options{
		Now:                        clock.Now,
		BrokerKeys:                 []identityharness.BrokerKey{{KeyID: "external", Public: &brokerKey.PublicKey}},
		ClientAssertionMaxLifetime: 5 * time.Minute,
	})
	b := h.ScriptedBroker()
	skew := identityharness.ClientAssertionClockSkew

	external := jwt.NewWithClaims(jwt.SigningMethodES256, assertionClaims(h, clock))
	external.Header["kid"] = "external"
	externalAssertion, err := external.SignedString(brokerKey)
	if err != nil {
		t.Fatal(err)
	}
	for name, assertion := range map[string]string{
		"registered key from Options, aud array": externalAssertion,
		"untyped":                                b.Assertion(identityharness.ClientAssertionSpec{OmitType: true}),
		"typ JWT":                                b.Assertion(identityharness.ClientAssertionSpec{Type: "JWT"}),
		"lifetime exactly at the maximum":        b.Assertion(identityharness.ClientAssertionSpec{Lifetime: 5 * time.Minute}),
		// The clock skew AS-1 allows, on each side.
		"exp passed, within the skew": b.Assertion(identityharness.ClientAssertionSpec{
			IssuedAt: clock.Now().Add(-time.Minute - skew + time.Second), Lifetime: time.Minute,
		}),
		"iat ahead, within the skew": b.Assertion(identityharness.ClientAssertionSpec{
			IssuedAt: clock.Now().Add(skew - time.Second), Lifetime: time.Minute,
		}),
	} {
		t.Run(name, func(t *testing.T) {
			req := openReq(h, aliceToken(h), "A42", identityharness.Alice)
			req.Assertion = assertion
			if _, err := b.OpenSession(t.Context(), req); err != nil {
				t.Fatalf("OpenSession: %v", err)
			}
		})
	}
}

// TestBrokerKeyOptionsRefused: New fails on a broker key AS-1 could never
// use or that shadows the harness's own.
func TestBrokerKeyOptionsRefused(t *testing.T) {
	p256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]identityharness.BrokerKey{
		"empty key ID":   {Public: &p256.PublicKey},
		"default key ID": {KeyID: identityharness.DefaultBrokerKeyID, Public: &p256.PublicKey},
		"P-384":          {KeyID: "p384", Public: &p384.PublicKey},
		"nil key":        {KeyID: "nil"},
	} {
		t.Run(name, func(t *testing.T) {
			ft := &fatalRecorder{TB: t}
			func() {
				defer func() { _ = recover() }()
				identityharness.New(ft, identityharness.Options{BrokerKeys: []identityharness.BrokerKey{key}})
			}()
			if !ft.failed {
				t.Error("New accepted the key")
			}
		})
	}
}

// fatalRecorder records Fatalf instead of ending the test, so a test can
// check that New refuses its options.
type fatalRecorder struct {
	testing.TB
	failed bool
}

func (f *fatalRecorder) Fatalf(string, ...any) {
	f.failed = true
	panic("fatal")
}

// assertionClaims are claims of an assertion AS-1 accepts, at clock's time.
func assertionClaims(h *identityharness.Harness, clock *testClock) jwt.MapClaims {
	now := clock.Now()
	return jwt.MapClaims{
		"iss": "mecatl", "sub": "mecatl", "aud": []string{h.AS1.Issuer()},
		"iat": now.Unix(), "exp": now.Add(time.Minute).Unix(), "jti": randomJTI(),
	}
}

// handBuilt signs valid claims with method and key under the harness's broker
// kid, for an algorithm AS-1 must refuse.
func handBuilt(t *testing.T, h *identityharness.Harness, clock *testClock, method jwt.SigningMethod, key any) string {
	t.Helper()
	token := jwt.NewWithClaims(method, assertionClaims(h, clock))
	token.Header["kid"] = identityharness.DefaultBrokerKeyID
	token.Header["typ"] = "client-authentication+jwt"
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func randomJTI() string { return rand.Text() }

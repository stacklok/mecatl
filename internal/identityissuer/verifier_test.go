package identityissuer

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestIdentityIssuerSubstrate_Scenario3_IndependentBundleVerification(t *testing.T) {
	issuer := testIssuer(t)
	bundle, err := issuer.Bundle(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(testVerifierConfig(), BundleFetcherFunc(func(context.Context, string) ([]byte, error) {
		return bundle, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	token, err := issuer.IssueJWTSubject("spiffe://example.org/workload/api", testConfig().Audience, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := verifier.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "spiffe://example.org/workload/api" || identity.TrustDomain != "example.org" || identity.Audience != testConfig().Audience {
		t.Fatalf("Verify() identity = %#v", identity)
	}
}

func TestADR_0251_VerifierFailsClosed(t *testing.T) {
	issuer := testIssuer(t)
	bundle, err := issuer.Bundle(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(testVerifierConfig(), BundleFetcherFunc(func(context.Context, string) ([]byte, error) { return bundle, nil }))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongAlgorithmKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := duplicateKIDBundle(t, bundle)
	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "algorithm confusion", token: signedToken(t, wrongAlgorithmKey, issuer.ActiveKID(), jwt.SigningMethodES384, jwt.RegisteredClaims{})},
		{name: "unknown kid", token: signedToken(t, issuer.key, "unknown", jwt.SigningMethodES256, jwt.RegisteredClaims{})},
		{name: "key substitution", token: signedToken(t, otherKey, issuer.ActiveKID(), jwt.SigningMethodES256, jwt.RegisteredClaims{})},
		{name: "foreign subject", token: signedToken(t, issuer.key, issuer.ActiveKID(), jwt.SigningMethodES256, testClaims("spiffe://other.example/workload/api", jwt.ClaimStrings{testConfig().Audience}, time.Now()))},
		{name: "wrong audience", token: signedToken(t, issuer.key, issuer.ActiveKID(), jwt.SigningMethodES256, testClaims("spiffe://example.org/workload/api", jwt.ClaimStrings{"other"}, time.Now()))},
		{name: "multiple audience", token: signedToken(t, issuer.key, issuer.ActiveKID(), jwt.SigningMethodES256, testClaims("spiffe://example.org/workload/api", jwt.ClaimStrings{testConfig().Audience, "other"}, time.Now()))},
		{name: "expired", token: signedToken(t, issuer.key, issuer.ActiveKID(), jwt.SigningMethodES256, testClaims("spiffe://example.org/workload/api", jwt.ClaimStrings{testConfig().Audience}, time.Now().Add(-2*time.Minute)))},
		{name: "future", token: signedToken(t, issuer.key, issuer.ActiveKID(), jwt.SigningMethodES256, jwt.RegisteredClaims{Issuer: "spiffe://example.org", Subject: "spiffe://example.org/workload/api", Audience: jwt.ClaimStrings{testConfig().Audience}, IssuedAt: jwt.NewNumericDate(time.Now()), NotBefore: jwt.NewNumericDate(time.Now().Add(time.Hour)), ExpiresAt: jwt.NewNumericDate(time.Now().Add(2 * time.Hour))})},
		{name: "excessive lifetime", token: signedToken(t, issuer.key, issuer.ActiveKID(), jwt.SigningMethodES256, jwt.RegisteredClaims{Issuer: "spiffe://example.org", Subject: "spiffe://example.org/workload/api", Audience: jwt.ClaimStrings{testConfig().Audience}, IssuedAt: jwt.NewNumericDate(time.Now()), NotBefore: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(time.Now().Add(2 * time.Hour))})},
		{name: "malformed", token: "not.a.jwt"},
		{name: "oversized", token: strings.Repeat("a", MaxTokenBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := verifier.Verify(tc.token); err == nil {
				t.Fatal("Verify() accepted invalid token or bundle")
			}
		})
	}
	duplicateVerifier, err := NewVerifier(testVerifierConfig(), BundleFetcherFunc(func(context.Context, string) ([]byte, error) { return duplicate, nil }))
	if err != nil {
		t.Fatal(err)
	}
	if err := duplicateVerifier.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh() accepted a bundle with duplicate key ids")
	}
	if err := verifier.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh() accepted a regressed bundle sequence")
	}
	for _, raw := range [][]byte{[]byte(`{"sequence":1,"keys":`), make([]byte, MaxBundleBytes+1)} {
		v, err := NewVerifier(testVerifierConfig(), BundleFetcherFunc(func(context.Context, string) ([]byte, error) { return raw, nil }))
		if err != nil {
			t.Fatal(err)
		}
		if err := v.Refresh(context.Background()); err == nil {
			t.Fatal("Refresh() accepted malformed or oversized bundle")
		}
	}
}

func TestInvariant_identity_bundle_freshness_bound(t *testing.T) {
	issuer := testIssuer(t)
	bundle, err := issuer.Bundle(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	fetchErr := errors.New("bundle unavailable")
	fetch := func(context.Context, string) ([]byte, error) { return bundle, nil }
	verifier, err := NewVerifier(VerifierConfig{TrustDomain: "example.org", Audience: testConfig().Audience, ClockSkew: time.Second, TokenTTL: 5 * time.Minute, BundleCacheTTL: time.Minute, HTTPSBootstrapURL: testConfig().HTTPSBootstrapURL, Now: func() time.Time { return now }}, BundleFetcherFunc(fetch))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	token := signedToken(t, issuer.key, issuer.ActiveKID(), jwt.SigningMethodES256, testClaims("spiffe://example.org/workload/api", jwt.ClaimStrings{testConfig().Audience}, now))
	fetch = func(context.Context, string) ([]byte, error) { return nil, fetchErr }
	now = now.Add(30 * time.Second)
	if err := verifier.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh() succeeded after fetch failure")
	}
	if _, err := verifier.Verify(token); err != nil {
		t.Fatalf("Verify() rejected complete bundle inside freshness bound: %v", err)
	}
	now = now.Add(31 * time.Second)
	if _, err := verifier.Verify(token); err == nil {
		t.Fatal("Verify() accepted stale bundle after freshness bound")
	}
}

func TestADR_0251_CanonicalBundleHasNoPrivateMaterial(t *testing.T) {
	issuer := testIssuer(t)
	first, err := issuer.Bundle(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := issuer.Bundle(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var firstBundle, secondBundle struct {
		Sequence    uint64           `json:"sequence"`
		RefreshHint int64            `json:"refresh_hint"`
		Keys        []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(first, &firstBundle); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second, &secondBundle); err != nil {
		t.Fatal(err)
	}
	if firstBundle.Sequence == 0 || secondBundle.Sequence <= firstBundle.Sequence || firstBundle.RefreshHint != 60 || len(firstBundle.Keys) != 1 {
		t.Fatalf("bundle sequence/hint/keys = %#v / %#v", firstBundle, secondBundle)
	}
	for _, key := range firstBundle.Keys {
		if key["kty"] != "EC" || key["crv"] != "P-256" || key["kid"] != issuer.ActiveKID() || key["x"] == "" || key["y"] == "" {
			t.Fatalf("public JWK = %#v", key)
		}
		for _, forbidden := range []string{"d", "k", "p", "q", "dp", "dq", "qi"} {
			if _, found := key[forbidden]; found {
				t.Fatalf("bundle exposes private JWK field %q", forbidden)
			}
		}
	}
}

func testIssuer(t *testing.T) *Issuer {
	t.Helper()
	issuer, err := Load(testConfig(), testManifest("active", true), func(string) ([]byte, error) { return testPKCS8(t), nil })
	if err != nil {
		t.Fatal(err)
	}
	return issuer
}

func testVerifierConfig() VerifierConfig {
	return VerifierConfig{TrustDomain: "example.org", Audience: testConfig().Audience, TokenTTL: 5 * time.Minute, ClockSkew: 30 * time.Second, BundleCacheTTL: time.Minute, HTTPSBootstrapURL: testConfig().HTTPSBootstrapURL}
}

func testClaims(subject string, audience jwt.ClaimStrings, expiry time.Time) jwt.RegisteredClaims {
	return jwt.RegisteredClaims{Issuer: "spiffe://example.org", Subject: subject, Audience: audience, IssuedAt: jwt.NewNumericDate(expiry.Add(-time.Minute)), NotBefore: jwt.NewNumericDate(expiry.Add(-time.Minute)), ExpiresAt: jwt.NewNumericDate(expiry)}
}

func signedToken(t *testing.T, key *ecdsa.PrivateKey, kid string, method jwt.SigningMethod, claims jwt.RegisteredClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(method, claims)
	token.Header["kid"] = kid
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func duplicateKIDBundle(t *testing.T, bundle []byte) []byte {
	t.Helper()
	var parsed struct {
		Sequence    uint64           `json:"sequence"`
		RefreshHint int64            `json:"refresh_hint"`
		Keys        []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(bundle, &parsed); err != nil {
		t.Fatal(err)
	}
	parsed.Keys = append(parsed.Keys, parsed.Keys[0])
	result, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

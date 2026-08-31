package identityissuer

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestADR_0251_IdentityConfigFailsClosed(t *testing.T) {
	key := testPKCS8(t)
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "missing trust domain", mutate: func(c *Config) { c.TrustDomain = "" }},
		{name: "invalid trust domain", mutate: func(c *Config) { c.TrustDomain = "https://example.org" }},
		{name: "invalid token ttl", mutate: func(c *Config) { c.TokenTTL = 0 }},
		{name: "zero skew", mutate: func(c *Config) { c.ClockSkew = 0 }},
		{name: "invalid skew", mutate: func(c *Config) { c.ClockSkew = -time.Second }},
		{name: "skew exceeds ttl", mutate: func(c *Config) { c.ClockSkew = c.TokenTTL + time.Second }},
		{name: "invalid cache bound", mutate: func(c *Config) { c.BundleCacheTTL = 0 }},
		{name: "missing audience", mutate: func(c *Config) { c.Audience = "" }},
		{name: "incomplete bootstrap", mutate: func(c *Config) { c.HTTPSBootstrapURL = "http://bundle.example.org" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			tc.mutate(&cfg)
			loads := 0
			_, err := Load(cfg, testManifest("active", true), func(string) ([]byte, error) {
				loads++
				return key, nil
			})
			if err == nil {
				t.Fatal("Load() succeeded with invalid enabled configuration")
			}
			if loads != 0 {
				t.Fatalf("key loader called %d times before configuration validation", loads)
			}
		})
	}
}

func TestInvariant_identity_trust_domain_explicit(t *testing.T) {
	for _, trustDomain := range []string{"example.org", "prod.example.org"} {
		if err := ValidateTrustDomain(trustDomain); err != nil {
			t.Fatalf("ValidateTrustDomain(%q): %v", trustDomain, err)
		}
	}
	for _, trustDomain := range []string{"", "https://example.org", "example.org:443", "Example.org", "127.0.0.1", "example.org/path"} {
		if err := ValidateTrustDomain(trustDomain); err == nil {
			t.Fatalf("ValidateTrustDomain(%q) succeeded", trustDomain)
		}
	}

	cfg := testConfig()
	issuer, err := Load(cfg, testManifest("active", true), func(string) ([]byte, error) { return testPKCS8(t), nil })
	if err != nil {
		t.Fatal(err)
	}
	if got := issuer.TrustDomain(); got != cfg.TrustDomain {
		t.Fatalf("TrustDomain() = %q, want configured %q", got, cfg.TrustDomain)
	}
}

func TestIdentityIssuerSubstrate_Scenario2_LoadImmutableKeyring(t *testing.T) {
	key := testPKCS8(t)
	issuer, err := Load(testConfig(), testManifest("active", true), func(name string) ([]byte, error) {
		if name != "active" {
			return nil, fmt.Errorf("unexpected key %q", name)
		}
		return key, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if issuer.Algorithm() != "ES256" {
		t.Fatalf("Algorithm() = %q, want ES256", issuer.Algorithm())
	}
	if issuer.ActiveKID() == "" {
		t.Fatal("ActiveKID() is empty")
	}

	token, err := issuer.IssueJWTSubject("spiffe://example.org/workload/api", testConfig().Audience, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jwt.Parse(token, func(token *jwt.Token) (any, error) {
		if token.Method.Alg() != jwt.SigningMethodES256.Alg() {
			return nil, errors.New("unexpected signing method")
		}
		return issuer.PublicKey(), nil
	})
	if err != nil || !parsed.Valid {
		t.Fatalf("issued token validation = %v, valid=%v", err, parsed != nil && parsed.Valid)
	}
	if got := parsed.Header["kid"]; got != issuer.ActiveKID() {
		t.Fatalf("kid = %q, want %q", got, issuer.ActiveKID())
	}

	second, err := Load(testConfig(), testManifest("active", true), func(string) ([]byte, error) { return key, nil })
	if err != nil {
		t.Fatal(err)
	}
	if second.ActiveKID() != issuer.ActiveKID() {
		t.Fatalf("kid changed across identical immutable keyring: %q != %q", second.ActiveKID(), issuer.ActiveKID())
	}
}

func TestADR_0251_KeyringRejectsInvalidGeneration(t *testing.T) {
	valid := testPKCS8(t)
	wrongCurve := testPKCS8Curve(t, elliptic.P384())
	for _, tc := range []struct {
		name     string
		manifest []byte
		loader   func(string) ([]byte, error)
	}{
		{name: "malformed manifest", manifest: []byte(`{"keys":`)},
		{name: "oversized manifest", manifest: make([]byte, MaxManifestBytes+1)},
		{name: "unknown manifest field", manifest: []byte(`{"version":1,"keys":[{"name":"active","active":true,"unknown":true}]}`)},
		{name: "duplicate key names", manifest: []byte(`{"version":1,"keys":[{"name":"active","active":true},{"name":"active","active":false}]}`)},
		{name: "no active signer", manifest: testManifest("active", false)},
		{name: "multiple active signers", manifest: []byte(`{"version":1,"keys":[{"name":"active","active":true},{"name":"next","active":true}]}`), loader: func(string) ([]byte, error) { return valid, nil }},
		{name: "missing key item", manifest: testManifest("active", true), loader: func(string) ([]byte, error) { return nil, errors.New("not found") }},
		{name: "non p256 key", manifest: testManifest("active", true), loader: func(string) ([]byte, error) { return wrongCurve, nil }},
		{name: "mismatched item", manifest: testManifest("active", true), loader: func(string) ([]byte, error) { return []byte("not pkcs8"), nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loader := tc.loader
			if loader == nil {
				loader = func(string) ([]byte, error) { return valid, nil }
			}
			issuer, err := Load(testConfig(), tc.manifest, loader)
			if err == nil {
				t.Fatal("Load() succeeded for invalid keyring generation")
			}
			if issuer != nil {
				t.Fatal("Load() published partial issuer state")
			}
		})
	}
}

func TestInvariant_identity_issuer_typed_envelope(t *testing.T) {
	issuer, err := Load(testConfig(), testManifest("active", true), func(string) ([]byte, error) { return testPKCS8(t), nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, subject, audience string
		ttl                     time.Duration
	}{
		{name: "foreign trust domain", subject: "spiffe://other.example/workload/api", audience: testConfig().Audience, ttl: time.Minute},
		{name: "non SPIFFE subject", subject: "https://example.org/workload/api", audience: testConfig().Audience, ttl: time.Minute},
		{name: "unregistered audience", subject: "spiffe://example.org/workload/api", audience: "other", ttl: time.Minute},
		{name: "zero ttl", subject: "spiffe://example.org/workload/api", audience: testConfig().Audience, ttl: 0},
		{name: "ttl over configured bound", subject: "spiffe://example.org/workload/api", audience: testConfig().Audience, ttl: testConfig().TokenTTL + time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := issuer.IssueJWTSubject(tc.subject, tc.audience, tc.ttl); err == nil {
				t.Fatal("IssueJWTSubject() accepted untyped or out-of-policy input")
			}
		})
	}
}

func testConfig() Config {
	return Config{
		Enabled:           true,
		TrustDomain:       "example.org",
		TokenTTL:          5 * time.Minute,
		ClockSkew:         30 * time.Second,
		BundleCacheTTL:    time.Minute,
		Audience:          "spiffe://example.org/broker-verifier",
		HTTPSBootstrapURL: "https://bundle.example.org/spiffe/jwt",
	}
}

func testManifest(name string, active bool) []byte {
	return []byte(fmt.Sprintf(`{"version":1,"keys":[{"name":%q,"active":%t}]}`, name, active))
}

func testPKCS8(t *testing.T) []byte {
	t.Helper()
	return testPKCS8Curve(t, elliptic.P256())
}

func testPKCS8Curve(t *testing.T, curve elliptic.Curve) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

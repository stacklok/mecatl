package mcp

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

func TestCanonicalOAuthResource(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		in   string
		want string
	}{
		{"host and default port", "HTTPS://EXAMPLE.com:443", "https://example.com/"},
		{"non-default port", "http://EXAMPLE.com:8080/a", "http://example.com:8080/a"},
		{"dot segments", "https://example.com/a/./b/../c/", "https://example.com/a/c/"},
		{"duplicate slashes", "https://example.com/a//b", "https://example.com/a//b"},
		{"escaped separator and query", "https://example.com/a%2Fb?z=2&z=1", "https://example.com/a%2Fb?z=2&z=1"},
		{"escaped dot is data", "https://example.com/a/%2e%2e/b", "https://example.com/a/%2e%2e/b"},
		{"ipv6", "http://[2001:0db8::1]:80/mcp", "http://[2001:db8::1]/mcp"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := canonicalOAuthResource(test.in)
			if err != nil {
				t.Fatalf("canonicalOAuthResource: %v", err)
			}
			if got != test.want {
				t.Fatalf("canonicalOAuthResource(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
	for _, raw := range []string{"", "file:///x", "https://user@example.com/x", "https://example.com/x#fragment", "://bad"} {
		if _, err := canonicalOAuthResource(raw); err == nil {
			t.Errorf("canonicalOAuthResource(%q) succeeded", raw)
		}
	}
}

func TestOAuthCredentialKeyCoversEveryIdentityDimension(t *testing.T) {
	t.Parallel()
	base := testOAuthIdentity()
	baseKey, err := oauthCredentialKeyHex(base)
	if err != nil {
		t.Fatal(err)
	}
	variants := []oauthCredentialIdentity{
		{Profile: "other", Principal: base.Principal, Resource: base.Resource, Issuer: base.Issuer, ClientKind: base.ClientKind, ClientID: base.ClientID},
		{Profile: base.Profile, Principal: "other", Resource: base.Resource, Issuer: base.Issuer, ClientKind: base.ClientKind, ClientID: base.ClientID},
		{Profile: base.Profile, Principal: base.Principal, Resource: "https://mcp.example/other", Issuer: base.Issuer, ClientKind: base.ClientKind, ClientID: base.ClientID},
		{Profile: base.Profile, Principal: base.Principal, Resource: base.Resource, Issuer: base.Issuer + "/", ClientKind: base.ClientKind, ClientID: base.ClientID},
		{Profile: base.Profile, Principal: base.Principal, Resource: base.Resource, Issuer: base.Issuer, ClientKind: "cimd", ClientID: "https://client.example/metadata.json"},
		{Profile: base.Profile, Principal: base.Principal, Resource: base.Resource, Issuer: base.Issuer, ClientKind: base.ClientKind, ClientID: "other"},
	}
	for i, variant := range variants {
		key, keyErr := oauthCredentialKeyHex(variant)
		if keyErr != nil {
			t.Fatalf("variant %d: %v", i, keyErr)
		}
		if key == baseKey {
			t.Errorf("variant %d did not alter key", i)
		}
	}
	if len(baseKey) != 64 {
		t.Fatalf("key length = %d, want 64 hex characters", len(baseKey))
	}
}

func TestOAuthCredentialEnvelopeStrictRoundTrip(t *testing.T) {
	t.Parallel()
	identity := testOAuthIdentity()
	origins := testOAuthOrigins()
	cfg := testOAuthConfig("https://issuer.example/token")
	token := &oauth2.Token{AccessToken: "access-canary", TokenType: "Bearer", RefreshToken: "refresh-canary", Expiry: time.Date(2026, 8, 14, 12, 34, 56, 123, time.UTC)}
	envelope := newOAuthCredentialEnvelope(identity, cfg, token)
	value, err := encodeOAuthCredential(envelope, identity, true, origins)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(value, []byte(testClientSecretCanary)) {
		t.Fatal("client secret was persisted")
	}
	decoded, err := decodeOAuthCredential(value, identity, true, origins)
	if err != nil {
		t.Fatal(err)
	}
	got, err := envelopeToken(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !tokensEqual(got, token) {
		t.Fatalf("round-trip token = %#v, want %#v", got, token)
	}
	if strings.Join(decoded.Refresh.Scopes, ",") != "offline_access,read" {
		t.Fatalf("canonical scopes = %v", decoded.Refresh.Scopes)
	}
}

func TestOAuthCredentialEnvelopeRejectsMalformedRecords(t *testing.T) {
	t.Parallel()
	identity := testOAuthIdentity()
	origins := testOAuthOrigins()
	valid, err := encodeOAuthCredential(newOAuthCredentialEnvelope(identity, testOAuthConfig("https://issuer.example/token"), &oauth2.Token{AccessToken: "a", TokenType: "Bearer", RefreshToken: "r"}), identity, true, origins)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"unknown field":        bytes.Replace(valid, []byte(`"version":1`), []byte(`"version":1,"extra":true`), 1),
		"trailing data":        append(append([]byte(nil), valid...), []byte(` {}`)...),
		"wrong schema":         bytes.Replace(valid, []byte(oauthCredentialSchema), []byte("other.schema"), 1),
		"identity mismatch":    bytes.Replace(valid, []byte(`"principal":"principal"`), []byte(`"principal":"other"`), 1),
		"missing refresh":      bytes.Replace(valid, []byte(`,"refresh_token":"r"`), nil, 1),
		"foreign token origin": bytes.Replace(valid, []byte("https://issuer.example/token"), []byte("https://evil.example/token"), 1),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			if _, decodeErr := decodeOAuthCredential(value, identity, true, origins); decodeErr == nil {
				t.Fatal("malformed envelope accepted")
			}
		})
	}
	oversized := bytes.Repeat([]byte{'x'}, credentialstore.MaxValueBytes+1)
	if _, err := decodeOAuthCredential(oversized, identity, true, origins); err == nil {
		t.Fatal("oversized envelope accepted")
	}
}

const testClientSecretCanary = "client-secret-canary"

func testOAuthIdentity() oauthCredentialIdentity {
	return oauthCredentialIdentity{Profile: "profile", Principal: "principal", Resource: "https://mcp.example/mcp", Issuer: "https://issuer.example", ClientKind: "preregistered", ClientID: "client-id"}
}

func testOAuthOrigins() map[string]struct{} {
	return map[string]struct{}{"https://issuer.example": {}, "https://mcp.example": {}}
}

func testOAuthConfig(tokenURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID: "client-id", ClientSecret: testClientSecretCanary,
		Endpoint:    oauth2.Endpoint{TokenURL: tokenURL, AuthStyle: oauth2.AuthStyleInHeader},
		RedirectURL: "http://127.0.0.1/callback", Scopes: []string{"read", "offline_access", "read"},
	}
}

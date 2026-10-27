package identitybroker

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testAS = "https://broker.example.com/as"

// thumbprint computes the RFC 7638 thumbprint of a P-256 key independently.
func thumbprint(t *testing.T, pub *ecdsa.PublicKey) string {
	t.Helper()
	point, err := pub.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	canonical, err := json.Marshal(map[string]string{ // encoding/json sorts the members
		"crv": "P-256", "kty": "EC", "x": enc.EncodeToString(point[1:33]), "y": enc.EncodeToString(point[33:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	return enc.EncodeToString(sum[:])
}

func TestClientAssertionHeaderAndClaims(t *testing.T) {
	key := testKey(t, "broker client key")
	// Through crypto.Signer, as a KMS handle would sign.
	s, err := NewClientAssertionSigner(keySigner{key: key}, 0)
	if err != nil {
		t.Fatalf("NewClientAssertionSigner: %v", err)
	}
	now := time.Unix(1_800_000_000, 400_000_000)
	a, err := s.ClientAssertion(testAS, now)
	if err != nil {
		t.Fatalf("ClientAssertion: %v", err)
	}
	wantKID := thumbprint(t, &key.PublicKey)
	header := segment(t, a.Value(), "header")
	wantHeader := map[string]any{"typ": "client-authentication+jwt", "alg": "ES256", "kid": wantKID}
	if kid := s.JWKS().Keys[0].KeyID; !maps.Equal(header, wantHeader) || kid != wantKID {
		t.Fatalf("header = %v, JWKS kid = %q; want exactly %v", header, kid, wantHeader)
	}
	claims := segment(t, a.Value(), "claims")
	jti, _ := claims["jti"].(string)
	if len(jti) < 20 {
		t.Fatalf("jti = %v, want a random value", claims["jti"])
	}
	delete(claims, "jti")
	wantClaims := map[string]any{
		"iss": "mecatl", "sub": "mecatl", "aud": testAS, // a string, not an array
		"iat": float64(1_800_000_000), "exp": float64(1_800_000_060),
	}
	if !maps.Equal(claims, wantClaims) {
		t.Fatalf("claims = %v, want %v plus jti", claims, wantClaims)
	}
}

func TestClientAssertionJTIIsFreshEachCall(t *testing.T) {
	s, err := NewClientAssertionSigner(testKey(t, "broker client key"), 0)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	seen := map[any]bool{}
	for range 5 {
		a, err := s.ClientAssertion(testAS, now)
		if err != nil {
			t.Fatal(err)
		}
		jti := segment(t, a.Value(), "claims")["jti"]
		if seen[jti] {
			t.Fatalf("jti %v repeated", jti)
		}
		seen[jti] = true
	}
}

func TestClientAssertionLifetime(t *testing.T) {
	key := testKey(t, "broker client key")
	var s *ClientAssertionSigner
	for _, lifetime := range []time.Duration{time.Second, 30 * time.Second, MaxClientAssertionLifetime} {
		var err error
		s, err = NewClientAssertionSigner(key, lifetime)
		if err != nil {
			t.Fatalf("lifetime %v: %v", lifetime, err)
		}
		a, err := s.ClientAssertion(testAS, time.Unix(1_800_000_000, 999_000_000))
		if err != nil {
			t.Fatal(err)
		}
		claims := segment(t, a.Value(), "claims")
		if got := claims["exp"].(float64) - claims["iat"].(float64); got != lifetime.Seconds() {
			t.Fatalf("lifetime %v: exp - iat = %v", lifetime, got)
		}
	}
	for _, lifetime := range []time.Duration{61 * time.Second, time.Hour, -time.Second, time.Second - time.Nanosecond, time.Millisecond} {
		if _, err := NewClientAssertionSigner(key, lifetime); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("lifetime %v: err = %v, want ErrInvalidConfig", lifetime, err)
		}
	}
	if _, err := s.ClientAssertion("", time.Now()); err == nil {
		t.Error("an empty audience was accepted")
	}
}

func TestClientAssertionSignerRefusesNonP256Keys(t *testing.T) {
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, signer := range map[string]crypto.Signer{"raw P-384": p384, "opaque P-384": opaqueSigner{pub: &p384.PublicKey}} {
		if _, err := NewClientAssertionSigner(signer, 0); !errors.Is(err, ErrInvalidSigner) {
			t.Errorf("%s: err = %v, want ErrInvalidSigner", name, err)
		}
	}
	if _, err := NewClientAssertionSigner(nil, 0); !errors.Is(err, ErrInvalidSigner) {
		t.Errorf("nil signer: err = %v, want ErrInvalidSigner", err)
	}
}

// TestClientAssertionVerifiesWithPublishedJWKS checks the JWKS round trip:
// the key a server registers from JWKS verifies a minted assertion, under its
// kid.
func TestClientAssertionVerifiesWithPublishedJWKS(t *testing.T) {
	key := testKey(t, "broker client key")
	s, err := NewClientAssertionSigner(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(s.JWKS())
	if err != nil {
		t.Fatal(err)
	}
	var set struct {
		Keys []map[string]string `json:"keys"`
	}
	if err := json.Unmarshal(raw, &set); err != nil || len(set.Keys) != 1 {
		t.Fatalf("JWKS = %s, %v; want one key", raw, err)
	}
	jwk := set.Keys[0]
	if got := slices.Sorted(maps.Keys(jwk)); !slices.Equal(got, []string{"alg", "crv", "kid", "kty", "use", "x", "y"}) {
		t.Fatalf("JWK members = %v", got)
	}
	if jwk["kty"] != "EC" || jwk["crv"] != "P-256" || jwk["use"] != "sig" || jwk["alg"] != "ES256" || jwk["kid"] != thumbprint(t, &key.PublicKey) {
		t.Fatalf("JWK = %v", jwk)
	}
	x, xErr := base64.RawURLEncoding.DecodeString(jwk["x"])
	y, yErr := base64.RawURLEncoding.DecodeString(jwk["y"])
	if xErr != nil || yErr != nil {
		t.Fatalf("JWK coordinates: %v, %v", xErr, yErr)
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
	if err != nil || !pub.Equal(&key.PublicKey) {
		t.Fatalf("JWK key = %v, %v; want the signer's public key", pub, err)
	}
	now := time.Now()
	a, err := s.ClientAssertion(testAS, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jwt.Parse(a.Value(), func(tok *jwt.Token) (any, error) {
		if tok.Header["kid"] != jwk["kid"] {
			return nil, errors.New("kid does not select the published key")
		}
		return pub, nil
	}, jwt.WithValidMethods([]string{"ES256"}), jwt.WithAudience(testAS), jwt.WithIssuer("mecatl")); err != nil {
		t.Fatalf("verify with the published key: %v", err)
	}
}

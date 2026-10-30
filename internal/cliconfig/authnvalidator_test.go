package cliconfig

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	oidcauthn "github.com/stacklok/mecatl/authn/oidc"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestErrToSentinelPreservesRootContractAndContext(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		err      error
		want     error
		opposite error
	}{
		{name: "invalid", err: oidcauthn.ErrInvalidToken, want: server.ErrInvalidToken, opposite: server.ErrIdentityUnavailable},
		{name: "unavailable", err: oidcauthn.ErrIdentityUnavailable, want: server.ErrIdentityUnavailable, opposite: server.ErrInvalidToken},
		{name: "canceled", err: context.Canceled, want: context.Canceled, opposite: server.ErrInvalidToken},
		{name: "deadline", err: context.DeadlineExceeded, want: context.DeadlineExceeded, opposite: server.ErrInvalidToken},
		{name: "unknown fails closed", err: errors.New("unknown"), want: server.ErrInvalidToken, opposite: server.ErrIdentityUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := errToSentinel(tc.err)
			if !errors.Is(got, tc.want) {
				t.Fatalf("errToSentinel(%v) = %v, want %v", tc.err, got, tc.want)
			}
			if errors.Is(got, tc.opposite) {
				t.Fatalf("errToSentinel(%v) = %v, unexpectedly wraps %v", tc.err, got, tc.opposite)
			}
		})
	}
}

const (
	rootTestKID      = "root-test-key-1"
	rootTestAudience = "mecatl"
	rootTestSubject  = "alice"
)

type rootJWKSFixture struct {
	srv       *httptest.Server
	key       *rsa.PrivateKey
	discovery atomic.Int64
	jwks      atomic.Int64
}

func newRootJWKSFixture(t *testing.T) *rootJWKSFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	fixture := &rootJWKSFixture{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		fixture.jwks.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": rootTestKID,
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		fixture.discovery.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": fixture.srv.URL, "jwks_uri": fixture.srv.URL + "/keys",
		})
	})
	fixture.srv = httptest.NewTLSServer(mux)
	t.Cleanup(fixture.srv.Close)
	return fixture
}

func (f *rootJWKSFixture) sign(t *testing.T, audience string) string {
	t.Helper()
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": f.srv.URL, "aud": audience, "sub": rootTestSubject, "name": "Alice",
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
	})
	token.Header["kid"] = rootTestKID
	signed, err := token.SignedString(f.key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

// validator builds through OIDCValidator's nil-constructor default so these
// tests pin root composition, not only the reusable adapter beneath it.
func (f *rootJWKSFixture) validator(t *testing.T) server.PrincipalValidator {
	t.Helper()
	validator, err := OIDCValidator(context.Background(), OIDCConfig{
		Issuer: f.srv.URL, Audience: rootTestAudience, JWKSURI: f.srv.URL + "/keys",
		HTTPClient: f.srv.Client(),
	})
	if err != nil {
		t.Fatalf("OIDCValidator: %v", err)
	}
	if closer, ok := validator.(interface{ Close() error }); ok {
		t.Cleanup(func() { _ = closer.Close() })
	}
	return validator
}

func TestDefaultOIDCValidatorAcceptsSignedToken(t *testing.T) {
	fixture := newRootJWKSFixture(t)
	principal, err := fixture.validator(t).Validate(context.Background(), fixture.sign(t, rootTestAudience))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	want := session.Principal{
		Issuer: fixture.srv.URL, Subject: rootTestSubject, Name: "Alice", GrantType: session.GrantTypeUser,
	}
	if principal == nil || *principal != want {
		t.Fatalf("principal = %#v, want %#v", principal, want)
	}
}

func TestDefaultOIDCValidatorMapsWrongAudience(t *testing.T) {
	fixture := newRootJWKSFixture(t)
	_, err := fixture.validator(t).Validate(context.Background(), fixture.sign(t, "other-service"))
	if !errors.Is(err, server.ErrInvalidToken) || errors.Is(err, server.ErrIdentityUnavailable) {
		t.Fatalf("wrong audience error = %v, want server.ErrInvalidToken only", err)
	}
}

// TestDefaultOIDCValidatorPassesOwnerIdentity pins that the flags reach
// authn/oidc: the owner comes from sub_id and a token failing its preconditions
// is the server's 401-class rejection.
func TestDefaultOIDCValidatorPassesOwnerIdentity(t *testing.T) {
	fixture := newRootJWKSFixture(t)
	validator, err := OIDCValidator(context.Background(), OIDCConfig{
		Issuer: fixture.srv.URL, Audience: rootTestAudience, JWKSURI: fixture.srv.URL + "/keys",
		OwnerIdentity: oidcauthn.OwnerIdentitySubID, AcceptedClientIDs: []string{"mecatui"},
		HTTPClient: fixture.srv.Client(),
	})
	if err != nil {
		t.Fatalf("OIDCValidator: %v", err)
	}
	t.Cleanup(func() { _ = validator.(interface{ Close() error }).Close() })
	sign := func(typ, clientID string) string {
		now := time.Now()
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": fixture.srv.URL, "aud": rootTestAudience, "sub": "u-alice-91", "client_id": clientID,
			"sub_id": map[string]any{"format": "iss_sub", "iss": "https://login.example.com", "sub": rootTestSubject},
			"iat":    now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
		})
		token.Header["kid"], token.Header["typ"] = rootTestKID, typ
		signed, err := token.SignedString(fixture.key)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return signed
	}
	principal, err := validator.Validate(context.Background(), sign("at+jwt", "mecatui"))
	if err != nil || principal.Issuer != "https://login.example.com" || principal.Subject != rootTestSubject {
		t.Fatalf("Validate = (%#v, %v), want the sub_id identity", principal, err)
	}
	for _, bad := range []string{sign("JWT", "mecatui"), sign("at+jwt", "rogue")} {
		if _, err := validator.Validate(context.Background(), bad); !errors.Is(err, server.ErrInvalidToken) || errors.Is(err, server.ErrIdentityUnavailable) {
			t.Fatalf("precondition rejection = %v, want server.ErrInvalidToken only", err)
		}
	}
}

func TestDefaultOIDCValidatorUsesPinnedJWKS(t *testing.T) {
	fixture := newRootJWKSFixture(t)
	if _, err := fixture.validator(t).Validate(context.Background(), fixture.sign(t, rootTestAudience)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := fixture.discovery.Load(); got != 0 {
		t.Fatalf("discovery fetched %d times with a pinned JWKS URI", got)
	}
	if got := fixture.jwks.Load(); got == 0 {
		t.Fatal("pinned JWKS endpoint was not fetched")
	}
}

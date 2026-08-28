package oidc

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/toolhive-core/authn"
)

func TestAuthnConfigPreservesSecurityPolicy(t *testing.T) {
	t.Parallel()
	got := authnConfig(Config{
		Issuer: "https://idp.example", JWKSURI: "https://idp.example/keys",
		Audience: "mecatl", MaxJWKSStaleness: 23 * time.Minute,
	})
	if got.MaxJWKSStaleness != 23*time.Minute || len(got.Audiences) != 1 || got.Audiences[0] != "mecatl" {
		t.Fatalf("authn config did not preserve audience/staleness: %#v", got)
	}
	if got.AllowAnyAudience || got.InsecureAllowHTTP || got.AllowPrivateIP {
		t.Fatalf("secure defaults changed: %#v", got)
	}
}

func TestAuthnConfigPrivateHTTPSIssuerPolicy(t *testing.T) {
	t.Parallel()

	legacy := authnConfig(Config{InsecureAllowPrivateIssuer: true})
	if !legacy.InsecureAllowHTTP || !legacy.AllowPrivateIP || legacy.CACertPath != "" {
		t.Fatalf("legacy escape hatch changed: %#v", legacy)
	}

	privateHTTPS := authnConfig(Config{AllowPrivateHTTPSIssuer: true, TrustedCAFile: "/run/oidc/ca.pem"})
	if privateHTTPS.InsecureAllowHTTP || privateHTTPS.AllowPrivateIP || privateHTTPS.CACertPath != "/run/oidc/ca.pem" {
		t.Fatalf("private HTTPS policy delegated an unsafe private-IP relaxation: %#v", privateHTTPS)
	}
}

func TestNewValidatorPrivateHTTPSIssuerGuards(t *testing.T) {
	t.Parallel()
	base := Config{Issuer: "https://idp.example", Audience: testAudience, AllowPrivateHTTPSIssuer: true, TrustedCAFile: "/run/oidc/ca.pem"}
	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "CA required", cfg: Config{Issuer: base.Issuer, Audience: base.Audience, AllowPrivateHTTPSIssuer: true}, want: "trusted CA file is empty"},
		{name: "custom client forbidden", cfg: Config{Issuer: base.Issuer, Audience: base.Audience, AllowPrivateHTTPSIssuer: true, TrustedCAFile: base.TrustedCAFile, HTTPClient: &http.Client{}}, want: "custom HTTP client is not allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			validator, err := NewValidator(context.Background(), tc.cfg)
			if validator != nil || !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewValidator(%#v) = (%#v, %v), want nil %q ErrInvalidConfig", tc.cfg, validator, err, tc.want)
			}
		})
	}
}

func TestNewValidatorRejectsEmptyTrustInputs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "issuer", cfg: Config{Audience: testAudience}, want: "issuer is empty"},
		{name: "audience", cfg: Config{Issuer: "https://idp.example"}, want: "audience is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			validator, err := NewValidator(context.Background(), tc.cfg)
			if validator != nil || !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewValidator(%#v) = (%#v, %v), want nil %q ErrInvalidConfig", tc.cfg, validator, err, tc.want)
			}
		})
	}
}

func TestNewValidatorContainsConstructorErrors(t *testing.T) {
	t.Parallel()
	validator, err := NewValidator(context.Background(), Config{Issuer: "://bad", Audience: testAudience})
	if validator != nil || !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewValidator malformed issuer = (%#v, %v), want nil ErrInvalidConfig", validator, err)
	}
	var authnErr *authn.Error
	if errors.As(err, &authnErr) {
		t.Fatalf("constructor error exposes ToolHive type: %T", authnErr)
	}
}

func TestMapError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		err      error
		want     error
		category string
	}{
		{name: "invalid token", err: &authn.Error{Code: authn.CodeInvalidToken}, want: ErrInvalidToken, category: "invalid_token"},
		{name: "invalid request", err: &authn.Error{Code: authn.CodeInvalidRequest}, want: ErrInvalidToken, category: "invalid_token"},
		{name: "wrong audience", err: &authn.Error{Code: authn.CodeInvalidToken, Reason: authn.ReasonAudience}, want: ErrInvalidToken, category: "wrong_audience"},
		{name: "wrong issuer", err: &authn.Error{Code: authn.CodeInvalidToken, Reason: authn.ReasonIssuer}, want: ErrInvalidToken, category: "wrong_issuer"},
		{name: "unavailable", err: &authn.Error{Code: authn.CodeUnavailable}, want: ErrIdentityUnavailable, category: "invalid_token"},
		{name: "unknown authn code", err: &authn.Error{Code: authn.Code("future_code")}, want: ErrInvalidToken, category: "invalid_token"},
		{name: "unknown error", err: errors.New("unknown"), want: ErrInvalidToken, category: "invalid_token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mapError(tc.err)
			if !errors.Is(got, tc.want) {
				t.Fatalf("mapError(%v) = %v, want %v", tc.err, got, tc.want)
			}
			opposite := ErrIdentityUnavailable
			if tc.want == ErrIdentityUnavailable {
				opposite = ErrInvalidToken
			}
			if errors.Is(got, opposite) {
				t.Fatalf("mapError(%v) = %v, unexpectedly wraps %v", tc.err, got, opposite)
			}
			categorized, ok := got.(interface{ AuthenticationRejectionCategory() string })
			if !ok || categorized.AuthenticationRejectionCategory() != tc.category {
				t.Fatalf("mapError(%v) category = %#v, want %q", tc.err, categorized, tc.category)
			}
		})
	}
	for _, want := range []error{context.Canceled, context.DeadlineExceeded} {
		if got := mapError(want); !errors.Is(got, want) || errors.Is(got, ErrInvalidToken) || errors.Is(got, ErrIdentityUnavailable) {
			t.Fatalf("mapError(%v) = %v, want original context error only", want, got)
		}
	}
}

const (
	testKID      = "test-key-1"
	testAudience = "mecatl"
)

type jwksFixture struct {
	srv *httptest.Server
	key *rsa.PrivateKey
}

func newJWKSFixture(t *testing.T) *jwksFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	fixture := &jwksFixture{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": testKID,
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	fixture.srv = httptest.NewTLSServer(mux)
	t.Cleanup(fixture.srv.Close)
	return fixture
}

func TestPrivateHTTPSIssuerUsesHardenedDefaultClient(t *testing.T) {
	fixture := newJWKSFixture(t)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fixture.srv.Certificate().Raw})

	validator, err := NewValidator(context.Background(), Config{
		Issuer: fixture.srv.URL, JWKSURI: fixture.srv.URL + "/keys", Audience: testAudience,
		AllowPrivateHTTPSIssuer: true, TrustedCAFile: "/run/oidc/ca.pem", TrustedCAPEM: caPEM,
	})
	if err != nil {
		t.Fatalf("NewValidator(private HTTPS issuer): %v", err)
	}
	t.Cleanup(func() { _ = validator.Close() })

	plaintext := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(plaintext.Close)
	if _, err := NewValidator(context.Background(), Config{
		Issuer: plaintext.URL, JWKSURI: plaintext.URL, Audience: testAudience,
		AllowPrivateHTTPSIssuer: true, TrustedCAFile: "/run/oidc/ca.pem", TrustedCAPEM: caPEM,
	}); err == nil {
		t.Fatal("private HTTPS mode accepted an HTTP issuer")
	}
}

func TestPrivateHTTPSIssuerRejectsDiscoveredJWKSOnDifferentPrivateHost(t *testing.T) {
	attacker := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(attacker.Close)

	var issuerURL string
	issuer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuerURL, "jwks_uri": attacker.URL + "/keys"})
	}))
	t.Cleanup(issuer.Close)
	issuerURL = issuer.URL

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issuer.Certificate().Raw})
	if _, err := NewValidator(context.Background(), Config{
		Issuer: issuer.URL, Audience: testAudience, AllowPrivateHTTPSIssuer: true, TrustedCAFile: "/run/oidc/ca.pem", TrustedCAPEM: caPEM,
	}); err == nil || !strings.Contains(err.Error(), "not an approved endpoint") {
		t.Fatalf("NewValidator accepted discovery-provided private JWKS target: %v", err)
	}
}

func (f *jwksFixture) validator(t *testing.T) *Validator {
	t.Helper()
	validator, err := NewValidator(context.Background(), Config{
		Issuer: f.srv.URL, Audience: testAudience, JWKSURI: f.srv.URL + "/keys",
		HTTPClient: f.srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	t.Cleanup(func() { _ = validator.Close() })
	return validator
}

func (f *jwksFixture) token(t *testing.T, audience string) string {
	return f.tokenWithIssuer(t, f.srv.URL, audience)
}

func (f *jwksFixture) tokenWithIssuer(t *testing.T, issuer, audience string) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": testKID, "typ": "JWT"})
	if err != nil {
		t.Fatalf("marshal JWT header: %v", err)
	}
	now := time.Now()
	claims, err := json.Marshal(map[string]any{
		"iss": issuer, "sub": "alice-uid", "aud": audience,
		"iat": now.Unix(), "exp": now.Add(time.Minute).Unix(),
	})
	if err != nil {
		t.Fatalf("marshal JWT claims: %v", err)
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign JWT: %v", err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// TestCallerIdentityE2E_Scenario2_WrongAudienceRejected pins that a valid
// signature cannot cross a deployment boundary with a different audience.
func TestCallerIdentityE2E_Scenario2_WrongAudienceRejected(t *testing.T) {
	fixture := newJWKSFixture(t)
	validator := fixture.validator(t)

	principal, err := validator.Validate(context.Background(), fixture.token(t, "another-service"))
	if principal != nil || !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Validate(wrong audience) = (%#v, %v), want nil ErrInvalidToken", principal, err)
	}
}

func TestMecak8sKindFixture_Scenario3_AuthenticatedRequest(t *testing.T) {
	fixture := newJWKSFixture(t)
	validator, err := NewValidator(context.Background(), Config{
		Issuer: fixture.srv.URL, JWKSURI: fixture.srv.URL + "/keys", Audience: "mecak8s",
		HTTPClient: fixture.srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	t.Cleanup(func() { _ = validator.Close() })

	valid := fixture.token(t, "mecak8s")
	principal, err := validator.Validate(context.Background(), valid)
	if err != nil || principal == nil || principal.Subject != "alice-uid" {
		t.Fatalf("Validate(valid mecak8s access token) = (%#v, %v), want alice", principal, err)
	}
	for _, tc := range []struct {
		name   string
		bearer string
	}{
		{name: "absent", bearer: ""},
		{name: "forged", bearer: valid[:len(valid)-1] + "x"},
		{name: "wrong issuer", bearer: fixture.tokenWithIssuer(t, "https://wrong-issuer.example", "mecak8s")},
		{name: "wrong audience", bearer: fixture.token(t, "another-service")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			principal, err := validator.Validate(context.Background(), tc.bearer)
			if principal != nil || !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("Validate(%s) = (%#v, %v), want nil ErrInvalidToken", tc.name, principal, err)
			}
		})
	}

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fixture.srv.Certificate().Raw})
	wrongCA := append([]byte(nil), caPEM...)
	wrongCAIndex := len(wrongCA) * 3 / 4
	if wrongCA[wrongCAIndex] == 'A' {
		wrongCA[wrongCAIndex] = 'B'
	} else {
		wrongCA[wrongCAIndex] = 'A'
	}
	for _, tc := range []struct {
		name   string
		issuer string
		caPEM  []byte
	}{
		{name: "wrong hostname", issuer: strings.Replace(fixture.srv.URL, "127.0.0.1", "localhost", 1), caPEM: caPEM},
		{name: "untrusted CA", issuer: fixture.srv.URL, caPEM: wrongCA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			validator, err := NewValidator(context.Background(), Config{
				Issuer: tc.issuer, JWKSURI: tc.issuer + "/keys", Audience: "mecak8s",
				AllowPrivateHTTPSIssuer: true, TrustedCAFile: "/run/oidc/ca.pem", TrustedCAPEM: tc.caPEM,
			})
			if validator != nil || !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("NewValidator(%s) = (%#v, %v), want nil ErrInvalidConfig", tc.name, validator, err)
			}
		})
	}
}

func TestValidatorCloseIsIdempotent(t *testing.T) {
	fixture := newJWKSFixture(t)
	validator := fixture.validator(t)
	if err := validator.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := validator.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	var nilValidator *Validator
	if err := nilValidator.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
}

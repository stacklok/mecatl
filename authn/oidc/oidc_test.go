package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v4/jwk"
	"github.com/stacklok/toolhive-core/authn"
)

func TestExplicitJWKSDoesNotApproveIssuerAsNetworkEndpoint(t *testing.T) {
	fixture := newJWKSFixture(t)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fixture.srv.Certificate().Raw})
	client, err := newPrivateHTTPSClient(t.Context(), Config{Issuer: "https://issuer.example.invalid", JWKSURI: fixture.srv.URL + "/keys", TrustedCAPEM: caPEM})
	if err != nil {
		t.Fatalf("newPrivateHTTPSClient: %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)
	req := httpRequest(t, http.MethodGet, "https://issuer.example.invalid/.well-known/openid-configuration")
	if _, err := client.Do(req); err == nil || !strings.Contains(err.Error(), "not an approved endpoint") {
		t.Fatalf("explicit JWKS client allowed issuer request: %v", err)
	}
}

func TestExplicitJWKSValidatesDistinctIssuerWithoutResolvingIssuer(t *testing.T) {
	fixture := newJWKSFixture(t)
	const unreachableIssuer = "https://issuer.example.invalid"

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fixture.srv.Certificate().Raw})
	validator, err := NewValidator(t.Context(), Config{
		Issuer: unreachableIssuer, JWKSURI: fixture.srv.URL + "/keys", Audience: testAudience,
		AllowPrivateHTTPSIssuer: true, TrustedCAFile: "/run/oidc/ca.pem", TrustedCAPEM: caPEM,
	})
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	t.Cleanup(func() { _ = validator.Close() })

	if _, err := validator.Validate(t.Context(), fixture.tokenWithIssuer(t, unreachableIssuer, testAudience)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func httpRequest(t *testing.T, method, rawURL string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

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
		{name: "malformed", err: &authn.Error{Code: authn.CodeInvalidRequest, Reason: authn.ReasonMalformed}, want: ErrInvalidToken, category: "malformed"},
		{name: "signature", err: &authn.Error{Code: authn.CodeInvalidToken, Reason: authn.ReasonSignature}, want: ErrInvalidToken, category: "signature"},
		{name: "unknown kid", err: &authn.Error{Code: authn.CodeInvalidToken, Reason: authn.ReasonUnknownKID}, want: ErrInvalidToken, category: "unknown_kid"},
		{name: "expired", err: &authn.Error{Code: authn.CodeInvalidToken, Reason: authn.ReasonExpired}, want: ErrInvalidToken, category: "expired"},
		{name: "not yet valid", err: &authn.Error{Code: authn.CodeInvalidToken, Reason: authn.ReasonNotYetValid}, want: ErrInvalidToken, category: "not_yet_valid"},
		{name: "keys unavailable", err: &authn.Error{Code: authn.CodeUnavailable, Reason: authn.ReasonKeysUnavailable}, want: ErrIdentityUnavailable, category: "jwks_unavailable"},
		{name: "keys stale", err: &authn.Error{Code: authn.CodeUnavailable, Reason: authn.ReasonKeysStale}, want: ErrIdentityUnavailable, category: "jwks_stale"},
		{name: "keys unavailable contradictory code", err: &authn.Error{Code: authn.CodeInvalidToken, Reason: authn.ReasonKeysUnavailable}, want: ErrIdentityUnavailable, category: "jwks_unavailable"},
		{name: "keys stale contradictory code", err: &authn.Error{Code: authn.CodeInvalidRequest, Reason: authn.ReasonKeysStale}, want: ErrIdentityUnavailable, category: "jwks_stale"},
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

	mu   sync.Mutex
	auth map[string]string // request path -> last Authorization header
}

func (f *jwksFixture) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth[r.URL.Path] = r.Header.Get("Authorization")
}

func (f *jwksFixture) authorization(path string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.auth[path]
}

func newJWKSFixture(t *testing.T) *jwksFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	fixture := &jwksFixture{key: key, auth: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		fixture.record(r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": "https://" + r.Host, "jwks_uri": "https://discovered.example.invalid/keys"})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		fixture.record(r)
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

func TestValidatorOptionalAudiencePreservesConfiguredAudienceBinding(t *testing.T) {
	fixture := newJWKSFixture(t)

	optional, err := NewValidator(context.Background(), Config{
		Issuer: fixture.srv.URL, JWKSURI: fixture.srv.URL + "/keys",
		AllowAnyAudience: true, HTTPClient: fixture.srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewValidator without audience: %v", err)
	}
	t.Cleanup(func() { _ = optional.Close() })
	if principal, err := optional.Validate(context.Background(), fixture.token(t, "another-service")); err != nil || principal == nil {
		t.Fatalf("optional-audience Validate = (%#v, %v), want valid principal", principal, err)
	}

	strict := fixture.validator(t)
	if principal, err := strict.Validate(context.Background(), fixture.token(t, "another-service")); principal != nil || !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("configured-audience Validate = (%#v, %v), want nil ErrInvalidToken", principal, err)
	}
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

func (f *jwksFixture) writeJWKS(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": testKID,
		"n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.E)).Bytes()),
	}}})
}

// jwksUntilBroken serves a valid key set until broken is set, then answers every
// request with handle. It lets a test build a validator, then degrade the endpoint.
func jwksUntilBroken(t *testing.T, f *jwksFixture, broken *atomic.Bool, handle http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if broken.Load() {
			handle(w, r)
			return
		}
		f.writeJWKS(w)
	}))
	t.Cleanup(server.Close)
	return server
}

// This guards the wiring in readinessClient (the builder must be told to refuse
// private addresses); the blocking itself belongs to toolhive-core/networking.
func TestReadinessClientRefusesPrivateAddressesByDefault(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)

	client, err := readinessClient(Config{JWKSURI: server.URL})
	if err != nil {
		t.Fatalf("readinessClient: %v", err)
	}
	if response, err := client.Get(server.URL); err == nil {
		_ = response.Body.Close()
		t.Fatal("default readiness client reached a loopback server")
	}
}

// The broker's explicit-JWKS mode: private HTTPS, pinned JWKS, no bootstrap.
func TestReadyThroughPrivateHTTPSClient(t *testing.T) {
	fixture := newJWKSFixture(t)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fixture.srv.Certificate().Raw})
	validator, err := NewValidator(t.Context(), Config{
		Issuer: "https://issuer.example.invalid", JWKSURI: fixture.srv.URL + "/keys", Audience: testAudience,
		AllowPrivateHTTPSIssuer: true, TrustedCAFile: "/run/ca.pem", TrustedCAPEM: caPEM,
	})
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	t.Cleanup(func() { _ = validator.Close() })
	if err := validator.Ready(t.Context()); err != nil {
		t.Fatalf("Ready: %v", err)
	}
}

// Ready serves the endpoint health of the key set: it must accept a usable key
// set and reject every other answer, without following redirects. Each case
// builds a validator against a server that is healthy at construction and then
// switches to the case's answer, so Ready is always reached.
func TestReady(t *testing.T) {
	fixture := newJWKSFixture(t)
	body := func(s string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(s)) }
	}
	for _, tc := range []struct {
		name    string
		after   http.HandlerFunc
		wantErr bool
	}{
		{"usable keys", func(w http.ResponseWriter, _ *http.Request) { fixture.writeJWKS(w) }, false},
		{"server error", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }, true},
		{"redirect to usable keys", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/followed", http.StatusFound) }, true},
		{"not JSON", body("not-json"), true},
		{"empty key set", body(`{"keys":[]}`), true},
		{"key without material", body(`{"keys":[{"kid":"missing-material","kty":"RSA"}]}`), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var broken atomic.Bool
			var followed atomic.Int32
			server := jwksUntilBroken(t, fixture, &broken, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/followed" {
					followed.Add(1)
					fixture.writeJWKS(w)
					return
				}
				tc.after(w, r)
			})
			validator, err := NewValidator(t.Context(), Config{Issuer: server.URL, JWKSURI: server.URL, Audience: testAudience, HTTPClient: server.Client()})
			if err != nil {
				t.Fatalf("NewValidator: %v", err)
			}
			t.Cleanup(func() { _ = validator.Close() })
			broken.Store(true)
			if err := validator.Ready(t.Context()); (err != nil) != tc.wantErr {
				t.Fatalf("Ready = %v, want error: %v", err, tc.wantErr)
			}
			if followed.Load() != 0 {
				t.Fatal("Ready followed a redirect")
			}
		})
	}
}

func jwkJSON(t *testing.T, pub any) string {
	t.Helper()
	key, err := jwk.Import[jwk.Key](pub)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestUsableJWKS(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	set := func(keys ...string) string { return `{"keys":[` + strings.Join(keys, ",") + `]}` }
	goodRSA, goodEC := jwkJSON(t, &rsaKey.PublicKey), jwkJSON(t, &ecKey.PublicKey)
	const notAKey = `{"kty":"RSA","n":"n","e":"e"}`

	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"RSA", set(goodRSA), true},
		{"EC", set(goodEC), true},
		{"EdDSA is not accepted by toolhive-core", set(jwkJSON(t, edPub)), false},
		{"usable key after an unusable one", set(notAKey, goodRSA), true},
		{"fields present but not a key", set(notAKey), false},
		{"RSA without exponent", set(`{"kty":"RSA","n":"n"}`), false},
		{"EC without y", set(`{"kty":"EC","crv":"P-256","x":"x"}`), false},
		{"unknown key type", set(`{"kty":"oct","k":"c2VjcmV0"}`), false},
		{"empty key set", set(), false},
		{"not JSON", `not-json`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := usableJWKS([]byte(tc.body)); got != tc.want {
				t.Fatalf("usableJWKS(%s) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// kubernetesTestConfig writes a CA file for srv and a token file holding token,
// and returns a configuration that bootstraps against srv.
func kubernetesTestConfig(t *testing.T, srv *httptest.Server, token string) (Config, KubernetesBootstrapConfig) {
	t.Helper()
	dir := t.TempDir()
	caFile, tokenFile := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "token")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	for path, data := range map[string][]byte{caFile: caPEM, tokenFile: []byte(token + "\n")} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Config{Audience: testAudience, TrustedCAFile: caFile}, KubernetesBootstrapConfig{
		DiscoveryURL: srv.URL + "/.well-known/openid-configuration", JWKSURI: srv.URL + "/keys",
		TokenFile:   tokenFile,
		TokenSource: func() ([]byte, error) { return os.ReadFile(tokenFile) },
	}
}

// The bootstrap derives the issuer from the projected token, sends that token to
// both endpoints, and then accepts only tokens from that issuer. Ready sends a
// rotated token.
func TestKubernetesValidatorDerivesIssuerAndAuthenticatesEndpoints(t *testing.T) {
	fixture := newJWKSFixture(t)
	projected := fixture.tokenWithIssuer(t, fixture.srv.URL, testAudience)
	cfg, bootstrap := kubernetesTestConfig(t, fixture.srv, projected)
	validator, err := NewKubernetesValidator(t.Context(), cfg, bootstrap)
	if err != nil {
		t.Fatalf("NewKubernetesValidator: %v", err)
	}
	t.Cleanup(func() { _ = validator.Close() })

	if _, err := validator.Validate(t.Context(), projected); err != nil {
		t.Fatalf("Validate(token from the derived issuer): %v", err)
	}
	if _, err := validator.Validate(t.Context(), fixture.tokenWithIssuer(t, "https://other.example.invalid", testAudience)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Validate(token from another issuer) = %v, want ErrInvalidToken", err)
	}
	for _, path := range []string{"/.well-known/openid-configuration", "/keys"} {
		if got := fixture.authorization(path); got != "Bearer "+projected {
			t.Fatalf("Authorization sent to %s does not carry the projected token", path)
		}
	}

	if err := os.WriteFile(bootstrap.TokenFile, []byte("rotated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validator.Ready(t.Context()); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if got := fixture.authorization("/keys"); got != "Bearer rotated" {
		t.Fatalf("Ready sent %q after rotation, want the rotated token", got)
	}
}

func TestKubernetesValidatorRejectsIssuerDiscoveryDoesNotConfirm(t *testing.T) {
	fixture := newJWKSFixture(t)
	foreign := fixture.tokenWithIssuer(t, "https://other.example.invalid", testAudience)
	cfg, bootstrap := kubernetesTestConfig(t, fixture.srv, foreign)
	if _, err := NewKubernetesValidator(t.Context(), cfg, bootstrap); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewKubernetesValidator = %v, want ErrInvalidConfig", err)
	}
}

func TestKubernetesValidatorRejectsIncompleteConfiguration(t *testing.T) {
	fixture := newJWKSFixture(t)
	projected := fixture.tokenWithIssuer(t, fixture.srv.URL, testAudience)
	for name, mutate := range map[string]func(*Config, *KubernetesBootstrapConfig){
		"no token file":          func(_ *Config, b *KubernetesBootstrapConfig) { b.TokenFile = "" },
		"missing token file":     func(_ *Config, b *KubernetesBootstrapConfig) { b.TokenFile += ".missing" },
		"no token source":        func(_ *Config, b *KubernetesBootstrapConfig) { b.TokenSource = nil },
		"no discovery URL":       func(_ *Config, b *KubernetesBootstrapConfig) { b.DiscoveryURL = "" },
		"no JWKS URL":            func(_ *Config, b *KubernetesBootstrapConfig) { b.JWKSURI = "" },
		"issuer preset":          func(c *Config, _ *KubernetesBootstrapConfig) { c.Issuer = "https://preset.example" },
		"JWKS URL preset":        func(c *Config, _ *KubernetesBootstrapConfig) { c.JWKSURI = "https://preset.example/keys" },
		"no CA file":             func(c *Config, _ *KubernetesBootstrapConfig) { c.TrustedCAFile = "" },
		"empty audience":         func(c *Config, _ *KubernetesBootstrapConfig) { c.Audience = "" },
		"any audience permitted": func(c *Config, _ *KubernetesBootstrapConfig) { c.Audience, c.AllowAnyAudience = "", true },
		"any audience with one":  func(c *Config, _ *KubernetesBootstrapConfig) { c.AllowAnyAudience = true },
		"caller HTTP client":     func(c *Config, _ *KubernetesBootstrapConfig) { c.HTTPClient = http.DefaultClient },
		"insecure private mode":  func(c *Config, _ *KubernetesBootstrapConfig) { c.InsecureAllowPrivateIssuer = true },
		"scoped private mode":    func(c *Config, _ *KubernetesBootstrapConfig) { c.AllowPrivateHTTPSIssuer = true },
	} {
		t.Run(name, func(t *testing.T) {
			cfg, bootstrap := kubernetesTestConfig(t, fixture.srv, projected)
			mutate(&cfg, &bootstrap)
			if _, err := NewKubernetesValidator(t.Context(), cfg, bootstrap); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("NewKubernetesValidator = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestKubernetesValidatorRejectsUnusableProjectedTokens(t *testing.T) {
	fixture := newJWKSFixture(t)
	payload := func(claims string) string {
		return "e30." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".c2ln"
	}
	const secret = "projected-token-must-not-leak"
	for name, source := range map[string]func() ([]byte, error){
		"empty":            func() ([]byte, error) { return nil, nil },
		"oversize":         func() ([]byte, error) { return []byte(strings.Repeat("a", maxKubernetesTokenBytes+1)), nil },
		"not a JWT":        func() ([]byte, error) { return []byte(secret), nil },
		"bad claims":       func() ([]byte, error) { return []byte("e30." + secret + ".c2ln"), nil },
		"no issuer":        func() ([]byte, error) { return []byte(payload(`{"sub":"` + secret + `"}`)), nil },
		"source error":     func() ([]byte, error) { return nil, errors.New(secret) },
		"claims not JSON":  func() ([]byte, error) { return []byte(payload("not-json-" + secret)), nil },
		"issuer not given": func() ([]byte, error) { return []byte(payload(`{"iss":""}`)), nil },
	} {
		t.Run(name, func(t *testing.T) {
			cfg, bootstrap := kubernetesTestConfig(t, fixture.srv, fixture.token(t, testAudience))
			bootstrap.TokenSource = source
			_, err := NewKubernetesValidator(t.Context(), cfg, bootstrap)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("NewKubernetesValidator = %v, want ErrInvalidConfig", err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error exposes token material: %v", err)
			}
		})
	}
}

func TestKubernetesValidatorRejectsBadDiscoveryResponses(t *testing.T) {
	fixture := newJWKSFixture(t)
	projected := fixture.tokenWithIssuer(t, "https://issuer.example.invalid", testAudience)
	for name, handler := range map[string]http.HandlerFunc{
		"not found": http.NotFound,
		"bad JSON":  func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not json")) },
		"no issuer": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"jwks_uri":"https://x.invalid/keys"}`))
		},
		"oversize": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"issuer":"` + strings.Repeat("a", maxIdentityDocumentBytes) + `"}`))
		},
		"redirect": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/keys", http.StatusFound) },
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewTLSServer(handler)
			t.Cleanup(server.Close)
			cfg, bootstrap := kubernetesTestConfig(t, server, projected)
			if _, err := NewKubernetesValidator(t.Context(), cfg, bootstrap); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("NewKubernetesValidator = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestKubernetesBootstrapRequiresSameHTTPSOrigin(t *testing.T) {
	const discovery = "https://kubernetes.example/.well-known/openid-configuration"
	for name, tc := range map[string]struct {
		discovery, jwks string
		wantErr         bool
	}{
		"same origin":           {discovery, "https://kubernetes.example/openid/v1/jwks", false},
		"explicit default port": {"https://kubernetes.example:443/.well-known/openid-configuration", "https://kubernetes.example/openid/v1/jwks", false},
		"different host":        {discovery, "https://other.example/openid/v1/jwks", true},
		"different port":        {discovery, "https://kubernetes.example:8443/openid/v1/jwks", true},
		"discovery over HTTP":   {"http://kubernetes.example/.well-known/openid-configuration", "https://kubernetes.example/openid/v1/jwks", true},
		"JWKS with credentials": {discovery, "https://user@kubernetes.example/openid/v1/jwks", true},
		"JWKS with fragment":    {discovery, "https://kubernetes.example/openid/v1/jwks#frag", true},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateKubernetesEndpointOrigin(tc.discovery, tc.jwks); (err != nil) != tc.wantErr {
				t.Fatalf("validateKubernetesEndpointOrigin() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// The bearer must not follow a redirect, even to a valid key set: the redirect
// target would receive the projected token.
func TestKubernetesValidatorDoesNotFollowRedirects(t *testing.T) {
	fixture := newJWKSFixture(t)
	mux := http.NewServeMux()
	var serverURL string
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": serverURL})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/real", http.StatusFound) })
	mux.HandleFunc("/real", func(w http.ResponseWriter, _ *http.Request) { fixture.writeJWKS(w) })
	redirecting := httptest.NewTLSServer(mux)
	t.Cleanup(redirecting.Close)
	serverURL = redirecting.URL

	cfg, bootstrap := kubernetesTestConfig(t, redirecting, fixture.tokenWithIssuer(t, redirecting.URL, testAudience))
	if _, err := NewKubernetesValidator(t.Context(), cfg, bootstrap); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewKubernetesValidator = %v, want ErrInvalidConfig because the redirect is not followed", err)
	}
}

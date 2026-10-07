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

func TestKubernetesBootstrapDerivesIssuerWithAuthenticatedFixedEndpoints(t *testing.T) {
	fixture := newJWKSFixture(t)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fixture.srv.Certificate().Raw})
	projected := fixture.tokenWithIssuer(t, fixture.srv.URL, testAudience)
	calls := 0
	validator, err := NewKubernetesValidator(t.Context(), Config{
		Audience: testAudience, AllowPrivateHTTPSIssuer: true, TrustedCAFile: "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt", TrustedCAPEM: caPEM,
	}, KubernetesBootstrapConfig{
		DiscoveryURL: fixture.srv.URL + "/.well-known/openid-configuration", JWKSURI: fixture.srv.URL + "/keys",
		TokenSource: func() ([]byte, error) { calls++; return []byte(projected + "\n"), nil },
	})
	if err != nil {
		t.Fatalf("NewKubernetesValidator: %v", err)
	}
	t.Cleanup(func() { _ = validator.Close() })
	if _, err := validator.Validate(t.Context(), projected); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if calls < 2 { // discovery and JWKS each obtain a fresh projected token.
		t.Fatalf("token source calls = %d, want at least 2", calls)
	}
}

func TestKubernetesBootstrapRejectsIssuerMismatchAndSanitizesTokenErrors(t *testing.T) {
	fixture := newJWKSFixture(t)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fixture.srv.Certificate().Raw})
	projected := fixture.tokenWithIssuer(t, fixture.srv.URL, testAudience)
	_, err := NewKubernetesValidator(t.Context(), Config{Audience: testAudience, AllowPrivateHTTPSIssuer: true, TrustedCAFile: "/run/ca.pem", TrustedCAPEM: caPEM}, KubernetesBootstrapConfig{
		DiscoveryURL: fixture.srv.URL + "/.well-known/openid-configuration", JWKSURI: fixture.srv.URL + "/wrong-keys",
		TokenSource: func() ([]byte, error) { return []byte(projected), nil },
	})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("issuer/JWKS mismatch = %v, want ErrInvalidConfig", err)
	}
	secret := "projected-token-must-not-leak"
	_, err = NewKubernetesValidator(t.Context(), Config{Audience: testAudience, AllowPrivateHTTPSIssuer: true, TrustedCAFile: "/run/ca.pem", TrustedCAPEM: caPEM}, KubernetesBootstrapConfig{
		DiscoveryURL: fixture.srv.URL + "/.well-known/openid-configuration", JWKSURI: fixture.srv.URL + "/keys",
		TokenSource: func() ([]byte, error) { return nil, errors.New(secret) },
	})
	if !errors.Is(err, ErrInvalidConfig) || strings.Contains(err.Error(), secret) {
		t.Fatalf("token-source error = %v, want sanitized ErrInvalidConfig", err)
	}
}

func TestKubernetesBootstrapRequiresSameHTTPSOrigin(t *testing.T) {
	for name, tc := range map[string]struct {
		discovery string
		jwks      string
		wantErr   bool
	}{
		"same host with different paths": {
			discovery: "https://kubernetes.example:443/.well-known/openid-configuration",
			jwks:      "https://KUBERNETES.example/openid/v1/jwks",
		},
		"different host": {
			discovery: "https://kubernetes.example/.well-known/openid-configuration",
			jwks:      "https://other.example/openid/v1/jwks",
			wantErr:   true,
		},
		"different effective port": {
			discovery: "https://kubernetes.example/.well-known/openid-configuration",
			jwks:      "https://kubernetes.example:8443/openid/v1/jwks",
			wantErr:   true,
		},
		"non HTTPS": {
			discovery: "http://kubernetes.example/.well-known/openid-configuration",
			jwks:      "https://kubernetes.example/openid/v1/jwks",
			wantErr:   true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateKubernetesEndpointOrigin(tc.discovery, tc.jwks)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateKubernetesEndpointOrigin() error = %v, want error: %v", err, tc.wantErr)
			}
		})
	}
}

func TestKubernetesBearerTransportForwardsCloseIdleConnections(t *testing.T) {
	closed := false
	transport := kubernetesBearerTransport{next: closeIdleRoundTripper{closed: &closed}}

	transport.CloseIdleConnections()

	if !closed {
		t.Fatal("CloseIdleConnections was not forwarded")
	}
}

func TestKubernetesBearerTransportRejectsUnapprovedOrAnonymousRequests(t *testing.T) {
	called := false
	transport := kubernetesBearerTransport{next: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		called = req.Header.Get("Authorization") == "Bearer a.b.c"
		return nil, errors.New("stop")
	}), source: func() ([]byte, error) { return []byte("a.b.c"), nil }, endpoints: []string{"https://kubernetes.example/openid/v1/jwks"}}
	approved := httpRequest(t, http.MethodGet, "https://kubernetes.example/openid/v1/jwks")
	if _, err := transport.RoundTrip(approved); err == nil || !called {
		t.Fatal("approved Kubernetes request did not receive a fresh Authorization header")
	}
	called = false
	for _, req := range []*http.Request{
		httpRequest(t, http.MethodPost, "https://kubernetes.example/openid/v1/jwks"),
		httpRequest(t, http.MethodGet, "https://issuer-from-token.example/openid/v1/jwks"),
	} {
		if _, err := transport.RoundTrip(req); err == nil {
			t.Fatal("unapproved Kubernetes request was allowed")
		}
	}
	if called {
		t.Fatal("unapproved request received an Authorization header")
	}
}

type closeIdleRoundTripper struct {
	closed *bool
}

func (closeIdleRoundTripper) RoundTrip(*http.Request) (*http.Response, error) { return nil, nil }
func (r closeIdleRoundTripper) CloseIdleConnections()                         { *r.closed = true }

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
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
}

func newJWKSFixture(t *testing.T) *jwksFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	fixture := &jwksFixture{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": "https://" + r.Host, "jwks_uri": "https://discovered.example.invalid/keys"})
	})
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

func TestReadyRejectsMalformedOrEmptyJWKS(t *testing.T) {
	for _, body := range []string{"not-json", `{"keys":[]}`, `{"keys":[{"kid":"missing-material","kty":"RSA"}]}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			validator, err := NewValidator(t.Context(), Config{Issuer: server.URL, JWKSURI: server.URL, Audience: testAudience, HTTPClient: server.Client()})
			if err != nil {
				return // Constructor validation is an equally fail-closed JWKS readiness path.
			}
			defer validator.Close()
			if err := validator.Ready(t.Context()); err == nil {
				t.Fatal("Ready accepted unusable JWKS")
			}
		})
	}
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

package oidc

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
		name string
		err  error
		want error
	}{
		{name: "invalid token", err: &authn.Error{Code: authn.CodeInvalidToken}, want: ErrInvalidToken},
		{name: "invalid request", err: &authn.Error{Code: authn.CodeInvalidRequest}, want: ErrInvalidToken},
		{name: "unavailable", err: &authn.Error{Code: authn.CodeUnavailable}, want: ErrIdentityUnavailable},
		{name: "unknown authn code", err: &authn.Error{Code: authn.Code("future_code")}, want: ErrInvalidToken},
		{name: "unknown error", err: errors.New("unknown"), want: ErrInvalidToken},
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
}

func newJWKSFixture(t *testing.T) *jwksFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	fixture := &jwksFixture{}
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

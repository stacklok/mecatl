package oidc_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stacklok/mecatl/authn/oidc"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type unavailableValidator struct{}

func (unavailableValidator) Validate(context.Context, string) (*session.Principal, error) {
	return nil, server.ErrIdentityUnavailable
}

// TestADR_0205_KeycloakFixtureJWKSStaleness pins the two fail-closed JWKS
// boundaries used by the optional Keycloak fixture: the initial fetch prevents
// startup, while a refresh outage is retriable infrastructure failure (503),
// never an unauthenticated fallback or a 401 token classification.
func TestADR_0205_KeycloakFixtureJWKSStaleness(t *testing.T) {
	initial := httptest.NewTLSServer(http.NotFoundHandler())
	url, client := initial.URL, initial.Client()
	initial.Close()

	validator, err := oidc.NewValidator(context.Background(), oidc.Config{
		Issuer: url, JWKSURI: url + "/keys", Audience: "mecak8s", HTTPClient: client,
	})
	if validator != nil || !errors.Is(err, oidc.ErrInvalidConfig) {
		t.Fatalf("NewValidator(initial JWKS outage) = (%#v, %v), want nil ErrInvalidConfig", validator, err)
	}

	auth := server.NewAuthenticator(server.SecurityConfig{Validator: unavailableValidator{}})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer valid-token-with-stale-keys")
	response := httptest.NewRecorder()
	handlerCalled := false
	auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		handlerCalled = true
	})).ServeHTTP(response, req)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("stale JWKS response = %d, want 503", response.Code)
	}
	if handlerCalled {
		t.Fatal("authenticated API handler ran after a JWKS availability failure")
	}
}

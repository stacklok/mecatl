package server_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stacklok/mecatl/authn/oidc"
)

// TestADR_0205_InitialJWKSOutagePreventsValidatorStartup pins the authn
// module's fail-closed startup boundary: an unavailable initial JWKS fetch
// prevents validator startup. The root server test
// TestCallerIdentity_Scenario1_JWKSDownIsTransientNotUnauthorized independently
// pins post-start outage mapping (gRPC Unavailable and HTTP 503), handler
// exclusion, and the distinction from 401-class token rejection.
func TestADR_0205_InitialJWKSOutagePreventsValidatorStartup(t *testing.T) {
	initial := httptest.NewTLSServer(http.NotFoundHandler())
	url, client := initial.URL, initial.Client()
	initial.Close()

	validator, err := oidc.NewValidator(context.Background(), oidc.Config{
		Issuer: url, JWKSURI: url + "/keys", Audience: "mecak8s", HTTPClient: client,
	})
	if validator != nil || !errors.Is(err, oidc.ErrInvalidConfig) {
		t.Fatalf("NewValidator(initial JWKS outage) = (%#v, %v), want nil ErrInvalidConfig", validator, err)
	}
}

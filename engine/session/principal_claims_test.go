package session_test

import (
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestPrincipalFromClaims(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		claims map[string]any
		want   *session.Principal
	}{
		{name: "nil claims"},
		{name: "missing issuer", claims: map[string]any{"sub": "alice"}},
		{name: "empty issuer", claims: map[string]any{"iss": "", "sub": "alice"}},
		{name: "non-string issuer", claims: map[string]any{"iss": 42, "sub": "alice"}},
		{name: "missing subject", claims: map[string]any{"iss": "https://idp.example"}},
		{name: "empty subject", claims: map[string]any{"iss": "https://idp.example", "sub": ""}},
		{name: "non-string subject", claims: map[string]any{"iss": "https://idp.example", "sub": 42}},
		{
			name:   "projects byte-exact user identity",
			claims: map[string]any{"iss": " https://IDP.example/ ", "sub": " alice\n", "name": " Ada ", "azp": "client"},
			want: &session.Principal{
				Issuer: " https://IDP.example/ ", Subject: " alice\n", Name: " Ada ", GrantType: session.GrantTypeUser,
			},
		},
		{
			name:   "ignores non-string name and derives machine grant",
			claims: map[string]any{"iss": "issuer", "sub": "service", "name": 42, "client_id": "service"},
			want:   &session.Principal{Issuer: "issuer", Subject: "service", GrantType: session.GrantTypeClientCredentials},
		},
		{
			name:   "cannot fabricate system",
			claims: map[string]any{"iss": "issuer", "sub": "caller", "grant_type": "system"},
			want:   &session.Principal{Issuer: "issuer", Subject: "caller", GrantType: session.GrantTypeUser},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := session.PrincipalFromClaims(tc.claims); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("PrincipalFromClaims(%v) = %#v, want %#v", tc.claims, got, tc.want)
			}
		})
	}
}

func TestGrantTypeFromClaims(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		claims map[string]any
		want   session.GrantType
	}{
		{name: "nil", want: session.GrantTypeUser},
		{name: "authorization code", claims: map[string]any{"grant_type": "authorization_code"}, want: session.GrantTypeUser},
		{name: "other user grant", claims: map[string]any{"grant_type": "urn:ietf:params:oauth:grant-type:device_code"}, want: session.GrantTypeUser},
		{name: "client credentials", claims: map[string]any{"grant_type": "CLIENT_CREDENTIALS"}, want: session.GrantTypeClientCredentials},
		{name: "auth0 spelling", claims: map[string]any{"gty": "client-credentials"}, want: session.GrantTypeClientCredentials},
		{name: "grant_type takes precedence over gty", claims: map[string]any{"grant_type": "authorization_code", "gty": "client_credentials"}, want: session.GrantTypeUser},
		{name: "explicit user grant beats equal client identity", claims: map[string]any{"sub": "svc", "grant_type": "authorization_code", "azp": "svc"}, want: session.GrantTypeUser},
		{name: "azp equals sub", claims: map[string]any{"sub": "svc", "azp": "svc"}, want: session.GrantTypeClientCredentials},
		{name: "client_id equals sub", claims: map[string]any{"sub": "svc", "client_id": "svc"}, want: session.GrantTypeClientCredentials},
		{name: "cid equals sub", claims: map[string]any{"sub": "svc", "cid": "svc"}, want: session.GrantTypeClientCredentials},
		{name: "empty values do not match", claims: map[string]any{"sub": "", "azp": ""}, want: session.GrantTypeUser},
		{name: "non-string ignored", claims: map[string]any{"grant_type": 42}, want: session.GrantTypeUser},
		{name: "hostile system defaults user", claims: map[string]any{"grant_type": "system"}, want: session.GrantTypeUser},
		{name: "unknown falls through", claims: map[string]any{"sub": "svc", "grant_type": "custom", "azp": "svc"}, want: session.GrantTypeClientCredentials},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := session.GrantTypeFromClaims(tc.claims)
			if got != tc.want {
				t.Fatalf("GrantTypeFromClaims(%v) = %q, want %q", tc.claims, got, tc.want)
			}
			if !got.Valid() || got == session.GrantTypeSystem {
				t.Fatalf("GrantTypeFromClaims(%v) returned inadmissible grant %q", tc.claims, got)
			}
		})
	}
}

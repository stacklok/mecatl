package oidcclient_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/oidcclient"
)

func TestNativeEndpointRefreshAndRevocationStayValueFree(t *testing.T) {
	var issuer *httptest.Server
	issuer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token", "jwks_uri": issuer.URL + "/keys", "revocation_endpoint": issuer.URL + "/revoke", "code_challenge_methods_supported": []string{"S256"}})
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "next-access", "refresh_token": "next-refresh", "token_type": "Bearer", "expires_in": 60})
		case "/revoke":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	cfg := oidcclient.Config{Issuer: issuer.URL, ClientID: "client", Audience: "gateway", Scopes: []string{"offline_access"}, HTTPClient: issuer.Client(), ValidateAccessToken: func(context.Context, string) error { return nil }}
	tok, err := oidcclient.Refresh(t.Context(), cfg, "refresh-canary")
	if err != nil || tok.AccessToken != "next-access" {
		t.Fatalf("Refresh = %+v, %v", tok, err)
	}
	if err := oidcclient.Revoke(t.Context(), cfg, "next-refresh", "refresh_token"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
}

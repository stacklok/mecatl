package oidcclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/internal/adapter/oidcclient"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

func TestInvariant_native_oidc_discovered_endpoints_stay_on_issuer_origin(t *testing.T) {
	var requests atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if r.URL.String() != "https://issuer.example/.well-known/openid-configuration" {
			return nil, errors.New("off-origin endpoint was contacted")
		}
		body := `{"issuer":"https://issuer.example","authorization_endpoint":"https://attacker.example/authorize","token_endpoint":"https://attacker.example/token","jwks_uri":"https://attacker.example/keys","code_challenge_methods_supported":["S256"]}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	presented := false
	_, err := oidcclient.AuthorizationCode(t.Context(), oidcclient.Config{
		Issuer: "https://issuer.example", ClientID: "client", Audience: "gateway", RedirectURI: "http://127.0.0.1:18473/oauth/callback", Scopes: []string{"offline_access"}, HTTPClient: client,
		Present: func(context.Context, string) (oauthlogin.Result, error) {
			presented = true
			return oauthlogin.Result{}, nil
		},
	})
	if !errors.Is(err, oidcclient.ErrDiscovery) || requests.Load() != 1 || presented {
		t.Fatalf("off-origin metadata err=%v requests=%d presented=%v", err, requests.Load(), presented)
	}
}

func TestInvariant_native_oidc_invalid_grant_remains_structured(t *testing.T) {
	var issuer *httptest.Server
	issuer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token", "jwks_uri": issuer.URL + "/keys", "code_challenge_methods_supported": []string{"S256"}})
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh-canary"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	_, err := oidcclient.Refresh(t.Context(), oidcclient.Config{Issuer: issuer.URL, ClientID: "client", Audience: "gateway", Scopes: []string{"offline_access"}, HTTPClient: issuer.Client(), ValidateAccessToken: func(context.Context, string) error { return nil }}, "refresh-canary")
	var retrieve *oauth2.RetrieveError
	if !errors.As(err, &retrieve) || retrieve.ErrorCode != "invalid_grant" || strings.Contains(err.Error(), "refresh-canary") {
		t.Fatalf("invalid_grant was not safely structured: %T %v", err, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

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

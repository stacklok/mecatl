package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/server"
)

func protectedResourceProfile() server.ProtectedResourceProfile {
	return server.ProtectedResourceProfile{
		Resource: "https://api.example.com/mecatl/v1",
		Issuer:   "https://issuer.example.com",
		Audience: "api://mecatl",
		ClientID: "public-client-id",
		Scopes:   []string{"profile", "read"},
	}
}

func TestADR_0290_ProtectedResourceMetadata(t *testing.T) {
	h := server.NewProtectedResourceHandler(protectedResourceProfile())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/mecatl/v1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metadata status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}

func TestADR_0290_MetadataFields(t *testing.T) {
	h := server.NewProtectedResourceHandler(protectedResourceProfile())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/mecatl/v1", nil))
	body := rec.Body.String()

	var got map[string]any
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&got); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	want := map[string]any{
		"resource":                      "https://api.example.com/mecatl/v1",
		"authorization_servers":         []any{"https://issuer.example.com"},
		"scopes_supported":              []any{"profile", "read"},
		"com.stacklok.mecatl.audience":  "api://mecatl",
		"com.stacklok.mecatl.client_id": "public-client-id",
	}
	for key, value := range want {
		if got[key] == nil || !jsonEqual(got[key], value) {
			t.Errorf("metadata[%q] = %#v, want %#v", key, got[key], value)
		}
	}
	for _, forbidden := range []string{"secret", "ca-cert", "127.0.0.1:808", "private-key"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("metadata leaked %q: %s", forbidden, body)
		}
	}
}

func TestADR_0290_WellKnownRouting(t *testing.T) {
	h := server.NewProtectedResourceHandler(protectedResourceProfile())
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/.well-known/oauth-protected-resource/mecatl/v1", http.StatusOK},
		{http.MethodGet, "/mecatl/v1/.well-known/oauth-protected-resource", http.StatusNotFound},
		{http.MethodGet, "/.well-known/oauth-protected-resource/mecatl/v1/extra", http.StatusNotFound},
		{http.MethodGet, "/.well-known/oauth-protected-resource/mecatl/v1?ignored=true", http.StatusNotFound},
		{http.MethodPost, "/.well-known/oauth-protected-resource/mecatl/v1", http.StatusMethodNotAllowed},
	} {
		t.Run(tc.method+"_"+strings.ReplaceAll(tc.path, "/", "_"), func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestADR_0290_WellKnownPathDerivation(t *testing.T) {
	for _, tc := range []struct{ resource, want string }{
		{"https://api.example.com", "https://api.example.com/.well-known/oauth-protected-resource"},
		{"https://api.example.com/mecatl/v1", "https://api.example.com/.well-known/oauth-protected-resource/mecatl/v1"},
	} {
		if got := server.WellKnownProtectedResourceURL(tc.resource); got != tc.want {
			t.Errorf("WellKnownProtectedResourceURL(%q) = %q, want %q", tc.resource, got, tc.want)
		}
	}
}

func TestADR_0290_MetadataDisabledCompatibility(t *testing.T) {
	if h := server.NewProtectedResourceHandler(server.ProtectedResourceProfile{}); h != nil {
		t.Fatal("disabled profile registered a metadata handler")
	}
	auth := server.NewAuthenticator(server.SecurityConfig{AuthToken: "static-token"})
	defer auth.Close()
	rec := httptest.NewRecorder()
	auth.Middleware(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unrelated", nil))
	if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("static-token challenge = %q, want bare Bearer", got)
	}
}

func TestADR_0290_ChallengeMatrix(t *testing.T) {
	metadataURL := server.WellKnownProtectedResourceURL("https://api.example.com/mecatl/v1")
	auth := server.NewAuthenticator(server.SecurityConfig{AuthToken: "static-token", ResourceMetadataURL: metadataURL})
	defer auth.Close()
	rec := httptest.NewRecorder()
	auth.Middleware(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/sessions", nil))
	if got, want := rec.Header().Get("WWW-Authenticate"), `Bearer resource_metadata="https://api.example.com/.well-known/oauth-protected-resource/mecatl/v1"`; got != want {
		t.Fatalf("challenge = %q, want %q", got, want)
	}
}

func TestADR_0290_ToolHiveParity(t *testing.T) {
	h := server.NewProtectedResourceHandler(server.ProtectedResourceProfile{
		Resource: "https://api.example.com",
		Issuer:   "https://issuer.example.com",
		Audience: "audience",
		ClientID: "client",
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil))
	var got map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["resource"] != "https://api.example.com" || !jsonEqual(got["authorization_servers"], []any{"https://issuer.example.com"}) {
		t.Fatalf("standard ToolHive-compatible fields = %#v", got)
	}
	if _, exists := got["scopes_supported"]; exists {
		t.Fatalf("default scopes unexpectedly advertised: %#v", got)
	}
}

func jsonEqual(got, want any) bool {
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	return string(gotJSON) == string(wantJSON)
}

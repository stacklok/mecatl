package mcp

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestDirectMCPOnboarding_Scenario1_ChallengeParsingAndMetadataPolicy(t *testing.T) {
	metadata, err := directResourceMetadata([]string{`Bearer realm="example, protected", resource_metadata="https://mcp.example/.well-known/oauth-protected-resource"`, `Bearer resource_metadata="https://mcp.example/.well-known/oauth-protected-resource"`})
	if err != nil || metadata != "https://mcp.example/.well-known/oauth-protected-resource" {
		t.Fatalf("metadata = %q, %v", metadata, err)
	}
	if _, err := directResourceMetadata([]string{`Bearer resource_metadata="https://one.example/meta"`, `Bearer resource_metadata="https://two.example/meta"`}); err == nil {
		t.Fatal("conflicting metadata was accepted")
	}
}

func TestDirectResourceMetadataParsesBearerChallengesAtomically(t *testing.T) {
	metadata, err := directResourceMetadata([]string{`Basic realm="ignored", Bearer error=invalid_token, resource_metadata="https://mcp.example/meta", scope="openid", Digest realm="also ignored"`})
	if err != nil || metadata != "https://mcp.example/meta" {
		t.Fatalf("metadata = %q, %v", metadata, err)
	}
	metadata, err = directResourceMetadata([]string{`Basic resource_metadata="https://mcp.example/meta", Bearer realm="x"`})
	if err != nil || metadata != "" {
		t.Fatalf("Basic parameter leaked into Bearer: %q, %v", metadata, err)
	}
	for _, header := range []string{
		`Bearer resource_metadata="https://mcp.example/a", Basic realm="x", Bearer resource_metadata="https://mcp.example/b"`,
		`Bearer resource_metadata="https://mcp.example/meta", scope="openid", Bearer resource_metadata="https://mcp.example/meta", scope="profile"`,
		`Bearer resource_metadata=https://mcp.example/meta`,
		`Bearer resource_metadata="https://mcp.example/meta`,
	} {
		if _, err := directResourceMetadata([]string{header}); err == nil {
			t.Fatalf("hostile or conflicting header accepted: %q", header)
		}
	}
}

func TestDirectMCPOnboarding_Scenario1_ChallengeAndFallbackOrder(t *testing.T) {
	resource, err := exactDirectResource("https://mcp.example/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if got := directEndpointMetadataURL(mustURL(t, resource)); got != "https://mcp.example/.well-known/oauth-protected-resource/mcp" {
		t.Fatalf("pathful metadata URL = %q", got)
	}
	if got := directEndpointMetadataURL(mustURL(t, "https://mcp.example/")); got != "https://mcp.example/.well-known/oauth-protected-resource/" {
		t.Fatalf("root metadata URL = %q", got)
	}
	if _, err := directResourceMetadata([]string{`Bearer resource_metadata="https://mcp.example/meta"`}); err != nil {
		t.Fatal(err)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestDirectMCPOnboarding_Scenario1_BearerScopeMetadataAtomicity(t *testing.T) {
	if _, err := directResourceMetadata([]string{`Bearer scope="openid"`}); err == nil {
		t.Fatal("scope-bearing Bearer challenge without resource metadata was accepted")
	}
	if _, err := directResourceMetadata([]string{
		`Bearer resource_metadata="https://mcp.example/meta"`,
		`Bearer scope="openid", resource_metadata="https://mcp.example/meta"`,
	}); err == nil {
		t.Fatal("scope from a separate challenge was accepted")
	}
	metadata, err := directResourceMetadata([]string{`Bearer scope="openid", resource_metadata="https://mcp.example/meta"`})
	if err != nil || metadata != "https://mcp.example/meta" {
		t.Fatalf("atomic challenge = %q, %v", metadata, err)
	}
}
func TestDirectMCPOnboarding_Scenario1_IssuerMetadataMatrix(t *testing.T) {
	var rfc8414Hits, oidcHits atomic.Int32
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("discovery request leaked side effects: %s auth=%q cookie=%q", r.Method, r.Header.Get("Authorization"), r.Header.Get("Cookie"))
		}
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server/tenant":
			rfc8414Hits.Add(1)
			http.NotFound(w, r)
		case "/.well-known/openid-configuration/tenant":
			oidcHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"issuer":"` + server.URL + `/tenant","authorization_endpoint":"` + server.URL + `/authorize","token_endpoint":"` + server.URL + `/token","registration_endpoint":"` + server.URL + `/register","authorization_response_iss_parameter_supported":true}`))
		default:
			t.Errorf("unexpected discovery request: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	got, err := directIssuerMetadata(t.Context(), server.Client(), server.URL+"/tenant")
	if err != nil {
		t.Fatal(err)
	}
	if got.Issuer != server.URL+"/tenant" || !got.AuthorizationResponseIssParameterSupported || rfc8414Hits.Load() != 1 || oidcHits.Load() != 1 {
		t.Fatalf("discovery=%+v rfc8414=%d oidc=%d", got, rfc8414Hits.Load(), oidcHits.Load())
	}
}
func TestDirectIssuerMetadataTriesPathfulOIDCAppendLast(t *testing.T) {
	var paths []string
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/tenant/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"issuer":"` + server.URL + `/tenant","authorization_endpoint":"` + server.URL + `/authorize","token_endpoint":"` + server.URL + `/token","registration_endpoint":"` + server.URL + `/register"}`))
	}))
	defer server.Close()

	if _, err := directIssuerMetadata(t.Context(), server.Client(), server.URL+"/tenant"); err != nil {
		t.Fatal(err)
	}
	want := []string{"/.well-known/oauth-authorization-server/tenant", "/.well-known/openid-configuration/tenant", "/tenant/.well-known/openid-configuration"}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths = %v, want %v", paths, want)
		}
	}
}
func TestDirectIssuerMetadataRejectsNonJSONContentType(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-authorization-server" {
			t.Errorf("unexpected fallback after terminal response: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(`{"issuer":"` + server.URL + `"}`))
	}))
	defer server.Close()

	if _, err := directIssuerMetadata(t.Context(), server.Client(), server.URL); err == nil {
		t.Fatal("non-JSON issuer metadata was accepted")
	}
}

func TestDirectMetadataRequiresJSONContentTypeAndEnforcesBodyLimit(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
		body        []byte
	}{
		{name: "wrong content type", contentType: "text/plain", body: []byte(`{"resource":"https://mcp.example","authorization_servers":["https://issuer.example"]}`)},
		{name: "oversized body", contentType: "application/json", body: append([]byte(`{}`), bytes.Repeat([]byte(" "), maxDirectDiscoveryBody)...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = w.Write(tc.body)
			}))
			defer server.Close()
			if _, _, err := directProtectedResource(t.Context(), server.Client(), server.URL, server.URL); err == nil {
				t.Fatal("invalid metadata response was accepted")
			}
		})
	}
}

func TestDirectMCPOnboarding_Scenario1_ExactResourceAndRedirectBinding(t *testing.T) {
	root, _ := exactDirectURL("https://mcp.example/")
	pathful, _ := exactDirectURL("https://mcp.example/mcp")
	if got, want := directEndpointMetadataURL(root), "https://mcp.example/.well-known/oauth-protected-resource/"; got != want {
		t.Fatalf("root metadata URL = %q, want %q", got, want)
	}
	if got, want := directEndpointMetadataURL(pathful), "https://mcp.example/.well-known/oauth-protected-resource/mcp"; got != want {
		t.Fatalf("pathful metadata URL = %q, want %q", got, want)
	}
}

func TestExactDirectURLRejectsForceQuery(t *testing.T) {
	if _, err := exactDirectURL("https://mcp.example/mcp?"); err == nil {
		t.Fatal("exactDirectURL accepted ForceQuery")
	}
}
func TestDirectIssuerMetadataDoesNotAdvanceAfterNon404(t *testing.T) {
	var oidcHits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			oidcHits.Add(1)
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if _, err := directIssuerMetadata(t.Context(), server.Client(), server.URL); err == nil {
		t.Fatal("non-404 RFC 8414 response advanced to OIDC")
	}
	if oidcHits.Load() != 0 {
		t.Fatalf("OIDC fallback requests = %d, want 0", oidcHits.Load())
	}
}

func TestExactDirectResourceRejectsCanonicalizationAndQuery(t *testing.T) {
	for _, raw := range []string{"https://MCP.example/mcp", "https://mcp.example/mcp?x=1", "https://mcp.example/mcp?", "http://mcp.example/mcp"} {
		if _, err := exactDirectResource(raw); err == nil {
			t.Fatalf("resource accepted: %q", raw)
		}
	}
	if _, err := exactDirectResource("https://mcp.example/mcp"); err != nil {
		t.Fatal(err)
	}
	if ErrDirectPathMetadataFallbackUnsupported.Error() == "" {
		t.Fatal("pathful fallback error is empty")
	}
}

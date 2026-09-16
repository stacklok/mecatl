package mcp

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestDirectResourceMetadataRejectsConflictingAndQuotedComma(t *testing.T) {
	metadata, err := directResourceMetadata([]string{`Bearer realm="example, protected", resource_metadata="https://mcp.example/.well-known/oauth-protected-resource"`, `Bearer resource_metadata="https://mcp.example/.well-known/oauth-protected-resource"`})
	if err != nil || metadata != "https://mcp.example/.well-known/oauth-protected-resource" {
		t.Fatalf("metadata = %q, %v", metadata, err)
	}
	if _, err := directResourceMetadata([]string{`Bearer resource_metadata="https://one.example/meta"`, `Bearer resource_metadata="https://two.example/meta"`}); err == nil {
		t.Fatal("conflicting metadata was accepted")
	}
}

func TestDirectResourceMetadataParsesMixedChallengesAtomically(t *testing.T) {
	metadata, err := directResourceMetadata([]string{`Basic realm="ignored", Bearer error=invalid_token, resource_metadata="https://mcp.example/meta", Digest realm="also ignored"`})
	if err != nil || metadata != "https://mcp.example/meta" {
		t.Fatalf("metadata = %q, %v", metadata, err)
	}
	metadata, err = directResourceMetadata([]string{`Basic resource_metadata="https://mcp.example/meta", Bearer realm="x"`})
	if err != nil || metadata != "" {
		t.Fatalf("Basic parameter leaked into Bearer: %q, %v", metadata, err)
	}
	for _, header := range []string{
		`Bearer resource_metadata="https://mcp.example/a", Basic realm="x", Bearer resource_metadata="https://mcp.example/b"`,
		`Bearer resource_metadata=https://mcp.example/meta`,
		`Bearer resource_metadata="https://mcp.example/meta`,
	} {
		if _, err := directResourceMetadata([]string{header}); err == nil {
			t.Fatalf("hostile header accepted: %q", header)
		}
	}
}

func TestDirectIssuerMetadataFallsBackOnlyFrom404WithoutCredentials(t *testing.T) {
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
func TestDirectIssuerMetadataDoesNotAdvanceAfterNon404(t *testing.T) {
	var oidcHits atomic.Int32
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	for _, raw := range []string{"https://MCP.example/mcp", "https://mcp.example/mcp?x=1", "http://mcp.example/mcp"} {
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

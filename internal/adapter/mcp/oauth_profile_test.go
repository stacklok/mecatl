package mcp

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"testing"
)

func TestOAuthCredentialHeadersAreMutuallyExclusive(t *testing.T) {
	for _, header := range []string{"Authorization", "authorization", "Proxy-Authorization", "proxy-authorization", "Cookie", "cookie"} {
		t.Run(header, func(t *testing.T) {
			_, controller, err := prepareOAuthServerConfig(context.Background(), ServerConfig{
				Name: "svc", URL: "https://mcp.example/mcp",
				Headers: map[string]string{header: "canary-secret"}, OAuth: &OAuthOptions{},
			})
			if err == nil || controller != nil {
				t.Fatalf("prepare error/controller = %v/%v", err, controller)
			}
			if !HasCredentialHeaders(map[string]string{header: "x"}) {
				t.Fatalf("HasCredentialHeaders(%q) = false", header)
			}
		})
	}
	if HasCredentialHeaders(map[string]string{"X-Trace": "ok"}) {
		t.Fatal("noncredential header rejected")
	}
}

func TestOAuthResourceRedirectLimit(t *testing.T) {
	resource, _ := url.Parse("https://mcp.example/next")
	previous := &http.Request{URL: resource}
	for _, test := range []struct {
		name  string
		limit int
		via   []*http.Request
		want  bool
	}{
		{name: "zero", limit: 0, via: []*http.Request{previous}, want: false},
		{name: "one", limit: 1, via: []*http.Request{previous}, want: true},
		{name: "exceeded", limit: 1, via: []*http.Request{previous, previous}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := ServerConfig{URL: "https://mcp.example/mcp", OAuth: &OAuthOptions{Network: OAuthNetworkPolicy{MaxRedirects: test.limit}}}
			client := newMCPHTTPClient(cfg, &OAuthController{transport: &oauthHTTPTransport{}})
			err := client.CheckRedirect(&http.Request{URL: resource}, test.via)
			if (err == nil) != test.want {
				t.Fatalf("redirect allowed = %v, want %v (error %v)", err == nil, test.want, err)
			}
		})
	}
	if client := newMCPHTTPClient(ServerConfig{URL: "https://mcp.example/mcp"}, nil); client.CheckRedirect != nil {
		t.Fatal("OAuth-disabled client changed the standard redirect policy")
	}
}

func TestOAuthCredentialRecordKeyMatchesControllerDerivation(t *testing.T) {
	opts := testOAuthOptions(newOAuthMemoryStore(t))
	got, err := OAuthCredentialRecordKey("https://MCP.EXAMPLE:443/mcp", opts)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := validateOAuthRegistration(opts.Client, opts.Issuer)
	if err != nil {
		t.Fatal(err)
	}
	want, err := oauthCredentialKey(oauthCredentialIdentity{
		Profile: opts.Subject.Profile, Principal: opts.Subject.Principal,
		Resource: "https://mcp.example/mcp", Issuer: opts.Issuer,
		ClientKind: registration.kind, ClientID: registration.clientID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("record key differs: %x != %x", got, want)
	}
}

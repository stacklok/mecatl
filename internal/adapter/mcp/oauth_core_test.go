package mcp

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

func TestOAuthPersistenceCoreWiresOfficialInitialAndNewTokenHooks(t *testing.T) {
	store := newOAuthMemoryStore(t)
	seed := newTestOAuthState(t, store, http.DefaultClient, "https://issuer.example/token", true)
	if _, err := seed.newTokenSource(context.Background(), testOAuthConfig("https://issuer.example/token"), validOAuthToken("restored", "refresh")); err != nil {
		t.Fatal(err)
	}
	opts := testOAuthOptions(store)
	state, handler, err := newOAuthPersistenceCore(context.Background(), "HTTPS://MCP.EXAMPLE:443/mcp", opts, http.DefaultClient, func(context.Context, *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
		t.Fatal("authorization fetcher should not run while testing warm restore")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if state == nil || handler == nil {
		t.Fatal("persistence core returned nil state or official handler")
	}
	source, err := handler.TokenSource(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertTokenAccess(t, source, "restored")
}

func TestOAuthOptionsAllowOnlyOnePreregisteredOrCIMDRegistration(t *testing.T) {
	store := newOAuthMemoryStore(t)
	preregistered := testOAuthOptions(store)
	if _, _, err := newOAuthPersistenceCore(context.Background(), "https://mcp.example/mcp", preregistered, http.DefaultClient, noopAuthorizationFetcher); err != nil {
		t.Fatalf("preregistered: %v", err)
	}

	cimd := preregistered
	cimd.Client = OAuthClientConfig{ClientIDMetadataDocumentURL: "https://client.example/metadata.json"}
	if _, _, err := newOAuthPersistenceCore(context.Background(), "https://mcp.example/mcp", cimd, http.DefaultClient, noopAuthorizationFetcher); err != nil {
		t.Fatalf("CIMD: %v", err)
	}

	for name, client := range map[string]OAuthClientConfig{
		"none": {},
		"both": {Preregistered: preregistered.Client.Preregistered, ClientIDMetadataDocumentURL: "https://client.example/metadata.json"},
	} {
		t.Run(name, func(t *testing.T) {
			opts := preregistered
			opts.Client = client
			if _, _, err := newOAuthPersistenceCore(context.Background(), "https://mcp.example/mcp", opts, http.DefaultClient, noopAuthorizationFetcher); err == nil {
				t.Fatal("unsupported registration configuration accepted")
			}
		})
	}
}

func TestOAuthScopeFilterIsIntersectionAndDeduplicates(t *testing.T) {
	filter := scopeFilter([]string{"read", "offline_access"})
	got := filter([]string{"write", "read", "read", "offline_access", "admin"})
	want := []string{"read", "offline_access"}
	if !slices.Equal(got, want) {
		t.Fatalf("filtered scopes = %v, want %v", got, want)
	}
}

func testOAuthOptions(store credentialstore.Store) OAuthOptions {
	return OAuthOptions{
		Subject: OAuthSubject{Profile: "profile", Principal: "principal"},
		Issuer:  "https://issuer.example",
		Client: OAuthClientConfig{Preregistered: &oauthex.ClientCredentials{
			ClientID: "client-id", ClientSecretAuth: &oauthex.ClientSecretAuth{ClientSecret: testClientSecretCanary}, Issuer: "https://issuer.example",
		}},
		RedirectURL: "http://127.0.0.1/callback", CredentialStore: store,
		RequestRefreshToken: true, AllowedScopes: []string{"read", "offline_access"}, Timeout: 30 * time.Second,
		allowLoopbackForTest: true,
	}
}

func noopAuthorizationFetcher(context.Context, *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
	return nil, context.Canceled
}

package mcp

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

func TestOAuthPersistenceRequiresOneCASDomain(t *testing.T) {
	store := newOAuthMemoryStore(t)
	reader, err := credentialstore.NewEnvironment("tenant", []byte("record"), "MECATL_MCP_CREDENTIAL", func(string) (string, bool) {
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })

	gotReader, gotWriter, err := oauthPersistence(OAuthOptions{CredentialStore: store})
	if err != nil {
		t.Fatal(err)
	}
	if gotReader != store || gotWriter != store {
		t.Fatal("mutable store did not supply both persistence capabilities from one domain")
	}

	gotReader, gotWriter, err = oauthPersistence(OAuthOptions{CredentialReader: reader})
	if err != nil {
		t.Fatal(err)
	}
	if gotReader != reader || gotWriter != nil {
		t.Fatal("read-only source unexpectedly supplied mutation capability")
	}

	gotReader, gotWriter, err = oauthPersistence(OAuthOptions{CredentialStore: store, CredentialReader: reader})
	if err == nil {
		t.Fatal("mixed credential store and reader configuration was accepted")
	}
	if gotReader != nil || gotWriter != nil {
		t.Fatal("mixed persistence configuration returned usable capabilities")
	}
	mixed := testOAuthOptions(store)
	mixed.CredentialReader = reader
	if _, _, err := validateOAuthOptions(mixed); err == nil {
		t.Fatal("OAuth option validation accepted mixed credential persistence")
	}
}

func TestOAuthReadOnlyEnvironmentWarmRestore(t *testing.T) {
	opts, reader, _, _ := readOnlyOAuthFixture(t, validOAuthToken("warm-access", "warm-refresh"), nil)
	opts.CredentialReader = reader
	state, _, err := newOAuthPersistenceCore(context.Background(), "https://mcp.example/mcp", opts, http.DefaultClient, noopAuthorizationFetcher)
	if err != nil {
		t.Fatal(err)
	}
	assertTokenAccess(t, state.initialTokenSource(), "warm-access")
}

func TestOAuthReadOnlyExpiredRefreshFailsClosedBeforeNetwork(t *testing.T) {
	var hits atomic.Int32
	client := &http.Client{Transport: controllerRoundTripFunc(func(*http.Request) (*http.Response, error) {
		hits.Add(1)
		return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader("unexpected")), Header: make(http.Header)}, nil
	})}
	opts, reader, _, _ := readOnlyOAuthFixture(t, expiredOAuthToken("old-access", "old-refresh"), client)
	opts.CredentialReader = reader
	state, _, err := newOAuthPersistenceCore(context.Background(), "https://mcp.example/mcp", opts, client, noopAuthorizationFetcher)
	if err != nil {
		t.Fatal(err)
	}
	_, err = state.initialTokenSource().Token()
	if !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("Token error = %v, want unavailable", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("refresh made %d network requests", hits.Load())
	}
}

func TestOAuthReadOnlyExplicitInMemoryRefreshIsProcessLocal(t *testing.T) {
	var hits atomic.Int32
	client := &http.Client{Transport: controllerRoundTripFunc(func(*http.Request) (*http.Response, error) {
		hits.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"fresh-access","token_type":"Bearer","expires_in":3600}`)),
		}, nil
	})}
	opts, reader, encoded, key := readOnlyOAuthFixture(t, expiredOAuthToken("old-access", "old-refresh"), client)
	opts.CredentialReader = reader
	opts.AllowInMemoryRefresh = true
	state, _, err := newOAuthPersistenceCore(context.Background(), "https://mcp.example/mcp", opts, client, noopAuthorizationFetcher)
	if err != nil {
		t.Fatal(err)
	}
	assertTokenAccess(t, state.initialTokenSource(), "fresh-access")
	if hits.Load() != 1 {
		t.Fatalf("refresh requests = %d, want 1", hits.Load())
	}

	unchanged, err := reader.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if base64.StdEncoding.EncodeToString(unchanged.Value) != encoded {
		t.Fatal("read-only source was mutated")
	}
	restarted, err := restoreOAuthCredential(context.Background(), reader, nil, testOAuthIdentity(), oauthRegistration{kind: "preregistered", clientID: "client-id", clientSecret: testClientSecretCanary}, testOAuthOrigins(), client, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := restarted.token.AccessToken; got != "old-access" {
		t.Fatalf("restart access token = %q, want old source credential", got)
	}
}

func TestOAuthReadOnlyRejectsAuthorizationAndResetBeforeSideEffects(t *testing.T) {
	opts, reader, _, _ := readOnlyOAuthFixture(t, validOAuthToken("warm", "refresh"), nil)
	opts.CredentialReader = reader
	var presentations atomic.Int32
	opts.Presenter = OAuthPresenterFunc(func(context.Context, string) (*auth.AuthorizationResult, error) {
		presentations.Add(1)
		return nil, nil
	})
	controller, err := NewOAuthController(context.Background(), "https://mcp.example/mcp", opts)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	resp := &http.Response{Body: io.NopCloser(strings.NewReader("challenge-canary"))}
	if err := controller.Authorize(context.Background(), &http.Request{}, resp); !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("Authorize error = %v, want unavailable", err)
	}
	if presentations.Load() != 0 {
		t.Fatal("read-only authorization invoked presenter")
	}
	if err := controller.ResetCredential(context.Background()); !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("ResetCredential error = %v, want unavailable", err)
	}
}

func TestOAuthReadOnlyInvalidGrantClearsOnlyMemory(t *testing.T) {
	client := &http.Client{Transport: controllerRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_grant","error_description":"response-body-canary"}`)),
		}, nil
	})}
	opts, reader, _, _ := readOnlyOAuthFixture(t, expiredOAuthToken("old", "invalid-refresh"), client)
	opts.CredentialReader = reader
	opts.AllowInMemoryRefresh = true
	state, _, err := newOAuthPersistenceCore(context.Background(), "https://mcp.example/mcp", opts, client, noopAuthorizationFetcher)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.initialTokenSource().Token(); !errors.Is(err, ErrOAuthLoginRequired) {
		t.Fatalf("Token error = %v, want login required", err)
	}
	if state.tokenSource(context.Background()) != nil {
		t.Fatal("invalid_grant retained in-memory credential")
	}
	if _, err := reader.Get(context.Background(), state.key); err != nil {
		t.Fatalf("invalid_grant mutated read-only source: %v", err)
	}
}

func TestOAuthWithoutConfigurationDoesNotLookupEnvironment(t *testing.T) {
	var calls atomic.Int32
	reader, err := credentialstore.NewEnvironment("tenant", []byte("unused"), "MECATL_MCP_CREDENTIAL", func(string) (string, bool) {
		calls.Add(1)
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = reader
	cfg, controller, err := prepareOAuthServerConfig(context.Background(), ServerConfig{URL: "https://mcp.example/mcp"})
	if err != nil || controller != nil || cfg.OAuth != nil {
		t.Fatalf("OAuth-disabled preparation = (%+v, %v, %v)", cfg, controller, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("OAuth-disabled path performed %d environment lookups", calls.Load())
	}
}

func readOnlyOAuthFixture(t *testing.T, token *oauth2.Token, client *http.Client) (OAuthOptions, credentialstore.Reader, string, []byte) {
	t.Helper()
	store := newOAuthMemoryStore(t)
	state := newTestOAuthState(t, store, client, "https://issuer.example/token", true)
	if _, err := state.newTokenSource(context.Background(), testOAuthConfig("https://issuer.example/token"), token); err != nil {
		t.Fatal(err)
	}
	record, err := store.Get(context.Background(), state.key)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(record.Value)
	reader, err := credentialstore.NewEnvironment("mcp-oauth-env", state.key, "MECATL_MCP_OAUTH_CREDENTIAL", func(string) (string, bool) { return encoded, true })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	opts := testOAuthOptions(nil)
	return opts, reader, encoded, append([]byte(nil), state.key...)
}

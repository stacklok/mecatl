package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

func TestSDKAuthorizationCodePublicServerDoesNotStartOAuth(t *testing.T) {
	fixture := newOAuthFixture(t, oauthFixtureOptions{public: true})
	handler, err := fixture.newHandler(nil)
	if err != nil {
		t.Fatalf("construct handler: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	session, err := fixture.connect(ctx, handler)
	if err != nil {
		t.Fatalf("connect public server: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}
	for _, key := range []string{"resource-metadata", "oauth-metadata", "authorize", "token", "register", "unexpected-bearer"} {
		if got := fixture.count(key); got != 0 {
			t.Errorf("%s requests = %d, want 0", key, got)
		}
	}
}

func TestSDKAuthorizationCodeChallengeHappyPath(t *testing.T) {
	fixture := newOAuthFixture(t, oauthFixtureOptions{challengeMetadata: true})
	handler, err := fixture.newHandler(func(cfg *auth.AuthorizationCodeHandlerConfig) {
		cfg.ScopeFilter = func(scopes []string) []string {
			if !slices.Contains(scopes, "read") {
				t.Errorf("discovered scopes = %v, want read", scopes)
			}
			return []string{"read"}
		}
	})
	if err != nil {
		t.Fatalf("construct handler: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	session, err := fixture.connect(ctx, handler)
	if err != nil {
		t.Fatalf("connect protected server: %v", err)
	}
	defer session.Close()

	if got := fixture.count("unauthorized"); got != 1 {
		t.Errorf("unauthorized attempts = %d, want 1", got)
	}
	if got := fixture.count("authorized"); got == 0 {
		t.Error("authorized attempts = 0, want at least one retried MCP request")
	}
	for _, key := range []string{"resource-metadata", "oauth-metadata", "authorize", "token"} {
		if got := fixture.count(key); got != 1 {
			t.Errorf("%s requests = %d, want 1", key, got)
		}
	}
	if got := fixture.count("oidc-metadata"); got != 0 {
		t.Errorf("OIDC metadata requests = %d, want OAuth metadata to win", got)
	}
	if len(fixture.authQ) != 1 || fixture.authQ[0].Get("code_challenge_method") != "S256" {
		t.Fatalf("authorization did not use PKCE S256")
	}
	if got := fixture.authQ[0].Get("resource"); got != fixture.mcpURL {
		t.Errorf("authorization resource = %q, want protected resource", got)
	}
	if got := fixture.authQ[0].Get("scope"); got != "read" {
		t.Errorf("authorization scope = %q, want read", got)
	}
	if len(fixture.tokenQ) != 1 || fixture.tokenQ[0].Get("resource") != fixture.mcpURL || fixture.tokenQ[0].Get("code_verifier") == "" {
		t.Fatal("token exchange did not carry resource and PKCE verifier")
	}
}

func TestSDKAuthorizationCodeUsesRFC9728WellKnownFallback(t *testing.T) {
	fixture := newOAuthFixture(t, oauthFixtureOptions{})
	handler, err := fixture.newHandler(nil)
	if err != nil {
		t.Fatalf("construct handler: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	session, err := fixture.connect(ctx, handler)
	if err != nil {
		t.Fatalf("connect through well-known discovery: %v", err)
	}
	defer session.Close()
	if got := fixture.count("resource-metadata"); got != 1 {
		t.Errorf("well-known protected-resource requests = %d, want 1", got)
	}
}

func TestSDKAuthorizationCodeFallsBackToResourceOriginWithoutRFC9728Metadata(t *testing.T) {
	fixture := newOAuthFixture(t, oauthFixtureOptions{noResourceMetadata: true})
	fixture.opts.issuerOverride = fixture.server.URL
	handler, err := fixture.newHandler(nil)
	if err != nil {
		t.Fatal("construct handler")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	session, err := fixture.connect(ctx, handler)
	if err != nil {
		t.Fatal("connect did not use the pinned SDK's legacy resource-origin authorization-server fallback")
	}
	defer session.Close()
	if got := fixture.count("resource-metadata"); got != 2 {
		t.Errorf("protected-resource metadata requests = %d, want pinned-SDK behavior of 2", got)
	}
	if got := fixture.count("oauth-metadata"); got != 1 {
		t.Errorf("origin authorization-server metadata requests = %d, want 1", got)
	}
}

func TestSDKAuthorizationCodeSelectsFirstAuthorizationServer(t *testing.T) {
	fixture := newOAuthFixture(t, oauthFixtureOptions{challengeMetadata: true})
	fixture.opts.metadataBody = []byte(`{"resource":"` + fixture.mcpURL + `","authorization_servers":["http://127.0.0.1:1","` + fixture.issuer + `"]}`)
	handler, err := fixture.newHandler(nil)
	if err != nil {
		t.Fatalf("construct handler: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if session, err := fixture.connect(ctx, handler); err == nil {
		_ = session.Close()
		t.Fatal("connect skipped the unavailable first authorization server")
	}
	if got := fixture.count("oauth-metadata"); got != 0 {
		t.Errorf("second authorization server metadata requests = %d, want 0", got)
	}
	if got := fixture.count("authorize"); got != 0 {
		t.Errorf("browser authorization requests = %d, want 0", got)
	}
}

func TestSDKAuthorizationCodeBoundsRepeatedProtectedRejection(t *testing.T) {
	fixture := newOAuthFixture(t, oauthFixtureOptions{keepRejecting: true, challengeMetadata: true})
	handler, err := fixture.newHandler(nil)
	if err != nil {
		t.Fatalf("construct handler: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if session, err := fixture.connect(ctx, handler); err == nil {
		_ = session.Close()
		t.Fatal("connect succeeded despite repeated 401")
	}
	// The pinned SDK gives the JSON-RPC initialize path one connection-level
	// replay in addition to each transport-level OAuth retry. Assert that the
	// observed discrepancy remains bounded; ADR 0219 excludes repeated rejection
	// from the qualified production profile pending an upstream correction.
	if got := fixture.count("unauthorized"); got != 4 {
		t.Errorf("protected attempts = %d, want bounded pinned-SDK behavior of 4", got)
	}
	if got := fixture.count("authorize"); got != 2 {
		t.Errorf("authorization flows = %d, want bounded pinned-SDK behavior of 2", got)
	}
}

func TestSDKAuthorizationCodeRejectsInvalidDiscovery(t *testing.T) {
	tests := []struct {
		name string
		opts oauthFixtureOptions
	}{
		{name: "resource mismatch", opts: oauthFixtureOptions{challengeMetadata: true, metadataResource: "http://127.0.0.1/other"}},
		{name: "malformed metadata", opts: oauthFixtureOptions{challengeMetadata: true, metadataBody: []byte(`{"resource":`)}},
		{name: "oversized metadata", opts: oauthFixtureOptions{challengeMetadata: true, metadataBody: oversizedMetadata()}},
		{name: "issuer mismatch", opts: oauthFixtureOptions{challengeMetadata: true, issuerOverride: "http://127.0.0.1/other-as"}},
		{name: "prohibited authorization server scheme", opts: oauthFixtureOptions{challengeMetadata: true, metadataBody: []byte(`{"resource":"PLACEHOLDER","authorization_servers":["file:///etc/passwd"]}`)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newOAuthFixture(t, test.opts)
			if strings.Contains(string(fixture.opts.metadataBody), "PLACEHOLDER") {
				fixture.opts.metadataBody = []byte(strings.ReplaceAll(string(fixture.opts.metadataBody), "PLACEHOLDER", fixture.mcpURL))
			}
			handler, err := fixture.newHandler(nil)
			if err != nil {
				t.Fatalf("construct handler: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if session, err := fixture.connect(ctx, handler); err == nil {
				_ = session.Close()
				t.Fatal("connect unexpectedly succeeded")
			}
			if got := fixture.count("authorize"); got != 0 {
				t.Errorf("browser authorization requests = %d, want failure before browser", got)
			}
			if got := fixture.count("token"); got != 0 {
				t.Errorf("token requests = %d, want failure before exchange", got)
			}
		})
	}
}

func oversizedMetadata() []byte {
	value := strings.Repeat("x", (1<<20)+1)
	body, _ := json.Marshal(map[string]any{"resource": value, "authorization_servers": []string{"http://127.0.0.1"}})
	return body
}

func TestSDKAuthorizationCodeValidatesCallbackStateAndIssuer(t *testing.T) {
	tests := []struct {
		name string
		opts oauthFixtureOptions
	}{
		{name: "wrong state", opts: oauthFixtureOptions{challengeMetadata: true, callbackState: "wrong"}},
		{name: "missing issuer", opts: oauthFixtureOptions{challengeMetadata: true, omitCallbackIssuer: true}},
		{name: "wrong issuer", opts: oauthFixtureOptions{challengeMetadata: true, callbackIssuer: "http://127.0.0.1/wrong"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newOAuthFixture(t, test.opts)
			handler, err := fixture.newHandler(nil)
			if err != nil {
				t.Fatalf("construct handler: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if session, err := fixture.connect(ctx, handler); err == nil {
				_ = session.Close()
				t.Fatal("connect unexpectedly succeeded")
			}
			if got := fixture.count("token"); got != 0 {
				t.Errorf("token exchanges = %d, want callback rejected before exchange", got)
			}
		})
	}
}

func TestSDKAuthorizationCodeRequiresEndToEndS256(t *testing.T) {
	fixture := newOAuthFixture(t, oauthFixtureOptions{challengeMetadata: true, codeMethods: []string{"plain"}})
	handler, err := fixture.newHandler(nil)
	if err != nil {
		t.Fatalf("construct handler: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if session, err := fixture.connect(ctx, handler); err == nil {
		_ = session.Close()
		t.Fatal("connect succeeded when AS metadata omitted S256")
	}
	if got := fixture.count("token"); got != 0 {
		t.Errorf("token requests = %d, want failure before exchange", got)
	}
}

func TestSDKAuthorizationCodeMixedClientSecretMethodsDowngradesToForm(t *testing.T) {
	fixture := newOAuthFixture(t, oauthFixtureOptions{
		challengeMetadata: true,
		tokenAuthMethods:  []string{"client_secret_basic", "client_secret_post"},
	})
	handler, err := fixture.newHandler(nil)
	if err != nil {
		t.Fatal("construct handler")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if session, err := fixture.connect(ctx, handler); err == nil {
		_ = session.Close()
		t.Fatal("connect succeeded after the fixture rejected form-secret authentication")
	}
	if got := fixture.count("token-form-secret"); got != 2 {
		t.Errorf("form-secret token exchanges = %d, want pinned-SDK behavior of 2", got)
	}
	if got := fixture.count("token-basic-auth"); got != 0 {
		t.Errorf("Basic-authenticated token exchanges = %d, want 0", got)
	}
}

func TestSDKAuthorizationCodeStepUpRetainsGrantedScopes(t *testing.T) {
	fixture := newOAuthFixture(t, oauthFixtureOptions{challengeMetadata: true, stepUp: true, grantScopes: []string{"read"}})
	handler, err := fixture.newHandler(func(cfg *auth.AuthorizationCodeHandlerConfig) {
		cfg.ScopeFilter = func(scopes []string) []string { return slices.Clone(scopes) }
	})
	if err != nil {
		t.Fatalf("construct handler: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	session, err := fixture.connect(ctx, handler)
	if err != nil {
		t.Fatalf("connect with step-up: %v", err)
	}
	defer session.Close()
	if got := fixture.count("authorize"); got != 2 {
		t.Fatalf("authorization flows = %d, want initial plus step-up", got)
	}
	secondScopes := strings.Fields(fixture.authQ[1].Get("scope"))
	if !slices.Contains(secondScopes, "read") || !slices.Contains(secondScopes, "admin") {
		t.Errorf("step-up scopes = %v, want prior granted read plus admin", secondScopes)
	}
}

func TestSDKAuthorizationCodeBoundsRepeatedEligible403(t *testing.T) {
	fixture := newOAuthFixture(t, oauthFixtureOptions{
		challengeMetadata:   true,
		stepUp:              true,
		keepStepUpRejecting: true,
		grantScopes:         []string{"read"},
	})
	handler, err := fixture.newHandler(nil)
	if err != nil {
		t.Fatalf("construct handler: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if session, err := fixture.connect(ctx, handler); err == nil {
		_ = session.Close()
		t.Fatal("connect succeeded despite repeated eligible 403")
	}
	if got := fixture.count("authorize"); got != 2 {
		t.Errorf("authorization flows = %d, want initial plus one step-up", got)
	}
	if got := fixture.count("step-up-rejected"); got != 1 {
		t.Errorf("rejected step-up attempts = %d, want 1", got)
	}
}

func TestSDKAuthorizationCodePinnedIneligible403Behavior(t *testing.T) {
	fixture := newOAuthFixture(t, oauthFixtureOptions{ineligibleForbidden: true})
	handler, err := fixture.newHandler(func(cfg *auth.AuthorizationCodeHandlerConfig) {
		cfg.InitialTokenSource = oauth2.StaticTokenSource(&oauth2.Token{
			AccessToken: testAccessToken,
			TokenType:   "Bearer",
			Expiry:      time.Now().Add(time.Hour),
		})
	})
	if err != nil {
		t.Fatalf("construct handler: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if session, err := fixture.connect(ctx, handler); err == nil {
		_ = session.Close()
		t.Fatal("connect succeeded despite ineligible 403")
	}
	// This pins an unsafe dependency discrepancy, not supported behavior: the
	// transport retries a 403 carrying invalid_token even though the handler
	// starts no authorization flow. ADR 0219 excludes this path from future
	// production wiring until the official SDK is corrected or mediated.
	if got := fixture.count("ineligible-forbidden"); got != 4 {
		t.Errorf("ineligible 403 attempts = %d, want bounded pinned transport behavior of 4", got)
	}
	if got := fixture.count("authorize"); got != 0 {
		t.Errorf("authorization flows = %d, want 0", got)
	}
}

func TestSDKAuthorizationCodeDynamicRegistration(t *testing.T) {
	fixture := newOAuthFixture(t, oauthFixtureOptions{challengeMetadata: true, dynamicRegistration: true})
	handler, err := fixture.newHandler(nil)
	if err != nil {
		t.Fatalf("construct handler: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	session, err := fixture.connect(ctx, handler)
	if err != nil {
		t.Fatalf("connect with DCR: %v", err)
	}
	defer session.Close()
	if got := fixture.count("register"); got != 1 {
		t.Fatalf("registration requests = %d, want 1", got)
	}
	if len(fixture.regBody) != 1 || !slices.Contains(fixture.regBody[0].RedirectURIs, "http://127.0.0.1/callback") {
		t.Fatal("DCR request did not preserve configured redirect URI")
	}
	if got := fixture.regBody[0].TokenEndpointAuthMethod; got != "client_secret_basic" {
		t.Errorf("DCR token auth method = %q, want client_secret_basic", got)
	}
	if got := fixture.count("token-basic-auth"); got != 1 {
		t.Errorf("Basic-authenticated token exchanges = %d, want 1", got)
	}
	if got := fixture.count("token-form-secret"); got != 0 {
		t.Errorf("client_secret form downgrades = %d, want 0", got)
	}
}

func TestSDKAuthorizationCodeRegistrationPrecedenceAndValidation(t *testing.T) {
	const cimdURL = "https://client.example/oauth/client.json"

	t.Run("CIMD wins when advertised", func(t *testing.T) {
		fixture := newOAuthFixture(t, oauthFixtureOptions{challengeMetadata: true, dynamicRegistration: true, cimdSupported: true})
		handler, err := fixture.newHandler(func(cfg *auth.AuthorizationCodeHandlerConfig) {
			cfg.ClientIDMetadataDocumentConfig = &auth.ClientIDMetadataDocumentConfig{URL: cimdURL}
			cfg.PreregisteredClient = &oauthex.ClientCredentials{ClientID: testClientID, ClientSecretAuth: &oauthex.ClientSecretAuth{ClientSecret: testClientSecret}, Issuer: fixture.issuer}
		})
		if err != nil {
			t.Fatalf("construct handler: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		session, err := fixture.connect(ctx, handler)
		if err != nil {
			t.Fatalf("connect: %v (basic=%d form-secret=%d invalid-code=%d invalid-verifier=%d invalid-redirect=%d invalid-resource=%d invalid-client=%d)", err,
				fixture.count("token-basic-auth"), fixture.count("token-form-secret"), fixture.count("token-invalid-code"),
				fixture.count("token-invalid-verifier"), fixture.count("token-invalid-redirect"), fixture.count("token-invalid-resource"), fixture.count("token-invalid-client"))
		}
		defer session.Close()
		if got := fixture.authQ[0].Get("client_id"); got != cimdURL {
			t.Errorf("authorization client_id = %q, want CIMD URL", got)
		}
		if got := fixture.count("register"); got != 0 {
			t.Errorf("registration requests = %d, want CIMD to win", got)
		}
	})

	t.Run("preregistered wins when CIMD unavailable", func(t *testing.T) {
		fixture := newOAuthFixture(t, oauthFixtureOptions{challengeMetadata: true, dynamicRegistration: true})
		handler, err := fixture.newHandler(func(cfg *auth.AuthorizationCodeHandlerConfig) {
			cfg.ClientIDMetadataDocumentConfig = &auth.ClientIDMetadataDocumentConfig{URL: cimdURL}
			cfg.PreregisteredClient = &oauthex.ClientCredentials{ClientID: testClientID, ClientSecretAuth: &oauthex.ClientSecretAuth{ClientSecret: testClientSecret}, Issuer: fixture.issuer}
		})
		if err != nil {
			t.Fatalf("construct handler: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		session, err := fixture.connect(ctx, handler)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer session.Close()
		if got := fixture.authQ[0].Get("client_id"); got != testClientID {
			t.Errorf("authorization client_id = %q, want preregistered client", got)
		}
		if got := fixture.count("register"); got != 0 {
			t.Errorf("registration requests = %d, want preregistration to win", got)
		}
	})

	t.Run("preregistered issuer mismatch is terminal", func(t *testing.T) {
		fixture := newOAuthFixture(t, oauthFixtureOptions{challengeMetadata: true, dynamicRegistration: true})
		handler, err := fixture.newHandler(func(cfg *auth.AuthorizationCodeHandlerConfig) {
			cfg.PreregisteredClient = &oauthex.ClientCredentials{ClientID: testClientID, ClientSecretAuth: &oauthex.ClientSecretAuth{ClientSecret: testClientSecret}, Issuer: "https://wrong.example/as"}
		})
		if err != nil {
			t.Fatalf("construct handler: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if session, err := fixture.connect(ctx, handler); err == nil {
			_ = session.Close()
			t.Fatal("connect succeeded with mismatched preregistered issuer")
		}
		for _, key := range []string{"register", "authorize", "token"} {
			if got := fixture.count(key); got != 0 {
				t.Errorf("%s requests = %d, want terminal failure before fallback", key, got)
			}
		}
	})

	t.Run("unsupported registration fails", func(t *testing.T) {
		fixture := newOAuthFixture(t, oauthFixtureOptions{challengeMetadata: true})
		handler, err := fixture.newHandler(func(cfg *auth.AuthorizationCodeHandlerConfig) {
			cfg.PreregisteredClient = nil
			cfg.ClientIDMetadataDocumentConfig = &auth.ClientIDMetadataDocumentConfig{URL: cimdURL}
		})
		if err != nil {
			t.Fatalf("construct handler: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if session, err := fixture.connect(ctx, handler); err == nil {
			_ = session.Close()
			t.Fatal("connect succeeded without an AS-supported registration mechanism")
		}
		for _, key := range []string{"register", "authorize", "token"} {
			if got := fixture.count(key); got != 0 {
				t.Errorf("%s requests = %d, want failure before registration-dependent work", key, got)
			}
		}
	})

	t.Run("redirect must be registered", func(t *testing.T) {
		fixture := newOAuthFixture(t, oauthFixtureOptions{})
		_, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
			DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{RedirectURIs: []string{"http://127.0.0.1/other"}}},
			RedirectURL:                     "http://127.0.0.1/callback",
			AuthorizationCodeFetcher:        fixture.fetchAuthorization,
			Client:                          fixture.client,
		})
		if err == nil {
			t.Fatal("constructor accepted redirect absent from DCR metadata")
		}
	})
}

func TestSDKAuthorizationCodeInitialAndNewTokenSources(t *testing.T) {
	t.Run("accepted initial source avoids interactive flow", func(t *testing.T) {
		fixture := newOAuthFixture(t, oauthFixtureOptions{})
		handler, err := fixture.newHandler(func(cfg *auth.AuthorizationCodeHandlerConfig) {
			cfg.InitialTokenSource = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: testAccessToken, TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)})
		})
		if err != nil {
			t.Fatalf("construct handler: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		session, err := fixture.connect(ctx, handler)
		if err != nil {
			t.Fatalf("connect with restored token: %v", err)
		}
		defer session.Close()
		for _, key := range []string{"resource-metadata", "authorize", "token"} {
			if got := fixture.count(key); got != 0 {
				t.Errorf("%s requests = %d, want 0", key, got)
			}
		}
	})

	t.Run("rejected initial source is replaced interactively", func(t *testing.T) {
		fixture := newOAuthFixture(t, oauthFixtureOptions{challengeMetadata: true})
		handler, err := fixture.newHandler(func(cfg *auth.AuthorizationCodeHandlerConfig) {
			cfg.InitialTokenSource = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "restored-but-rejected", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)})
		})
		if err != nil {
			t.Fatalf("construct handler: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		session, err := fixture.connect(ctx, handler)
		if err != nil {
			t.Fatalf("connect after restored token rejection: %v", err)
		}
		defer session.Close()
		if got := fixture.count("unauthorized"); got != 1 {
			t.Errorf("restored-token rejections = %d, want 1", got)
		}
		for _, key := range []string{"authorize", "token"} {
			if got := fixture.count(key); got != 1 {
				t.Errorf("%s requests = %d, want 1", key, got)
			}
		}
	})

	t.Run("new source refreshes lazily for subsequent MCP request", func(t *testing.T) {
		fixture := newOAuthFixture(t, oauthFixtureOptions{challengeMetadata: true})
		var calls atomic.Int64
		var source oauth2.TokenSource
		var issued *oauth2.Token
		handler, err := fixture.newHandler(func(cfg *auth.AuthorizationCodeHandlerConfig) {
			cfg.NewTokenSource = func(ctx context.Context, oauthCfg *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
				calls.Add(1)
				issued = token
				source = oauthCfg.TokenSource(ctx, token)
				return source, nil
			}
		})
		if err != nil {
			t.Fatalf("construct handler: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		session, err := fixture.connect(ctx, handler)
		if err != nil {
			cancel()
			t.Fatalf("connect: %v", err)
		}
		cancel()
		defer session.Close()
		if got := calls.Load(); got != 1 {
			t.Fatalf("NewTokenSource calls = %d, want 1", got)
		}
		issued.Expiry = time.Now().Add(-time.Minute)
		callCtx, callCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer callCancel()
		if _, err := session.CallTool(callCtx, &mcpsdk.CallToolParams{Name: "protected", Arguments: map[string]any{}}); err != nil {
			t.Fatalf("MCP request after lazy refresh: %v", err)
		}
		if got := fixture.count("token"); got != 2 {
			t.Errorf("token requests = %d, want exchange plus one lazy refresh", got)
		}
		if got := fixture.count("authorized"); got < 2 {
			t.Errorf("authorized MCP requests = %d, want connect and post-refresh call", got)
		}
	})

	t.Run("refresh invalid_grant is terminal through public transport", func(t *testing.T) {
		fixture := newOAuthFixture(t, oauthFixtureOptions{challengeMetadata: true, refreshInvalidGrant: true})
		var issued *oauth2.Token
		handler, err := fixture.newHandler(func(cfg *auth.AuthorizationCodeHandlerConfig) {
			cfg.NewTokenSource = func(ctx context.Context, oauthCfg *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
				issued = token
				return oauthCfg.TokenSource(ctx, token), nil
			}
		})
		if err != nil {
			t.Fatalf("construct handler: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		session, err := fixture.connect(ctx, handler)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer session.Close()
		issued.Expiry = time.Now().Add(-time.Minute)
		if _, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "protected", Arguments: map[string]any{}}); err == nil {
			t.Fatal("MCP request succeeded after refresh invalid_grant")
		}
		if got := fixture.count("token"); got != 2 {
			t.Errorf("token requests = %d, want exchange plus failed refresh", got)
		}
		if got := fixture.count("authorize"); got != 1 {
			t.Errorf("authorization flows = %d, want no automatic reauthorization after refresh failure", got)
		}
	})
}

func TestSDKAuthorizationCodeCallerHTTPPolicyBoundsOriginsAndTimeouts(t *testing.T) {
	t.Run("untrusted origin denied", func(t *testing.T) {
		var destinationHits atomic.Int64
		destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { destinationHits.Add(1) }))
		t.Cleanup(destination.Close)
		fixture := newOAuthFixture(t, oauthFixtureOptions{challengeMetadata: true})
		base := fixture.client.Transport
		fixture.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != strings.TrimPrefix(fixture.server.URL, "http://") {
				return nil, errors.New("origin denied by caller policy")
			}
			return base.RoundTrip(req)
		})
		fixture.opts.metadataBody = []byte(`{"resource":"` + fixture.mcpURL + `","authorization_servers":["` + destination.URL + `"]}`)
		handler, err := fixture.newHandler(nil)
		if err != nil {
			t.Fatalf("construct handler: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if session, err := fixture.connect(ctx, handler); err == nil {
			_ = session.Close()
			t.Fatal("connect unexpectedly followed untrusted authorization-server path")
		}
		if got := destinationHits.Load(); got != 0 {
			t.Errorf("untrusted destination hits = %d, want 0", got)
		}
	})

	for _, test := range []struct {
		name        string
		locationFor func(*oauthFixture, *httptest.Server) string
	}{
		{name: "cross-origin metadata redirect", locationFor: func(_ *oauthFixture, destination *httptest.Server) string { return destination.URL + "/metadata" }},
		{name: "metadata redirect loop", locationFor: func(fixture *oauthFixture, _ *httptest.Server) string { return fixture.server.URL + "/metadata" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var destinationHits atomic.Int64
			destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { destinationHits.Add(1) }))
			t.Cleanup(destination.Close)
			fixture := newOAuthFixture(t, oauthFixtureOptions{challengeMetadata: true})
			base := fixture.client.Transport
			fixture.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/metadata" || strings.Contains(req.URL.Path, "oauth-protected-resource") {
					return &http.Response{
						StatusCode: http.StatusFound,
						Header:     http.Header{"Location": []string{test.locationFor(fixture, destination)}},
						Body:       http.NoBody,
						Request:    req,
					}, nil
				}
				return base.RoundTrip(req)
			})
			handler, err := fixture.newHandler(nil)
			if err != nil {
				t.Fatalf("construct handler: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if session, err := fixture.connect(ctx, handler); err == nil {
				_ = session.Close()
				t.Fatal("connect unexpectedly accepted metadata redirect")
			}
			if got := destinationHits.Load(); got != 0 {
				t.Errorf("redirect destination hits = %d, want 0", got)
			}
			if got := fixture.count("resource-metadata"); got != 0 {
				t.Errorf("fixture metadata handler hits = %d, want intercepted initial request only", got)
			}
		})
	}

	t.Run("response header timeout bounds authorization-server metadata", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		var enteredOnce sync.Once
		stall := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			enteredOnce.Do(func() { close(entered) })
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}))
		defer func() {
			close(release)
			stall.Close()
		}()
		fixture := newOAuthFixture(t, oauthFixtureOptions{challengeMetadata: true})
		fixture.opts.metadataBody = []byte(`{"resource":"` + fixture.mcpURL + `","authorization_servers":["` + stall.URL + `"]}`)
		transport := fixture.server.Client().Transport.(*http.Transport).Clone()
		transport.ResponseHeaderTimeout = 50 * time.Millisecond
		fixture.client.Transport = transport
		fixture.client.Timeout = 2 * time.Second
		handler, err := fixture.newHandler(nil)
		if err != nil {
			t.Fatalf("construct handler: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			session, connectErr := fixture.connect(ctx, handler)
			if session != nil {
				_ = session.Close()
			}
			done <- connectErr
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("authorization-server metadata request did not start")
		}
		select {
		case connectErr := <-done:
			if connectErr == nil {
				t.Fatal("connect succeeded despite response-header stall")
			}
		case <-time.After(time.Second):
			t.Fatal("response-header timeout did not bound connect")
		}
	})

	for _, phase := range []struct {
		name string
		path string
		opts oauthFixtureOptions
	}{
		{name: "protected-resource metadata", path: "/metadata", opts: oauthFixtureOptions{challengeMetadata: true}},
		{name: "authorization", path: "/as/authorize", opts: oauthFixtureOptions{challengeMetadata: true}},
		{name: "token", path: "/as/token", opts: oauthFixtureOptions{challengeMetadata: true}},
		{name: "registration", path: "/as/register", opts: oauthFixtureOptions{challengeMetadata: true, dynamicRegistration: true}},
	} {
		t.Run("total timeout bounds "+phase.name, func(t *testing.T) {
			fixture := newOAuthFixture(t, phase.opts)
			fixture.client.Timeout = 100 * time.Millisecond
			base := fixture.client.Transport
			entered := make(chan struct{}, 1)
			var signal sync.Once
			fixture.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				matchesPhase := req.URL.Path == phase.path
				if phase.path == "/metadata" {
					matchesPhase = matchesPhase || strings.Contains(req.URL.Path, "oauth-protected-resource")
				}
				if matchesPhase {
					signal.Do(func() {
						select {
						case entered <- struct{}{}:
						default:
						}
					})
					<-req.Context().Done()
					return nil, req.Context().Err()
				}
				return base.RoundTrip(req)
			})
			handler, err := fixture.newHandler(nil)
			if err != nil {
				t.Fatalf("construct handler: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				session, connectErr := fixture.connect(ctx, handler)
				if session != nil {
					_ = session.Close()
				}
				done <- connectErr
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatalf("%s request did not start", phase.name)
			}
			select {
			case connectErr := <-done:
				if connectErr == nil {
					t.Fatalf("connect succeeded despite %s stall", phase.name)
				}
			case <-time.After(time.Second):
				t.Fatalf("client total timeout did not bound %s stall", phase.name)
			}
		})
	}

	t.Run("authorization transport error omits authorization URL", func(t *testing.T) {
		fixture := newOAuthFixture(t, oauthFixtureOptions{challengeMetadata: true})
		base := fixture.client.Transport
		var rawAuthorizationURL string
		fixture.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/as/authorize" {
				rawAuthorizationURL = req.URL.String()
				return nil, errors.New("transport rejected " + rawAuthorizationURL)
			}
			return base.RoundTrip(req)
		})
		handler, err := fixture.newHandler(nil)
		if err != nil {
			t.Fatalf("construct handler: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		session, connectErr := fixture.connect(ctx, handler)
		if session != nil {
			_ = session.Close()
		}
		if connectErr == nil {
			t.Fatal("connect succeeded despite authorization transport failure")
		}
		message := connectErr.Error()
		if rawAuthorizationURL == "" {
			t.Fatal("authorization transport was not reached")
		}
		if strings.Contains(message, rawAuthorizationURL) {
			t.Error("connect error exposed the raw authorization URL")
		}
		for _, sensitive := range []string{"client_id=", "code_challenge=", "redirect_uri=", "resource="} {
			if strings.Contains(message, sensitive) {
				t.Errorf("connect error exposed authorization query field %q", sensitive)
			}
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

package mcp_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	mcpadapter "github.com/stacklok/mecatl/internal/adapter/mcp"
)

const (
	testClientID     = "qualified-client"
	testClientSecret = "fixture-secret"
	testAccessToken  = "fixture-access"
	testRefreshToken = "fixture-refresh"
	refreshedToken   = "fixture-refreshed"
)

type oauthFixtureOptions struct {
	public              bool
	keepRejecting       bool
	metadataResource    string
	metadataBody        []byte
	challengeMetadata   bool
	issuerOverride      string
	codeMethods         []string
	callbackState       string
	callbackIssuer      string
	omitCallbackIssuer  bool
	unadvertisedIssuer  bool
	stepUp              bool
	keepStepUpRejecting bool
	ineligibleForbidden bool
	dynamicRegistration bool
	cimdSupported       bool
	grantScopes         []string
	refreshInvalidGrant bool
	noResourceMetadata  bool
	tokenAuthMethods    []string
}

type oauthFixture struct {
	t       *testing.T
	opts    oauthFixtureOptions
	server  *httptest.Server
	mcp     *mcpsdk.Server
	client  *http.Client
	mcpURL  string
	issuer  string
	mu      sync.Mutex
	counts  map[string]int
	paths   []string
	authQ   []url.Values
	tokenQ  []url.Values
	regBody []oauthex.ClientRegistrationMetadata
	codes   map[string]codeRecord
	tokens  map[string][]string
	seq     atomic.Int64
}

type codeRecord struct {
	challenge  string
	clientID   string
	redirect   string
	resource   string
	scopes     []string
	issuerSent bool
	used       bool
}

func newOAuthFixture(t *testing.T, opts oauthFixtureOptions) *oauthFixture {
	t.Helper()
	f := &oauthFixture{
		t:      t,
		opts:   opts,
		counts: make(map[string]int),
		codes:  make(map[string]codeRecord),
		tokens: map[string][]string{testAccessToken: {"read"}, refreshedToken: {"read", "admin"}},
	}
	if opts.codeMethods == nil {
		f.opts.codeMethods = []string{"S256"}
	}
	f.mcp = mcpsdk.NewServer(&mcpsdk.Implementation{Name: "oauth-fixture", Version: "1"}, nil)
	mcpsdk.AddTool(f.mcp, &mcpsdk.Tool{Name: "protected", Description: "qualified operation"},
		func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "ok"}}}, nil, nil
		})
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return f.mcp }, nil)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.serveHTTP(mcpHandler, w, r)
	}))
	f.mcpURL = f.server.URL + "/mcp"
	f.issuer = f.server.URL + "/as"
	transport := f.server.Client().Transport.(*http.Transport).Clone()
	f.client = &http.Client{
		Transport: transport,
		Timeout:   2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	t.Cleanup(func() {
		f.client.CloseIdleConnections()
		f.server.Close()
	})
	return f
}

func (f *oauthFixture) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[key]
}

func (f *oauthFixture) record(key, path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts[key]++
	f.paths = append(f.paths, path)
}

func (f *oauthFixture) serveHTTP(mcpHandler http.Handler, w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/mcp":
		f.serveMCP(mcpHandler, w, r)
	case r.URL.Path == "/metadata" || strings.Contains(r.URL.Path, "oauth-protected-resource"):
		f.serveResourceMetadata(w, r)
	case strings.Contains(r.URL.Path, ".well-known/oauth-authorization-server"):
		f.serveASMetadata(w, r, "oauth-metadata")
	case strings.Contains(r.URL.Path, ".well-known/openid-configuration"):
		f.record("oidc-metadata", r.URL.Path)
		http.NotFound(w, r)
	case r.URL.Path == "/as/authorize":
		f.serveAuthorize(w, r)
	case r.URL.Path == "/as/token":
		f.serveToken(w, r)
	case r.URL.Path == "/as/register":
		f.serveRegister(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *oauthFixture) serveMCP(next http.Handler, w http.ResponseWriter, r *http.Request) {
	f.record("mcp", r.URL.Path)
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if f.opts.public {
		if token != "" {
			f.record("unexpected-bearer", r.URL.Path)
		}
		next.ServeHTTP(w, r)
		return
	}
	f.mu.Lock()
	scopes, valid := f.tokens[token]
	f.mu.Unlock()
	if !valid || f.opts.keepRejecting {
		f.record("unauthorized", r.URL.Path)
		metadata := ""
		if f.opts.challengeMetadata {
			metadata = fmt.Sprintf(", resource_metadata=%q", f.server.URL+"/metadata")
		}
		w.Header().Set("WWW-Authenticate", `Bearer scope="read"`+metadata)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.opts.stepUp && !slices.Contains(scopes, "admin") {
		f.record("step-up", r.URL.Path)
		w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="admin"`)
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if f.opts.keepStepUpRejecting && slices.Contains(scopes, "admin") {
		f.record("step-up-rejected", r.URL.Path)
		w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="admin"`)
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if f.opts.ineligibleForbidden {
		f.record("ineligible-forbidden", r.URL.Path)
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		w.WriteHeader(http.StatusForbidden)
		return
	}
	f.record("authorized", r.URL.Path)
	next.ServeHTTP(w, r)
}

func (f *oauthFixture) serveResourceMetadata(w http.ResponseWriter, r *http.Request) {
	f.record("resource-metadata", r.URL.Path)
	if f.opts.noResourceMetadata {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if f.opts.metadataBody != nil {
		_, _ = w.Write(f.opts.metadataBody)
		return
	}
	resource := f.opts.metadataResource
	if resource == "" {
		resource = f.mcpURL
	}
	_ = json.NewEncoder(w).Encode(oauthex.ProtectedResourceMetadata{
		Resource:             resource,
		AuthorizationServers: []string{f.issuer},
		ScopesSupported:      []string{"read", "write"},
	})
}

func (f *oauthFixture) authorizationServerIssuer() string {
	if f.opts.issuerOverride != "" {
		return f.opts.issuerOverride
	}
	return f.issuer
}

func (f *oauthFixture) tokenAuthMethods() []string {
	if f.opts.tokenAuthMethods != nil {
		return f.opts.tokenAuthMethods
	}
	return []string{"client_secret_basic"}
}

func (f *oauthFixture) serveASMetadata(w http.ResponseWriter, r *http.Request, key string) {
	f.record(key, r.URL.Path)
	issuer := f.authorizationServerIssuer()
	registration := ""
	if f.opts.dynamicRegistration {
		registration = f.issuer + "/register"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(oauthex.AuthServerMeta{
		Issuer:                                     issuer,
		AuthorizationEndpoint:                      f.issuer + "/authorize",
		TokenEndpoint:                              f.issuer + "/token",
		RegistrationEndpoint:                       registration,
		ScopesSupported:                            []string{"read", "write", "admin", "offline_access"},
		ResponseTypesSupported:                     []string{"code"},
		GrantTypesSupported:                        []string{"authorization_code", "refresh_token"},
		TokenEndpointAuthMethodsSupported:          f.tokenAuthMethods(),
		CodeChallengeMethodsSupported:              f.opts.codeMethods,
		ClientIDMetadataDocumentSupported:          f.opts.cimdSupported,
		AuthorizationResponseIssParameterSupported: !f.opts.unadvertisedIssuer,
	})
}

func (f *oauthFixture) serveAuthorize(w http.ResponseWriter, r *http.Request) {
	f.record("authorize", r.URL.Path)
	q := r.URL.Query()
	f.mu.Lock()
	f.authQ = append(f.authQ, q)
	f.mu.Unlock()
	if q.Get("code_challenge_method") != "S256" || !slices.Contains(f.opts.codeMethods, "S256") || q.Get("resource") != f.mcpURL || q.Get("response_type") != "code" {
		http.Error(w, "unsupported authorization request", http.StatusBadRequest)
		return
	}
	code := fmt.Sprintf("code-%d", f.seq.Add(1))
	scopes := strings.Fields(q.Get("scope"))
	granted := f.opts.grantScopes
	if granted == nil || f.seq.Load() > 1 {
		granted = scopes
	}
	f.mu.Lock()
	f.codes[code] = codeRecord{challenge: q.Get("code_challenge"), clientID: q.Get("client_id"), redirect: q.Get("redirect_uri"), resource: q.Get("resource"), scopes: slices.Clone(granted), issuerSent: true}
	f.mu.Unlock()
	callback, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "bad redirect", http.StatusBadRequest)
		return
	}
	state := q.Get("state")
	if f.opts.callbackState != "" {
		state = f.opts.callbackState
	}
	values := callback.Query()
	values.Set("code", code)
	values.Set("state", state)
	if !f.opts.omitCallbackIssuer {
		iss := f.authorizationServerIssuer()
		if f.opts.callbackIssuer != "" {
			iss = f.opts.callbackIssuer
		}
		values.Set("iss", iss)
	}
	callback.RawQuery = values.Encode()
	w.Header().Set("Location", callback.String())
	w.WriteHeader(http.StatusFound)
}

func (f *oauthFixture) serveToken(w http.ResponseWriter, r *http.Request) {
	f.record("token", r.URL.Path)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.tokenQ = append(f.tokenQ, r.Form)
	f.mu.Unlock()
	if r.Form.Get("grant_type") == "refresh_token" {
		if f.opts.refreshInvalidGrant || r.Form.Get("refresh_token") != testRefreshToken {
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		writeToken(w, refreshedToken, []string{"read", "admin"})
		return
	}
	code := r.Form.Get("code")
	if code == "" {
		f.record("token-empty-code", r.URL.Path)
	}
	f.mu.Lock()
	record, ok := f.codes[code]
	if ok && !record.used {
		record.used = true
		f.codes[code] = record
	} else {
		ok = false
	}
	f.mu.Unlock()
	verifierHash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
	challenge := base64.RawURLEncoding.EncodeToString(verifierHash[:])
	clientID, secret, basicOK := r.BasicAuth()
	if basicOK {
		clientID, _ = url.QueryUnescape(clientID)
		secret, _ = url.QueryUnescape(secret)
		f.record("token-basic-auth", r.URL.Path)
	}
	if r.Form.Get("client_secret") != "" {
		f.record("token-form-secret", r.URL.Path)
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	if !basicOK {
		clientID = r.Form.Get("client_id")
	}
	if !ok {
		f.record("token-invalid-code", r.URL.Path)
	}
	if challenge != record.challenge {
		f.record("token-invalid-verifier", r.URL.Path)
	}
	if r.Form.Get("redirect_uri") != record.redirect {
		f.record("token-invalid-redirect", r.URL.Path)
	}
	if r.Form.Get("resource") != record.resource {
		f.record("token-invalid-resource", r.URL.Path)
	}
	if clientID != record.clientID {
		f.record("token-invalid-client", r.URL.Path)
	}
	if record.clientID == testClientID && (!basicOK || secret != testClientSecret) {
		f.record("token-invalid-secret", r.URL.Path)
	}
	if !ok || challenge != record.challenge || r.Form.Get("redirect_uri") != record.redirect || r.Form.Get("resource") != record.resource || clientID != record.clientID || (record.clientID == testClientID && (!basicOK || secret != testClientSecret)) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.tokens[testAccessToken] = slices.Clone(record.scopes)
	f.mu.Unlock()
	writeToken(w, testAccessToken, record.scopes)
}

func writeToken(w http.ResponseWriter, access string, scopes []string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"refresh_token": testRefreshToken,
		"expires_in":    3600,
		"scope":         strings.Join(scopes, " "),
	})
}

func (f *oauthFixture) serveRegister(w http.ResponseWriter, r *http.Request) {
	f.record("register", r.URL.Path)
	var metadata oauthex.ClientRegistrationMetadata
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&metadata); err != nil {
		http.Error(w, "invalid registration", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.regBody = append(f.regBody, metadata)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"redirect_uris":              metadata.RedirectURIs,
		"token_endpoint_auth_method": metadata.TokenEndpointAuthMethod,
		"grant_types":                metadata.GrantTypes,
		"response_types":             metadata.ResponseTypes,
		"client_name":                metadata.ClientName,
		"application_type":           metadata.ApplicationType,
		"client_id":                  testClientID,
		"client_secret":              testClientSecret,
	})
}

func (f *oauthFixture) fetchAuthorization(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, args.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("authorization request failed: %w", ctxErr)
		}
		return nil, errors.New("authorization request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		return nil, fmt.Errorf("authorization endpoint returned status %d", resp.StatusCode)
	}
	callback, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		return nil, err
	}
	want, _ := url.Parse("http://127.0.0.1/callback")
	if callback.Scheme != want.Scheme || callback.Host != want.Host || callback.Path != want.Path {
		return nil, errors.New("authorization callback did not match configured redirect URI")
	}
	return &auth.AuthorizationResult{Code: callback.Query().Get("code"), State: callback.Query().Get("state"), Iss: callback.Query().Get("iss")}, nil
}

func (f *oauthFixture) newHandler(configure func(*auth.AuthorizationCodeHandlerConfig)) (*auth.AuthorizationCodeHandler, error) {
	cfg := &auth.AuthorizationCodeHandlerConfig{
		PreregisteredClient: &oauthex.ClientCredentials{
			ClientID:         testClientID,
			ClientSecretAuth: &oauthex.ClientSecretAuth{ClientSecret: testClientSecret},
			Issuer:           f.authorizationServerIssuer(),
		},
		RedirectURL:              "http://127.0.0.1/callback",
		AuthorizationCodeFetcher: f.fetchAuthorization,
		Client:                   f.client,
	}
	if f.opts.dynamicRegistration {
		cfg.PreregisteredClient = nil
		cfg.DynamicClientRegistrationConfig = &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			RedirectURIs:            []string{cfg.RedirectURL},
			TokenEndpointAuthMethod: "client_secret_basic",
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           []string{"code"},
			ClientName:              "mecatl qualification fixture",
		}}
	}
	if configure != nil {
		configure(cfg)
	}
	return auth.NewAuthorizationCodeHandler(cfg)
}

func (f *oauthFixture) connectController(ctx context.Context) (*mcpadapter.Server, error) {
	store, err := credentialstore.NewMemoryBackend().Open("oauth-sdk-fixture")
	if err != nil {
		return nil, err
	}
	f.t.Cleanup(func() { _ = store.Close() })
	opts := mcpadapter.OAuthOptions{
		Subject: mcpadapter.OAuthSubject{Profile: "qualification", Principal: "fixture"},
		Issuer:  f.issuer,
		Client: mcpadapter.OAuthClientConfig{Preregistered: &oauthex.ClientCredentials{
			ClientID:         testClientID,
			ClientSecretAuth: &oauthex.ClientSecretAuth{ClientSecret: testClientSecret},
			Issuer:           f.issuer,
		}},
		RedirectURL: "http://127.0.0.1/callback",
		Presenter: mcpadapter.OAuthPresenterFunc(func(ctx context.Context, authorizationURL string) (*auth.AuthorizationResult, error) {
			return f.fetchAuthorization(ctx, &auth.AuthorizationArgs{URL: authorizationURL})
		}),
		CredentialStore: store,
		Network:         mcpadapter.OAuthNetworkPolicy{PrivateOrigins: []string{f.server.URL}},
		AllowedScopes:   []string{"read"},
		Timeout:         2 * time.Second,
	}
	mcpadapter.AllowOAuthLoopbackForTest(f.t, &opts)
	server, err := mcpadapter.Connect(ctx, mcpadapter.ServerConfig{Name: "qualification", URL: f.mcpURL, OAuth: &opts}, nil)
	if err != nil {
		return nil, err
	}
	f.t.Cleanup(func() { _ = server.Close() })
	return server, nil
}

func (f *oauthFixture) connect(ctx context.Context, handler *auth.AuthorizationCodeHandler) (*mcpsdk.ClientSession, error) {
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "qualification-client", Version: "1"}, nil)
	return client.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint:             f.mcpURL,
		HTTPClient:           f.client,
		OAuthHandler:         handler,
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
}

package app_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

const (
	loginClientID              = "login-client"
	loginClientSecret          = "login-secret-canary"
	loginAccessToken           = "login-access-canary"
	loginRefreshToken          = "login-refresh-canary"
	loginRotatedAccessToken    = "login-rotated-access-canary"
	loginRotatedRefreshToken   = "login-rotated-refresh-canary"
	loginSuccessorAccessToken  = "login-successor-access-canary"
	loginSuccessorRefreshToken = "login-successor-refresh-canary"
)

type loginBrowser struct {
	client *http.Client
	calls  atomic.Int32
	active atomic.Int32
	max    atomic.Int32
	delay  time.Duration
	block  bool
	mu     sync.Mutex
	urls   []string
}

func (b *loginBrowser) Open(ctx context.Context, authorizationURL string) error {
	b.calls.Add(1)
	b.mu.Lock()
	b.urls = append(b.urls, authorizationURL)
	b.mu.Unlock()
	active := b.active.Add(1)
	defer b.active.Add(-1)
	for current := b.max.Load(); active > current && !b.max.CompareAndSwap(current, active); current = b.max.Load() {
	}
	if b.delay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(b.delay):
		}
	}
	if b.block {
		<-ctx.Done()
		return ctx.Err()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authorizationURL, nil)
	if err != nil {
		return err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("browser authorization failed")
	}
	return nil
}

type loginFixture struct {
	server          *httptest.Server
	mcpServer       *mcpsdk.Server
	mcpHandler      http.Handler
	mu              sync.Mutex
	codes           map[string]loginCode
	authorize       int
	token           int
	refresh         int
	refreshTokens   map[string]int
	metadata        int
	authorized      int
	toolCalls       int
	unexpectedAuth  int
	sessionsOpened  int
	sessionsClosed  int
	failToken       bool
	keepRejecting   bool
	publicResource  bool
	acceptedBearer  string
	initialExpiry   int
	failClose       bool
	mcpBlockState   *loginMCPBlockState
	redirectURL     string
	dcr             bool
	badDCRResponse  bool
	register        int
	basicRequests   int
	tokenForms      []url.Values
	registeredURI   string
	dcrClientID     string
	dcrAccessToken  string
	dcrRegAccess    string
	dcrRefreshToken string
}

type loginMCPBlockState struct {
	method       string
	release      <-chan struct{}
	notification chan<- struct{}
	onceHold     <-chan struct{}
	once         sync.Once
}

type loginCode struct {
	challenge string
	redirect  string
	resource  string
	used      bool
}

func newLoginFixture(t *testing.T) *loginFixture {
	return newLoginFixtureWithTLS(t, false)
}

func newDCRLoginFixture(t *testing.T) *loginFixture {
	f := newLoginFixtureWithTLS(t, true)
	f.dcr = true
	return f
}

func newLoginFixtureWithTLS(t *testing.T, useTLS bool) *loginFixture {
	t.Helper()
	f := &loginFixture{codes: make(map[string]loginCode), refreshTokens: make(map[string]int), acceptedBearer: loginAccessToken, initialExpiry: 3600}
	f.mcpServer = mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: "login-fixture", Version: "1"},
		&mcpsdk.ServerOptions{GetSessionID: func() string {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.sessionsOpened++
			return "login-session-" + strconv.Itoa(f.sessionsOpened)
		}},
	)
	mcpsdk.AddTool(f.mcpServer, &mcpsdk.Tool{Name: "ready", Description: "ready"}, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
		f.mu.Lock()
		f.toolCalls++
		f.mu.Unlock()
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "fixture-ready"}}}, nil, nil
	})
	f.mcpHandler = f.newMCPHandler()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.serveHTTP(w, r)
	})
	if useTLS {
		f.server = httptest.NewTLSServer(handler)
	} else {
		f.server = httptest.NewServer(handler)
	}
	t.Cleanup(f.server.Close)
	return f
}

func (f *loginFixture) origin() string   { return f.server.URL }
func (f *loginFixture) resource() string { return f.server.URL + "/mcp" }
func (f *loginFixture) issuer() string   { return f.server.URL }

func (f *loginFixture) newMCPHandler() http.Handler {
	return mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return f.mcpServer }, nil)
}

type loginFixtureCounts struct {
	authorize, token, refresh, metadata, authenticated, toolCalls, unexpectedAuth, registered, opened, closed int
}

func (f *loginFixture) snapshot() loginFixtureCounts {
	f.mu.Lock()
	defer f.mu.Unlock()
	return loginFixtureCounts{
		authorize: f.authorize, token: f.token, refresh: f.refresh, metadata: f.metadata,
		authenticated: f.authorized, toolCalls: f.toolCalls, unexpectedAuth: f.unexpectedAuth, registered: f.register,
		opened: f.sessionsOpened, closed: f.sessionsClosed,
	}
}

func (f *loginFixture) counts() (authorize, token, authenticated int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authorize, f.token, f.authorized
}

func (f *loginFixture) refreshRequestCount(token string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refreshTokens[token]
}

func (f *loginFixture) sessionCounts() (opened, closed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessionsOpened, f.sessionsClosed
}

func (f *loginFixture) blockAuthenticatedMCP(method string, release <-chan struct{}, notification chan<- struct{}) {
	f.blockAuthenticatedMCPWithOnceHold(method, release, notification, nil)
}

func (f *loginFixture) blockAuthenticatedMCPWithOnceHold(method string, release <-chan struct{}, notification chan<- struct{}, onceHold <-chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if release == nil {
		f.mcpBlockState = nil
		return
	}
	f.mcpBlockState = &loginMCPBlockState{method: method, release: release, notification: notification, onceHold: onceHold}
}

func (f *loginFixture) callbackAddress() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, _ := url.Parse(f.redirectURL)
	return u.Host
}

func (f *loginFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/mcp":
		f.mu.Lock()
		handler := f.mcpHandler
		acceptedBearer := f.acceptedBearer
		f.mu.Unlock()
		if r.Method == http.MethodDelete && f.failClose {
			f.mu.Lock()
			f.sessionsClosed++
			f.mu.Unlock()
			panic(http.ErrAbortHandler)
		}
		if f.publicResource {
			handler.ServeHTTP(w, r)
			if r.Method == http.MethodDelete {
				f.mu.Lock()
				f.sessionsClosed++
				f.mu.Unlock()
			}
			return
		}
		authorization := r.Header.Get("Authorization")
		if authorization != "Bearer "+acceptedBearer || f.keepRejecting {
			if authorization != "" && authorization != "Bearer "+loginAccessToken && authorization != "Bearer "+loginRotatedAccessToken && authorization != "Bearer "+acceptedBearer {
				f.mu.Lock()
				f.unexpectedAuth++
				f.mu.Unlock()
			}
			scope := "read"
			if f.dcr {
				scope = "openid"
			}
			w.Header().Set("WWW-Authenticate", `Bearer scope="`+scope+`"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var rpcRequest struct {
			Method string `json:"method"`
		}
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			_ = json.Unmarshal(body, &rpcRequest)
		}
		f.mu.Lock()
		f.authorized++
		blockState := f.mcpBlockState
		f.mu.Unlock()
		if blockState != nil && (blockState.method == "" || blockState.method == rpcRequest.Method) {
			blockState.once.Do(func() {
				blockState.notification <- struct{}{}
				if blockState.onceHold != nil {
					<-blockState.onceHold
				}
			})
			select {
			case <-blockState.release:
			case <-r.Context().Done():
				return
			}
		}
		handler.ServeHTTP(w, r)
		if r.Method == http.MethodDelete {
			f.mu.Lock()
			f.sessionsClosed++
			f.mu.Unlock()
		}
	case strings.Contains(r.URL.Path, ".well-known/oauth-protected-resource"):
		f.mu.Lock()
		f.metadata++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		scopes := []string{"read"}
		if f.dcr {
			scopes = []string{"openid"}
		}
		_ = json.NewEncoder(w).Encode(oauthex.ProtectedResourceMetadata{
			Resource: f.resource(), AuthorizationServers: []string{f.issuer()}, ScopesSupported: scopes,
		})
	case strings.Contains(r.URL.Path, ".well-known/oauth-authorization-server"):
		f.mu.Lock()
		f.metadata++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		meta := oauthex.AuthServerMeta{
			Issuer: f.issuer(), AuthorizationEndpoint: f.origin() + "/as/authorize", TokenEndpoint: f.origin() + "/as/token",
			ScopesSupported: []string{"read"}, ResponseTypesSupported: []string{"code"}, GrantTypesSupported: []string{"authorization_code", "refresh_token"},
			TokenEndpointAuthMethodsSupported: []string{"client_secret_basic"}, CodeChallengeMethodsSupported: []string{"S256"}, AuthorizationResponseIssParameterSupported: true,
		}
		if f.dcr {
			meta.RegistrationEndpoint = f.origin() + "/as/register"
			meta.ScopesSupported = []string{"openid"}
			meta.GrantTypesSupported = []string{"authorization_code"}
			meta.TokenEndpointAuthMethodsSupported = []string{"none"}
		}
		_ = json.NewEncoder(w).Encode(meta)
	case strings.Contains(r.URL.Path, ".well-known/openid-configuration"):
		http.NotFound(w, r)
	case r.URL.Path == "/as/register":
		f.serveRegister(w, r)
	case r.URL.Path == "/as/authorize":
		f.serveAuthorize(w, r)
	case r.URL.Path == "/as/token":
		f.serveToken(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *loginFixture) serveRegister(w http.ResponseWriter, r *http.Request) {
	var request oauthex.ClientRegistrationMetadata
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "invalid registration", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.register++
	if len(request.RedirectURIs) == 1 {
		f.registeredURI = request.RedirectURIs[0]
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	clientID := f.dcrClientID
	if clientID == "" {
		clientID = loginClientID
	}
	response := map[string]any{"client_id": clientID, "token_endpoint_auth_method": "none", "redirect_uris": request.RedirectURIs, "grant_types": request.GrantTypes, "response_types": request.ResponseTypes, "scope": request.Scope}
	if f.badDCRResponse {
		response["token_endpoint_auth_method"] = "client_secret_basic"
		response["client_secret"] = f.dcrRegAccess
	}
	if f.dcrRegAccess != "" {
		response["registration_access_token"] = f.dcrRegAccess
	}
	_ = json.NewEncoder(w).Encode(response)
}

func (f *loginFixture) serveAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	wantScope, wantClientID := "read", loginClientID
	if f.dcr {
		wantScope = "openid"
		if f.dcrClientID != "" {
			wantClientID = f.dcrClientID
		}
	}
	if q.Get("client_id") != wantClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("resource") != f.resource() || q.Get("scope") != wantScope {
		http.Error(w, "invalid authorization request", http.StatusBadRequest)
		return
	}
	code := "login-code-canary"
	f.mu.Lock()
	f.authorize++
	f.redirectURL = q.Get("redirect_uri")
	f.codes[code] = loginCode{challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), resource: q.Get("resource")}
	f.mu.Unlock()
	callback, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "invalid redirect", http.StatusBadRequest)
		return
	}
	values := callback.Query()
	values.Set("code", code)
	values.Set("state", q.Get("state"))
	values.Set("iss", f.issuer())
	callback.RawQuery = values.Encode()
	http.Redirect(w, r, callback.String(), http.StatusFound)
}

func (f *loginFixture) serveToken(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.token++
	fail := f.failToken
	initialExpiry := f.initialExpiry
	f.mu.Unlock()
	if fail {
		http.Error(w, `{"error":"server_error","error_description":"token-failure-canary"}`, http.StatusInternalServerError)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if f.dcr {
		_, _, basic := r.BasicAuth()
		f.mu.Lock()
		if basic {
			f.basicRequests++
		}
		f.tokenForms = append(f.tokenForms, r.PostForm)
		f.mu.Unlock()
		wantClientID := f.dcrClientID
		if wantClientID == "" {
			wantClientID = loginClientID
		}
		if basic || r.Form.Get("client_id") != wantClientID || r.Form.Get("client_secret") != "" || r.Form.Get("client_assertion") != "" || r.Form.Get("grant_type") != "authorization_code" {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		access := f.dcrAccessToken
		if access == "" {
			access = loginAccessToken
		}
		f.mu.Lock()
		f.acceptedBearer = access
		f.mu.Unlock()
		response := map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": initialExpiry, "scope": "openid"}
		if f.dcrRefreshToken != "" {
			response["refresh_token"] = f.dcrRefreshToken
		}
		f.finishCodeExchange(w, r, response)
		return
	}
	clientID, secret, ok := r.BasicAuth()
	clientID, _ = url.QueryUnescape(clientID)
	secret, _ = url.QueryUnescape(secret)
	if !ok || clientID != loginClientID || secret != loginClientSecret {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	if r.Form.Get("grant_type") == "refresh_token" {
		refreshToken := r.Form.Get("refresh_token")
		var accessToken, successorRefreshToken string
		switch refreshToken {
		case loginRefreshToken:
			accessToken, successorRefreshToken = loginRotatedAccessToken, loginRotatedRefreshToken
		case loginRotatedRefreshToken:
			accessToken, successorRefreshToken = loginSuccessorAccessToken, loginSuccessorRefreshToken
		default:
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.refresh++
		f.refreshTokens[refreshToken]++
		f.acceptedBearer = accessToken
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": accessToken, "token_type": "Bearer", "refresh_token": successorRefreshToken, "expires_in": 3600, "scope": "read"})
		return
	}
	f.finishCodeExchange(w, r, map[string]any{"access_token": loginAccessToken, "token_type": "Bearer", "refresh_token": loginRefreshToken, "expires_in": initialExpiry, "scope": "read"})
}

func (f *loginFixture) finishCodeExchange(w http.ResponseWriter, r *http.Request, response map[string]any) {
	f.mu.Lock()
	record, found := f.codes[r.Form.Get("code")]
	if found && !record.used {
		record.used = true
		f.codes[r.Form.Get("code")] = record
	} else {
		found = false
	}
	f.mu.Unlock()
	verifier := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
	if !found || base64.RawURLEncoding.EncodeToString(verifier[:]) != record.challenge || r.Form.Get("redirect_uri") != record.redirect || r.Form.Get("resource") != record.resource {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func loginConfig(t *testing.T, fixture *loginFixture, store credentialstore.Store) mcp.ServerConfig {
	t.Helper()
	oauth := &mcp.OAuthOptions{
		Subject: mcp.OAuthSubject{Profile: "profile", Principal: "principal"}, Issuer: fixture.issuer(),
		Client:          mcp.OAuthClientConfig{Preregistered: &oauthex.ClientCredentials{ClientID: loginClientID, ClientSecretAuth: &oauthex.ClientSecretAuth{ClientSecret: loginClientSecret}, Issuer: fixture.issuer()}},
		CredentialStore: store, Network: mcp.OAuthNetworkPolicy{PrivateOrigins: []string{fixture.origin()}}, AllowedScopes: []string{"read"}, Timeout: 3 * time.Second,
	}
	mcp.AllowOAuthLoopbackForTest(t, oauth)
	return mcp.ServerConfig{
		Name: "protected", URL: fixture.resource(), Timeout: 3 * time.Second,
		OAuth: oauth,
	}
}

func TestLoginMCPRestoresEncryptedCredentialAfterRestart(t *testing.T) {
	fixture := newLoginFixture(t)
	root := filepath.Join(t.TempDir(), "credentials")
	key := []byte("0123456789abcdef0123456789abcdef")
	store, err := credentialstore.NewEncryptedFile(root, "mcp-login-restart", key)
	if err != nil {
		t.Fatal(err)
	}
	browser := &loginBrowser{client: fixture.server.Client()}
	runtime, err := oauthlogin.New(oauthlogin.Options{Launcher: browser})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.LoginMCP(context.Background(), loginConfig(t, fixture, store), runtime); err != nil {
		t.Fatalf("initial login: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close initial credential store: %v", err)
	}
	// Reopen both runtime and durable store, matching a process restart rather than
	// reusing any in-memory OAuth state.
	store, err = credentialstore.NewEncryptedFile(root, "mcp-login-restart", key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	restartedBrowser := &loginBrowser{client: fixture.server.Client()}
	runtime, err = oauthlogin.New(oauthlogin.Options{Launcher: restartedBrowser})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.LoginMCP(context.Background(), loginConfig(t, fixture, store), runtime); err != nil {
		t.Fatalf("login after restart: %v", err)
	}
	authorize, tokens, authenticated := fixture.counts()
	if authorize != 1 || tokens != 1 || authenticated < 2 || browser.calls.Load() != 1 || restartedBrowser.calls.Load() != 0 {
		t.Fatalf("counts authorize=%d token=%d authenticated=%d initial-browser=%d restarted-browser=%d", authorize, tokens, authenticated, browser.calls.Load(), restartedBrowser.calls.Load())
	}
}

func TestLoginMCPCleansUpTemporarySession(t *testing.T) {
	fixture := newLoginFixture(t)
	store, err := credentialstore.NewMemoryBackend().Open("mcp-login-cleanup")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := oauthlogin.New(oauthlogin.Options{Launcher: &loginBrowser{client: fixture.server.Client()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.LoginMCP(context.Background(), loginConfig(t, fixture, store), runtime); err != nil {
		t.Fatal(err)
	}
	issued, closed := fixture.sessionCounts()
	if issued == 0 || closed != 1 {
		t.Fatalf("temporary MCP session ids issued=%d connection closes=%d", issued, closed)
	}
	if _, err := store.Get(context.Background(), []byte("still-open")); !errors.Is(err, credentialstore.ErrNotFound) {
		t.Fatalf("borrowed store was closed or changed: %v", err)
	}
}

func TestLoginMCPCancellationAfterCallbackReleasesRuntime(t *testing.T) {
	fixture := newLoginFixture(t)
	store, err := credentialstore.NewMemoryBackend().Open("mcp-login-cancel-after-callback")
	if err != nil {
		t.Fatal(err)
	}
	browser := &loginBrowser{client: fixture.server.Client()}
	runtime, err := oauthlogin.New(oauthlogin.Options{Launcher: browser})
	if err != nil {
		t.Fatal(err)
	}
	block := make(chan struct{})
	blocked := make(chan struct{}, 1)
	onceHold := make(chan struct{})
	fixture.blockAuthenticatedMCPWithOnceHold("tools/list", block, blocked, onceHold)
	ctx, cancel := context.WithCancel(context.Background())
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			cancel()
			close(onceHold)
			close(block)
		})
	}
	t.Cleanup(release)
	result := make(chan error, 1)
	go func() { result <- app.LoginMCP(ctx, loginConfig(t, fixture, store), runtime) }()

	select {
	case <-blocked:
	case <-time.After(3 * time.Second):
		t.Fatal("MCP initialize did not block after the OAuth callback")
	}
	// Reconfiguration while the request is inside Once.Do must leave its captured
	// state intact; it only affects subsequent requests.
	fixture.blockAuthenticatedMCP("", nil, nil)
	release()
	var loginErr error
	select {
	case loginErr = <-result:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled login did not return")
	}
	if !errors.Is(loginErr, context.Canceled) || errors.Is(loginErr, context.DeadlineExceeded) {
		t.Fatalf("cancelled login classification = %v", loginErr)
	}
	_, closed := fixture.sessionCounts()
	if closed != 1 {
		t.Fatalf("cancelled listing did not close its temporary session: closes=%d", closed)
	}
	listener, err := net.Listen("tcp4", fixture.callbackAddress())
	if err != nil {
		t.Fatalf("callback listener was not released: %v", err)
	}
	_ = listener.Close()

	if err := app.LoginMCP(context.Background(), loginConfig(t, fixture, store), runtime); err != nil {
		t.Fatalf("subsequent login after cancellation: %v", err)
	}
	if browser.calls.Load() != 1 {
		t.Fatalf("subsequent login did not use the credential saved before cancellation: browser calls=%d", browser.calls.Load())
	}
	_, closed = fixture.sessionCounts()
	if closed != 2 {
		t.Fatalf("subsequent temporary session was not closed: total closes=%d", closed)
	}
}

func TestLoginMCPRejectsPublicResourceWithoutOAuthCredential(t *testing.T) {
	fixture := newLoginFixture(t)
	fixture.publicResource = true
	store, err := credentialstore.NewMemoryBackend().Open("mcp-login-public")
	if err != nil {
		t.Fatal(err)
	}
	browser := &loginBrowser{client: fixture.server.Client()}
	runtime, err := oauthlogin.New(oauthlogin.Options{Launcher: browser})
	if err != nil {
		t.Fatal(err)
	}

	err = app.LoginMCP(context.Background(), loginConfig(t, fixture, store), runtime)
	if !errors.Is(err, app.ErrMCPLoginFailed) {
		t.Fatalf("public-resource login error = %v", err)
	}
	if !errors.Is(err, app.ErrMCPLoginCredential) || err.Error() != app.ErrMCPLoginCredential.Error() || strings.Contains(err.Error(), fixture.server.URL) {
		t.Fatalf("public-resource failure was not redacted: %v", err)
	}
	authorize, tokens, _ := fixture.counts()
	if browser.calls.Load() != 0 || authorize != 0 || tokens != 0 {
		t.Fatalf("public resource unexpectedly ran OAuth: browser=%d authorize=%d token=%d", browser.calls.Load(), authorize, tokens)
	}
	opened, closed := fixture.sessionCounts()
	if opened == 0 || closed != 1 {
		t.Fatalf("temporary server lifecycle opened=%d closed=%d, want at least one open and exactly one close", opened, closed)
	}
}

func TestLoginMCPCloseFailureIsCleanupError(t *testing.T) {
	fixture := newLoginFixture(t)
	fixture.failClose = true
	store, err := credentialstore.NewMemoryBackend().Open("mcp-login-close-error")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := oauthlogin.New(oauthlogin.Options{Launcher: &loginBrowser{client: fixture.server.Client()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.LoginMCP(context.Background(), loginConfig(t, fixture, store), runtime); !errors.Is(err, app.ErrMCPLoginCleanup) || err.Error() != app.ErrMCPLoginCleanup.Error() {
		t.Fatalf("close failure = %v, want redacted cleanup category", err)
	}
	opened, closed := fixture.sessionCounts()
	if opened == 0 || closed != 1 {
		t.Fatalf("temporary server lifecycle opened=%d closed=%d, want at least one open and exactly one close", opened, closed)
	}
}

func TestLoginMCPNoBrowserAndCancellation(t *testing.T) {
	fixture := newLoginFixture(t)
	store, err := credentialstore.NewMemoryBackend().Open("mcp-login-no-browser")
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	runtime, err := oauthlogin.New(oauthlogin.Options{NoBrowser: true, URLWriter: &output})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := app.LoginMCP(ctx, loginConfig(t, fixture, store), runtime); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled login = %v", err)
	}
	if strings.Count(output.String(), "http://") != 1 {
		t.Fatalf("no-browser output did not contain exactly one URL: %q", output.String())
	}
}

func TestLoginMCPSerializesSharedRuntime(t *testing.T) {
	fixtures := []*loginFixture{newLoginFixture(t), newLoginFixture(t)}
	browser := &loginBrowser{client: &http.Client{Timeout: 2 * time.Second}, delay: 30 * time.Millisecond}
	runtime, err := oauthlogin.New(oauthlogin.Options{Launcher: browser})
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, len(fixtures))
	for _, fixture := range fixtures {
		store, openErr := credentialstore.NewMemoryBackend().Open("mcp-login-serial")
		if openErr != nil {
			t.Fatal(openErr)
		}
		cfg := loginConfig(t, fixture, store)
		go func() { errs <- app.LoginMCP(context.Background(), cfg, runtime) }()
	}
	for range fixtures {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if browser.calls.Load() != 2 || browser.max.Load() != 1 {
		t.Fatalf("browser calls=%d max concurrent=%d", browser.calls.Load(), browser.max.Load())
	}
}

func TestLoginMCPRejectsInvalidShapesBeforeRuntime(t *testing.T) {
	fixture := newLoginFixture(t)
	store, err := credentialstore.NewMemoryBackend().Open("mcp-login-validation")
	if err != nil {
		t.Fatal(err)
	}
	browser := &loginBrowser{client: fixture.server.Client()}
	runtime, err := oauthlogin.New(oauthlogin.Options{Launcher: browser})
	if err != nil {
		t.Fatal(err)
	}
	base := loginConfig(t, fixture, store)
	cases := map[string]func(*mcp.ServerConfig){
		"nil runtime": nil,
		"non OAuth":   func(c *mcp.ServerConfig) { c.OAuth = nil },
		"presenter": func(c *mcp.ServerConfig) {
			c.OAuth.Presenter = mcp.OAuthPresenterFunc(func(context.Context, string) (*auth.AuthorizationResult, error) { return nil, nil })
		},
		"redirect":             func(c *mcp.ServerConfig) { c.OAuth.RedirectURL = "http://127.0.0.1/existing" },
		"static authorization": func(c *mcp.ServerConfig) { c.Headers = map[string]string{"authorization": "Bearer secret-canary"} },
		"proxy authorization":  func(c *mcp.ServerConfig) { c.Headers = map[string]string{"Proxy-Authorization": "secret-canary"} },
		"cookie":               func(c *mcp.ServerConfig) { c.Headers = map[string]string{"cookie": "secret-canary"} },
		"read only credentials": func(c *mcp.ServerConfig) {
			c.OAuth.CredentialReader = c.OAuth.CredentialStore
			c.OAuth.CredentialStore = nil
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := base
			oauthCopy := *base.OAuth
			cfg.OAuth = &oauthCopy
			selectedRuntime := runtime
			if mutate == nil {
				selectedRuntime = nil
			} else {
				mutate(&cfg)
			}
			if err := app.LoginMCP(context.Background(), cfg, selectedRuntime); !errors.Is(err, app.ErrMCPLoginConfig) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if err := app.LoginMCPWithOptions(context.Background(), base, runtime, app.MCPLoginOptions{DCRAction: mcp.OAuthDCRLoginRetryRegistration}); !errors.Is(err, app.ErrMCPLoginConfig) {
		t.Fatalf("non-DCR recovery action error = %v", err)
	}
	if browser.calls.Load() != 0 {
		t.Fatalf("invalid config launched browser %d times", browser.calls.Load())
	}
}

func TestLoginMCPRedactsConnectFailure(t *testing.T) {
	fixture := newLoginFixture(t)
	store, err := credentialstore.NewMemoryBackend().Open("mcp-login-connect-failure")
	if err != nil {
		t.Fatal(err)
	}
	browser := &loginBrowser{client: fixture.server.Client()}
	runtime, err := oauthlogin.New(oauthlogin.Options{Launcher: browser})
	if err != nil {
		t.Fatal(err)
	}
	cfg := loginConfig(t, fixture, store)
	cfg.URL = fixture.server.URL + "/missing-connect-canary"
	err = app.LoginMCP(context.Background(), cfg, runtime)
	if !errors.Is(err, app.ErrMCPLoginConnect) || !errors.Is(err, app.ErrMCPLoginFailed) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "missing-connect-canary") || strings.Contains(err.Error(), fixture.server.URL) {
		t.Fatalf("connect error leaked endpoint: %v", err)
	}
	if browser.calls.Load() != 0 {
		t.Fatalf("connect failure launched browser %d times", browser.calls.Load())
	}
}

func TestLoginMCPRedactsAuthenticationFailure(t *testing.T) {
	fixture := newLoginFixture(t)
	fixture.failToken = true
	store, err := credentialstore.NewMemoryBackend().Open("mcp-login-failure")
	if err != nil {
		t.Fatal(err)
	}
	browser := &loginBrowser{client: fixture.server.Client()}
	runtime, err := oauthlogin.New(oauthlogin.Options{Launcher: browser})
	if err != nil {
		t.Fatal(err)
	}
	err = app.LoginMCP(context.Background(), loginConfig(t, fixture, store), runtime)
	if !errors.Is(err, app.ErrMCPLoginAuthorization) || !errors.Is(err, app.ErrMCPLoginFailed) {
		t.Fatalf("error = %v", err)
	}
	for _, secret := range []string{"token-failure-canary", loginClientSecret, loginAccessToken, fixture.server.URL} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked %q: %v", secret, err)
		}
	}
}

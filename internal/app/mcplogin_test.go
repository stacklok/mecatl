package app

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
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

const (
	loginClientID     = "login-client"
	loginClientSecret = "login-secret-canary"
	loginAccessToken  = "login-access-canary"
)

type loginBrowser struct {
	client *http.Client
	calls  atomic.Int32
	active atomic.Int32
	max    atomic.Int32
	delay  time.Duration
	block  bool
}

func (b *loginBrowser) Open(ctx context.Context, authorizationURL string) error {
	b.calls.Add(1)
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
	server         *httptest.Server
	mcpServer      *mcpsdk.Server
	mu             sync.Mutex
	codes          map[string]loginCode
	authorize      int
	token          int
	authorized     int
	sessionsOpened int
	sessionsClosed int
	failToken      bool
	keepRejecting  bool
	publicResource bool
	blockMCP       <-chan struct{}
	blockMCPMethod string
	mcpBlocked     chan<- struct{}
	blockOnce      sync.Once
	redirectURL    string
}

type loginCode struct {
	challenge string
	redirect  string
	resource  string
	used      bool
}

func newLoginFixture(t *testing.T) *loginFixture {
	t.Helper()
	f := &loginFixture{codes: make(map[string]loginCode)}
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
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "ok"}}}, nil, nil
	})
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return f.mcpServer }, nil)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.serveHTTP(mcpHandler, w, r)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *loginFixture) origin() string   { return f.server.URL }
func (f *loginFixture) resource() string { return f.server.URL + "/mcp" }
func (f *loginFixture) issuer() string   { return f.server.URL + "/as" }

func (f *loginFixture) counts() (authorize, token, authenticated int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authorize, f.token, f.authorized
}

func (f *loginFixture) sessionCounts() (opened, closed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessionsOpened, f.sessionsClosed
}

func (f *loginFixture) blockAuthenticatedMCP(method string, block <-chan struct{}, blocked chan<- struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blockMCPMethod = method
	f.blockMCP = block
	f.mcpBlocked = blocked
	f.blockOnce = sync.Once{}
}

func (f *loginFixture) callbackAddress() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, _ := url.Parse(f.redirectURL)
	return u.Host
}

func (f *loginFixture) serveHTTP(mcpHandler http.Handler, w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/mcp":
		if f.publicResource {
			mcpHandler.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+loginAccessToken || f.keepRejecting {
			w.Header().Set("WWW-Authenticate", `Bearer scope="read"`)
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
		block := f.blockMCP
		blockMethod := f.blockMCPMethod
		blocked := f.mcpBlocked
		f.mu.Unlock()
		if block != nil && (blockMethod == "" || blockMethod == rpcRequest.Method) {
			f.blockOnce.Do(func() { blocked <- struct{}{} })
			select {
			case <-block:
			case <-r.Context().Done():
				return
			}
		}
		mcpHandler.ServeHTTP(w, r)
		if r.Method == http.MethodDelete {
			f.mu.Lock()
			f.sessionsClosed++
			f.mu.Unlock()
		}
	case strings.Contains(r.URL.Path, ".well-known/oauth-protected-resource"):
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(oauthex.ProtectedResourceMetadata{
			Resource: f.resource(), AuthorizationServers: []string{f.issuer()}, ScopesSupported: []string{"read"},
		})
	case strings.Contains(r.URL.Path, ".well-known/oauth-authorization-server"):
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(oauthex.AuthServerMeta{
			Issuer: f.issuer(), AuthorizationEndpoint: f.issuer() + "/authorize", TokenEndpoint: f.issuer() + "/token",
			ScopesSupported: []string{"read"}, ResponseTypesSupported: []string{"code"}, GrantTypesSupported: []string{"authorization_code", "refresh_token"},
			TokenEndpointAuthMethodsSupported: []string{"client_secret_basic"}, CodeChallengeMethodsSupported: []string{"S256"}, AuthorizationResponseIssParameterSupported: true,
		})
	case strings.Contains(r.URL.Path, ".well-known/openid-configuration"):
		http.NotFound(w, r)
	case r.URL.Path == "/as/authorize":
		f.serveAuthorize(w, r)
	case r.URL.Path == "/as/token":
		f.serveToken(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *loginFixture) serveAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != loginClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("resource") != f.resource() {
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
	f.mu.Unlock()
	if fail {
		http.Error(w, `{"error":"server_error","error_description":"token-failure-canary"}`, http.StatusInternalServerError)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	clientID, secret, ok := r.BasicAuth()
	clientID, _ = url.QueryUnescape(clientID)
	secret, _ = url.QueryUnescape(secret)
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
	if !found || !ok || clientID != loginClientID || secret != loginClientSecret || base64.RawURLEncoding.EncodeToString(verifier[:]) != record.challenge || r.Form.Get("redirect_uri") != record.redirect || r.Form.Get("resource") != record.resource {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": loginAccessToken, "token_type": "Bearer", "refresh_token": "login-refresh-canary", "expires_in": 3600, "scope": "read"})
}

func loginConfig(t *testing.T, fixture *loginFixture, store credentialstore.Store) mcp.ServerConfig {
	t.Helper()
	return mcp.ServerConfig{
		Name: "protected", URL: fixture.resource(), Timeout: 3 * time.Second,
		OAuth: &mcp.OAuthOptions{
			Subject: mcp.OAuthSubject{Profile: "profile", Principal: "principal"}, Issuer: fixture.issuer(),
			Client:          mcp.OAuthClientConfig{Preregistered: &oauthex.ClientCredentials{ClientID: loginClientID, ClientSecretAuth: &oauthex.ClientSecretAuth{ClientSecret: loginClientSecret}, Issuer: fixture.issuer()}},
			CredentialStore: store, Network: mcp.OAuthNetworkPolicy{PrivateOrigins: []string{fixture.origin()}}, AllowedScopes: []string{"read"}, Timeout: 3 * time.Second,
		},
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
	if err := LoginMCP(context.Background(), loginConfig(t, fixture, store), runtime); err != nil {
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
	if err := LoginMCP(context.Background(), loginConfig(t, fixture, store), runtime); err != nil {
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
	if err := LoginMCP(context.Background(), loginConfig(t, fixture, store), runtime); err != nil {
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
	fixture.blockAuthenticatedMCP("tools/list", block, blocked)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- LoginMCP(ctx, loginConfig(t, fixture, store), runtime) }()

	select {
	case <-blocked:
	case <-time.After(3 * time.Second):
		t.Fatal("MCP initialize did not block after the OAuth callback")
	}
	cancel()
	close(block)
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

	fixture.blockAuthenticatedMCP("", nil, nil)
	if err := LoginMCP(context.Background(), loginConfig(t, fixture, store), runtime); err != nil {
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

	err = LoginMCP(context.Background(), loginConfig(t, fixture, store), runtime)
	if !errors.Is(err, ErrMCPLoginFailed) {
		t.Fatalf("public-resource login error = %v", err)
	}
	if err.Error() != ErrMCPLoginFailed.Error() || strings.Contains(err.Error(), fixture.server.URL) {
		t.Fatalf("public-resource failure was not redacted: %v", err)
	}
	authorize, tokens, _ := fixture.counts()
	if browser.calls.Load() != 0 || authorize != 0 || tokens != 0 {
		t.Fatalf("public resource unexpectedly ran OAuth: browser=%d authorize=%d token=%d", browser.calls.Load(), authorize, tokens)
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
	if err := LoginMCP(ctx, loginConfig(t, fixture, store), runtime); !errors.Is(err, context.DeadlineExceeded) {
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
		go func() { errs <- LoginMCP(context.Background(), cfg, runtime) }()
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
			if err := LoginMCP(context.Background(), cfg, selectedRuntime); !errors.Is(err, ErrMCPLoginConfig) {
				t.Fatalf("error = %v", err)
			}
		})
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
	err = LoginMCP(context.Background(), cfg, runtime)
	if !errors.Is(err, ErrMCPLoginFailed) {
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
	err = LoginMCP(context.Background(), loginConfig(t, fixture, store), runtime)
	if !errors.Is(err, ErrMCPLoginFailed) {
		t.Fatalf("error = %v", err)
	}
	for _, secret := range []string{"token-failure-canary", loginClientSecret, loginAccessToken, fixture.server.URL} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked %q: %v", secret, err)
		}
	}
}

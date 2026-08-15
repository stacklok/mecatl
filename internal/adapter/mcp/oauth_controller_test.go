package mcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

type trackingBody struct{ closed atomic.Bool }

type controllerRoundTripFunc func(*http.Request) (*http.Response, error)

func (f controllerRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func (*trackingBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b *trackingBody) Close() error           { b.closed.Store(true); return nil }

func newControllerForTest(t *testing.T, presenter OAuthPresenter) *OAuthController {
	t.Helper()
	store := newOAuthMemoryStore(t)
	opts := testOAuthOptions(store)
	opts.Presenter = presenter
	controller, err := NewOAuthController(context.Background(), "https://mcp.example/mcp", opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	return controller
}

func TestOAuthControllerConstructionHonorsCallerCancellation(t *testing.T) {
	store := newOAuthMemoryStore(t)
	started := make(chan struct{})
	blocked := &oauthStoreWrapper{Store: store}
	blocked.get = func(ctx context.Context, _ []byte) (credentialstore.Record, error) {
		close(started)
		<-ctx.Done()
		return credentialstore.Record{}, ctx.Err()
	}
	opts := testOAuthOptions(blocked)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := NewOAuthController(ctx, "https://mcp.example/mcp", opts)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("constructor error = %v, want canceled", err)
	}
}

func TestOAuthControllerNilPresenterFailsImmediatelyAndClosesResponse(t *testing.T) {
	controller := newControllerForTest(t, nil)
	body := &trackingBody{}
	err := controller.Authorize(context.Background(), &http.Request{}, &http.Response{Body: body})
	if !errors.Is(err, ErrOAuthLoginRequired) {
		t.Fatalf("Authorize error = %v, want login required", err)
	}
	if !body.closed.Load() {
		t.Fatal("response body was not closed")
	}
}

func TestOAuthControllerPresenterEnforcesExpectedIssuerOrigin(t *testing.T) {
	var calls atomic.Int32
	controller := newControllerForTest(t, OAuthPresenterFunc(func(context.Context, string) (*auth.AuthorizationResult, error) {
		calls.Add(1)
		return &auth.AuthorizationResult{}, nil
	}))
	for _, authorizationURL := range []string{
		"https://mcp.example/authorize?code=canary",
		"https://additional.example/authorize?code=canary",
		"https://issuer.example.evil/authorize?code=canary",
	} {
		if _, err := controller.presentAuthorization(context.Background(), &auth.AuthorizationArgs{URL: authorizationURL}); !errors.Is(err, ErrOAuthUnavailable) {
			t.Fatalf("PresentAuthorization(%q) error = %v, want unavailable", authorizationURL, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("presenter calls for foreign origins = %d, want 0", calls.Load())
	}
	if _, err := controller.presentAuthorization(context.Background(), &auth.AuthorizationArgs{URL: "https://issuer.example:443/authorize?client_id=x"}); err != nil {
		t.Fatalf("canonical default-port issuer URL: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("presenter calls = %d, want 1", calls.Load())
	}
}

func TestOAuthControllerCoalescesConcurrentAuthorization(t *testing.T) {
	controller := newControllerForTest(t, OAuthPresenterFunc(func(context.Context, string) (*auth.AuthorizationResult, error) { return nil, nil }))
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	controller.authorize = func(context.Context, *http.Request, *http.Response) error {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return nil
	}
	const callers = 16
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- controller.Authorize(context.Background(), &http.Request{}, &http.Response{Body: io.NopCloser(strings.NewReader(""))})
		}()
	}
	close(start)
	<-started
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Authorize: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("authorization calls = %d, want 1", got)
	}
}

func TestOAuthControllerStartsNewFlightOnlyForNewChallengeOrReset(t *testing.T) {
	controller := newControllerForTest(t, OAuthPresenterFunc(func(context.Context, string) (*auth.AuthorizationResult, error) { return nil, nil }))
	var calls atomic.Int32
	controller.authorize = func(context.Context, *http.Request, *http.Response) error {
		calls.Add(1)
		return nil
	}
	authorize := func(bearer string) error {
		req, _ := http.NewRequest(http.MethodPost, "https://mcp.example/mcp", nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp := &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     http.Header{"Www-Authenticate": {`Bearer scope="read"`}},
			Body:       io.NopCloser(strings.NewReader("")),
		}
		return controller.Authorize(context.Background(), req, resp)
	}
	if err := authorize("old-token"); err != nil {
		t.Fatal(err)
	}
	if err := authorize("old-token"); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("equivalent completed challenges started %d flights, want 1", got)
	}
	if err := authorize("new-token"); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("changed credential started %d flights, want 2", got)
	}
	if err := controller.ResetCredential(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := authorize("new-token"); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("post-reset challenge started %d flights, want 3", got)
	}
}

func TestOAuthControllerWaiterAndLeaderCancellationAreBounded(t *testing.T) {
	controller := newControllerForTest(t, OAuthPresenterFunc(func(context.Context, string) (*auth.AuthorizationResult, error) { return nil, nil }))
	firstStarted := make(chan struct{})
	var calls atomic.Int32
	controller.authorize = func(ctx context.Context, _ *http.Request, resp *http.Response) error {
		closeOAuthResponse(resp)
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		leaderDone <- controller.Authorize(leaderCtx, &http.Request{}, &http.Response{Body: io.NopCloser(strings.NewReader(""))})
	}()
	<-firstStarted

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	cancelWaiter()
	if err := controller.Authorize(waiterCtx, &http.Request{}, &http.Response{Body: io.NopCloser(strings.NewReader(""))}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v", err)
	}

	liveDone := make(chan error, 1)
	go func() {
		liveDone <- controller.Authorize(context.Background(), &http.Request{}, &http.Response{Body: io.NopCloser(strings.NewReader(""))})
	}()
	cancelLeader()
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error = %v", err)
	}
	select {
	case err := <-liveDone:
		if err != nil {
			t.Fatalf("takeover error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("live waiter did not take over")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("authorization calls = %d, want one leader plus one takeover", got)
	}
}

func TestOAuthControllerRedactsAuthorizationFailure(t *testing.T) {
	const canary = "token-secret-client-principal-issuer-code-state"
	controller := newControllerForTest(t, OAuthPresenterFunc(func(context.Context, string) (*auth.AuthorizationResult, error) { return nil, nil }))
	controller.authorize = func(context.Context, *http.Request, *http.Response) error { return errors.New(canary) }
	err := controller.Authorize(context.Background(), &http.Request{}, &http.Response{Body: io.NopCloser(strings.NewReader(canary))})
	if !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("Authorize error = %v", err)
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatal("OAuth error exposed canary")
	}
}

func TestOAuthControllerCloseCancelsAndJoinsBlockedCredentialMutations(t *testing.T) {
	t.Run("put", func(t *testing.T) {
		base := newOAuthMemoryStore(t)
		started := make(chan struct{})
		exited := make(chan struct{})
		wrapped := &oauthStoreWrapper{Store: base}
		wrapped.put = func(ctx context.Context, _ []byte, _ []byte, _ *credentialstore.Version) (credentialstore.Record, error) {
			close(started)
			<-ctx.Done()
			close(exited)
			return credentialstore.Record{}, ctx.Err()
		}
		opts := testOAuthOptions(wrapped)
		controller, err := NewOAuthController(context.Background(), "https://mcp.example/mcp", opts)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, putErr := controller.state.newTokenSource(context.Background(), testOAuthConfig("https://issuer.example/token"), validOAuthToken("new", "refresh"))
			done <- putErr
		}()
		<-started
		if err := controller.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-exited:
		default:
			t.Fatal("Close returned before blocked Put exited")
		}
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("Put error = %v, want canceled", err)
		}
		key, _ := oauthCredentialKey(testOAuthIdentity())
		if _, err := base.Get(context.Background(), key); !errors.Is(err, credentialstore.ErrNotFound) {
			t.Fatalf("blocked Put changed record: %v", err)
		}
	})

	t.Run("delete", func(t *testing.T) {
		base := newOAuthMemoryStore(t)
		opts := testOAuthOptions(base)
		seed, err := NewOAuthController(context.Background(), "https://mcp.example/mcp", opts)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := seed.state.newTokenSource(context.Background(), testOAuthConfig("https://issuer.example/token"), validOAuthToken("kept", "refresh")); err != nil {
			t.Fatal(err)
		}
		before, err := base.Get(context.Background(), seed.state.key)
		if err != nil {
			t.Fatal(err)
		}
		_ = seed.Close()

		started := make(chan struct{})
		exited := make(chan struct{})
		wrapped := &oauthStoreWrapper{Store: base}
		wrapped.delete = func(ctx context.Context, _ []byte, _ credentialstore.Version) error {
			close(started)
			<-ctx.Done()
			close(exited)
			return ctx.Err()
		}
		opts.CredentialStore = wrapped
		controller, err := NewOAuthController(context.Background(), "https://mcp.example/mcp", opts)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- controller.ResetCredential(context.Background()) }()
		<-started
		if err := controller.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-exited:
		default:
			t.Fatal("Close returned before blocked Delete exited")
		}
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("Delete error = %v, want canceled", err)
		}
		after, err := base.Get(context.Background(), controller.state.key)
		if err != nil {
			t.Fatal(err)
		}
		if !after.Version.Equal(before.Version) || !strings.Contains(string(after.Value), "kept") {
			t.Fatal("blocked Delete changed record or version")
		}
	})
}

func TestOAuthControllerCloseCancelsFlightAndIssuedTokenSource(t *testing.T) {
	controller := newControllerForTest(t, OAuthPresenterFunc(func(context.Context, string) (*auth.AuthorizationResult, error) { return nil, nil }))
	var refreshes atomic.Int32
	controller.state.client = &http.Client{Transport: controllerRoundTripFunc(func(*http.Request) (*http.Response, error) {
		refreshes.Add(1)
		return nil, errors.New("refresh must not run after close")
	})}
	source, err := controller.state.newTokenSource(context.Background(), testOAuthConfig("https://issuer.example/token"), expiredOAuthToken("access", "refresh"))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	controller.authorize = func(ctx context.Context, _ *http.Request, resp *http.Response) error {
		closeOAuthResponse(resp)
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	leaderDone := make(chan error, 1)
	go func() {
		leaderDone <- controller.Authorize(context.Background(), &http.Request{}, &http.Response{Body: io.NopCloser(strings.NewReader(""))})
	}()
	<-started
	waiterDone := make(chan error, 1)
	go func() {
		waiterDone <- controller.Authorize(context.Background(), &http.Request{}, &http.Response{Body: io.NopCloser(strings.NewReader(""))})
	}()
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	for name, done := range map[string]<-chan error{"leader": leaderDone, "waiter": waiterDone} {
		if err := <-done; !errors.Is(err, context.Canceled) && !errors.Is(err, ErrOAuthUnavailable) {
			t.Fatalf("%s error = %v", name, err)
		}
	}
	if _, err := source.Token(); !errors.Is(err, context.Canceled) {
		t.Fatalf("issued TokenSource after close = %v, want canceled", err)
	}
	if refreshes.Load() != 0 {
		t.Fatalf("issued TokenSource made %d refresh requests after close", refreshes.Load())
	}
}

func TestOAuthControllerCloseWaitsForPresenterLifetime(t *testing.T) {
	controller := newControllerForTest(t, OAuthPresenterFunc(func(context.Context, string) (*auth.AuthorizationResult, error) { return nil, nil }))
	started := make(chan struct{})
	release := make(chan struct{})
	controller.authorize = func(context.Context, *http.Request, *http.Response) error {
		close(started)
		<-release
		return nil
	}
	authorizeDone := make(chan error, 1)
	go func() { authorizeDone <- controller.Authorize(context.Background(), &http.Request{}, nil) }()
	<-started
	closeDone := make(chan error, 1)
	go func() { closeDone <- controller.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned while presenter was active: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-authorizeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
}

func TestOAuthControllerResetAndCloseAreIdempotent(t *testing.T) {
	controller := newControllerForTest(t, nil)
	if _, err := controller.state.newTokenSource(context.Background(), testOAuthConfig("https://issuer.example/token"), validOAuthToken("access", "refresh")); err != nil {
		t.Fatal(err)
	}
	if err := controller.ResetCredential(context.Background()); err != nil {
		t.Fatal(err)
	}
	if source, err := controller.TokenSource(context.Background()); err != nil || source != nil {
		t.Fatalf("TokenSource after reset = %v, %v", source, err)
	}
	if err := controller.ResetCredential(context.Background()); err != nil {
		t.Fatalf("second reset: %v", err)
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	if err := controller.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := controller.TokenSource(context.Background()); !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("TokenSource after close = %v", err)
	}
}

func TestServerRetainsOAuthHandlerAcrossReconnectAndRejectsStaticAuthorization(t *testing.T) {
	var gotAuth string
	resource := newTestServer(t, &gotAuth)
	store, err := credentialstore.NewMemoryBackend().Open("mcp-oauth-test")
	if err != nil {
		t.Fatal(err)
	}
	opts := OAuthOptions{
		Subject: OAuthSubject{Profile: "profile", Principal: "principal"},
		Issuer:  resource,
		Client: OAuthClientConfig{Preregistered: &oauthex.ClientCredentials{
			ClientID: "client", ClientSecretAuth: &oauthex.ClientSecretAuth{ClientSecret: "secret"}, Issuer: resource,
		}},
		RedirectURL: "http://127.0.0.1/callback", CredentialStore: store,
		Network:       OAuthNetworkPolicy{PrivateOrigins: []string{resource}},
		AllowedScopes: []string{"read"}, Timeout: time.Second, allowLoopbackForTest: true,
	}
	if _, err := Connect(context.Background(), ServerConfig{Name: "conflict", URL: resource, Headers: map[string]string{"authorization": "Bearer static"}, OAuth: &opts}, nil); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("static/OAuth conflict error = %v", err)
	}
	seed, err := NewOAuthController(context.Background(), resource, opts)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &oauth2.Config{
		ClientID: "client", ClientSecret: "secret", RedirectURL: opts.RedirectURL, Scopes: []string{"read"},
		Endpoint: oauth2.Endpoint{AuthURL: resource, TokenURL: resource, AuthStyle: oauth2.AuthStyleInHeader},
	}
	if _, err := seed.state.newTokenSource(context.Background(), cfg, validOAuthToken("restored-bearer", "refresh")); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	server := connectTest(t, ServerConfig{Name: "oauth-public", URL: resource, OAuth: &opts})
	resourceTransport, ok := server.httpClient.Transport.(oauthResourceRoundTripper)
	if !ok || resourceTransport.base != server.oauth.transport {
		t.Fatal("OAuth MCP resource client did not reuse the pinned OAuth transport")
	}
	if server.oauth.transport.base.Proxy != nil {
		t.Fatal("OAuth MCP resource transport inherited an environment proxy")
	}
	handler := server.oauth.handler
	oldSession := server.session
	server.mu.Lock()
	server.dropped = true
	server.mu.Unlock()
	var echoToolIndex = -1
	for i, candidate := range server.Tools() {
		if candidate.Spec().Name == "mcp__oauth-public__echo" {
			echoToolIndex = i
			break
		}
	}
	if echoToolIndex < 0 {
		t.Fatal("connected server omitted echo tool")
	}
	result, err := server.Tools()[echoToolIndex].Execute(context.Background(), session.NewToolCall("reconnect-call", "mcp__oauth-public__echo", []byte(`{"text":"after-reconnect"}`)), tool.Environment{})
	if err != nil {
		t.Fatalf("public MCP operation: %v", err)
	}
	if result.IsError || result.Content != "echo:after-reconnect" {
		t.Fatalf("public MCP operation result = %#v", result)
	}
	if server.session == oldSession {
		t.Fatal("public MCP operation did not install a replacement session")
	}
	if server.oauth.handler != handler {
		t.Fatal("reconnect replaced the official OAuth handler")
	}
	resourceTransport, ok = server.httpClient.Transport.(oauthResourceRoundTripper)
	if !ok || resourceTransport.base != server.oauth.transport {
		t.Fatal("reconnect replaced the pinned OAuth resource transport")
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer restored-bearer" {
		t.Fatalf("resource Authorization = %q, want restored bearer", gotAuth)
	}
}

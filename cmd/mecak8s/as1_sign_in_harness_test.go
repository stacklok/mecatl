package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mecatuiclient "github.com/stacklok/mecatl/cmd/mecatui/client"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/clientauth"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

// TestHostedAS1RoutesMountWithoutConflicts pins that the hosted AS-1's routes
// are served anonymously on the normal HTTP listener, that a route colliding
// with another mecak8s route refuses startup, and that nothing answers on
// AS-1's routes when it is not hosted.
func TestHostedAS1RoutesMountWithoutConflicts(t *testing.T) {
	built, err := buildIsolated(t, context.Background(), app.Config{
		Workspace: t.TempDir(), UseMock: true, NoSoul: true, NoUserModel: true, Diagnostics: port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	defer built.Close()
	auth := server.NewAuthenticator(server.SecurityConfig{AuthToken: "test-token"})
	defer auth.Close()
	profile := server.ProtectedResourceProfile{Resource: "https://mecatl.test/api", Issuer: "https://mecatl.test/as1", Audience: "https://mecatl.test/api", ClientID: "mecatui"}
	as1Handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("as1")) })
	mux := func(routes ...string) (http.Handler, error) {
		var as1 *app.IdentityBrokerAS1Server
		if routes != nil {
			as1 = &app.IdentityBrokerAS1Server{Issuer: "https://mecatl.test/as1", Routes: routes, Handler: as1Handler}
		}
		return normalHTTPMux(built.Service, auth, profile, "", true, built.MCPBrokerHandlers, built.MCPBrokerCallbackPath, as1)
	}

	hosted, err := mux("/as1/oauth/token", "/as1/.well-known/openid-configuration")
	if err != nil {
		t.Fatalf("normalHTTPMux: %v", err)
	}
	srv := httptest.NewServer(hosted)
	defer srv.Close()
	for _, path := range []string{"/as1/oauth/token", "/as1/.well-known/openid-configuration"} {
		if code := probe(t, srv.URL+path); code != http.StatusOK {
			t.Errorf("hosted AS-1 %s = %d, want 200 without credentials", path, code)
		}
	}
	if code := probe(t, srv.URL+"/readyz"); code != http.StatusOK {
		t.Errorf("/readyz beside the hosted AS-1 = %d, want 200", code)
	}

	for name, route := range map[string]string{
		"health":                      "/readyz",
		"API namespace":               "/v1/oauth/token",
		"API root":                    "/v1",
		"protected-resource metadata": "/.well-known/oauth-protected-resource/api",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := mux(route); err == nil || !strings.Contains(err.Error(), "conflicts with a mecak8s route") {
				t.Fatalf("normalHTTPMux with AS-1 route %s = %v, want a conflict", route, err)
			}
		})
	}

	for _, method := range []string{http.MethodConnect, http.MethodTrace} {
		t.Run(method+" route", func(t *testing.T) {
			methodMux := http.NewServeMux()
			methodMux.Handle(method+" /as1/oauth/token", as1Handler)
			hosted := &app.IdentityBrokerAS1Server{Issuer: "https://mecatl.test/as1", Routes: []string{"/as1/oauth/token"}, Handler: as1Handler}
			if err := mountAS1(methodMux, hosted, server.ProtectedResourceProfile{}); err == nil || !strings.Contains(err.Error(), "conflicts with a mecak8s route") {
				t.Fatalf("mountAS1 over a %s-only route = %v, want a conflict", method, err)
			}
		})
	}

	off, err := mux()
	if err != nil {
		t.Fatalf("normalHTTPMux: %v", err)
	}
	offSrv := httptest.NewServer(off)
	defer offSrv.Close()
	for _, path := range []string{"/as1/oauth/token", "/as1/.well-known/openid-configuration", "/.well-known/openid-configuration", "/oauth/authorize"} {
		req, err := http.NewRequest(http.MethodGet, offSrv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer test-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s without a hosted AS-1 = %d, want 404", path, resp.StatusCode)
		}
	}
}

// TestHostedAS1ServesBesideTheMCPBroker mounts AS-1 beside a complete
// protected MCP broker bundle through the real broker mount: each answers on
// its own issuer's routes, and an AS-1 route taking the broker's callback path
// refuses startup.
func TestHostedAS1ServesBesideTheMCPBroker(t *testing.T) {
	built, err := buildIsolated(t, context.Background(), app.Config{
		Workspace: t.TempDir(), UseMock: true, NoSoul: true, NoUserModel: true, Diagnostics: port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	defer built.Close()
	auth := server.NewAuthenticator(server.SecurityConfig{AuthToken: "test-token"})
	defer auth.Close()
	answer := func(body string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
	}
	broker := answer("broker")
	bundle := mcpbroker.HandlerBundle{
		Authorization: broker, Token: broker, UpstreamCallback: broker, Discovery: broker,
		JWKS: broker, ProtectedResource: broker, VMCP: broker, Callback: broker,
	}
	const brokerCallback = "/oauth/callback"
	as1Routes := []string{"/as1/oauth/authorize", "/as1/oauth/token", "/as1/oauth/callback", "/as1/.well-known/openid-configuration", "/as1/.well-known/jwks.json"}
	as1 := &app.IdentityBrokerAS1Server{Issuer: "https://mecatl.test/as1", Routes: as1Routes, Handler: answer("as1")}
	mux, err := normalHTTPMux(built.Service, auth, server.ProtectedResourceProfile{}, "", true, bundle, brokerCallback, as1)
	if err != nil {
		t.Fatalf("normalHTTPMux: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()
	get := func(path string) string {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	for _, path := range as1Routes {
		if got := get(path); got != "as1" {
			t.Errorf("GET %s = %q, want AS-1", path, got)
		}
	}
	for _, path := range []string{
		"/v1/mcp/broker/oauth/authorize", "/v1/mcp/broker/oauth/token", "/v1/mcp/broker/oauth/callback",
		"/v1/mcp/broker/.well-known/openid-configuration", "/v1/mcp/broker/.well-known/jwks.json", "/v1/mcp/broker/mcp", brokerCallback,
	} {
		if got := get(path); got != "broker" {
			t.Errorf("GET %s = %q, want the MCP broker", path, got)
		}
	}

	rootAS1 := &app.IdentityBrokerAS1Server{Issuer: "https://mecatl.test", Routes: []string{"/oauth/authorize", "/oauth/callback"}, Handler: answer("as1")}
	if _, err := normalHTTPMux(built.Service, auth, server.ProtectedResourceProfile{}, "", true, bundle, brokerCallback, rootAS1); err == nil || !strings.Contains(err.Error(), "conflicts with a mecak8s route") {
		t.Fatalf("normalHTTPMux with AS-1 on the broker's callback path = %v, want a conflict", err)
	}
}

// TestHostedAS1AnonymousSurfaceIsBounded pins the limits on AS-1's anonymous
// routes: an oversized query never reaches AS-1, where /oauth/authorize would
// store it, the body is capped, and a peer past the burst gets 429.
func TestHostedAS1AnonymousSurfaceIsBounded(t *testing.T) {
	built, err := buildIsolated(t, context.Background(), app.Config{
		Workspace: t.TempDir(), UseMock: true, NoSoul: true, NoUserModel: true, Diagnostics: port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	defer built.Close()
	auth := server.NewAuthenticator(server.SecurityConfig{AuthToken: "test-token"})
	defer auth.Close()
	var reached atomic.Int32
	var bodyCapped atomic.Bool
	as1Handler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		if _, err := io.ReadAll(r.Body); err != nil {
			var tooLarge *http.MaxBytesError
			bodyCapped.Store(errors.As(err, &tooLarge))
		}
	})
	as1 := &app.IdentityBrokerAS1Server{Issuer: "https://mecatl.test/as1", Routes: []string{"/as1/oauth/authorize", "/as1/oauth/token"}, Handler: as1Handler}
	mux, err := normalHTTPMux(built.Service, auth, server.ProtectedResourceProfile{}, "", true, built.MCPBrokerHandlers, built.MCPBrokerCallbackPath, as1)
	if err != nil {
		t.Fatalf("normalHTTPMux: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if code := probe(t, srv.URL+"/as1/oauth/authorize?state="+strings.Repeat("s", as1MaxQueryBytes)); code != http.StatusRequestURITooLong {
		t.Errorf("authorize with an oversized query = %d, want 414", code)
	}
	if n := reached.Load(); n != 0 {
		t.Fatalf("an oversized query reached AS-1 %d times", n)
	}
	resp, err := http.Post(srv.URL+"/as1/oauth/token", "application/x-www-form-urlencoded", strings.NewReader(strings.Repeat("a", as1MaxBodyBytes+1)))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !bodyCapped.Load() {
		t.Error("an oversized body was not capped")
	}

	// The two requests above were charged. The bucket refills while the test
	// runs, so the remaining burst passes and a 429 follows soon after it.
	passed := 0
	for range 10 * as1PeerBurst {
		code := probe(t, srv.URL+"/as1/oauth/authorize")
		if code == http.StatusTooManyRequests {
			break
		}
		if code != http.StatusOK {
			t.Fatalf("request within the burst = %d, want 200", code)
		}
		passed++
	}
	if passed < as1PeerBurst-2 || passed == 10*as1PeerBurst {
		t.Fatalf("%d requests passed before a 429, want the burst of %d then 429", passed+2, as1PeerBurst)
	}
}

// TestHostedAS1SignsTheTUIIn hosts AS-1 in mecak8s, with the harness IdP as
// its upstream, and signs the TUI's real client code in there: mecak8s's
// protected-resource metadata names AS-1, the token AS-1 issues passes
// mecak8s's sub_id checks for each session create, each with a refreshed
// token, and session open converts nothing at AS-1. With a signing key file,
// a token AS-1 issued before a restart still passes after it.
func TestHostedAS1SignsTheTUIIn(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	dir := t.TempDir()
	certFile, keyFile := h.WriteServerKeyPair(t, dir)
	caFile := h.WriteCACert(t, dir)
	httpAddr := freeLoopbackPort(t)
	issuer := "https://" + httpAddr + "/as1"
	secret := rand.Text()
	secretFile := filepath.Join(dir, "upstream-client-secret")
	if err := os.WriteFile(secretFile, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	h.IdP.RegisterClient("mecak8s-as1", secret, issuer+"/oauth/callback")
	cfg, err := parseFlags([]string{
		"--mock",
		"--enable-identity-broker", "--no-shell",
		"--permission-mode=yolo", "--guardrails=off",
		"--grpc-addr", freeLoopbackPort(t),
		"--http-addr", httpAddr,
		"--drain-addr", freeLoopbackPort(t),
		"--session-lease-k8s-namespace", "",
		"--tls-cert", certFile, "--tls-key", keyFile,
		"--identity-broker-as1-issuer", issuer,
		"--identity-broker-as1-signing-key-file", h.WriteSigningKey(t, dir),
		"--identity-broker-as1-tui-client-id", identityharness.DefaultClientID,
		"--identity-broker-as1-upstream-issuer", h.IdP.Issuer(),
		"--identity-broker-as1-upstream-client-id", "mecak8s-as1",
		"--identity-broker-as1-upstream-client-secret-file", secretFile,
		"--oidc-audience", identityharness.DefaultAudience,
		"--oidc-resource", "https://mecatl.test",
		"--oidc-owner-identity=sub_id",
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	cfg.diagnostics = port.NopDiagnostics{}
	httpsClient := h.HTTPClient()
	// as1Grants counts the grant types of token requests that reach the
	// hosted AS-1 over the listener.
	var as1Mu sync.Mutex
	as1Grants := map[string]int{}

	// start builds and serves mecak8s as run does, and returns its broker and
	// a function that shuts it down.
	start := func() (identitybroker.Service, func()) {
		composition := appConfig(cfg, port.NopDiagnostics{}, observability{})
		composition.IdentityBrokerHostedAS1.UpstreamCAFile = caFile
		composition.IdentityBrokerHostedAS1.UpstreamAllowPrivateAddresses = true
		built, err := buildIsolated(t, context.Background(), composition)
		if err != nil {
			t.Fatalf("app.Build: %v", err)
		}
		serving := cfg
		hosted := *built.IdentityBrokerHostedAS1
		hosted.Handler = recordGrants(hosted.Handler, func(grant string) {
			as1Mu.Lock()
			defer as1Mu.Unlock()
			as1Grants[grant]++
		})
		serving.as1Server = &hosted
		ctx, cancel := context.WithCancel(context.Background())
		serveErr := make(chan error, 1)
		go func() {
			serveErr <- serveWithDrainWait(ctx, serving, built.Service, observability{}, func(time.Duration) {}, built.MCPBrokerHandlers, built.MCPBrokerCallbackPath)
		}()
		waitForHTTPStatus(t, httpsClient, "https://"+httpAddr+"/readyz", http.StatusOK)
		return built.IdentityBroker, func() {
			cancel()
			select {
			case err := <-serveErr:
				if err != nil {
					t.Errorf("serve: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Error("serve did not stop after cancellation")
			}
			built.Close()
		}
	}
	broker, stop := start()
	stopped := false
	defer func() {
		if !stopped {
			stop()
		}
	}()

	profile := fetchProtectedResourceProfile(t, httpsClient, "https://"+httpAddr+"/.well-known/oauth-protected-resource")
	if len(profile.AuthorizationServers) != 1 || profile.AuthorizationServers[0] != issuer || profile.Audience != identityharness.DefaultAudience ||
		profile.ClientID != identityharness.DefaultClientID || !profile.IdentityBrokerSignIn {
		t.Fatalf("protected-resource metadata = %+v, want the hosted AS-1 as the only authorization server, its audience and TUI client, and broker sign-in", profile)
	}
	identity := clientauth.Identity{
		Target: cfg.grpcAddr, Issuer: profile.AuthorizationServers[0], ClientID: profile.ClientID,
		Audience: profile.Audience, RedirectURI: oauthlogin.ExactRedirectURL, Scopes: []string{"openid", "offline_access"},
	}
	asClient := h.HTTPClient()
	tokenRequests := &resourceRecorder{next: asClient.Transport}
	asClient.Transport = tokenRequests
	presenter := clientauth.PresenterFunc(func(ctx context.Context, authorizationURL string) (oauthlogin.Result, error) {
		back, err := h.AuthorizeWithConsent(ctx, authorizationURL)
		if err != nil {
			return oauthlogin.Result{}, err
		}
		if back.Get("error") != "" {
			return oauthlogin.Result{}, errors.New("authorization refused: " + back.Get("error"))
		}
		return oauthlogin.Result{Code: back.Get("code"), State: back.Get("state"), Iss: back.Get("iss")}, nil
	})
	login := clientauth.LoginConfig{Identity: identity, Presenter: presenter, HTTPClient: asClient, IdentityBrokerSignIn: profile.IdentityBrokerSignIn}
	token, err := clientauth.Login(t.Context(), login)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	store, err := credentialstore.NewMemoryBackend().Open("hosted-as1-sign-in")
	if err != nil {
		t.Fatal(err)
	}
	creds, err := clientauth.NewCredentials(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Save(t.Context(), identity, token, nil); err != nil {
		t.Fatalf("save credential: %v", err)
	}
	registry, err := clientauth.OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	login.Presenter, login.Registry = nil, registry
	source, err := clientauth.NewRefreshSource(t.Context(), creds, login)
	if err != nil {
		t.Fatalf("NewRefreshSource: %v", err)
	}
	defer func() { _ = source.Close() }()
	recorder := &refreshRecorder{source: source}
	tui, err := mecatuiclient.Dial(mecatuiclient.DialConfig{Server: cfg.grpcAddr, UseTLS: true, TLSCAFile: caFile, TokenSource: recorder, IdentityBrokerSignIn: profile.IdentityBrokerSignIn})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = tui.Close() }()

	aliceCompany := identitybroker.Owner{Issuer: h.IdP.Issuer(), Subject: identityharness.Alice}
	checkSession := func(broker identitybroker.Service, id string) {
		t.Helper()
		brokerSession, ok, err := broker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: id})
		if err != nil || !ok || brokerSession.Owner != aliceCompany {
			t.Fatalf("session %s broker session: found=%v err=%v owner=%+v; want one owned by %+v", id, ok, err, brokerSession.Owner, aliceCompany)
		}
		// Session open is unchanged: the broker is not pointed at the hosted
		// AS-1, so it converts no token and holds no refresh token.
		if brokerSession.HoldsRefreshToken {
			t.Fatalf("session %s broker session holds a refresh token; session open must not convert at the hosted AS-1", id)
		}
	}
	for range 2 {
		id, _, _, err := tui.CreateSession(t.Context(), mecatlv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED, mecatuiclient.ModelSelection{})
		if err != nil {
			t.Fatalf("CreateSession through the TUI client: %v", err)
		}
		checkSession(broker, id)
	}
	jtis := recorder.refreshedJTIs()
	if len(jtis) != 2 || jtis[0] == "" || jtis[0] == jtis[1] {
		t.Fatalf("session creates refreshed %d times with distinct jti=%v; want 2 distinct", len(jtis), len(jtis) == 2 && jtis[0] != jtis[1])
	}
	if grants := tokenRequests.grantsWithResource(profile.Audience); grants["authorization_code"] != 1 || grants["refresh_token"] < 2 || grants["missing"] != 0 {
		t.Fatalf("token requests naming the audience as resource = %v, want the code exchange and every refresh", grants)
	}
	if got := h.IdP.RefreshTokensIssued(identityharness.Alice); got != 1 {
		t.Fatalf("upstream refresh tokens issued = %d, want 1: AS-1 asks the IdP for offline_access with prompt=consent", got)
	}
	as1Mu.Lock()
	reachedAS1 := maps.Clone(as1Grants)
	as1Mu.Unlock()
	if reachedAS1["authorization_code"] != 1 || reachedAS1[tokenExchangeGrant] != 0 {
		t.Fatalf("token requests at the hosted AS-1 by grant = %v, want the code exchange and no token exchange", reachedAS1)
	}

	// A token AS-1 issued before a restart, never presented, still passes
	// after it: the signing key comes from the file.
	beforeRestart, err := source.Refresh(t.Context())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	_ = tui.Close()
	stop()
	stopped = true
	broker, stop = start()
	stopped = false
	restarted, err := mecatuiclient.Dial(mecatuiclient.DialConfig{Server: cfg.grpcAddr, UseTLS: true, TLSCAFile: caFile, AuthToken: beforeRestart})
	if err != nil {
		t.Fatalf("Dial after restart: %v", err)
	}
	defer func() { _ = restarted.Close() }()
	id, _, _, err := restarted.CreateSession(t.Context(), mecatlv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED, mecatuiclient.ModelSelection{})
	if err != nil {
		t.Fatalf("CreateSession after restart with a token issued before it: %v", err)
	}
	checkSession(broker, id)
	as1Mu.Lock()
	defer as1Mu.Unlock()
	if as1Grants[tokenExchangeGrant] != 0 {
		t.Fatalf("token requests at the hosted AS-1 by grant = %v after restart, want no token exchange", as1Grants)
	}
}

// tokenExchangeGrant is the RFC 8693 grant type session open would use.
const tokenExchangeGrant = "urn:ietf:params:oauth:grant-type:token-exchange"

// recordGrants passes each token-endpoint request's grant_type to record
// before next serves it.
func recordGrants(next http.Handler, record func(grant string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth/token") {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "unreadable body", http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			form, _ := url.ParseQuery(string(body))
			record(form.Get("grant_type"))
		}
		next.ServeHTTP(w, r)
	})
}

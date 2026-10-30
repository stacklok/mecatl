package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	mecatuiclient "github.com/stacklok/mecatl/cmd/mecatui/client"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/clientauth"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

// refreshRecorder passes a RefreshSource through and keeps the jti of every
// token a forced refresh returned, never the token itself.
type refreshRecorder struct {
	source *clientauth.RefreshSource
	mu     sync.Mutex
	jtis   []string
}

func (r *refreshRecorder) Token(ctx context.Context) (string, error) { return r.source.Token(ctx) }

func (r *refreshRecorder) Refresh(ctx context.Context) (string, error) {
	token, err := r.source.Refresh(ctx)
	if err != nil {
		return "", err
	}
	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, claims); err != nil {
		return "", errors.New("refreshed token is not a JWT")
	}
	jti, _ := claims["jti"].(string)
	r.mu.Lock()
	r.jtis = append(r.jtis, jti)
	r.mu.Unlock()
	return token, nil
}

func (r *refreshRecorder) refreshedJTIs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.jtis...)
}

// protectedResourceDocument is the part of the RFC 9728 document the TUI's
// sign-in uses.
type protectedResourceDocument struct {
	AuthorizationServers []string `json:"authorization_servers"`
	Audience             string   `json:"com.stacklok.mecatl.audience"`
	ClientID             string   `json:"com.stacklok.mecatl.client_id"`
	IdentityBrokerSignIn bool     `json:"com.stacklok.mecatl.identity_broker_sign_in"`
}

func fetchProtectedResourceProfile(t *testing.T, client *http.Client, metadataURL string) protectedResourceDocument {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, metadataURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("fetch protected-resource metadata: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	var doc protectedResourceDocument
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&doc) != nil {
		t.Fatalf("protected-resource metadata: status %d, want a JSON document", res.StatusCode)
	}
	return doc
}

// resourceRecorder counts token endpoint requests by grant type, and those
// whose RFC 8707 resource is not the expected one under "missing". It keeps no
// form values, so no token is retained.
type resourceRecorder struct {
	next      http.RoundTripper
	mu        sync.Mutex
	resources map[string][]string
}

func (r *resourceRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost && req.Body != nil {
		body, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		if form, err := url.ParseQuery(string(body)); err == nil && form.Get("grant_type") != "" {
			r.mu.Lock()
			if r.resources == nil {
				r.resources = map[string][]string{}
			}
			r.resources[form.Get("grant_type")] = append(r.resources[form.Get("grant_type")], strings.Join(form["resource"], ","))
			r.mu.Unlock()
		}
	}
	return r.next.RoundTrip(req)
}

func (r *resourceRecorder) grantsWithResource(want string) map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	counts := map[string]int{}
	for grant, resources := range r.resources {
		for _, got := range resources {
			if got == want {
				counts[grant]++
			} else {
				counts["missing"]++
			}
		}
	}
	return counts
}

// TestTUISignsInAtAS1AndOpensEachSessionWithAFreshToken signs the TUI's real
// client code in at the offline AS-1 (authorization code + PKCE, RFC 8707
// resource as mecak8s's protected-resource metadata asks), then drives mecak8s over gRPC through the TUI client with the
// clientauth refresh source: each session create carries a freshly refreshed
// token with its own jti, and mecak8s accepts each and opens a broker session
// owned by the signed-in company identity.
func TestTUISignsInAtAS1AndOpensEachSessionWithAFreshToken(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{Clients: []identityharness.Client{
		{ID: identityharness.DefaultClientID, RedirectURIs: []string{oauthlogin.ExactRedirectURL}},
	}})
	cfg, err := parseFlags([]string{
		"--mock",
		"--enable-identity-broker", "--no-shell",
		"--permission-mode=yolo", "--guardrails=off",
		"--grpc-addr", freeLoopbackPort(t),
		"--http-addr", freeLoopbackPort(t),
		"--drain-addr", freeLoopbackPort(t),
		"--session-lease-k8s-namespace", "",
		"--oidc-issuer", h.AS1.Issuer(),
		"--oidc-audience", h.AS1.Audience(),
		"--oidc-allow-private-https-issuer",
		"--oidc-ca-cert-file", h.WriteCACert(t, t.TempDir()),
		"--oidc-owner-identity=sub_id",
		"--oidc-accepted-client-ids", identityharness.DefaultClientID,
		"--oidc-resource", "https://mecatl.test",
		"--oidc-client-id", identityharness.DefaultClientID,
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	cfg.diagnostics = port.NopDiagnostics{}
	built, err := buildIsolated(t, context.Background(), appConfig(cfg, port.NopDiagnostics{}, observability{}))
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	defer built.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- serveWithDrainWait(ctx, cfg, built.Service, observability{}, func(time.Duration) {}, builtServing{brokerHandlers: built.MCPBrokerHandlers, brokerCallbackPath: built.MCPBrokerCallbackPath})
	}()
	waitForHTTPStatus(t, http.DefaultClient, "http://"+cfg.httpAddr+"/readyz", http.StatusOK)

	// The client learns its sign-in profile, including whether to send the
	// RFC 8707 resource, from mecak8s's protected-resource metadata.
	profile := fetchProtectedResourceProfile(t, http.DefaultClient, "http://"+cfg.httpAddr+"/.well-known/oauth-protected-resource")
	if len(profile.AuthorizationServers) != 1 || profile.AuthorizationServers[0] != h.AS1.Issuer() || profile.Audience != h.AS1.Audience() || profile.ClientID != identityharness.DefaultClientID {
		t.Fatalf("protected-resource metadata = %+v, want the AS-1 issuer, audience, and client", profile)
	}
	if !profile.IdentityBrokerSignIn {
		t.Fatal("protected-resource metadata does not ask for the resource indicator with --oidc-owner-identity=sub_id")
	}
	identity := clientauth.Identity{
		Target: cfg.grpcAddr, Issuer: profile.AuthorizationServers[0], ClientID: profile.ClientID,
		Audience: profile.Audience, RedirectURI: oauthlogin.ExactRedirectURL, Scopes: []string{"openid"},
	}
	asClient := h.HTTPClient()
	tokenRequests := &resourceRecorder{next: asClient.Transport}
	asClient.Transport = tokenRequests
	presenter := clientauth.PresenterFunc(func(ctx context.Context, authorizationURL string) (oauthlogin.Result, error) {
		if u, err := url.Parse(authorizationURL); err != nil || u.Query().Get("resource") != profile.Audience {
			return oauthlogin.Result{}, errors.New("authorization request did not name the audience as resource")
		}
		back, err := h.Authorize(ctx, authorizationURL)
		if err != nil {
			return oauthlogin.Result{}, err
		}
		return oauthlogin.Result{Code: back.Get("code"), State: back.Get("state"), Iss: back.Get("iss")}, nil
	})
	token, err := clientauth.Login(t.Context(), clientauth.LoginConfig{Identity: identity, Presenter: presenter, HTTPClient: asClient, IdentityBrokerSignIn: profile.IdentityBrokerSignIn})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	store, err := credentialstore.NewMemoryBackend().Open("tui-sign-in")
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
	source, err := clientauth.NewRefreshSource(t.Context(), creds, clientauth.LoginConfig{Identity: identity, HTTPClient: asClient, Registry: registry, IdentityBrokerSignIn: profile.IdentityBrokerSignIn})
	if err != nil {
		t.Fatalf("NewRefreshSource: %v", err)
	}
	defer func() { _ = source.Close() }()
	recorder := &refreshRecorder{source: source}
	tui, err := mecatuiclient.Dial(mecatuiclient.DialConfig{Server: cfg.grpcAddr, TokenSource: recorder, IdentityBrokerSignIn: profile.IdentityBrokerSignIn})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = tui.Close() }()

	aliceCompany := identitybroker.Owner{Issuer: h.IdP.Issuer(), Subject: identityharness.Alice}
	for range 2 {
		id, _, _, err := tui.CreateSession(t.Context(), mecatlv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED, mecatuiclient.ModelSelection{})
		if err != nil {
			t.Fatalf("CreateSession through the TUI client: %v", err)
		}
		brokerSession, ok, err := built.IdentityBroker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: id})
		if err != nil || !ok || brokerSession.Owner != aliceCompany {
			t.Fatalf("session %s broker session: found=%v err=%v owner=%+v; want one owned by %+v", id, ok, err, brokerSession.Owner, aliceCompany)
		}
	}
	jtis := recorder.refreshedJTIs()
	if len(jtis) != 2 || jtis[0] == "" || jtis[0] == jtis[1] {
		t.Fatalf("session creates refreshed %d times with distinct jti=%v; want 2 distinct", len(jtis), len(jtis) == 2 && jtis[0] != jtis[1])
	}
	if grants := tokenRequests.grantsWithResource(profile.Audience); grants["authorization_code"] != 1 || grants["refresh_token"] < 2 || grants["missing"] != 0 {
		t.Fatalf("token requests naming the audience as resource = %v, want the code exchange and every refresh", grants)
	}

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop after cancellation")
	}
}

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// TestIdentityBrokerAgainstOfflineHarness starts mecak8s with the identity
// broker on and AS-1 as its token issuer, all offline. Users sign in through
// the harness's AS-1 and upstream IdP; their AS-1 access tokens authenticate
// API requests and own the sessions they create, and tokens AS-1 would not
// issue are refused. Session open converts each user's access token at AS-1
// into a refresh token the broker holds for the session. Later identity work
// extends this test as it adds steps.
func TestIdentityBrokerAgainstOfflineHarness(t *testing.T) {
	// The broker authenticates to AS-1 with its client key, which AS-1 holds
	// registered for the broker's client.
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := identitybroker.NewClientAssertionSigner(clientKey, 0)
	if err != nil {
		t.Fatal(err)
	}
	h := identityharness.New(t, identityharness.Options{
		BrokerKeys: []identityharness.BrokerKey{{KeyID: signer.JWKS().Keys[0].KeyID, Public: &clientKey.PublicKey}},
	})
	cfg, err := parseFlags([]string{
		"--mock",
		"--enable-identity-broker", "--no-shell", // the identity broker refuses to run beside a shell
		"--permission-mode=yolo", "--guardrails=off", // unchecked allow-all is yolo
		"--grpc-addr", freeLoopbackPort(t),
		"--http-addr", freeLoopbackPort(t),
		"--drain-addr", freeLoopbackPort(t),
		"--session-lease-k8s-namespace", "",
		"--oidc-issuer", h.AS1.Issuer(),
		"--oidc-audience", h.AS1.Audience(),
		"--oidc-allow-private-https-issuer",
		"--oidc-ca-cert-file", h.WriteCACert(t, t.TempDir()),
		"--oidc-owner-identity=sub_id",
		"--oidc-accepted-client-ids", identityharness.DefaultClientID + ",studio",
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	cfg.diagnostics = port.NopDiagnostics{}
	appCfg := appConfig(cfg, port.NopDiagnostics{}, observability{})
	// No flag sets AS-1's endpoints yet (#2490); the test points the broker at
	// the harness's AS-1 directly.
	appCfg.IdentityBrokerAS1 = &app.IdentityBrokerAS1{
		Issuer: h.AS1.Issuer(), TokenEndpoint: h.AS1.TokenEndpoint(), HTTPClient: h.HTTPClient(), ClientKey: clientKey,
	}
	built, err := buildIsolated(t, context.Background(), appCfg)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	defer built.Close()
	if built.IdentityBroker == nil {
		t.Fatal("--enable-identity-broker built no identity broker")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- serveWithDrainWait(ctx, cfg, built.Service, observability{}, func(time.Duration) {}, built.MCPBrokerHandlers, built.MCPBrokerCallbackPath)
	}()
	api := "http://" + cfg.httpAddr
	waitForHTTPStatus(t, http.DefaultClient, api+"/readyz", http.StatusOK)

	alice, err := h.SignIn(t.Context(), identityharness.SignInRequest{User: identityharness.Alice})
	if err != nil {
		t.Fatalf("Alice signs in: %v", err)
	}
	bob, err := h.SignIn(t.Context(), identityharness.SignInRequest{User: identityharness.Bob})
	if err != nil {
		t.Fatalf("Bob signs in: %v", err)
	}

	status, body := apiCall(t, http.MethodPost, api+"/v1/sessions", alice.AccessToken, "{}")
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("Alice creates a session: status %d: %s", status, body)
	}
	var created struct {
		ID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.ID == "" {
		t.Fatalf("create session response %s: %v", body, err)
	}
	// The session owner is Alice's company identity from sub_id, not AS-1's
	// issuer and pseudonymous subject.
	aliceCompany := identitybroker.Owner{Issuer: h.IdP.Issuer(), Subject: identityharness.Alice}
	bobCompany := identitybroker.Owner{Issuer: h.IdP.Issuer(), Subject: identityharness.Bob}
	if owners := listSessionOwners(t, api, alice.AccessToken); owners[created.ID] != aliceCompany {
		t.Errorf("Alice's sessions %v: %s owner = %+v, want %+v", owners, created.ID, owners[created.ID], aliceCompany)
	}
	// The second accepted client carries the same identity.
	studio := h.AS1.MintAccessToken(identityharness.AccessTokenSpec{User: identityharness.Alice, ClientID: "studio"})
	if owners := listSessionOwners(t, api, studio); owners[created.ID] != aliceCompany {
		t.Errorf("Alice through studio: sessions %v, want %s owned by %+v", owners, created.ID, aliceCompany)
	}
	if _, listed := listSessionOwners(t, api, bob.AccessToken)[created.ID]; listed {
		t.Errorf("Bob's sessions include Alice's %s", created.ID)
	}

	// Each user's session has a broker session owned by their company identity.
	// Running the session keeps it, a fork gets its own, releasing the session
	// keeps it, and deleting the session closes it.
	findBrokerSession := func(id string) (identitybroker.Session, bool) {
		t.Helper()
		s, ok, err := built.IdentityBroker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: id})
		if err != nil {
			t.Fatalf("FindSession(%s): %v", id, err)
		}
		return s, ok
	}
	opened, ok := findBrokerSession(created.ID)
	if !ok {
		t.Fatalf("Alice's session %s has no broker session", created.ID)
	}
	if opened.Owner != aliceCompany {
		t.Errorf("broker session owner = %+v, want Alice's company identity %+v", opened.Owner, aliceCompany)
	}
	// Creating the session converted Alice's sign-in at AS-1: the broker holds
	// a refresh token for it, from AS-1's one grant.
	if !opened.HoldsRefreshToken || h.AS1.GrantsIssued() != 1 {
		t.Fatalf("Alice's broker session %+v after %d grants; want it holding the refresh token of AS-1's one grant", opened, h.AS1.GrantsIssued())
	}

	status, body = apiCall(t, http.MethodPost, api+"/v1/sessions", bob.AccessToken, "{}")
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("Bob creates a session: status %d: %s", status, body)
	}
	var bobCreated struct {
		ID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(body), &bobCreated); err != nil || bobCreated.ID == "" {
		t.Fatalf("create session response %s: %v", body, err)
	}
	if bobs, ok := findBrokerSession(bobCreated.ID); !ok || bobs.Owner != bobCompany || !bobs.HoldsRefreshToken {
		t.Errorf("Bob's broker session = %+v, %v; want one owned by %+v holding a refresh token", bobs, ok, bobCompany)
	}
	grants := h.AS1.GrantsIssued()

	// The run passes run entry, which finds the open broker session and keeps it.
	const aliceTitle, alicePrompt = "alice-private-title", "alice-private-prompt"
	if status, body := apiCall(t, http.MethodPost, api+"/v1/sessions/"+created.ID+"/rename", alice.AccessToken, `{"title":"`+aliceTitle+`"}`); status != http.StatusOK {
		t.Fatalf("Alice renames her session: status %d: %s", status, body)
	}
	if status, body := apiCall(t, http.MethodPost, api+"/v1/sessions/"+created.ID+"/prompt", alice.AccessToken, `{"text":"`+alicePrompt+`"}`); status != http.StatusOK {
		t.Fatalf("Alice prompts her session: status %d: %s", status, body)
	}
	if got, ok := findBrokerSession(created.ID); !ok || got != opened {
		t.Errorf("after a run, broker session = %+v, %v; want %+v", got, ok, opened)
	}
	// A session that holds a refresh token does not spend later requests' tokens.
	if n := h.AS1.GrantsIssued(); n != grants {
		t.Errorf("the run converted a token: %d grants, want %d", n, grants)
	}

	// Another user's session is not found, never forbidden, so its existence,
	// title, and prompts stay undisclosed. Delete is idempotent success for an
	// absent or foreign session alike, and deletes nothing.
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "", http.StatusNotFound},
		{http.MethodGet, "/events", http.StatusNotFound},
		{http.MethodGet, "/transcript", http.StatusNotFound},
		{http.MethodPost, "/fork", http.StatusNotFound},
		{http.MethodPost, "/delete", http.StatusNoContent},
	} {
		body := ""
		if c.method == http.MethodPost {
			body = "{}"
		}
		status, resp := apiCall(t, c.method, api+"/v1/sessions/"+created.ID+c.path, bob.AccessToken, body)
		if status != c.want || (c.want == http.StatusNotFound && !strings.Contains(resp, "session_not_found")) {
			t.Errorf("Bob %s %s on Alice's session: status %d: %s; want %d", c.method, c.path, status, resp, c.want)
		}
		if strings.Contains(resp, aliceTitle) || strings.Contains(resp, alicePrompt) {
			t.Errorf("Bob %s %s on Alice's session disclosed her title or prompt: %s", c.method, c.path, resp)
		}
	}
	if status, resp := apiCall(t, http.MethodGet, api+"/v1/sessions/"+created.ID, alice.AccessToken, ""); status != http.StatusOK || !strings.Contains(resp, aliceTitle) {
		t.Errorf("after Bob's delete, Alice gets her session: status %d: %s; want it intact", status, resp)
	}
	if status, resp := apiCall(t, http.MethodGet, api+"/v1/sessions/"+created.ID+"/transcript", alice.AccessToken, ""); status != http.StatusOK || !strings.Contains(resp, alicePrompt) {
		t.Errorf("Alice reads her transcript: status %d: %s; want her prompt", status, resp)
	}

	// Alice's sign-in token was converted for her first session, and a token
	// converts once: a fork with it is refused, retryably, before it creates
	// anything.
	sessionsBefore := len(listSessionOwners(t, api, alice.AccessToken))
	status, body, challenge := apiRequest(t, http.MethodPost, api+"/v1/sessions/"+created.ID+"/fork", alice.AccessToken, "{}")
	if status != http.StatusUnauthorized || !strings.Contains(body, `"bearer_rejected"`) || challenge != `Bearer error="invalid_token"` {
		t.Fatalf("Alice forks with her spent token: status %d, challenge %q: %s; want 401 bearer_rejected with an invalid_token challenge", status, challenge, body)
	}
	if n := len(listSessionOwners(t, api, alice.AccessToken)); n != sessionsBefore {
		t.Fatalf("the refused fork created a session: %d sessions, want %d", n, sessionsBefore)
	}
	// Her client refreshes its token and retries.
	refreshed := h.AS1.MintAccessToken(identityharness.AccessTokenSpec{User: identityharness.Alice})
	status, body = apiCall(t, http.MethodPost, api+"/v1/sessions/"+created.ID+"/fork", refreshed, "{}")
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("Alice forks her session: status %d: %s", status, body)
	}
	var forked struct {
		ID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(body), &forked); err != nil || forked.ID == "" {
		t.Fatalf("fork response %s: %v", body, err)
	}
	if fork, ok := findBrokerSession(forked.ID); !ok || fork.ID == opened.ID || fork.Owner != opened.Owner || !fork.HoldsRefreshToken {
		t.Errorf("fork's broker session = %+v, %v; want its own, owned by Alice, holding a refresh token", fork, ok)
	}

	if status, body := apiCall(t, http.MethodDelete, api+"/v1/sessions/"+created.ID, alice.AccessToken, ""); status >= 300 {
		t.Fatalf("Alice releases her session: status %d: %s", status, body)
	}
	if got, ok := findBrokerSession(created.ID); !ok || got != opened {
		t.Errorf("after releasing the session, broker session = %+v, %v; want %+v", got, ok, opened)
	}
	if status, body := apiCall(t, http.MethodPost, api+"/v1/sessions/"+created.ID+"/delete", alice.AccessToken, ""); status >= 300 {
		t.Fatalf("Alice deletes her session: status %d: %s", status, body)
	}
	if _, ok := findBrokerSession(created.ID); ok {
		t.Error("deleting Alice's session left its broker session open")
	}

	for name, token := range map[string]string{
		"no token":       "",
		"foreign key":    h.AS1.MintAccessToken(identityharness.AccessTokenSpec{ForeignKey: true}),
		"other audience": h.AS1.MintAccessToken(identityharness.AccessTokenSpec{Audience: "https://other.test/api"}),
		"other issuer":   h.AS1.MintAccessToken(identityharness.AccessTokenSpec{Issuer: h.IdP.Issuer()}),
		"expired":        h.AS1.MintAccessToken(identityharness.AccessTokenSpec{Lifetime: -time.Hour}),
		"typ JWT":        h.AS1.MintAccessToken(identityharness.AccessTokenSpec{Type: "JWT"}),
		"no typ":         h.AS1.MintAccessToken(identityharness.AccessTokenSpec{OmitType: true}),
		"alg none":       unsignedToken(t, h.AS1.Issuer(), h.AS1.Audience()), // toolhive-core's fixed algorithm allowlist rejects this
		"other client":   h.AS1.MintAccessToken(identityharness.AccessTokenSpec{ClientID: "rogue"}),
		"no sub_id":      h.AS1.MintAccessToken(identityharness.AccessTokenSpec{OmitSubID: true}),
		"sub_id format":  h.AS1.MintAccessToken(identityharness.AccessTokenSpec{SubID: map[string]string{"format": "email", "iss": h.IdP.Issuer(), "sub": identityharness.Alice}}),
		"sub_id string":  h.AS1.MintAccessToken(identityharness.AccessTokenSpec{SubID: identityharness.Alice}),
		"sub_id no sub":  h.AS1.MintAccessToken(identityharness.AccessTokenSpec{SubID: map[string]string{"format": "iss_sub", "iss": h.IdP.Issuer()}}),
	} {
		status, _, challenge := apiRequest(t, http.MethodGet, api+"/v1/sessions", token, "")
		if status != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, status)
		}
		want := `Bearer error="invalid_token"`
		if token == "" {
			want = "Bearer"
		}
		if challenge != want {
			t.Errorf("%s: WWW-Authenticate = %q, want %q", name, challenge, want)
		}
	}

	// Upgrade: a session created before AS-1, when mecak8s validated the
	// company IdP's tokens directly, is owned by the company identity. The
	// same user arriving through AS-1 still owns it; another user does not.
	preAS1 := session.WithPrincipal(t.Context(), &session.Principal{Issuer: h.IdP.Issuer(), Subject: identityharness.Alice, GrantType: session.GrantTypeUser})
	legacy, err := built.Service.CreateSession(preAS1, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("create a session under the company IdP identity: %v", err)
	}
	if status, body := apiCall(t, http.MethodGet, api+"/v1/sessions/"+string(legacy.ID), alice.AccessToken, ""); status != http.StatusOK {
		t.Errorf("Alice through AS-1 gets her pre-AS-1 session: status %d: %s", status, body)
	}
	if status, _ := apiCall(t, http.MethodGet, api+"/v1/sessions/"+string(legacy.ID), bob.AccessToken, ""); status != http.StatusNotFound {
		t.Errorf("Bob gets Alice's pre-AS-1 session: status %d, want 404", status)
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

func apiCall(t *testing.T, method, url, token, body string) (int, string) {
	t.Helper()
	status, resp, _ := apiRequest(t, method, url, token, body)
	return status, resp
}

// apiRequest returns the status, body, and WWW-Authenticate challenge.
func apiRequest(t *testing.T, method, url, token, body string) (int, string, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw), resp.Header.Get("WWW-Authenticate")
}

// unsignedToken is an otherwise well-formed AS-1 access token with alg none.
func unsignedToken(t *testing.T, issuer, audience string) string {
	t.Helper()
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"iss": issuer, "sub": "u-alice", "aud": audience, "client_id": identityharness.DefaultClientID,
		"sub_id": map[string]string{"format": "iss_sub", "iss": "https://login.example.com", "sub": identityharness.Alice},
		"iat":    now.Unix(), "exp": now.Add(time.Minute).Unix(),
	})
	token.Header["typ"] = "at+jwt"
	signed, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign alg none: %v", err)
	}
	return signed
}

// listSessionOwners maps each listed session to its owner.
func listSessionOwners(t *testing.T, api, token string) map[string]identitybroker.Owner {
	t.Helper()
	status, body := apiCall(t, http.MethodGet, api+"/v1/sessions", token, "")
	if status != http.StatusOK {
		t.Fatalf("list sessions: status %d: %s", status, body)
	}
	var page struct {
		Sessions []struct {
			ID    string               `json:"session_id"`
			Owner identitybroker.Owner `json:"owner"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatalf("list sessions response %s: %v", body, err)
	}
	owners := make(map[string]identitybroker.Owner, len(page.Sessions))
	for _, s := range page.Sessions {
		owners[s.ID] = s.Owner
	}
	return owners
}

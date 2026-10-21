package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// TestIdentityBrokerAgainstOfflineHarness starts mecak8s with the identity
// broker on and AS-1 as its token issuer, all offline. Users sign in through
// the harness's AS-1 and upstream IdP; their AS-1 access tokens authenticate
// API requests and own the sessions they create, and tokens AS-1 would not
// issue are refused. Later identity work extends this test as it adds steps.
func TestIdentityBrokerAgainstOfflineHarness(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	cfg, err := parseFlags([]string{
		"--mock",
		"--enable-identity-broker",
		"--permission-mode=yolo", "--guardrails=off", // unchecked allow-all is yolo
		"--grpc-addr", freeLoopbackPort(t),
		"--http-addr", freeLoopbackPort(t),
		"--drain-addr", freeLoopbackPort(t),
		"--session-lease-k8s-namespace", "",
		"--oidc-issuer", h.AS1.Issuer(),
		"--oidc-audience", h.AS1.Audience(),
		"--oidc-allow-private-https-issuer",
		"--oidc-ca-cert-file", h.WriteCACert(t, t.TempDir()),
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
	waitForHTTPStatus(t, api+"/readyz", http.StatusOK)

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
	if ids := listSessionIDs(t, api, alice.AccessToken); !slices.Contains(ids, created.ID) {
		t.Errorf("Alice's sessions %v do not include %s", ids, created.ID)
	}
	if ids := listSessionIDs(t, api, bob.AccessToken); slices.Contains(ids, created.ID) {
		t.Errorf("Bob's sessions %v include Alice's %s", ids, created.ID)
	}

	// Alice's session has a broker session owned by her AS-1 identity. Releasing
	// the session keeps it; deleting the session closes it.
	findBrokerSession := func() (identitybroker.Session, bool) {
		t.Helper()
		s, ok, err := built.IdentityBroker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: created.ID})
		if err != nil {
			t.Fatalf("FindSession: %v", err)
		}
		return s, ok
	}
	opened, ok := findBrokerSession()
	if !ok {
		t.Fatalf("Alice's session %s has no broker session", created.ID)
	}
	if opened.Owner.Issuer != h.AS1.Issuer() || opened.Owner.Subject == "" {
		t.Errorf("broker session owner = %+v, want Alice at AS-1 %s", opened.Owner, h.AS1.Issuer())
	}
	if status, body := apiCall(t, http.MethodDelete, api+"/v1/sessions/"+created.ID, alice.AccessToken, ""); status >= 300 {
		t.Fatalf("Alice releases her session: status %d: %s", status, body)
	}
	if got, ok := findBrokerSession(); !ok || got != opened {
		t.Errorf("after releasing the session, broker session = %+v, %v; want %+v", got, ok, opened)
	}
	if status, body := apiCall(t, http.MethodPost, api+"/v1/sessions/"+created.ID+"/delete", alice.AccessToken, ""); status >= 300 {
		t.Fatalf("Alice deletes her session: status %d: %s", status, body)
	}
	if _, ok := findBrokerSession(); ok {
		t.Error("deleting Alice's session left its broker session open")
	}

	for name, token := range map[string]string{
		"no token":       "",
		"foreign key":    h.AS1.MintAccessToken(identityharness.AccessTokenSpec{ForeignKey: true}),
		"other audience": h.AS1.MintAccessToken(identityharness.AccessTokenSpec{Audience: "https://other.test/api"}),
		"other issuer":   h.AS1.MintAccessToken(identityharness.AccessTokenSpec{Issuer: h.IdP.Issuer()}),
		"expired":        h.AS1.MintAccessToken(identityharness.AccessTokenSpec{Lifetime: -time.Hour}),
	} {
		if status, _ := apiCall(t, http.MethodGet, api+"/v1/sessions", token, ""); status != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, status)
		}
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
	return resp.StatusCode, string(raw)
}

func listSessionIDs(t *testing.T, api, token string) []string {
	t.Helper()
	status, body := apiCall(t, http.MethodGet, api+"/v1/sessions", token, "")
	if status != http.StatusOK {
		t.Fatalf("list sessions: status %d: %s", status, body)
	}
	var page struct {
		Sessions []struct {
			ID string `json:"session_id"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatalf("list sessions response %s: %v", body, err)
	}
	ids := make([]string, 0, len(page.Sessions))
	for _, s := range page.Sessions {
		ids = append(ids, s.ID)
	}
	return ids
}

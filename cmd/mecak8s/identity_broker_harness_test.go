package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker/mcpbrokertest"
	"github.com/stacklok/mecatl/internal/adapter/mockscript"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server/as1servertest"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// TestIdentityBrokerAgainstOfflineHarness starts mecak8s with the identity
// broker on and AS-1 as its token issuer, all offline. Users sign in through
// the harness's AS-1 and upstream IdP; their AS-1 access tokens authenticate
// API requests and own the sessions they create, and tokens AS-1 would not
// issue are refused. Session open converts each user's access token at AS-1
// into a refresh token the broker holds for the session. Later identity work
// extends this test as it adds steps.
//
// It stays on the harness's AS-1, not the real one, because later steps add
// gateway calls that need ID-JAGs, which ToolHive's AS-1 does not issue yet:
// retired by stacklok/toolhive#6898 (and stacklok/toolhive#6899).
// TestIdentityBrokerAgainstRealAS1 proves the session close and hard-expiry
// reopen against the real AS-1.
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
	// AS-1 and the broker share one clock, which the test moves past the
	// sessions' hard expiry; AS-1 checks the broker's client assertions
	// against it, so the broker's AS-1 client uses it too.
	var clockOffset atomic.Int64
	now := func() time.Time { return time.Now().Add(time.Duration(clockOffset.Load())) }
	const grantMaxLifetime = 5 * time.Minute
	h := identityharness.New(t, identityharness.Options{
		BrokerKeys:       []identityharness.BrokerKey{{KeyID: signer.JWKS().Keys[0].KeyID, Public: &clientKey.PublicKey}},
		Now:              now,
		GrantMaxLifetime: grantMaxLifetime,
	})
	api, built := serveIdentityBroker(t, identityBrokerAS1Tokens{
		issuer: h.AS1.Issuer(), audience: h.AS1.Audience(), caFile: h.WriteCACert(t, t.TempDir()),
		clientIDs: identityharness.DefaultClientID + ",studio",
	}, &app.IdentityBrokerAS1{
		Issuer: h.AS1.Issuer(), TokenEndpoint: h.AS1.TokenEndpoint(), HTTPClient: h.HTTPClient(), ClientKey: clientKey, Now: now, AssertionNow: now,
	}, nil, nil)

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

	// Hard expiry: a broker session holding a refresh token closes when the
	// grant expires, and the user's next request opens a new one for the same
	// agent. The fork and Bob's session each converted a token, so each has a
	// hard expiry.
	expiring, ok := findBrokerSession(forked.ID)
	if !ok || !expiring.HoldsRefreshToken {
		t.Fatalf("fork's broker session = %+v, %v; want one holding a refresh token", expiring, ok)
	}
	bobsExpiring, ok := findBrokerSession(bobCreated.ID)
	if !ok || !bobsExpiring.HoldsRefreshToken {
		t.Fatalf("Bob's broker session = %+v, %v; want one holding a refresh token", bobsExpiring, ok)
	}
	registerMain := func(brokerSession string) (identitybroker.Registration, error) {
		return built.IdentityBroker.RegisterAgent(t.Context(), identitybroker.RegisterAgentRequest{
			SessionID: brokerSession, HostSessionID: forked.ID, Owner: aliceCompany, Incarnation: expiring.Incarnation,
			Definition: identitybroker.DefinitionRef{Source: "system", Name: "main"},
		})
	}
	before, err := registerMain(expiring.ID)
	if err != nil {
		t.Fatalf("register the fork's main agent: %v", err)
	}
	clockOffset.Store(int64(grantMaxLifetime + time.Second))
	closed := identitybroker.ReasonError(identitybroker.ReasonSessionClosed, "")
	if _, err := registerMain(expiring.ID); !errors.Is(err, closed) {
		t.Errorf("register in the expired broker session = %v, want %v", err, closed)
	}
	if _, err := built.IdentityBroker.AuthorizeCall(t.Context(), identitybroker.AuthorizeCallRequest{
		SessionID: expiring.ID, Credential: before.Credential, OperationID: "op-1", RunID: "R42", RunOwner: aliceCompany,
		Method: identitybroker.MethodToolsCall, Server: "gateway", Tool: "search", ArgumentsSHA256: strings.Repeat("ab", 32),
	}); !errors.Is(err, closed) {
		t.Errorf("authorize in the expired broker session = %v, want %v", err, closed)
	}
	// A fresh token is issued now by the API's clock, which the test did not
	// move; it is still live on AS-1's, as the move is shorter than its
	// lifetime.
	fresh := h.AS1.MintAccessToken(identityharness.AccessTokenSpec{User: identityharness.Alice, IssuedAt: time.Now()})
	if status, body := apiCall(t, http.MethodPost, api+"/v1/sessions/"+forked.ID+"/prompt", fresh, `{"text":"after expiry"}`); status != http.StatusOK {
		t.Fatalf("Alice prompts her fork after its broker session expired: status %d: %s", status, body)
	}
	reopened, ok := findBrokerSession(forked.ID)
	if !ok || reopened.ID == expiring.ID || !reopened.HoldsRefreshToken {
		t.Fatalf("fork's broker session after expiry = %+v, %v; want a new one holding a refresh token", reopened, ok)
	}
	// The 200 means the prompt's run entry registered the main agent in the
	// new broker session (TestIdentityBrokerRegistersAgainInAReopenedBrokerSession
	// pins that at the server); registering again here returns its agent.
	if after, err := registerMain(reopened.ID); err != nil || after.Agent != before.Agent {
		t.Errorf("register in the new broker session = %+v, %v; want agent %+v", after.Agent, err, before.Agent)
	}
	// A tokenless open after the expiry pairs a new session that holds no
	// refresh token.
	bobsReopened, err := built.IdentityBroker.OpenSession(t.Context(), identitybroker.OpenSessionRequest{
		HostSessionID: bobCreated.ID, Incarnation: bobsExpiring.Incarnation, Owner: bobCompany,
	})
	if err != nil || bobsReopened.ID == bobsExpiring.ID || bobsReopened.HoldsRefreshToken {
		t.Errorf("Bob's tokenless open after expiry = %+v, %v; want a new broker session holding no refresh token", bobsReopened, err)
	}
}

// gatewayCallbackURL is the MCP broker callback the gateway test configures;
// AS-2's issuer, gatewayAS2Issuer, derives from it, and the vMCP's resource,
// gatewayResource, from that.
const (
	gatewayCallbackURL = "https://broker.example/oauth/callback"
	gatewayAS2Issuer   = "https://broker.example/v1/mcp/broker"
	gatewayResource    = gatewayAS2Issuer + "/mcp"
	gatewayVMCPPath    = "/v1/mcp/broker/mcp"
	// gatewayClientSecretEnv names the protected route's client secret,
	// which ToolHive reads at construction.
	gatewayClientSecretEnv = "MECATL_TEST_CALENDAR_CLIENT_SECRET" // #nosec G101 -- environment variable name, not a credential.
)

// gatewaySettings declares the protected calendar route the gateway routes,
// in broker mode, the way an operator configures mecak8s's MCP servers. The
// upstream is never dialled: the vMCP holds no upstream token for it.
const gatewaySettings = `mcp:
  mode: broker
  broker:
    callback_url: ` + gatewayCallbackURL + `
  servers:
    - name: calendar
      url: https://mcp.example/calendar
      auth:
        mode: oauth
        oauth:
          upstream:
            mode: oauth2
            oauth2:
              authorization_endpoint: https://issuer.example/authorize
              token_endpoint: https://issuer.example/token
          client:
            mode: preregistered
            preregistered: {id: calendar-client, secret_env: ` + gatewayClientSecretEnv + `}
          scopes: [openid]
          network: {}
          tools:
            - name: echo
              description: echo
              input_schema: {type: object}
`

// TestIdentityBrokerGatewayCallAgainstOfflineHarness runs a per-call gateway
// token through mecak8s's real startup and served HTTP mux, offline: Alice
// signs in at the harness's AS-1 and creates a session over the API, whose
// run registers the session's main agent; the identity broker authorizes a
// tool call for that agent, getting an ID-JAG at AS-1 and a gateway token at
// the real AS-2; the real embedded vMCP accepts the token on the served
// endpoint; a second call gets another token; and once Alice deletes the
// session, calls are refused with its closing reason.
//
// Stand-ins and what retires them, here and in
// TestIdentityBrokerSessionToolCallThroughGateway:
//
//	stand-in                                       retired by
//	harness AS-1 issuing per-call ID-JAGs, and     #2654, after stacklok/toolhive#6898
//	  validating actor tokens through                (PR stacklok/toolhive#7130) and
//	  IdentityBrokerAS1.UseCredentialVerifier        stacklok/toolhive#6899
//	test-only AS-2 trust and gateway seam          #2533 and #2538
//	  (IdentityBrokerAS2Trust,
//	  IdentityBrokerGatewayServers)
//	the embedded vMCP as the gateway, reached      #2812
//	  in process
//	no gateway stand-in: the vMCP is real, but     #2537
//	  it finds no provider credential for the
//	  caller, so initialize succeeds without
//	  the backend and a call does not reach it
//	in-memory send-once record                     #2730
//
// It is not parallel: the protected route's client secret is read from an
// environment variable.
func TestIdentityBrokerGatewayCallAgainstOfflineHarness(t *testing.T) {
	h, api, built := serveGatewayIdentityBroker(t, "", nil, nil)

	alice, err := h.SignIn(t.Context(), identityharness.SignInRequest{User: identityharness.Alice})
	if err != nil {
		t.Fatalf("Alice signs in: %v", err)
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
	// The run's entry registers the session's main agent.
	if status, body := apiCall(t, http.MethodPost, api+"/v1/sessions/"+created.ID+"/prompt", alice.AccessToken, `{"text":"hello"}`); status != http.StatusOK {
		t.Fatalf("Alice prompts her session: status %d: %s", status, body)
	}
	opened, ok, err := built.IdentityBroker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: created.ID})
	if err != nil || !ok || !opened.HoldsRefreshToken {
		t.Fatalf("Alice's broker session = %+v, %v, %v; want one holding a refresh token", opened, ok, err)
	}
	register := func() identitybroker.Registration {
		t.Helper()
		reg, err := built.IdentityBroker.RegisterAgent(t.Context(), identitybroker.RegisterAgentRequest{
			SessionID: opened.ID, HostSessionID: created.ID, Owner: opened.Owner, Incarnation: opened.Incarnation,
			Definition: identitybroker.DefinitionRef{Source: "system", Name: "main"},
		})
		if err != nil {
			t.Fatalf("register the session's main agent: %v", err)
		}
		return reg
	}
	// Registering again returns the agent the run registered, with a credential.
	agent := register()
	if again := register(); again.Agent != agent.Agent {
		t.Fatalf("registering the main agent again = %+v, want the run's agent %+v", again.Agent, agent.Agent)
	}

	call := func(operation string) (identitybroker.Authorization, error) {
		return built.IdentityBroker.AuthorizeCall(t.Context(), identitybroker.AuthorizeCallRequest{
			SessionID: opened.ID, Credential: agent.Credential, OperationID: operation, RunID: "R1", RunOwner: opened.Owner,
			Method: identitybroker.MethodToolsCall, Server: "calendar", Tool: "echo", ArgumentsSHA256: strings.Repeat("ab", 32),
		})
	}
	first, err := call("op-1")
	if err != nil {
		t.Fatalf("authorize the first call: %v", err)
	}
	issued := h.AS1.IDJAGsIssued()
	if len(issued) != 1 || !strings.HasPrefix(issued[0].AgentID, "ag_") || issued[0].Audience != gatewayAS2Issuer ||
		issued[0].Resource != gatewayResource || issued[0].Operation != "op-1" {
		t.Fatalf("AS-1 issued ID-JAGs %+v, want one for op-1 for an ag_ agent addressed to AS-2 for the vMCP", issued)
	}
	// authorized checks a call's token expires no later than its ID-JAG and
	// names an attempt.
	authorized := func(name string, got identitybroker.Authorization, idjag identityharness.IDJAGIssuance) {
		t.Helper()
		if got.ExpiresAt.IsZero() || got.ExpiresAt.After(idjag.ExpiresAt) || got.AttemptID == "" {
			t.Fatalf("the %s call expires at %v with attempt %q; want an expiry no later than its ID-JAG's %v, and an attempt",
				name, got.ExpiresAt, got.AttemptID, idjag.ExpiresAt)
		}
	}
	authorized("first", first, issued[0])
	// The served endpoint authenticates: without a token it refuses.
	if status := vmcpInitialize(t, api, ""); status != http.StatusUnauthorized {
		t.Fatalf("served vMCP initialize without a token: status %d, want 401", status)
	}
	if status := vmcpInitialize(t, api, first.GatewayToken.Value()); status != http.StatusOK {
		t.Fatalf("served vMCP initialize with the first gateway token: status %d, want 200", status)
	}
	second, err := call("op-2")
	if err != nil {
		t.Fatalf("authorize the second call: %v", err)
	}
	if second.GatewayToken.Value() == first.GatewayToken.Value() {
		t.Fatal("the second call got the first call's gateway token")
	}
	issued = h.AS1.IDJAGsIssued()
	if len(issued) != 2 || issued[1].Operation != "op-2" {
		t.Fatalf("AS-1 issued ID-JAGs %+v, want one per call, the second for op-2", issued)
	}
	authorized("second", second, issued[1])
	if second.AttemptID == first.AttemptID {
		t.Fatal("the second call got the first call's attempt")
	}
	if status := vmcpInitialize(t, api, second.GatewayToken.Value()); status != http.StatusOK {
		t.Fatalf("served vMCP initialize with the second gateway token: status %d, want 200", status)
	}

	if status, body := apiCall(t, http.MethodPost, api+"/v1/sessions/"+created.ID+"/delete", alice.AccessToken, ""); status >= 300 {
		t.Fatalf("Alice deletes her session: status %d: %s", status, body)
	}
	closed := identitybroker.ReasonError(identitybroker.ReasonSessionClosed, "")
	if _, err := call("op-3"); !errors.Is(err, closed) {
		t.Fatalf("authorize after the session closed = %v, want %v", err, closed)
	}
	if n := len(h.AS1.IDJAGsIssued()); n != 2 {
		t.Fatalf("AS-1 issued %d ID-JAGs, want none after the session closed", n)
	}
}

// serveGatewayIdentityBroker serves mecak8s, offline, with the identity
// broker on at the harness's AS-1, the calendar route of gatewaySettings plus
// extraSettings, and a gateway entry routing calendar, whose AS-2 trusts the
// harness's AS-1 for Alice. extraArgs are further flags, and edit, when set,
// changes the application configuration after that.
func serveGatewayIdentityBroker(t *testing.T, extraSettings string, extraArgs []string, edit func(*app.Config)) (*identityharness.Harness, string, *app.Built) {
	t.Helper()
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
	h.AS1.AddIDJAGTarget(gatewayAS2Issuer, gatewayResource)
	t.Setenv(gatewayClientSecretEnv, "construction-only-secret")
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(settings, []byte(gatewaySettings+extraSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	caFile := h.WriteCACert(t, t.TempDir())
	api, built := serveIdentityBroker(t, identityBrokerAS1Tokens{
		issuer: h.AS1.Issuer(), audience: h.AS1.Audience(), caFile: caFile, clientIDs: identityharness.DefaultClientID,
	}, &app.IdentityBrokerAS1{
		Issuer: h.AS1.Issuer(), TokenEndpoint: h.AS1.TokenEndpoint(), HTTPClient: h.HTTPClient(), ClientKey: clientKey,
		UseCredentialVerifier: func(v identitybroker.CredentialVerifier) {
			h.AS1.SetActorValidator(as1servertest.HarnessActorValidator(v))
		},
	}, append([]string{"--permission-config", settings}, extraArgs...), func(cfg *app.Config) {
		cfg.IdentityBrokerGatewayServers = []string{"calendar"}
		cfg.IdentityBrokerAS2Trust = mcpbrokertest.TrustAS1(mcpbrokertest.AS1Trust{
			Issuer: h.AS1.Issuer(), JWKSURL: h.AS1.JWKSURL(), CAFile: caFile,
			Subjects: []string{h.AS1.Subject(identityharness.Alice)},
		})
		if edit != nil {
			edit(cfg)
		}
	})
	return h, api, built
}

// gatewayToolScript is a mock model whose first and third turns call the
// gateway route's echo tool, each with its own call ID.
const gatewayToolScript = `{"turns":[
  {"tool_calls":[{"id":"call-open","name":"mcp__calendar__echo","args":{"a":1}}]},
  {"text":"done"},
  {"tool_calls":[{"id":"call-closed","name":"mcp__calendar__echo","args":{"a":1}}]},
  {"text":"done"}
]}`

// TestIdentityBrokerSessionToolCallThroughGateway runs a real session's tool
// call through mecak8s's startup to the gateway, offline: the model calls the
// gateway route's tool, the user approves it, and the MCP broker has the
// identity broker authorize the call for the session's main agent, getting an
// ID-JAG at AS-1 for the call's operation and a gateway token at AS-2, then
// sends it to the embedded vMCP, which accepts the token. When the broker
// session closes while a later call waits for approval, the approved call is
// refused with the closing reason, without an ID-JAG and without reopening
// the broker session. Approval resolves the live run without passing run
// entry, which would open a new broker session. Its stand-ins are listed on
// TestIdentityBrokerGatewayCallAgainstOfflineHarness.
func TestIdentityBrokerSessionToolCallThroughGateway(t *testing.T) {
	script := filepath.Join(t.TempDir(), "script.json")
	if err := os.WriteFile(script, []byte(gatewayToolScript), 0o600); err != nil {
		t.Fatal(err)
	}
	// The vMCP ends a session in which no backend answers, and the calendar
	// backend never does without a provider credential, so an anonymous
	// backend, offline, keeps the call's vMCP session open.
	notes := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "notes", Version: "v1"}, nil)
	mcpsdk.AddTool(notes, &mcpsdk.Tool{Name: "ping"}, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{}, nil, nil
	})
	notesServer := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return notes }, nil))
	t.Cleanup(notesServer.Close)
	diag := &gatewayCallDiagnostics{}
	// Every call of the echo tool asks, so the test acts between the model's
	// call and its dispatch.
	extraSettings := "    - name: notes\n      url: " + notesServer.URL + "\n      auth: {mode: none}\n" +
		"permissions:\n  ask: [mcp__calendar__echo]\n"
	h, api, built := serveGatewayIdentityBroker(t, extraSettings,
		[]string{"--mock-script", script}, func(cfg *app.Config) { cfg.Diagnostics = diag })

	alice, err := h.SignIn(t.Context(), identityharness.SignInRequest{User: identityharness.Alice})
	if err != nil {
		t.Fatalf("Alice signs in: %v", err)
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
	owner := session.WithPrincipal(t.Context(), &session.Principal{Issuer: h.IdP.Issuer(), Subject: identityharness.Alice, GrantType: session.GrantTypeUser})
	toolResult := func(call session.ToolCallID) session.ToolResult {
		t.Helper()
		sess, err := built.Service.GetSession(owner, session.SessionID(created.ID))
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		for _, m := range sess.Conversation.Messages {
			if m.ToolResult != nil && m.ToolResult.CallID == call {
				return *m.ToolResult
			}
		}
		t.Fatalf("the transcript has no result for %s", call)
		return session.ToolResult{}
	}
	// operations is each gateway call's operation, in call order, as its
	// gateway_call diagnostic names it.
	operations := map[session.ToolCallID]string{}
	nextOperation := func(call session.ToolCallID) string {
		t.Helper()
		ops := diag.operations(created.ID)
		if len(ops) != len(operations)+1 {
			t.Fatalf("gateway call diagnostics name operations %v, want one more than %v", ops, operations)
		}
		operations[call] = ops[len(ops)-1]
		return operations[call]
	}
	operation := func(call session.ToolCallID) string { return operations[call] }

	// The run's entry registers the main agent; the approved call goes
	// through the gateway.
	if asks := promptApprovingAsks(t, api, alice.AccessToken, created.ID, "call the calendar", nil); asks != 1 {
		t.Fatalf("the run asked %d times, want once for the tool call", asks)
	}
	nextOperation("call-open")
	opened, ok, err := built.IdentityBroker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: created.ID})
	if err != nil || !ok || !opened.HoldsRefreshToken {
		t.Fatalf("Alice's broker session = %+v, %v, %v; want one holding a refresh token", opened, ok, err)
	}
	issued := h.AS1.IDJAGsIssued()
	if len(issued) != 1 || issued[0].Operation != operation("call-open") || !strings.HasPrefix(issued[0].AgentID, "ag_") ||
		issued[0].Audience != gatewayAS2Issuer || issued[0].Resource != gatewayResource {
		t.Fatalf("AS-1 issued ID-JAGs %+v, want one for the call's operation %s for an ag_ agent addressed to AS-2 for the vMCP", issued, operation("call-open"))
	}
	// The broker allowed the call: only an allowed decision issues an
	// attempt, and the call completed at the vMCP with it.
	if got := diag.call(operation("call-open")); got["code"] != "completed" || got["outcome"] != "completed" || got["attempts"] == "" || strings.Contains(got["attempts"], ",") {
		t.Fatalf("gateway call diagnostics %v; want one attempt the vMCP completed", got)
	}
	// The vMCP accepted the token. Without a provider credential it does not
	// reach the backend, so its answer is whatever it returns then; only a
	// refusal or a rejected token would be the gateway path failing.
	if res := toolResult("call-open"); strings.HasPrefix(res.Content, "MCP gateway call") || strings.Contains(res.Content, "401") {
		t.Fatalf("the call's result is a refusal: %q", res.Content)
	}

	// The broker session closes while the next call waits for approval.
	closeBroker := func() {
		if err := built.IdentityBroker.CloseSession(t.Context(), identitybroker.CloseSessionRequest{SessionID: opened.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
			t.Errorf("close the broker session: %v", err)
		}
	}
	if asks := promptApprovingAsks(t, api, alice.AccessToken, created.ID, "call it again", closeBroker); asks != 1 {
		t.Fatalf("the second run asked %d times, want once for the tool call", asks)
	}
	if nextOperation("call-closed") == operation("call-open") {
		t.Fatal("both calls name the same operation")
	}
	const sessionClosed = "MCP gateway call refused: the session's authority is closed and is not reopened for this call (code session_closed)."
	if res := toolResult("call-closed"); !res.IsError || res.Content != sessionClosed {
		t.Fatalf("the call after the close = %+v, want the session_closed refusal", res)
	}
	if got := diag.call(operation("call-closed")); got["code"] != "session_closed" || got["attempts"] != "" {
		t.Fatalf("gateway call diagnostics %v; want session_closed with no attempt", got)
	}
	if n := len(h.AS1.IDJAGsIssued()); n != 1 {
		t.Fatalf("AS-1 issued %d ID-JAGs, want none after the session closed", n)
	}
	if reopened, ok, err := built.IdentityBroker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: created.ID}); err != nil || ok {
		t.Fatalf("after the refused call, broker session = %+v, %v, %v; want none: the call must not reopen it", reopened, ok, err)
	}

	// No token reaches the transcript, the events, or the gateway call
	// diagnostics. The gateway token AS-2 issued for the call is a JWT that
	// never leaves the process, so the scan refuses every JWT there.
	sess, err := built.Service.GetSession(owner, session.SessionID(created.ID))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%+v %+v\n", sess, sess.Conversation.Messages)
	if raw, err := json.Marshal(sess); err == nil {
		out.Write(raw)
	}
	stream, err := built.Service.StreamSessionEvents(owner, session.SessionID(created.ID))
	if err != nil {
		t.Fatalf("StreamSessionEvents: %v", err)
	}
	events := 0
	for ev, err := range stream {
		events++
		fmt.Fprintf(&out, "%+v %v\n", ev, err)
	}
	if events == 0 {
		t.Fatal("the session has no events")
	}
	fmt.Fprintf(&out, "%v\n", diag.operations(created.ID))
	for _, call := range []session.ToolCallID{"call-open", "call-closed"} {
		fmt.Fprintf(&out, "%v\n", diag.call(operation(call)))
	}
	jwtShaped := regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
	// Positive control: a JWT planted in the scanned text is found.
	if !jwtShaped.MatchString(out.String() + alice.AccessToken) {
		t.Fatal("the scan missed a planted JWT")
	}
	if jwtShaped.MatchString(out.String()) || strings.Contains(out.String(), alice.AccessToken) {
		t.Fatal("the transcript, the events, or the gateway call diagnostics carry a token")
	}
}

// promptApprovingAsks prompts the session over the streaming prompt endpoint
// with token and allows each permission ask once, after beforeApprove when
// set, until the run ends. It returns the number of asks.
func promptApprovingAsks(t *testing.T, api, token, id, text string, beforeApprove func()) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":`+strconv.Quote(text)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("prompt: status %d: %s", resp.StatusCode, raw)
	}
	asks := 0
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type  string `json:"type"`
			RunID string `json:"run_id"`
			Ask   struct {
				AskID string `json:"ask_id"`
			} `json:"ask"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil || ev.Type != string(session.EvPermissionAsk) {
			continue
		}
		asks++
		if beforeApprove != nil {
			beforeApprove()
		}
		resolve, err := json.Marshal(map[string]string{"expected_run_id": ev.RunID, "ask_id": ev.Ask.AskID, "verdict": session.VerdictStringAllowOnce})
		if err != nil {
			t.Fatal(err)
		}
		if status, body := apiCall(t, http.MethodPost, api+"/v1/sessions/"+id+"/controls/resolve-ask", token, string(resolve)); status != http.StatusOK {
			t.Fatalf("resolve the ask: status %d: %s", status, body)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read the prompt's events: %v", err)
	}
	return asks
}

// gatewayCallDiagnostics keeps the MCP broker's gateway call diagnostics.
type gatewayCallDiagnostics struct {
	mu    sync.Mutex
	calls []map[string]string
}

func (d *gatewayCallDiagnostics) Log(_ context.Context, _ port.Level, msg string, args ...any) {
	if msg != "mcp broker gateway call" {
		return
	}
	fields := make(map[string]string, len(args)/2)
	for i := 0; i+1 < len(args); i += 2 {
		fields[fmt.Sprint(args[i])] = fmt.Sprint(args[i+1])
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, fields)
}

func (d *gatewayCallDiagnostics) With(...any) port.Diagnostics { return d }

// call returns the fields of the one gateway call logged for operation, or
// nil unless exactly one was.
func (d *gatewayCallDiagnostics) call(operation string) map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var found map[string]string
	for _, c := range d.calls {
		if c["operation"] == operation {
			if found != nil {
				return nil
			}
			found = c
		}
	}
	return found
}

// operations is the operation of each gateway call logged for sessionID, in
// the order they were logged.
func (d *gatewayCallDiagnostics) operations(sessionID string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var ops []string
	for _, c := range d.calls {
		if c["session"] == sessionID {
			ops = append(ops, c["operation"])
		}
	}
	return ops
}

// vmcpInitialize is the status of an MCP initialize at the vMCP mecak8s
// serves under api, with token, when set, as its Bearer token; a 200 must
// carry a JSON-RPC result. A session it opens is ended, so the vMCP lets its
// backend go.
func vmcpInitialize(t *testing.T, api, token string) int {
	t.Helper()
	send := func(method, body, sessionID string) (*http.Response, []byte) {
		req, err := http.NewRequestWithContext(t.Context(), method, api+gatewayVMCPPath, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("vMCP %s: %v", method, err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("vMCP %s response: %v", method, err)
		}
		return resp, raw
	}
	const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mecak8s-test","version":"1.0"}}}`
	resp, raw := send(http.MethodPost, initialize, "")
	if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
		send(http.MethodDelete, "", id)
	}
	if resp.StatusCode == http.StatusOK && !identityharness.HasJSONRPCResult(raw) {
		t.Fatal("served vMCP initialize answered 200 without a JSON-RPC result")
	}
	return resp.StatusCode
}

// identityBrokerAS1Tokens is the AS-1 whose access tokens mecak8s accepts:
// its issuer and audience, a CA file trusted for its TLS certificate, and the
// accepted client IDs, comma-separated.
type identityBrokerAS1Tokens struct {
	issuer, audience, caFile, clientIDs string
}

// serveIdentityBroker builds and serves mecak8s with the identity broker on,
// accepting access tokens with sub_id owners from the AS-1 that tokens
// describes, and its session open converting at as1. No flag sets AS-1's
// endpoints yet (#2490), so the test points the broker at as1 directly.
// extraArgs are further flags, and edit, when set, changes the application
// configuration before the build. It returns the HTTP API's base URL and the
// built application; both stop when t ends.
func serveIdentityBroker(t *testing.T, tokens identityBrokerAS1Tokens, as1 *app.IdentityBrokerAS1, extraArgs []string, edit func(*app.Config)) (string, *app.Built) {
	t.Helper()
	cfg, err := parseFlags(append([]string{
		"--mock",
		"--enable-identity-broker", "--no-shell", // the identity broker refuses to run beside a shell
		"--permission-mode=yolo", "--guardrails=off", // unchecked allow-all is yolo
		"--grpc-addr", freeLoopbackPort(t),
		"--http-addr", freeLoopbackPort(t),
		"--drain-addr", freeLoopbackPort(t),
		"--session-lease-k8s-namespace", "",
		"--oidc-issuer", tokens.issuer,
		"--oidc-audience", tokens.audience,
		"--oidc-allow-private-https-issuer",
		"--oidc-ca-cert-file", tokens.caFile,
		"--oidc-owner-identity=sub_id",
		"--oidc-accepted-client-ids", tokens.clientIDs,
	}, extraArgs...))
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.mockScript != "" {
		// As run loads it.
		if cfg.mockProvider, err = mockscript.Load(cfg.mockScript); err != nil {
			t.Fatal(err)
		}
	}
	cfg.diagnostics = port.NopDiagnostics{}
	appCfg := appConfig(cfg, port.NopDiagnostics{}, observability{})
	appCfg.IdentityBrokerAS1 = as1
	if edit != nil {
		edit(&appCfg)
	}
	built, err := buildIsolated(t, context.Background(), appCfg)
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	t.Cleanup(built.Close)
	if built.IdentityBroker == nil {
		t.Fatal("--enable-identity-broker built no identity broker")
	}

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- serveWithDrainWait(ctx, cfg, built.Service, observability{}, func(time.Duration) {}, builtServing{brokerHandlers: built.MCPBrokerHandlers, brokerCallbackPath: built.MCPBrokerCallbackPath})
	}()
	// Runs before built.Close: serving stops first.
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-serveErr:
			if err != nil {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("serve did not stop after cancellation")
		}
	})
	api := "http://" + cfg.httpAddr
	waitForHTTPStatus(t, http.DefaultClient, api+"/readyz", http.StatusOK)
	return api, built
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

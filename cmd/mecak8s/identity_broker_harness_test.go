package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
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
	"github.com/stacklok/mecatl/internal/buildinfo"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server/as1servertest"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/brokerconfig/brokerconfigtest"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/definitionloader"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// TestIdentityBrokerOwnsSessionsAgainstRealAS1 starts mecak8s with the
// identity broker on and the real AS-1, built by the test with the harness
// IdP as its upstream, as its token issuer, all offline. Users sign in at
// AS-1, through two accepted clients; their access tokens authenticate API
// requests and own the sessions they create, and tokens AS-1 would not
// issue are refused. Session open converts each user's access token at AS-1
// into a refresh token the broker holds for the session.
// TestIdentityBrokerAgainstRealAS1 proves the hard-expiry close and reopen,
// and TestIdentityBrokerGatewayCallAgainstRealAS1 the gateway calls.
func TestIdentityBrokerOwnsSessionsAgainstRealAS1(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	studio := as1server.PublicClient{ID: "studio", RedirectURIs: []string{"http://127.0.0.1:18474/oauth/callback"}}
	a := as1servertest.Start(t, h, func(c *as1server.Config, _ *as1server.DelegateClient) {
		c.PublicClients = append(c.PublicClients, studio)
	})
	api, built := serveIdentityBroker(t, identityBrokerAS1Tokens{
		issuer: a.Issuer(), audience: as1servertest.Audience, caFile: h.WriteCACert(t, t.TempDir()),
		clientIDs: as1servertest.PublicClientID + "," + studio.ID,
	}, &app.IdentityBrokerAS1{
		Issuer: a.Issuer(), TokenEndpoint: a.Server.TokenEndpoint(), HTTPClient: a.HTTPClient(),
	}, identityBrokerServeOptions{clientKey: a.Key})

	alice := a.SignIn(t, as1servertest.SignIn{User: identityharness.Alice}).Value()
	bob := a.SignIn(t, as1servertest.SignIn{User: identityharness.Bob}).Value()

	status, body := apiCall(t, http.MethodPost, api+"/v1/sessions", alice, "{}")
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
	if owners := listSessionOwners(t, api, alice); owners[created.ID] != aliceCompany {
		t.Errorf("Alice's sessions %v: %s owner = %+v, want %+v", owners, created.ID, owners[created.ID], aliceCompany)
	}
	// The second accepted client carries the same identity.
	throughStudio := a.SignIn(t, as1servertest.SignIn{User: identityharness.Alice, Client: studio}).Value()
	if owners := listSessionOwners(t, api, throughStudio); owners[created.ID] != aliceCompany {
		t.Errorf("Alice through studio: sessions %v, want %s owned by %+v", owners, created.ID, aliceCompany)
	}
	if _, listed := listSessionOwners(t, api, bob)[created.ID]; listed {
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
	// a refresh token for it, from AS-1's one conversion.
	if !opened.HoldsRefreshToken || a.Faults.Conversions() != 1 {
		t.Fatalf("Alice's broker session %+v after %d conversions; want it holding the refresh token of AS-1's one conversion", opened, a.Faults.Conversions())
	}

	status, body = apiCall(t, http.MethodPost, api+"/v1/sessions", bob, "{}")
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
	conversions := a.Faults.Conversions()

	// The run passes run entry, which finds the open broker session and keeps it.
	const aliceTitle, alicePrompt = "alice-private-title", "alice-private-prompt"
	if status, body := apiCall(t, http.MethodPost, api+"/v1/sessions/"+created.ID+"/rename", alice, `{"title":"`+aliceTitle+`"}`); status != http.StatusOK {
		t.Fatalf("Alice renames her session: status %d: %s", status, body)
	}
	if status, body := apiCall(t, http.MethodPost, api+"/v1/sessions/"+created.ID+"/prompt", alice, `{"text":"`+alicePrompt+`"}`); status != http.StatusOK {
		t.Fatalf("Alice prompts her session: status %d: %s", status, body)
	}
	if got, ok := findBrokerSession(created.ID); !ok || got != opened {
		t.Errorf("after a run, broker session = %+v, %v; want %+v", got, ok, opened)
	}
	// A session that holds a refresh token does not spend later requests' tokens.
	if n := a.Faults.Conversions(); n != conversions {
		t.Errorf("the run converted a token: %d conversions, want %d", n, conversions)
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
		status, resp := apiCall(t, c.method, api+"/v1/sessions/"+created.ID+c.path, bob, body)
		if status != c.want || (c.want == http.StatusNotFound && !strings.Contains(resp, "session_not_found")) {
			t.Errorf("Bob %s %s on Alice's session: status %d: %s; want %d", c.method, c.path, status, resp, c.want)
		}
		if strings.Contains(resp, aliceTitle) || strings.Contains(resp, alicePrompt) {
			t.Errorf("Bob %s %s on Alice's session disclosed her title or prompt: %s", c.method, c.path, resp)
		}
	}
	if status, resp := apiCall(t, http.MethodGet, api+"/v1/sessions/"+created.ID, alice, ""); status != http.StatusOK || !strings.Contains(resp, aliceTitle) {
		t.Errorf("after Bob's delete, Alice gets her session: status %d: %s; want it intact", status, resp)
	}
	if status, resp := apiCall(t, http.MethodGet, api+"/v1/sessions/"+created.ID+"/transcript", alice, ""); status != http.StatusOK || !strings.Contains(resp, alicePrompt) {
		t.Errorf("Alice reads her transcript: status %d: %s; want her prompt", status, resp)
	}

	// Alice's sign-in token was converted for her first session, and a token
	// converts once: a fork with it is refused, retryably, before it creates
	// anything.
	sessionsBefore := len(listSessionOwners(t, api, alice))
	status, body, challenge := apiRequest(t, http.MethodPost, api+"/v1/sessions/"+created.ID+"/fork", alice, "{}")
	if status != http.StatusUnauthorized || !strings.Contains(body, `"bearer_rejected"`) || challenge != `Bearer error="invalid_token"` {
		t.Fatalf("Alice forks with her spent token: status %d, challenge %q: %s; want 401 bearer_rejected with an invalid_token challenge", status, challenge, body)
	}
	if n := len(listSessionOwners(t, api, alice)); n != sessionsBefore {
		t.Fatalf("the refused fork created a session: %d sessions, want %d", n, sessionsBefore)
	}
	// Her client gets a new token and retries.
	refreshed := a.SignIn(t, as1servertest.SignIn{User: identityharness.Alice}).Value()
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

	if status, body := apiCall(t, http.MethodDelete, api+"/v1/sessions/"+created.ID, alice, ""); status >= 300 {
		t.Fatalf("Alice releases her session: status %d: %s", status, body)
	}
	if got, ok := findBrokerSession(created.ID); !ok || got != opened {
		t.Errorf("after releasing the session, broker session = %+v, %v; want %+v", got, ok, opened)
	}
	if status, body := apiCall(t, http.MethodPost, api+"/v1/sessions/"+created.ID+"/delete", alice, ""); status >= 300 {
		t.Fatalf("Alice deletes her session: status %d: %s", status, body)
	}
	if _, ok := findBrokerSession(created.ID); ok {
		t.Error("deleting Alice's session left its broker session open")
	}

	// Tokens AS-1 would not issue are refused. Each forgery changes one thing
	// in an access token AS-1 issued and, unless it changes the key, is
	// signed again with AS-1's key, so it is refused for that change alone:
	// the unchanged re-signed token is accepted.
	issued := identitybroker.NewToken(refreshed)
	forge := func(f as1servertest.Forgery) string { return a.Forge(t, issued, f).Value() }
	claim := func(name string, value any) as1servertest.Forgery {
		return as1servertest.Forgery{Claims: func(c jwt.MapClaims) {
			if value == nil {
				delete(c, name)
				return
			}
			c[name] = value
		}}
	}
	typ := func(value string) as1servertest.Forgery {
		return as1servertest.Forgery{Header: func(h map[string]any) {
			if value == "" {
				delete(h, "typ")
				return
			}
			h["typ"] = value
		}}
	}
	if status, body := apiCall(t, http.MethodGet, api+"/v1/sessions", forge(as1servertest.Forgery{}), ""); status != http.StatusOK {
		t.Fatalf("an AS-1 access token signed again unchanged: status %d: %s; want 200", status, body)
	}
	for name, token := range map[string]string{
		"no token":       "",
		"foreign key":    forge(as1servertest.Forgery{ForeignKey: true}),
		"other audience": forge(claim("aud", "https://other.test/api")),
		"other issuer":   forge(claim("iss", h.IdP.Issuer())),
		"expired":        a.ExpiredAccessToken(t, issued).Value(),
		"typ JWT":        forge(typ("JWT")),
		"no typ":         forge(typ("")),
		"alg none":       unsignedToken(t, issued.Value()), // toolhive-core's fixed algorithm allowlist rejects this
		"other client":   forge(claim("client_id", "rogue")),
		"no sub_id":      forge(claim("sub_id", nil)),
		"sub_id format":  forge(claim("sub_id", map[string]string{"format": "email", "iss": h.IdP.Issuer(), "sub": identityharness.Alice})),
		"sub_id string":  forge(claim("sub_id", identityharness.Alice)),
		"sub_id no sub":  forge(claim("sub_id", map[string]string{"format": "iss_sub", "iss": h.IdP.Issuer()})),
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
	if status, body := apiCall(t, http.MethodGet, api+"/v1/sessions/"+string(legacy.ID), alice, ""); status != http.StatusOK {
		t.Errorf("Alice through AS-1 gets her pre-AS-1 session: status %d: %s", status, body)
	}
	if status, _ := apiCall(t, http.MethodGet, api+"/v1/sessions/"+string(legacy.ID), bob, ""); status != http.StatusNotFound {
		t.Errorf("Bob gets Alice's pre-AS-1 session: status %d, want 404", status)
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
	// gatewaySigningKeyPlaceholder stands in gatewaySettings for the path of
	// the embedded gateway's signing key, which each test writes.
	gatewaySigningKeyPlaceholder = "/GATEWAY-SIGNING-KEY.pem"
)

// gatewaySettings declares the protected calendar route the gateway routes,
// in broker mode, the way an operator configures mecak8s's MCP servers, with
// the embedded gateway's signing key at gatewaySigningKeyPlaceholder. The
// upstream is never dialled: the vMCP holds no upstream token for it.
const gatewaySettings = `mcp:
  mode: broker
  broker:
    callback_url: ` + gatewayCallbackURL + `
    signing_key_file: ` + gatewaySigningKeyPlaceholder + `
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

// TestIdentityBrokerGatewayCallAgainstRealAS1 runs a per-call gateway token
// through mecak8s's real startup and served HTTP mux, offline: Alice signs in
// at the real AS-1 and creates a session over the API, whose run registers
// the session's main agent; the identity broker authorizes a tool call for
// that agent, getting an ID-JAG at AS-1, which checks the agent's credential
// with the broker's verifier, and a gateway token at the real AS-2; the real
// embedded vMCP accepts the token on the served endpoint; a second call gets
// another token; and once Alice deletes the session, calls are refused with
// its closing reason.
//
// Stand-ins and what retires them, here and in
// TestIdentityBrokerSessionToolCallThroughGateway:
//
//	stand-in                                       retired by
//	test-only AS-2 trust (IdentityBrokerAS2Trust),  #2533
//	  which a gateway entry routing servers
//	  requires; AS-2's test trust names Alice's
//	  AS-1 subject, which exists only after her
//	  sign-in, so these tests run the real AS-1
//	  outside mecak8s rather than the one
//	  mecak8s hosts
//	the embedded vMCP as the gateway, reached      #2812
//	  in process
//	no gateway stand-in: the vMCP is real, but     #2537
//	  it finds no provider credential for the
//	  caller, so initialize succeeds without
//	  the backend and a call does not reach it:
//	  the vMCP accepts the call-bound token and
//	  refuses the call as tool_not_found
//	  (wantStandInRefusal)
//	in-memory send-once record                     #2730
//
// It is not parallel: the protected route's client secret is read from an
// environment variable.
func TestIdentityBrokerGatewayCallAgainstRealAS1(t *testing.T) {
	gw := serveGatewayIdentityBroker(t, "", nil, nil)
	api, built, alice := gw.api, gw.built, gw.alice

	status, body := apiCall(t, http.MethodPost, api+"/v1/sessions", alice, "{}")
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
	if status, body := apiCall(t, http.MethodPost, api+"/v1/sessions/"+created.ID+"/prompt", alice, `{"text":"hello"}`); status != http.StatusOK {
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
			Definition: builtinMainDefinition(),
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
			Method: identitybroker.MethodToolsCall, Server: "calendar", Tool: "calendar.echo", ArgumentsSHA256: strings.Repeat("ab", 32),
		})
	}
	first, err := call("op-1")
	if err != nil {
		t.Fatalf("authorize the first call: %v", err)
	}
	issued := gw.idjags(t)
	if ops := gw.idjagOperations(); len(issued) != 1 || !slices.Equal(ops, []string{"op-1"}) {
		t.Fatalf("AS-1 issued %d ID-JAGs for operations %v, want one for op-1", len(issued), ops)
	}
	// authorized checks a call's token names its agent as the ID-JAG does,
	// expires no later than its ID-JAG, and names an attempt.
	authorized := func(name string, got identitybroker.Authorization, idjag jwt.MapClaims) {
		t.Helper()
		exp, err := idjag.GetExpirationTime()
		if err != nil || exp == nil || got.ExpiresAt.IsZero() || got.ExpiresAt.After(exp.Time) || got.AttemptID == "" {
			t.Fatalf("the %s call expires at %v with attempt %q; want an expiry no later than its ID-JAG's %v, and an attempt",
				name, got.ExpiresAt, got.AttemptID, exp)
		}
		if act := gatewayTokenClaims(t, got)["act"]; !reflect.DeepEqual(act, idjag["act"]) {
			t.Fatalf("the %s call's gateway token act = %v, want its ID-JAG's %v", name, act, idjag["act"])
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
	issued = gw.idjags(t)
	if ops := gw.idjagOperations(); len(issued) != 2 || issued[0]["jti"] == issued[1]["jti"] || !slices.Equal(ops, []string{"op-1", "op-2"}) {
		t.Fatalf("AS-1 issued %d ID-JAGs for operations %v, want a new one per call, the second for op-2", len(issued), ops)
	}
	authorized("second", second, issued[1])
	if second.AttemptID == first.AttemptID {
		t.Fatal("the second call got the first call's attempt")
	}
	if status := vmcpInitialize(t, api, second.GatewayToken.Value()); status != http.StatusOK {
		t.Fatalf("served vMCP initialize with the second gateway token: status %d, want 200", status)
	}

	if status, body := apiCall(t, http.MethodPost, api+"/v1/sessions/"+created.ID+"/delete", alice, ""); status >= 300 {
		t.Fatalf("Alice deletes her session: status %d: %s", status, body)
	}
	closed := identitybroker.ReasonError(identitybroker.ReasonSessionClosed, "")
	if _, err := call("op-3"); !errors.Is(err, closed) {
		t.Fatalf("authorize after the session closed = %v, want %v", err, closed)
	}
	if n := gw.as1.Count(identityharness.KindIDJAG); n != 2 {
		t.Fatalf("the broker asked AS-1 for %d ID-JAGs, want none after the session closed", n)
	}
}

// notesRoute starts an anonymous MCP backend, offline, and returns the route
// declaring it, to append to gatewaySettings' servers. The vMCP ends a
// session in which no backend answers, and the calendar backend never does
// without a provider credential, so this one keeps a call's vMCP session open.
func notesRoute(t *testing.T) string {
	t.Helper()
	notes := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "notes", Version: "v1"}, nil)
	mcpsdk.AddTool(notes, &mcpsdk.Tool{Name: "ping"}, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{}, nil, nil
	})
	notesServer := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return notes }, nil))
	t.Cleanup(notesServer.Close)
	return "    - name: notes\n      url: " + notesServer.URL + "\n      auth: {mode: none}\n"
}

// expiringToolScript is a mock model that calls the gateway route's echo
// tool once.
const expiringToolScript = `{"turns":[
  {"tool_calls":[{"id":"call-expiring","name":"mcp__calendar__echo","args":{"a":1}}]},
  {"text":"done"}
]}`

// TestIdentityBrokerGatewayCallRenewsAnExpiredCredential runs a session's
// gateway tool call whose agent credential expires on its way to the real
// AS-1: the broker accepted it, but the broker's clock, which the real
// verifier AS-1 calls uses, moves past its expiry and clock skew as the ID-JAG
// exchange leaves. AS-1 refuses the exchange with actor_token_expired, the
// MCP broker renews the credential and asks once more with the new one, and
// the call completes at the gateway.
func TestIdentityBrokerGatewayCallRenewsAnExpiredCredential(t *testing.T) {
	script := filepath.Join(t.TempDir(), "script.json")
	if err := os.WriteFile(script, []byte(expiringToolScript), 0o600); err != nil {
		t.Fatal(err)
	}
	diag := &gatewayCallDiagnostics{}
	gw := serveGatewayIdentityBroker(t, notesRoute(t), []string{"--mock-script", script}, func(cfg *app.Config) { cfg.Diagnostics = diag })
	status, body := apiCall(t, http.MethodPost, gw.api+"/v1/sessions", gw.alice, "{}")
	var created struct {
		ID string `json:"session_id"`
	}
	if status != http.StatusCreated && status != http.StatusOK || json.Unmarshal([]byte(body), &created) != nil || created.ID == "" {
		t.Fatalf("Alice creates a session: status %d: %s", status, body)
	}
	// The run's entry registers the main agent on the broker's clock, before
	// the call; the agent's credential lasts an hour.
	gw.as1.OnNext(identityharness.KindIDJAG, func() { gw.clockOffset.Store(int64(time.Hour + 2*time.Minute)) })
	if asks := promptApprovingAsks(t, gw.api, gw.alice, created.ID, "call the calendar", nil); asks != 0 {
		t.Fatalf("the run asked %d times, want none", asks)
	}

	ops := diag.operations(created.ID)
	if len(ops) != 1 {
		t.Fatalf("gateway call diagnostics name operations %v, want the one call's", ops)
	}
	var exchanges []identityharness.GrantRequest
	for _, r := range gw.as1.Requests() {
		if r.Kind == identityharness.KindIDJAG {
			exchanges = append(exchanges, r)
		}
	}
	// Exactly one retry, for the same operation.
	if len(exchanges) != 2 || exchanges[0].Operation != ops[0] || exchanges[1].Operation != ops[0] {
		t.Fatalf("the broker sent %d ID-JAG exchanges, want the refused one and one retry for operation %s", len(exchanges), ops[0])
	}
	if r := exchanges[0]; !r.Reached || r.Status != http.StatusBadRequest || r.Code != "invalid_request" || r.Reason != "actor_token_expired" || r.IssuedToken != "" {
		t.Fatalf("AS-1 answered the first exchange %d %q (%s), want its own invalid_request actor_token_expired", r.Status, r.Code, r.Reason)
	}
	// The retry presented a renewed credential, expiring after the first.
	expired, renewed := jwtClaims(t, exchanges[0].ActorToken), jwtClaims(t, exchanges[1].ActorToken)
	expiredExp, errExpired := expired.GetExpirationTime()
	renewedExp, errRenewed := renewed.GetExpirationTime()
	if exchanges[1].ActorToken == exchanges[0].ActorToken || errExpired != nil || errRenewed != nil || !renewedExp.After(expiredExp.Time) || renewed["sub"] != expired["sub"] {
		t.Fatal("the retry did not present a renewed credential of the same agent")
	}
	if issued := gw.idjags(t); len(issued) != 1 || exchanges[1].IssuedToken == "" {
		t.Fatalf("AS-1 issued %d ID-JAGs, want one, for the retry", len(issued))
	}
	gw.wantStandInRefusal(t, ops[0], diag.call(ops[0]))
}

// brokerTrustDomain is the agent trust domain of the identity broker's test
// configuration file, the act.iss of every ID-JAG AS-1 issues for its agents.
const brokerTrustDomain = brokerconfigtest.TrustDomain

// gatewayGrantLifetime is the lifetime of the broker's grants at the gateway
// tests' AS-1: long enough that a test can move the broker's clock past an
// agent credential's expiry, an hour, without ending the broker session.
const gatewayGrantLifetime = 4 * time.Hour

// gatewayServe is mecak8s serving the gateway path at the real AS-1, with
// Alice signed in there.
type gatewayServe struct {
	h     *identityharness.Harness
	api   string
	built *app.Built
	// as1 is in front of the broker's transport to AS-1.
	as1 *identityharness.FaultTransport
	// as1Server is AS-1, where a test can sign Alice in again.
	as1Server *as1servertest.AS1
	// alice is Alice's access token from her sign-in before the build.
	alice string
	// clockOffset moves the broker's clock from wall time, which AS-1, AS-2,
	// and the vMCP keep.
	clockOffset atomic.Int64
	// refusals are the call-binding refusals the vMCP logged.
	refusals *bindingRefusals
}

// bindingRefusals is a slog handler that records the reason of each
// call-binding refusal the vMCP logs, by the operation its token's call
// entry names, and passes every record on.
type bindingRefusals struct {
	next slog.Handler
	mu   *sync.Mutex
	byOp map[string][]string
}

// captureBindingRefusals installs a bindingRefusals as the default logger
// for t, whose tests must not be parallel: the vMCP logs through it.
func captureBindingRefusals(t *testing.T) *bindingRefusals {
	t.Helper()
	previous := slog.Default()
	// Records go on to stderr, not to previous's handler: slog's initial
	// handler writes through the log package, which SetDefault points back
	// at the new default, so wrapping it would deadlock.
	h := &bindingRefusals{next: slog.NewTextHandler(os.Stderr, nil), mu: &sync.Mutex{}, byOp: map[string][]string{}}
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return h
}

func (*bindingRefusals) Enabled(context.Context, slog.Level) bool { return true }

func (h *bindingRefusals) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "call binding: refused a request" {
		var op, reason string
		r.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case "operation_id":
				op = a.Value.String()
			case "reason":
				reason = a.Value.String()
			}
			return true
		})
		h.mu.Lock()
		h.byOp[op] = append(h.byOp[op], reason)
		h.mu.Unlock()
	}
	if h.next.Enabled(ctx, r.Level) {
		return h.next.Handle(ctx, r)
	}
	return nil
}

func (h *bindingRefusals) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.next = h.next.WithAttrs(attrs)
	return &c
}

func (h *bindingRefusals) WithGroup(name string) slog.Handler {
	c := *h
	c.next = h.next.WithGroup(name)
	return &c
}

// reasons returns the refusal reasons logged for operation op, oldest first.
func (h *bindingRefusals) reasons(op string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.byOp[op])
}

// wantStandInRefusal fails t unless the gateway call op, whose diagnostic
// is got, reached the vMCP once with a call-bound token the vMCP accepted,
// and the vMCP refused it before running it only because it finds no
// provider credential for the calendar backend, #2537's stand-in: the call
// binding's tool_not_found, and no other refusal. A token the vMCP did not
// accept gets 401, and one whose call entry does not match the call another
// reason.
func (gw *gatewayServe) wantStandInRefusal(t *testing.T, op string, got map[string]string) {
	t.Helper()
	if got["code"] != "gateway_refused" || got["outcome"] != "refused" || got["http_status"] != "403" ||
		got["attempts"] == "" || strings.Contains(got["attempts"], ",") {
		t.Errorf("gateway call %s diagnostics %v; want one attempt the vMCP refused before running it", op, got)
	}
	if reasons := gw.refusals.reasons(op); !slices.Equal(reasons, []string{"tool_not_found"}) {
		t.Errorf("the vMCP refused gateway call %s for %v; want only tool_not_found, the stand-in's missing provider credential", op, reasons)
	}
}

// standInResult is the tool result of a gateway call wantStandInRefusal
// accepts.
const standInResult = "MCP gateway call not run: the gateway refused it before running it; it is not retried (code gateway_refused)."

// idjags returns the claims of the ID-JAGs AS-1 issued the broker, oldest
// first, after checking each is for AS-2 and the vMCP and names an ag_ agent
// of the broker's trust domain in act.
func (gw *gatewayServe) idjags(t *testing.T) []jwt.MapClaims {
	t.Helper()
	actIssuer := identitybroker.ActorIssuer(brokerTrustDomain)
	var out []jwt.MapClaims
	for _, r := range gw.as1.Requests() {
		if r.Kind != identityharness.KindIDJAG || r.IssuedToken == "" {
			continue
		}
		claims := jwtClaims(t, r.IssuedToken)
		act, _ := claims["act"].(map[string]any)
		sub, _ := act["sub"].(string)
		if claims["aud"] != gatewayAS2Issuer || claims["resource"] != gatewayResource ||
			act["iss"] != actIssuer || !strings.HasPrefix(sub, actIssuer+"/agent/ag_") {
			t.Fatalf("ID-JAG aud %v resource %v act %v; want AS-2, the vMCP, and an ag_ agent of %s", claims["aud"], claims["resource"], act, actIssuer)
		}
		out = append(out, claims)
	}
	return out
}

// idjagOperations returns the operation each ID-JAG exchange the broker sent
// AS-1 named, oldest first.
func (gw *gatewayServe) idjagOperations() []string {
	var out []string
	for _, r := range gw.as1.Requests() {
		if r.Kind == identityharness.KindIDJAG {
			out = append(out, r.Operation)
		}
	}
	return out
}

// jwtClaims returns token's claims without verifying it.
func jwtClaims(t *testing.T, token string) jwt.MapClaims {
	t.Helper()
	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, claims); err != nil {
		t.Fatalf("parse a JWT: %v", err)
	}
	return claims
}

// gatewayTokenClaims returns the claims of got's gateway token.
func gatewayTokenClaims(t *testing.T, got identitybroker.Authorization) jwt.MapClaims {
	t.Helper()
	return jwtClaims(t, got.GatewayToken.Value())
}

// serveGatewayIdentityBroker serves mecak8s, offline, with the identity
// broker on at the real AS-1, which issues ID-JAGs for AS-2 and checks actor
// tokens with the broker's verifier, the calendar route of gatewaySettings
// plus extraSettings, and a gateway entry routing calendar, whose AS-2 trusts
// AS-1 for Alice. extraArgs are further flags, and edit, when set, changes the
// application configuration after that.
func serveGatewayIdentityBroker(t *testing.T, extraSettings string, extraArgs []string, edit func(*app.Config)) *gatewayServe {
	t.Helper()
	return serveGatewayIdentityBrokerWith(t, "", extraSettings, extraArgs, edit)
}

// serveGatewayIdentityBrokerWith is serveGatewayIdentityBroker with
// brokerSettings, further lines of the mcp.broker mapping, each indented by
// four spaces.
func serveGatewayIdentityBrokerWith(t *testing.T, brokerSettings, extraSettings string, extraArgs []string, edit func(*app.Config)) *gatewayServe {
	t.Helper()
	h := identityharness.New(t, identityharness.Options{})
	issueIDJAGs := as1servertest.IssueIDJAGs(brokerTrustDomain, gatewayAS2Issuer, gatewayResource)
	a := as1servertest.Start(t, h, func(c *as1server.Config, d *as1server.DelegateClient) {
		issueIDJAGs(c, d)
		d.IDJAGMaxLifetime = gatewayGrantLifetime
	})
	gw := &gatewayServe{h: h, as1Server: a, refusals: captureBindingRefusals(t)}
	// AS-2's test trust names Alice's AS-1 subject, which AS-1 assigns at her
	// first sign-in, so she signs in before the build.
	alice := a.SignIn(t, as1servertest.SignIn{User: identityharness.Alice})
	as1HTTP := a.HTTPClient()
	faults := identityharness.NewFaultTransport(as1HTTP.Transport, a.Server.TokenEndpoint())
	as1HTTP.Transport = faults
	t.Setenv(gatewayClientSecretEnv, "construction-only-secret")
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	signingKey := brokerconfigtest.WriteKey(t, filepath.Join(t.TempDir(), "gateway-signing-key.pem"), brokerconfigtest.NewKey(t))
	if err := os.WriteFile(settings, []byte(strings.Replace(gatewaySettings, gatewaySigningKeyPlaceholder, signingKey+"\n"+brokerSettings, 1)+extraSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	caFile := h.WriteCACert(t, t.TempDir())
	api, built := serveIdentityBroker(t, identityBrokerAS1Tokens{
		issuer: a.Issuer(), audience: as1servertest.Audience, caFile: caFile, clientIDs: as1servertest.PublicClientID,
	}, &app.IdentityBrokerAS1{
		Issuer: a.Issuer(), TokenEndpoint: a.Server.TokenEndpoint(), HTTPClient: as1HTTP, ActorTokens: a.Server,
		// The broker's client assertions stay on wall time, which AS-1 checks
		// them against.
		Now: func() time.Time { return time.Now().Add(time.Duration(gw.clockOffset.Load())) },
	}, identityBrokerServeOptions{
		clientKey: a.Key,
		gateways:  []identitybroker.GatewayEntry{brokerconfigtest.EmbeddedGateway(gatewayAS2Issuer, "calendar")},
		args:      append([]string{"--permission-config", settings}, extraArgs...),
		edit: func(cfg *app.Config) {
			cfg.IdentityBrokerAS2Trust = mcpbrokertest.TrustAS1(mcpbrokertest.AS1Trust{
				Issuer: a.Issuer(), JWKSURL: a.JWKSURL(), CAFile: caFile, AllowPrivateAddresses: true,
				Subjects: []string{as1servertest.Subject(t, alice)},
			})
			if edit != nil {
				edit(cfg)
			}
		},
	})
	gw.api, gw.built, gw.as1, gw.alice = api, built, faults, alice.Value()
	return gw
}

// TestGatewaySigningKeySettingNeedsAnEmbeddedGatewayEntry: mecak8s reads the
// embedded gateway's signing key from settings.yaml and refuses it without
// the identity broker and an embedded gateway entry in its configuration
// file. It is not parallel: the protected route's client secret is read from
// an environment variable.
func TestGatewaySigningKeySettingNeedsAnEmbeddedGatewayEntry(t *testing.T) {
	t.Setenv(gatewayClientSecretEnv, "construction-only-secret")
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	signingKey := brokerconfigtest.WriteKey(t, filepath.Join(t.TempDir(), "gateway-signing-key.pem"), brokerconfigtest.NewKey(t))
	if err := os.WriteFile(settings, []byte(strings.Replace(gatewaySettings, gatewaySigningKeyPlaceholder, signingKey, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"--mock", "--no-shell", "--permission-mode=yolo", "--guardrails=off", "--session-lease-k8s-namespace", "", "--permission-config", settings}
	for name, args := range map[string][]string{
		"identity broker off": base,
		"no gateway entry":    append(slices.Clone(base), "--enable-identity-broker", "--identity-broker-config", brokerconfigtest.Write(t, brokerconfigtest.Options{}).Path),
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := parseFlags(args)
			if err != nil {
				t.Fatalf("parseFlags: %v", err)
			}
			built, err := buildIsolated(t, t.Context(), appConfig(cfg, port.NopDiagnostics{}, observability{}))
			if err == nil {
				built.Close()
				t.Fatal("app.Build accepted the signing key")
			}
			if !strings.Contains(err.Error(), "mcp.broker.signing_key_file requires the identity broker") {
				t.Fatalf("app.Build error = %v, want the signing key refused", err)
			}
		})
	}
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
// TestIdentityBrokerGatewayCallAgainstRealAS1.
func TestIdentityBrokerSessionToolCallThroughGateway(t *testing.T) {
	script := filepath.Join(t.TempDir(), "script.json")
	if err := os.WriteFile(script, []byte(gatewayToolScript), 0o600); err != nil {
		t.Fatal(err)
	}
	diag := &gatewayCallDiagnostics{}
	// Every call of the echo tool asks, so the test acts between the model's
	// call and its dispatch.
	extraSettings := notesRoute(t) + "permissions:\n  ask: [mcp__calendar__echo]\n"
	gw := serveGatewayIdentityBroker(t, extraSettings,
		[]string{"--mock-script", script}, func(cfg *app.Config) { cfg.Diagnostics = diag })
	h, api, built, alice := gw.h, gw.api, gw.built, gw.alice

	status, body := apiCall(t, http.MethodPost, api+"/v1/sessions", alice, "{}")
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
	if asks := promptApprovingAsks(t, api, alice, created.ID, "call the calendar", nil); asks != 1 {
		t.Fatalf("the run asked %d times, want once for the tool call", asks)
	}
	nextOperation("call-open")
	opened, ok, err := built.IdentityBroker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: created.ID})
	if err != nil || !ok || !opened.HoldsRefreshToken {
		t.Fatalf("Alice's broker session = %+v, %v, %v; want one holding a refresh token", opened, ok, err)
	}
	// One ID-JAG, for the call's operation and an ag_ agent, addressed to
	// AS-2 for the vMCP.
	if issued, ops := gw.idjags(t), gw.idjagOperations(); len(issued) != 1 || !slices.Equal(ops, []string{operation("call-open")}) {
		t.Fatalf("AS-1 issued %d ID-JAGs for operations %v, want one for the call's operation %s", len(issued), ops, operation("call-open"))
	}
	// The broker allowed the call: only an allowed decision issues an
	// attempt, and the vMCP accepted its call-bound token. Without a
	// provider credential the vMCP does not route the tool, so it refuses
	// the call before running it.
	gw.wantStandInRefusal(t, operation("call-open"), diag.call(operation("call-open")))
	if res := toolResult("call-open"); !res.IsError || res.Content != standInResult {
		t.Fatalf("the call's result = %q, want the stand-in's refusal", res.Content)
	}

	// The broker session closes while the next call waits for approval.
	closeBroker := func() {
		if err := built.IdentityBroker.CloseSession(t.Context(), identitybroker.CloseSessionRequest{SessionID: opened.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
			t.Errorf("close the broker session: %v", err)
		}
	}
	if asks := promptApprovingAsks(t, api, alice, created.ID, "call it again", closeBroker); asks != 1 {
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
	if n := gw.as1.Count(identityharness.KindIDJAG); n != 1 {
		t.Fatalf("the broker asked AS-1 for %d ID-JAGs, want none after the session closed", n)
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
	if !jwtShaped.MatchString(out.String() + alice) {
		t.Fatal("the scan missed a planted JWT")
	}
	if jwtShaped.MatchString(out.String()) || strings.Contains(out.String(), alice) {
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

// identityBrokerServeOptions are serveIdentityBroker's optional settings.
type identityBrokerServeOptions struct {
	// clientKey is the broker client key, the key the AS-1 registered; nil
	// generates one.
	clientKey *ecdsa.PrivateKey
	// gateways are the configuration file's gateway entries.
	gateways []identitybroker.GatewayEntry
	// args are further flags.
	args []string
	// edit, when set, changes the application configuration before the
	// build.
	edit func(*app.Config)
}

// serveIdentityBroker builds and serves mecak8s with the identity broker on,
// accepting access tokens with sub_id owners from the AS-1 that tokens
// describes, and its session open converting at as1, authenticating with
// opts.clientKey. No flag sets AS-1's endpoints yet (#2490), so the test
// points the broker at as1 directly. It returns the HTTP API's base URL and
// the built application; both stop when t ends.
func serveIdentityBroker(t *testing.T, tokens identityBrokerAS1Tokens, as1 *app.IdentityBrokerAS1, opts identityBrokerServeOptions) (string, *app.Built) {
	t.Helper()
	brokerFile := brokerconfigtest.Write(t, brokerconfigtest.Options{BrokerClientKey: opts.clientKey, Gateways: opts.gateways}).Path
	listeners := boundListeners{}
	cfg, err := parseFlags(append([]string{
		"--mock",
		"--enable-identity-broker", "--no-shell", // the identity broker refuses to run beside a shell
		"--identity-broker-config", brokerFile,
		"--permission-mode=yolo", "--guardrails=off", // unchecked allow-all is yolo
		"--grpc-addr", listeners.bind(t),
		"--http-addr", listeners.bind(t),
		"--drain-addr", listeners.bind(t),
		"--session-lease-k8s-namespace", "",
		"--oidc-issuer", tokens.issuer,
		"--oidc-audience", tokens.audience,
		"--oidc-allow-private-https-issuer",
		"--oidc-ca-cert-file", tokens.caFile,
		"--oidc-owner-identity=sub_id",
		"--oidc-accepted-client-ids", tokens.clientIDs,
	}, opts.args...))
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
	cfg.listen = listeners.listen
	appCfg := appConfig(cfg, port.NopDiagnostics{}, observability{})
	appCfg.IdentityBrokerAS1 = as1
	if opts.edit != nil {
		opts.edit(&appCfg)
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

// unsignedToken is issued, an AS-1 access token, with its claims and typ
// unchanged but unsigned with alg none.
func unsignedToken(t *testing.T, issued string) string {
	t.Helper()
	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(issued, claims); err != nil {
		t.Fatalf("parse the issued access token: %v", err)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	token.Header["typ"] = "at+jwt"
	signed, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign alg none: %v", err)
	}
	return signed
}

// createSession creates a session over the HTTP API with token and returns
// its ID.
func createSession(t *testing.T, api, token string) string {
	t.Helper()
	status, body := apiCall(t, http.MethodPost, api+"/v1/sessions", token, "{}")
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("create session: status %d: %s", status, body)
	}
	var created struct {
		ID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.ID == "" {
		t.Fatalf("create session response %s: %v", body, err)
	}
	return created.ID
}

// toolResult returns the result of call in Alice's session id, failing t if
// it has none.
func (gw *gatewayServe) toolResult(t *testing.T, id session.SessionID, call session.ToolCallID) session.ToolResult {
	t.Helper()
	owner := session.WithPrincipal(t.Context(), &session.Principal{Issuer: gw.h.IdP.Issuer(), Subject: identityharness.Alice, GrantType: session.GrantTypeUser})
	sess, err := gw.built.Service.GetSession(owner, id)
	if err != nil {
		t.Fatalf("GetSession(%s): %v", id, err)
	}
	for _, m := range sess.Conversation.Messages {
		if m.ToolResult != nil && m.ToolResult.CallID == call {
			return *m.ToolResult
		}
	}
	t.Fatalf("session %s has no result for %s", id, call)
	return session.ToolResult{}
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

// builtinMainDefinition is the reference of the built-in main definition the
// identity broker loads in this build.
func builtinMainDefinition() identitybroker.DefinitionRef {
	return identitybroker.DefinitionRef{Source: identitybroker.SourceSystem, Name: identitybroker.BuiltinMain, Revision: definitionloader.BuiltinRevision(buildinfo.BuildID)}
}

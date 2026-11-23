package app

import (
	"context"
	"crypto"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker/mcpbrokertest"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server/as1servertest"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/authserverclient"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/brokerconfig/brokerconfigtest"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// The tests in this file build Mecatl with the real AS-1, external to the
// build, as the identity broker's AS-1 and the bundled MCP broker's AS-2 for
// a protected calendar route. None is parallel: the protected route's AS-2
// reads an environment variable.

// gatewayBuild is a Built whose identity broker converts and gets ID-JAGs at
// the real AS-1, behind as1, with Alice signed in there.
type gatewayBuild struct {
	h     *identityharness.Harness
	built *Built
	as1   *identityharness.FaultTransport
	// access is Alice's access token from her sign-in before the build.
	access identitybroker.Token
}

// gatewayTestServer is the protected route the gateway routes.
const gatewayTestServer = "calendar"

// wantAct is the act claim of an ID-JAG, and of the gateway token AS-2
// redeems it for, naming agentID of the test trust domain.
func wantAct(agentID string) map[string]any {
	actIssuer := identitybroker.ActorIssuer(brokerconfigtest.TrustDomain)
	return map[string]any{"iss": actIssuer, "sub": actIssuer + "/agent/" + agentID}
}

// buildWithRealAS1 builds with the real AS-1, issuing ID-JAGs for AS-2 and
// checking actor tokens with the built broker's verifier, a protected
// calendar route with the embedded gateway's signing key, and gateways, the
// broker's gateway entries, after edit changes the configuration. trustAS1
// makes AS-2 trust AS-1 for Alice.
func buildWithRealAS1(t *testing.T, trustAS1 bool, gateways []identitybroker.GatewayEntry, edit func(*Config)) *gatewayBuild {
	t.Helper()
	h := identityharness.New(t, identityharness.Options{})
	a := as1servertest.Start(t, h, as1servertest.IssueIDJAGs(brokerconfigtest.TrustDomain, as2TestIssuer, as2TestIssuer+"/mcp"))
	// AS-2's test trust names Alice's AS-1 subject, which AS-1 assigns at
	// her first sign-in, so she signs in before the build.
	access := a.SignIn(t, as1servertest.SignIn{User: identityharness.Alice})
	as1HTTP := a.HTTPClient()
	faults := identityharness.NewFaultTransport(as1HTTP.Transport, a.Server.TokenEndpoint())
	as1HTTP.Transport = faults
	cfg := Config{
		UseMock: true, NoSoul: true, NoShell: true,
		IdentityBroker: true, MCPAuthority: protectedBrokerAuthority(),
		// The file's broker client key is the one AS-1 registered.
		IdentityBrokerConfigFile: brokerconfigtest.Write(t, brokerconfigtest.Options{BrokerClientKey: a.Key, Gateways: gateways}).Path,
		IdentityBrokerAS1: &IdentityBrokerAS1{
			Issuer: a.Issuer(), TokenEndpoint: a.Server.TokenEndpoint(), HTTPClient: as1HTTP, ActorTokens: a.Server,
		},
	}
	if len(gateways) != 0 {
		cfg.MCPAuthority = protectedBrokerAuthorityWithKey(t)
	}
	if trustAS1 {
		cfg.IdentityBrokerAS2Trust = mcpbrokertest.TrustAS1(mcpbrokertest.AS1Trust{
			Issuer: a.Issuer(), JWKSURL: a.JWKSURL(), CAFile: h.WriteCACert(t, t.TempDir()),
			Subjects: []string{as1servertest.Subject(t, access)},
		})
	}
	if edit != nil {
		edit(&cfg)
	}
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(built.Close)
	if built.identityBrokerCore == nil {
		t.Fatal("startup built no real identity broker core")
	}
	return &gatewayBuild{h: h, built: built, as1: faults, access: access}
}

func (g *gatewayBuild) owner() identitybroker.Owner {
	return identitybroker.Owner{Issuer: g.h.IdP.Issuer(), Subject: identityharness.Alice}
}

// open opens a broker session for Alice holding a grant with her access
// token, through the boundary Mecatl calls, and registers its main agent. It
// returns the session, the agent's credential, and the access token it
// opened with.
func (g *gatewayBuild) open(t *testing.T) (identitybroker.Session, identitybroker.Token, string) {
	t.Helper()
	access := g.access.Value()
	s, err := g.built.IdentityBroker.OpenSession(t.Context(), identitybroker.OpenSessionRequest{
		HostSessionID: "hostS42", Incarnation: string(session.NewIncarnationID()), Owner: g.owner(),
		AccessToken: g.access,
	})
	if err != nil || !s.HoldsRefreshToken {
		t.Fatalf("OpenSession = %+v, %v; want a session holding a grant", s, err)
	}
	reg, err := g.built.IdentityBroker.RegisterAgent(t.Context(), identitybroker.RegisterAgentRequest{
		SessionID: s.ID, HostSessionID: s.HostSessionID, Owner: s.Owner, Incarnation: s.Incarnation,
		Definition: builtinMainRef(),
	})
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	return s, reg.Credential, access
}

func (g *gatewayBuild) call(s identitybroker.Session, cred identitybroker.Token) identitybroker.AuthorizeCallRequest {
	return identitybroker.AuthorizeCallRequest{
		SessionID: s.ID, Credential: cred, OperationID: "op-1", RunID: "R42", RunOwner: g.owner(),
		Method: identitybroker.MethodToolsCall, Server: gatewayTestServer, Tool: "calendar.list_events",
		ArgumentsSHA256: strings.Repeat("ab", 32),
	}
}

// wantBrokerError fails t unless err is a broker error with code and, when
// set, reason.
func wantBrokerError(t *testing.T, err error, code identitybroker.Code, reason identitybroker.Reason) {
	t.Helper()
	var e *identitybroker.Error
	if !errors.As(err, &e) || e.Code != code || (reason != "" && e.Reason != reason) {
		t.Fatalf("error = %v, want %s %s", err, code, reason)
	}
}

// jwtSegmentText returns the decoded header (i 0) or payload (i 1) of a
// compact JWS as text.
func jwtSegmentText(t *testing.T, token string, i int) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("a token has %d parts, want a compact JWS", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[i])
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// jwtClaims returns a compact JWS's claims.
func jwtClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	var claims map[string]any
	if err := json.Unmarshal([]byte(jwtSegmentText(t, token, 1)), &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

// TestBuildBindsAS2ToTheIdentityBrokerGateway shows startup binds the bundled
// MCP broker's AS-2 to the identity broker's gateway: a call gets an ID-JAG at
// AS-1 and a gateway token at the real AS-2 naming the agent as the actor and
// the broker as the client.
func TestBuildBindsAS2ToTheIdentityBrokerGateway(t *testing.T) {
	g := buildWithRealAS1(t, true, embeddedEntry(gatewayTestServer), nil)
	if got := g.built.identityBrokerAS2; got == nil || got.Issuer != as2TestIssuer || got.TokenEndpoint != as2TestIssuer+"/oauth/token" || got.Resource != as2TestIssuer+"/mcp" {
		t.Fatalf("AS-2 = %+v, want the entry's", got)
	}
	s, cred, _ := g.open(t)
	auth, err := g.built.IdentityBroker.AuthorizeCall(t.Context(), g.call(s, cred))
	if err != nil {
		t.Fatalf("AuthorizeCall: %v", err)
	}
	if n := g.as1.Count(identityharness.KindIDJAG); n != 1 {
		t.Fatalf("sent %d ID-JAG exchanges, want 1", n)
	}
	jags := g.built.identityBrokerRecorder.IDJAGs()
	if len(jags) != 1 || !strings.HasPrefix(jags[0].AgentID, "ag_") {
		t.Fatalf("ID-JAG records = %+v, want one naming an ag_ agent", jags)
	}
	v := jwtClaims(t, auth.GatewayToken.Value())
	if v["client_id"] != identitybroker.BrokerClientID ||
		!reflect.DeepEqual(v["act"], wantAct(jags[0].AgentID)) ||
		!reflect.DeepEqual(v["aud"], []any{as2TestIssuer + "/mcp"}) {
		t.Fatalf("gateway token claims = %v, want act %v, client_id %s, and aud the vMCP", v, wantAct(jags[0].AgentID), identitybroker.BrokerClientID)
	}
}

// TestBuildIdentityBrokerUnboundGatewayIsUnavailable shows a gateway whose
// AS-2 shutdown unbound refuses calls as unavailable before asking AS-1 for
// an ID-JAG. Startup never leaves a gateway unbound: it refuses an entry
// without the embedded gateway (TestBuildRefusesADisagreeingGatewayEntry).
func TestBuildIdentityBrokerUnboundGatewayIsUnavailable(t *testing.T) {
	t.Run("unbound at shutdown", func(t *testing.T) {
		g := buildWithRealAS1(t, true, embeddedEntry(gatewayTestServer), nil)
		s, cred, _ := g.open(t)
		if _, err := g.built.IdentityBroker.AuthorizeCall(t.Context(), g.call(s, cred)); err != nil {
			t.Fatalf("AuthorizeCall before shutdown: %v", err)
		}
		g.built.Close()
		req := g.call(s, cred)
		req.OperationID = "op-2"
		_, err := g.built.IdentityBroker.AuthorizeCall(t.Context(), req)
		wantBrokerError(t, err, identitybroker.CodeUnavailable, "")
		if n := g.as1.Count(identityharness.KindIDJAG); n != 1 {
			t.Fatalf("sent %d ID-JAG exchanges, want only the one before shutdown", n)
		}
	})
}

// TestBuildCloseFinishesACallInFlightBeforeTheMCPBroker shows shutdown
// unbinds AS-2, waiting for a call in flight, before it closes the MCP broker
// that serves AS-2: the call held at its ID-JAG exchange while Close runs
// still gets its gateway token.
func TestBuildCloseFinishesACallInFlightBeforeTheMCPBroker(t *testing.T) {
	g := buildWithRealAS1(t, true, embeddedEntry(gatewayTestServer), nil)
	s, cred, _ := g.open(t)
	entered, proceed := make(chan struct{}), make(chan struct{})
	// A failed assertion still lets the call in flight finish.
	release := sync.OnceFunc(func() { close(proceed) })
	t.Cleanup(release)
	g.as1.OnNext(identityharness.KindIDJAG, func() {
		close(entered)
		<-proceed
	})
	type result struct {
		auth identitybroker.Authorization
		err  error
	}
	called := make(chan result, 1)
	go func() {
		auth, err := g.built.IdentityBroker.AuthorizeCall(t.Context(), g.call(s, cred))
		called <- result{auth, err}
	}()
	const limit = 30 * time.Second
	select {
	case <-entered:
	case <-time.After(limit):
		t.Fatal("the call never reached AS-1")
	}

	closed := make(chan struct{})
	go func() {
		g.built.Close()
		close(closed)
	}()
	// Wait until Close has unbound AS-2. A probe call, already cancelled,
	// fails either way without reaching AS-1, but only one that found AS-2
	// bound was given an attempt.
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	deadline := time.Now().Add(limit)
	for i := 0; ; i++ {
		if time.Now().After(deadline) {
			t.Fatal("Close never unbound AS-2")
		}
		probe := g.call(s, cred)
		probe.OperationID = fmt.Sprintf("probe-%d", i)
		_, err := g.built.IdentityBroker.AuthorizeCall(cancelled, probe)
		d := g.built.identityBrokerRecorder.Decisions()
		if last := d[len(d)-1]; err != nil && last.OperationID == probe.OperationID && last.AttemptID == "" {
			wantBrokerError(t, err, identitybroker.CodeUnavailable, "")
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-closed:
		t.Fatal("Close returned while a call was in flight")
	default:
	}

	release()
	select {
	case got := <-called:
		if got.err != nil || got.auth.GatewayToken.IsZero() {
			t.Fatalf("the call in flight = %+v, %v; want a gateway token from the open MCP broker", got.auth, got.err)
		}
	case <-time.After(limit):
		t.Fatal("the call in flight never finished")
	}
	select {
	case <-closed:
	case <-time.After(limit):
		t.Fatal("Close never returned")
	}
}

// TestBuildConfiguresNoGatewayInProduction shows the configurations
// production can start with, an embedded gateway entry routing no servers or
// no entry, give the identity broker no gateway: a session holding a grant is
// denied without an ID-JAG request. The entry still sets AS-2 up, with the
// operator's signing key; without one there is no AS-2, as with the identity
// broker off.
func TestBuildConfiguresNoGatewayInProduction(t *testing.T) {
	for name, tc := range map[string]struct {
		gateways []identitybroker.GatewayEntry
		as2      bool
	}{
		"an entry routing no servers": {embeddedEntry(), true},
		"no entry":                    {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			g := buildWithRealAS1(t, false, tc.gateways, nil)
			if got := g.built.identityBrokerAS2 != nil; got != tc.as2 {
				t.Fatalf("startup set up AS-2: %v, want %v", got, tc.as2)
			}
			if g.built.mcpBrokerGatewayServers != nil {
				t.Fatalf("gateway servers = %v, want none", g.built.mcpBrokerGatewayServers)
			}
			s, cred, _ := g.open(t)
			_, err := g.built.IdentityBroker.AuthorizeCall(t.Context(), g.call(s, cred))
			wantBrokerError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied)
			if n := g.as1.Count(identityharness.KindIDJAG); n != 0 {
				t.Fatalf("sent %d ID-JAG exchanges, want none", n)
			}
		})
	}
}

// TestIdentityBrokerTokensStayOutOfSessionsAndLogs authorizes a call and a
// refused one, then runs a prompt, and looks for every token the call
// involved in the session's transcript and events, the diagnostics, the
// environment, the errors, and the broker's records (#2535 done-when 8). The
// same places must not carry the client assertions the broker sent to AS-1,
// and no assertion may carry a token. Its run makes no tool call; a real
// gateway tool call's transcript and events are scanned by mecak8s's
// TestIdentityBrokerSessionToolCallThroughGateway.
func TestIdentityBrokerTokensStayOutOfSessionsAndLogs(t *testing.T) {
	diag := &recordingDiag{}
	g := buildWithRealAS1(t, true, embeddedEntry(gatewayTestServer), func(cfg *Config) {
		cfg.Diagnostics = diag
		cfg.UseMock, cfg.MockProvider = false, mockllm.New(mockllm.TextTurn("done"))
	})
	s, cred, access := g.open(t)
	auth, err := g.built.IdentityBroker.AuthorizeCall(t.Context(), g.call(s, cred))
	if err != nil {
		t.Fatalf("AuthorizeCall: %v", err)
	}
	var errs []error
	g.as1.FailNext(identityharness.KindIDJAG, identityharness.Fault{Error: "invalid_request", Reason: "denied"})
	refused := g.call(s, cred)
	refused.OperationID = "op-2"
	_, err = g.built.IdentityBroker.AuthorizeCall(t.Context(), refused)
	errs = append(errs, err)

	ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: g.h.IdP.Issuer(), Subject: identityharness.Alice, GrantType: session.GrantTypeUser})
	sess, err := g.built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := g.built.Service.StartRun(ctx, sess.ID, "hello")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var out strings.Builder
	var events int
	for ev := range run.Events() {
		events++
		fmt.Fprintf(&out, "%+v\n", ev)
	}
	g.built.Service.FinishRun(sess.ID, run)
	if events == 0 {
		t.Fatal("the run emitted no events")
	}
	transcript, err := g.built.Service.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if transcript.Conversation == nil || transcript.Conversation.Len() == 0 {
		t.Fatal("the session has no transcript")
	}
	fmt.Fprintf(&out, "%+v %+v\n", transcript, transcript.Conversation.Messages)
	if raw, err := json.Marshal(transcript); err == nil {
		out.Write(raw)
	}
	if stream, err := g.built.Service.StreamSessionEvents(ctx, sess.ID); err == nil {
		for ev, err := range stream {
			fmt.Fprintf(&out, "%+v %v\n", ev, err)
		}
	}

	secrets := []string{auth.GatewayToken.Value(), cred.Value(), access}
	var assertions []string
	for _, r := range g.as1.Requests() {
		secrets = append(secrets, r.IssuedToken, r.Assertion)
		assertions = append(assertions, r.ClientAssertion)
	}
	distinct := func(in []string) []string {
		return slices.Compact(slices.Sorted(slices.Values(slices.DeleteFunc(in, func(s string) bool { return s == "" }))))
	}
	secrets, assertions = distinct(secrets), distinct(assertions)
	if len(assertions) == 0 {
		t.Fatal("captured no client assertions")
	}
	// The gateway token, the credential, the access token, the session's
	// refresh token, and the first call's ID-JAG; the refusal was answered
	// without reaching AS-1.
	if len(secrets) != 5 {
		t.Fatalf("captured %d distinct tokens, want 5", len(secrets))
	}

	for _, err := range errs {
		if err == nil {
			t.Fatal("a refusal returned no error")
		}
		fmt.Fprintf(&out, "%v %+v\n", err, err)
	}
	fmt.Fprintf(&out, "%v %+v %#v\n", auth, auth, auth)
	rec := g.built.identityBrokerRecorder
	for _, records := range []any{rec.IDJAGs(), rec.Decisions()} {
		raw, err := json.Marshal(records)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&out, "%+v %s\n", records, raw)
	}
	msgs, attrs := diag.snapshot()
	if len(msgs) == 0 {
		t.Fatal("captured no diagnostics")
	}
	fmt.Fprintf(&out, "%v %+v\n", msgs, attrs)
	out.WriteString(strings.Join(os.Environ(), "\n"))
	leaks := func(text string, needles []string) bool {
		for _, n := range needles {
			if strings.Contains(text, n) {
				return true
			}
		}
		return false
	}
	both := append(slices.Clone(secrets), assertions...)
	// Positive control: a captured token and a captured client assertion
	// logged through a diagnostics sink, formatted as the scan formats
	// diagnostics, are each found.
	for name, planted := range map[string]string{"gateway token": auth.GatewayToken.Value(), "client assertion": assertions[0]} {
		control := &recordingDiag{}
		control.Log(t.Context(), port.LevelWarn, "planted", "value", planted)
		cMsgs, cAttrs := control.snapshot()
		if !leaks(out.String()+fmt.Sprintf("%v %+v\n", cMsgs, cAttrs), both) {
			t.Fatalf("the scan missed a %s planted in the diagnostics", name)
		}
	}
	if leaks(out.String(), both) {
		t.Fatal("a transcript, event, diagnostic, error, record, the environment, or a formatted authorization carries a token or a client assertion")
	}
	// An assertion is signed over its header and payload, so look for a token
	// in their decoded text.
	for _, a := range assertions {
		if leaks(jwtSegmentText(t, a, 0)+jwtSegmentText(t, a, 1), secrets) {
			t.Fatal("a client assertion carries a token")
		}
	}
}

// TestBuildInstallsGatewayCallsOnlyWithGatewayServers shows the MCP broker
// sends calls through the identity broker's gateway only for the servers its
// gateway entry routes, and only with the identity broker on.
func TestBuildInstallsGatewayCallsOnlyWithGatewayServers(t *testing.T) {
	t.Run("with gateway servers", func(t *testing.T) {
		g := buildWithRealAS1(t, true, embeddedEntry(gatewayTestServer), nil)
		if got := g.built.mcpBrokerGatewayServers; !slices.Equal(got, []string{gatewayTestServer}) {
			t.Fatalf("gateway servers = %v, want [%s]", got, gatewayTestServer)
		}
	})
	t.Run("without gateway servers", func(t *testing.T) {
		g := buildWithRealAS1(t, true, embeddedEntry(), nil)
		if g.built.identityBrokerAS2 == nil {
			t.Fatal("startup set up no AS-2")
		}
		if got := g.built.mcpBrokerGatewayServers; got != nil {
			t.Fatalf("gateway servers = %v, want none", got)
		}
	})
	t.Run("without the identity broker", func(t *testing.T) {
		built, err := buildIsolated(t, t.Context(), Config{
			Workspace: t.TempDir(), UseMock: true, NoSoul: true, NoShell: true,
			MCPAuthority: protectedBrokerAuthority(),
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(built.Close)
		if built.MCPBroker == nil || built.IdentityBroker != nil {
			t.Fatalf("broker=%v identity broker=%v, want only the MCP broker", built.MCPBroker != nil, built.IdentityBroker != nil)
		}
		if got := built.mcpBrokerGatewayServers; got != nil {
			t.Fatalf("gateway servers = %v, want none", got)
		}
	})
}

// TestGatewayInProcessClientRefusesOtherURLs shows the gateway's in-process
// client serves only the gateway endpoint.
func TestGatewayInProcessClientRefusesOtherURLs(t *testing.T) {
	const endpoint = as2TestIssuer + "/mcp"
	served := 0
	client, err := gatewayInProcessClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served++
		w.WriteHeader(http.StatusNoContent)
	}), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	for _, refused := range []string{as2TestIssuer + "/token", "http://broker.example/v1/mcp/broker/mcp", "https://other.example/v1/mcp/broker/mcp", "https://user@broker.example/v1/mcp/broker/mcp"} {
		if resp, err := client.Get(refused); err == nil {
			_ = resp.Body.Close()
			t.Errorf("GET %s succeeded, want a refusal", refused)
		}
	}
	resp, err := client.Get(endpoint)
	if err != nil {
		t.Fatalf("GET the endpoint: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || served != 1 {
		t.Fatalf("endpoint status %d after %d handler calls; want 204 from the one call", resp.StatusCode, served)
	}
}

// TestHostedAS1IssuesTheGatewaysIDJAGs builds the production path, the
// identity broker beside a hosted AS-1, with a gateway entry routing a
// protected route: the hosted AS-1 issues a call's ID-JAG for the entry's
// AS-2, naming the agent in act with the broker's trust domain, and checks
// the agent's credential with the broker's verifier, which startup bound.
// AS-2 then refuses the ID-JAG: its test trust would have to list Alice's
// AS-1 subject, which the hosted AS-1 assigns only at a sign-in after the
// build, so it trusts another issuer, and the test reads the ID-JAG from the
// redemption.
func TestHostedAS1IssuesTheGatewaysIDJAGs(t *testing.T) {
	hb := buildWithHostedAS1(t, embeddedEntry(gatewayTestServer))
	// The hosted AS-1 issues ID-JAGs, and startup bound its actor-token check.
	wantBindRefused(t, hb.built, "already bound")
	g := hb.signIn(t)
	s, cred, _ := g.open(t)
	_, err := g.built.IdentityBroker.AuthorizeCall(t.Context(), g.call(s, cred))
	// An AS-2 trusting another issuer answers invalid_grant without a reason
	// (the #2533 shortcut on Config.IdentityBrokerAS2Trust), which the broker
	// retries once with a new ID-JAG before failing the call.
	wantBrokerError(t, err, identitybroker.CodeInternal, "")
	jags := g.built.identityBrokerRecorder.IDJAGs()
	redeemed := hb.as2.Requests()
	if len(jags) != 2 || len(redeemed) != 2 {
		t.Fatalf("%d ID-JAG records and %d redemptions, want two of each: the call and its one retry", len(jags), len(redeemed))
	}
	issuer := hb.issuer
	for i, r := range redeemed {
		if r.Kind != identityharness.KindRedemption || r.Code != "invalid_grant" || r.IssuedToken != "" {
			t.Fatalf("redemption %d = %s answered %q, want an invalid_grant refusal", i, r.Kind, r.Code)
		}
		var header struct{ Typ string }
		if err := json.Unmarshal([]byte(jwtSegmentText(t, r.Assertion, 0)), &header); err != nil || header.Typ != "oauth-id-jag+jwt" {
			t.Fatalf("redemption %d's assertion typ = %q, %v; want an ID-JAG", i, header.Typ, err)
		}
		claims := jwtClaims(t, r.Assertion)
		if claims["iss"] != issuer || claims["aud"] != as2TestIssuer || claims["resource"] != as2TestIssuer+"/mcp" ||
			claims["client_id"] != identitybroker.BrokerClientID || !reflect.DeepEqual(claims["act"], wantAct(jags[i].AgentID)) {
			t.Fatalf("ID-JAG %d claims = %v, want the hosted AS-1's for AS-2 naming %v", i, claims, wantAct(jags[i].AgentID))
		}
		// AS-1 caps the ID-JAG at the agent credential's expiry.
		jagExp, okJAG := claims["exp"].(float64)
		credExp, okCred := jwtClaims(t, cred.Value())["exp"].(float64)
		if !okJAG || !okCred || jagExp > credExp {
			t.Fatalf("ID-JAG %d expires at %v, want no later than the agent credential's %v", i, claims["exp"], credExp)
		}
	}
}

// hostedBuild is a Built hosting AS-1 at issuer, served on a loopback TLS
// listener so users can sign in there, with as2 in front of the identity
// broker's transport to AS-2.
type hostedBuild struct {
	h      *identityharness.Harness
	cfg    Config
	built  *Built
	issuer string
	as2    *identityharness.FaultTransport
}

// buildWithHostedAS1 builds the production path, the identity broker beside
// a hosted AS-1, with a protected calendar route and gateways, the broker's
// gateway entries. With an entry, the embedded gateway has a signing key;
// with one routing servers, AS-2 trusts an issuer other than AS-1, which an
// entry routing servers requires and which refuses AS-1's ID-JAGs.
func buildWithHostedAS1(t *testing.T, gateways []identitybroker.GatewayEntry) *hostedBuild {
	t.Helper()
	h := identityharness.New(t, identityharness.Options{})
	// The issuer names the listener the hosted AS-1 is served on once built.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	hb := &hostedBuild{h: h, issuer: "https://" + ln.Addr().String() + "/as1"}
	hb.cfg = Config{
		UseMock: true, NoSoul: true, NoShell: true,
		IdentityBroker:           true,
		IdentityBrokerConfigFile: brokerconfigtest.Write(t, brokerconfigtest.Options{HostedAS1: true, Gateways: gateways}).Path,
		IdentityBrokerHostedAS1:  hostedAS1Config(t, h, hb.issuer),
		MCPAuthority:             protectedBrokerAuthority(),
		identityBrokerAS2Transport: func(next http.RoundTripper) http.RoundTripper {
			hb.as2 = identityharness.NewFaultTransport(next, as2TestIssuer+"/oauth/token")
			return hb.as2
		},
	}
	if len(gateways) != 0 {
		hb.cfg.MCPAuthority = protectedBrokerAuthorityWithKey(t)
		if len(gateways[0].Servers) != 0 {
			hb.cfg.IdentityBrokerAS2Trust = mcpbrokertest.TrustAS1(mcpbrokertest.AS1Trust{
				Issuer: "https://untrusted-as1.mecatl.test", JWKSURL: "https://untrusted-as1.mecatl.test/jwks",
				CAFile: h.WriteCACert(t, t.TempDir()), Subjects: []string{"nobody"},
			})
		}
	}
	hb.built, err = buildIsolated(t, t.Context(), hb.cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hb.built.Close)
	if (hb.as2 != nil) != (len(gateways) != 0) {
		t.Fatalf("startup set up AS-2: %v, want it exactly with a gateway entry", hb.as2 != nil)
	}
	ts := &httptest.Server{Listener: ln, Config: &http.Server{Handler: hb.built.IdentityBrokerHostedAS1.Handler, ReadHeaderTimeout: 10 * time.Second}}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return hb
}

// signIn signs Alice in at the hosted AS-1 and returns the build with her
// access token, for gatewayBuild's open and call.
func (hb *hostedBuild) signIn(t *testing.T) *gatewayBuild {
	t.Helper()
	access := as1servertest.SignInAt(t, hb.h, hb.issuer, as1servertest.SignIn{
		User:   identityharness.Alice,
		Client: as1server.PublicClient{ID: identityharness.DefaultClientID, RedirectURIs: []string{identityharness.DefaultRedirectURI}},
	})
	return &gatewayBuild{h: hb.h, built: hb.built, access: access}
}

// TestHostedAS1IssuesNoIDJAGsInProduction builds the hosted AS-1 path as
// production can, with an embedded gateway entry routing no servers or with
// no entry: the hosted AS-1 has no ID-JAG issuance (it refuses a verifier
// because no delegate client issues ID-JAGs, where one with issuance refuses
// only a second bind), and a call in a session holding a grant is denied
// without an ID-JAG or a redemption.
func TestHostedAS1IssuesNoIDJAGsInProduction(t *testing.T) {
	for name, gateways := range map[string][]identitybroker.GatewayEntry{
		"an entry routing no servers": embeddedEntry(),
		"no entry":                    nil,
	} {
		t.Run(name, func(t *testing.T) {
			hb := buildWithHostedAS1(t, gateways)
			wantBindRefused(t, hb.built, "no delegate client issues ID-JAGs")
			g := hb.signIn(t)
			s, cred, _ := g.open(t)
			_, err := g.built.IdentityBroker.AuthorizeCall(t.Context(), g.call(s, cred))
			wantBrokerError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied)
			if jags := g.built.identityBrokerRecorder.IDJAGs(); len(jags) != 0 {
				t.Fatalf("ID-JAG records = %+v, want none", jags)
			}
		})
	}
}

// TestHostedAS1IssuesIDJAGsOnlyForTheGatewayEntry asks the hosted AS-1, as
// the broker client, for ID-JAGs for an audience or resource the gateway
// entry does not name: AS-1 refuses each with invalid_target, and issues one
// for the entry's.
func TestHostedAS1IssuesIDJAGsOnlyForTheGatewayEntry(t *testing.T) {
	hb := buildWithHostedAS1(t, embeddedEntry(gatewayTestServer))
	_, cred, _ := hb.signIn(t).open(t)
	// A grant of Alice's own, from another sign-in, for the same agent.
	access := hb.signIn(t).access
	client, err := authserverclient.New(authserverclient.Config{
		Issuer: hb.issuer, TokenEndpoint: hb.built.identityBrokerHostedAS1Server.TokenEndpoint(),
		Assertions: hb.built.identityBrokerClient, HTTPClient: hb.built.IdentityBrokerHostedAS1.HTTPClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	grant, err := client.Exchange(t.Context(), identitybroker.TokenExchange{SubjectToken: access, Grant: identitybroker.ExchangeGrant{
		Type: identitybroker.ExchangeGrantType, IssuableTokenTypes: []string{identitybroker.TokenTypeIDJAG},
	}})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	issue := func(audience, resource string) error {
		_, err := client.IssueIDJAG(t.Context(), identitybroker.IDJAGRequest{
			SubjectToken: grant.RefreshToken, ActorToken: cred, Audience: audience, Resource: resource, OperationID: "op-target",
		})
		return err
	}
	for name, target := range map[string][2]string{
		"another audience": {"https://other.example/v1/mcp/broker", as2TestIssuer + "/mcp"},
		"another resource": {as2TestIssuer, as2TestIssuer + "/other-mcp"},
	} {
		var refusal *identitybroker.ServerRefusal
		if err := issue(target[0], target[1]); !errors.As(err, &refusal) || refusal.Code != "invalid_target" {
			t.Errorf("%s: IssueIDJAG = %v, want invalid_target", name, err)
		}
	}
	if err := issue(as2TestIssuer, as2TestIssuer+"/mcp"); err != nil {
		t.Fatalf("IssueIDJAG for the entry's AS-2 and resource: %v", err)
	}
}

// TestDeriveEmbeddedGateway derives the embedded gateway an embedded gateway
// entry must describe only for a bundled broker with an OAuth route and a
// callback URL AS-2 can be set up for.
func TestDeriveEmbeddedGateway(t *testing.T) {
	protected, _ := protectedBrokerAuthority().Broker()
	protected.Routes = append(protected.Routes, permconfig.MCPServerProfile{Name: "docs", URL: "https://docs.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "none"}})
	want := &embeddedGateway{Issuer: as2TestIssuer, TokenEndpoint: as2TestIssuer + "/oauth/token", Resource: as2TestIssuer + "/mcp", Routes: []string{"calendar"}}
	if got := deriveEmbeddedGateway(Config{}, protected, true); !reflect.DeepEqual(got, want) {
		t.Fatalf("embedded gateway = %+v, want %+v", got, want)
	}
	httpCallback := protected
	httpCallback.CallbackURL = "http://broker.example/oauth/callback"
	anonymousOnly := protected
	anonymousOnly.Routes = protected.Routes[1:]
	configured := Config{}
	configured.MCPBrokerCaller = unusedBrokerCaller
	for name, tc := range map[string]struct {
		cfg         Config
		declaration mcpauthority.BrokerConfig
		selected    bool
	}{
		"global authority":   {Config{}, protected, false},
		"no routes":          {Config{}, mcpauthority.BrokerConfig{CallbackURL: brokerTestCallbackURL}, true},
		"no OAuth route":     {Config{}, anonymousOnly, true},
		"configured broker":  {configured, protected, true},
		"non-HTTPS callback": {Config{}, httpCallback, true},
	} {
		if got := deriveEmbeddedGateway(tc.cfg, tc.declaration, tc.selected); got != nil {
			t.Errorf("%s: embedded gateway = %+v, want none", name, got)
		}
	}
}

// wantBindRefused fails t unless the built hosted AS-1 refuses another
// verifier for the reason want: "no delegate client issues ID-JAGs" when it
// has no ID-JAG issuance, "already bound" when startup bound its check.
func wantBindRefused(t *testing.T, built *Built, want string) {
	t.Helper()
	err := built.identityBrokerHostedAS1Server.UseCredentialVerifier(built.identityBrokerCore)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("hosted AS-1 UseCredentialVerifier = %v, want refused as %q", err, want)
	}
}

// failingActorTokens is an AS-1 actor-token check that refuses every
// verifier.
type failingActorTokens struct{ calls int }

var errActorTokensRefused = errors.New("actor-token check refused the verifier")

func (f *failingActorTokens) UseCredentialVerifier(identitybroker.CredentialVerifier) error {
	f.calls++
	return errActorTokensRefused
}

// TestBuildFailsWhenAS1sActorTokenCheckCannotBind shows startup fails when
// AS-1's actor-token check refuses the built broker's verifier, as the real
// AS-1 does when bound twice.
func TestBuildFailsWhenAS1sActorTokenCheckCannotBind(t *testing.T) {
	t.Run("refused", func(t *testing.T) {
		check := &failingActorTokens{}
		as1 := unusedAS1("https://as1.example")
		as1.ActorTokens = check
		built, err := buildIsolated(t, t.Context(), Config{UseMock: true, NoSoul: true, NoShell: true, IdentityBroker: true, IdentityBrokerConfigFile: testBrokerConfigFile(t), IdentityBrokerAS1: as1})
		if err == nil {
			built.Close()
			t.Fatal("Build succeeded with an actor-token check that refuses the verifier")
		}
		if !errors.Is(err, errActorTokensRefused) || check.calls != 1 {
			t.Fatalf("Build error = %v after %d binds, want %v from one", err, check.calls, errActorTokensRefused)
		}
	})
	t.Run("already bound", func(t *testing.T) {
		a := as1servertest.Start(t, identityharness.New(t, identityharness.Options{}), as1servertest.IssueIDJAGs(brokerconfigtest.TrustDomain, as2TestIssuer, as2TestIssuer+"/mcp"))
		cfg := Config{UseMock: true, NoSoul: true, NoShell: true, IdentityBroker: true, IdentityBrokerConfigFile: brokerconfigtest.Write(t, brokerconfigtest.Options{BrokerClientKey: a.Key}).Path,
			IdentityBrokerAS1: &IdentityBrokerAS1{Issuer: a.Issuer(), TokenEndpoint: a.Server.TokenEndpoint(), HTTPClient: a.HTTPClient(), ActorTokens: a.Server}}
		built, err := buildIsolated(t, t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		built.Close()
		if built, err = buildIsolated(t, t.Context(), cfg); err == nil {
			built.Close()
			t.Fatal("a second Build bound AS-1's actor-token check again")
		}
		// Only the bind, not another startup step, may be what failed.
		if !strings.Contains(err.Error(), "bind AS-1's actor-token check") || !strings.Contains(err.Error(), "already bound") {
			t.Fatalf("second Build error = %v, want the bind refused as already bound", err)
		}
	})
}

// TestBuildMCPBrokerRefusesAS2OtherThanTheEntry shows the bundled MCP broker
// refuses to start when the AS-2 it set up is not the one the identity
// broker's gateway entry names, the check after startup's own.
func TestBuildMCPBrokerRefusesAS2OtherThanTheEntry(t *testing.T) {
	keySet := brokerconfigtest.WriteKeySet(t, filepath.Join(t.TempDir(), "jwks.json"), newBrokerClientKey(t))
	declaration, _ := protectedBrokerAuthorityWithKey(t).Broker()
	for name, tc := range map[string]struct {
		edit  func(*identitybroker.GatewayEntry)
		field string
	}{
		"another issuer":         {func(e *identitybroker.GatewayEntry) { e.AS2Issuer = "https://other.example/v1/mcp/broker" }, "its issuer"},
		"another token endpoint": {func(e *identitybroker.GatewayEntry) { e.TokenEndpoint = as2TestIssuer + "/token" }, "its token endpoint"},
		"another resource":       {func(e *identitybroker.GatewayEntry) { e.Resource = "https://other.example/mcp" }, "its resource"},
	} {
		t.Run(name, func(t *testing.T) {
			entry := embeddedEntry()[0]
			tc.edit(&entry)
			parts, err := buildMCPBroker(t.Context(), Config{}, declaration, nil, mcpBrokerIdentity{clientKeySetFile: keySet, entry: &entry})
			if err == nil {
				parts.close()
				t.Fatal("buildMCPBroker set up an AS-2 the gateway entry does not name")
			}
			// The error names the field that differs, as startup's check does.
			if !errors.Is(err, errAS2IsNotTheEntry) || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("buildMCPBroker error = %v, want %v naming %q", err, errAS2IsNotTheEntry, tc.field)
			}
		})
	}
	entry := embeddedEntry()[0]
	parts, err := buildMCPBroker(t.Context(), Config{}, declaration, nil, mcpBrokerIdentity{clientKeySetFile: keySet, entry: &entry})
	if err != nil {
		t.Fatalf("buildMCPBroker with the matching entry: %v", err)
	}
	parts.close()
}

// TestBuildMCPBrokerRefusesAS2SigningWithABrokerKey shows the bundled MCP
// broker refuses to start when AS-2 loaded a signing key that is one of the
// identity broker's keys, the check after startup's own: startup read the
// key files once, and AS-2 reads them again, so a file changed in between
// is caught here.
func TestBuildMCPBrokerRefusesAS2SigningWithABrokerKey(t *testing.T) {
	keySet := brokerconfigtest.WriteKeySet(t, filepath.Join(t.TempDir(), "jwks.json"), newBrokerClientKey(t))
	declaration, _ := protectedBrokerAuthority().Broker()
	key := brokerconfigtest.NewKey(t)
	declaration.SigningKeyFile = writeAS2Key(t, key)
	entry := embeddedEntry()[0]
	identity := mcpBrokerIdentity{clientKeySetFile: keySet, entry: &entry, brokerKeys: []crypto.PublicKey{&newBrokerClientKey(t).PublicKey, &key.PublicKey}}
	parts, err := buildMCPBroker(t.Context(), Config{}, declaration, nil, identity)
	if err == nil {
		parts.close()
		t.Fatal("buildMCPBroker set up an AS-2 signing with an identity broker key")
	}
	if !errors.Is(err, errAS2SigningKeyIsABrokerKey) {
		t.Fatalf("buildMCPBroker error = %v, want %v", err, errAS2SigningKeyIsABrokerKey)
	}
	identity.brokerKeys = identity.brokerKeys[:1]
	parts, err = buildMCPBroker(t.Context(), Config{}, declaration, nil, identity)
	if err != nil {
		t.Fatalf("buildMCPBroker with a signing key no broker role holds: %v", err)
	}
	parts.close()
}

// uncomparableKey is a public key type without an Equal method.
type uncomparableKey struct{}

// TestCheckAS2KeysAreNotBrokerKeysFailsClosed refuses an AS-2 key it cannot
// compare with the identity broker's keys rather than skipping it.
func TestCheckAS2KeysAreNotBrokerKeysFailsClosed(t *testing.T) {
	broker := []crypto.PublicKey{&newBrokerClientKey(t).PublicKey}
	if err := checkAS2KeysAreNotBrokerKeys([]crypto.PublicKey{uncomparableKey{}}, broker); err == nil {
		t.Fatal("checkAS2KeysAreNotBrokerKeys accepted a key it cannot compare")
	}
	if err := checkAS2KeysAreNotBrokerKeys([]crypto.PublicKey{&newBrokerClientKey(t).PublicKey}, broker); err != nil {
		t.Fatalf("checkAS2KeysAreNotBrokerKeys with a distinct key = %v", err)
	}
}

// resourceGateway is a Gateway with a given endpoint and resource.
type resourceGateway struct{ endpoint, resource string }

func (g resourceGateway) Endpoint() string                        { return g.endpoint }
func (g resourceGateway) Resource() string                        { return g.resource }
func (resourceGateway) HTTPClient() *http.Client                  { return http.DefaultClient }
func (resourceGateway) Route(b, n string) (string, string, error) { return b, n, nil }
func (resourceGateway) ListTools(context.Context, string, identitybroker.Token) ([]mcpbroker.GatewayTool, error) {
	return nil, nil
}

// TestCheckGatewayResource refuses a gateway whose resource, not endpoint, is
// other than AS-2's resource.
func TestCheckGatewayResource(t *testing.T) {
	const resource = as2TestIssuer + "/mcp"
	as2 := &mcpbroker.AS2{Issuer: as2TestIssuer, Resource: resource}
	for name, tc := range map[string]struct {
		gateway mcpbroker.Gateway
		as2     *mcpbroker.AS2
		want    error
	}{
		"no gateway":             {nil, nil, nil},
		"resource is AS-2's":     {resourceGateway{resource, resource}, as2, nil},
		"endpoint other than it": {resourceGateway{"https://vmcp.internal:8443/mcp", resource}, as2, nil},
		"another resource":       {resourceGateway{resource, "https://other.example/mcp"}, as2, errGatewayIsNotAS2Resource},
		"no AS-2":                {resourceGateway{resource, resource}, nil, errGatewayIsNotAS2Resource},
	} {
		if err := checkGatewayResource(tc.gateway, tc.as2); !errors.Is(err, tc.want) {
			t.Errorf("%s: checkGatewayResource = %v, want %v", name, err, tc.want)
		}
	}
}

// TestGatewayCallsFollowALoadedDefinitionsCeiling shows the ceiling the
// loader resolves from a definition's files bounds the calls of a helper
// running it, through the real build: the definition references the calendar
// server and excludes one of its tools, so the helper may call the server's
// other tool, and is refused the excluded one and a server it does not
// reference, before the broker asks AS-1 for an ID-JAG.
func TestGatewayCallsFollowALoadedDefinitionsCeiling(t *testing.T) {
	const github = "github"
	dir := t.TempDir()
	def := "---\nname: scheduler\ndescription: Reads calendars.\nmcpServers: [calendar]\ndisallowedTools: [mcp__calendar__echo]\n---\nRead the calendar.\n"
	if err := os.WriteFile(filepath.Join(dir, "scheduler.md"), []byte(def), 0o600); err != nil {
		t.Fatal(err)
	}
	servers := []string{gatewayTestServer, github}
	g := buildWithRealAS1(t, true, embeddedEntry(servers...), func(cfg *Config) {
		cfg.AgentsDirs = []string{dir}
		// The entry's servers must be OAuth routes of the MCP broker.
		cfg.MCPAuthority = mcpauthority.NewBroker(mcpauthority.BrokerConfig{
			CallbackURL:    brokerTestCallbackURL,
			Routes:         []permconfig.MCPServerProfile{protectedToolHiveRoute(gatewayTestServer), protectedToolHiveRoute(github)},
			SigningKeyFile: writeAS2Key(t, brokerconfigtest.NewKey(t)),
		})
	})

	// The reference the broker holds for the definition, through the loader
	// startup used.
	records, err := loadIdentityBrokerDefinitions(t.Context(), Config{AgentsDirs: []string{dir}, Diagnostics: port.NopDiagnostics{}}, servers)
	if err != nil {
		t.Fatalf("loadIdentityBrokerDefinitions: %v", err)
	}
	var ref identitybroker.DefinitionRef
	for _, rec := range records {
		if rec.Ref.Source == identitybroker.SourceGlobal && rec.Ref.Name == "scheduler" {
			ref = rec.Ref
		}
	}
	if ref == (identitybroker.DefinitionRef{}) {
		t.Fatalf("the loader returned no global/scheduler record in %+v", definitionRefs(records))
	}
	if _, ok := g.built.identityBrokerCore.Definition(ref); !ok {
		t.Fatalf("the built broker did not load %+v", ref)
	}

	s, _, _ := g.open(t)
	main, err := g.built.IdentityBroker.RegisterAgent(t.Context(), identitybroker.RegisterAgentRequest{
		SessionID: s.ID, HostSessionID: s.HostSessionID, Owner: s.Owner, Incarnation: s.Incarnation,
		Definition: builtinMainRef(),
	})
	if err != nil {
		t.Fatalf("registering the main agent again: %v", err)
	}
	helper, err := g.built.IdentityBroker.RegisterAgent(t.Context(), identitybroker.RegisterAgentRequest{
		SessionID: s.ID, HostSessionID: "hostS42-helper", Owner: s.Owner, Incarnation: string(session.NewIncarnationID()),
		Definition: ref, Parent: main.Agent, ParentHostSessionID: s.HostSessionID,
	})
	if err != nil {
		t.Fatalf("RegisterAgent(helper): %v", err)
	}
	// The main agent's run delegates to the helper's, which then calls
	// under that delegation.
	if err := g.built.IdentityBroker.ReportDelegation(t.Context(), identitybroker.ReportDelegationRequest{
		SessionID: s.ID, DelegationID: "D1", DelegatingRun: "R42", DelegatingAgent: main.Agent,
		ReceivingRun: "R43", ReceivingAgent: helper.Agent, Owner: s.Owner,
	}); err != nil {
		t.Fatalf("ReportDelegation: %v", err)
	}
	call := func(op, server, name string) (identitybroker.Authorization, error) {
		req := g.call(s, helper.Credential)
		req.OperationID, req.Server, req.Tool = op, server, server+"."+name
		req.RunID, req.DelegationID = "R43", "D1"
		return g.built.IdentityBroker.AuthorizeCall(t.Context(), req)
	}

	for _, tc := range []struct{ op, server, name string }{
		{"op-excluded", gatewayTestServer, "echo"},
		{"op-unreferenced", github, "list_issues"},
	} {
		_, err := call(tc.op, tc.server, tc.name)
		wantBrokerError(t, err, identitybroker.CodePermissionDenied, "")
		if n := g.as1.Count(identityharness.KindIDJAG); n != 0 {
			t.Fatalf("%s.%s sent %d ID-JAG exchanges, want none", tc.server, tc.name, n)
		}
	}
	if _, err := call("op-allowed", gatewayTestServer, "lookup"); err != nil {
		t.Fatalf("calendar.lookup: %v", err)
	}
	if n := g.as1.Count(identityharness.KindIDJAG); n != 1 {
		t.Fatalf("sent %d ID-JAG exchanges, want 1 for the allowed call", n)
	}
}

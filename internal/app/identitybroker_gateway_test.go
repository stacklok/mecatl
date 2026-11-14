package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker/mcpbrokertest"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server/as1servertest"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// The tests in this file build Mecatl with the identity harness's AS-1 as the
// identity broker's AS-1 and the bundled MCP broker's AS-2 for a protected
// calendar route. None is parallel: the protected route's AS-2 reads an
// environment variable.

// gatewayBuild is a Built whose identity broker converts and issues ID-JAGs
// at the harness's AS-1, behind as1.
type gatewayBuild struct {
	h     *identityharness.Harness
	built *Built
	as1   *identityharness.FaultTransport
}

// gatewayTestServer is the protected route the gateway routes.
const gatewayTestServer = "calendar"

// buildWithHarnessAS1 builds with the harness's AS-1 and a protected
// calendar route, after edit changes the configuration. trustAS1 makes AS-2
// trust the harness's AS-1 for Alice.
func buildWithHarnessAS1(t *testing.T, trustAS1 bool, edit func(*Config)) *gatewayBuild {
	t.Helper()
	clientKey := newBrokerClientKey(t)
	signer, err := identitybroker.NewClientAssertionSigner(clientKey, 0)
	if err != nil {
		t.Fatal(err)
	}
	h := identityharness.New(t, identityharness.Options{
		BrokerKeys: []identityharness.BrokerKey{{KeyID: signer.JWKS().Keys[0].KeyID, Public: &clientKey.PublicKey}},
	})
	h.AS1.AddIDJAGTarget(as2TestIssuer, as2TestIssuer+"/mcp")
	as1HTTP := h.HTTPClient()
	faults := identityharness.NewFaultTransport(as1HTTP.Transport, h.AS1.TokenEndpoint())
	as1HTTP.Transport = faults
	cfg := Config{
		Workspace: t.TempDir(), UseMock: true, NoSoul: true, NoShell: true,
		IdentityBroker: true, MCPAuthority: protectedBrokerAuthority(),
		IdentityBrokerAS1: &IdentityBrokerAS1{Issuer: h.AS1.Issuer(), TokenEndpoint: h.AS1.TokenEndpoint(), HTTPClient: as1HTTP, ClientKey: clientKey},
	}
	if trustAS1 {
		caFile := h.WriteCACert(t, t.TempDir())
		cfg.IdentityBrokerAS2Trust = mcpbrokertest.TrustAS1(mcpbrokertest.AS1Trust{
			Issuer: h.AS1.Issuer(), JWKSURL: h.AS1.JWKSURL(), CAFile: caFile,
			Subjects: []string{h.AS1.Subject(identityharness.Alice)},
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
	h.AS1.SetActorValidator(as1servertest.HarnessActorValidator(built.identityBrokerCore))
	return &gatewayBuild{h: h, built: built, as1: faults}
}

func (g *gatewayBuild) owner() identitybroker.Owner {
	return identitybroker.Owner{Issuer: g.h.IdP.Issuer(), Subject: identityharness.Alice}
}

// open opens a broker session for Alice holding a grant, through the
// boundary Mecatl calls, and registers its main agent. It returns the
// session, the agent's credential, and the access token it opened with.
func (g *gatewayBuild) open(t *testing.T) (identitybroker.Session, identitybroker.Token, string) {
	t.Helper()
	access := g.h.AS1.MintAccessToken(identityharness.AccessTokenSpec{User: identityharness.Alice})
	s, err := g.built.IdentityBroker.OpenSession(t.Context(), identitybroker.OpenSessionRequest{
		HostSessionID: "hostS42", Incarnation: string(session.NewIncarnationID()), Owner: g.owner(),
		AccessToken: identitybroker.NewToken(access),
	})
	if err != nil || !s.HoldsRefreshToken {
		t.Fatalf("OpenSession = %+v, %v; want a session holding a grant", s, err)
	}
	reg, err := g.built.IdentityBroker.RegisterAgent(t.Context(), identitybroker.RegisterAgentRequest{
		SessionID: s.ID, HostSessionID: s.HostSessionID, Owner: s.Owner, Incarnation: s.Incarnation,
		Definition: identitybroker.DefinitionRef{Source: "system", Name: "main"},
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

// jwtClaims returns a compact JWS's claims.
func jwtClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("a token has %d parts, want a compact JWS", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

// TestBuildBindsAS2ToTheIdentityBrokerGateway shows startup binds the bundled
// MCP broker's AS-2 to the identity broker's gateway: a call gets an ID-JAG at
// AS-1 and a gateway token at the real AS-2 naming the agent as the actor and
// the broker as the client.
func TestBuildBindsAS2ToTheIdentityBrokerGateway(t *testing.T) {
	g := buildWithHarnessAS1(t, true, func(cfg *Config) { cfg.IdentityBrokerGatewayServers = []string{gatewayTestServer} })
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
		!reflect.DeepEqual(v["act"], map[string]any{"iss": g.h.AS1.Issuer(), "sub": jags[0].AgentID}) ||
		!reflect.DeepEqual(v["aud"], []any{as2TestIssuer + "/mcp"}) {
		t.Fatalf("gateway token claims = %v, want act AS-1 and %s, client_id %s, and aud the vMCP", v, jags[0].AgentID, identitybroker.BrokerClientID)
	}
}

// TestBuildIdentityBrokerUnboundGatewayIsUnavailable shows a gateway whose
// AS-2 is not bound, because startup set up no AS-2 or shutdown unbound it,
// refuses calls as unavailable before asking AS-1 for an ID-JAG.
func TestBuildIdentityBrokerUnboundGatewayIsUnavailable(t *testing.T) {
	t.Run("no AS-2", func(t *testing.T) {
		g := buildWithHarnessAS1(t, false, func(cfg *Config) {
			cfg.IdentityBrokerGatewayServers = []string{gatewayTestServer}
			cfg.MCPAuthority = nil
		})
		if g.built.identityBrokerAS2 != nil {
			t.Fatal("startup set up AS-2 without a protected route")
		}
		s, cred, _ := g.open(t)
		_, err := g.built.IdentityBroker.AuthorizeCall(t.Context(), g.call(s, cred))
		wantBrokerError(t, err, identitybroker.CodeUnavailable, "")
		if n := g.as1.Count(identityharness.KindIDJAG); n != 0 {
			t.Fatalf("sent %d ID-JAG exchanges, want none", n)
		}
	})
	t.Run("unbound at shutdown", func(t *testing.T) {
		g := buildWithHarnessAS1(t, true, func(cfg *Config) { cfg.IdentityBrokerGatewayServers = []string{gatewayTestServer} })
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
	g := buildWithHarnessAS1(t, true, func(cfg *Config) { cfg.IdentityBrokerGatewayServers = []string{gatewayTestServer} })
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

// TestBuildConfiguresNoGatewayInProduction shows that without the test-only
// gateway servers, which production never sets, the identity broker has no
// gateway even with AS-2 set up: a session holding a grant is denied without
// an ID-JAG request.
func TestBuildConfiguresNoGatewayInProduction(t *testing.T) {
	g := buildWithHarnessAS1(t, false, nil)
	if g.built.identityBrokerAS2 == nil {
		t.Fatal("startup set up no AS-2")
	}
	s, cred, _ := g.open(t)
	_, err := g.built.IdentityBroker.AuthorizeCall(t.Context(), g.call(s, cred))
	wantBrokerError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied)
	if n := g.as1.Count(identityharness.KindIDJAG); n != 0 {
		t.Fatalf("sent %d ID-JAG exchanges, want none", n)
	}
}

// TestIdentityBrokerTokensStayOutOfSessionsAndLogs authorizes a call and a
// refused one, then runs a prompt, and looks for every token the call
// involved in the session's transcript and events, the diagnostics, the
// environment, the errors, and the broker's records (#2535 done-when 8). Model and
// tool content cannot carry the tokens until #2536 wires calls through the
// gateway.
func TestIdentityBrokerTokensStayOutOfSessionsAndLogs(t *testing.T) {
	diag := &recordingDiag{}
	g := buildWithHarnessAS1(t, true, func(cfg *Config) {
		cfg.IdentityBrokerGatewayServers = []string{gatewayTestServer}
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
	for _, r := range g.as1.Requests() {
		secrets = append(secrets, r.IssuedToken, r.Assertion)
		fmt.Fprintf(&out, "%s\n", r.ClientAssertion)
	}
	secrets = slices.Compact(slices.Sorted(slices.Values(slices.DeleteFunc(secrets, func(s string) bool { return s == "" }))))
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
	leaks := func(text string) bool {
		for _, secret := range secrets {
			if strings.Contains(text, secret) {
				return true
			}
		}
		return false
	}
	// Positive control: a captured token logged through a diagnostics sink,
	// formatted as the scan formats diagnostics, is found.
	control := &recordingDiag{}
	control.Log(t.Context(), port.LevelWarn, "planted", "token", auth.GatewayToken.Value())
	cMsgs, cAttrs := control.snapshot()
	if !leaks(out.String() + fmt.Sprintf("%v %+v\n", cMsgs, cAttrs)) {
		t.Fatal("the scan missed a gateway token planted in the diagnostics")
	}
	if leaks(out.String()) {
		t.Fatal("a transcript, event, diagnostic, error, record, client assertion, the environment, or a formatted authorization carries a token")
	}
}

package mcpbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	mcpadapter "github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/agentcredential"
	"github.com/stacklok/mecatl/internal/identitybroker"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

const (
	gatewayTestSession = "gateway-session"
	gatewayTestTool    = "mcp__github__create"
	gatewayTestRouted  = "github.create"
	gatewayTestResult  = "called " + gatewayTestRouted
	gatewayTestSerial  = int64(7)
	gatewayTestArgs    = `{"a":1}`
)

// fakeAuthorizer records AuthorizeCall requests and answers from script:
// a nil entry, or one past the end, allows with a fresh token and attempt.
type fakeAuthorizer struct {
	tokenPrefix string
	mu          sync.Mutex
	script      []error
	requests    []identitybroker.AuthorizeCallRequest
}

func (f *fakeAuthorizer) AuthorizeCall(_ context.Context, req identitybroker.AuthorizeCallRequest) (identitybroker.Authorization, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	n := len(f.requests)
	if n <= len(f.script) && f.script[n-1] != nil {
		return identitybroker.Authorization{}, f.script[n-1]
	}
	return identitybroker.Authorization{
		GatewayToken: identitybroker.NewToken(f.tokenPrefix + strconv.Itoa(n)),
		ExpiresAt:    time.Now().Add(time.Minute),
		AttemptID:    "att_" + strconv.Itoa(n),
	}, nil
}

func (f *fakeAuthorizer) calls() []identitybroker.AuthorizeCallRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// fakeCredentials holds credentials by key and records every lookup's renew
// flag. It has no way to open, find, or register: a lookup only reads.
type fakeCredentials struct {
	mu      sync.Mutex
	held    map[agentcredential.Key]agentcredential.Credential
	lookups []bool
}

func (f *fakeCredentials) Credential(_ context.Context, key agentcredential.Key, renew bool) (agentcredential.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups = append(f.lookups, renew)
	cred, ok := f.held[key]
	if !ok {
		return agentcredential.Credential{}, agentcredential.ErrNoCredential
	}
	return cred, nil
}

func (f *fakeCredentials) renewFlags() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.lookups)
}

// testGateway is the gateway stand-in as a Gateway.
type testGateway struct {
	endpoint string
	client   *http.Client
}

func (g testGateway) Endpoint() string         { return g.endpoint }
func (g testGateway) HTTPClient() *http.Client { return g.client }
func (testGateway) Route(backend, name string) (string, string, error) {
	return embeddedGateway{}.Route(backend, name)
}

type wireRequest struct {
	header http.Header
	body   []byte
}

// gatewayTestTransport sits below the gateway call's own transport: it sees
// requests as written and responses as received.
type gatewayTestTransport struct {
	base http.RoundTripper

	mu          sync.Mutex
	onToolsCall func()
	rewrite     func([]byte) []byte
	// respond, when it returns a response, answers a request in place of
	// the gateway.
	respond func(req *http.Request, body []byte) *http.Response
	sent    []wireRequest
}

func (w *gatewayTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	w.mu.Lock()
	w.sent = append(w.sent, wireRequest{header: req.Header.Clone(), body: body})
	hook, rewrite, respond := w.onToolsCall, w.rewrite, w.respond
	if hook != nil && bytes.Contains(body, []byte(`"method":"tools/call"`)) {
		w.onToolsCall = nil
	} else {
		hook = nil
	}
	w.mu.Unlock()
	if hook != nil {
		hook()
	}
	if respond != nil {
		if resp := respond(req, body); resp != nil {
			resp.Request = req
			return resp, nil
		}
	}
	resp, err := w.base.RoundTrip(req)
	if err != nil || rewrite == nil || resp.Header.Get("Content-Type") != "application/json" {
		return resp, err
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	raw = rewrite(raw)
	resp.Body, resp.ContentLength = io.NopCloser(bytes.NewReader(raw)), int64(len(raw))
	resp.Header.Del("Content-Length")
	return resp, nil
}

func (w *gatewayTestTransport) setOnToolsCall(fn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onToolsCall = fn
}

func (w *gatewayTestTransport) requests() []wireRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.sent)
}

type gatewayFixture struct {
	g          *identityharness.Gateway
	wire       *gatewayTestTransport
	auth       *fakeAuthorizer
	creds      *fakeCredentials
	diag       *recordingBrokerDiagnostics
	runtime    *Runtime
	attachment *Attachment
	agentToken string
}

type gatewayFixtureOptions struct {
	servers     []string
	noGateway   bool
	tokenPrefix string
	agentToken  string
}

func newGatewayFixture(t *testing.T, opts gatewayFixtureOptions) *gatewayFixture {
	t.Helper()
	if opts.servers == nil {
		opts.servers = []string{"github"}
	}
	if opts.tokenPrefix == "" {
		opts.tokenPrefix = "gateway-token-"
	}
	if opts.agentToken == "" {
		opts.agentToken = "agent-credential"
	}
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a gateway call must not reach the MCP broker's OAuth token endpoint")
	}))
	t.Cleanup(tokenServer.Close)
	harness := newProtectedHarness(t, tokenServer)
	harness.runtime.catalogue.routes[0].broker = true

	h := identityharness.New(t, identityharness.Options{})
	f := &gatewayFixture{
		g:    h.StartGateway(t, identityharness.GatewayOptions{}),
		auth: &fakeAuthorizer{tokenPrefix: opts.tokenPrefix},
		creds: &fakeCredentials{held: map[agentcredential.Key]agentcredential.Credential{
			{RootSessionID: gatewayTestSession, AgentSessionID: gatewayTestSession}: {
				BrokerSessionID: "bs_test",
				Agent:           identitybroker.AgentRef{Subject: "spiffe://example.org/agent/shared/inst/inc_test"},
				Owner:           identitybroker.Owner{Issuer: "https://login.example.com", Subject: "alice"},
				Token:           identitybroker.NewToken(opts.agentToken),
				ExpiresAt:       time.Now().Add(time.Hour),
			},
		}},
		diag:       &recordingBrokerDiagnostics{},
		runtime:    harness.runtime,
		agentToken: opts.agentToken,
	}
	f.wire = &gatewayTestTransport{base: h.HTTPClient().Transport}
	harness.runtime.diag = f.diag
	if !opts.noGateway {
		WithGatewayCalls(
			testGateway{endpoint: f.g.URL(), client: &http.Client{Transport: f.wire, Timeout: 10 * time.Second}},
			GatewayCalls{Authorizer: f.auth, Credentials: f.creds, Servers: opts.servers},
		)(harness.runtime)
	}
	f.attachment, _ = attach(t, harness.runtime, gatewayTestSession)
	return f
}

// runContext is the context of a tool call by the session's main agent.
func runContext(ctx context.Context, root, acting string) context.Context {
	return port.WithRunAttemptContext(port.WithRootSessionID(ctx, session.SessionID(root)), session.SessionID(acting), gatewayTestSerial)
}

func (f *gatewayFixture) execute(ctx context.Context, t *testing.T, callID, args string) (session.ToolResult, error) {
	t.Helper()
	return toolByName(t, f.attachment, gatewayTestTool).Execute(ctx, session.NewToolCall(session.ToolCallID(callID), gatewayTestTool, json.RawMessage(args)), tool.Environment{})
}

func mainAgent(t *testing.T) context.Context {
	return runContext(t.Context(), gatewayTestSession, gatewayTestSession)
}

func (f *gatewayFixture) toolsCalls() []identityharness.GatewayCall {
	var out []identityharness.GatewayCall
	for _, c := range f.g.Calls() {
		if c.Method == "tools/call" {
			out = append(out, c)
		}
	}
	return out
}

// claim claims op with the test arguments in the test session's share of the
// send-once record.
func (f *gatewayFixture) claim(t *testing.T, op string) error {
	t.Helper()
	return f.runtime.gatewayCalls.SendOnce.Claim(t.Context(), gatewayTestSession, op, testHash(t, gatewayTestArgs))
}

func testHash(t *testing.T, args string) string {
	t.Helper()
	_, sum, err := argumentsSHA256(json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

// wantRow checks res is the outcome row o, returned as a tool error.
func wantRow(t *testing.T, res session.ToolResult, err error, o gatewayOutcome) {
	t.Helper()
	if err != nil {
		t.Fatalf("Execute error = %v, want the %s row", err, o.code)
	}
	if !res.IsError || res.Content != o.text {
		t.Fatalf("result = %+v, want tool error %q", res, o.text)
	}
}

func wantResult(t *testing.T, res session.ToolResult, err error, content string) {
	t.Helper()
	if err != nil || res.IsError || res.Content != content {
		t.Fatalf("result = %+v, %v; want %q", res, err, content)
	}
}

func TestGatewayCallDenialBlocksDispatch(t *testing.T) {
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	f.auth.script = []error{identitybroker.ReasonError(identitybroker.ReasonDenied, "no")}
	res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantRow(t, res, err, outcomeDenied)
	if calls := f.g.Calls(); len(calls) != 0 {
		t.Fatalf("gateway received %d requests after a denial", len(calls))
	}
}

func TestGatewayCallLogsTheBrokerRefusal(t *testing.T) {
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	detail := "identitybroker: the authorization server refused the request: HTTP 400 invalid_request (sub_id_missing)"
	f.auth.script = []error{identitybroker.NewError(identitybroker.CodeInternal, "", detail)}
	res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantRow(t, res, err, outcomeInternal)
	if strings.Contains(res.Content, "sub_id_missing") {
		t.Fatalf("result = %q, want the refusal reason only in diagnostics", res.Content)
	}
	if got := f.diag.String(); !strings.Contains(got, "broker_codeINTERNAL") || !strings.Contains(got, "broker_detail"+detail) {
		t.Fatalf("diagnostics = %s, want the broker's code and detail", got)
	}
}

func TestGatewayCallWithoutAgentCredentialIsRefused(t *testing.T) {
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	for name, ctx := range map[string]context.Context{
		"no run scope":       t.Context(),
		"child agent":        runContext(t.Context(), gatewayTestSession, "child-session"),
		"unregistered agent": runContext(t.Context(), "other-root", "other-root"),
	} {
		res, err := f.execute(ctx, t, "call-"+name, gatewayTestArgs)
		wantRow(t, res, err, outcomeIdentityUnavailable)
	}
	if n := len(f.auth.calls()); n != 0 {
		t.Fatalf("AuthorizeCall called %d times without a credential", n)
	}
	if calls := f.g.Calls(); len(calls) != 0 {
		t.Fatalf("gateway received %d requests", len(calls))
	}
}

func TestGatewayCallUnownedBrokerSessionRefused(t *testing.T) {
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	f.auth.script = []error{identitybroker.ReasonError(identitybroker.ReasonSessionOwned, "another owner")}
	res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantRow(t, res, err, outcomeDenied)
	if calls := f.g.Calls(); len(calls) != 0 {
		t.Fatalf("gateway received %d requests", len(calls))
	}
}

func TestGatewayCallSendsTokenAndMeta(t *testing.T) {
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantResult(t, res, err, gatewayTestResult)

	op := operationID(gatewayTestSession, gatewayTestSession, "call-1")
	reqs := f.auth.calls()
	want := identitybroker.AuthorizeCallRequest{
		SessionID: "bs_test", OperationID: op, RunID: "run-7",
		RunOwner: identitybroker.Owner{Issuer: "https://login.example.com", Subject: "alice"},
		Method:   identitybroker.MethodToolsCall, Server: "github", Tool: gatewayTestRouted,
		ArgumentsSHA256: testHash(t, gatewayTestArgs),
	}
	if len(reqs) != 1 || reqs[0].Credential.Value() != f.agentToken {
		t.Fatalf("AuthorizeCall requests = %+v", reqs)
	}
	got := reqs[0]
	got.Credential = identitybroker.Token{}
	if got != want {
		t.Fatalf("AuthorizeCall request = %+v, want %+v", got, want)
	}

	calls := f.toolsCalls()
	if len(calls) != 1 || calls[0].Tool != gatewayTestRouted || string(calls[0].Arguments) != gatewayTestArgs ||
		calls[0].Meta[identityharness.MetaOperation] != op || calls[0].Meta[identityharness.MetaAttempt] != "att_1" {
		t.Fatalf("tools/call = %+v", calls)
	}
	for _, c := range f.g.Calls() {
		if c.Method != http.MethodDelete && c.Meta[identityharness.MetaOperation] != op {
			t.Fatalf("message %+v lacks the operation _meta", c)
		}
	}
	for _, r := range f.wire.requests() {
		if r.header.Get("Authorization") != "Bearer gateway-token-1" {
			t.Fatalf("request sent with Authorization %q", r.header.Get("Authorization"))
		}
	}
	// The call is claimed: it is never sent again.
	if err := f.claim(t, op); !errors.Is(err, ErrAlreadySent) {
		t.Fatalf("Claim after a completed call = %v, want ErrAlreadySent", err)
	}
}

// changeFirstAttempts makes the first n attempts' wire see arguments other
// than the authorized ones.
func changeFirstAttempts(f *gatewayFixture, n int) {
	var mu sync.Mutex
	f.runtime.sendGateway = func(ctx context.Context, req mcpadapter.GatewayCallRequest) mcpadapter.GatewayAttempt {
		mu.Lock()
		change := n > 0
		n--
		mu.Unlock()
		if change {
			req.HashArguments = func(json.RawMessage) (string, error) { return strings.Repeat("0", 64), nil }
		}
		return mcpadapter.SendGatewayCall(ctx, req)
	}
}

func TestGatewayCallChangedAfterAuthorizationIsRefusedAndNotSent(t *testing.T) {
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	changeFirstAttempts(f, 1)
	res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantRow(t, res, err, outcomeInternal)
	if n := len(f.toolsCalls()); n != 0 {
		t.Fatalf("%d tools/call sent", n)
	}
	if n := len(f.auth.calls()); n != 1 {
		t.Fatalf("AuthorizeCall called %d times, want 1", n)
	}
	op := operationID(gatewayTestSession, gatewayTestSession, "call-1")
	if err := f.claim(t, op); err != nil {
		t.Fatalf("Claim after a call never sent = %v, want released", err)
	}
}

func TestGatewayCallRetriesOneRejectedToken(t *testing.T) {
	for _, variant := range []string{"handshake", "call"} {
		t.Run(variant, func(t *testing.T) {
			f := newGatewayFixture(t, gatewayFixtureOptions{})
			if variant == "handshake" {
				f.g.RejectNextToken()
			} else {
				f.wire.setOnToolsCall(f.g.RejectNextToken)
			}
			res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
			wantResult(t, res, err, gatewayTestResult)
			reqs := f.auth.calls()
			if len(reqs) != 2 || reqs[0].RejectedAttemptID != "" || reqs[1].RejectedAttemptID != "att_1" {
				t.Fatalf("AuthorizeCall requests = %+v, want a retry reporting att_1", reqs)
			}
			retry := reqs[1]
			retry.RejectedAttemptID = ""
			if retry != reqs[0] {
				t.Fatalf("retry = %+v, want the call's own fields %+v", retry, reqs[0])
			}
			var answered []string
			for _, c := range f.toolsCalls() {
				if c.Status == http.StatusOK {
					answered = append(answered, c.Meta[identityharness.MetaAttempt])
				}
			}
			if !slices.Equal(answered, []string{"att_2"}) {
				t.Fatalf("answered tools/call attempts = %v, want [att_2]", answered)
			}
		})
	}
}

func TestGatewayCallFailsOnSecondRejectedToken(t *testing.T) {
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	f.g.RejectTokens(true)
	res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantRow(t, res, err, outcomeTokenRejected)
	if n := len(f.auth.calls()); n != 2 {
		t.Fatalf("AuthorizeCall called %d times, want 2", n)
	}
	f.g.RejectTokens(false)
	res, err = f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantResult(t, res, err, gatewayTestResult)
}

func TestGatewayCallSessionNotFoundIsNotSent(t *testing.T) {
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	f.wire.setOnToolsCall(f.g.ForgetSessions)
	res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantRow(t, res, err, outcomeSessionNotFound)
	// Not counted as sent: the same call goes through when retried.
	res, err = f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantResult(t, res, err, gatewayTestResult)
}

func TestGatewayCallURLElicitationFailsClosedWithLink(t *testing.T) {
	for link, want := range map[string]string{
		"https://gateway.example/connect/github": "MCP gateway call not run: connect your github account at the gateway, then retry: https://gateway.example/connect/github (code connection_required).",
		"http://gateway.example/connect/github":  "MCP gateway call not run: connect your github account at the gateway, then retry (code connection_required).",
	} {
		f := newGatewayFixture(t, gatewayFixtureOptions{})
		f.g.MissingConnection("github", link)
		res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
		wantRow(t, res, err, gatewayOutcome{code: "connection_required", text: want})
		op := operationID(gatewayTestSession, gatewayTestSession, "call-1")
		if err := f.claim(t, op); err != nil {
			t.Fatalf("Claim after a refused call = %v, want released", err)
		}
	}
}

func TestGatewayCallDroppedAfterWriteIsUnknownAndNotReplayed(t *testing.T) {
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	f.g.DropAfterRequest("github")
	res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantRow(t, res, err, outcomeUnknown)
	res, err = f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantRow(t, res, err, outcomeAlreadySent)
	if n := len(f.toolsCalls()); n != 1 {
		t.Fatalf("gateway saw %d tools/call, want 1", n)
	}
}

func TestGatewayCallPassesResultThroughAfterDecliningARequest(t *testing.T) {
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	f.g.ElicitDuringCall("github")
	res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantResult(t, res, err, gatewayTestResult+"\n\n"+gatewayDeclinedNote)
	if n := len(f.toolsCalls()); n != 1 {
		t.Fatalf("gateway saw %d tools/call, want 1", n)
	}
	replies := 0
	for _, c := range f.g.Calls() {
		if c.Reply {
			replies++
		}
	}
	if replies != 1 {
		t.Fatalf("gateway saw %d replies, want the one decline", replies)
	}
	op := operationID(gatewayTestSession, gatewayTestSession, "call-1")
	if err := f.claim(t, op); !errors.Is(err, ErrAlreadySent) {
		t.Fatalf("Claim = %v, want the call counted as sent", err)
	}
}

func TestGatewayCallRefusesArgumentsOutsideIJSON(t *testing.T) {
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	res, err := f.execute(mainAgent(t), t, "call-1", `{"a":9007199254740993}`)
	wantRow(t, res, err, outcomeRefusedArguments)
	if n := len(f.auth.calls()); n != 0 {
		t.Fatalf("AuthorizeCall called %d times", n)
	}
	if n := len(f.creds.renewFlags()); n != 0 {
		t.Fatalf("credential looked up %d times", n)
	}
}

func TestGatewayCallRenewsOnSVIDExpiredOnce(t *testing.T) {
	expired := identitybroker.ReasonError(identitybroker.ReasonSVIDExpired, "expired")
	t.Run("renewed", func(t *testing.T) {
		f := newGatewayFixture(t, gatewayFixtureOptions{})
		f.auth.script = []error{expired}
		res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
		wantResult(t, res, err, gatewayTestResult)
		if got := f.creds.renewFlags(); !slices.Equal(got, []bool{false, true}) {
			t.Fatalf("credential lookups renew = %v, want [false true]", got)
		}
	})
	t.Run("still expired", func(t *testing.T) {
		f := newGatewayFixture(t, gatewayFixtureOptions{})
		f.auth.script = []error{expired, expired}
		res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
		wantRow(t, res, err, outcomeSVIDExpired)
		if got := f.creds.renewFlags(); !slices.Equal(got, []bool{false, true}) {
			t.Fatalf("credential lookups renew = %v, want one renewal", got)
		}
		if n := len(f.auth.calls()); n != 2 {
			t.Fatalf("AuthorizeCall called %d times, want 2", n)
		}
	})
}

func TestGatewayCallClosedSessionFailsWithClosingReason(t *testing.T) {
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	f.auth.script = []error{identitybroker.ReasonError(identitybroker.ReasonSessionClosed, "closed")}
	res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantRow(t, res, err, outcomeSessionClosed)
	// The credentials are only read, never renewed; the fakes cannot open,
	// find, or register, so nothing reopened the session.
	if got := f.creds.renewFlags(); !slices.Equal(got, []bool{false}) {
		t.Fatalf("credential lookups renew = %v, want one plain lookup", got)
	}
	if n := len(f.auth.calls()); n != 1 {
		t.Fatalf("AuthorizeCall called %d times, want 1", n)
	}
	if calls := f.g.Calls(); len(calls) != 0 {
		t.Fatalf("gateway received %d requests", len(calls))
	}
}

func TestGatewayOutcomeRows(t *testing.T) {
	reason := func(r identitybroker.Reason) error { return identitybroker.ReasonError(r, "") }
	for _, c := range []struct {
		err  error
		want string
	}{
		{reason(identitybroker.ReasonNoGrant), "no_grant"},
		{reason(identitybroker.ReasonSessionClosed), "session_closed"},
		{reason(identitybroker.ReasonSignedOut), "signed_out"},
		{reason(identitybroker.ReasonUpstreamRevoked), "upstream_revoked"},
		{reason(identitybroker.ReasonOwnerActionRequired), "owner_action_required"},
		{reason(identitybroker.ReasonConsentRequired), "consent_required"},
		{reason(identitybroker.ReasonDenied), "denied"},
		{reason(identitybroker.ReasonSVIDExpired), "svid_expired"},
		{identitybroker.NewError(identitybroker.CodeUnavailable, "", ""), "unavailable"},
		{context.DeadlineExceeded, "unavailable"},
		{identitybroker.NewError(identitybroker.CodeInternal, "", ""), "internal"},
		{errors.New("unexpected"), "internal"},
		{errNotIJSON, "refused_arguments"},
		{ErrAlreadySent, "already_sent"},
		{errSendOnceFull, "send_once_full"},
		{agentcredential.ErrNoCredential, "identity_unavailable"},
		{agentcredential.ErrDropped, "identity_unavailable"},
	} {
		if got := outcomeForAuthorization(c.err); got.code != c.want || !strings.HasSuffix(got.text, "(code "+c.want+").") {
			t.Errorf("outcomeForAuthorization(%v) = %+v, want %s", c.err, got, c.want)
		}
	}

	const link = "https://gateway.example/connect"
	for _, c := range []struct {
		name    string
		last    mcpadapter.GatewayAttempt
		retried bool
		want    gatewayOutcome
	}{
		{"connection required with link", mcpadapter.GatewayAttempt{Outcome: mcpadapter.GatewayAttemptRefused, Rejection: mcpadapter.GatewayRejectionURLElicitation, ConnectURL: link}, false, outcomeConnectionRequired("github", link)},
		{"connection required without link", mcpadapter.GatewayAttempt{Outcome: mcpadapter.GatewayAttemptRefused, Rejection: mcpadapter.GatewayRejectionURLElicitation}, false, outcomeConnectionRequired("github", "")},
		{"session not found", mcpadapter.GatewayAttempt{Outcome: mcpadapter.GatewayAttemptRefused, Rejection: mcpadapter.GatewayRejectionSessionNotFound}, false, outcomeSessionNotFound},
		{"token rejected twice", mcpadapter.GatewayAttempt{Outcome: mcpadapter.GatewayAttemptRefused, Rejection: mcpadapter.GatewayRejectionInvalidToken}, true, outcomeTokenRejected},
		{"changed after authorization", mcpadapter.GatewayAttempt{Outcome: mcpadapter.GatewayAttemptNotSent, Rejection: mcpadapter.GatewayRejectionChanged}, false, outcomeInternal},
		{"authorization expired", mcpadapter.GatewayAttempt{Outcome: mcpadapter.GatewayAttemptNotSent, Rejection: mcpadapter.GatewayRejectionExpired}, false, outcomeUnavailable},
		{"handshake failed", mcpadapter.GatewayAttempt{Outcome: mcpadapter.GatewayAttemptNotSent}, false, outcomeUnavailable},
		{"outcome unknown", mcpadapter.GatewayAttempt{Outcome: mcpadapter.GatewayAttemptUnknown}, false, outcomeUnknown},
	} {
		if got := outcomeForAttempts("github", c.last, c.retried); got != c.want {
			t.Errorf("%s: outcomeForAttempts = %+v, want %+v", c.name, got, c.want)
		}
	}
	if got := outcomeConnectionRequired("github", link); !strings.Contains(got.text, link) || got.code != "connection_required" {
		t.Errorf("connection_required with link = %+v", got)
	}

	// Through the broker: no_grant dispatches nothing.
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	f.auth.script = []error{reason(identitybroker.ReasonNoGrant)}
	res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantRow(t, res, err, outcomeNoGrant)
	if calls := f.g.Calls(); len(calls) != 0 {
		t.Fatalf("gateway received %d requests after no_grant", len(calls))
	}
}

// unauthorized is a 401 answer carrying challenge, or no challenge when it
// is empty.
func unauthorized(challenge string) *http.Response {
	header := http.Header{}
	if challenge != "" {
		header.Set("WWW-Authenticate", challenge)
	}
	return &http.Response{StatusCode: http.StatusUnauthorized, Status: "401 Unauthorized", Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: header, Body: io.NopCloser(strings.NewReader(""))}
}

func TestGatewayCall401WithoutInvalidTokenIsNotRetried(t *testing.T) {
	op := operationID(gatewayTestSession, gatewayTestSession, "call-1")
	for name, challenge := range map[string]string{
		"insufficient_scope": `Bearer error="insufficient_scope", scope="tools"`,
		"no challenge":       "",
	} {
		t.Run(name+" on the call", func(t *testing.T) {
			f := newGatewayFixture(t, gatewayFixtureOptions{})
			f.wire.respond = func(_ *http.Request, body []byte) *http.Response {
				if bytes.Contains(body, []byte(`"method":"tools/call"`)) {
					return unauthorized(challenge)
				}
				return nil
			}
			res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
			wantRow(t, res, err, outcomeUnknown)
			if n := len(f.auth.calls()); n != 1 {
				t.Fatalf("AuthorizeCall called %d times, want 1 (no retry)", n)
			}
			if err := f.claim(t, op); !errors.Is(err, ErrAlreadySent) {
				t.Fatalf("Claim = %v, want the call kept as sent", err)
			}
		})
		t.Run(name+" on the handshake", func(t *testing.T) {
			f := newGatewayFixture(t, gatewayFixtureOptions{})
			f.wire.respond = func(_ *http.Request, body []byte) *http.Response {
				if bytes.Contains(body, []byte(`"method":"initialize"`)) {
					return unauthorized(challenge)
				}
				return nil
			}
			res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
			wantRow(t, res, err, outcomeUnavailable)
			if n := len(f.auth.calls()); n != 1 {
				t.Fatalf("AuthorizeCall called %d times, want 1 (no retry)", n)
			}
			if n := len(f.toolsCalls()); n != 0 {
				t.Fatalf("%d tools/call sent", n)
			}
			if err := f.claim(t, op); err != nil {
				t.Fatalf("Claim = %v, want the never-sent call released", err)
			}
		})
	}
}

func TestGatewayCallNeverFollowsARedirect(t *testing.T) {
	var reached atomic.Int64
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Add(1) }))
	t.Cleanup(other.Close)
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	gateway, err := url.Parse(f.g.URL())
	if err != nil {
		t.Fatal(err)
	}
	f.wire.respond = func(req *http.Request, body []byte) *http.Response {
		if req.URL.Host != gateway.Host || !bytes.Contains(body, []byte(`"method":"initialize"`)) {
			return nil
		}
		return &http.Response{StatusCode: http.StatusTemporaryRedirect, Status: "307 Temporary Redirect", Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: http.Header{"Location": {other.URL + identityharness.GatewayMCPPath}}, Body: io.NopCloser(strings.NewReader(""))}
	}
	res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantRow(t, res, err, outcomeUnavailable)
	if n := reached.Load(); n != 0 {
		t.Fatalf("the redirect target received %d requests", n)
	}
	if n := len(f.toolsCalls()); n != 0 {
		t.Fatalf("%d tools/call sent", n)
	}
	if err := f.claim(t, operationID(gatewayTestSession, gatewayTestSession, "call-1")); err != nil {
		t.Fatalf("Claim = %v, want the never-sent call released", err)
	}
}

func TestSendOnceRefusesSecondSendIncludingAcrossRetry(t *testing.T) {
	record := NewMemorySendOnceRecord(2)
	ctx := t.Context()
	if err := record.Claim(ctx, "root", "op_1", "h"); err != nil {
		t.Fatal(err)
	}
	if err := record.Claim(ctx, "root", "op_1", "h"); !errors.Is(err, ErrAlreadySent) {
		t.Fatalf("second Claim = %v, want ErrAlreadySent", err)
	}
	if err := record.Claim(ctx, "root", "op_2", "h"); err != nil {
		t.Fatal(err)
	}
	if err := record.Claim(ctx, "root", "op_3", "h"); !errors.Is(err, errSendOnceFull) {
		t.Fatalf("Claim at the limit = %v, want it refused", err)
	}
	if err := record.Claim(ctx, "root", "op_1", "h"); !errors.Is(err, ErrAlreadySent) {
		t.Fatalf("Claim after the limit = %v, want op_1 still claimed", err)
	}
	if err := record.Release(ctx, "root", "op_1", "h"); err != nil {
		t.Fatal(err)
	}
	if err := record.Claim(ctx, "root", "op_1", "h"); err != nil {
		t.Fatalf("Claim after Release = %v", err)
	}

	// Through the broker: the token retry is one claim, and a later Execute
	// of the same call is refused without reaching the gateway.
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	f.g.RejectNextToken()
	res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantResult(t, res, err, gatewayTestResult)
	res, err = f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantRow(t, res, err, outcomeAlreadySent)
	if n := len(f.toolsCalls()); n != 1 {
		t.Fatalf("gateway saw %d tools/call, want 1", n)
	}
}

func TestSendOnceIsBoundedPerRootSession(t *testing.T) {
	record := NewMemorySendOnceRecord(1)
	ctx := t.Context()
	if err := record.Claim(ctx, "root-a", "op_1", "h"); err != nil {
		t.Fatal(err)
	}
	if err := record.Claim(ctx, "root-a", "op_2", "h"); !errors.Is(err, errSendOnceFull) {
		t.Fatalf("Claim past root-a's share = %v, want errSendOnceFull", err)
	}
	// One root filling its share does not block another.
	if err := record.Claim(ctx, "root-b", "op_1", "h"); err != nil {
		t.Fatalf("root-b Claim = %v, want its own share", err)
	}
	if err := record.Forget(ctx, "root-a"); err != nil {
		t.Fatal(err)
	}
	if err := record.Claim(ctx, "root-a", "op_2", "h"); err != nil {
		t.Fatalf("Claim after Forget = %v, want root-a's share freed", err)
	}
	if err := record.Claim(ctx, "root-b", "op_1", "h"); !errors.Is(err, ErrAlreadySent) {
		t.Fatalf("root-b Claim after root-a's Forget = %v, want ErrAlreadySent", err)
	}

	// Through the broker: a full share refuses the call before anything is
	// sent.
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	f.runtime.gatewayCalls.SendOnce = NewMemorySendOnceRecord(1)
	if err := f.runtime.gatewayCalls.SendOnce.Claim(ctx, gatewayTestSession, "op_other", "h"); err != nil {
		t.Fatal(err)
	}
	res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantRow(t, res, err, outcomeSendOnceFull)
	if calls := f.g.Calls(); len(calls) != 0 {
		t.Fatalf("gateway received %d requests with the record full", len(calls))
	}
}

func TestSendOnceDropsOnlyCompletedClaimsWhenFull(t *testing.T) {
	record := NewMemorySendOnceRecord(2)
	ctx := t.Context()
	for _, op := range []string{"op_unknown", "op_1"} {
		if err := record.Claim(ctx, "root", op, "h"); err != nil {
			t.Fatal(err)
		}
	}
	if err := record.Complete(ctx, "root", "op_1", "h"); err != nil {
		t.Fatal(err)
	}
	if err := record.Claim(ctx, "root", "op_2", "h"); err != nil {
		t.Fatalf("Claim with a completed claim held = %v, want it dropped", err)
	}
	if err := record.Claim(ctx, "root", "op_3", "h"); !errors.Is(err, errSendOnceFull) {
		t.Fatalf("Claim with no completed claim held = %v, want errSendOnceFull", err)
	}
	if err := record.Complete(ctx, "root", "op_2", "h"); err != nil {
		t.Fatal(err)
	}
	if err := record.Claim(ctx, "root", "op_3", "h"); err != nil {
		t.Fatal(err)
	}
	if err := record.Claim(ctx, "root", "op_unknown", "h"); !errors.Is(err, ErrAlreadySent) {
		t.Fatalf("Claim of the unknown call = %v, want it never dropped", err)
	}

	// Through the broker: a session sends more completed calls than its
	// share, while calls with an unknown outcome stay held and refused.
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	f.runtime.gatewayCalls.SendOnce = NewMemorySendOnceRecord(2)
	for _, id := range []string{"call-1", "call-2", "call-3", "call-4"} {
		res, err := f.execute(mainAgent(t), t, id, gatewayTestArgs)
		wantResult(t, res, err, gatewayTestResult)
	}
	f.g.DropAfterRequest("github")
	for _, id := range []string{"call-unknown-1", "call-unknown-2"} {
		res, err := f.execute(mainAgent(t), t, id, gatewayTestArgs)
		wantRow(t, res, err, outcomeUnknown)
	}
	sent := len(f.toolsCalls())
	res, err := f.execute(mainAgent(t), t, "call-unknown-1", gatewayTestArgs)
	wantRow(t, res, err, outcomeAlreadySent)
	res, err = f.execute(mainAgent(t), t, "call-5", gatewayTestArgs)
	wantRow(t, res, err, outcomeSendOnceFull)
	if n := len(f.toolsCalls()); n != sent {
		t.Fatalf("gateway saw %d more tools/call after the share filled with unknown outcomes", n-sent)
	}
}

func TestGatewayClaimsKeptOnCloseAndForgottenOnDelete(t *testing.T) {
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	f.g.DropAfterRequest("github")
	res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantRow(t, res, err, outcomeUnknown)
	op := operationID(gatewayTestSession, gatewayTestSession, "call-1")

	// Closing the attachment is not deleting the session: a resumed run is
	// still refused.
	if _, err := f.attachment.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.attachment, _ = attach(t, f.runtime, gatewayTestSession)
	res, err = f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantRow(t, res, err, outcomeAlreadySent)

	if _, err := f.runtime.DeleteSession(t.Context(), gatewayTestSession); err != nil {
		t.Fatal(err)
	}
	if err := f.claim(t, op); err != nil {
		t.Fatalf("Claim after the session was deleted = %v, want its claims forgotten", err)
	}
}

func TestGatewayCallThroughCallMcpWithQuery(t *testing.T) {
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	f.wire.rewrite = func(body []byte) []byte {
		return bytes.ReplaceAll(body, []byte(`"`+gatewayTestResult+`"`), []byte(`"{\"called\":\"`+gatewayTestRouted+`\",\"secret\":\"unprojected\"}"`))
	}
	query := &attachmentQueryTool{attachment: f.attachment}
	call := session.NewToolCall("query-1", "CallMcpWithQuery", json.RawMessage(`{"server":"github","tool":"create","args":{"a":1},"jq_filter":".called"}`))
	if _, required, err := query.RequestAuthorization(mainAgent(t), call); required || err != nil {
		t.Fatalf("query RequestAuthorization = required %v, %v", required, err)
	}
	res, err := query.Execute(mainAgent(t), call, tool.Environment{})
	if err != nil || res.IsError || !strings.Contains(res.Content, gatewayTestRouted) || strings.Contains(res.Content, "unprojected") {
		t.Fatalf("query result = %+v, %v; want the projected value only", res, err)
	}
	calls := f.toolsCalls()
	if len(calls) != 1 || calls[0].Meta[identityharness.MetaOperation] != operationID(gatewayTestSession, gatewayTestSession, "query-1") {
		t.Fatalf("tools/call = %+v", calls)
	}
}

func TestGatewayRouteNeedsNoOAuthAuthorization(t *testing.T) {
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	requester := toolByName(t, f.attachment, gatewayTestTool).(tool.AuthorizationRequester)
	call := session.NewToolCall("call-1", gatewayTestTool, json.RawMessage(gatewayTestArgs))
	if auth, required, err := requester.RequestAuthorization(mainAgent(t), call); required || err != nil || auth != (session.ExternalAuthorization{}) {
		t.Fatalf("RequestAuthorization = %+v, required %v, %v", auth, required, err)
	}
	f.attachment.logical.mu.Lock()
	transactions := len(f.attachment.logical.authorizations)
	f.attachment.logical.mu.Unlock()
	if transactions != 0 {
		t.Fatalf("%d OAuth authorizations started for a gateway route", transactions)
	}
	res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantResult(t, res, err, gatewayTestResult)
}

func TestGatewayOperationIDStableAcrossResume(t *testing.T) {
	op := operationID("root", "acting", "call-1")
	if op != operationID("root", "acting", "call-1") || !strings.HasPrefix(op, "op_") || len(op) != 35 {
		t.Fatalf("operationID = %q", op)
	}
	for _, other := range []string{operationID("root2", "acting", "call-1"), operationID("root", "acting2", "call-1"), operationID("root", "acting", "call-2")} {
		if other == op {
			t.Fatal("operationID ignores one of its inputs")
		}
	}

	f := newGatewayFixture(t, gatewayFixtureOptions{})
	f.g.DropAfterRequest("github")
	res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantRow(t, res, err, outcomeUnknown)
	// A resumed run reattaches and executes the same call again.
	f.attachment, _ = attach(t, f.runtime, gatewayTestSession)
	res, err = f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantRow(t, res, err, outcomeAlreadySent)
	reqs := f.auth.calls()
	if len(reqs) != 2 || reqs[0].OperationID != reqs[1].OperationID {
		t.Fatalf("AuthorizeCall requests = %+v, want the same operation", reqs)
	}
}

func TestBrokerRoutesWithoutGatewayCallsUnchanged(t *testing.T) {
	for name, opts := range map[string]gatewayFixtureOptions{
		"no gateway":           {noGateway: true},
		"server not a gateway": {servers: []string{"slack"}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newGatewayFixture(t, opts)
			wrapped := toolByName(t, f.attachment, gatewayTestTool)
			call := session.NewToolCall("call-1", gatewayTestTool, json.RawMessage(gatewayTestArgs))
			if _, required, err := wrapped.(tool.AuthorizationRequester).RequestAuthorization(mainAgent(t), call); !required || err != nil {
				t.Fatalf("RequestAuthorization = required %v, %v; want the OAuth bundle", required, err)
			}
			if _, err := wrapped.Execute(mainAgent(t), call, tool.Environment{}); !errors.Is(err, contract.ErrAuthorizationNotFound) {
				t.Fatalf("Execute = %v, want the OAuth route's ErrAuthorizationNotFound", err)
			}
			if n := len(f.auth.calls()); n != 0 {
				t.Fatalf("AuthorizeCall called %d times", n)
			}
			if calls := f.g.Calls(); len(calls) != 0 {
				t.Fatalf("gateway received %d requests", len(calls))
			}
		})
	}
}

func TestToolHiveProcessInstallsTheEmbeddedGateway(t *testing.T) {
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	t.Setenv("TMPDIR", t.TempDir())
	calls := GatewayCalls{Authorizer: &fakeAuthorizer{}, Credentials: &fakeCredentials{}, Servers: []string{"private"}}
	config := ToolHiveConfig{
		CallbackURL: as2TestCallbackURL, Profiles: []ToolHiveProfile{staticProtectedProfile("private")},
		BrokerClient: newAS2BrokerClient(t).config, GatewayCalls: &calls,
	}
	if _, err := NewToolHiveProcess(t.Context(), config); !errors.Is(err, ErrInvalidCatalogue) {
		t.Fatalf("NewToolHiveProcess without a gateway HTTP client = %v, want ErrInvalidCatalogue", err)
	}

	client := &http.Client{}
	var gotEndpoint string
	config.EmbeddedGatewayClient = func(vmcp http.Handler, endpoint string) (*http.Client, error) {
		if vmcp == nil {
			t.Error("no vMCP handler")
		}
		gotEndpoint = endpoint
		return client, nil
	}
	process, err := NewToolHiveProcess(t.Context(), config)
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	gw, ok := process.Runtime.gateway.(embeddedGateway)
	if !ok || gw.Endpoint() != as2Resource(as2TestIssuer) || gotEndpoint != gw.Endpoint() || process.GatewayEndpoint != gw.Endpoint() ||
		gw.Endpoint() != process.AS2.Resource || gw.HTTPClient() != client {
		t.Fatalf("gateway = %+v (endpoint passed %q, process endpoint %q), AS-2 resource %q", process.Runtime.gateway, gotEndpoint, process.GatewayEndpoint, process.AS2.Resource)
	}
	if server, routed, err := gw.Route("private", "mcp__private__echo"); server != "private" || routed != "private.echo" || err != nil {
		t.Fatalf("Route = %q, %q, %v", server, routed, err)
	}
	if !process.Runtime.gatewayRoutes("private") || process.Runtime.gatewayCalls.SendOnce == nil {
		t.Fatal("gateway calls not installed")
	}
}

func TestGatewayTokenNeverLeaks(t *testing.T) {
	const canary = "LEAKCANARY"
	paths := map[string]func(*gatewayFixture){
		"allow":           func(*gatewayFixture) {},
		"401 retry":       func(f *gatewayFixture) { f.wire.setOnToolsCall(f.g.RejectNextToken) },
		"unknown":         func(f *gatewayFixture) { f.g.DropAfterRequest("github") },
		"url elicitation": func(f *gatewayFixture) { f.g.MissingConnection("github", "https://gateway.example/connect") },
		"elicit during":   func(f *gatewayFixture) { f.g.ElicitDuringCall("github") },
	}
	for name, setup := range paths {
		t.Run(name, func(t *testing.T) {
			f := newGatewayFixture(t, gatewayFixtureOptions{tokenPrefix: "gw-" + canary + "-", agentToken: "agent-" + canary})
			setup(f)
			res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			exposed := []string{res.Content, f.diag.String(), fmt.Sprintf("%v %+v", res, res)}
			for _, p := range res.Parts {
				exposed = append(exposed, p.Text, string(p.Data))
			}
			exposed = append(exposed, os.Environ()...)
			for _, s := range exposed {
				if strings.Contains(s, canary) {
					t.Fatalf("a token leaked: %q", s)
				}
			}
			if !strings.Contains(f.diag.String(), diagnosticEventGatewayCall) {
				t.Fatalf("no gateway_call diagnostic: %s", f.diag.String())
			}
			// Positive control: the gateway got the token, in the
			// Authorization header only.
			sent := f.wire.requests()
			if len(sent) == 0 {
				t.Fatal("nothing sent")
			}
			for _, r := range sent {
				if !strings.HasPrefix(r.header.Get("Authorization"), "Bearer gw-"+canary+"-") {
					t.Fatalf("Authorization = %q, want the gateway token", r.header.Get("Authorization"))
				}
				if bytes.Contains(r.body, []byte(canary)) {
					t.Fatalf("token in a request body: %s", r.body)
				}
				for key, values := range r.header {
					if key != "Authorization" && strings.Contains(strings.Join(values, ","), canary) {
						t.Fatalf("token in header %s", key)
					}
				}
			}
		})
	}
	// A returned error never carries a token either.
	f := newGatewayFixture(t, gatewayFixtureOptions{tokenPrefix: "gw-" + canary + "-", agentToken: "agent-" + canary})
	ctx, cancel := context.WithCancel(mainAgent(t))
	cancel()
	_, err := f.execute(ctx, t, "call-1", gatewayTestArgs)
	if err == nil || strings.Contains(fmt.Sprintf("%v %+v", err, err), canary) {
		t.Fatalf("cancelled Execute error = %v", err)
	}
}

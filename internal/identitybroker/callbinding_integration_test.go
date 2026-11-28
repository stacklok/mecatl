package identitybroker_test

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp/callbinding"

	mcpadapter "github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker/mcpbrokertest"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/inprocesshttp"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// The tests in this file are implementation.md's "Test the per-call V" and
// "Test the binding at the vMCP" (credentials-and-lifecycle.md, steps (8),
// (8a), and (9), check 3) against the real AS-1, the real ToolHive AS-2, and
// the real vMCP, routing a real backend. Like the rest of the harness, none
// is parallel.

// bindingIDJAGLifetime caps the gateway tokens' lifetime, and
// bindingMaxCall is the vMCP's maximum call duration, which is also the
// grace after a token's exp in which it accepts the token's call's messages,
// as in production: short, so the tests can outlive them.
const (
	bindingIDJAGLifetime = 3 * time.Second
	bindingMaxCall       = 10 * time.Second
)

// calendarBackend is an anonymous MCP backend the vMCP routes as
// "calendar": echo answers at once, counting its runs; slow runs until it is
// cancelled; deny counts its runs and answers with a JSON-RPC error of code
// 403, as a backend's own policy might after running.
type calendarBackend struct {
	slowStarted, slowCancelled chan struct{}
	echoRuns, denyRuns         atomic.Int64
}

func startCalendarBackend(t *testing.T) (*calendarBackend, mcpbroker.ToolHiveProfile) {
	t.Helper()
	b := &calendarBackend{slowStarted: make(chan struct{}, 4), slowCancelled: make(chan struct{}, 4)}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "calendar", Version: "v1"}, nil)
	type args struct {
		A    int            `json:"a,omitempty"`
		Pad  string         `json:"pad,omitempty"`
		Nest map[string]any `json:"nest,omitempty"`
	}
	text := func(s string) *mcpsdk.CallToolResult {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: s}}}
	}
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "echo"}, func(_ context.Context, _ *mcpsdk.CallToolRequest, in args) (*mcpsdk.CallToolResult, any, error) {
		b.echoRuns.Add(1)
		return text(fmt.Sprintf("echo %d", in.A)), nil, nil
	})
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "deny"}, func(context.Context, *mcpsdk.CallToolRequest, args) (*mcpsdk.CallToolResult, any, error) {
		b.denyRuns.Add(1)
		return nil, nil, &jsonrpc.Error{Code: 403, Message: "Forbidden: the backend's own policy"}
	})
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "slow"}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, _ args) (*mcpsdk.CallToolResult, any, error) {
		b.slowStarted <- struct{}{}
		select {
		case <-ctx.Done():
			b.slowCancelled <- struct{}{}
			return nil, nil, ctx.Err()
		case <-time.After(time.Minute):
			return text("slow done"), nil, nil
		}
	})
	backend := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
	// The vMCP's streams to the backend can outlive its stop; Close would
	// wait for them.
	t.Cleanup(func() { backend.CloseClientConnections(); backend.Close() })
	return b, mcpbroker.ToolHiveProfile{Name: "calendar", URL: backend.URL, Auth: "none"}
}

// newBindingFixture is a harness fixture whose gateway routes the calendar
// backend, with short ID-JAGs, so short gateway tokens, and a short grace.
func newBindingFixture(t *testing.T) (*harnessFixture, *calendarBackend) {
	t.Helper()
	backend, profile := startCalendarBackend(t)
	f := newHarnessFixtureWith(t, harnessOptions{
		as2: mcpbrokertest.AS2Options{
			Profiles: []mcpbroker.ToolHiveProfile{profile}, MaxCallDuration: bindingMaxCall,
		},
		servers: []string{"calendar"}, idjagLifetime: bindingIDJAGLifetime,
	})
	return f, backend
}

// calendarCall is a tools/call of the calendar backend's tool with args, for
// operation op.
func (f *harnessFixture) calendarCall(t *testing.T, op, tool, args string) identitybroker.AuthorizeCallRequest {
	t.Helper()
	req := ownerCall(f.s, f.cred)
	req.OperationID, req.Server, req.Tool = op, "calendar", "calendar."+tool
	sum, err := callbinding.ArgumentsSHA256(json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	req.ArgumentsSHA256 = sum
	return req
}

// calendarListing is a tools/list of the calendar backend, for operation op.
func (f *harnessFixture) calendarListing(op string) identitybroker.AuthorizeCallRequest {
	req := ownerCall(f.s, f.cred)
	req.OperationID, req.Method, req.Server, req.Tool, req.ArgumentsSHA256 = op, identitybroker.MethodToolsList, "calendar", "", ""
	return req
}

func (f *harnessFixture) mustAuthorize(t *testing.T, req identitybroker.AuthorizeCallRequest) identitybroker.Authorization {
	t.Helper()
	got, err := f.authorize(req)
	if err != nil {
		t.Fatalf("AuthorizeCall(%s %s): %v", req.Method, req.Tool, err)
	}
	return got
}

// send makes the call auth is for with Mecatl's MCP client, as the MCP
// broker does, sending args.
func (f *harnessFixture) send(ctx context.Context, auth identitybroker.Authorization, req identitybroker.AuthorizeCallRequest, args string) mcpadapter.GatewayAttempt {
	return mcpadapter.SendGatewayCall(ctx, mcpadapter.GatewayCallRequest{
		Endpoint: f.as2Resource, HTTPClient: f.vmcpClient(), Token: auth.GatewayToken,
		OperationID: req.OperationID, AttemptID: auth.AttemptID, Tool: req.Tool,
		Arguments: json.RawMessage(args), ArgumentsSHA256: req.ArgumentsSHA256, SendBy: auth.ExpiresAt,
		HashArguments: func(raw json.RawMessage) (string, error) { return callbinding.ArgumentsSHA256(raw) },
	})
}

// vmcpClient reaches the real vMCP in process, streaming its answers.
func (f *harnessFixture) vmcpClient() *http.Client {
	return inprocesshttp.NewStreamingClient(f.vmcp, func(*url.URL) error { return nil })
}

// exchange is one HTTP exchange a wireRecorder saw: the JSON-RPC message
// kind it sent (a method, "response" for a reply to the vMCP, or the HTTP
// method of a bodiless request), when it was sent, and the status it got.
type exchange struct {
	kind   string
	sent   time.Time
	status int
	body   string
}

// wireRecorder sends every request with a bearer token and records each
// exchange. recorded, buffered with room for one, is signalled after each
// exchange is recorded.
type wireRecorder struct {
	base     http.RoundTripper
	token    string
	recorded chan struct{}

	mu   sync.Mutex
	seen []exchange
}

func (w *wireRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	kind := req.Method
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		var msg struct {
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(body, &msg) == nil {
			switch {
			case msg.Method != "":
				kind = msg.Method
			case msg.Result != nil || msg.Error != nil:
				kind = "response"
			}
		}
	}
	out := req.Clone(req.Context())
	out.Header.Set("Authorization", "Bearer "+w.token)
	sent := time.Now()
	resp, err := w.base.RoundTrip(out)
	if err != nil {
		return nil, err
	}
	e := exchange{kind: kind, sent: sent, status: resp.StatusCode}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(body))
		e.body = string(body)
	}
	w.mu.Lock()
	w.seen = append(w.seen, e)
	w.mu.Unlock()
	select {
	case w.recorded <- struct{}{}:
	default:
	}
	return resp, nil
}

// await returns the first exchange of kind, waiting up to waitLimit for it
// to be recorded. The vMCP forwards a cancellation to the backend before its
// answer reaches the client, so the backend can see the call cancelled
// before the cancellation's exchange is recorded.
func (w *wireRecorder) await(t *testing.T, kind string) exchange {
	t.Helper()
	deadline := time.After(waitLimit)
	for {
		w.mu.Lock()
		for _, e := range w.seen {
			if e.kind == kind {
				w.mu.Unlock()
				return e
			}
		}
		w.mu.Unlock()
		select {
		case <-w.recorded:
		case <-deadline:
			t.Fatalf("no %s exchange within %v", kind, waitLimit)
		}
	}
}

// statuses returns the statuses of the exchanges of kind, oldest first.
func (w *wireRecorder) statuses(kind string) []int {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []int
	for _, e := range w.seen {
		if e.kind == kind {
			out = append(out, e.status)
		}
	}
	return out
}

// last returns the latest exchange of kind, false when there is none.
func (w *wireRecorder) last(kind string) (exchange, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := len(w.seen) - 1; i >= 0; i-- {
		if w.seen[i].kind == kind {
			return w.seen[i], true
		}
	}
	return exchange{}, false
}

// reply posts a JSON-RPC response on the vMCP session, as a client answers a
// request the vMCP sent it during a call, such as an elicitation, and
// returns its status.
func (w *wireRecorder) reply(t *testing.T, endpoint, session string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint,
		strings.NewReader(`{"jsonrpc":"2.0","id":"el-1","result":{"action":"decline"}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Session-Id", session)
	resp, err := w.RoundTrip(req)
	if err != nil {
		t.Fatalf("post a reply: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// connect opens a vMCP session with token through a new wireRecorder. A
// refused initialize returns the error and the recorder.
func (f *harnessFixture) connect(t *testing.T, token identitybroker.Token) (*mcpsdk.ClientSession, *wireRecorder, error) {
	t.Helper()
	rec := &wireRecorder{base: f.vmcpClient().Transport, token: token.Value(), recorded: make(chan struct{}, 1)}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "identitybroker-test", Version: "v1"}, &mcpsdk.ClientOptions{
		Capabilities: &mcpsdk.ClientCapabilities{Elicitation: &mcpsdk.ElicitationCapabilities{URL: &mcpsdk.URLElicitationCapabilities{}}},
		ElicitationHandler: func(context.Context, *mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error) {
			return &mcpsdk.ElicitResult{Action: "decline"}, nil
		},
	})
	cs, err := client.Connect(t.Context(), &mcpsdk.StreamableClientTransport{
		Endpoint: f.as2Resource, HTTPClient: &http.Client{Transport: rec}, MaxRetries: -1, DisableStandaloneSSE: true,
	}, nil)
	if err == nil {
		t.Cleanup(func() { _ = cs.Close() })
	}
	return cs, rec, err
}

// gatewayTokenExp is the exp of a gateway token.
func gatewayTokenExp(t *testing.T, token identitybroker.Token) time.Time {
	t.Helper()
	return unixClaim(t, jwtPart(t, token.Value(), 1), "exp")
}

// TestAuthorizeCallBindsEachGatewayTokenToItsCallAtAS2: the broker sends
// AS-1 a call entry for every gateway call, tool calls and tool listings
// alike; AS-1 signs it into the ID-JAG; AS-2 copies it into the gateway
// token, unchanged, and returns it; the broker's output check finds it equal
// and records that; and each call gets its own token, which expires no
// later than its ID-JAG (implementation.md, "Test the per-call V").
func TestAuthorizeCallBindsEachGatewayTokenToItsCallAtAS2(t *testing.T) {
	f, _ := newBindingFixture(t)
	call := f.calendarCall(t, "op-bind-call", "echo", `{"a":1}`)
	listing := f.calendarListing("op-bind-list")
	want := map[string][]any{
		call.OperationID: {map[string]any{
			"type": identitybroker.CallEntryType, "server": "calendar", "tool": "calendar.echo",
			"arguments_sha256": call.ArgumentsSHA256, "operation_id": call.OperationID,
		}},
		listing.OperationID: {map[string]any{"type": identitybroker.CallEntryType, "server": "calendar", "method": "tools/list"}},
	}
	for i, req := range []identitybroker.AuthorizeCallRequest{call, listing} {
		got := f.mustAuthorize(t, req)
		exchanges := requests(f.as1, identityharness.KindIDJAG)
		if len(exchanges) != i+1 {
			t.Fatalf("the broker sent %d ID-JAG exchanges, want %d", len(exchanges), i+1)
		}
		var sent []any
		if r := exchanges[i]; !r.HasAuthorizationDetails || json.Unmarshal([]byte(r.AuthorizationDetails), &sent) != nil || !reflect.DeepEqual(sent, want[req.OperationID]) {
			t.Errorf("%s: the exchange sent authorization_details %q, want %v", req.Method, r.AuthorizationDetails, want[req.OperationID])
		}
		if claim := f.issued(t)[i]["authorization_details"]; !reflect.DeepEqual(claim, want[req.OperationID]) {
			t.Errorf("%s: the ID-JAG's authorization_details = %v, want the entry sent", req.Method, claim)
		}
		if redemption := requests(f.as2, identityharness.KindRedemption)[i]; redemption.HasAuthorizationDetails {
			t.Errorf("%s: the redemption carried authorization_details %q, want none", req.Method, redemption.AuthorizationDetails)
		}
		v := jwtPart(t, got.GatewayToken.Value(), 1)
		if !reflect.DeepEqual(v["authorization_details"], want[req.OperationID]) {
			t.Errorf("%s: the gateway token's authorization_details = %v, want the entry sent", req.Method, v["authorization_details"])
		}
		if exp, idjagExp := unixClaim(t, v, "exp"), unixClaim(t, f.issued(t)[i], "exp"); exp.After(idjagExp) {
			t.Errorf("%s: the gateway token expires at %v, after its ID-JAG at %v", req.Method, exp, idjagExp)
		}
		if d := f.rec.Decisions()[i]; d.Outcome != identitybroker.OutcomeSuccess || d.CallEntryCheck != identitybroker.CallEntryEqual || d.GatewayToken.ID != v["jti"] {
			t.Errorf("%s: the decision = %s, check %q, token %+v; want success, equal, and the token's jti", req.Method, d.Outcome, d.CallEntryCheck, d.GatewayToken)
		}
	}
}

// TestRealVMCPRunsOnlyTheCallItsTokenNames: a gateway token starts only the
// tools/call its call entry names, once; a listing token only its listing,
// answered with the calendar backend's tools alone and no cursor
// (implementation.md, "Test the binding at the vMCP").
func TestRealVMCPRunsOnlyTheCallItsTokenNames(t *testing.T) {
	f, _ := newBindingFixture(t)
	ctx := t.Context()

	// The call the token names runs, through Mecatl's MCP client.
	echo := f.calendarCall(t, "op-run", "echo", `{"a":1}`)
	auth := f.mustAuthorize(t, echo)
	if a := f.send(ctx, auth, echo, `{"a":1}`); a.Outcome != mcpadapter.GatewayAttemptCompleted || !strings.Contains(fmt.Sprint(mcpadapter.GatewayToolResult("c", "calendar", "mcp__calendar__echo", a).Content), "echo 1") {
		t.Fatalf("the bound call = %+v, want it completed with the backend's answer", a)
	}
	// The token has started its call: it starts no other, on the same call's
	// terms or any other.
	if cs, rec, err := f.connect(t, auth.GatewayToken); err == nil {
		if _, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "calendar.echo", Arguments: map[string]any{"a": 1}}); err == nil {
			t.Fatal("a spent token started a second call")
		}
		if e, _ := rec.last("tools/call"); e.status != http.StatusUnauthorized || !strings.Contains(e.body, "token_reused") {
			t.Fatalf("a second call with a spent token: %d %s, want 401 token_reused", e.status, e.body)
		}
	} else if e, _ := rec.last("initialize"); e.status != http.StatusUnauthorized || !strings.Contains(e.body, "token_reused") {
		t.Fatalf("a second session with a spent token: %d %s, want 401 token_reused", e.status, e.body)
	}

	// refused calls tool with args using a token for req, and wants the vMCP
	// to refuse it with reason before it runs.
	refused := func(name string, req identitybroker.AuthorizeCallRequest, tool string, args map[string]any, status int, reason string) {
		t.Helper()
		auth := f.mustAuthorize(t, req)
		cs, rec, err := f.connect(t, auth.GatewayToken)
		if err != nil {
			t.Fatalf("%s: initialize: %v", name, err)
		}
		if _, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: tool, Arguments: args}); err == nil {
			t.Fatalf("%s: the vMCP ran the call", name)
		}
		if e, _ := rec.last("tools/call"); e.status != status || !strings.Contains(e.body, reason) {
			t.Fatalf("%s: the vMCP answered %d %s, want %d %s", name, e.status, e.body, status, reason)
		}
	}
	refused("other arguments", f.calendarCall(t, "op-args", "echo", `{"a":1}`), "calendar.echo", map[string]any{"a": 2}, http.StatusForbidden, "arguments_mismatch")
	refused("another tool", f.calendarCall(t, "op-tool", "echo", `{"a":1}`), "calendar.slow", map[string]any{"a": 1}, http.StatusForbidden, "tool_mismatch")
	// A listing token cannot start a tools/call.
	refused("a listing token", f.calendarListing("op-list-call"), "calendar.echo", map[string]any{"a": 1}, http.StatusForbidden, "method_mismatch")

	// A listing token lists the calendar backend's tools alone.
	list := f.mustAuthorize(t, f.calendarListing("op-list"))
	cs, listRec, err := f.connect(t, list.GatewayToken)
	if err != nil {
		t.Fatalf("initialize with the listing token: %v", err)
	}
	listed, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list with the listing token: %v", err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"calendar.deny", "calendar.echo", "calendar.slow"}) || listed.NextCursor != "" {
		t.Fatalf("the listing = %v, cursor %q; want the calendar backend's tools alone and no cursor", names, listed.NextCursor)
	}
	if _, err := cs.ListTools(ctx, nil); err == nil {
		t.Fatal("the listing token listed twice")
	}
	if e, _ := listRec.last("tools/list"); e.status != http.StatusUnauthorized || !strings.Contains(e.body, "token_reused") {
		t.Fatalf("a second listing: %d %s, want 401 token_reused", e.status, e.body)
	}
}

// padArgs is a calendar call's arguments of exactly n bytes, padded with
// "<", which a JSON encoder that escapes HTML would make six times longer.
func padArgs(n int64) string {
	const open, closing = `{"pad":"`, `"}`
	return open + strings.Repeat("<", int(n)-len(open)-len(closing)) + closing
}

// TestRealVMCPRefusesACallTooLargeForItBeforeRunningIt: the real vMCP takes
// a call whose arguments are as long as the MCP broker lets through,
// mcpbroker.MaxGatewayArgumentBytes, and runs it, so the cap is within the
// vMCP's request body limits; a call over the lower of them, the MCP SDK's,
// it refuses with 413 before running it, and Mecatl's MCP client reports it
// refused, not as an unknown outcome (stacklok/mecatl#3009). Hashing MiBs
// of arguments is slow under the race detector, so the gateway tokens last
// a minute rather than the binding fixture's few seconds: nothing here is
// about their expiry.
func TestRealVMCPRefusesACallTooLargeForItBeforeRunningIt(t *testing.T) {
	backend, profile := startCalendarBackend(t)
	f := newHarnessFixtureWith(t, harnessOptions{
		as2:     mcpbrokertest.AS2Options{Profiles: []mcpbroker.ToolHiveProfile{profile}},
		servers: []string{"calendar"}, idjagLifetime: time.Minute,
	})
	ctx := t.Context()

	fits := padArgs(mcpbroker.MaxGatewayArgumentBytes)
	req := f.calendarCall(t, "op-fits", "echo", fits)
	if a := f.send(ctx, f.mustAuthorize(t, req), req, fits); a.Outcome != mcpadapter.GatewayAttemptCompleted || backend.echoRuns.Load() != 1 {
		t.Fatalf("a call at the MCP broker's limit = %+v, backend runs %d; want completed once", a, backend.echoRuns.Load())
	}

	tooLarge := padArgs(mcpsdk.DefaultMaxRequestBodyBytes + 1)
	req = f.calendarCall(t, "op-too-large", "echo", tooLarge)
	if a := f.send(ctx, f.mustAuthorize(t, req), req, tooLarge); a.Outcome != mcpadapter.GatewayAttemptRefused || a.Rejection != mcpadapter.GatewayRejectionTooLarge || a.HTTPStatus != http.StatusRequestEntityTooLarge {
		t.Fatalf("a call over the vMCP's limit = %+v, want refused as request_too_large with 413", a)
	}
	if n := backend.echoRuns.Load(); n != 1 {
		t.Fatalf("backend runs = %d, want the call over the limit not run", n)
	}
}

// TestRealVMCPRefusesCaseCollidingArgumentsBeforeRunningThem: arguments
// whose member names differ only by case, at the top level or nested, are
// valid I-JSON and get a gateway token, but the real vMCP refuses them as
// ambiguous with 400 before running them (stacklok/mecatl#3009). The MCP
// broker therefore refuses them as refused_arguments before authorizing
// (TestGatewayCallRefusesArgumentsTheVMCPWouldRefuse). The same arguments
// without the collision run once, as the control.
func TestRealVMCPRefusesCaseCollidingArgumentsBeforeRunningThem(t *testing.T) {
	f, backend := newBindingFixture(t)
	ctx := t.Context()

	control := `{"a":1,"nest":{"b":1,"c":2}}`
	req := f.calendarCall(t, "op-no-collision", "echo", control)
	if a := f.send(ctx, f.mustAuthorize(t, req), req, control); a.Outcome != mcpadapter.GatewayAttemptCompleted || backend.echoRuns.Load() != 1 {
		t.Fatalf("the control call = %+v, backend runs %d; want completed once", a, backend.echoRuns.Load())
	}

	for name, args := range map[string]string{
		"top level": `{"a":1,"A":2,"nest":{"b":1,"c":2}}`,
		"nested":    `{"a":1,"nest":{"b":1,"B":2}}`,
	} {
		req := f.calendarCall(t, "op-collision-"+strings.ReplaceAll(name, " ", "-"), "echo", args)
		if a := f.send(ctx, f.mustAuthorize(t, req), req, args); a.Outcome == mcpadapter.GatewayAttemptCompleted || a.HTTPStatus != http.StatusBadRequest {
			t.Fatalf("%s collision = %+v, want the vMCP to refuse it with 400", name, a)
		}
	}
	if n := backend.echoRuns.Load(); n != 1 {
		t.Fatalf("backend runs = %d, want the colliding calls not run", n)
	}
}

// TestRealVMCPRelaysABackendErrorAfterItRan: a backend that runs the call
// and answers with a JSON-RPC error of code 403, as its own policy might,
// reaches Mecatl's MCP client as the vMCP relays it: a tool error result
// over HTTP 200. The call is completed, never refused as one the vMCP
// denied before running it, and its result carries the backend's message.
func TestRealVMCPRelaysABackendErrorAfterItRan(t *testing.T) {
	f, backend := newBindingFixture(t)
	req := f.calendarCall(t, "op-deny", "deny", `{}`)
	a := f.send(t.Context(), f.mustAuthorize(t, req), req, `{}`)
	if a.Outcome != mcpadapter.GatewayAttemptCompleted || a.Rejection != "" || a.HTTPStatus != http.StatusOK || backend.denyRuns.Load() != 1 {
		t.Fatalf("the backend's error after running = %+v, backend runs %d; want completed over HTTP 200, run once", a, backend.denyRuns.Load())
	}
	if res := mcpadapter.GatewayToolResult("c", "calendar", "mcp__calendar__deny", a); !res.IsError || !strings.Contains(res.Content, "the backend's own policy") {
		t.Fatalf("the result = %+v, want a tool error carrying the backend's message", res)
	}
}

// TestRetriedCallGetsANewTokenAtTheVMCP: a call the vMCP refuses with 401
// invalid_token before it ran is retried with a new gateway token, which the
// vMCP accepts. A token that has already started a call, as a stolen one
// replayed first would, gets 401 token_reused instead: the call it names may
// have run, so Mecatl's MCP client reports an unknown outcome and the call is
// not retried (implementation.md, "Test the per-call V";
// credentials-and-lifecycle.md, step (9)).
func TestRetriedCallGetsANewTokenAtTheVMCP(t *testing.T) {
	f, _ := newBindingFixture(t)
	req := f.calendarCall(t, "op-retry", "echo", `{"a":3}`)
	first := f.mustAuthorize(t, req)
	// The token expires before it is sent; the client's own send-by check is
	// moved out of the way so the vMCP answers.
	time.Sleep(time.Until(gatewayTokenExp(t, first.GatewayToken).Add(time.Second)))
	late := first
	late.ExpiresAt = time.Now().Add(time.Minute)
	if a := f.send(t.Context(), late, req, `{"a":3}`); a.Outcome != mcpadapter.GatewayAttemptNotSent || a.Rejection != mcpadapter.GatewayRejectionInvalidToken {
		t.Fatalf("the expired token = %+v, want refused as invalid_token before the call", a)
	}
	req.RejectedAttemptID = first.AttemptID
	retry := f.mustAuthorize(t, req)
	firstJTI, retryJTI := jwtPart(t, first.GatewayToken.Value(), 1)["jti"], jwtPart(t, retry.GatewayToken.Value(), 1)["jti"]
	if retry.AttemptID == first.AttemptID || retryJTI == firstJTI {
		t.Fatalf("the retry got attempt %s token %v, the first %s %v; want new ones", retry.AttemptID, retryJTI, first.AttemptID, firstJTI)
	}
	if a := f.send(t.Context(), retry, req, `{"a":3}`); a.Outcome != mcpadapter.GatewayAttemptCompleted {
		t.Fatalf("the retry = %+v, want completed with its new token", a)
	}

	// A token someone else spent first.
	stolen := f.calendarCall(t, "op-stolen", "echo", `{"a":4}`)
	auth := f.mustAuthorize(t, stolen)
	cs, _, err := f.connect(t, auth.GatewayToken)
	if err != nil {
		t.Fatalf("the replay's initialize: %v", err)
	}
	if _, err := cs.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "calendar.echo", Arguments: map[string]any{"a": 4}}); err != nil {
		t.Fatalf("the replayed call: %v", err)
	}
	if a := f.send(t.Context(), auth, stolen, `{"a":4}`); a.Outcome != mcpadapter.GatewayAttemptUnknown || a.Rejection != mcpadapter.GatewayRejectionTokenReused {
		t.Fatalf("the call after its token was spent = %+v, want an unknown outcome for token_reused", a)
	}
}

// TestSpentTokenIsNotPresentedAfterItsSendBy: someone who took a gateway
// token spends it on its call while Mecatl's handshake is still under way,
// and that handshake ends after the token's exp. Before exp, the vMCP answers
// Mecatl's tools/call with token_reused, never retried; after exp it answers
// token_expired, an invalid_token the MCP broker would retry with a new
// token, running the call a second time. Mecatl's MCP client writes no
// request with the token after its send-by deadline, less a margin, until
// its tools/call has started; so its tools/call is not sent, the attempt is
// authorization_expired, which the MCP broker does not retry, and the call
// ran once.
func TestSpentTokenIsNotPresentedAfterItsSendBy(t *testing.T) {
	f, backend := newBindingFixture(t)
	req := f.calendarCall(t, "op-spent-late", "echo", `{"a":7}`)
	auth := f.mustAuthorize(t, req)
	exp := gatewayTokenExp(t, auth.GatewayToken)
	var spent error
	held := &heldHandshake{base: f.vmcpClient().Transport, hold: func() {
		// The vMCP has accepted Mecatl's handshake. The token is spent
		// elsewhere, and the answer reaches Mecatl after exp.
		cs, _, err := f.connect(t, auth.GatewayToken)
		if err == nil {
			_, err = cs.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "calendar.echo", Arguments: map[string]any{"a": 7}})
		}
		spent = err
		time.Sleep(time.Until(exp.Add(500 * time.Millisecond)))
	}}
	a := mcpadapter.SendGatewayCall(t.Context(), mcpadapter.GatewayCallRequest{
		Endpoint: f.as2Resource, HTTPClient: &http.Client{Transport: held}, Token: auth.GatewayToken,
		OperationID: req.OperationID, AttemptID: auth.AttemptID, Tool: req.Tool,
		Arguments: json.RawMessage(`{"a":7}`), ArgumentsSHA256: req.ArgumentsSHA256, SendBy: auth.ExpiresAt,
		HashArguments: func(raw json.RawMessage) (string, error) { return callbinding.ArgumentsSHA256(raw) },
	})
	if spent != nil {
		t.Fatalf("spend the token elsewhere: %v", spent)
	}
	if a.Outcome != mcpadapter.GatewayAttemptNotSent || a.Rejection != mcpadapter.GatewayRejectionExpired {
		t.Fatalf("Mecatl's attempt after exp = %+v, want not sent as authorization_expired", a)
	}
	if got := held.methods(); slices.Contains(got, "tools/call") {
		t.Fatalf("Mecatl wrote %v, want no tools/call with the spent token", got)
	}
	if n := backend.echoRuns.Load(); n != 1 {
		t.Fatalf("the call ran %d times, want once", n)
	}
}

// heldHandshake sends requests to base and records each one's JSON-RPC
// method; it runs hold once, after the vMCP has answered
// notifications/initialized and before that answer is returned.
type heldHandshake struct {
	base http.RoundTripper
	hold func()

	once sync.Once
	mu   sync.Mutex
	sent []string
}

func (h *heldHandshake) RoundTrip(req *http.Request) (*http.Response, error) {
	var msg struct {
		Method string `json:"method"`
	}
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		_ = json.Unmarshal(body, &msg)
	}
	h.mu.Lock()
	h.sent = append(h.sent, cmp.Or(msg.Method, req.Method))
	h.mu.Unlock()
	resp, err := h.base.RoundTrip(req)
	if err == nil && msg.Method == "notifications/initialized" {
		h.once.Do(h.hold)
	}
	return resp, err
}

// methods returns the JSON-RPC methods, or HTTP methods of bodiless
// requests, written so far.
func (h *heldHandshake) methods() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.sent)
}

// TestRealVMCPCancelsACallThatOutlivesItsToken: Mecatl's MCP client cancels
// a call late in its maximum duration, long after its gateway token
// expired; the vMCP accepts the cancellation and the backend's tool is
// cancelled, since the grace after exp equals the maximum call duration
// (implementation.md, "Test the binding at the vMCP").
func TestRealVMCPCancelsACallThatOutlivesItsToken(t *testing.T) {
	f, backend := newBindingFixture(t)
	req := f.calendarCall(t, "op-outlive", "slow", `{}`)
	auth := f.mustAuthorize(t, req)
	exp := gatewayTokenExp(t, auth.GatewayToken)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan mcpadapter.GatewayAttempt, 1)
	go func() { done <- f.send(ctx, auth, req, `{}`) }()
	receive(t, backend.slowStarted)
	started := time.Now()
	// Cancel late in the call: well past the point where a grace of a sixth
	// of the maximum call duration (production's former five of 30 minutes)
	// would have ended, and before the call's maximum.
	late := started.Add(bindingMaxCall - 3*time.Second)
	if !late.After(exp.Add(bindingMaxCall / 6)) {
		t.Fatalf("the cancellation at %v is not past a sixth of the maximum after exp %v; the test proves nothing", late, exp)
	}
	time.Sleep(time.Until(late))
	cancel()
	receive(t, backend.slowCancelled)
	if time.Since(started) >= bindingMaxCall {
		t.Fatal("the cancellation arrived after the call's maximum; the test proves nothing")
	}
	if a := receive(t, done); a.Outcome != mcpadapter.GatewayAttemptUnknown {
		t.Fatalf("the cancelled call = %+v, want an unknown outcome", a)
	}
}

// TestRealVMCPAcceptsAnOpenCallsMessagesOnlyWithinTheGrace: once a call's
// gateway token has expired, the vMCP accepts the call's cancellation and a
// reply on its session for the grace, and answers 401 after it. Mecatl's MCP
// client sends no such reply today: the vMCP answers a tools/call as one
// JSON response, and the client opens no stream for requests during a call,
// so the reply is posted by hand.
func TestRealVMCPAcceptsAnOpenCallsMessagesOnlyWithinTheGrace(t *testing.T) {
	t.Run("a cancellation within the grace", func(t *testing.T) {
		f, backend := newBindingFixture(t)
		auth := f.mustAuthorize(t, f.calendarCall(t, "op-cancel", "slow", `{}`))
		cs, rec, err := f.connect(t, auth.GatewayToken)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		go func() {
			_, _ = cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "calendar.slow", Arguments: map[string]any{}})
		}()
		receive(t, backend.slowStarted)
		exp := gatewayTokenExp(t, auth.GatewayToken)
		time.Sleep(time.Until(exp.Add(500 * time.Millisecond)))
		if status := rec.reply(t, f.as2Resource, cs.ID()); status != http.StatusAccepted {
			t.Fatalf("a reply within the grace got %d, want 202", status)
		}
		cancel()
		receive(t, backend.slowCancelled)
		if e := rec.await(t, "notifications/cancelled"); e.status != http.StatusAccepted && e.sent.After(exp.Add(bindingMaxCall)) {
			t.Fatalf("the cancellation was sent %v after exp, past the grace; the test proves nothing", e.sent.Sub(exp))
		}
		if got := rec.statuses("notifications/cancelled"); !slices.Equal(got, []int{http.StatusAccepted}) {
			t.Fatalf("the cancellation got %v, want 202", got)
		}
	})
	t.Run("a cancellation after the grace", func(t *testing.T) {
		f, backend := newBindingFixture(t)
		auth := f.mustAuthorize(t, f.calendarCall(t, "op-late", "slow", `{}`))
		cs, rec, err := f.connect(t, auth.GatewayToken)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		go func() {
			_, _ = cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "calendar.slow", Arguments: map[string]any{}})
		}()
		receive(t, backend.slowStarted)
		time.Sleep(time.Until(gatewayTokenExp(t, auth.GatewayToken).Add(bindingMaxCall + time.Second)))
		if status := rec.reply(t, f.as2Resource, cs.ID()); status != http.StatusUnauthorized {
			t.Fatalf("a reply after the grace got %d, want 401", status)
		}
		cancel()
		rec.await(t, "notifications/cancelled")
		if got := rec.statuses("notifications/cancelled"); !slices.Equal(got, []int{http.StatusUnauthorized}) {
			t.Fatalf("the cancellation after the grace got %v, want 401", got)
		}
	})
}

package identityharness_test

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// rpc posts a JSON-RPC body to g with token as the bearer, empty for none,
// and returns the status, the WWW-Authenticate header, and the decoded body.
func rpc(t *testing.T, h *identityharness.Harness, g *identityharness.Gateway, token, body string) (int, string, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, g.URL(), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := h.HTTPClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, resp.Header.Get("WWW-Authenticate"), out
}

// unsignedJWT is a JWT with claims and alg none, for the gateway stand-in,
// which reads a bearer token's jti without verifying it.
func unsignedJWT(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

const (
	initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	listBody       = `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	callBody       = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"github.pull_request_read","arguments":{}}}`
)

func TestGatewayServesWithABearerToken(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	g := h.StartGateway(t, identityharness.GatewayOptions{})
	if !strings.HasPrefix(g.URL(), "https://") || !strings.HasSuffix(g.URL(), "/mcp") {
		t.Fatalf("URL = %q", g.URL())
	}
	jti := rand.Text()
	token := unsignedJWT(t, jwt.MapClaims{"jti": jti})
	if status, _, body := rpc(t, h, g, token, initializeBody); status != http.StatusOK || body["result"] == nil {
		t.Fatalf("initialize = %d, %v", status, body)
	}
	if status, _, body := rpc(t, h, g, token, listBody); status != http.StatusOK || body["result"] == nil {
		t.Fatalf("tools/list = %d, %v", status, body)
	}
	status, _, body := rpc(t, h, g, token, callBody)
	result, _ := body["result"].(map[string]any)
	if status != http.StatusOK || !strings.Contains(mustJSON(t, result["content"]), "github.pull_request_read") {
		t.Fatalf("tools/call = %d, %v", status, body)
	}
	status, challenge, _ := rpc(t, h, g, "", callBody)
	if status != http.StatusUnauthorized || challenge != "Bearer" {
		t.Fatalf("no token = %d, %q; want 401 Bearer", status, challenge)
	}
	want := []identityharness.GatewayCall{
		{Method: "initialize", Status: 200, TokenJTI: jti},
		{Method: "tools/list", Status: 200, TokenJTI: jti},
		{Method: "tools/call", Tool: "github.pull_request_read", Status: 200, TokenJTI: jti, Arguments: json.RawMessage(`{}`)},
		{Method: "tools/call", Tool: "github.pull_request_read", Status: 401, Arguments: json.RawMessage(`{}`)},
	}
	if got := g.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %+v, want %+v", got, want)
	}
}

func TestGatewayRejectsTokens(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	g := h.StartGateway(t, identityharness.GatewayOptions{Verify: func(token string) error {
		if token == "bad" {
			return errors.New("bad token")
		}
		return nil
	}})
	invalid := func(name, token string, want int) {
		t.Helper()
		status, challenge, _ := rpc(t, h, g, token, callBody)
		if want == http.StatusUnauthorized && (status != want || challenge != `Bearer error="invalid_token"`) {
			t.Fatalf("%s = %d, %q; want 401 invalid_token", name, status, challenge)
		}
		if want != http.StatusUnauthorized && status != want {
			t.Fatalf("%s = %d, want %d", name, status, want)
		}
	}
	invalid("a token Verify refuses", "bad", http.StatusUnauthorized)
	g.RejectNextToken()
	invalid("the next token", "good", http.StatusUnauthorized)
	invalid("the one after", "good", http.StatusOK)
	g.RejectTokens(true)
	invalid("while rejecting", "good", http.StatusUnauthorized)
	invalid("still rejecting", "good", http.StatusUnauthorized)
	g.RejectTokens(false)
	invalid("after rejecting", "good", http.StatusOK)
}

func TestGatewayMissingConnectionElicitsAURL(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	g := h.StartGateway(t, identityharness.GatewayOptions{})
	g.MissingConnection("github", "https://gateway.example/connect/github")
	status, _, body := rpc(t, h, g, "token", callBody)
	rpcErr, _ := body["error"].(map[string]any)
	if status != http.StatusOK || rpcErr["code"] != float64(identityharness.URLElicitationRequired) ||
		!strings.Contains(mustJSON(t, rpcErr["data"]), `"url":"https://gateway.example/connect/github"`) {
		t.Fatalf("tools/call on a missing connection = %d, %v", status, body)
	}
	other := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"calendar.list"}}`
	if status, _, body := rpc(t, h, g, "token", other); status != http.StatusOK || body["result"] == nil {
		t.Fatalf("tools/call on another server = %d, %v", status, body)
	}
}

func TestHasJSONRPCResult(t *testing.T) {
	for body, want := range map[string]bool{
		`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}`:               true,
		"event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n":           true,
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`:    false,
		"event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-1}}\n\n": false,
		`{"jsonrpc":"2.0","id":1,"result":null}`:                                           false,
		``:                                                                                 false,
	} {
		if got := identityharness.HasJSONRPCResult([]byte(body)); got != want {
			t.Errorf("HasJSONRPCResult(%q) = %v, want %v", body, got, want)
		}
	}
}

// send sends method with body (empty for none) to g with token and session
// (empty for none) and returns the response; the caller closes its body.
func send(t *testing.T, h *identityharness.Harness, g *identityharness.Gateway, method, token, session, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, g.URL(), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := h.HTTPClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// status sends like send and returns the status, closing the body.
func status(t *testing.T, h *identityharness.Harness, g *identityharness.Gateway, method, session, body string) int {
	t.Helper()
	resp := send(t, h, g, method, "token", session, body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// openSession initializes a session on g and returns its ID.
func openSession(t *testing.T, h *identityharness.Harness, g *identityharness.Gateway) string {
	t.Helper()
	resp := send(t, h, g, http.MethodPost, "token", "", initializeBody)
	_ = resp.Body.Close()
	id := resp.Header.Get("Mcp-Session-Id")
	if resp.StatusCode != http.StatusOK || id == "" {
		t.Fatalf("initialize = %d with session %q", resp.StatusCode, id)
	}
	return id
}

func TestGatewaySessionNotFound(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	g := h.StartGateway(t, identityharness.GatewayOptions{})
	first := openSession(t, h, g)
	if got := status(t, h, g, http.MethodPost, first, callBody); got != http.StatusOK {
		t.Fatalf("tools/call in the session = %d, want 200", got)
	}
	if got := status(t, h, g, http.MethodPost, "sess-unknown", callBody); got != http.StatusNotFound {
		t.Fatalf("tools/call in an unknown session = %d, want 404", got)
	}
	g.ForgetSessions()
	if got := status(t, h, g, http.MethodPost, first, callBody); got != http.StatusNotFound {
		t.Fatalf("tools/call in a forgotten session = %d, want 404", got)
	}
	if got := status(t, h, g, http.MethodPost, "", callBody); got != http.StatusOK {
		t.Fatalf("tools/call without a session = %d, want 200", got)
	}

	second := openSession(t, h, g)
	if second == first {
		t.Fatal("initialize reused a session ID")
	}
	if got := status(t, h, g, http.MethodDelete, second, ""); got != http.StatusNoContent {
		t.Fatalf("DELETE = %d, want 204", got)
	}
	if got := status(t, h, g, http.MethodPost, second, listBody); got != http.StatusNotFound {
		t.Fatalf("tools/list in a deleted session = %d, want 404", got)
	}
	if got := status(t, h, g, http.MethodDelete, second, ""); got != http.StatusNotFound {
		t.Fatalf("DELETE of a deleted session = %d, want 404", got)
	}
	calls := g.Calls()
	if last := calls[len(calls)-1]; last.Method != http.MethodDelete || last.SessionID != second || last.Status != http.StatusNotFound {
		t.Fatalf("last call = %+v, want the refused DELETE", last)
	}

	// A rejected token is answered before the session is looked up, on
	// initialize too.
	g.RejectNextToken()
	resp := send(t, h, g, http.MethodPost, "token", "", initializeBody)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("Mcp-Session-Id") != "" {
		t.Fatalf("initialize with a rejected token = %d, session %q", resp.StatusCode, resp.Header.Get("Mcp-Session-Id"))
	}
}

func TestGatewayElicitsDuringCall(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	g := h.StartGateway(t, identityharness.GatewayOptions{})
	g.ElicitDuringCall("github")
	session := openSession(t, h, g)
	resp := send(t, h, g, http.MethodPost, "token", session, callBody)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("tools/call = %d %q, want an event stream", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	events := bufio.NewReader(resp.Body)
	next := func() map[string]any {
		t.Helper()
		for {
			line, err := events.ReadString('\n')
			if err != nil {
				t.Fatalf("read event: %v", err)
			}
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				var msg map[string]any
				if err := json.Unmarshal([]byte(data), &msg); err != nil {
					t.Fatal(err)
				}
				return msg
			}
		}
	}
	elicit := next()
	params, _ := elicit["params"].(map[string]any)
	if elicit["method"] != "elicitation/create" || params["mode"] != "url" || params["url"] != identityharness.ElicitationConnectURL {
		t.Fatalf("first event = %v, want a URL elicitation", elicit)
	}
	reply := `{"jsonrpc":"2.0","id":` + mustJSON(t, elicit["id"]) + `,"result":{"action":"decline"}}`
	if got := status(t, h, g, http.MethodPost, session, reply); got != http.StatusAccepted {
		t.Fatalf("reply = %d, want 202", got)
	}
	if result := next(); result["id"] != float64(3) || result["result"] == nil {
		t.Fatalf("second event = %v, want the tools/call result", result)
	}
	calls := g.Calls()
	if got := calls[len(calls)-1]; !got.Reply || got.Method != "" || got.SessionID != session || got.Status != http.StatusAccepted {
		t.Fatalf("last call = %+v, want the recorded reply", got)
	}
}

func TestGatewayDropsAfterRequest(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	g := h.StartGateway(t, identityharness.GatewayOptions{})
	g.DropAfterRequest("github")
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, g.URL(), strings.NewReader(callBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer token")
	resp, err := h.HTTPClient().Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("tools/call answered %d, want a dropped connection", resp.StatusCode)
	}
	want := []identityharness.GatewayCall{{Method: "tools/call", Tool: "github.pull_request_read", Arguments: json.RawMessage(`{}`)}}
	if got := g.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %+v, want %+v", got, want)
	}
	if got := status(t, h, g, http.MethodPost, "", strings.Replace(callBody, "github.", "calendar.", 1)); got != http.StatusOK {
		t.Fatalf("tools/call on another server = %d, want 200", got)
	}
}

func TestGatewayRecordsMetaAndReplies(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	g := h.StartGateway(t, identityharness.GatewayOptions{})
	const meta = `"_meta":{"dev.mecatl/operation":"op_1","dev.mecatl/attempt":"att_1","other":"x"}`
	want := map[string]string{identityharness.MetaOperation: "op_1", identityharness.MetaAttempt: "att_1"}
	for _, tc := range []struct {
		name, body string
		status     int
		reply      bool
		meta       map[string]string
	}{
		{"request", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"github.x","arguments":{"a":1},` + meta + `}}`, http.StatusOK, false, want},
		{"notification", `{"jsonrpc":"2.0","method":"notifications/initialized","params":{` + meta + `}}`, http.StatusAccepted, false, want},
		{"cancellation", `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":3,` + meta + `}}`, http.StatusAccepted, false, want},
		{"result reply", `{"jsonrpc":"2.0","id":"gw-1","result":{"action":"decline",` + meta + `}}`, http.StatusAccepted, true, want},
		{"error reply", `{"jsonrpc":"2.0","id":"gw-2","error":{"code":-32601,"message":"declined","data":{` + meta + `}}}`, http.StatusAccepted, true, want},
		{"no meta", `{"jsonrpc":"2.0","id":"gw-3","result":{}}`, http.StatusAccepted, true, nil},
		{"neither", `{"jsonrpc":"2.0","id":4}`, http.StatusBadRequest, false, nil},
	} {
		if got := status(t, h, g, http.MethodPost, "", tc.body); got != tc.status {
			t.Fatalf("%s = %d, want %d", tc.name, got, tc.status)
		}
		calls := g.Calls()
		got := calls[len(calls)-1]
		if got.Reply != tc.reply || !reflect.DeepEqual(got.Meta, tc.meta) {
			t.Fatalf("%s recorded %+v, want reply %v and meta %v", tc.name, got, tc.reply, tc.meta)
		}
	}
	if got := g.Calls()[0].Arguments; string(got) != `{"a":1}` {
		t.Fatalf("arguments = %s", got)
	}
}

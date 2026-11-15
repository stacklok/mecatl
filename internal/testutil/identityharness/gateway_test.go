package identityharness_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

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
	token := h.AS1.MintAccessToken(identityharness.AccessTokenSpec{})
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
	_, claims := verify(t, h, token)
	jti := claims["jti"].(string)
	want := []identityharness.GatewayCall{
		{Method: "initialize", Status: 200, TokenJTI: jti},
		{Method: "tools/list", Status: 200, TokenJTI: jti},
		{Method: "tools/call", Tool: "github.pull_request_read", Status: 200, TokenJTI: jti},
		{Method: "tools/call", Tool: "github.pull_request_read", Status: 401},
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

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// TestIdentityBrokerBindsEachGatewayTokenToItsCall runs #2727 through
// mecak8s's real startup and served vMCP endpoint, offline: the operator
// sets the gateway token's lifetime in settings.yaml; every gateway token
// the identity broker obtains carries the call entry it sent AS-1, copied by
// the real AS-2; a listing token cannot start a tools/call at the served
// vMCP; and a token past its exp with no open call is refused. The calls
// themselves are refused by the stand-in (TestIdentityBrokerGatewayCallAgainstRealAS1),
// so a call outliving its token is covered against the real AS-2 and vMCP
// by the identity broker's TestRealVMCPCancelsACallThatOutlivesItsToken. It
// is not parallel: the protected route's client secret is read from an
// environment variable.
func TestIdentityBrokerBindsEachGatewayTokenToItsCall(t *testing.T) {
	const lifetime = 3 * time.Second
	gw := serveGatewayIdentityBrokerWith(t, "    gateway_token_lifetime: 3s", notesRoute(t), nil, nil)
	created := createSession(t, gw.api, gw.alice)
	if status, body := apiCall(t, http.MethodPost, gw.api+"/v1/sessions/"+created+"/prompt", gw.alice, `{"text":"hello"}`); status != http.StatusOK {
		t.Fatalf("Alice prompts her session: status %d: %s", status, body)
	}
	opened, ok, err := gw.built.IdentityBroker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: created})
	if err != nil || !ok {
		t.Fatalf("Alice's broker session = %+v, %v, %v", opened, ok, err)
	}
	agent, err := gw.built.IdentityBroker.RegisterAgent(t.Context(), identitybroker.RegisterAgentRequest{
		SessionID: opened.ID, HostSessionID: created, Owner: opened.Owner, Incarnation: opened.Incarnation,
		Definition: builtinMainDefinition(),
	})
	if err != nil {
		t.Fatalf("register the session's main agent: %v", err)
	}
	authorize := func(op, method, tool, hash string) identitybroker.Authorization {
		t.Helper()
		got, err := gw.built.IdentityBroker.AuthorizeCall(t.Context(), identitybroker.AuthorizeCallRequest{
			SessionID: opened.ID, Credential: agent.Credential, OperationID: op, RunID: "R1", RunOwner: opened.Owner,
			Method: method, Server: "calendar", Tool: tool, ArgumentsSHA256: hash,
		})
		if err != nil {
			t.Fatalf("authorize %s: %v", op, err)
		}
		return got
	}
	hash := strings.Repeat("ab", 32)
	call := authorize("op-call", identitybroker.MethodToolsCall, "calendar.echo", hash)
	listing := authorize("op-list", identitybroker.MethodToolsList, "", "")

	// Each token carries the entry the broker sent, and lives for the
	// configured lifetime.
	for _, c := range []struct {
		name  string
		auth  identitybroker.Authorization
		entry map[string]any
	}{
		{"tool call", call, map[string]any{
			"type": identitybroker.CallEntryType, "server": "calendar", "tool": "calendar.echo",
			"arguments_sha256": hash, "operation_id": "op-call",
		}},
		{"listing", listing, map[string]any{"type": identitybroker.CallEntryType, "server": "calendar", "method": "tools/list"}},
	} {
		claims := gatewayTokenClaims(t, c.auth)
		if want := []any{c.entry}; !reflect.DeepEqual(claims["authorization_details"], want) {
			t.Errorf("the %s token's authorization_details = %v, want %v", c.name, claims["authorization_details"], want)
		}
		iat, _ := claims.GetIssuedAt()
		exp, _ := claims.GetExpirationTime()
		if iat == nil || exp == nil || exp.Sub(iat.Time) != lifetime {
			t.Errorf("the %s token lives from %v to %v, want %v", c.name, iat, exp, lifetime)
		}
	}

	// The listing token opens a session but cannot start a tools/call.
	session, status, body := servedVMCP(t, gw.api, listing.GatewayToken.Value(), "", mcpInitialize)
	if status != http.StatusOK || session == "" {
		t.Fatalf("initialize with the listing token: %d %s", status, body)
	}
	_, status, body = servedVMCP(t, gw.api, listing.GatewayToken.Value(), session, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"calendar.echo","arguments":{}}}`)
	if status != http.StatusForbidden || !strings.Contains(body, "method_mismatch") {
		t.Fatalf("tools/call with the listing token: %d %s, want 403 method_mismatch", status, body)
	}
	// The refusal did not spend the listing token, which ends its session.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, gw.api+gatewayVMCPPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+listing.GatewayToken.Value())
	req.Header.Set("Mcp-Session-Id", session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("DELETE of the listing token's session: %d", resp.StatusCode)
	}

	// Past its exp, a token that started no call is refused.
	time.Sleep(time.Until(call.ExpiresAt.Add(time.Second)))
	if _, status, body := servedVMCP(t, gw.api, call.GatewayToken.Value(), "", mcpInitialize); status != http.StatusUnauthorized {
		t.Fatalf("initialize with an expired gateway token: %d %s, want 401", status, body)
	}
}

// mcpInitialize is an MCP initialize request.
const mcpInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mecak8s-test","version":"1.0"}}}`

// servedVMCP posts body to the served vMCP endpoint with token, in session
// when set, and returns the session the answer names, its status, and its
// body.
func servedVMCP(t *testing.T, api, token, session, body string) (string, int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, api+gatewayVMCPPath, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("served vMCP: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode == http.StatusOK && !json.Valid(raw) && !strings.Contains(string(raw), "data:") {
		t.Fatalf("served vMCP answered 200 with %q", raw)
	}
	return resp.Header.Get("Mcp-Session-Id"), resp.StatusCode, string(raw)
}

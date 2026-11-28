package main

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// TestIdentityBrokerBindsEachGatewayTokenToItsCall runs #2727 through
// mecak8s's real startup and served vMCP endpoint, offline: every gateway
// token the identity broker obtains carries the call entry it sent AS-1,
// copied by the real AS-2, and expires no later than its ID-JAG; and a
// listing token cannot start a tools/call at the served vMCP. The calls
// themselves are refused by the stand-in (TestIdentityBrokerGatewayCallAgainstRealAS1),
// so a call outliving its token is covered against the real AS-2 and vMCP
// by the identity broker's TestRealVMCPCancelsACallThatOutlivesItsToken. It
// is not parallel: the protected route's client secret is read from an
// environment variable.
func TestIdentityBrokerBindsEachGatewayTokenToItsCall(t *testing.T) {
	gw := serveGatewayIdentityBroker(t, notesRoute(t), nil, nil)
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

	// Each token carries the entry the broker sent, and expires no later than
	// the ID-JAG it was redeemed from.
	issued := gw.idjags(t)
	if len(issued) != 2 {
		t.Fatalf("AS-1 issued %d ID-JAGs, want one per call", len(issued))
	}
	for i, c := range []struct {
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
		exp, _ := claims.GetExpirationTime()
		idjagExp, _ := issued[i].GetExpirationTime()
		if exp == nil || idjagExp == nil || exp.After(idjagExp.Time) {
			t.Errorf("the %s token expires at %v, want no later than its ID-JAG at %v", c.name, exp, idjagExp)
		}
	}

	// The listing token opens a session but cannot start a tools/call.
	session, status, body := servedVMCP(t, http.MethodPost, gw.api, listing.GatewayToken.Value(), "", mcpInitialize)
	if status != http.StatusOK || session == "" {
		t.Fatalf("initialize with the listing token: %d %s", status, body)
	}
	_, status, body = servedVMCP(t, http.MethodPost, gw.api, listing.GatewayToken.Value(), session, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"calendar.echo","arguments":{}}}`)
	if status != http.StatusForbidden || !strings.Contains(body, "method_mismatch") {
		t.Fatalf("tools/call with the listing token: %d %s, want 403 method_mismatch", status, body)
	}
	// The refusal did not spend the listing token, which ends its session.
	if _, status, body := servedVMCP(t, http.MethodDelete, gw.api, listing.GatewayToken.Value(), session, ""); status >= 300 {
		t.Fatalf("DELETE of the listing token's session: %d %s", status, body)
	}
}

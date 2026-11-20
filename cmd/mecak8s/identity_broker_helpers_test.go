package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

// helperGatewayScript has the main agent delegate to a default subagent,
// which tries a non-gateway broker tool, then calls the gateway route's tool;
// a second run has the main agent call that tool itself.
const helperGatewayScript = `{"turns":[
  {"tool_calls":[{"id":"delegate","name":"Subagent","args":{"prompt":"call the calendar"}}]},
  {"tool_calls":[{"id":"child-notes","name":"mcp__notes__ping","args":{}}]},
  {"tool_calls":[{"id":"child-call","name":"mcp__calendar__echo","args":{"a":1}}]},
  {"text":"child done"},
  {"text":"parent done"},
  {"tool_calls":[{"id":"main-call","name":"mcp__calendar__echo","args":{"a":1}}]},
  {"text":"done"}
]}`

// TestIdentityBrokerHelperGatewayCallCarriesItsOwnIdentity is #2555's slice
// test through mecak8s's startup, offline: a subagent the main agent creates
// is registered as its own agent, gets the session's gateway tools but no
// other broker tool, and its gateway call reaches AS-1 with its own SPIFFE ID
// as the actor, not the main agent's. The helper's call names the run its
// reported delegation started (#2557), which the broker's run and owner
// checks refuse without that report. Its stand-ins are listed on
// TestIdentityBrokerGatewayCallAgainstRealAS1.
func TestIdentityBrokerHelperGatewayCallCarriesItsOwnIdentity(t *testing.T) {
	script := filepath.Join(t.TempDir(), "script.json")
	if err := os.WriteFile(script, []byte(helperGatewayScript), 0o600); err != nil {
		t.Fatal(err)
	}
	gw := serveGatewayIdentityBroker(t, notesRoute(t), []string{"--mock-script", script}, nil)
	api, alice := gw.api, gw.alice

	created := createSession(t, api, alice)
	toolResult := func(id session.SessionID, call session.ToolCallID) session.ToolResult {
		t.Helper()
		return gw.toolResult(t, id, call)
	}

	promptApprovingAsks(t, api, alice, created, "delegate", nil)
	child := session.SessionID("subagent-" + created + "-delegate")
	// A non-gateway broker tool acts as the session, so the helper's catalog
	// has none. Its authority, derived from the main agent's, still names the
	// tool, so the engine reports it unavailable rather than unknown.
	if res := toolResult(child, "child-notes"); !res.IsError || !strings.Contains(res.Content, "currently unavailable") {
		t.Fatalf("the helper's non-gateway broker call = %+v, want a tool missing from its catalog", res)
	}
	if res := toolResult(child, "child-call"); strings.HasPrefix(res.Content, "MCP gateway call") || strings.Contains(res.Content, "401") {
		t.Fatalf("the helper's gateway call was refused: %q", res.Content)
	}
	promptApprovingAsks(t, api, alice, created, "call it yourself", nil)
	if res := toolResult(session.SessionID(created), "main-call"); strings.HasPrefix(res.Content, "MCP gateway call") || strings.Contains(res.Content, "401") {
		t.Fatalf("the main agent's gateway call was refused: %q", res.Content)
	}

	// Two ID-JAGs: the helper's, then the main agent's, each naming its own
	// agent as the actor.
	issued := gw.idjags(t)
	if len(issued) != 2 {
		t.Fatalf("AS-1 issued %d ID-JAGs, want one for the helper and one for the main agent", len(issued))
	}
	actor := func(claims map[string]any) string {
		act, _ := claims["act"].(map[string]any)
		sub, _ := act["sub"].(string)
		return sub
	}
	helperActor, mainActor := actor(issued[0]), actor(issued[1])
	if helperActor == "" || helperActor == mainActor {
		t.Fatalf("the helper's ID-JAG actor %q, the main agent's %q; want two distinct agents", helperActor, mainActor)
	}
	if issued[0]["sub"] != issued[1]["sub"] {
		t.Fatalf("the ID-JAG subjects %v and %v differ; both act for Alice", issued[0]["sub"], issued[1]["sub"])
	}
}

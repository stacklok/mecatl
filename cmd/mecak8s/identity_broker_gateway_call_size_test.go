package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// TestIdentityBrokerRefusesAGatewayCallTooLargeForTheVMCP runs a session's
// gateway tool calls through mecak8s's real startup, offline, with
// arguments on either side of the MCP broker's cap
// (stacklok/mecatl#3009). One byte over it, the call is refused as
// refused_arguments before anything is authorized: no ID-JAG is issued and
// nothing reaches the vMCP. At the cap, the call is authorized and sent, and
// the vMCP's call binding refuses the stand-in as in the other gateway tests.
// That refusal comes before the MCP SDK's handler reads the body, so the
// identity broker's TestRealVMCPRefusesACallTooLargeForItBeforeRunningIt,
// with a routed backend, pins the cap against both of the vMCP's body
// limits. The arguments are too large for a --mock-script file, so the
// scripted model is set directly. It is not parallel: the protected route's
// client secret is read from an environment variable.
func TestIdentityBrokerRefusesAGatewayCallTooLargeForTheVMCP(t *testing.T) {
	echo := func(id string, size int64) mockllm.Turn {
		const open, closing = `{"pad":"`, `"}`
		args := open + strings.Repeat("x", int(size)-len(open)-len(closing)) + closing
		return mockllm.ToolCallTurn(session.NewToolCall(session.ToolCallID(id), "mcp__calendar__echo", json.RawMessage(args)))
	}
	model := mockllm.New(
		echo("call-over-cap", mcpbroker.MaxGatewayArgumentBytes+1),
		echo("call-at-cap", mcpbroker.MaxGatewayArgumentBytes),
		mockllm.TextTurn("done"),
	)
	diag := &gatewayCallDiagnostics{}
	gw := serveGatewayIdentityBroker(t, notesRoute(t), []string{"--mock"}, func(cfg *app.Config) {
		cfg.Diagnostics, cfg.MockProvider = diag, model
	})
	created := createSession(t, gw.api, gw.alice)
	if asks := promptApprovingAsks(t, gw.api, gw.alice, created, "call the calendar twice", nil); asks != 0 {
		t.Fatalf("the run asked %d times, want none", asks)
	}

	ops := diag.operations(created)
	if len(ops) != 2 {
		t.Fatalf("gateway call diagnostics name operations %v, want the two calls'", ops)
	}
	if over := diag.call(ops[0]); over["code"] != "refused_arguments" || over["attempts"] != "" ||
		over["cause"] != "mcpbroker: arguments are too large for the gateway" {
		t.Fatalf("the call over the cap = %v, want refused_arguments for its size, with no attempt", over)
	}
	var exchanges []string
	for _, r := range gw.as1.Requests() {
		if r.Kind == identityharness.KindIDJAG {
			exchanges = append(exchanges, r.Operation)
		}
	}
	if len(exchanges) != 1 || exchanges[0] != ops[1] {
		t.Fatalf("ID-JAG exchanges for operations %v, want one, for the call at the cap (%s)", exchanges, ops[1])
	}
	if reasons := gw.refusals.reasons(ops[0]); len(reasons) != 0 {
		t.Fatalf("the vMCP refused the call over the cap for %v, want it never sent", reasons)
	}
	gw.wantStandInRefusal(t, ops[1], diag.call(ops[1]))
}

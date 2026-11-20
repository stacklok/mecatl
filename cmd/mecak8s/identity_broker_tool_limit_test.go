package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

// TestIdentityBrokerNamedHelperGetsItsDefinitionsGatewayTools runs subagents
// that run named definitions through mecak8s's startup, the real AS-1 and
// AS-2, and the embedded vMCP (#2842). One referencing the gateway's server
// calls its tool under its own identity; one that excludes the tool by name
// and one that references no server do not get it, so they never reach the
// gateway, matching the ceilings the broker resolved for them. Its stand-ins
// are listed on TestIdentityBrokerGatewayCallAgainstRealAS1.
func TestIdentityBrokerNamedHelperGetsItsDefinitionsGatewayTools(t *testing.T) {
	agents := t.TempDir()
	for name, frontmatter := range map[string]string{
		"calendar-reader": "mcpServers: [calendar]\n",
		"no-echo":         "mcpServers: [calendar]\ndisallowedTools: [mcp__calendar__echo]\n",
		"no-servers":      "",
	} {
		content := "---\nname: " + name + "\ndescription: A " + name + " specialist.\n" + frontmatter + "---\nYou help.\n"
		if err := os.WriteFile(filepath.Join(agents, name+".md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d := serveDelegations(t, "--agents-dir", agents)
	subagent := func(id, agent string) string {
		return `{"tool_calls":[{"id":"` + id + `","name":"Subagent","args":{"prompt":"call the calendar","agent":"` + agent + `"}}]}`
	}
	// Each child calls the tool, then answers; the main agent starts the
	// next child.
	d.writeScript(t, gatewayTurn("main-call"),
		subagent("reader", "calendar-reader"), gatewayTurn("reader-call"), scriptText,
		subagent("excluded", "no-echo"), gatewayTurn("excluded-call"), scriptText,
		subagent("serverless", "no-servers"), gatewayTurn("serverless-call"), scriptText)
	promptApprovingAsks(t, d.api, d.alice, d.session, "go", nil)

	// Only the main agent's and the calendar reader's calls reached the
	// gateway, the reader's under its own identity.
	d.wantHelperGatewayCalls(t, 1)
	if ops := d.diag.operations(d.session); len(ops) != 2 {
		t.Fatalf("%d gateway calls, want the main agent's and the calendar reader's", len(ops))
	}
	result := func(id session.SessionID, call session.ToolCallID) session.ToolResult {
		t.Helper()
		return d.toolResult(t, id, call)
	}
	for delegate, call := range map[session.ToolCallID]session.ToolCallID{"excluded": "excluded-call", "serverless": "serverless-call"} {
		// The parent's Subagent result names the child's session.
		_, child, ok := strings.Cut(result(session.SessionID(d.session), delegate).Content, "agentId: ")
		if !ok {
			t.Fatalf("the %s Subagent result names no child session", delegate)
		}
		child, _, _ = strings.Cut(child, "\n")
		if res := result(session.SessionID(child), call); !res.IsError {
			t.Fatalf("%s's gateway tool call = %+v, want an error for a tool missing from its catalog", delegate, res)
		}
	}
}

// TestIdentityBrokerNamedTeamMemberGetsItsDefinitionsGatewayTools pins the
// same for a team member that runs a named definition: one referencing the
// gateway's server calls its tool under its own identity; one that excludes
// the tool never reaches the gateway.
func TestIdentityBrokerNamedTeamMemberGetsItsDefinitionsGatewayTools(t *testing.T) {
	agents := t.TempDir()
	for name, frontmatter := range map[string]string{
		"calendar-reader": "mcpServers: [calendar]\n",
		"no-echo":         "mcpServers: [calendar]\ndisallowedTools: [mcp__calendar__echo]\n",
	} {
		content := "---\nname: " + name + "\ndescription: A " + name + " specialist.\n" + frontmatter + "---\nYou help.\n"
		if err := os.WriteFile(filepath.Join(agents, name+".md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	team := func(agent string) string {
		return `{"tool_calls":[{"id":"team","name":"Team","args":{"goal":"call the calendar","members":[{"name":"member","role":"call","agent":"` + agent + `"}]}}]}`
	}

	t.Run("referencing the server", func(t *testing.T) {
		d := serveDelegations(t, "--agents-dir", agents)
		d.writeScript(t, gatewayTurn("main-call"), team("calendar-reader"), gatewayTurn("member-call"), scriptText)
		promptApprovingAsks(t, d.api, d.alice, d.session, "go", nil)
		d.wantHelperGatewayCalls(t, 1)
	})

	t.Run("excluding the tool", func(t *testing.T) {
		d := serveDelegations(t, "--agents-dir", agents)
		d.writeScript(t, gatewayTurn("main-call"), team("no-echo"), gatewayTurn("member-call"), scriptText)
		promptApprovingAsks(t, d.api, d.alice, d.session, "go", nil)
		if ops := d.diag.operations(d.session); len(ops) != 1 {
			t.Fatalf("%d gateway calls, want only the main agent's", len(ops))
		}
	})
}

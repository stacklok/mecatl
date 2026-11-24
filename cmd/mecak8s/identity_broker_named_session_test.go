package main

import (
	"context"
	"encoding/json"
	"iter"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server/as1servertest"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// offeredTools is a mock model that records the tools each request offers,
// in order, and answers from inner.
type offeredTools struct {
	inner    port.LLMProvider
	mu       sync.Mutex
	requests [][]string
}

func (o *offeredTools) Capabilities() port.ProviderCapabilities { return o.inner.Capabilities() }

func (o *offeredTools) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	names := make([]string, 0, len(req.Tools))
	for _, spec := range req.Tools {
		names = append(names, spec.Name)
	}
	o.mu.Lock()
	o.requests = append(o.requests, names)
	o.mu.Unlock()
	return o.inner.Stream(ctx, req)
}

// since returns the tool lists of the requests after the first n.
func (o *offeredTools) since(n int) [][]string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.requests[n:])
}

func (o *offeredTools) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.requests)
}

// TestIdentityBrokerNamedSessionGetsItsDefinitionsGatewayTools runs sessions
// created with a named agent definition through mecak8s's startup, the real
// AS-1 and AS-2, and the embedded vMCP (#2938). The main agent of a session
// whose definition references the gateway's server is offered its tool and
// calls it under its own identity; it is never offered the notes server's
// tool, a broker route that acts as the session. One whose definition
// excludes the tool by name, and one that references no server, are never
// offered it, so their calls never reach the gateway. Its stand-ins are
// listed on TestIdentityBrokerGatewayCallAgainstRealAS1.
func TestIdentityBrokerNamedSessionGetsItsDefinitionsGatewayTools(t *testing.T) {
	agents := t.TempDir()
	for name, frontmatter := range map[string]string{
		"calendar-reader": "mcpServers: [calendar, notes]\n",
		"no-echo":         "mcpServers: [calendar]\ndisallowedTools: [mcp__calendar__echo]\n",
		"no-servers":      "",
	} {
		content := "---\nname: " + name + "\ndescription: A " + name + " specialist.\n" + frontmatter + "---\nYou help.\n"
		if err := os.WriteFile(filepath.Join(agents, name+".md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(t.TempDir(), "script.json")
	model := &offeredTools{inner: newLateScript(script)}
	diag := &gatewayCallDiagnostics{}
	// Named definitions run only in sessions with a workspace, and the broker
	// allows one only through a placement provider, so sessions use the Redis
	// filesystem.
	mr := miniredis.RunT(t)
	gw := serveGatewayIdentityBroker(t, notesRoute(t),
		[]string{"--agents-dir", agents, "--redis-url", mr.Addr(), "--redis-allow-plaintext", "--redis-filesystem"},
		func(cfg *app.Config) {
			cfg.MockProvider = model
			cfg.Diagnostics = diag
		})
	// createNamed creates a session running agent. AS-1 converts an access
	// token at one session open, so Alice signs in for each, as mecatui gets
	// a fresh token for each; it returns the session and her token.
	createNamed := func(agent string) (string, string) {
		t.Helper()
		token := gw.as1Server.SignIn(t, as1servertest.SignIn{User: identityharness.Alice}).Value()
		status, body := apiCall(t, http.MethodPost, gw.api+"/v1/sessions", token, `{"agent_definition_name":"`+agent+`"}`)
		var created struct {
			ID    string `json:"session_id"`
			Agent string `json:"resolved_agent_definition_name"`
		}
		if status != http.StatusCreated && status != http.StatusOK || json.Unmarshal([]byte(body), &created) != nil || created.ID == "" || created.Agent != agent {
			t.Fatalf("create a session running %s: status %d: %s", agent, status, body)
		}
		return created.ID, token
	}
	notesTurn := `{"tool_calls":[{"id":"reader-notes","name":"mcp__notes__ping","args":{}}]}`
	turns := append([]string{
		gatewayTurn("reader-call"), notesTurn, scriptText,
		gatewayTurn("excluded-call"), scriptText,
		gatewayTurn("serverless-call"), scriptText,
	}, slices.Repeat([]string{scriptText}, 20)...)
	if err := os.WriteFile(script, []byte(`{"turns":[`+strings.Join(turns, ",")+`]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// offered fails t unless every request since the first n offered the
	// gateway's echo tool exactly when want says, and never the notes tool.
	offered := func(n int, sessionName string, want bool) {
		t.Helper()
		requests := model.since(n)
		if len(requests) == 0 {
			t.Fatalf("%s: the model got no request", sessionName)
		}
		for i, tools := range requests {
			if slices.Contains(tools, "mcp__calendar__echo") != want || slices.Contains(tools, "mcp__notes__ping") {
				t.Fatalf("%s: request %d offered %v; want mcp__calendar__echo %v and never mcp__notes__ping", sessionName, i+1, tools, want)
			}
		}
	}

	reader, token := createNamed("calendar-reader")
	n := model.count()
	promptApprovingAsks(t, gw.api, token, reader, "call the calendar", nil)
	offered(n, "calendar-reader", true)
	ops := diag.operations(reader)
	if len(ops) != 1 {
		t.Fatalf("calendar-reader made %d gateway calls, want 1", len(ops))
	}
	if got := diag.call(ops[0]); got["code"] != "completed" || got["outcome"] != "completed" {
		t.Fatalf("calendar-reader's gateway call = %v, want completed at the vMCP", got)
	}
	if issued := gw.idjags(t); len(issued) != 1 || issued[0]["act"] == nil {
		t.Fatalf("AS-1 issued %d ID-JAGs, want one naming the session's main agent", len(issued))
	}
	// The vMCP holds no calendar credential offline, so the backend's own
	// answer is an error; the call reaching it is what counts.
	if res := gw.toolResult(t, session.SessionID(reader), "reader-call"); strings.Contains(res.Content, "unknown tool") {
		t.Fatalf("calendar-reader's gateway call result = %+v, want the tool in its catalog", res)
	}
	if res := gw.toolResult(t, session.SessionID(reader), "reader-notes"); !res.IsError {
		t.Fatalf("calendar-reader's notes call = %+v, want an error for a tool missing from its catalog", res)
	}

	for _, tc := range []struct{ agent, call string }{
		{"no-echo", "excluded-call"},
		{"no-servers", "serverless-call"},
	} {
		id, token := createNamed(tc.agent)
		n := model.count()
		promptApprovingAsks(t, gw.api, token, id, "call the calendar", nil)
		offered(n, tc.agent, false)
		if ops := diag.operations(id); len(ops) != 0 {
			t.Fatalf("%s made %d gateway calls, want none", tc.agent, len(ops))
		}
		if res := gw.toolResult(t, session.SessionID(id), session.ToolCallID(tc.call)); !res.IsError {
			t.Fatalf("%s's gateway call = %+v, want an error for a tool missing from its catalog", tc.agent, res)
		}
	}
	if issued := gw.idjags(t); len(issued) != 1 {
		t.Fatalf("AS-1 issued %d ID-JAGs, want only calendar-reader's", len(issued))
	}
}

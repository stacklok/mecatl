// Package memory_test holds the engine+memory INTEGRATION tests: they drive a
// real agent.Engine against the REAL file-backed memory store and its tools
// (SearchMemory, the RememberUser user-model tools). They live here (not in
// engine/agent) because the adapter under test is this package — the engine
// tree stays self-contained, importing no internal/ package even from tests;
// the engine-side seams (MemoryIndexSource, the Stop-review fork contract) are
// covered in engine/agent and engine/prompt with scripted fakes.
package memory_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// --- local copies of the engine test helpers ---------------------------------

func toolCall(id, name string, args string) session.ToolCall {
	return session.NewToolCall(session.ToolCallID(id), name, json.RawMessage(args))
}

func newSession(t *testing.T, limits session.Limits) *session.Session {
	t.Helper()
	return session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, limits, time.Unix(0, 0))
}

func catalogWith(t *testing.T, tools ...tool.Tool) *tool.Catalog {
	t.Helper()
	c := tool.NewCatalog()
	for _, tl := range tools {
		c.MustRegister(tl)
	}
	return c
}

func newEngine(d agent.Deps) *agent.Engine {
	if d.Policy == nil {
		d.Policy = permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil)
	}
	if d.Model == "" {
		d.Model = "test-model"
	}
	return agent.NewEngine(d)
}

func drain(r *agent.Run) []session.Event {
	var evs []session.Event
	for ev := range r.Events() {
		evs = append(evs, ev)
	}
	return evs
}

func typesOf(evs []session.Event) []session.EventType {
	out := make([]session.EventType, len(evs))
	for i, e := range evs {
		out[i] = e.Type
	}
	return out
}

func toolResultEvent(evs []session.Event) *session.ToolResult {
	for _, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			return ev.ToolResult
		}
	}
	return nil
}

func lastResult(t *testing.T, evs []session.Event) *session.ResultPayload {
	t.Helper()
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == session.EvResult {
			return evs[i].Result
		}
	}
	t.Fatalf("no result event in %v", typesOf(evs))
	return nil
}

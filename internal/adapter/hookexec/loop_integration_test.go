package hookexec_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
)

// This is the engine+hookexec INTEGRATION test (gauntlet #5): it drives a real
// agent.Engine with a REAL shell-exec hook runner and asserts the veto reaches
// the loop. It lives here (not in engine/agent) because the runner under test
// is this adapter — the engine tree stays self-contained, importing no
// internal/ package even from tests; the engine-side hook semantics are covered
// in engine/agent with scripted port.HookRunner fakes.

// --- local copies of the engine test helpers ---------------------------------

// fakeTool is an instrumented Tool whose execution body is controllable.
type fakeTool struct {
	name     string
	readOnly bool
	exec     func(ctx context.Context, in session.ToolCall, ws tool.Workspace) (session.ToolResult, error)
}

func (f *fakeTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: f.name, Description: f.name + ": test tool", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (f *fakeTool) ReadOnly() bool { return f.readOnly }
func (f *fakeTool) Execute(ctx context.Context, in session.ToolCall, ws tool.Workspace) (session.ToolResult, error) {
	return f.exec(ctx, in, ws)
}

func toolCall(id, name string, args string) session.ToolCall {
	return session.NewToolCall(session.ToolCallID(id), name, json.RawMessage(args))
}

func newSession(t *testing.T, limits session.Limits) *session.Session {
	t.Helper()
	return session.New("s1", session.ModeDefault, "/ws", limits, time.Unix(0, 0))
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

// TestPreToolUseHookBlocks runs an exit-2 PreToolUse hook and confirms the tool
// is not executed and the model receives the block message (gauntlet #5).
func TestPreToolUseHookBlocks(t *testing.T) {
	var executed atomic.Bool
	write := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			executed.Store(true)
			return session.NewToolResult(in.ID, "wrote"), nil
		}}
	hooks := hookexec.New(map[governance.HookPhase]string{
		governance.PhasePreToolUse: "echo blocked-by-policy >&2; exit 2",
	})
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Write", `{"path":"a"}`)),
		mockllm.TextTurn("ok"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, write), Hooks: hooks})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})

	evs := drain(r)

	var hookEv, blockRes bool
	var hookCallID session.ToolCallID
	// Indices of the three c1 events, to assert ordering: the card must open BEFORE
	// the veto (issue #6 — otherwise the failed update keys an unopened card).
	cardIdx, hookIdx, resultIdx := -1, -1, -1
	for i, ev := range evs {
		switch {
		case ev.Type == session.EvToolCall && ev.ToolCall != nil && ev.ToolCall.ID == "c1":
			if cardIdx == -1 {
				cardIdx = i
			}
		case ev.Type == session.EvHook && strings.Contains(ev.Text, "blocked-by-policy"):
			hookEv = true
			hookIdx = i
			if ev.Hook != nil {
				hookCallID = ev.Hook.CallID
			}
		case ev.Type == session.EvToolResult && ev.ToolResult.IsError &&
			strings.Contains(ev.ToolResult.Content, "blocked-by-policy"):
			blockRes = true
			resultIdx = i
		}
	}
	if executed.Load() {
		t.Fatalf("hook-blocked tool was executed")
	}
	if !hookEv {
		t.Fatalf("no hook event emitted")
	}
	// The blocked EvHook must carry the originating tool-call id so a client can
	// address the veto to the exact tool card (issue #6).
	if hookCallID != "c1" {
		t.Fatalf("blocked hook CallID = %q, want c1", hookCallID)
	}
	if !blockRes {
		t.Fatalf("block message not fed to the model as a tool result")
	}
	// Ordering: the card (EvToolCall) opens first, THEN the veto (EvHook), THEN the
	// synthesized error result — so both failure events land on an already-open card.
	if cardIdx == -1 {
		t.Fatalf("no EvToolCall opened for c1 (the card must open before the gate); events=%v", typesOf(evs))
	}
	if cardIdx >= hookIdx || hookIdx >= resultIdx {
		t.Fatalf("event order = card@%d, hook@%d, result@%d; want card < hook < result; events=%v",
			cardIdx, hookIdx, resultIdx, typesOf(evs))
	}
}

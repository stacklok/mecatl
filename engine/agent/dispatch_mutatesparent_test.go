package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestMutatesParentCallIsDispatchSerial proves a writable-Subagent CALL (which writes
// the parent workspace IN PLACE during its run, ADR 0041) does NOT overlap a sibling
// parent Read in the same turn: the writable child's Write and the sibling Read share
// an overlapTracker whose max concurrency must stay 1. SubagentTool.ReadOnly() is still
// true, but readBatchable excludes the writable call via MutatesParent, so it flushes
// alone via runOne — read-parallel/mutate-serial against the torn-read hazard.
func TestMutatesParentCallIsDispatchSerial(t *testing.T) {
	var tracker overlapTracker

	// The writable child's Write tool enters/leaves the shared tracker while it mutates
	// the real parent tree. If the writable call ran in the same concurrent batch as
	// the sibling Read, max concurrency would reach 2.
	writeTool := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			tracker.enter()
			time.Sleep(5 * time.Millisecond) // widen the overlap window
			tracker.leave()
			return session.NewToolResult(in.ID, "wrote"), nil
		}}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("w1", "Write", `{"path":"x.txt","content":"hi"}`)),
		mockllm.TextTurn("did the work"),
	)
	writable := childEngineWith(childLLM, catalogWith(t, writeTool))
	subagent := newWritableSubagent(t, writable)

	// A sibling parent Read that also touches the shared tracker.
	readTool := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			tracker.enter()
			time.Sleep(5 * time.Millisecond)
			tracker.leave()
			return session.NewToolResult(in.ID, "read ok"), nil
		}}

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(
			toolCall("p1", "Subagent", `{"prompt":"implement","mode":"read-write"}`),
			toolCall("p2", "Read", `{"path":"a"}`),
		),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, subagent, readTool)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	drain(r)

	if tracker.max() > 1 {
		t.Fatalf("a writable Subagent call overlapped a sibling Read (max concurrency %d); "+
			"it must be dispatch-serial (MutatesParent → flushed alone)", tracker.max())
	}
}

// TestReadOnlySubagentStaysBatchedWithSiblingRead proves the surgical scope: a
// read-ONLY Subagent call (mode unset) still batches/runs in parallel with a sibling
// read — only the writable call is excluded. The read-only child's tool and the
// sibling parent Read share a tracker; they MUST overlap (max >= 2).
func TestReadOnlySubagentStaysBatchedWithSiblingRead(t *testing.T) {
	var tracker overlapTracker
	bodies := make(chan struct{}, 2)
	gate := make(chan struct{})

	// The read-only child runs a tool that enters the tracker and parks on the gate,
	// so it is genuinely co-running when the sibling parent Read also enters.
	childTool := &fakeTool{name: "Look", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			tracker.enter()
			defer tracker.leave()
			bodies <- struct{}{}
			<-gate
			return session.NewToolResult(in.ID, "looked"), nil
		}}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Look", `{}`)),
		mockllm.TextTurn("child done"),
	)
	// A plain read-only Subagent (no writable wiring → MutatesParent always false).
	subagent := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, childTool)))

	readTool := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			tracker.enter()
			defer tracker.leave()
			bodies <- struct{}{}
			<-gate
			return session.NewToolResult(in.ID, "read ok"), nil
		}}

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(
			toolCall("p1", "Subagent", `{"prompt":"explore"}`),
			toolCall("p2", "Read", `{"path":"a"}`),
		),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, subagent, readTool)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	// Both the child's tool and the sibling Read must be running before either is
	// released → proves the read-only Subagent stayed in the parallel batch.
	<-bodies
	<-bodies
	close(gate)
	drain(r)

	if tracker.max() < 2 {
		t.Fatalf("a read-only Subagent call must stay batched/parallel with a sibling Read, "+
			"but max concurrency was %d", tracker.max())
	}
}

// TestSubagentMutatesParent unit-tests the MutatesParent predicate on SubagentTool and
// proves it is DECOUPLED from any merger (ADR 0041 — direct-write has no merge): with
// only the writable engine wired, read-write → true; read-only / "" → false; an unwired
// tool → false; malformed args → false.
func TestSubagentMutatesParent(t *testing.T) {
	writable := childEngineWith(mockllm.New(mockllm.TextTurn("x")), catalogWith(t))

	wired := newWritableSubagent(t, writable).(interface {
		MutatesParent(session.ToolCall) bool
	})

	cases := []struct {
		name string
		args string
		want bool
	}{
		{"read-write mutates", `{"prompt":"go","mode":"read-write"}`, true},
		{"read-only does not", `{"prompt":"go","mode":"read-only"}`, false},
		{"default mode does not", `{"prompt":"go"}`, false},
		{"malformed args", `{not json`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wired.MutatesParent(toolCall("p1", "Subagent", tc.args))
			if got != tc.want {
				t.Fatalf("MutatesParent(%s) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}

	// A tool with NO writable engine wired never runs writable, so MutatesParent is
	// false even for read-write (the call errors as unsupported later anyway).
	unwired := agent.NewSubagentTool(
		childEngineWith(mockllm.New(mockllm.TextTurn("ro")), catalogWith(t)),
	).(interface{ MutatesParent(session.ToolCall) bool })
	if unwired.MutatesParent(toolCall("p1", "Subagent", `{"prompt":"go","mode":"read-write"}`)) {
		t.Fatal("read-write with no writable engine wired must not report MutatesParent")
	}
}

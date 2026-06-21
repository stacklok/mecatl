package agent_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// trackingMerger records overlap via a shared overlapTracker so a test can assert
// the writable-Subagent merge never overlaps a sibling parent Read (FIX C: a
// merge-completing call is dispatch-serial, excluded from the concurrent read batch).
type trackingMerger struct {
	tracker *overlapTracker
}

func (m *trackingMerger) Merge(_ context.Context, _ string, _ tool.Workspace) error {
	m.tracker.enter()
	time.Sleep(5 * time.Millisecond) // widen the overlap window
	m.tracker.leave()
	return nil
}

// TestMutatesParentCallIsDispatchSerial proves a writable-Subagent CALL (which will
// merge into the parent at run end) does NOT overlap a sibling parent Read in the
// same turn: the merge and the sibling Read share an overlapTracker whose max
// concurrency must stay 1. SubagentTool.ReadOnly() is still true, but readBatchable
// excludes the merge-completing call via MutatesParent, so it flushes alone via
// runOne — restoring read-parallel/mutate-serial against the torn-read hazard.
func TestMutatesParentCallIsDispatchSerial(t *testing.T) {
	var tracker overlapTracker

	var rr atomic.Pointer[string]
	writable := writableChildWriting(t, "did the work", &rr)
	merger := &trackingMerger{tracker: &tracker}
	subagent := newWritableSubagent(t, writable, &memForker{}, merger)

	// A sibling parent Read that also touches the shared tracker. If it ran in the
	// same concurrent batch as the (merging) Subagent call, max concurrency would
	// reach 2.
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
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")
	drain(r)

	if merger.tracker.max() > 1 {
		t.Fatalf("a merge-completing Subagent call overlapped a sibling Read (max concurrency %d); "+
			"it must be dispatch-serial (MutatesParent → flushed alone)", merger.tracker.max())
	}
}

// TestReadOnlySubagentStaysBatchedWithSiblingRead proves the surgical scope of FIX C:
// a read-ONLY Subagent call (mode unset) still batches/runs in parallel with a sibling
// read — only the merge-completing call is excluded. The read-only child's tool and
// the sibling parent Read share a tracker; they MUST overlap (max >= 2).
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
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")

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

// TestSubagentMutatesParent unit-tests the MutatesParent predicate on SubagentTool:
// read-write (with writable engine + merger wired) → true; read-only / "" → false; a
// writable tool with no merger wired → false; malformed args → false.
func TestSubagentMutatesParent(t *testing.T) {
	var rr atomic.Pointer[string]
	writable := writableChildWriting(t, "x", &rr)

	wired := newWritableSubagent(t, writable, &memForker{}, &fakeMerger{}).(interface {
		MutatesParent(session.ToolCall) bool
	})

	cases := []struct {
		name string
		args string
		want bool
	}{
		{"read-write merges", `{"prompt":"go","mode":"read-write"}`, true},
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

	// A writable tool with NO merger wired never merges, so MutatesParent is false
	// even for read-write.
	noMerger := agent.NewSubagentTool(
		childEngineWith(mockllm.New(mockllm.TextTurn("ro")), catalogWith(t)),
		agent.WithWritableChildEngine(writable),
		agent.WithWritableChildForker(&memForker{}),
	).(interface{ MutatesParent(session.ToolCall) bool })
	if noMerger.MutatesParent(toolCall("p1", "Subagent", `{"prompt":"go","mode":"read-write"}`)) {
		t.Fatal("read-write with no merger wired must not report MutatesParent (it cannot merge)")
	}
}

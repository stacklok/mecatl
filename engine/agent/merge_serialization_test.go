package agent_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// testSerializingMerger is a local mirror of forker.SerializingMerger (engine/agent
// is layering-forbidden from importing the internal forker adapter). It wraps an
// inner tool.EnvironmentMerger with a single mutex so concurrent Merge calls serialize —
// the SAME shape composition injects into both Parallel and the writable Subagent.
type testSerializingMerger struct {
	mu    sync.Mutex
	inner tool.EnvironmentMerger
}

func (m *testSerializingMerger) Merge(ctx context.Context, child, parent tool.Environment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inner.Merge(ctx, child, parent)
}

// concurrentEntryDetector is an inner tool.EnvironmentMerger that FLAGS overlapping
// entries: a non-serializing wrapper would let two merges enter at once and trip
// the flag.
type concurrentEntryDetector struct {
	inFlight atomic.Int32
	overlap  atomic.Bool
}

func (d *concurrentEntryDetector) Merge(_ context.Context, _, _ tool.Environment) error {
	if d.inFlight.Add(1) > 1 {
		d.overlap.Store(true)
	}
	time.Sleep(time.Millisecond) // widen the overlap window
	d.inFlight.Add(-1)
	return nil
}

// mergingReadOnlyTool is a ReadOnly tool whose Execute drives a shared
// tool.EnvironmentMerger — modelling Parallel's auto-merge and the writable Subagent's
// post-run merge, both of which call the SAME composition-injected (serialized)
// merger from inside a ReadOnly tool's execution.
type mergingReadOnlyTool struct {
	name   string
	merger tool.EnvironmentMerger
}

func (t *mergingReadOnlyTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: t.name, Description: t.name, Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*mergingReadOnlyTool) ReadOnly() bool { return true }
func (t *mergingReadOnlyTool) Execute(ctx context.Context, in session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	child := agent.MemEnv("/fork/" + t.name)
	if err := t.merger.Merge(ctx, child, env); err != nil {
		return session.NewToolError(in.ID, err.Error()), nil
	}
	return session.NewToolResult(in.ID, t.name+" merged"), nil
}

// TestSharedMergerSerializesAcrossReadParallelBatch is the RACE REGRESSION test:
// two ReadOnly tools that each call the SAME serializing merger are dispatched in
// ONE read-parallel batch (the dispatcher fans read-only tools out concurrently).
// With the serializing wrapper, the inner merger never sees overlapping entry; the
// test would trip the overlap flag without it. Run under -race.
func TestSharedMergerSerializesAcrossReadParallelBatch(t *testing.T) {
	detector := &concurrentEntryDetector{}
	shared := &testSerializingMerger{inner: detector}

	toolA := &mergingReadOnlyTool{name: "MergeA", merger: shared}
	toolB := &mergingReadOnlyTool{name: "MergeB", merger: shared}
	cat := catalogWith(t, toolA, toolB)

	// One assistant turn issues BOTH read-only tool calls, so the dispatcher fans
	// them out concurrently (read-parallel). The second turn ends the run.
	llm := mockllm.New(
		mockllm.ToolCallTurn(
			toolCall("c1", "MergeA", `{}`),
			toolCall("c2", "MergeB", `{}`),
		),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	_ = drain(r)

	if detector.overlap.Load() {
		t.Fatal("the shared serializing merger allowed two Merge calls to overlap; serialization is broken")
	}
}

package memory_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/memory"
)

// TestSearchMemoryToolThroughLoop exercises the SearchMemory tool end-to-end
// through the real Engine loop on the offline mockllm: the model emits a
// SearchMemory tool call, the engine dispatches it against a real (memfs-free,
// on-disk under t.TempDir) memory store seeded with entries, and the ranked
// result flows back as a tool result before the run completes.
//
// It asserts the three things that prove the tool is genuinely wired into the
// loop (not just unit-tested in isolation): (a) an EvToolCall for SearchMemory is
// emitted, (b) a non-error EvToolResult comes back whose content contains the
// best-matching key pref/test-runner, and (c) the run reaches a terminal
// end_turn result.
func TestSearchMemoryToolThroughLoop(t *testing.T) {
	store, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	ctx := context.Background()
	// Seed a few entries; only pref/test-runner overlaps the query terms.
	for _, kv := range [][3]string{
		{"pref/test-runner", "preferred test runner", "Run tests with gotestsum"},
		{"pref/editor", "favourite editor", "vim"},
		{"project/tracker", "issue tracker", "issues live in Linear"},
	} {
		if err := store.RememberEntry(ctx, tool.MemoryEntry{Key: kv[0], Description: kv[1], Value: kv[2]}); err != nil {
			t.Fatalf("seed %q: %v", kv[0], err)
		}
	}

	cat := catalogWith(t, memory.NewSearchMemoryTool(store))

	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", memory.SearchMemoryToolName, `{"query":"preferred test runner"}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat})
	ws := memfs.NewWorkspace("/ws")
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, ws, memledger.New(), nil)
	r := e.Run(ctx, newSession(t, session.Limits{}), env, agent.RunRequest{Text: "find the test runner pref"})
	evs := drain(r)

	// (a) The SearchMemory tool call must be emitted.
	var sawCall bool
	for _, ev := range evs {
		if ev.Type == session.EvToolCall && ev.ToolCall != nil && ev.ToolCall.Name == memory.SearchMemoryToolName {
			sawCall = true
		}
	}
	if !sawCall {
		t.Fatalf("no EvToolCall for %q in %v", memory.SearchMemoryToolName, typesOf(evs))
	}

	// (b) A non-error tool result must flow back carrying the best match.
	res := toolResultEvent(evs)
	if res == nil {
		t.Fatalf("no tool result event in %v", typesOf(evs))
		return
	}
	if res.IsError {
		t.Fatalf("SearchMemory result is an error: %q", res.Content)
	}
	if !strings.Contains(res.Content, "pref/test-runner") {
		t.Fatalf("SearchMemory result does not contain the best-matching key; content = %q", res.Content)
	}

	// (c) The run completes.
	if got := lastResult(t, evs).Stop; got != session.StopEndTurn {
		t.Fatalf("stop = %q, want end_turn", got)
	}
}

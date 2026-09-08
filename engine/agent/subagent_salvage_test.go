package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// salvageReadTool is a read-only child tool whose Execute always succeeds — a child
// that keeps calling it never produces a final text answer, so it exhausts its
// turn/tool-call budget with an EMPTY summary (the issue #48 failure mode).
func salvageReadTool() tool.Tool {
	return &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "read ok"), nil
		}}
}

// TestSubagentSalvagesPartialSummaryOnMaxTurns is the issue #48 MODEL-FACING e2e: a
// free-text child burns its whole turn budget fetching (never emitting text) and trips
// StopMaxTurns. The salvage drives ONE wrap-up turn (post-Reopen) in which the child
// finally summarizes. The parent's ToolResult must carry that summary AND still bear the
// honest "[subagent stopped: reached its max-turns limit]" note — never the empty
// "(subagent produced no summary)".
func TestSubagentSalvagesPartialSummaryOnMaxTurns(t *testing.T) {
	// max_turns=2 → two tool-call turns trip the limit; cursor index 2 (the text turn)
	// is consumed by the bounded salvage drive after Reopen.
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k", "Read", `{"path":"a"}`)),
		mockllm.ToolCallTurn(toolCall("k", "Read", `{"path":"b"}`)),
		mockllm.TextTurn("SALVAGED PARTIAL FINDINGS"),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t, salvageReadTool()))
	task := agent.NewSubagentTool(childEngine)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate","max_turns":2}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].IsError {
		t.Fatalf("salvaged limit stop is a success-with-note, got error: %+v", results[0])
	}
	body := results[0].Content
	if strings.Contains(body, "(subagent produced no summary)") {
		t.Fatalf("salvage should have filled in a body, got the empty placeholder: %q", body)
	}
	if !strings.Contains(body, "SALVAGED PARTIAL FINDINGS") {
		t.Fatalf("result must carry the salvaged summary, got: %q", body)
	}
	if !strings.Contains(body, "reached its max-turns limit") {
		t.Fatalf("result must keep the honest max-turns note, got: %q", body)
	}
	// The salvage spends exactly ONE extra model call: 2 limit turns + 1 wrap-up.
	if got := childLLM.Calls(); got != 3 {
		t.Fatalf("child made %d model calls, want 3 (2 limit turns + 1 bounded salvage turn)", got)
	}
}

// TestSubagentSalvageDoesNotClobberExistingSummary is the guard: when the child DID
// produce a final summary before the limit tripped, the salvage must NOT run (no extra
// model call) and must NOT overwrite the real answer.
func TestSubagentSalvageDoesNotClobberExistingSummary(t *testing.T) {
	// The child answers with text on turn 2 (within max_turns=2) — a clean end, not a
	// limit stop with an empty body. No salvage turn should be consumed.
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k", "Read", `{"path":"a"}`)),
		mockllm.TextTurn("REAL SUMMARY"),
		mockllm.TextTurn("SALVAGE SHOULD NOT RUN"),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t, salvageReadTool()))
	task := agent.NewSubagentTool(childEngine)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate","max_turns":2}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("want 1 success result, got %+v", results)
	}
	if !strings.Contains(results[0].Content, "REAL SUMMARY") {
		t.Fatalf("must return the child's real summary, got: %q", results[0].Content)
	}
	if strings.Contains(results[0].Content, "SALVAGE SHOULD NOT RUN") {
		t.Fatalf("salvage must not run when the child already summarized, got: %q", results[0].Content)
	}
	// Exactly 2 model calls: one tool-call turn + the answering text turn. The third
	// scripted turn (the salvage sentinel) must remain unconsumed.
	if got := childLLM.Calls(); got != 2 {
		t.Fatalf("child made %d model calls, want 2 (no salvage drive)", got)
	}
}

// TestSubagentBudgetStopSalvages grants a budget-stopped free-text child exactly
// one cleanup turn. Its baseline is non-mutating: the cleanup spend is added to
// the existing lifetime main usage rather than replacing it.
func TestSubagentBudgetStopSalvages(t *testing.T) {
	store := memstore.New()
	childLLM := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(toolCall("k", "Read", `{"path":"a"}`)),
			mockllm.UsageChunk(session.Usage{InputTokens: 100, OutputTokens: 100}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk("SALVAGED BUDGET FINDINGS"),
			mockllm.UsageChunk(session.Usage{InputTokens: 7, OutputTokens: 3}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	childEngine := agent.NewEngine(agent.Deps{
		LLM:          childLLM,
		Catalog:      catalogWith(t, salvageReadTool()),
		Policy:       allowAll(),
		Model:        "child-model",
		MaxRunTokens: 50,
	})
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("want one clean salvaged result, got %+v", results)
	}
	if !strings.Contains(results[0].Content, "SALVAGED BUDGET FINDINGS") {
		t.Fatalf("result must carry the cleanup summary, got %q", results[0].Content)
	}
	if !strings.Contains(results[0].Content, "reached its token budget") {
		t.Fatalf("result must retain the original budget stop, got %q", results[0].Content)
	}
	if got := childLLM.Calls(); got != 2 {
		t.Fatalf("child made %d model calls, want initial turn plus one cleanup", got)
	}

	child, err := store.Load(context.Background(), session.SessionID("subagent-s1-p1"))
	if err != nil {
		t.Fatalf("load persisted child: %v", err)
	}
	const wantLifetime = 210
	if got := child.Usage.TotalTokens(); got != wantLifetime {
		t.Fatalf("lifetime Session.Usage = %d, want initial 200 + cleanup 10 = %d", got, wantLifetime)
	}
	if got := child.TokenUsageSnapshot()[session.UsageKindMain].Total.TotalTokens(); got != wantLifetime {
		t.Fatalf("lifetime token_usage[main] = %d, want %d", got, wantLifetime)
	}
}

// TestSubagentBudgetStopCleanupHasOneTurn proves the cleanup's MaxTurns=1 pin
// remains the hard brake even though its internal baseline makes one model turn
// available. A cleanup tool call cannot consume a second turn.
func TestSubagentBudgetStopCleanupHasOneTurn(t *testing.T) {
	childLLM := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(toolCall("k1", "Read", `{"path":"a"}`)),
			mockllm.UsageChunk(session.Usage{InputTokens: 100, OutputTokens: 100}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(toolCall("k2", "Read", `{"path":"b"}`)),
			mockllm.UsageChunk(session.Usage{InputTokens: 1, OutputTokens: 1}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("MUST NOT RUN"),
	)
	childEngine := agent.NewEngine(agent.Deps{
		LLM:          childLLM,
		Catalog:      catalogWith(t, salvageReadTool()),
		Policy:       allowAll(),
		Model:        "child-model",
		MaxRunTokens: 50,
	})
	task := agent.NewSubagentTool(childEngine)

	_, _ = subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate"}`)),
		mockllm.TextTurn("parent done"),
	)
	if got := childLLM.Calls(); got != 2 {
		t.Fatalf("child made %d model calls, want initial turn plus exactly one cleanup turn", got)
	}
}

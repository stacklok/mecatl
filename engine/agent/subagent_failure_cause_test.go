package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// chattyThenBrokenChild scripts the issue-#319 failure shape with an UNCOOPERATIVE
// provider: the child's first turn streams a chatty line AND a tool call (so the text is
// recorded and the loop continues), and the second turn streams a first chunk and THEN
// fails mid-stream with a provider error. That is exactly the production shape — a
// mid-stream break after a committing chunk is terminal (never retried) — and it is what
// made the bug so misleading: the loop captured the provider error on
// session.ResultPayload.Error while the Subagent result rendered the chatty line AS the
// failure reason.
func chattyThenBrokenChild(chatter string, cause error) *mockllm.Provider {
	return mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk(chatter),
			mockllm.ToolCallChunk(toolCall("k", "Read", `{"path":"a"}`)),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ErrorTurn(cause, mockllm.TextChunk("partial output before the break")),
	)
}

// failureCauseReadTool is the innocuous read-only child tool the scripted first turn
// calls so the turn is recorded and the child loop reaches a second turn.
func failureCauseReadTool() tool.Tool {
	return &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "read ok"), nil
		}}
}

// TestSubagentFailureSurfacesCauseNotLastChatLine is the PRIMARY regression test for
// issue #319, asserted MODEL-FACING: it drives a real parent loop whose Subagent child
// dies on a mid-stream provider error after having said something chatty, then asserts
// the parent's RECORDED tool result (what the model actually reads) leads with the
// provider cause and does NOT present the chatty line as the failure reason.
func TestSubagentFailureSurfacesCauseNotLastChatLine(t *testing.T) {
	const chatter = "Now let me check the tests."
	const causeText = "upstream 503: model overloaded"
	childLLM := chattyThenBrokenChild(chatter, errors.New(causeText))
	childEngine := childEngineWith(childLLM, catalogWith(t, failureCauseReadTool()))
	task := agent.NewSubagentTool(childEngine)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 {
		t.Fatalf("want 1 recorded tool result, got %d", len(results))
	}
	res := results[0]
	if !res.IsError {
		t.Fatalf("a crashed child must surface a tool error, got success:\n%s", res.Content)
	}
	// (a) The ACTIONABLE half is present.
	if !strings.Contains(res.Content, causeText) {
		t.Fatalf("the model-facing result must carry the provider cause %q, got:\n%s", causeText, res.Content)
	}
	// (b) The chatty line is NOT the failure reason. It may appear, but only behind the
	//     explicit context label — never as the leading body the model reads as "why".
	body := strings.TrimPrefix(res.Content, "Subagent: ")
	if strings.HasPrefix(body, chatter) {
		t.Fatalf("the child's last chat line must not lead the error body (issue #319), got:\n%s", res.Content)
	}
	if strings.Contains(res.Content, chatter) && !strings.Contains(res.Content, "Last activity before the failure: "+chatter) {
		t.Fatalf("the child's last text may only appear as labelled context, got:\n%s", res.Content)
	}
	// (c) The agentId trailer still rides every terminal, so the failure is not a dead end.
	if !strings.Contains(res.Content, "agentId: ") {
		t.Fatalf("the error result must keep the agentId trailer, got:\n%s", res.Content)
	}
}

// TestSubagentFailureWithNoChildTextStillSurfacesCause is the second half of the
// adversarial case: the child says NOTHING at all before the provider breaks. Before
// #319 that produced subagentErrorBody's opaque no-summary floor — the operator-visible
// symptom that started the investigation. The cause must be shown instead of the floor.
func TestSubagentFailureWithNoChildTextStillSurfacesCause(t *testing.T) {
	const causeText = "context deadline exceeded: stream idle for 180s"
	// One turn only: it streams a first chunk then breaks, so the child never records
	// any assistant text (RecordAssistant is never reached on the error path).
	childLLM := mockllm.New(
		mockllm.ErrorTurn(errors.New(causeText), mockllm.TextChunk("thinking")),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t))
	task := agent.NewSubagentTool(childEngine)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("want 1 errored tool result, got %+v", results)
	}
	res := results[0]
	if !strings.Contains(res.Content, causeText) {
		t.Fatalf("result must carry the cause even with no child text, got:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "failed without producing a summary") {
		t.Fatalf("the opaque floor must not stand when a cause is available, got:\n%s", res.Content)
	}
}

// TestSubagentTruncatedTurnStillSurfacesAReason is the terminateComplete-SHAPE sibling
// of the two tests above: the provider ends the turn on a terminal STOP CHUNK (no Go
// error) AFTER the child streamed visible text — the Anthropic max_tokens / refusal
// shape (both mapStop to StopError and yield a stop, not an error; unlike OpenAI's
// incomplete/failed, which return a Go error and take the terminate path). The loop's
// finishTurnNoTools routes a text-bearing StopError through terminateComplete, which
// used to emit an EMPTY cause — so subagentErrorBody fell through to rendering the
// child's truncated text AS the failure, the exact #319 presentation on a different
// terminal. The reason is now synthesised from the stop itself, so the model reads
// WHY (truncated by the provider) and the partial text is demoted to labelled context.
func TestSubagentTruncatedTurnStillSurfacesAReason(t *testing.T) {
	const truncated = "I was in the middle of analysing when the provider cut me"
	childLLM := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk(truncated),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopError), // a terminal stop chunk, NOT a Go error
		),
	)
	childEngine := childEngineWith(childLLM, catalogWith(t))
	task := agent.NewSubagentTool(childEngine)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("want 1 errored tool result, got %+v", results)
	}
	res := results[0]
	// The reason must be named — not the bare truncated text leading the body.
	body := strings.TrimPrefix(res.Content, "Subagent: ")
	if strings.HasPrefix(body, truncated) {
		t.Fatalf("the truncated text must not lead the error body (the terminateComplete #319 shape), got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "terminal stop reason") {
		t.Fatalf("the synthesised reason must name the terminal stop, got:\n%s", res.Content)
	}
	// The partial text may still appear, but only as labelled context.
	if strings.Contains(res.Content, truncated) && !strings.Contains(res.Content, "Last activity before the failure: "+truncated) {
		t.Fatalf("the truncated text may only appear as labelled context, got:\n%s", res.Content)
	}
}

// TestSubagentEndEventCarriesClampedCause pins the OBSERVABILITY half: the redacted
// subagent.end projection carries the cause (so a client — the mecatui fleet pane, or
// any gRPC consumer — can show WHY a delegation failed), and it is NORMALISED at the emit
// site — CLAMPED, so a pathological provider error body cannot dump unbounded bytes onto
// the event stream, and whitespace-COLLAPSED, because every consumer of this field is a
// single-line surface (a roster row, an ACP status line, a log line) while a real provider
// error body carries newlines. That contract is what lets those consumers just render the
// value instead of each re-deriving the collapse. It also pins that the cause is set on
// subagent.end ONLY.
func TestSubagentEndEventCarriesClampedCause(t *testing.T) {
	// Multi-line AND over-long: the two halves of the field's normalisation contract in
	// one adversarial input, driven through the real loop.
	huge := "BOOM-\n  upstream detail\n\t" + strings.Repeat("z", 5000)
	childLLM := mockllm.New(mockllm.ErrorTurn(errors.New(huge), mockllm.TextChunk("x")))
	childEngine := childEngineWith(childLLM, catalogWith(t))
	task := agent.NewSubagentTool(childEngine)

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	var ends, causesOnOtherKinds int
	var endCause string
	for _, ev := range drain(r) {
		if ev.Subagent == nil {
			continue
		}
		if ev.Type == session.EvSubagentEnd {
			ends++
			endCause = ev.Subagent.Cause
			continue
		}
		if ev.Subagent.Cause != "" {
			causesOnOtherKinds++
		}
	}
	if ends != 1 {
		t.Fatalf("want exactly 1 subagent.end, got %d", ends)
	}
	if causesOnOtherKinds != 0 {
		t.Fatalf("Cause is a subagent.end-only field, but %d other subagent.* events carried one", causesOnOtherKinds)
	}
	if !strings.Contains(endCause, "BOOM-") {
		t.Fatalf("subagent.end must carry the failure cause, got %q", endCause)
	}
	if n := len([]rune(endCause)); n > 401 { // maxSubagentCausePreview (400) + the ellipsis
		t.Fatalf("subagent.end cause was not clamped: %d runes", n)
	}
	// The line-oriented half of the contract: no newline, CR or tab reaches a consumer,
	// and the source lines are joined with a single space so nothing is lost.
	if strings.ContainsAny(endCause, "\n\r\t") {
		t.Fatalf("subagent.end cause must be collapsed to ONE line at the emit site, got %q", endCause)
	}
	if !strings.Contains(endCause, "BOOM- upstream detail ") {
		t.Fatalf("collapsing must join the source lines with single spaces, got %q", endCause)
	}
}

// TestSubagentSuccessCarriesNoCause is the negative guard: a clean delegation must leave
// the field empty, so a consumer can treat a non-empty Cause as "this failed".
func TestSubagentSuccessCarriesNoCause(t *testing.T) {
	childEngine := childEngineWith(mockllm.New(mockllm.TextTurn("all good")), catalogWith(t))
	task := agent.NewSubagentTool(childEngine)

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	for _, ev := range drain(r) {
		if ev.Type == session.EvSubagentEnd && ev.Subagent != nil && ev.Subagent.Cause != "" {
			t.Fatalf("a clean subagent.end must carry no cause, got %q", ev.Subagent.Cause)
		}
	}
}

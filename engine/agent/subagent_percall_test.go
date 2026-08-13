package agent_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestSubagentPerCallMaxTurnsTightens is the MODEL-FACING e2e: a Subagent call that supplies
// max_turns lower than the default makes its child STRICTER for that call. The child
// would loop far longer (20 scripted tool-call turns) but the per-call max_turns=3 caps
// it at exactly 3 model calls.
func TestSubagentPerCallMaxTurnsTightens(t *testing.T) {
	boundedEngine, boundedLLM := readLoopChild(t, 20)
	task := agent.NewSubagentTool(boundedEngine)

	subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"loop","max_turns":3}`)),
		mockllm.TextTurn("parent done"),
	)
	// 3 turns trip max_turns; the child never emits text, so the issue-#48 salvage drives
	// ONE more bounded wrap-up turn (also tool-only here) → 3 + 1 = 4 model calls. The
	// per-call cap is still proven: without it the child would loop all 20 scripted turns.
	if got := boundedLLM.Calls(); got != 4 {
		t.Fatalf("child made %d model calls, want 4 (max_turns=3 + 1 bounded salvage turn)", got)
	}
}

// TestSubagentPerCallMaxToolCallsTightens proves the per-call max_tool_calls override caps
// the child's total tool invocations. With max_tool_calls=2 and a child that would call
// a tool every turn, the child stops after the tool-call limit trips.
func TestSubagentPerCallMaxToolCallsTightens(t *testing.T) {
	boundedEngine, boundedLLM := readLoopChild(t, 20)
	task := agent.NewSubagentTool(boundedEngine)

	subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"loop","max_tool_calls":2}`)),
		mockllm.TextTurn("parent done"),
	)
	// Each turn issues exactly one tool call; MaxToolCalls=2 stops the run at the next
	// turn boundary once 2 tool calls are recorded — turn 1 (1 call) + turn 2 (2 calls,
	// limit reached) → the boundary check trips before turn 3 (2 model calls). The child
	// produced no summary, so the issue-#48 salvage drives ONE more bounded wrap-up turn
	// (MaxTurns=1, so the salvage's own tool call cannot exceed it) → 2 + 1 = 3.
	if got := boundedLLM.Calls(); got != 3 {
		t.Fatalf("child made %d model calls, want 3 (max_tool_calls=2 bound + 1 bounded salvage turn)", got)
	}
}

// TestSubagentPerCallTightenOnlyCannotLoosen proves the TIGHTEN-ONLY guarantee: a per-call
// max_turns HIGHER than the inherited (default) limit is ignored — the model cannot use
// a per-call arg to escape the operator's bound. The child still stops at the inherited
// default MaxTurns (500), not the requested 1000.
func TestSubagentPerCallTightenOnlyCannotLoosen(t *testing.T) {
	boundedEngine, boundedLLM := readLoopChild(t, 520)
	task := agent.NewSubagentTool(boundedEngine)

	subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"loop","max_turns":1000}`)),
		mockllm.TextTurn("parent done"),
	)
	// The default MaxTurns bounds the loop; the child emits no summary, so the issue-#48
	// salvage adds ONE bounded wrap-up turn on top of the inherited cap.
	if want := agent.DefaultChildLimits().MaxTurns + 1; boundedLLM.Calls() != want {
		t.Fatalf("child made %d model calls, want the inherited default MaxTurns=%d + 1 salvage turn = %d (tighten-only: a higher per-call arg must NOT loosen)",
			boundedLLM.Calls(), agent.DefaultChildLimits().MaxTurns, want)
	}
}

// sleepThenLoopTool is a read-only child tool that sleeps a fixed duration (long
// enough to blow a short deadline) and keeps returning success, so a child that calls
// it repeatedly will overrun any short wall-clock deadline. It is the ADVERSARIAL,
// deadline-ignoring child: it never voluntarily stops.
type sleepThenLoopTool struct {
	sleep time.Duration
	calls atomic.Int64
}

func (*sleepThenLoopTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Slow", Description: "slow", Schema: []byte(`{"type":"object"}`)}
}
func (*sleepThenLoopTool) ReadOnly() bool { return true }
func (s *sleepThenLoopTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	s.calls.Add(1)
	select {
	case <-time.After(s.sleep):
	case <-ctx.Done():
	}
	return session.NewToolResult(in.ID, "slow ok"), nil
}

// TestSubagentPerCallTimeoutProducesTimeBudgetError is the MODEL-FACING e2e + ADVERSARIAL
// (uncooperative-mock) test: a Subagent call with a short timeout_ms drives a child that
// IGNORES the deadline and keeps emitting tool calls / sleeping. The ctx deadline must
// terminate it, and the RESULT must be a model-addressable time-budget tool error.
func TestSubagentPerCallTimeoutProducesTimeBudgetError(t *testing.T) {
	slow := &sleepThenLoopTool{sleep: 50 * time.Millisecond}
	// A child whose every turn calls the slow tool — it never stops on its own.
	var script []mockllm.Turn
	for i := 0; i < 100; i++ {
		script = append(script, mockllm.ToolCallTurn(toolCall("k", "Slow", `{}`)))
	}
	childEngine := childEngineWith(mockllm.New(script...), catalogWith(t, slow))
	task := agent.NewSubagentTool(childEngine)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"loop forever","timeout_ms":120}`)),
		mockllm.TextTurn("parent recovered"),
	)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if !results[0].IsError {
		t.Fatalf("timed-out subagent should be an error result, got %+v", results[0])
	}
	if !strings.Contains(results[0].Content, "time budget") {
		t.Fatalf("error should mention the time budget, got %q", results[0].Content)
	}
}

// signalThenBlockTool signals each Execute entry on `entered` then blocks until the
// ctx is cancelled, so a test can deterministically know the child is in-flight before
// it cancels the PARENT ctx.
type signalThenBlockTool struct {
	entered chan struct{}
}

func (*signalThenBlockTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Block", Description: "block", Schema: []byte(`{"type":"object"}`)}
}
func (*signalThenBlockTool) ReadOnly() bool { return true }
func (b *signalThenBlockTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	b.entered <- struct{}{}
	<-ctx.Done()
	return session.NewToolResult(in.ID, "blocked ok"), nil
}

// TestSubagentParentCancelWithTimeoutIsNotTimeBudget is the BRANCH-GAP guard (QA #1): when
// timeout_ms is ALSO set, a PARENT cancellation must take the cancellation path, NOT be
// mislabeled "exceeded its time budget". The terminal branch keys off the SEPARATE
// timeoutCtx (DeadlineExceeded) rather than the merged ctx; if it regressed to ctx.Err()
// a parent cancel would falsely read as a deadline. A generous timeout_ms (that must NOT
// fire) is set; the child blocks in-flight; the test cancels the PARENT ctx. The result
// must be an error result that does NOT mention the time budget.
func TestSubagentParentCancelWithTimeoutIsNotTimeBudget(t *testing.T) {
	block := &signalThenBlockTool{entered: make(chan struct{}, 1)}
	childEngine := childEngineWith(mockllm.New(
		mockllm.ToolCallTurn(toolCall("k", "Block", `{}`)),
		mockllm.TextTurn("should not reach"),
	), catalogWith(t, block))
	task := agent.NewSubagentTool(childEngine)

	// A GENEROUS deadline that must not fire within the test; the parent cancel wins.
	parentCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type out struct {
		res session.ToolResult
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := task.Execute(parentCtx,
			session.NewToolCall("c1", "Subagent", []byte(`{"prompt":"block","timeout_ms":600000}`)),
			agent.MemEnv("/base"))
		done <- out{res, err}
	}()

	<-block.entered // the child is in-flight inside Block, holding on ctx.Done()
	cancel()        // cancel the PARENT — NOT the deadline

	got := <-done
	if got.err != nil {
		t.Fatalf("Subagent.Execute returned a transport error: %v", got.err)
	}
	// A parent cancel must NOT be mislabeled as a time-budget overrun. The exact
	// rendering of a cancellation is not under test here; the load-bearing assertion is
	// the NEGATIVE: the result must never claim the child "exceeded its time budget".
	if strings.Contains(got.res.Content, "time budget") {
		t.Fatalf("parent cancel was mislabeled as a time-budget overrun: %q", got.res.Content)
	}
}

// TestSubagentOmittedPerCallArgsUnchanged is the regression guard: a Subagent call with NONE of
// the per-call knobs behaves exactly as before — the child runs to its scripted text
// answer under the inherited default limits, and the result is the child's summary.
func TestSubagentOmittedPerCallArgsUnchanged(t *testing.T) {
	childEngine := childEngineWith(mockllm.New(mockllm.TextTurn("CHILD SUMMARY")), catalogWith(t))
	task := agent.NewSubagentTool(childEngine)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"investigate"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || results[0].IsError || !strings.Contains(results[0].Content, "CHILD SUMMARY") {
		t.Fatalf("omitted per-call args must return the child summary; got %+v", results[0])
	}
}

// TestSubagentPerCallTimeoutOmittedNoDeadline proves an omitted timeout_ms imposes NO
// deadline: a child with a brief sleep finishes normally (no time-budget error) even
// though the same sleep would blow a tight deadline if one were set.
func TestSubagentPerCallTimeoutOmittedNoDeadline(t *testing.T) {
	slow := &sleepThenLoopTool{sleep: 5 * time.Millisecond}
	childEngine := childEngineWith(mockllm.New(
		mockllm.ToolCallTurn(toolCall("k", "Slow", `{}`)),
		mockllm.TextTurn("finished"),
	), catalogWith(t, slow))
	task := agent.NewSubagentTool(childEngine)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"go"}`)),
		mockllm.TextTurn("parent done"),
	)
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("omitted timeout must impose no deadline; got error result %+v", results[0])
	}
	if !strings.Contains(results[0].Content, "finished") {
		t.Fatalf("result = %q, want it to contain the child's summary (no deadline)", results[0].Content)
	}
}

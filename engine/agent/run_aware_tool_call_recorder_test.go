package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// runAwareFakeRecorder implements BOTH port.ToolCallRecorder and the new
// port.RunAwareToolCallRecorder, recording which method the dispatcher chose.
type runAwareFakeRecorder struct {
	plainCalls    int
	runAwareCalls int
	lastRunID     string
}

func (f *runAwareFakeRecorder) ToolCall(session.SessionID, session.ToolCall, session.ToolResult, time.Duration, time.Duration) {
	f.plainCalls++
}

func (f *runAwareFakeRecorder) ToolCallForRun(runID string, _ session.SessionID, _ session.ToolCall, _ session.ToolResult, _, _ time.Duration) {
	f.runAwareCalls++
	f.lastRunID = runID
}

// newToolCallingEngine builds an engine + session wired with a single Read tool
// call followed by a final text turn, mirroring TestFullCycle's mockllm script,
// with the given ToolCallRecorder injected.
func newToolCallingEngine(t *testing.T, rec interface {
	ToolCall(session.SessionID, session.ToolCall, session.ToolResult, time.Duration, time.Duration)
}) (*agent.Engine, *session.Session) {
	t.Helper()
	read := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "file contents"), nil
		}}
	cat := catalogWith(t, read)

	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("let me look"),
			mockllm.ToolCallChunk(toolCall("c1", "Read", `{"path":"a.go"}`)),
			mockllm.UsageChunk(session.Usage{InputTokens: 10, OutputTokens: 2}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk("all done"),
			mockllm.UsageChunk(session.Usage{InputTokens: 5, OutputTokens: 3}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)

	clk := &fakeClock{t: time.Unix(0, 0)}
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Clock: clk, ToolCallRecorder: rec})
	sess := newSession(t, session.Limits{})
	return e, sess
}

// driveOneToolCallingTurn drives the engine through the single tool-calling
// turn scripted by newToolCallingEngine and returns the enclosing Run's RunID.
func driveOneToolCallingTurn(t *testing.T, e *agent.Engine, sess *session.Session) string {
	t.Helper()
	ws := memfs.NewWorkspace("/ws")
	r := e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "look at a.go"})
	runID := r.RunID()
	drain(r)
	return runID
}

// TestExecutePrefersRunAwareToolCallRecorderWhenImplemented pins that the
// dispatcher, at its one ToolCallRecorder call site, calls ToolCallForRun
// (never both) when the injected recorder implements it, passing the SAME
// RunID the enclosing Run already carries — and falls back to the plain
// ToolCall for a recorder that does not implement the richer interface
// (every existing ToolCallRecorder implementer is unaffected).
func TestExecutePrefersRunAwareToolCallRecorderWhenImplemented(t *testing.T) {
	rec := &runAwareFakeRecorder{}
	e, sess := newToolCallingEngine(t, rec)
	runID := driveOneToolCallingTurn(t, e, sess)

	if rec.plainCalls != 0 {
		t.Errorf("plainCalls = %d, want 0 (RunAwareToolCallRecorder must be preferred)", rec.plainCalls)
	}
	if rec.runAwareCalls == 0 {
		t.Fatal("runAwareCalls = 0, want at least 1")
	}
	if rec.lastRunID != runID {
		t.Errorf("lastRunID = %q, want %q (the enclosing Run's own id)", rec.lastRunID, runID)
	}
}

// TestExecuteFallsBackToPlainToolCallRecorder is a regression guard: a
// recorder implementing ONLY port.ToolCallRecorder (not the richer
// RunAwareToolCallRecorder) must keep working exactly as before, using the
// package's existing plain recordingLogger fixture.
func TestExecuteFallsBackToPlainToolCallRecorder(t *testing.T) {
	logger := &recordingLogger{}
	e, sess := newToolCallingEngine(t, logger)
	driveOneToolCallingTurn(t, e, sess)

	if logger.calls != 1 {
		t.Fatalf("logger recorded %d tool calls, want 1", logger.calls)
	}
}

package agent_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// The per-call TIME-BUDGET terminal was the one remaining Subagent failure path with no
// next action, after issue #318 taught the model that every other bad terminal is
// recoverable. A timed-out child lands StateCancelled, which resolveResumeSession has
// ALWAYS recovered, so the silence was not honesty — it read as "this delegation is
// dead", and the parent's only move was to re-delegate from scratch.
//
// These four tests drive the REAL render path (the parent loop's recorded tool result —
// what the model actually reads) across the axes the gate resolves: read-only vs
// direct-write, store-wired vs store-less, foreground vs background.

// TestSubagentTimeoutResultAdvertisesResume is the positive foreground case: a read-only
// child that blows its timeout_ms is told the terminal is recoverable AND told the knob
// that fixes a genuine overrun (timeout_ms), with the agentId trailer ABOVE the hint so
// "the agentId above" is literally accurate.
func TestSubagentTimeoutResultAdvertisesResume(t *testing.T) {
	slow := &sleepThenLoopTool{sleep: 50 * time.Millisecond}
	var script []mockllm.Turn
	for i := 0; i < 100; i++ {
		script = append(script, mockllm.ToolCallTurn(toolCall("k", "Slow", `{}`)))
	}
	childEngine := childEngineWith(mockllm.New(script...), catalogWith(t, slow))
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(memstore.New()))

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"loop forever","timeout_ms":120}`)),
		mockllm.TextTurn("parent recovered"),
	)
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("want 1 errored tool result, got %+v", results)
	}
	body := results[0].Content
	if !strings.Contains(body, "time budget") {
		t.Fatalf("the terminal must still name the time budget, got:\n%s", body)
	}
	if !strings.Contains(body, "resume it with the agentId above") {
		t.Fatalf("a timed-out child is resumable — the terminal must say so (it was the last dead end), got:\n%s", body)
	}
	if !strings.Contains(body, "timeout_ms") {
		t.Fatalf("the terminal must name `timeout_ms` as the knob to raise, got:\n%s", body)
	}
	idAt := strings.Index(body, "agentId: ")
	hintAt := strings.Index(body, "resume it with the agentId above")
	if idAt < 0 || hintAt < idAt {
		t.Fatalf("the hint says \"the agentId above\" but the trailer is not above it:\n%s", body)
	}
}

// TestStorelessSubagentTimeoutDoesNotAdvertiseResume is the negative on the OTHER
// precondition, matching the StopError path's policy exactly: validateResume's FIRST
// check is a wired session store, so a SubagentTool built without WithSubagentStore
// (a supported engine-module construction, ADR 0036) must not advertise a `resume` it
// will then refuse with "not supported in this deployment".
func TestStorelessSubagentTimeoutDoesNotAdvertiseResume(t *testing.T) {
	slow := &sleepThenLoopTool{sleep: 50 * time.Millisecond}
	var script []mockllm.Turn
	for i := 0; i < 100; i++ {
		script = append(script, mockllm.ToolCallTurn(toolCall("k", "Slow", `{}`)))
	}
	childEngine := childEngineWith(mockllm.New(script...), catalogWith(t, slow))
	// No WithSubagentStore.
	task := agent.NewSubagentTool(childEngine)

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"loop forever","timeout_ms":120}`)),
		mockllm.TextTurn("parent recovered"),
	)
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("want 1 errored tool result, got %+v", results)
	}
	body := results[0].Content
	if strings.Contains(body, "resume it with the agentId") {
		t.Fatalf("a store-less deployment must NOT advertise resume, got:\n%s", body)
	}
	// The pre-existing contract is otherwise untouched.
	if !strings.Contains(body, "time budget") || !strings.Contains(body, "agentId: ") {
		t.Fatalf("the time-budget error and its agentId trailer must survive, got:\n%s", body)
	}
}

// TestWritableSubagentTimeoutGivesOneCombinedNextAction is the direct-write (ADR 0041)
// case, and the reason the timeout path needs its own gate rather than appending a hint.
// A timed-out writable child's edits ARE in the real tree, so "resume to finish on top of
// them" and "discard them with git" are both true — and a model handed them as two
// independent imperatives can do both, then resume a child that resumeWritableNote greets
// with "the file edits you already made are STILL IN PLACE", which the discard just
// falsified. So the two options must arrive as ONE exclusive decision.
func TestWritableSubagentTimeoutGivesOneCombinedNextAction(t *testing.T) {
	ws := memfs.NewWorkspace("/ws")
	block := &signalThenBlockTool{entered: make(chan struct{}, 1)}
	writeTool := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, w tool.Workspace) (session.ToolResult, error) {
			if err := w.Write(context.Background(), "timed-out.txt", []byte("first edit landed\n")); err != nil {
				return session.NewToolError(in.ID, "write failed: "+err.Error()), nil
			}
			return session.NewToolResult(in.ID, "wrote timed-out.txt"), nil
		}}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("w1", "Write", `{"path":"timed-out.txt"}`)),
		mockllm.ToolCallTurn(toolCall("b1", "Block", `{}`)),
		mockllm.TextTurn("never reached"),
	)
	writable := childEngineWith(childLLM, catalogWith(t, writeTool, block))
	task := newWritableSubagent(t, writable, agent.WithSubagentStore(memstore.New()))

	res, err := task.Execute(context.Background(),
		session.NewToolCall("p1", "Subagent", []byte(`{"prompt":"loop forever","mode":"read-write","timeout_ms":120}`)), ws)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "time budget") {
		t.Fatalf("a timed-out writable child must surface the time-budget error, got %+v", res)
	}
	body := res.Content
	// Still honest that the edits landed and may be half-finished.
	if !strings.Contains(body, "PARTIAL") {
		t.Fatalf("a writable timeout must warn the edits may be partial, got:\n%s", body)
	}
	// ONE decision, explicitly exclusive — not two independent imperatives.
	if !strings.Contains(body, "Do not do both") {
		t.Fatalf("the writable timeout must state resume-or-discard as ONE exclusive decision, got:\n%s", body)
	}
	if !strings.Contains(body, "timeout_ms") {
		t.Fatalf("the writable timeout must name `timeout_ms` as the knob to raise, got:\n%s", body)
	}
	// The generic read-only hint (which claims the workspace does not carry over) must
	// NOT also appear: for a direct-write child that claim is false.
	if strings.Contains(body, "WORKSPACE does not carry over") {
		t.Fatalf("the read-only workspace wording must not reach a direct-write child, got:\n%s", body)
	}
}

// TestBackgroundSubagentTimeoutAdvertisesResume covers the SECOND call site. A background
// child's rendered result is delivered verbatim by SubagentStatus, so the terminal
// rendering must mirror the foreground path — a background timeout that stayed a dead end
// would be the worse half, since the event stream is the operator's only other channel
// and the MODEL has none.
func TestBackgroundSubagentTimeoutAdvertisesResume(t *testing.T) {
	block := &signalThenBlockTool{entered: make(chan struct{}, 1)}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Block", `{}`)),
		mockllm.TextTurn("never reached"),
	)
	task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t, block)),
		agent.WithSubagentStore(memstore.New()))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x","background":true,"timeout_ms":120}`)),
		mockllm.ToolCallTurn(toolCall("p2", "SubagentStatus", `{"agent_id":"subagent-p1","wait_ms":30000}`)),
		mockllm.TextTurn("parent done"),
	)
	cat := catalogWith(t, task, agent.NewSubagentStatusTool())
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: cat})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	evs := drainObserving(t, r, nil)

	collected := resultByCallID(evs)["p2"]
	if collected == nil {
		t.Fatalf("the background child's result must be collectible via SubagentStatus")
	}
	if !strings.Contains(collected.Content, "time budget") {
		t.Fatalf("the collected body must name the time budget, got:\n%s", collected.Content)
	}
	if !strings.Contains(collected.Content, "resume it with the agentId above") {
		t.Fatalf("a timed-out BACKGROUND child is resumable too — the collected body must say so, got:\n%s", collected.Content)
	}
	if !strings.Contains(collected.Content, "timeout_ms") {
		t.Fatalf("the collected body must name `timeout_ms` as the knob to raise, got:\n%s", collected.Content)
	}
}

// ctxHonouringStore is a SessionStore that refuses to Save on a dead context — the
// behaviour redisstore.Save (it passes ctx straight to HSet) and
// grpcdriver.SessionStore.Save (it passes ctx to the RPC) genuinely have, and the one the
// in-tree memstore/jsonlstore do NOT, which is why this residual survived offline testing.
type ctxHonouringStore struct {
	mu     sync.Mutex
	inner  port.SessionStore
	denied int
}

func (s *ctxHonouringStore) Save(ctx context.Context, sess *session.Session) error {
	if err := ctx.Err(); err != nil {
		s.mu.Lock()
		s.denied++
		s.mu.Unlock()
		return err
	}
	return s.inner.Save(ctx, sess)
}

func (s *ctxHonouringStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.inner.Load(ctx, id)
}

func (s *ctxHonouringStore) deniedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.denied
}

// TestTimedOutSubagentIsPersistedDespiteTheExpiredContext closes the gap between what the
// timeout terminal now ADVERTISES and what the harness actually saves.
//
// The terminal tells the model "resume it with the agentId above", and resume works only
// off the persisted snapshot — but the terminal persist ran on the very ctx applyCallTimeout
// had just expired. Against the in-tree stores (which ignore ctx on Save) that was
// invisible; against redisstore or the remote driver the save failed and the advertised
// resume came back as "no subagent found for resume id". persistChild therefore detaches
// cancellation (context.WithoutCancel) under its own short deadline.
func TestTimedOutSubagentIsPersistedDespiteTheExpiredContext(t *testing.T) {
	store := &ctxHonouringStore{inner: memstore.New()}
	slow := &sleepThenLoopTool{sleep: 50 * time.Millisecond}
	var script []mockllm.Turn
	for i := 0; i < 100; i++ {
		script = append(script, mockllm.ToolCallTurn(toolCall("k", "Slow", `{}`)))
	}
	childEngine := childEngineWith(mockllm.New(script...), catalogWith(t, slow))
	task := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))

	results, _ := subagentParentResults(t, task,
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"loop forever","timeout_ms":120}`)),
		mockllm.TextTurn("parent recovered"),
	)
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("want 1 errored tool result, got %+v", results)
	}
	// Precondition: the terminal really does advertise the resume this save has to make
	// possible. Without it the assertion below would be testing an unadvertised path.
	if !strings.Contains(results[0].Content, "resume it with the agentId above") {
		t.Fatalf("precondition: the timeout terminal must advertise the resume, got:\n%s", results[0].Content)
	}
	if n := store.deniedCount(); n != 0 {
		t.Fatalf("the timed-out child's snapshot was refused %d time(s) because the save ran on the EXPIRED ctx — the advertised resume is a dead end on a ctx-honouring store", n)
	}
	if _, err := store.Load(context.Background(), "subagent-p1"); err != nil {
		t.Fatalf("the timed-out child must be loadable for the resume the terminal advertises: %v", err)
	}
}

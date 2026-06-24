package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// --- test doubles -----------------------------------------------------------

// fakeTool is an instrumented Tool whose read-only flag, execution body, and
// concurrency observation are controllable from a test.
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

// overlapTracker records the maximum number of concurrently-running tool
// executions observed, so tests can assert read-parallel vs mutate-serial.
type overlapTracker struct {
	mu      sync.Mutex
	running int
	maxObs  int
}

func (o *overlapTracker) enter() {
	o.mu.Lock()
	o.running++
	if o.running > o.maxObs {
		o.maxObs = o.running
	}
	o.mu.Unlock()
}
func (o *overlapTracker) leave() {
	o.mu.Lock()
	o.running--
	o.mu.Unlock()
}
func (o *overlapTracker) max() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.maxObs
}

// fakeClock advances by a fixed step on each Now() call so tool timing is
// deterministic.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Millisecond)
	return c.t
}

// recordingLogger captures tool-call observability for assertions.
type recordingLogger struct {
	mu      sync.Mutex
	calls   int
	results []session.ToolResult
}

func (l *recordingLogger) ToolCall(_ session.SessionID, _ session.ToolCall, result session.ToolResult, _, _ time.Duration) {
	l.mu.Lock()
	l.calls++
	l.results = append(l.results, result)
	l.mu.Unlock()
}

// lastResult returns the most recently logged tool result. Call it after the run
// completes (drain has returned), when no logging goroutine is still active.
func (l *recordingLogger) lastResult() (session.ToolResult, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.results) == 0 {
		return session.ToolResult{}, false
	}
	return l.results[len(l.results)-1], true
}

// --- helpers ----------------------------------------------------------------

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

// allowAll returns a policy that allows every tool call.
func allowAll() *permpolicy.Policy {
	return permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
}

// noopHooks is a no-op port.HookRunner: a runner is PRESENT (so the engine's
// hook-dispatch path executes) but no hook is configured, mirroring what
// hookexec.New(nil) provided before the engine tree became self-contained.
type noopHooks struct{}

func (noopHooks) Run(context.Context, governance.HookEvent) (governance.HookOutcome, error) {
	return governance.HookOutcome{}, nil
}

// drain collects events until the channel closes, returning them in order.
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

func containsType(evs []session.Event, ty session.EventType) bool {
	for _, e := range evs {
		if e.Type == ty {
			return true
		}
	}
	return false
}

// assertNoOrphanedToolCalls is a session-level structural invariant check: every
// ToolCall.ID emitted by an assistant message must be answered by some following
// ToolResult.CallID. This pins "the loop + Interrupt leaves a matched history"
// without importing a provider adapter — a real round-trip through the anthropic /
// openai request builders (their NoOrphaned tests) would reject any dangling
// tool_use, and this mirrors that contract at the session layer.
func assertNoOrphanedToolCalls(t *testing.T, msgs []session.Message) {
	t.Helper()
	answered := make(map[session.ToolCallID]struct{})
	for _, m := range msgs {
		if m.Role == session.RoleTool && m.ToolResult != nil {
			answered[m.ToolResult.CallID] = struct{}{}
		}
	}
	for _, m := range msgs {
		if m.Role != session.RoleAssistant {
			continue
		}
		for _, call := range m.ToolCalls {
			if _, ok := answered[call.ID]; !ok {
				t.Fatalf("orphaned tool call %q has no answering tool result", call.ID)
			}
		}
	}
}

func lastResult(t *testing.T, evs []session.Event) *session.ResultPayload {
	t.Helper()
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == session.EvResult {
			return evs[i].Result
		}
	}
	t.Fatalf("no result event in %v", typesOf(evs))
	return nil
}

func newEngine(d agent.Deps) *agent.Engine {
	if d.Policy == nil {
		d.Policy = allowAll()
	}
	if d.Model == "" {
		d.Model = "test-model"
	}
	return agent.NewEngine(d)
}

// --- tests ------------------------------------------------------------------

// TestFullCycle exercises a full multi-turn run: text → tool call (Read) → text.
// It asserts the ordered event taxonomy and the final usage accounting (gauntlet
// #1: a complete loop runs end-to-end on mockllm).
func TestFullCycle(t *testing.T) {
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
	logger := &recordingLogger{}
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Clock: clk, ToolCallRecorder: logger})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, ws, "look at a.go")
	evs := drain(r)

	if logger.calls != 1 {
		t.Fatalf("logger recorded %d tool calls, want 1", logger.calls)
	}

	for _, want := range []session.EventType{
		session.EvTurnStart, session.EvMessageDelta, session.EvToolCall,
		session.EvToolResult, session.EvResult,
	} {
		if !containsType(evs, want) {
			t.Fatalf("missing event %q in %v", want, typesOf(evs))
		}
	}
	res := lastResult(t, evs)
	if res.Stop != session.StopEndTurn {
		t.Fatalf("stop = %q, want end_turn", res.Stop)
	}
	if res.Text != "all done" {
		t.Fatalf("final text = %q", res.Text)
	}
	if got := res.Usage.InputTokens; got != 15 {
		t.Fatalf("cumulative input tokens = %d, want 15", got)
	}
	// Seq must be strictly increasing.
	for i := 1; i < len(evs); i++ {
		if evs[i].Seq <= evs[i-1].Seq {
			t.Fatalf("seq not increasing at %d: %d <= %d", i, evs[i].Seq, evs[i-1].Seq)
		}
	}
}

// TestNilDiagnosticsRunsWithoutPanic pins the production nil-safety guarantee:
// NewEngine defaults an OMITTED Deps.Diagnostics to port.NopDiagnostics
// (loop.go), so an engine built without injecting a Diagnostics sink runs a full
// cycle without nil-panicking. It deliberately constructs Deps via agent.Deps
// directly (NOT the newEngine test helper) and leaves Diagnostics nil, then drives
// a complete text → tool call → text loop and asserts it reaches a clean result.
func TestNilDiagnosticsRunsWithoutPanic(t *testing.T) {
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

	// Diagnostics is intentionally OMITTED (nil) — the engine must not panic.
	e := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		Policy:  allowAll(),
		Model:   "test-model",
	})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), "look at a.go")
	evs := drain(r)

	res := lastResult(t, evs)
	if res.Stop != session.StopEndTurn {
		t.Fatalf("stop = %q, want end_turn (engine should run cleanly with nil Diagnostics)", res.Stop)
	}
	if res.Text != "all done" {
		t.Fatalf("final text = %q, want %q", res.Text, "all done")
	}
}

// TestReasoningAndTurnEnd asserts the Tier B additive events and the
// display/replay reasoning split: a ChunkReasoning (human-readable summary)
// produces a reasoning.delta DISPLAY event but is NOT what gets stored for
// replay, while a ChunkReasoningItem (the opaque encrypted_content blob) is what
// lands on Message.Reasoning to be replayed verbatim. Each successful turn also
// emits a turn.end carrying that turn's usage and a non-zero elapsed duration
// (from the injected fakeClock).
func TestReasoningAndTurnEnd(t *testing.T) {
	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.ReasoningChunk("let me think about it"),
			mockllm.ReasoningItemChunk("ENCRYPTED_REPLAY_BLOB"),
			mockllm.TextChunk("here is the answer"),
			mockllm.UsageChunk(session.Usage{InputTokens: 12, OutputTokens: 4}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	clk := &fakeClock{t: time.Unix(0, 0)}
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t), Clock: clk})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), "go")
	evs := drain(r)

	// reasoning.delta must be emitted, carrying the human-readable SUMMARY text
	// and no result/usage payload of its own.
	var reasoning *session.Event
	for i := range evs {
		if evs[i].Type == session.EvReasoningDelta {
			reasoning = &evs[i]
			break
		}
	}
	if reasoning == nil {
		t.Fatalf("no reasoning.delta event in %v", typesOf(evs))
	}
	if reasoning.Text != "let me think about it" {
		t.Fatalf("reasoning text = %q", reasoning.Text)
	}

	// The REPLAY blob — not the display summary — must be stored on the recorded
	// assistant message's Reasoning for verbatim replay to the provider.
	var asst *session.Message
	for i := range sess.Conversation.Messages {
		if sess.Conversation.Messages[i].Role == session.RoleAssistant {
			asst = &sess.Conversation.Messages[i]
			break
		}
	}
	if asst == nil {
		t.Fatalf("no assistant message recorded")
	}
	if asst.Reasoning != "ENCRYPTED_REPLAY_BLOB" {
		t.Fatalf("Message.Reasoning = %q, want the encrypted replay blob (not the display summary)", asst.Reasoning)
	}

	// The reasoning.delta must precede the message.delta of the same turn (it is
	// the chain-of-thought that precedes the answer).
	var ri, mi = -1, -1
	for i := range evs {
		switch evs[i].Type {
		case session.EvReasoningDelta:
			if ri < 0 {
				ri = i
			}
		case session.EvMessageDelta:
			if mi < 0 {
				mi = i
			}
		}
	}
	if ri < 0 || mi < 0 || ri > mi {
		t.Fatalf("reasoning.delta (%d) should precede message.delta (%d)", ri, mi)
	}

	// turn.end must carry this turn's usage and a positive elapsed duration.
	var turnEnd *session.Event
	for i := range evs {
		if evs[i].Type == session.EvTurnEnd {
			turnEnd = &evs[i]
			break
		}
	}
	if turnEnd == nil {
		t.Fatalf("no turn.end event in %v", typesOf(evs))
	}
	// The per-turn data lives in the typed TurnEnd payload, NOT in Event.Usage
	// (which is reserved for the cumulative-on-result semantics).
	if turnEnd.Usage != nil {
		t.Fatalf("turn.end must not set Event.Usage (reserved for result): %+v", turnEnd.Usage)
	}
	if turnEnd.TurnEnd == nil {
		t.Fatalf("turn.end missing TurnEnd payload")
	}
	if turnEnd.TurnEnd.Usage.InputTokens != 12 || turnEnd.TurnEnd.Usage.OutputTokens != 4 {
		t.Fatalf("turn.end usage = %+v, want {12,4}", turnEnd.TurnEnd.Usage)
	}
	if turnEnd.TurnEnd.DurationMs <= 0 {
		t.Fatalf("turn.end duration = %dms, want > 0 (fakeClock injected)", turnEnd.TurnEnd.DurationMs)
	}
	// turn.end precedes the terminal result.
	te, re := -1, -1
	for i := range evs {
		switch evs[i].Type {
		case session.EvTurnEnd:
			te = i
		case session.EvResult:
			re = i
		}
	}
	if te < 0 || re < 0 || te > re {
		t.Fatalf("turn.end (%d) should precede result (%d)", te, re)
	}
}

// TestPhaseThreadedOntoAssistantMessage proves the loop THREADS the opaque
// OpenAI Responses phase marker (a ChunkPhase) onto the recorded assistant
// Message.ProviderPhase WITHOUT interpreting it — the neutral-seam analogue of the
// reasoning-replay-blob test. It deliberately scripts an UNKNOWN, non-enum value
// ("some_future_phase_v2", not "commentary"/"final_answer") to lock the
// forward-compat opaque-pass-through guarantee of issue #46: the harness never
// branches on or validates the value, so a novel phase must arrive verbatim on the
// recorded message. It also carries NO user-perceived token: there is no dedicated
// phase event, so the loop's event stream is unchanged (parallel to
// ChunkReasoningItem).
func TestPhaseThreadedOntoAssistantMessage(t *testing.T) {
	const phase = "some_future_phase_v2"
	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.PhaseChunk(phase),
			mockllm.TextChunk("here is the answer"),
			mockllm.UsageChunk(session.Usage{InputTokens: 12, OutputTokens: 4}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), "go")
	_ = drain(r)

	var asst *session.Message
	for i := range sess.Conversation.Messages {
		if sess.Conversation.Messages[i].Role == session.RoleAssistant {
			asst = &sess.Conversation.Messages[i]
			break
		}
	}
	if asst == nil {
		t.Fatalf("no assistant message recorded")
	}
	if asst.ProviderPhase != phase {
		t.Fatalf("Message.ProviderPhase = %q, want the verbatim threaded phase %q", asst.ProviderPhase, phase)
	}
	// The visible text and reasoning are untouched by the phase threading.
	if asst.Text != "here is the answer" {
		t.Fatalf("Message.Text = %q, want %q (phase must not perturb text)", asst.Text, "here is the answer")
	}
}

// TestTurnEndNoClock asserts turn.end is still emitted without a Clock, with a
// zero duration (the no-timing degradation).
func TestTurnEndNoClock(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("hi"))
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)}) // no Clock
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")
	evs := drain(r)

	if !containsType(evs, session.EvTurnEnd) {
		t.Fatalf("no turn.end event in %v", typesOf(evs))
	}
	for i := range evs {
		if evs[i].Type != session.EvTurnEnd {
			continue
		}
		if evs[i].TurnEnd == nil {
			t.Fatalf("turn.end missing TurnEnd payload")
		}
		if evs[i].TurnEnd.DurationMs != 0 {
			t.Fatalf("turn.end duration = %dms without a Clock, want 0", evs[i].TurnEnd.DurationMs)
		}
	}
}

// TestReadParallel asserts that two read-only calls in one turn run concurrently.
func TestReadParallel(t *testing.T) {
	var tracker overlapTracker
	bodies := make(chan struct{})
	gate := make(chan struct{})

	mk := func(name string) *fakeTool {
		return &fakeTool{name: name, readOnly: true,
			exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				tracker.enter()
				defer tracker.leave()
				bodies <- struct{}{} // signal "I'm running"
				<-gate               // block until both are confirmed running
				return session.NewToolResult(in.ID, name+" ok"), nil
			}}
	}
	cat := catalogWith(t, mk("Read"), mk("Grep"))

	llm := mockllm.New(
		mockllm.ToolCallTurn(
			toolCall("c1", "Read", `{"path":"a"}`),
			toolCall("c2", "Grep", `{"pattern":"x"}`),
		),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")

	// Both bodies must enter before either is released → proves overlap.
	<-bodies
	<-bodies
	close(gate)
	drain(r)

	if tracker.max() < 2 {
		t.Fatalf("read-only tools did not overlap: max concurrency = %d", tracker.max())
	}
}

// TestMutateSerial asserts that two mutating calls never interleave.
func TestMutateSerial(t *testing.T) {
	var tracker overlapTracker
	mk := func(name string) *fakeTool {
		return &fakeTool{name: name, readOnly: false,
			exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				tracker.enter()
				time.Sleep(5 * time.Millisecond) // widen the window for interleaving
				tracker.leave()
				return session.NewToolResult(in.ID, name+" ok"), nil
			}}
	}
	cat := catalogWith(t, mk("Write"), mk("Edit"))
	llm := mockllm.New(
		mockllm.ToolCallTurn(
			toolCall("c1", "Write", `{"path":"a"}`),
			toolCall("c2", "Edit", `{"path":"b"}`),
		),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")
	drain(r)

	if tracker.max() != 1 {
		t.Fatalf("mutating tools interleaved: max concurrency = %d", tracker.max())
	}
}

// TestPermissionApprove pauses on an ask, approves it, and confirms the tool ran
// and the loop continued (gauntlet #1: pause/resume).
func TestPermissionApprove(t *testing.T) {
	var executed atomic.Bool
	write := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			executed.Store(true)
			return session.NewToolResult(in.ID, "wrote"), nil
		}}
	cat := catalogWith(t, write)
	// Default policy (no rule) → Ask.
	policy := permpolicy.NewPolicy(nil, nil)

	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Write", `{"path":"a"}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: policy})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")

	var ask *session.PendingAsk
	var collected []session.Event
	for ev := range r.Events() {
		collected = append(collected, ev)
		if ev.Type == session.EvPermissionAsk {
			ask = ev.Ask
			r.Approve(ask.AskID, session.VerdictAllowOnce)
		}
	}
	if ask == nil {
		t.Fatalf("no permission.ask emitted")
	}
	if !executed.Load() {
		t.Fatalf("approved tool was not executed")
	}
	if res := lastResult(t, collected); res.Stop != session.StopEndTurn {
		t.Fatalf("stop = %q, want end_turn", res.Stop)
	}
}

// TestPermissionDeny pauses on an ask, denies it, and confirms the model receives
// the deny reason as an error tool result and the tool did NOT run.
func TestPermissionDeny(t *testing.T) {
	var executed atomic.Bool
	write := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			executed.Store(true)
			return session.NewToolResult(in.ID, "wrote"), nil
		}}
	cat := catalogWith(t, write)
	policy := permpolicy.NewPolicy(nil, nil) // Ask by default

	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Write", `{"path":"a"}`)),
		mockllm.TextTurn("understood"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: policy})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), "go")

	var denyResult *session.ToolResult
	// Track the order of the c1 events: the card (EvToolCall) must open BEFORE the
	// synthesized deny result, so the failed update lands on an already-open card.
	i, cardIdx, denyIdx := 0, -1, -1
	for ev := range r.Events() {
		switch ev.Type {
		case session.EvToolCall:
			if ev.ToolCall != nil && ev.ToolCall.ID == "c1" && cardIdx == -1 {
				cardIdx = i
			}
		case session.EvPermissionAsk:
			r.Approve(ev.Ask.AskID, session.VerdictDeny)
		case session.EvToolResult:
			denyResult = ev.ToolResult
			if ev.ToolResult != nil && ev.ToolResult.CallID == "c1" && denyIdx == -1 {
				denyIdx = i
			}
		}
		i++
	}
	if executed.Load() {
		t.Fatalf("denied tool was executed")
	}
	if denyResult == nil || !denyResult.IsError {
		t.Fatalf("expected an error tool result for the deny, got %+v", denyResult)
	}
	// Ordering: the card opens before the denial result (issue #6).
	if cardIdx == -1 {
		t.Fatalf("no EvToolCall opened for c1 (the card must open before the gate)")
	}
	if cardIdx >= denyIdx {
		t.Fatalf("event order = card@%d, deny@%d; want card < deny", cardIdx, denyIdx)
	}
	if !strings.Contains(denyResult.Content, "denied") {
		t.Fatalf("deny result does not carry a reason: %q", denyResult.Content)
	}
	// The deny reason must be in the conversation so the model can recover.
	found := false
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleTool && m.ToolResult != nil && m.ToolResult.IsError {
			found = true
		}
	}
	if !found {
		t.Fatalf("deny tool result not recorded in conversation")
	}
}

// TestCancelMidStream cancels the run while the model stream is in flight and
// asserts a terminal result=cancelled (gauntlet #1).
func TestCancelMidStream(t *testing.T) {
	// A turn whose stream blocks forever between chunks until ctx is cancelled.
	blocking := &blockingProvider{started: make(chan struct{})}

	e := newEngine(agent.Deps{LLM: blocking, Catalog: catalogWith(t)})
	ctx := context.Background()
	r := e.Run(ctx, newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")

	// Wait until the provider is streaming, then cancel.
	<-blocking.started
	r.Cancel()

	evs := drain(r)
	res := lastResult(t, evs)
	if res.Stop != session.StopCancelled {
		t.Fatalf("stop = %q, want cancelled", res.Stop)
	}
}

// blockingProvider streams one text chunk, signals started, then blocks until the
// context is cancelled.
type blockingProvider struct {
	started chan struct{}
	once    sync.Once
}

func (*blockingProvider) Capabilities() port.ProviderCapabilities { return port.ProviderCapabilities{} }

func (b *blockingProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return func(yield func(port.Chunk, error) bool) {
		b.once.Do(func() { close(b.started) })
		if !yield(port.Chunk{Kind: port.ChunkText, Text: "thinking"}, nil) {
			return
		}
		<-ctx.Done()
		yield(port.Chunk{}, ctx.Err())
	}, nil
}

// TestCancelMidToolThenResumeSucceeds drives turn-1 to record an assistant
// tool-call then blocks inside the tool until the run is cancelled, so dispatch
// returns cancelled=true AFTER RecordAssistant but before RecordToolResults — the
// exact mid-dispatch interruption that leaves an orphaned tool_use. It then
// Interrupt()s the cancelled session (history-repair) and runs a SECOND mock turn
// on the same engine+session, asserting the wedge error is gone, the second run
// completes, and a synthetic tool result precedes the new user prompt.
func TestCancelMidToolThenResumeSucceeds(t *testing.T) {
	// The tool signals it has started, then blocks until ctx is cancelled — so the
	// run is cancelled mid-dispatch, after the assistant message is recorded.
	toolStarted := make(chan struct{})
	read := &fakeTool{name: "Read", readOnly: true,
		exec: func(ctx context.Context, _ session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			close(toolStarted)
			<-ctx.Done()
			return session.ToolResult{}, ctx.Err()
		}}

	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Read", `{"path":"a.go"}`)),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, read)})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, ws, "look at a.go")
	<-toolStarted
	r.Cancel()
	res := lastResult(t, drain(r))
	if res.Stop != session.StopCancelled {
		t.Fatalf("turn-1 stop = %q, want cancelled", res.Stop)
	}
	if sess.State != session.StateCancelled {
		t.Fatalf("after cancel state = %q, want cancelled", sess.State)
	}

	// Recover the wedged session.
	if err := sess.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	// FIX 3: the loop-driven cancel + Interrupt must leave a provider-valid history
	// (no dangling tool_use) — the same contract the adapter NoOrphaned tests enforce
	// against a hand-built history, checked here against the real loop's output.
	assertNoOrphanedToolCalls(t, sess.Conversation.Messages)

	// Second turn: a clean end-of-turn answer. This is the regression assertion —
	// before the fix RecordUserPrompt would reject the cancelled session.
	llm2 := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("all done"),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	e2 := newEngine(agent.Deps{LLM: llm2, Catalog: catalogWith(t, read)})
	r2 := e2.Run(context.Background(), sess, ws, "second prompt")
	res2 := lastResult(t, drain(r2))
	if res2.Stop != session.StopEndTurn {
		t.Fatalf("turn-2 stop = %q, want end_turn (not the cancelled wedge)", res2.Stop)
	}

	// The synthetic tool result for the orphaned c1 must sit before the new user
	// prompt in the replayed history.
	var synthIdx, secondPromptIdx = -1, -1
	for i, m := range sess.Conversation.Messages {
		if m.Role == session.RoleTool && m.ToolResult != nil && m.ToolResult.CallID == "c1" && m.ToolResult.IsError {
			synthIdx = i
		}
		if m.Role == session.RoleUser && m.Text == "second prompt" {
			secondPromptIdx = i
		}
	}
	if synthIdx < 0 {
		t.Fatalf("no synthetic tool result for orphaned c1 in conversation")
	}
	if secondPromptIdx < 0 {
		t.Fatalf("second prompt not recorded in conversation")
	}
	if synthIdx >= secondPromptIdx {
		t.Fatalf("synthetic result@%d must precede second prompt@%d", synthIdx, secondPromptIdx)
	}
}

// TestCancelAwaitingApprovalThenResumeSucceeds exercises the SECOND path into the
// orphaned-tool_use wedge: a cancel while the session is paused at StateAwaiting on
// a permission gate. The assistant message with its ToolCalls is ALREADY recorded
// before the gate; cancelling while awaiting takes the authorize() cancelled=true
// branch (dispatch.go), leaving the same orphan as a mid-tool cancel. It then
// Interrupt()s and runs a clean second turn, asserting no wedge and a matched
// history.
func TestCancelAwaitingApprovalThenResumeSucceeds(t *testing.T) {
	var executed atomic.Bool
	write := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			executed.Store(true)
			return session.NewToolResult(in.ID, "wrote"), nil
		}}
	cat := catalogWith(t, write)
	policy := permpolicy.NewPolicy(nil, nil) // no rule → Ask, so the run pauses at the gate

	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Write", `{"path":"a"}`)),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: policy})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, ws, "write a")

	// Drain events; when the run pauses on the ask, cancel WHILE awaiting (do not
	// Approve/Deny). The cancel unblocks await() down the cancelled=true branch.
	var sawAsk bool
	for ev := range r.Events() {
		if ev.Type == session.EvPermissionAsk {
			sawAsk = true
			r.Cancel()
		}
	}
	if !sawAsk {
		t.Fatalf("run never paused on a permission.ask")
	}
	if executed.Load() {
		t.Fatalf("tool ran despite cancel-while-awaiting")
	}

	// drain already returned; recompute the terminal result by snapshotting state.
	if sess.State != session.StateCancelled {
		t.Fatalf("after cancel-while-awaiting state = %q, want cancelled", sess.State)
	}

	// The assistant message with the c1 tool call must already be recorded AND
	// orphaned at this point — that is the wedge this branch creates.
	orphanBefore := false
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleAssistant {
			for _, c := range m.ToolCalls {
				if c.ID == "c1" {
					orphanBefore = true
				}
			}
		}
	}
	if !orphanBefore {
		t.Fatalf("expected an orphaned assistant tool call c1 before Interrupt")
	}

	// Recover the wedged session.
	if err := sess.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	// FIX 3: post-Interrupt history is provider-valid (no dangling tool_use).
	assertNoOrphanedToolCalls(t, sess.Conversation.Messages)

	// Second turn: a clean end-of-turn answer. Before the fix RecordUserPrompt would
	// reject the cancelled session and the orphaned tool_use would wedge replay.
	llm2 := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("ok"),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	e2 := newEngine(agent.Deps{LLM: llm2, Catalog: cat, Policy: policy})
	r2 := e2.Run(context.Background(), sess, ws, "second prompt")
	res2 := lastResult(t, drain(r2))
	if res2.Stop != session.StopEndTurn {
		t.Fatalf("turn-2 stop = %q, want end_turn (not the cancelled wedge)", res2.Stop)
	}

	// The synthetic tool result closing out the orphaned c1 must precede the new
	// user prompt in the replayed history.
	var synthIdx, secondPromptIdx = -1, -1
	for i, m := range sess.Conversation.Messages {
		if m.Role == session.RoleTool && m.ToolResult != nil && m.ToolResult.CallID == "c1" && m.ToolResult.IsError {
			synthIdx = i
		}
		if m.Role == session.RoleUser && m.Text == "second prompt" {
			secondPromptIdx = i
		}
	}
	if synthIdx < 0 {
		t.Fatalf("no synthetic tool result for orphaned c1 in conversation")
	}
	if secondPromptIdx < 0 {
		t.Fatalf("second prompt not recorded in conversation")
	}
	if synthIdx >= secondPromptIdx {
		t.Fatalf("synthetic result@%d must precede second prompt@%d", synthIdx, secondPromptIdx)
	}
}

func TestStopMaxTurns(t *testing.T) {
	// The model always asks for a tool, so the loop would run forever without a
	// turn cap.
	read := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "ok"), nil
		}}
	turns := make([]mockllm.Turn, 10)
	for i := range turns {
		turns[i] = mockllm.ToolCallTurn(toolCall(fmt.Sprintf("c%d", i), "Read", `{"path":"a"}`))
	}
	llm := mockllm.New(turns...)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, read)})
	sess := newSession(t, session.Limits{MaxTurns: 3})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), "go")
	res := lastResult(t, drain(r))
	if res.Stop != session.StopMaxTurns {
		t.Fatalf("stop = %q, want max_turns", res.Stop)
	}
}

func TestStopMaxToolCalls(t *testing.T) {
	read := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "ok"), nil
		}}
	turns := make([]mockllm.Turn, 10)
	for i := range turns {
		turns[i] = mockllm.ToolCallTurn(toolCall(fmt.Sprintf("c%d", i), "Read", `{"path":"a"}`))
	}
	llm := mockllm.New(turns...)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, read)})
	sess := newSession(t, session.Limits{MaxToolCalls: 2})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), "go")
	res := lastResult(t, drain(r))
	if res.Stop != session.StopMaxToolCalls {
		t.Fatalf("stop = %q, want max_tool_calls", res.Stop)
	}
}

func TestStopMaxConsecutiveFailures(t *testing.T) {
	failing := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolError(in.ID, "boom"), nil
		}}
	turns := make([]mockllm.Turn, 10)
	for i := range turns {
		turns[i] = mockllm.ToolCallTurn(toolCall(fmt.Sprintf("c%d", i), "Read", `{"path":"a"}`))
	}
	llm := mockllm.New(turns...)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, failing)})
	sess := newSession(t, session.Limits{MaxConsecutiveFailures: 2})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), "go")
	res := lastResult(t, drain(r))
	if res.Stop != session.StopMaxConsecutiveFailures {
		t.Fatalf("stop = %q, want max_consecutive_failures", res.Stop)
	}
}

// argRecorder is a Tool that captures the Args of the last call it executed, so
// a test can assert which arguments actually reached the tool.
type argRecorder struct {
	name     string
	readOnly bool
	mu       sync.Mutex
	gotArgs  string
}

func (a *argRecorder) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: a.name, Description: a.name, Schema: json.RawMessage(`{"type":"object"}`)}
}
func (a *argRecorder) ReadOnly() bool { return a.readOnly }
func (a *argRecorder) Execute(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
	a.mu.Lock()
	a.gotArgs = string(in.Args)
	a.mu.Unlock()
	return session.NewToolResult(in.ID, "ok"), nil
}
func (a *argRecorder) args() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.gotArgs
}

// TestPreToolUseHookMutatesArgs asserts a PreToolUse hook returning a non-empty
// Mutated payload rewrites the tool call's args before execution: the tool runs
// with the MUTATED args, and the CallID/Name are preserved.
func TestPreToolUseHookMutatesArgs(t *testing.T) {
	rec := &argRecorder{name: "Write", readOnly: false}
	hooks := &mutatingHooks{mutate: map[governance.HookPhase]json.RawMessage{
		governance.PhasePreToolUse: json.RawMessage(`{"path":"mutated.txt"}`),
	}}
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Write", `{"path":"original.txt"}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, rec), Hooks: hooks})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")

	var rewriteEv bool
	for ev := range r.Events() {
		if ev.Type == session.EvHook && strings.Contains(ev.Text, "rewrote tool arguments") {
			rewriteEv = true
		}
	}
	if got := rec.args(); got != `{"path":"mutated.txt"}` {
		t.Fatalf("tool executed with args %q, want the mutated args", got)
	}
	if !rewriteEv {
		t.Fatalf("expected a hook event noting the arg rewrite")
	}
}

// TestPreToolUseHookMutationMalformedIgnored asserts a malformed (non-JSON)
// Mutated payload is ignored: the tool runs with the ORIGINAL args and a notice
// event is emitted.
func TestPreToolUseHookMutationMalformedIgnored(t *testing.T) {
	rec := &argRecorder{name: "Write", readOnly: false}
	hooks := &mutatingHooks{mutate: map[governance.HookPhase]json.RawMessage{
		governance.PhasePreToolUse: json.RawMessage(`not-json`),
	}}
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Write", `{"path":"original.txt"}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, rec), Hooks: hooks})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")

	var ignoredEv bool
	for ev := range r.Events() {
		if ev.Type == session.EvHook && strings.Contains(ev.Text, "malformed argument mutation") {
			ignoredEv = true
		}
	}
	if got := rec.args(); got != `{"path":"original.txt"}` {
		t.Fatalf("tool executed with args %q, want the ORIGINAL args (malformed mutation ignored)", got)
	}
	if !ignoredEv {
		t.Fatalf("expected a hook event noting the malformed mutation was ignored")
	}
}

// TestPreToolUseHookNoMutationKeepsArgs asserts that with no Mutated payload the
// tool runs with its original args (regression for the allow path).
func TestPreToolUseHookNoMutationKeepsArgs(t *testing.T) {
	rec := &argRecorder{name: "Write", readOnly: false}
	hooks := newRecordingHooks(nil) // allows everything, no mutation
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Write", `{"path":"original.txt"}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, rec), Hooks: hooks})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")
	drain(r)

	if got := rec.args(); got != `{"path":"original.txt"}` {
		t.Fatalf("tool executed with args %q, want the original args", got)
	}
}

// postMutResult finds the tool.result event and the recorded RoleTool message
// for callID, returning their ToolResults so a test can assert both the client
// (event) view and the model (recorded) view agree.
func toolResultEvent(evs []session.Event) *session.ToolResult {
	for _, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			return ev.ToolResult
		}
	}
	return nil
}

func recordedToolResult(sess *session.Session) *session.ToolResult {
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleTool && m.ToolResult != nil {
			return m.ToolResult
		}
	}
	return nil
}

// TestPostToolUseHookMutatesResult asserts a PostToolUse hook returning a Mutated
// {"content","is_error"} payload rewrites the result: BOTH the emitted tool.result
// event AND the result recorded for the model carry the rewritten content, and the
// is_error flag is honoured (here flipping a success into an error).
func TestPostToolUseHookMutatesResult(t *testing.T) {
	tl := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "SECRET=abc123"), nil
		}}
	hooks := &mutatingHooks{mutate: map[governance.HookPhase]json.RawMessage{
		governance.PhasePostToolUse: json.RawMessage(`{"content":"[redacted]","is_error":true}`),
	}}
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Read", `{"path":"a"}`)),
		mockllm.TextTurn("done"),
	)
	sess := newSession(t, session.Limits{})
	logger := &recordingLogger{}
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, tl), Hooks: hooks, ToolCallRecorder: logger})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), "go")
	evs := drain(r)

	evRes := toolResultEvent(evs)
	if evRes == nil || evRes.Content != "[redacted]" || !evRes.IsError {
		t.Fatalf("emitted tool.result = %+v, want content '[redacted]' is_error true", evRes)
	}
	recRes := recordedToolResult(sess)
	if recRes == nil || recRes.Content != "[redacted]" || !recRes.IsError {
		t.Fatalf("recorded result (model view) = %+v, want content '[redacted]' is_error true", recRes)
	}
	// The audit Logger must see the EFFECTIVE (redacted) result too: a redacting
	// PostToolUse hook must not leak the raw tool output ("SECRET=...") into the
	// audit log / telemetry. This is the whole point of logging after the hook.
	logged, ok := logger.lastResult()
	if !ok || logged.Content != "[redacted]" || !logged.IsError {
		t.Fatalf("logged result (audit view) = %+v ok=%v, want content '[redacted]' is_error true", logged, ok)
	}
	var rewriteEv bool
	for _, ev := range evs {
		if ev.Type == session.EvHook && strings.Contains(ev.Text, "rewrote the tool result") {
			rewriteEv = true
		}
	}
	if !rewriteEv {
		t.Fatalf("expected a hook event noting the result rewrite")
	}
}

// TestPostToolUseHookBlockLeavesResultUnchanged asserts a PostToolUse block still
// only annotates: the result the model sees is unchanged (block does not mutate).
func TestPostToolUseHookBlockLeavesResultUnchanged(t *testing.T) {
	tl := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "original output"), nil
		}}
	hooks := newRecordingHooks(map[governance.HookPhase]string{
		governance.PhasePostToolUse: "post annotation",
	})
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Read", `{"path":"a"}`)),
		mockllm.TextTurn("done"),
	)
	sess := newSession(t, session.Limits{})
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, tl), Hooks: hooks})
	evs := drain(e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), "go"))

	recRes := recordedToolResult(sess)
	if recRes == nil || recRes.Content != "original output" || recRes.IsError {
		t.Fatalf("recorded result = %+v, want unchanged 'original output'", recRes)
	}
	var annotated bool
	for _, ev := range evs {
		if ev.Type == session.EvHook && strings.Contains(ev.Text, "post annotation") {
			annotated = true
		}
	}
	if !annotated {
		t.Fatalf("expected the PostToolUse block annotation hook event")
	}
}

// TestPostToolUseHookMutationMalformedIgnored asserts a malformed (non-JSON)
// Mutated payload is ignored: the original result stands + a notice is emitted.
func TestPostToolUseHookMutationMalformedIgnored(t *testing.T) {
	tl := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "original output"), nil
		}}
	hooks := &mutatingHooks{mutate: map[governance.HookPhase]json.RawMessage{
		governance.PhasePostToolUse: json.RawMessage(`not-json`),
	}}
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Read", `{"path":"a"}`)),
		mockllm.TextTurn("done"),
	)
	sess := newSession(t, session.Limits{})
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, tl), Hooks: hooks})
	evs := drain(e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), "go"))

	recRes := recordedToolResult(sess)
	if recRes == nil || recRes.Content != "original output" {
		t.Fatalf("recorded result = %+v, want unchanged (malformed mutation ignored)", recRes)
	}
	var ignoredEv bool
	for _, ev := range evs {
		if ev.Type == session.EvHook && strings.Contains(ev.Text, "malformed result mutation") {
			ignoredEv = true
		}
	}
	if !ignoredEv {
		t.Fatalf("expected a hook event noting the malformed result mutation was ignored")
	}
}

// TestPostToolUseHookNoMutationKeepsResult asserts that with no Mutated payload
// the result is unchanged (allow-path regression).
func TestPostToolUseHookNoMutationKeepsResult(t *testing.T) {
	tl := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "original output"), nil
		}}
	hooks := newRecordingHooks(nil) // allow, no mutation
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Read", `{"path":"a"}`)),
		mockllm.TextTurn("done"),
	)
	sess := newSession(t, session.Limits{})
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, tl), Hooks: hooks})
	drain(e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), "go"))

	recRes := recordedToolResult(sess)
	if recRes == nil || recRes.Content != "original output" || recRes.IsError {
		t.Fatalf("recorded result = %+v, want unchanged 'original output'", recRes)
	}
}

// advisoryHooks is a test hook runner that returns an advisory outcome
// (Message set, no Block, no Mutated) on PreToolUse/PostToolUse — simulating
// a modelhook advisory finding.
type advisoryHooks struct {
	msg string
}

func (h *advisoryHooks) Run(_ context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	if ev.Phase == governance.PhasePreToolUse || ev.Phase == governance.PhasePostToolUse {
		return governance.HookOutcome{Message: h.msg}, nil
	}
	return governance.HookOutcome{}, nil
}

// TestAdvisoryHookEmitsEvHookAndLeavesResultUnchanged asserts that an advisory
// hook outcome (Message set, no Block, no Mutated) emits an EvHook with
// HookAdvisory decision AND leaves the tool result byte-unchanged
// (model-invisible). This is the core invariant of #170: client-visible,
// model-invisible.
func TestAdvisoryHookEmitsEvHookAndLeavesResultUnchanged(t *testing.T) {
	tl := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "original output"), nil
		}}
	hooks := &advisoryHooks{msg: "guardrail advisory: possible injection"}
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Read", `{"path":"a"}`)),
		mockllm.TextTurn("done"),
	)
	sess := newSession(t, session.Limits{})
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, tl), Hooks: hooks})
	r := e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), "go")
	evs := drain(r)

	// 1. An EvHook with HookAdvisory was emitted.
	var advisoryEv *session.Event
	for i, ev := range evs {
		if ev.Type == session.EvHook && ev.Hook != nil && ev.Hook.Decision == session.HookAdvisory {
			advisoryEv = &evs[i]
			break
		}
	}
	if advisoryEv == nil {
		t.Fatalf("expected an EvHook with HookAdvisory decision; events: %v", eventTypes(evs))
	}
	if !strings.Contains(advisoryEv.Text, "advisory") {
		t.Errorf("advisory EvHook text = %q, want to contain 'advisory'", advisoryEv.Text)
	}

	// 2. The tool result is byte-unchanged (model-invisible).
	recRes := recordedToolResult(sess)
	if recRes == nil || recRes.Content != "original output" || recRes.IsError {
		t.Fatalf("recorded result = %+v, want unchanged 'original output' (model-invisible)", recRes)
	}
	evRes := toolResultEvent(evs)
	if evRes == nil || evRes.Content != "original output" || evRes.IsError {
		t.Fatalf("event result = %+v, want unchanged 'original output' (client sees the same)", evRes)
	}
}

func eventTypes(evs []session.Event) []string {
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[i] = string(ev.Type)
	}
	return out
}

// TestUnknownToolError confirms an unknown tool yields an error result, not a
// crash, and the loop keeps going.
func TestUnknownToolError(t *testing.T) {
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Nope", `{}`)),
		mockllm.TextTurn("recovered"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")
	evs := drain(r)
	res := lastResult(t, evs)
	if res.Stop != session.StopEndTurn {
		t.Fatalf("stop = %q", res.Stop)
	}
}

// TestSessionInitEmittedOncePerRunBeforeFirstTurn asserts the loop emits exactly
// one session.init event per run, and that it precedes every turn.start. This is
// the run-open signal telemetry adapters switch on; the loop must emit it (not
// rely on telemetry's defensive fallback).
func TestSessionInitEmittedOncePerRunBeforeFirstTurn(t *testing.T) {
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Read", `{"path":"a.go"}`)),
		mockllm.TextTurn("done"),
	)
	read := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "ok"), nil
		}}
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, read)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), "go")
	evs := drain(r)

	// Exactly one session.init.
	var initCount, firstInitIdx, firstTurnIdx = 0, -1, -1
	for i, ev := range evs {
		switch ev.Type {
		case session.EvSessionInit:
			if firstInitIdx < 0 {
				firstInitIdx = i
			}
			initCount++
		case session.EvTurnStart:
			if firstTurnIdx < 0 {
				firstTurnIdx = i
			}
		}
	}
	if initCount != 1 {
		t.Fatalf("session.init count = %d, want exactly 1 (events: %v)", initCount, typesOf(evs))
	}
	if firstTurnIdx < 0 {
		t.Fatalf("no turn.start emitted (events: %v)", typesOf(evs))
	}
	if firstInitIdx > firstTurnIdx {
		t.Fatalf("session.init (idx %d) must precede first turn.start (idx %d): %v",
			firstInitIdx, firstTurnIdx, typesOf(evs))
	}
	// And it must be the very first event of the run.
	if evs[0].Type != session.EvSessionInit {
		t.Fatalf("first event = %q, want session.init: %v", evs[0].Type, typesOf(evs))
	}
}

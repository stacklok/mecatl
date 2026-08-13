package agent_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// scriptedClock returns a pre-scripted sequence of timestamps, one per Now()
// call, so a test can pin the exact instants the loop reads. After the script is
// exhausted it keeps returning the final value (so unrelated trailing reads do
// not panic) but counts every such read in overReads. It is the controlled clock
// the TTFT / inter-token measurement tests drive instead of the 1ms-step
// fakeClock, which cannot express specific gaps.
type scriptedClock struct {
	mu        sync.Mutex
	times     []time.Time
	i         int
	overReads int // reads past the end of the script — pins the per-delta read count
}

func (c *scriptedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.i >= len(c.times) {
		c.overReads++
		if len(c.times) == 0 {
			return time.Unix(0, 0)
		}
		return c.times[len(c.times)-1]
	}
	t := c.times[c.i]
	c.i++
	return t
}

// assertScriptFullyConsumed fails the test unless the loop read EXACTLY the
// scripted number of timestamps — no fewer (a skipped read) and no more (an
// over-read past the script). This pins the single-Clock-read-per-delta parity
// that keeps the inter-token gap series honest: a future double-read in
// noteStreamDelta / noteFirstOutput would push reads past the script and fail here
// LOUDLY, instead of being silently absorbed by the final-value fallback. Use it
// only with a script that covers EVERY read of the whole run.
func (c *scriptedClock) assertScriptFullyConsumed(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.overReads != 0 {
		t.Errorf("scriptedClock over-read %d time(s) past its %d-entry script — an extra Clock read crept into the latency path",
			c.overReads, len(c.times))
	}
	if c.i != len(c.times) {
		t.Errorf("scriptedClock consumed %d of %d scripted reads — a Clock read was skipped",
			c.i, len(c.times))
	}
}

// at builds a timestamp at ms milliseconds past the Unix epoch.
func at(ms int64) time.Time { return time.Unix(0, 0).Add(time.Duration(ms) * time.Millisecond) }

// turnEndOf returns the TurnEndPayload of the first turn.end event, or fails.
func turnEndOf(t *testing.T, evs []session.Event) *session.TurnEndPayload {
	t.Helper()
	for _, e := range evs {
		if e.Type == session.EvTurnEnd {
			if e.TurnEnd == nil {
				t.Fatalf("turn.end event carried a nil TurnEnd payload")
			}
			return e.TurnEnd
		}
	}
	t.Fatalf("no turn.end event in %v", typesOf(evs))
	return nil
}

// TestTurnEndLatencyMeasured drives a single streamed turn whose content chunks
// arrive at scripted instants and asserts TTFT, the mean inter-token gap, and the
// max gap are computed from the injected Clock.
//
// The clock is read in this order for a single-turn run:
//
//	[0] turnStart   (drive, before runTurn)
//	[1] streamStart  (runTurn, anchors TTFT)
//	[2] content #1 (text)      → TTFT = t2 - t1
//	[3] content #2 (text)      → gap1 = t3 - t2
//	[4] content #3 (text)      → gap2 = t4 - t3
//	[5] durMs       (drive, after runTurn)
func TestTurnEndLatencyMeasured(t *testing.T) {
	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("a"),
			mockllm.TextChunk("b"),
			mockllm.TextChunk("c"),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	// streamStart=10ms; first token at 40ms (TTFT=30); then 50ms (gap 10) and
	// 90ms (gap 40). Mean gap = (10+40)/2 = 25, max = 40. turnStart=5, durMs read
	// at 200ms (turn duration = 195).
	clk := &scriptedClock{times: []time.Time{
		at(5), at(10), at(40), at(50), at(90), at(200),
	}}
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t), Clock: clk})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)

	te := turnEndOf(t, evs)
	if te.TTFTMs != 30 {
		t.Errorf("TTFTMs = %d, want 30", te.TTFTMs)
	}
	if te.InterTokenMeanMs != 25 {
		t.Errorf("InterTokenMeanMs = %d, want 25", te.InterTokenMeanMs)
	}
	if te.InterTokenMaxMs != 40 {
		t.Errorf("InterTokenMaxMs = %d, want 40", te.InterTokenMaxMs)
	}
	if te.DurationMs != 195 {
		t.Errorf("DurationMs = %d, want 195", te.DurationMs)
	}
	clk.assertScriptFullyConsumed(t)
}

// TestTurnEndLatencyMaxIsFirstGap drives a turn whose LARGEST inter-token gap is
// the FIRST one (40ms), followed by a smaller gap (10ms). It asserts the max
// tracks the running maximum (40), not merely the most-recent gap — a regression
// where interTokenMaxMs latched the last gap would wrongly report 10. The mean is
// (40+10)/2 = 25, identical to TestTurnEndLatencyMeasured's, so only the max
// discriminates the two orderings.
//
// Clock reads: [0] turnStart=5, [1] streamStart=10, [2] content #1=20 (TTFT=10),
// [3] content #2=60 (gap1=40), [4] content #3=70 (gap2=10), [5] durMs=210.
func TestTurnEndLatencyMaxIsFirstGap(t *testing.T) {
	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("a"),
			mockllm.TextChunk("b"),
			mockllm.TextChunk("c"),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	clk := &scriptedClock{times: []time.Time{
		at(5), at(10), at(20), at(60), at(70), at(210),
	}}
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t), Clock: clk})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)

	te := turnEndOf(t, evs)
	if te.InterTokenMaxMs != 40 {
		t.Errorf("InterTokenMaxMs = %d, want 40 (the FIRST gap is the largest)", te.InterTokenMaxMs)
	}
	if te.InterTokenMeanMs != 25 { // (40 + 10) / 2
		t.Errorf("InterTokenMeanMs = %d, want 25", te.InterTokenMeanMs)
	}
	clk.assertScriptFullyConsumed(t)
}

// TestTurnEndLatencyReasoningCountsAsContent asserts a ChunkReasoning (the
// human-readable summary) counts as a content chunk for TTFT/inter-token, while a
// ChunkReasoningItem (the opaque replay blob), usage, and done chunks do NOT.
//
// Clock reads: [0] turnStart, [1] streamStart, [2] reasoning #1 → TTFT, [3] text
// #2 → gap, [4] durMs. The ReasoningItem/Usage/Done chunks consume no clock read.
func TestTurnEndLatencyReasoningCountsAsContent(t *testing.T) {
	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.ReasoningChunk("thinking"),
			mockllm.ReasoningItemChunk("BLOB"), // not content: no clock read, no gap
			mockllm.TextChunk("answer"),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	clk := &scriptedClock{times: []time.Time{
		at(0), at(20), at(35), at(60), at(100),
	}}
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t), Clock: clk})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)

	te := turnEndOf(t, evs)
	if te.TTFTMs != 15 { // 35 - 20
		t.Errorf("TTFTMs = %d, want 15", te.TTFTMs)
	}
	if te.InterTokenMeanMs != 25 { // single gap 60 - 35
		t.Errorf("InterTokenMeanMs = %d, want 25", te.InterTokenMeanMs)
	}
	if te.InterTokenMaxMs != 25 {
		t.Errorf("InterTokenMaxMs = %d, want 25", te.InterTokenMaxMs)
	}
	clk.assertScriptFullyConsumed(t)
}

// TestTurnEndLatencyOneContentChunk: a turn with exactly one content chunk has a
// TTFT but NO inter-token summary (no gap exists) — both gap fields must stay 0,
// never a bogus zero observation.
func TestTurnEndLatencyOneContentChunk(t *testing.T) {
	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("only"),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	clk := &scriptedClock{times: []time.Time{at(0), at(5), at(25), at(50)}}
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t), Clock: clk})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)

	te := turnEndOf(t, evs)
	if te.TTFTMs != 20 { // 25 - 5
		t.Errorf("TTFTMs = %d, want 20", te.TTFTMs)
	}
	if te.InterTokenMeanMs != 0 || te.InterTokenMaxMs != 0 {
		t.Errorf("inter-token = (%d,%d), want (0,0) for a single content chunk",
			te.InterTokenMeanMs, te.InterTokenMaxMs)
	}
	clk.assertScriptFullyConsumed(t)
}

// TestTurnEndLatencyToolCallAnchorsTTFT: a turn whose only chunks are a tool call
// (plus usage/done) carries NO streaming content delta, but the tool call IS
// observable output — so TTFT anchors on it (issue #155), while both inter-token
// fields stay 0 (a tool call must NOT seed the gap series).
//
// The script covers EVERY read of the whole two-turn run so the full-consumption
// oracle pins the tool-call branch's SINGLE noteFirstOutput read (a double-read
// regression there would over-run the script and fail loudly). Reads:
//
//	turn 1: [0] turnStart=5, [1] streamStart=10, [2] tool call → TTFT = 40-10 = 30,
//	        [3] durMs=200
//	dispatch: [4] enqueue=210, [5] execStart=215, [6] dur=220
//	turn 2: [7] turnStart=230, [8] streamStart=235, [9] text → TTFT, [10] durMs=300
func TestTurnEndLatencyToolCallAnchorsTTFT(t *testing.T) {
	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(toolCall("c1", "noop", `{}`)),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		// Second turn ends the run (the tool result feeds back; the model stops).
		mockllm.ChunksTurn(
			mockllm.TextChunk("done"),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	noop := &fakeTool{name: "noop", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "ok"), nil
		}}
	clk := &scriptedClock{times: []time.Time{
		at(5), at(10), at(40), at(200), // turn 1
		at(210), at(215), at(220), // dispatch
		at(230), at(235), at(250), at(300), // turn 2
	}}
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, noop), Clock: clk})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)

	// The FIRST turn.end is the tool-call-only turn: the tool call anchors TTFT,
	// but seeds no inter-token gap.
	te := turnEndOf(t, evs)
	if te.TTFTMs != 30 {
		t.Errorf("TTFTMs = %d, want 30 (a tool call anchors TTFT)", te.TTFTMs)
	}
	if te.InterTokenMeanMs != 0 || te.InterTokenMaxMs != 0 {
		t.Errorf("inter-token = (%d,%d), want (0,0) (a tool call must not seed the gap series)",
			te.InterTokenMeanMs, te.InterTokenMaxMs)
	}
	clk.assertScriptFullyConsumed(t)
}

// TestTurnEndLatencyReasoningItemAnchorsTTFT: a turn whose only observable output
// is a reasoning replay item (the opaque encrypted_content blob) anchors TTFT on
// it, with no inter-token gap (the replay blob is not a streamed token).
//
// Clock reads: [0] turnStart=0, [1] streamStart=5, [2] reasoning item → TTFT = 20
// - 5 = 15, [3] durMs=50. Usage/done consume no clock read.
func TestTurnEndLatencyReasoningItemAnchorsTTFT(t *testing.T) {
	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.ReasoningItemChunk("BLOB"),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	clk := &scriptedClock{times: []time.Time{at(0), at(5), at(20), at(50)}}
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t), Clock: clk})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)

	te := turnEndOf(t, evs)
	if te.TTFTMs != 15 {
		t.Errorf("TTFTMs = %d, want 15 (a reasoning replay item anchors TTFT)", te.TTFTMs)
	}
	if te.InterTokenMeanMs != 0 || te.InterTokenMaxMs != 0 {
		t.Errorf("inter-token = (%d,%d), want (0,0) (a replay blob is not a streamed token)",
			te.InterTokenMeanMs, te.InterTokenMaxMs)
	}
	// NOTE: a reasoning-item-only turn carries no text and no tool call, so it is an
	// empty turn that triggers the no-progress nudge (extra turns + dispatch-less
	// clock reads). The full-consumption oracle therefore does not apply here; the
	// TTFT value pins the single noteFirstOutput read for the reasoning-item branch.
}

// TestTurnEndLatencyTwoToolCalls: a turn carrying TWO tool calls and no text or
// reasoning anchors TTFT on the FIRST tool call only (first-only guard), and the
// second tool call must NOT pollute the inter-token gap series — both gap fields
// stay 0.
//
// Clock reads for the first (two-tool-call) turn: [0] turnStart=5, [1]
// streamStart=10, [2] tool call #1 → TTFT = 30 - 10 = 20, [3] tool call #2 → a
// no-op for TTFT and NOT a streaming delta, [4] durMs. Wait — the second tool call
// reads no clock (noteFirstOutput is a no-op after firstOutputSeen), so the script
// is: [2] tool #1 at 30, [3] durMs at 90.
func TestTurnEndLatencyTwoToolCalls(t *testing.T) {
	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(toolCall("c1", "noop", `{}`)),
			mockllm.ToolCallChunk(toolCall("c2", "noop", `{}`)),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk("done"),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	noop := &fakeTool{name: "noop", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "ok"), nil
		}}
	clk := &scriptedClock{times: []time.Time{at(5), at(10), at(30), at(90)}}
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, noop), Clock: clk})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)

	te := turnEndOf(t, evs)
	if te.TTFTMs != 20 {
		t.Errorf("TTFTMs = %d, want 20 (first tool call anchors TTFT)", te.TTFTMs)
	}
	if te.InterTokenMeanMs != 0 || te.InterTokenMaxMs != 0 {
		t.Errorf("inter-token = (%d,%d), want (0,0) (tool calls must not pollute the gap series)",
			te.InterTokenMeanMs, te.InterTokenMaxMs)
	}
}

// TestTurnEndLatencyTwoTextDeltasThenToolCall: TWO text deltas (which DO seed the
// inter-token gap series — one text-to-text gap) followed by a tool call. The tool
// call anchors no further TTFT (first-only guard) and must NOT append a spurious
// gap to the already-non-empty series: the mean and max stay the single text gap.
// This is the real pollution path — the reasoning-delta-then-tool-call test has
// only one streaming delta, so its series is empty and cannot prove a non-empty
// series is left unpolluted.
//
// Clock reads for the first turn: [0] turnStart=5, [1] streamStart=10, [2] text #1
// → TTFT = 30 - 10 = 20, [3] text #2 → gap = 70 - 30 = 40, the tool call reads NO
// clock (firstOutputSeen, not a streaming delta), [4] durMs=120.
func TestTurnEndLatencyTwoTextDeltasThenToolCall(t *testing.T) {
	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("a"),
			mockllm.TextChunk("b"),
			mockllm.ToolCallChunk(toolCall("c1", "noop", `{}`)),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk("done"),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	noop := &fakeTool{name: "noop", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "ok"), nil
		}}
	clk := &scriptedClock{times: []time.Time{at(5), at(10), at(30), at(70), at(120)}}
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, noop), Clock: clk})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)

	te := turnEndOf(t, evs)
	if te.TTFTMs != 20 {
		t.Errorf("TTFTMs = %d, want 20 (first text delta anchors TTFT)", te.TTFTMs)
	}
	// Exactly ONE gap (text#1 → text#2 = 40); the trailing tool call appends none.
	if te.InterTokenMeanMs != 40 {
		t.Errorf("InterTokenMeanMs = %d, want 40 (the single text-to-text gap; the tool call adds no gap)", te.InterTokenMeanMs)
	}
	if te.InterTokenMaxMs != 40 {
		t.Errorf("InterTokenMaxMs = %d, want 40 (the single text-to-text gap; the tool call adds no gap)", te.InterTokenMaxMs)
	}
}

// TestTurnEndLatencyReasoningDeltaThenToolCall: a reasoning DELTA followed by a
// tool call. The first-only guard means TTFT anchors on the reasoning delta
// instant, and the trailing tool call is a no-op for TTFT and never a streaming
// delta — so the inter-token gap series stays empty.
//
// Clock reads: [0] turnStart=0, [1] streamStart=10, [2] reasoning delta → TTFT =
// 25 - 10 = 15, [3] tool call is a no-op (firstOutputSeen) so reads NO clock, [3]
// durMs=80.
func TestTurnEndLatencyReasoningDeltaThenToolCall(t *testing.T) {
	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.ReasoningChunk("thinking"),
			mockllm.ToolCallChunk(toolCall("c1", "noop", `{}`)),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk("done"),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	noop := &fakeTool{name: "noop", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "ok"), nil
		}}
	clk := &scriptedClock{times: []time.Time{at(0), at(10), at(25), at(80)}}
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, noop), Clock: clk})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)

	te := turnEndOf(t, evs)
	if te.TTFTMs != 15 {
		t.Errorf("TTFTMs = %d, want 15 (reasoning delta anchors first; the tool call is a no-op)", te.TTFTMs)
	}
	if te.InterTokenMeanMs != 0 || te.InterTokenMaxMs != 0 {
		t.Errorf("inter-token = (%d,%d), want (0,0) (only one streaming delta; the tool call adds no gap)",
			te.InterTokenMeanMs, te.InterTokenMaxMs)
	}
}

// TestTurnEndLatencyNoClock: with no Clock injected every latency field is 0.
func TestTurnEndLatencyNoClock(t *testing.T) {
	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("a"),
			mockllm.TextChunk("b"),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)}) // no Clock
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)

	te := turnEndOf(t, evs)
	if te.TTFTMs != 0 || te.InterTokenMeanMs != 0 || te.InterTokenMaxMs != 0 || te.DurationMs != 0 {
		t.Errorf("latency fields = (ttft=%d mean=%d max=%d dur=%d), want all 0 without a Clock",
			te.TTFTMs, te.InterTokenMeanMs, te.InterTokenMaxMs, te.DurationMs)
	}
}

// queueRecordingLogger captures the queued/took durations passed to ToolCall,
// keyed by tool name, so a test can assert mutate-serial queueing was measured.
type queueRecordingLogger struct {
	mu     sync.Mutex
	queued map[string]time.Duration
	took   map[string]time.Duration
	order  []string
}

func (l *queueRecordingLogger) ToolCall(_ session.SessionID, call session.ToolCall, _ session.ToolResult, queued, took time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.queued == nil {
		l.queued = map[string]time.Duration{}
		l.took = map[string]time.Duration{}
	}
	l.queued[call.Name] = queued
	l.took[call.Name] = took
	l.order = append(l.order, call.Name)
}

// TestDispatchQueueTimeMeasured asserts queue time is captured from enqueue to
// execution start and passed to Logger.ToolCall, and — crucially — that a second
// mutating tool serialized BEHIND the first records a strictly larger queue time
// than the first (the coordinated-omission measure). The 1ms-step fakeClock makes
// every Now() read advance time, so the later-executing mutate tool, which sits
// through the first tool's gate+execution clock reads, sees a larger enqueue→start
// gap.
func TestDispatchQueueTimeMeasured(t *testing.T) {
	// Two mutating tools in one turn → dispatched serially, one after the other.
	first := &fakeTool{name: "edit_a", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "a"), nil
		}}
	second := &fakeTool{name: "edit_b", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "b"), nil
		}}

	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(toolCall("c1", "edit_a", `{}`)),
			mockllm.ToolCallChunk(toolCall("c2", "edit_b", `{}`)),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk("done"),
			mockllm.UsageChunk(session.Usage{}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	clk := &fakeClock{t: time.Unix(0, 0)} // advances 1ms per Now()
	logger := &queueRecordingLogger{}
	e := newEngine(agent.Deps{
		LLM: llm, Catalog: catalogWith(t, first, second), Clock: clk, ToolCallRecorder: logger,
	})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	_ = drain(r)

	logger.mu.Lock()
	defer logger.mu.Unlock()
	qa, okA := logger.queued["edit_a"]
	qb, okB := logger.queued["edit_b"]
	if !okA || !okB {
		t.Fatalf("missing queued samples: edit_a=%v edit_b=%v (order=%v)", okA, okB, logger.order)
	}
	// Both calls enqueued at the SAME instant (one enqueue read for the whole
	// turn); edit_b executes only after edit_a's gate+execution advanced the clock,
	// so its enqueue→start wait is strictly larger.
	if qb <= qa {
		t.Errorf("edit_b queue time (%v) not > edit_a queue time (%v); mutate-serial queueing not measured", qb, qa)
	}
	// edit_a still sat through at least its own authorize/openCard clock reads
	// before execution started, so its queue time is nonzero too.
	if qa <= 0 {
		t.Errorf("edit_a queue time = %v, want > 0", qa)
	}
}

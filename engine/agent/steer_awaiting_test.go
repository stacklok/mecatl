package agent_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// ---------------------------------------------------------------------------
// Task 04 — steer while awaiting an ask: queue-only, drain on resume.
//
// While a run is parked `awaiting` on a permission ask the loop is suspended
// in PauseForApproval (surfaceAsk → askRegistry.await) — it is NOT iterating
// runLoop, so the Step 2a drain cannot fire until the verdict resumes the
// loop. The inbox must HOLD the pending steer across the parked state (the
// run is parked, not terminal, so closeSteer must NOT have run); the verdict
// then resumes the loop and the first post-resume turn boundary drains it.
// The steer is purely additive input (the "yes-and"/"no-and" case) — it never
// resolves, modifies, or bypasses the pending ask.
// ---------------------------------------------------------------------------

// awaitAskParked consumes events until the run emits its first EvPermissionAsk
// (the run goroutine is then genuinely PARKED in askRegistry.await — the
// session is StateAwaiting and no runLoop iteration is in flight), records the
// ask, and returns the collected events plus the ask. It does NOT resolve the
// ask — the caller owns the verdict timing. Watchdog-bounded so a wedge fails
// rather than hanging the suite.
func awaitAskParked(t *testing.T, r *agent.Run) (evs []session.Event, ask *session.PendingAsk) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	ch := r.Events()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("run terminated before emitting a permission ask; types=%v", typesOf(evs))
			}
			evs = append(evs, ev)
			if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
				return evs, ev.Ask
			}
		case <-deadline:
			r.Cancel()
			t.Fatalf("run did not reach a permission ask within the deadline (possible wedge)")
			return nil, nil
		}
	}
}

// drainRest consumes the remaining events after awaitAskParked returned,
// invoking observe for each, until the run's channel closes.
func drainRest(t *testing.T, r *agent.Run, observe func(session.Event)) []session.Event {
	t.Helper()
	var evs []session.Event
	deadline := time.After(10 * time.Second)
	ch := r.Events()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return evs
			}
			evs = append(evs, ev)
			if observe != nil {
				observe(ev)
			}
		case <-deadline:
			r.Cancel()
			t.Fatalf("run did not terminate within the deadline after the ask (possible wedge)")
			return evs
		}
	}
}

// TestSteer_AwaitingAskIsHeld (AC4.1): a steer submitted while a run is parked
// `awaiting` on a permission ask is ACCEPTED into the inbox (not too_late —
// the run is parked, not terminal, so the inbox must NOT be closed while
// awaiting) and HELD — it is not recorded, echoed, or replayed to the model
// while the ask is still parked (the loop is suspended in PauseForApproval, so
// no turn boundary passes). Only the verdict resumes the loop, and the held
// steer drains at the first post-resume boundary.
func TestSteer_AwaitingAskIsHeld(t *testing.T) {
	const steerText = "steer: held across the parked ask"
	var executed atomic.Bool
	write := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			executed.Store(true)
			return session.NewToolResult(in.ID, "wrote"), nil
		}}
	rec := &requestRecorder{}
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(rec.observe)},
		mockllm.ToolCallTurn(toolCall("c1", "Write", `{"path":"a"}`)),
		mockllm.TextTurn("done after the verdict"),
	)
	e := newEngine(agent.Deps{
		LLM:         llm,
		Catalog:     catalogWith(t, write),
		Policy:      permpolicy.NewPolicy(nil, permstore.New()), // Write asks by default
		EnableSteer: true,
	})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"})
	parked, ask := awaitAskParked(t, r)

	// The run is PARKED awaiting: the inbox must still be OPEN (closeSteer only
	// runs on a genuine terminal), so the steer is accepted, not too_late.
	if got := steerEnqueueOutcome(t, r, steerText); got != agent.SteerAccepted {
		t.Fatalf("enqueue while awaiting outcome = %q, want %q (a parked run is live, not terminal — the inbox must stay open)", got, agent.SteerAccepted)
	}

	// HELD, not delivered early: while the ask is still parked, nothing may
	// record / echo / replay the steer. Give the loop a real chance to misbehave:
	// if a drain could fire while parked, the recorded message + EvSteer echo
	// would appear promptly. We assert their absence on the parked-phase stream
	// and history BEFORE resolving the ask (a drain while parked would also
	// wedge the run — the parked goroutine is inside dispatch, not runLoop).
	if userTextIn(sess.Conversation.Messages, steerText) {
		t.Fatalf("steer %q was recorded while the ask was still parked — it must be HELD until the verdict resumes the loop", steerText)
	}
	if echoes := steerEchoes(parked); len(echoes) != 0 {
		t.Fatalf("EvSteer echoes while parked = %v, want none (the drain must wait for the verdict)", echoes)
	}
	// Turn 1's request is already served (it produced the tool call); it must
	// NOT carry the steer, and there must be exactly ONE model call so far (no
	// phantom turn consumed the steer early).
	if rec.count() != 1 {
		t.Fatalf("model calls while parked = %d, want 1 (no turn may run while the ask is parked)", rec.count())
	}
	if userTextIn(rec.get(0).Messages, steerText) {
		t.Fatalf("steer %q reached the model on turn 1 — it must drain only after the verdict", steerText)
	}

	// Now the verdict resumes the loop: the held steer drains at the first
	// post-resume boundary and replays on the next turn.
	r.Approve(ask.AskID, session.VerdictAllowOnce)
	rest := drainRest(t, r, nil)
	evs := append(parked, rest...)

	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("terminal stop = %q, want end_turn (err %q)", res.Stop, res.Error)
	}
	if !executed.Load() {
		t.Fatalf("the approved tool was not executed after the verdict")
	}
	if !userTextIn(sess.Conversation.Messages, steerText) {
		t.Fatalf("held steer %q not recorded after the resume: %+v", steerText, sess.Conversation.Messages)
	}
	if echoes := steerEchoes(evs); len(echoes) != 1 || echoes[0] != steerText {
		t.Fatalf("EvSteer echoes = %v, want exactly one %q (the held steer drains once, on resume)", echoes, steerText)
	}
	if got := llm.Calls(); got != 2 {
		t.Fatalf("model calls = %d, want 2 (the steer replays on the post-resume turn)", got)
	}
	if !userTextIn(rec.get(1).Messages, steerText) {
		t.Fatalf("held steer %q not replayed to the model on the post-resume turn: %+v", steerText, rec.get(1).Messages)
	}
	// The steer drains at the post-resume BOUNDARY — AFTER the gated tool's
	// result settles — never inside the tool_use pair. Assert the ordering
	// (steer lands after the c1 tool result) and that pairing holds: a drain
	// that fired inside dispatch (before the tool result was recorded) would
	// wedge the user message between the assistant call and its result.
	if err := session.ValidateToolPairing(sess.Conversation.Messages); err != nil {
		t.Fatalf("ValidateToolPairing after the resume drain: %v (the steer must land after the settled tool pair, not inside it)", err)
	}
	steerIdx, toolResultIdx := -1, -1
	for i, m := range sess.Conversation.Messages {
		if m.Role == session.RoleUser && m.Text == steerText {
			steerIdx = i
		}
		if m.Role == session.RoleTool && m.ToolResult != nil && m.ToolResult.CallID == "c1" {
			toolResultIdx = i
		}
	}
	if steerIdx == -1 || toolResultIdx == -1 {
		t.Fatalf("expected the steer (idx %d) and the c1 tool result (idx %d) on history", steerIdx, toolResultIdx)
	}
	if steerIdx < toolResultIdx {
		t.Fatalf("steer (idx %d) recorded BEFORE the gated tool's result (idx %d) — it was delivered early, inside dispatch, not held to the post-resume boundary", steerIdx, toolResultIdx)
	}
}

// TestSteer_AwaitingResumeDrains (AC4.2): on a verdict-driven resume the held
// steer is drained at the resumed run's FIRST turn boundary and replayed to the
// model. This drives the CROSS-PROCESS seam (driveFromAwaiting via
// Engine.ResumeApproval): the parked run is snapshot-dead, the awaiting session
// is restored, the steer is enqueued on the RESUMED run, and the resumed run's
// first Step 2a boundary drains it — the steer lands after the settled tool
// results, before the first post-resume model call.
func TestSteer_AwaitingResumeDrains(t *testing.T) {
	const steerText = "steer: drained at the resumed run's first boundary"
	policy := permpolicy.NewPolicy(nil, permstore.New()) // Write asks by default
	sess := session.New("s-steer-resume", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))

	// Engine #1: emits a Write tool call (gated as Ask), parks awaiting, then
	// "dies" (snapshot round-trip models the process death; the parked run and
	// its askRegistry are discarded).
	write1 := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "wrote"), nil
		}}
	e1 := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(toolCall("w1", "Write", `{"path":"a.go"}`))),
		Catalog: catalogWith(t, write1),
		Policy:  policy,
	})
	askID, restored := driveToAwaiting(t, e1, sess, agent.EnvForWS(memfs.NewWorkspace("/ws"), nil), "go")

	// Engine #2 (the "restarted process"): a FRESH steer-enabled engine over the
	// restored awaiting session. Its mockllm serves only the CONTINUATION turn.
	rec := &requestRecorder{}
	writeStarted := make(chan struct{})
	writeProceed := make(chan struct{})
	var releaseWrite sync.Once
	release := func() { releaseWrite.Do(func() { close(writeProceed) }) }
	defer release()
	write2 := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			close(writeStarted)
			<-writeProceed
			return session.NewToolResult(in.ID, "wrote"), nil
		}}
	e2 := newEngine(agent.Deps{
		LLM:         mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(rec.observe)}, mockllm.TextTurn("done after resume")),
		Catalog:     catalogWith(t, write2),
		Policy:      policy,
		EnableSteer: true,
	})

	// ResumeApproval executes the pending Write before entering runLoop. Wait until
	// that execution is parked, then enqueue so the steer is held through verdict
	// resolution and drains at runLoop's first Step 2a boundary.
	r2 := e2.ResumeApproval(context.Background(), restored, agent.EnvForWS(memfs.NewWorkspace("/ws"), nil), askID, session.VerdictAllowOnce)
	<-writeStarted
	if got := steerEnqueueOutcome(t, r2, steerText); got != agent.SteerAccepted {
		t.Fatalf("enqueue on the resumed run outcome = %q, want %q", got, agent.SteerAccepted)
	}
	release()
	evs := drainObserving(t, r2, nil)

	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("resumed run stop = %q, want end_turn (err %q)", res.Stop, res.Error)
	}
	// The held steer drained: recorded as an ordinary user message, echoed once.
	if !userTextIn(restored.Conversation.Messages, steerText) {
		t.Fatalf("held steer %q not recorded on the resumed run: %+v", steerText, restored.Conversation.Messages)
	}
	if echoes := steerEchoes(evs); len(echoes) != 1 || echoes[0] != steerText {
		t.Fatalf("EvSteer echoes = %v, want exactly one %q (the held steer drains at the resumed run's first boundary)", echoes, steerText)
	}
	// Pairing holds: the steer lands AFTER the settled tool results (the resumed
	// pending call's result + any synthetic sibling close-outs), never inside a
	// tool_use pair.
	if err := session.ValidateToolPairing(restored.Conversation.Messages); err != nil {
		t.Fatalf("ValidateToolPairing after the resume drain: %v", err)
	}
	// Replayed to the model on the resumed run's FIRST post-resume turn (the
	// only model call this run makes — the drain precedes BeginTurn for it).
	if got := rec.count(); got != 1 {
		t.Fatalf("resumed run model calls = %d, want 1 (the steer drains before the first post-resume turn)", got)
	}
	if !userTextIn(rec.get(0).Messages, steerText) {
		t.Fatalf("held steer %q not replayed to the model on the resumed run's first turn: %+v", steerText, rec.get(0).Messages)
	}
	// The drain is sequenced BEFORE the turn it feeds: the EvSteer echo precedes
	// the first post-resume turn.start on the wire.
	var echoSeq, turnStartSeq int64 = -1, -1
	for _, ev := range evs {
		if ev.Type == session.EvSteer && echoSeq == -1 {
			echoSeq = ev.Seq
		}
		if ev.Type == session.EvTurnStart && turnStartSeq == -1 {
			turnStartSeq = ev.Seq
		}
	}
	if echoSeq == -1 || turnStartSeq == -1 {
		t.Fatalf("missing EvSteer echo (seq %d) or turn.start (seq %d); types=%v", echoSeq, turnStartSeq, typesOf(evs))
	}
	if echoSeq >= turnStartSeq {
		t.Fatalf("EvSteer echo seq %d must precede the resumed run's first turn.start (seq %d)", echoSeq, turnStartSeq)
	}
}

// TestSteer_AskStillRequiresVerdict (AC4.3): the held steer does NOT resolve,
// modify, or bypass the pending ask — the ask still requires an explicit
// verdict. Enqueue a steer while parked and DO NOT approve: the run must stay
// parked (the parked tool must NOT execute, no EvApproval may be emitted, the
// session stays StateAwaiting) until a real verdict arrives. The steer is
// purely additive input — the "yes-and" case (approve + steer) still needs the
// explicit approve, and the "no-and" case (deny + steer) still needs the
// explicit deny.
func TestSteer_AskStillRequiresVerdict(t *testing.T) {
	const steerText = "steer: yes-and — additive, never a verdict"
	var executed atomic.Bool
	write := &fakeTool{name: "Write", readOnly: false,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			executed.Store(true)
			return session.NewToolResult(in.ID, "wrote"), nil
		}}
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Write", `{"path":"a"}`)),
		mockllm.TextTurn("done after the verdict"),
	)
	e := newEngine(agent.Deps{
		LLM:         llm,
		Catalog:     catalogWith(t, write),
		Policy:      permpolicy.NewPolicy(nil, permstore.New()), // Write asks by default
		EnableSteer: true,
	})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"})
	parked, ask := awaitAskParked(t, r)

	// Enqueue the steer while parked. It must NOT resolve the ask: no verdict is
	// synthesized, the tool does NOT run, the session stays StateAwaiting.
	if got := steerEnqueueOutcome(t, r, steerText); got != agent.SteerAccepted {
		t.Fatalf("enqueue while awaiting outcome = %q, want %q", got, agent.SteerAccepted)
	}

	// Assert the ask is STILL pending after the steer: the session remains
	// StateAwaiting with the SAME pending ask (unmodified), no EvApproval was
	// emitted, and the gated tool has not executed. This is the core "the steer
	// is not a verdict" assertion — if the enqueue resolved or altered the ask,
	// one of these flips.
	if sess.State != session.StateAwaiting {
		t.Fatalf("session state after steer enqueue = %q, want %q (the steer must not resolve the ask)", sess.State, session.StateAwaiting)
	}
	pending, ok := sess.PendingAsk()
	if !ok {
		t.Fatalf("the pending ask was cleared by the steer enqueue — the ask must survive untouched")
	}
	if pending.AskID != ask.AskID || pending.Tool != ask.Tool {
		t.Fatalf("the pending ask was modified by the steer: before=%+v after=%+v", ask, pending)
	}
	for _, ev := range parked {
		if ev.Type == session.EvApproval {
			t.Fatalf("an EvApproval was emitted without a verdict — the steer must never resolve the ask")
		}
	}
	if executed.Load() {
		t.Fatalf("the gated tool executed without a verdict — the steer must not bypass the ask")
	}

	// THE CORE AC4.3 PROBE: with the steer enqueued but NO explicit verdict given,
	// the run must STAY parked — a steer that resolved/bypassed the ask would
	// un-park the loop, emitting EvApproval + EvToolResult (the tool runs) and
	// eventually closing the channel. Poll the event channel for a bounded window
	// and assert NONE of those arrive. This is the assertion that distinguishes
	// "the explicit verdict un-parked the run" from "the steer did": an
	// auto-resolving steer flips one of these BEFORE we ever call Approve.
	parkDeadline := time.After(300 * time.Millisecond)
parkLoop:
	for {
		select {
		case ev, ok := <-r.Events():
			if !ok {
				t.Fatalf("the run terminated without an explicit verdict — the steer resolved/bypassed the ask")
			}
			parked = append(parked, ev)
			switch ev.Type {
			case session.EvApproval:
				t.Fatalf("an EvApproval was emitted without an explicit verdict — the steer resolved the ask")
			case session.EvToolResult:
				t.Fatalf("the gated tool produced a result without an explicit verdict — the steer bypassed the ask")
			}
		case <-parkDeadline:
			// No verdict-driven event arrived in the window: the run is still
			// parked on the ask, exactly as required. Break and give the verdict.
			break parkLoop
		}
	}
	if executed.Load() {
		t.Fatalf("the gated tool executed during the no-verdict window — the steer bypassed the ask")
	}

	// The explicit verdict is what un-parks the run — approve now and confirm the
	// run completes, the tool runs, and the held steer drains as ADDITIVE input
	// (the yes-and case: approve AND steer, both honoured, neither replacing the
	// other).
	r.Approve(ask.AskID, session.VerdictAllowOnce)
	rest := drainRest(t, r, nil)
	evs := append(parked, rest...)

	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("terminal stop = %q, want end_turn (err %q)", res.Stop, res.Error)
	}
	if !executed.Load() {
		t.Fatalf("the approved tool was not executed after the explicit verdict")
	}
	// Exactly ONE EvApproval, carrying the explicit verdict — the steer added
	// input but the verdict came from Approve, not from the steer.
	var approvals int
	for _, ev := range evs {
		if ev.Type == session.EvApproval {
			approvals++
			if ev.Approval == nil || ev.Approval.AskID != ask.AskID {
				t.Fatalf("EvApproval does not name the parked ask %q: %+v", ask.AskID, ev.Approval)
			}
		}
	}
	if approvals != 1 {
		t.Fatalf("EvApproval count = %d, want exactly 1 (the explicit verdict; the steer synthesizes none)", approvals)
	}
	// The held steer drained as additive input after the verdict (yes-and).
	if !userTextIn(sess.Conversation.Messages, steerText) {
		t.Fatalf("held steer %q not recorded after the verdict (the yes-and case): %+v", steerText, sess.Conversation.Messages)
	}
	if echoes := steerEchoes(evs); len(echoes) != 1 || echoes[0] != steerText {
		t.Fatalf("EvSteer echoes = %v, want exactly one %q", echoes, steerText)
	}
	// The model sees BOTH the verdict's tool result AND the additive steer: the
	// run makes exactly two model calls (turn 1 gated the tool; turn 2 runs after
	// the verdict with the settled tool result + the drained steer on history).
	if rec := llm.Calls(); rec != 2 {
		t.Fatalf("model calls = %d, want 2 (verdict settles the tool, then the steered turn runs)", rec)
	}
}

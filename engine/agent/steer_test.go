package agent_test

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// userTextIn reports whether a user-role message with the given text appears in
// the message slice.
func userTextIn(msgs []session.Message, text string) bool {
	for _, m := range msgs {
		if m.Role == session.RoleUser && m.Text == text {
			return true
		}
	}
	return false
}

// firstTurnGate is a mockllm request observer that parks the FIRST model call
// until the test releases it, giving a deterministic MID-FLIGHT enqueue anchor.
// The first Stream invocation signals entered, then blocks on proceed; the test
// waits on entered (so turn 1's drain has already run and turn 1's request is
// being served — it can never carry the steer), enqueues, then releases. The
// steer is thus guaranteed to drain at the NEXT turn boundary (before turn 2),
// removing the enqueue-vs-turn-1-drain race a bare enqueue-after-Run leaves.
type firstTurnGate struct {
	entered chan struct{}
	proceed chan struct{}
	sigOnce sync.Once
	relOnce sync.Once
	seen    atomic.Bool
	rec     *requestRecorder
}

func newFirstTurnGate() *firstTurnGate {
	return &firstTurnGate{
		entered: make(chan struct{}),
		proceed: make(chan struct{}),
		rec:     &requestRecorder{},
	}
}

// observe is the mockllm WithRequestObserver callback.
func (g *firstTurnGate) observe(req port.LLMRequest) {
	if g.seen.CompareAndSwap(false, true) {
		g.sigOnce.Do(func() { close(g.entered) })
		<-g.proceed // park ONLY the first model call until the test enqueues
	}
	g.rec.observe(req)
}

// awaitEntered blocks until turn 1's model call has reached the observer (the
// run is genuinely mid-flight with turn 1's drain already done).
func (g *firstTurnGate) awaitEntered() { <-g.entered }

// release lets the parked first model call proceed. Idempotent.
func (g *firstTurnGate) release() { g.relOnce.Do(func() { close(g.proceed) }) }

// steerEnqueue enqueues a text-only steer through the canonical entry point. It
// asserts the enqueue was accepted (these tests enqueue into an empty live slot).
func steerEnqueue(t *testing.T, r *agent.Run, text string) {
	t.Helper()
	outcome, err := agent.EnqueueSteerForTest(r, text, nil)
	if err != nil {
		t.Fatalf("enqueue steer %q: %v", text, err)
	}
	if outcome != agent.SteerAccepted {
		t.Fatalf("enqueue steer %q outcome = %q, want %q", text, outcome, agent.SteerAccepted)
	}
}

// TestSteer_InjectedAtTurnBoundary (AC1.1): a steer enqueued while a run is
// mid-flight is recorded as an ordinary user message at the next turn boundary,
// appears in Conversation.Messages, and is replayed to the model on the
// following turn.
func TestSteer_InjectedAtTurnBoundary(t *testing.T) {
	const steerText = "steer: prefer the smaller refactor"
	gate := newFirstTurnGate()
	rec := gate.rec
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(gate.observe)},
		mockllm.ToolCallTurn(toolCall("c1", "Probe", `{}`)),
		mockllm.TextTurn("done after steer"),
	)
	e := newEngine(agent.Deps{
		LLM:         llm,
		Catalog:     catalogWith(t, &probeTool{}),
		EnableSteer: true,
	})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"})
	// Enqueue while the run is genuinely MID-FLIGHT: awaitEntered confirms turn
	// 1's model call is parked (turn 1's drain already ran), so the steer drains
	// at the NEXT boundary (before turn 2), never on turn 1's served request.
	gate.awaitEntered()
	steerEnqueue(t, r, steerText)
	gate.release()
	evs := drainObserving(t, r, nil)

	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("terminal stop = %q, want end_turn (a steer must not break the run; err %q)", res.Stop, res.Error)
	}
	// The steer must be recorded as an ordinary user message on history.
	if !userTextIn(sess.Conversation.Messages, steerText) {
		t.Fatalf("steer %q not recorded in Conversation.Messages: %+v", steerText, sess.Conversation.Messages)
	}
	// Exactly two model calls (turn 1 tool call → turn 2 text answer); the steer
	// is replayed on the SECOND turn's request (the turn after the boundary).
	if got := llm.Calls(); got != 2 {
		t.Fatalf("model calls = %d, want 2 (steer injected at the boundary before turn 2)", got)
	}
	if !userTextIn(rec.get(1).Messages, steerText) {
		t.Fatalf("steer %q not replayed to the model on turn 2's request: %+v", steerText, rec.get(1).Messages)
	}
	// Turn 1 must NOT have seen it (it was enqueued mid-flight, after turn 1 was
	// already underway or before — but the DRAIN is at the boundary before turn 2).
	if userTextIn(rec.get(0).Messages, steerText) {
		t.Fatalf("steer must not appear on turn 1's request (it drains at the boundary before turn 2)")
	}
}

// TestSteer_RecordedAndRehydrated (AC1.2): the steer is recorded via
// recordContinuation, so the durable log captures it (log-only EvUserPrompt) and
// eventsource.Fold reconstructs the user turn across rehydration.
func TestSteer_RecordedAndRehydrated(t *testing.T) {
	const steerText = "steer: use the eventsource fold"
	eventLog := memstore.NewEventLog()
	gate := newFirstTurnGate()
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(gate.observe)},
		mockllm.ToolCallTurn(toolCall("c1", "Probe", `{}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{
		LLM:         llm,
		Catalog:     catalogWith(t, &probeTool{}),
		EnableSteer: true,
	})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"})
	gate.awaitEntered()
	steerEnqueue(t, r, steerText)
	gate.release()
	evs := drainObserving(t, r, nil)
	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("terminal stop = %q, want end_turn (err %q)", res.Stop, res.Error)
	}

	// Persist the emitted stream to the durable log the way the relay does, then
	// fold it back — ADR-0038 rehydration. The steer's EvUserPrompt must be in
	// the stream so the fold rebuilds the user turn.
	for _, ev := range evs {
		if err := eventLog.Append(context.Background(), sess.ID, ev); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	// Sanity: the emitted stream carried the steer's log-only EvUserPrompt.
	var sawSteerPrompt bool
	for _, ev := range evs {
		if ev.Type == session.EvUserPrompt && ev.UserPrompt != nil && ev.UserPrompt.Text == steerText {
			sawSteerPrompt = true
		}
	}
	if !sawSteerPrompt {
		t.Fatalf("the steer's log-only EvUserPrompt was not emitted; types=%v", typesOf(evs))
	}

	folded, err := eventsource.Fold(eventsource.SessionMeta{
		ID:             sess.ID,
		Mode:           sess.Mode,
		Limits:         sess.Limits,
		EnvironmentRef: sess.EnvironmentRef,
	}, eventLog.Read(context.Background(), sess.ID))
	if err != nil {
		t.Fatalf("eventsource.Fold: %v", err)
	}
	if !userTextIn(folded.Conversation.Messages, steerText) {
		t.Fatalf("rehydrated conversation lost the steer %q: %+v", steerText, folded.Conversation.Messages)
	}
}

// TestSteer_PreservesToolPairing (AC1.3): the steer is appended AFTER the
// settled tool results, never inside a tool_use pair — ValidateToolPairing
// holds on the post-injection history.
func TestSteer_PreservesToolPairing(t *testing.T) {
	const steerText = "steer: keep pairing valid"
	gate := newFirstTurnGate()
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(gate.observe)},
		mockllm.ToolCallTurn(toolCall("c1", "Probe", `{}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{
		LLM:         llm,
		Catalog:     catalogWith(t, &probeTool{}),
		EnableSteer: true,
	})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"})
	// Mid-flight enqueue (gated on turn 1) so the steer drains at the NEXT
	// boundary — after turn 1's tool pair has settled, never inside it.
	gate.awaitEntered()
	steerEnqueue(t, r, steerText)
	gate.release()
	evs := drainObserving(t, r, nil)
	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("terminal stop = %q, want end_turn (err %q)", res.Stop, res.Error)
	}

	msgs := sess.Conversation.Messages
	if err := session.ValidateToolPairing(msgs); err != nil {
		t.Fatalf("ValidateToolPairing failed on the post-injection history: %v\n%+v", err, msgs)
	}
	// The steer (a user message) must come AFTER the tool result that answers
	// c1 — i.e. it lands after the settled tool pair, not between the assistant
	// tool call and its result.
	var steerIdx, toolResultIdx = -1, -1
	for i, m := range msgs {
		if m.Role == session.RoleUser && m.Text == steerText {
			steerIdx = i
		}
		if m.Role == session.RoleTool {
			toolResultIdx = i
		}
	}
	if steerIdx == -1 {
		t.Fatalf("steer not found on history")
	}
	if toolResultIdx == -1 {
		t.Fatalf("no tool result on history (script must dispatch the Probe call)")
	}
	if steerIdx < toolResultIdx {
		t.Fatalf("steer (idx %d) recorded before the settled tool result (idx %d) — it must drain at the boundary AFTER the pair settles", steerIdx, toolResultIdx)
	}
}

// TestSteer_KeepsPromptPrefixByteStable (AC1.4): the injected conversation
// keeps the message prefix byte-stable within the run — turn 2's request prefix
// (before the steer's appended message) equals turn 1's whole request, so the
// steer costs no prompt-cache rebuild beyond normal history growth.
func TestSteer_KeepsPromptPrefixByteStable(t *testing.T) {
	const steerText = "steer: keep the cache prefix"
	gate := newFirstTurnGate()
	rec := gate.rec
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(gate.observe)},
		mockllm.ToolCallTurn(toolCall("c1", "Probe", `{}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{
		LLM:         llm,
		Catalog:     catalogWith(t, &probeTool{}),
		EnableSteer: true,
	})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"})
	// Mid-flight enqueue (gated on turn 1): the steer drains at the boundary
	// before turn 2, so turn 2's request = turn 1's prefix + appended growth.
	gate.awaitEntered()
	steerEnqueue(t, r, steerText)
	gate.release()
	evs := drainObserving(t, r, nil)
	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("terminal stop = %q (err %q)", res.Stop, res.Error)
	}
	if got := llm.Calls(); got != 2 {
		t.Fatalf("model calls = %d, want 2", got)
	}

	turn1 := rec.get(0).Messages
	turn2 := rec.get(1).Messages
	// Turn 2 = turn 1's messages (the byte-stable prefix) PLUS the appended
	// assistant reply, tool result, and the steered user message. Assert the
	// leading turn-1 messages are byte-identical in turn 2 (no re-assembly or
	// re-serialization that would bust the prompt cache).
	if len(turn2) <= len(turn1) {
		t.Fatalf("turn 2 request (%d msgs) must grow past turn 1 (%d msgs) by the appended turn-1 reply + steer", len(turn2), len(turn1))
	}
	for i := range turn1 {
		if !reflect.DeepEqual(turn2[i], turn1[i]) {
			t.Fatalf("prefix message %d diverged between turns (prompt-cache bust):\n turn1: %+v\n turn2: %+v", i, turn1[i], turn2[i])
		}
	}
	// And the steer must be the LAST user message appended (the growth), replayed.
	if !userTextIn(turn2[len(turn1):], steerText) {
		t.Fatalf("steer %q not in the appended (post-prefix) region of turn 2: %+v", steerText, turn2[len(turn1):])
	}
}

// TestSteer_EmptyInboxNoOp (AC1.5): a run with an empty inbox drives exactly as
// before — no behavioural change when steer is unused. Mutation guard: with
// steer enabled but never enqueued, the run is byte-identical to a steer-less
// run (same turns, same recorded history, same terminal).
func TestSteer_EmptyInboxNoOp(t *testing.T) {
	turns := func() []mockllm.Turn {
		return []mockllm.Turn{
			mockllm.ToolCallTurn(toolCall("c1", "Probe", `{}`)),
			mockllm.TextTurn("the answer"),
		}
	}
	// Steer ENABLED but nothing ever enqueued.
	rec := &requestRecorder{}
	eOn := newEngine(agent.Deps{
		LLM:         mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(rec.observe)}, turns()...),
		Catalog:     catalogWith(t, &probeTool{}),
		EnableSteer: true,
	})
	sessOn := newSession(t, session.Limits{})
	evsOn := drainObserving(t, eOn.Run(context.Background(), sessOn, agent.EnvForWS(memfs.NewWorkspace("/ws"), nil), agent.RunRequest{Text: "go"}), nil)

	// Steer DISABLED (the pre-steer posture).
	eOff := newEngine(agent.Deps{
		LLM:         mockllm.New(turns()...),
		Catalog:     catalogWith(t, &probeTool{}),
		EnableSteer: false,
	})
	sessOff := newSession(t, session.Limits{})
	evsOff := drainObserving(t, eOff.Run(context.Background(), sessOff, agent.EnvForWS(memfs.NewWorkspace("/ws"), nil), agent.RunRequest{Text: "go"}), nil)

	// Same terminal, same number of model calls, identical recorded history.
	if lastResult(t, evsOn).Stop != lastResult(t, evsOff).Stop {
		t.Fatalf("empty-inbox run terminal %q differs from no-steer %q", lastResult(t, evsOn).Stop, lastResult(t, evsOff).Stop)
	}
	if lastResult(t, evsOn).Text != "the answer" {
		t.Fatalf("empty-inbox run answer = %q, want %q", lastResult(t, evsOn).Text, "the answer")
	}
	if len(sessOn.Conversation.Messages) != len(sessOff.Conversation.Messages) {
		t.Fatalf("empty-inbox history length %d differs from no-steer %d (the drain must be a no-op)", len(sessOn.Conversation.Messages), len(sessOff.Conversation.Messages))
	}
	for i := range sessOn.Conversation.Messages {
		if !reflect.DeepEqual(sessOn.Conversation.Messages[i], sessOff.Conversation.Messages[i]) {
			t.Fatalf("empty-inbox history message %d differs from no-steer run — the drain must be a strict no-op", i)
		}
	}
	// The run must have taken exactly 2 turns (no phantom extra turn from the drain).
	if got := rec.count(); got != 2 {
		t.Fatalf("empty-inbox model calls = %d, want 2 (no extra turn)", got)
	}
}

// TestSteer_SurvivesCompactionBoundary (AC1.6): a steer drained at a boundary
// where compaction ALSO fires is still replayed verbatim to the model on the
// following turn — the compaction recent-user back-snap keeps the just-drained
// user message out of the summarized head.
func TestSteer_SurvivesCompactionBoundary(t *testing.T) {
	const steerText = "steer: survive compaction verbatim"
	gate := newFirstTurnGate()
	rec := gate.rec
	// Turn 1: a tool call (so history has a settled pair). Between turn 1 and
	// turn 2 the steer drains AND compaction fires. Turn 2 must replay the steer.
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(gate.observe)},
		mockllm.ToolCallTurn(toolCall("c1", "Probe", `{}`)),
		mockllm.TextTurn("done after compaction"),
	)
	e := newEngine(agent.Deps{
		LLM:     llm,
		Catalog: catalogWith(t, &probeTool{}),
		// A TINY context window forces compaction to evaluate (and fire) at the
		// turn-2 boundary, right after the steer drains. Ratio 1.0 → threshold =
		// window; the heuristic counter grows past any tiny positive window once
		// a couple of messages exist.
		ContextWindow:   func() int { return 1 },
		CompactionRatio: 1.0,
		EnableSteer:     true,
	})
	sess := newSession(t, session.Limits{})
	seed := []session.Message{session.NewUserMessage("older goal")}
	for i := 0; i < 20; i++ {
		seed = append(seed, session.NewAssistantMessage(strings.Repeat("settled work ", 20), "", nil))
	}
	if err := sess.SeedHistory(seed); err != nil {
		t.Fatalf("SeedHistory: %v", err)
	}
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"})
	gate.awaitEntered()
	steerEnqueue(t, r, steerText)
	gate.release()
	evs := drainObserving(t, r, nil)
	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("terminal stop = %q (err %q)", res.Stop, res.Error)
	}
	// Compaction must actually have fired at the boundary (else the test is
	// vacuous — it would pass without exercising the snap).
	if !containsType(evs, session.EvCompaction) {
		t.Fatalf("compaction did not fire at the steer boundary; types=%v", typesOf(evs))
	}
	if got := llm.Calls(); got != 2 {
		t.Fatalf("model calls = %d, want 2", got)
	}
	// The just-drained steer is a recent USER turn: the back-snap keeps it
	// verbatim in the tail, so turn 2 replays it VERBATIM (not summarized away).
	if !userTextIn(rec.get(1).Messages, steerText) {
		t.Fatalf("steer %q was summarized away by compaction instead of surviving verbatim in the replayed tail: %+v", steerText, rec.get(1).Messages)
	}
}

// TestSteer_BrakeTerminalKeepsRecorded (AC1.7): a pending steer that trips the
// turn-limit / token-budget brake at the next boundary is STILL recorded
// (durable history) and the run terminates StopMaxTurns/StopBudget normally; the
// recorded steer is addressed by the NEXT run — never silently dropped, never
// bypassing BeginTurn's bound.
func TestSteer_BrakeTerminalKeepsRecorded(t *testing.T) {
	const steerText = "steer: recorded even when the brake trips"
	// Turn-limit brake: MaxTurns=1. Turn 1 makes a tool call (a run that would
	// continue). The steer drains at the boundary BEFORE BeginTurn for turn 2 —
	// but preTurnTerminal trips the recorded MaxTurns limit first, terminating the
	// run. The steer must STILL be on durable history.
	gate := newFirstTurnGate()
	t.Cleanup(gate.release)
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(gate.observe)},
		mockllm.ToolCallTurn(toolCall("c1", "Probe", `{}`)),
		// No further scripted turn needed: the limit trips before turn 2.
	)
	e := newEngine(agent.Deps{
		LLM:         llm,
		Catalog:     catalogWith(t, &probeTool{}),
		EnableSteer: true,
	})
	sess := newSession(t, session.Limits{MaxTurns: 1})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"})
	gate.awaitEntered()
	steerEnqueue(t, r, steerText)
	gate.release()
	evs := drainObserving(t, r, nil)

	res := lastResult(t, evs)
	if res.Stop != session.StopMaxTurns {
		t.Fatalf("terminal stop = %q, want %q (the turn limit must trip at the boundary; err %q)", res.Stop, session.StopMaxTurns, res.Error)
	}
	// The steer is durable history even though the run braked.
	if !userTextIn(sess.Conversation.Messages, steerText) {
		t.Fatalf("brake-terminated run dropped the steer %q from durable history: %+v", steerText, sess.Conversation.Messages)
	}
	// Pairing holds (no dangling tool_use from turn 1's settled pair + steer).
	if err := session.ValidateToolPairing(sess.Conversation.Messages); err != nil {
		t.Fatalf("ValidateToolPairing after brake: %v", err)
	}
	// Exactly ONE model call: the brake tripped before BeginTurn for turn 2 — the
	// steer never bypassed the turn bound to reach the model this run.
	if got := llm.Calls(); got != 1 {
		t.Fatalf("model calls = %d, want 1 (the turn-limit brake tripped before turn 2)", got)
	}

	// The recorded steer is addressed by the NEXT run: reopen the (completed)
	// session and drive a follow-up; the new run replays the carried steer.
	rec2 := &requestRecorder{}
	llm2 := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(rec2.observe)},
		mockllm.TextTurn("addressed the steer"),
	)
	e2 := newEngine(agent.Deps{
		LLM:         llm2,
		Catalog:     catalogWith(t, &probeTool{}),
		EnableSteer: true,
	})
	if err := sess.Reopen(); err != nil {
		t.Fatalf("Reopen after StopMaxTurns: %v", err)
	}
	evs2 := drainObserving(t, e2.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "continue"}), nil)
	if res2 := lastResult(t, evs2); res2.Stop != session.StopEndTurn {
		t.Fatalf("follow-up run stop = %q (err %q)", res2.Stop, res2.Error)
	}
	if rec2.count() < 1 || !userTextIn(rec2.get(0).Messages, steerText) {
		t.Fatalf("next run must replay the carried steer %q to the model: %+v", steerText, rec2.get(0).Messages)
	}
}

// TestSteer_PendingSteerLostOnRestart (AC1.8): a pending (un-drained) steer is
// best-effort, in-memory, and LOST with the run on a process crash / session
// Abandon — the explicit honest contract; it is NOT persisted across restart.
//
// Round-2 note: a clean Cancel is no longer lossy — the cancel terminate path
// close-DRAINS a parked steer into durable history before closing the inbox
// (R2-6, never closed-unconsumed). A true crash-orphan (the run goroutine dies
// without running any terminate path) still loses the pending steer, which is
// the contract this AC pins: the inbox never persists independent of the loop.
// The crash is modelled by freezing the durable state while the run is
// genuinely mid-flight with the steer pending (the crash-orphaned "running"
// snapshot a dead process leaves in the store); the parked gate is released
// only at the end for goleak cleanup, after the frozen snapshot is taken.
func TestSteer_PendingSteerLostOnRestart(t *testing.T) {
	const steerText = "steer: never persisted"
	// A gate tool parks mid-turn so the steer stays PENDING (enqueued but never
	// drained) while the durable state is frozen — the crash-orphaned shape.
	gate := newBgGateTool()
	store := memstore.New()
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Gate", `{}`)),
	)
	e := newEngine(agent.Deps{
		LLM:         llm,
		Catalog:     catalogWith(t, gate),
		Store:       store,
		EnableSteer: true,
	})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"})
	// Wait until the run is genuinely parked inside the gate tool (mid-turn), so
	// the steer enqueued now stays PENDING (the boundary drain already passed
	// for this turn and the run is blocked inside dispatch).
	<-gate.started
	steerEnqueue(t, r, steerText)
	// CRASH MODEL: marshal the "running" snapshot while the run is genuinely
	// mid-flight and the steer is pending — this is exactly the crash-orphaned
	// durable state a dead process leaves behind (the store holds a running
	// snapshot; the pending steer lives only on the crashed run's inbox).
	snap, err := sessnap.Marshal(sess)
	if err != nil {
		t.Fatalf("snapshot marshal: %v", err)
	}

	// The steer was never drained (the run is parked): it must NOT be in the
	// live session history.
	if userTextIn(sess.Conversation.Messages, steerText) {
		t.Fatalf("pending steer %q was drained/recorded before the crash — it must stay pending-only", steerText)
	}

	// Rehydrate from the durable snapshot (restart) and confirm the pending
	// steer is GONE — the honest best-effort contract.
	rehydrated, err := sessnap.Unmarshal(snap)
	if err != nil {
		t.Fatalf("snapshot unmarshal: %v", err)
	}
	if userTextIn(rehydrated.Conversation.Messages, steerText) {
		t.Fatalf("pending steer %q survived restart — it is in-memory best-effort and must be lost", steerText)
	}
	// And nothing in the persisted snapshot carries it either.
	if strings.Contains(string(snap), steerText) {
		t.Fatalf("pending steer leaked into the persisted snapshot")
	}
	// The rehydrated crash-orphaned running session is recoverable (Idle after
	// Abandon repair) and the repaired history does NOT see the lost steer.
	if rehydrated.State != session.StateRunning {
		t.Fatalf("rehydrated state = %q, want running (the crash-orphaned shape)", rehydrated.State)
	}
	if err := rehydrated.Abandon(); err != nil {
		t.Fatalf("Abandon crash-orphaned running session: %v", err)
	}
	if userTextIn(rehydrated.Conversation.Messages, steerText) {
		t.Fatalf("after Abandon repair the steer must still be absent (it was never durable)")
	}

	// Cleanup: release the parked gate and let the orphaned run unwind (the
	// snapshot above is already frozen, so the late drain cannot rewrite it).
	gate.releaseOnce()
	_ = drainObserving(t, r, nil)
}

// ---------------------------------------------------------------------------
// Task 02 — the single-slot inbox's append-default contract, the cancel
// contract, and the drain echo.
// ---------------------------------------------------------------------------

// steerEnqueueOutcome enqueues a steer and returns the engine's authoritative
// SteerOutcome (the enum, not a bool). It fails the test on the legacy
// error-return shape so a task-01 inbox that never grew the outcome API cannot
// silently pass.
func steerEnqueueOutcome(t *testing.T, r *agent.Run, text string) agent.SteerOutcome {
	t.Helper()
	outcome, err := agent.EnqueueSteerForTest(r, text, nil)
	if err != nil {
		t.Fatalf("enqueue steer %q returned an error %v — task 02 returns a SteerOutcome, not an error", text, err)
	}
	return outcome
}

// steerCancelOutcome cancels the pending steer and returns the outcome.
func steerCancelOutcome(t *testing.T, r *agent.Run) agent.SteerOutcome {
	t.Helper()
	outcome, err := agent.CancelSteerForTest(r)
	if err != nil {
		t.Fatalf("cancel steer returned an error %v — task 02 returns a SteerOutcome, not an error", err)
	}
	return outcome
}

// steerEchoes collects the committed-text echoes off the drained event stream.
func steerEchoes(evs []session.Event) []string {
	var out []string
	for _, ev := range evs {
		if ev.Type == session.EvSteer && ev.Steer != nil {
			out = append(out, ev.Steer.Text)
		}
	}
	return out
}

// TestSteer_AppendMultipleMerges (R2-1): at most one steer bundle is pending
// per run; subsequent enqueues append to that bundle. The merged text is
// drained, recorded, echoed, and replayed as one continuation.
func TestSteer_AppendMultipleMerges(t *testing.T) {
	const parked = "steer: the parked instruction"
	const appended = "steer: appended instruction"
	gate := newFirstTurnGate()
	rec := gate.rec
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(gate.observe)},
		mockllm.ToolCallTurn(toolCall("c1", "Probe", `{}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{
		LLM:         llm,
		Catalog:     catalogWith(t, &probeTool{}),
		EnableSteer: true,
	})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"})
	// Park turn 1 so both enqueues land in the SAME pending window (the drain for
	// the next boundary has not run yet).
	gate.awaitEntered()
	if got := steerEnqueueOutcome(t, r, parked); got != agent.SteerAccepted {
		t.Fatalf("first enqueue outcome = %q, want %q", got, agent.SteerAccepted)
	}
	// Append a second steer to the occupied slot; the bundle grows by
	// "\n\n"+appended.
	if got := steerEnqueueOutcome(t, r, appended); got != agent.SteerAppended {
		t.Fatalf("second enqueue outcome = %q, want %q (an occupied slot appends)", got, agent.SteerAppended)
	}
	// A third enqueue appends too — the bundle keeps growing in the single slot.
	if got := steerEnqueueOutcome(t, r, "steer: third"); got != agent.SteerAppended {
		t.Fatalf("third enqueue outcome = %q, want %q (append keeps merging)", got, agent.SteerAppended)
	}
	gate.release()
	evs := drainObserving(t, r, nil)

	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("terminal stop = %q (err %q)", res.Stop, res.Error)
	}
	// The merged bundle (parked + appended + third) drains as ONE continuation.
	merged := parked + "\n\n" + appended + "\n\nsteer: third"
	if !userTextIn(sess.Conversation.Messages, merged) {
		t.Fatalf("merged steer %q not recorded: %+v", merged, sess.Conversation.Messages)
	}
	// Exactly ONE echo, carrying the merged text.
	echoes := steerEchoes(evs)
	if len(echoes) != 1 || echoes[0] != merged {
		t.Fatalf("EvSteer echoes = %v, want exactly one %q (the committed merged text)", echoes, merged)
	}
	// The model sees the merged text on the turn after the boundary.
	if !userTextIn(rec.get(1).Messages, merged) {
		t.Fatalf("merged steer %q not replayed to the model: %+v", merged, rec.get(1).Messages)
	}
}

// TestSteer_NoSupersedePath: the old supersede behavior is gone — a second
// enqueue appends to the pending bundle. Explicit replacement is
// cancel-then-resend: cancel retracts the bundle, and a fresh enqueue is
// accepted into the empty slot.
func TestSteer_NoSupersedePath(t *testing.T) {
	const first = "steer: v1 (retracted by the explicit replace)"
	const second = "steer: v2 (the resent instruction)"
	gate := newFirstTurnGate()
	rec := gate.rec
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(gate.observe)},
		mockllm.ToolCallTurn(toolCall("c1", "Probe", `{}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{
		LLM:         llm,
		Catalog:     catalogWith(t, &probeTool{}),
		EnableSteer: true,
	})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"})
	gate.awaitEntered()
	if got := steerEnqueueOutcome(t, r, first); got != agent.SteerAccepted {
		t.Fatalf("first enqueue outcome = %q, want %q", got, agent.SteerAccepted)
	}
	// Append to the single pending bundle and then cancel it; cancel retracts the
	// whole merged bundle.
	if got := steerEnqueueOutcome(t, r, second); got != agent.SteerAppended {
		t.Fatalf("second enqueue on an occupied slot outcome = %q, want %q (append, not replace)", got, agent.SteerAppended)
	}
	// The explicit REPLACE path (distinct from append): cancel retracts the whole
	// merged bundle (v1+v2), then a fresh enqueue wins the (now empty) slot.
	if got := steerCancelOutcome(t, r); got != agent.SteerRetracted {
		t.Fatalf("cancel outcome = %q, want %q (replace = cancel-then-resend)", got, agent.SteerRetracted)
	}
	const replacement = "steer: v3 (the replacement after cancel)"
	if got := steerEnqueueOutcome(t, r, replacement); got != agent.SteerAccepted {
		t.Fatalf("post-cancel resend outcome = %q, want %q (the slot is empty after the retract)", got, agent.SteerAccepted)
	}
	gate.release()
	evs := drainObserving(t, r, nil)

	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("terminal stop = %q (err %q)", res.Stop, res.Error)
	}
	// The retracted merged bundle never drains; only the replacement commits.
	if userTextIn(sess.Conversation.Messages, first) {
		t.Fatalf("retracted steer %q was recorded — a retracted steer must drain nothing", first)
	}
	if !userTextIn(sess.Conversation.Messages, replacement) {
		t.Fatalf("replacement steer %q not recorded: %+v", replacement, sess.Conversation.Messages)
	}
	echoes := steerEchoes(evs)
	if len(echoes) != 1 || echoes[0] != replacement {
		t.Fatalf("EvSteer echoes = %v, want exactly one %q", echoes, replacement)
	}
	if userTextIn(rec.get(1).Messages, first) {
		t.Fatalf("retracted steer %q reached the model", first)
	}
	if !userTextIn(rec.get(1).Messages, replacement) {
		t.Fatalf("replacement steer %q not replayed to the model: %+v", replacement, rec.get(1).Messages)
	}
}

// TestSteer_CancelRetractsPending (AC2.2): steer_cancel on a pending steer
// retracts it (outcome retracted); the run drains nothing and the next turn
// sees no injected message. A cancel with nothing pending reports none_pending.
func TestSteer_CancelRetractsPending(t *testing.T) {
	const steerText = "steer: retracted before the boundary"
	gate := newFirstTurnGate()
	rec := gate.rec
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(gate.observe)},
		mockllm.ToolCallTurn(toolCall("c1", "Probe", `{}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{
		LLM:         llm,
		Catalog:     catalogWith(t, &probeTool{}),
		EnableSteer: true,
	})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"})
	gate.awaitEntered()
	if got := steerEnqueueOutcome(t, r, steerText); got != agent.SteerAccepted {
		t.Fatalf("enqueue outcome = %q, want %q", got, agent.SteerAccepted)
	}
	// Retract the pending steer before the boundary drain.
	if got := steerCancelOutcome(t, r); got != agent.SteerRetracted {
		t.Fatalf("cancel outcome = %q, want %q (a pending steer is retracted)", got, agent.SteerRetracted)
	}
	// A second cancel has nothing to retract.
	if got := steerCancelOutcome(t, r); got != agent.SteerNonePending {
		t.Fatalf("second cancel outcome = %q, want %q (nothing pending)", got, agent.SteerNonePending)
	}
	gate.release()
	evs := drainObserving(t, r, nil)

	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("terminal stop = %q (err %q)", res.Stop, res.Error)
	}
	// The retracted steer is never recorded, never echoed, never replayed.
	if userTextIn(sess.Conversation.Messages, steerText) {
		t.Fatalf("retracted steer %q was recorded — a retracted steer must drain nothing", steerText)
	}
	if echoes := steerEchoes(evs); len(echoes) != 0 {
		t.Fatalf("EvSteer echoes = %v, want none (the steer was retracted before the drain)", echoes)
	}
	if userTextIn(rec.get(1).Messages, steerText) {
		t.Fatalf("retracted steer %q reached the model on the next turn", steerText)
	}
}

// TestSteer_TooLateNotDrained (AC2.3): a steer that arrives after the run went
// terminal reports too_late and is never silently drained into a finished turn
// (nothing recorded, no echo).
func TestSteer_TooLateNotDrained(t *testing.T) {
	const steerText = "steer: arrived after the run ended"
	llm := mockllm.New(
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{
		LLM:         llm,
		Catalog:     catalogWith(t, &probeTool{}),
		EnableSteer: true,
	})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"})
	evs := drainObserving(t, r, nil)
	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("terminal stop = %q (err %q)", res.Stop, res.Error)
	}
	// The run is terminal now — a steer must report too_late, never be parked.
	if got := steerEnqueueOutcome(t, r, steerText); got != agent.SteerTooLate {
		t.Fatalf("post-terminal enqueue outcome = %q, want %q", got, agent.SteerTooLate)
	}
	// A cancel after terminal also has nothing live to retract.
	if got := steerCancelOutcome(t, r); got != agent.SteerNonePending {
		t.Fatalf("post-terminal cancel outcome = %q, want %q", got, agent.SteerNonePending)
	}
	// Nothing was drained into the finished turn.
	if userTextIn(sess.Conversation.Messages, steerText) {
		t.Fatalf("too-late steer %q was recorded into a finished run", steerText)
	}
	if echoes := steerEchoes(evs); len(echoes) != 0 {
		t.Fatalf("EvSteer echoes = %v, want none (the steer arrived too late)", echoes)
	}
}

// TestSteer_DrainEmitsCommittedEcho (AC2.4): the drain emits EvSteer carrying
// the committed text — the version actually parked and drained — and the drain
// lands at the boundary BEFORE the turn it feeds (Seq ordering: echo < that
// turn's turn.start).
func TestSteer_DrainEmitsCommittedEcho(t *testing.T) {
	const committed = "steer: the committed version"
	gate := newFirstTurnGate()
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(gate.observe)},
		mockllm.ToolCallTurn(toolCall("c1", "Probe", `{}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{
		LLM:         llm,
		Catalog:     catalogWith(t, &probeTool{}),
		EnableSteer: true,
	})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"})
	gate.awaitEntered()
	steerEnqueueOutcome(t, r, committed)
	gate.release()
	evs := drainObserving(t, r, nil)

	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("terminal stop = %q (err %q)", res.Stop, res.Error)
	}
	// Exactly one echo, carrying the committed text — the version actually
	// drained.
	var echo *session.Event
	for i := range evs {
		if evs[i].Type == session.EvSteer {
			if echo != nil {
				t.Fatalf("more than one EvSteer emitted (seqs %d and %d) — the drain commits once", echo.Seq, evs[i].Seq)
			}
			echo = &evs[i]
		}
	}
	if echo == nil || echo.Steer == nil {
		t.Fatalf("no EvSteer echo emitted on drain; types=%v", typesOf(evs))
	}
	if echo.Steer.Text != committed {
		t.Fatalf("EvSteer echo text = %q, want the committed %q (the version actually drained)", echo.Steer.Text, committed)
	}
	// The echo is sequenced BEFORE the turn it feeds (turn index 1's turn.start),
	// so a client renders the echo ahead of the model turn that consumed it.
	var turn1StartSeq int64 = -1
	for _, ev := range evs {
		if ev.Type == session.EvTurnStart && ev.Turn == 1 {
			turn1StartSeq = ev.Seq
			break
		}
	}
	if turn1StartSeq == -1 {
		t.Fatalf("no turn.start for turn 1 (the steer-fed turn); types=%v", typesOf(evs))
	}
	if echo.Seq >= turn1StartSeq {
		t.Fatalf("EvSteer echo seq %d must precede the turn.start (seq %d) of the turn it feeds", echo.Seq, turn1StartSeq)
	}
}

// TestSteer_InboxConcurrentSafe (AC2.5): enqueue/cancel/drain are safe under
// concurrent access — the run goroutine drains at boundaries while wire-handler
// goroutines enqueue and cancel. Run under -race. No data race, no double-drain
// (at most one EvSteer echo and at most one recorded steer), and every outcome
// is a defined enum value.
func TestSteer_InboxConcurrentSafe(t *testing.T) {
	// A gate tool parks mid-turn so several turn boundaries (and therefore
	// several drains) occur while the hammer goroutines race the inbox.
	gate := newBgGateTool()
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Gate", `{}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{
		LLM:         llm,
		Catalog:     catalogWith(t, gate),
		EnableSteer: true,
	})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"})
	<-gate.started // the run is genuinely mid-flight, inside dispatch

	// Hammer the inbox from several goroutines while the run is live: enqueues
	// append to the pending bundle and cancels retract it, racing the boundary
	// drain.
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				outcome, err := agent.EnqueueSteerForTest(r, "steer: racer", nil)
				if err != nil {
					t.Errorf("enqueue returned error %v — task 02 returns a SteerOutcome", err)
					return
				}
				switch outcome {
				case agent.SteerAccepted, agent.SteerAppended, agent.SteerTooLate:
					// defined live/terminal outcomes
				default:
					t.Errorf("enqueue outcome %q is not a defined SteerOutcome", outcome)
					return
				}
				if outcome == agent.SteerTooLate {
					return // the run ended; stop hammering
				}
				cOutcome, err := agent.CancelSteerForTest(r)
				if err != nil {
					t.Errorf("cancel returned error %v — task 02 returns a SteerOutcome", err)
					return
				}
				switch cOutcome {
				case agent.SteerRetracted, agent.SteerNonePending:
					// defined cancel outcomes
				default:
					t.Errorf("cancel outcome %q is not a defined SteerOutcome", cOutcome)
					return
				}
			}
		}()
	}
	// Let the racers run against the live run, then release the tool so the run
	// drains at the next boundary and finishes.
	gate.releaseOnce()
	wg.Wait()
	evs := drainObserving(t, r, nil)

	// The run terminates normally. The hammer can leave at most ONE parked steer
	// (the single slot) plus the at-most-one the pre-release boundary drained,
	// and the terminal close-drain commits whatever is parked rather than
	// closing it unconsumed (R2-6). The exact stop label is benign either way:
	// a racer steer parked at the final clean end defers it into an unscripted
	// model turn, whose empty scripted-turn sequence ends StopNoProgress — the
	// no-progress cap is exactly the mechanism that keeps a racer-steered
	// extra turn bounded. Accept either clean terminal.
	if res := lastResult(t, evs); res.Stop != session.StopEndTurn && res.Stop != session.StopNoProgress {
		t.Fatalf("terminal stop = %q (err %q), want a clean end_turn/no_progress", res.Stop, res.Error)
	}
	// No double-drain: every EvSteer echo must correspond to exactly one
	// recorded user continuation with the same committed bundle text. A bundle may
	// contain several racer fragments (append-default), and racers can park fresh
	// bundles at later boundaries, so neither an exact "steer: racer" count nor a
	// fixed number of drains is a valid concurrency invariant.
	echoes := steerEchoes(evs)
	recorded := make(map[string]int)
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleUser && strings.Contains(m.Text, "steer: racer") {
			recorded[m.Text]++
		}
	}
	for _, echo := range echoes {
		if recorded[echo] == 0 {
			t.Fatalf("EvSteer echo %q has no matching recorded steer — every committed steer echoes exactly once", echo)
		}
		recorded[echo]--
	}
	for text, n := range recorded {
		if n != 0 {
			t.Fatalf("recorded steer %q has no matching EvSteer echo (%d unmatched)", text, n)
		}
	}
	// Pairing holds no matter how many drains committed a steer.
	if err := session.ValidateToolPairing(sess.Conversation.Messages); err != nil {
		t.Fatalf("ValidateToolPairing after the raced drains: %v", err)
	}
}

// TestInvariant_recorded_streamed_model_view (AC2.6): the recorded steer, the
// streamed EvSteer echo, and the message the model sees are the SAME text — the
// recorded == streamed == model-view invariant holds.
func TestInvariant_recorded_streamed_model_view(t *testing.T) {
	const committed = "steer: the one committed instruction"
	gate := newFirstTurnGate()
	rec := gate.rec
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(gate.observe)},
		mockllm.ToolCallTurn(toolCall("c1", "Probe", `{}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{
		LLM:         llm,
		Catalog:     catalogWith(t, &probeTool{}),
		EnableSteer: true,
	})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"})
	gate.awaitEntered()
	steerEnqueueOutcome(t, r, committed)
	gate.release()
	evs := drainObserving(t, r, nil)

	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("terminal stop = %q (err %q)", res.Stop, res.Error)
	}

	// RECORDED: the committed text is a user message on durable history.
	var recordedText string
	var recordedCount int
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleUser && strings.HasPrefix(m.Text, "steer: ") {
			recordedText = m.Text
			recordedCount++
		}
	}
	if recordedCount != 1 || recordedText != committed {
		t.Fatalf("recorded steer = %q (x%d), want exactly one %q", recordedText, recordedCount, committed)
	}

	// STREAMED: the EvSteer echo carries the identical text.
	echoes := steerEchoes(evs)
	if len(echoes) != 1 || echoes[0] != committed {
		t.Fatalf("streamed EvSteer echo = %v, want exactly one %q (streamed must equal recorded)", echoes, committed)
	}

	// MODEL-VIEW: the model's next-turn request replays the identical text.
	var modelText string
	var modelCount int
	for _, m := range rec.get(1).Messages {
		if m.Role == session.RoleUser && strings.HasPrefix(m.Text, "steer: ") {
			modelText = m.Text
			modelCount++
		}
	}
	if modelCount != 1 || modelText != committed {
		t.Fatalf("model-view steer = %q (x%d), want exactly one %q (model-view must equal recorded)", modelText, modelCount, committed)
	}

	// The three surfaces agree byte-for-byte.
	if recordedText != echoes[0] || echoes[0] != modelText {
		t.Fatalf("recorded/streamed/model-view diverged: recorded=%q streamed=%q model=%q", recordedText, echoes[0], modelText)
	}
}

// ---------------------------------------------------------------------------
// Round-2 rework (the Ozz-review decisions): ingress UTF-8 repair, the
// clean-exit continue-run, and the never-closed-parked guarantee.
// ---------------------------------------------------------------------------

// TestSteer_IngressUTF8Repaired (R2-4): an operator steer carrying invalid
// UTF-8 (the BSD `cat -t`-class mangled byte sequence, issue #402's shape) is
// REPAIRED at ingress (session.ToValidUTF8 in enqueueSteer, BEFORE the text
// enters the inbox), so the recorded history, the EvSteer echo, and the
// model-visible message are all valid UTF-8 and byte-IDENTICAL — the
// recorded == streamed == model-view invariant holds for the repaired text.
func TestSteer_IngressUTF8Repaired(t *testing.T) {
	// A two-byte lead + stray continuation that is NOT valid UTF-8; the repair
	// substitutes U+FFFD exactly the way encoding/json does.
	const raw = "steer: mangled \xe2 orphan"
	repaired := session.ToValidUTF8(raw)
	if utf8.ValidString(raw) {
		t.Fatalf("test premise broken: the raw steer text is already valid UTF-8")
	}
	if !utf8.ValidString(repaired) || repaired == raw {
		t.Fatalf("test premise broken: ToValidUTF8(%q) = %q must repair and differ", raw, repaired)
	}
	gate := newFirstTurnGate()
	rec := gate.rec
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(gate.observe)},
		mockllm.ToolCallTurn(toolCall("c1", "Probe", `{}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{
		LLM:         llm,
		Catalog:     catalogWith(t, &probeTool{}),
		EnableSteer: true,
	})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"})
	gate.awaitEntered()
	if got := steerEnqueueOutcome(t, r, raw); got != agent.SteerAccepted {
		t.Fatalf("enqueue outcome = %q, want %q", got, agent.SteerAccepted)
	}
	gate.release()
	evs := drainObserving(t, r, nil)

	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("terminal stop = %q (err %q)", res.Stop, res.Error)
	}
	// RECORDED: the history carries the REPAIRED text, never the raw bytes.
	if userTextIn(sess.Conversation.Messages, raw) {
		t.Fatalf("the raw invalid-UTF-8 steer reached durable history — ingress repair must run BEFORE the inbox")
	}
	var recordedText string
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleUser && strings.HasPrefix(m.Text, "steer: ") {
			recordedText = m.Text
		}
	}
	if recordedText != repaired {
		t.Fatalf("recorded steer = %q, want the repaired %q", recordedText, repaired)
	}
	if !utf8.ValidString(recordedText) {
		t.Fatalf("recorded steer %q is not valid UTF-8", recordedText)
	}
	// STREAMED: the EvSteer echo carries the identical repaired text.
	echoes := steerEchoes(evs)
	if len(echoes) != 1 || echoes[0] != repaired {
		t.Fatalf("EvSteer echoes = %v, want exactly one %q (echo must equal recorded)", echoes, repaired)
	}
	if !utf8.ValidString(echoes[0]) {
		t.Fatalf("EvSteer echo %q is not valid UTF-8", echoes[0])
	}
	// MODEL-VIEW: the model's next-turn request replays the identical text.
	var modelText string
	for _, m := range rec.get(1).Messages {
		if m.Role == session.RoleUser && strings.HasPrefix(m.Text, "steer: ") {
			modelText = m.Text
		}
	}
	if modelText != repaired {
		t.Fatalf("model-view steer = %q, want the repaired %q (model-view must equal recorded)", modelText, repaired)
	}
	if !utf8.ValidString(modelText) {
		t.Fatalf("model-view steer %q is not valid UTF-8", modelText)
	}
}

// TestSteer_CleanExitContinuesRun (R2-5): a steer enqueued while a
// meaningful-text, no-tool-call answer is streaming is NOT dropped — the clean
// exit (terminateComplete on the benign stop) DEFERS while the steer is parked,
// the steer drains at the NEXT Step 2a boundary, and the model acts on it on
// the following turn. The run extends until the steer is consumed; the
// never-drop contract holds engine-internally.
func TestSteer_CleanExitContinuesRun(t *testing.T) {
	const steerText = "steer: act on this after the answer"
	gate := newFirstTurnGate()
	rec := gate.rec
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(gate.observe)},
		// Turn 1: a MEANINGFUL-TEXT, no-tool-call answer — the shape that used to
		// terminateComplete and silently drop the parked steer.
		mockllm.TextTurn("the first answer"),
		// Turn 2: the post-steer turn — the model acts on the drained steer.
		mockllm.TextTurn("acted on the steer"),
	)
	e := newEngine(agent.Deps{
		LLM:         llm,
		Catalog:     catalogWith(t, &probeTool{}),
		EnableSteer: true,
	})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"})
	// Enqueue while the first (text-bearing, no-tool) answer streams: turn 1's
	// drain already ran, so the steer is PARKED when finishTurnNoTools classifies
	// the clean end.
	gate.awaitEntered()
	if got := steerEnqueueOutcome(t, r, steerText); got != agent.SteerAccepted {
		t.Fatalf("enqueue outcome = %q, want %q", got, agent.SteerAccepted)
	}
	gate.release()
	evs := drainObserving(t, r, nil)

	res := lastResult(t, evs)
	if res.Stop != session.StopEndTurn {
		t.Fatalf("terminal stop = %q, want end_turn (err %q)", res.Stop, res.Error)
	}
	// The run EXTENDED: the model was called TWICE — the parked steer deferred
	// the clean exit, drained at the next boundary, and fed a second turn.
	if got := llm.Calls(); got != 2 {
		t.Fatalf("model calls = %d, want 2 (the parked steer must defer the clean exit and drive a post-steer turn)", got)
	}
	// The steer is recorded as durable history and echoed once.
	if !userTextIn(sess.Conversation.Messages, steerText) {
		t.Fatalf("steer %q not recorded: %+v", steerText, sess.Conversation.Messages)
	}
	if echoes := steerEchoes(evs); len(echoes) != 1 || echoes[0] != steerText {
		t.Fatalf("EvSteer echoes = %v, want exactly one %q (the deferred clean exit drains the steer)", echoes, steerText)
	}
	// The model ACTS on it: turn 2's request replays the steer, and the final
	// answer is the post-steer turn's text.
	if !userTextIn(rec.get(1).Messages, steerText) {
		t.Fatalf("steer %q not replayed to the model on the post-steer turn: %+v", steerText, rec.get(1).Messages)
	}
	if res.Text != "acted on the steer" {
		t.Fatalf("final answer = %q, want %q (the model acted on the drained steer)", res.Text, "acted on the steer")
	}
	// The steer lands at the boundary AFTER turn 1's recorded assistant answer,
	// never inside a pair.
	if err := session.ValidateToolPairing(sess.Conversation.Messages); err != nil {
		t.Fatalf("ValidateToolPairing after the deferred clean exit: %v", err)
	}
}

// TestSteer_NeverClosedParked (R2-6): a PARKED steer is never closed-unconsumed
// on ANY terminate path — a closeSteer that finds a parked steer drains it
// (records it as durable history) before the terminal lands, so the steer
// survives to be addressed by the NEXT run. Here the steer is parked while the
// run is mid-dispatch and the operator CANCELS: the cancelled-run terminal must
// still capture the steer (the strictest never-drop reading — cancel drops a
// parked steer only if closeSteer does).
func TestSteer_NeverClosedParked(t *testing.T) {
	const steerText = "steer: parked across the cancel"
	gate := newBgGateTool()
	llm := mockllm.New(
		mockllm.ToolCallTurn(toolCall("c1", "Gate", `{}`)),
		mockllm.TextTurn("addressed the parked steer"),
	)
	e := newEngine(agent.Deps{
		LLM:         llm,
		Catalog:     catalogWith(t, gate),
		EnableSteer: true,
	})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "go"})
	<-gate.started // the run is genuinely parked mid-dispatch

	// Park the steer, then CANCEL the run without draining events: the cancel
	// terminate path closes the inbox — it must drain the parked steer into
	// durable history first, never close it unconsumed.
	if got := steerEnqueueOutcome(t, r, steerText); got != agent.SteerAccepted {
		t.Fatalf("enqueue outcome = %q, want %q", got, agent.SteerAccepted)
	}
	r.Cancel()
	gate.releaseOnce()
	evs := drainObserving(t, r, nil)
	if res := lastResult(t, evs); res.Stop != session.StopCancelled {
		t.Fatalf("terminal stop = %q, want cancelled (err %q)", res.Stop, res.Error)
	}

	// The parked steer was NOT closed-unconsumed: it is durable history even
	// though the run was cancelled mid-dispatch.
	if !userTextIn(sess.Conversation.Messages, steerText) {
		t.Fatalf("the cancel terminate path closed a PARKED steer unconsumed — %q is not on durable history: %+v", steerText, sess.Conversation.Messages)
	}
	// The interrupted turn's orphaned Gate call is closed out by the FOLLOW-UP
	// run's Interrupt repair (the synthetic result lands AFTER the parked steer
	// — the close-drain records first, the repair appends after); pairing holds
	// on the healed history.
	if err := sess.Interrupt(); err != nil {
		t.Fatalf("Interrupt cancelled session: %v", err)
	}
	if err := session.ValidateToolPairing(sess.Conversation.Messages); err != nil {
		t.Fatalf("ValidateToolPairing after Interrupt's close-out repair: %v", err)
	}

	// And the captured steer is addressed by the NEXT run: drive a follow-up
	// that replays it.
	rec := &requestRecorder{}
	llm2 := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(rec.observe)},
		mockllm.TextTurn("follow-up done"),
	)
	e2 := newEngine(agent.Deps{
		LLM:         llm2,
		Catalog:     catalogWith(t, &probeTool{}),
		EnableSteer: true,
	})
	evs2 := drainObserving(t, e2.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "continue"}), nil)
	if res2 := lastResult(t, evs2); res2.Stop != session.StopEndTurn {
		t.Fatalf("follow-up run stop = %q (err %q)", res2.Stop, res2.Error)
	}
	if rec.count() < 1 || !userTextIn(rec.get(0).Messages, steerText) {
		t.Fatalf("the next run must replay the cancel-captured steer %q to the model: %+v", steerText, rec.get(0).Messages)
	}
}

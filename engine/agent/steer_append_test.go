package agent_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// TestSteer_AcceptedOnEmpty (R5-1): an enqueue on an EMPTY slot reports
// accepted (never appended) and parks the text as the pending bundle — it
// drains as exactly the parked text (nothing merged in).
func TestSteer_AcceptedOnEmpty(t *testing.T) {
	const only = "steer: the only instruction"
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
	if got := steerEnqueueOutcome(t, r, only); got != agent.SteerAccepted {
		t.Fatalf("enqueue on an empty slot outcome = %q, want %q (never appended)", got, agent.SteerAccepted)
	}
	gate.release()
	evs := drainObserving(t, r, nil)

	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("terminal stop = %q (err %q)", res.Stop, res.Error)
	}
	if !userTextIn(sess.Conversation.Messages, only) {
		t.Fatalf("parked steer %q not recorded: %+v", only, sess.Conversation.Messages)
	}
	echoes := steerEchoes(evs)
	if len(echoes) != 1 || echoes[0] != only {
		t.Fatalf("EvSteer echoes = %v, want exactly one %q (nothing merged in)", echoes, only)
	}
}

// TestSteer_AppendedMerges (R5-2): an enqueue on an OCCUPIED slot reports
// appended and merges into the pending bundle — old + "\n\n" + new, ONE
// merged bundle (the single slot grows by append; there is never a second
// slot).
func TestSteer_AppendedMerges(t *testing.T) {
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
	// Park turn 1 so BOTH enqueues land in the SAME pending window (the drain
	// for the next boundary has not run yet).
	gate.awaitEntered()
	if got := steerEnqueueOutcome(t, r, "steer: first line"); got != agent.SteerAccepted {
		t.Fatalf("first enqueue outcome = %q, want %q", got, agent.SteerAccepted)
	}
	if got := steerEnqueueOutcome(t, r, "steer: second line"); got != agent.SteerAppended {
		t.Fatalf("second enqueue outcome = %q, want %q (an occupied slot appends, never rejects)", got, agent.SteerAppended)
	}
	merged := "steer: first line\n\nsteer: second line"
	gate.release()
	evs := drainObserving(t, r, nil)

	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("terminal stop = %q (err %q)", res.Stop, res.Error)
	}
	// ONE merged bundle lands in history (the halves never split into separate
	// user messages).
	if !userTextIn(sess.Conversation.Messages, merged) {
		t.Fatalf("merged steer %q not recorded: %+v", merged, sess.Conversation.Messages)
	}
	if userTextIn(sess.Conversation.Messages, "steer: first line") {
		t.Fatalf("the un-merged first half was recorded — the slot must grow by append, not split")
	}
	if userTextIn(sess.Conversation.Messages, "steer: second line") {
		t.Fatalf("the un-merged second half was recorded — the slot must grow by append, not split")
	}
	// ONE echo, carrying the merged text — and it is the ONLY user-message the
	// model sees on the steer-fed turn (the invariant's model-view half).
	echoes := steerEchoes(evs)
	if len(echoes) != 1 || echoes[0] != merged {
		t.Fatalf("EvSteer echoes = %v, want exactly one %q (the merged bundle)", echoes, merged)
	}
	if !userTextIn(rec.get(1).Messages, merged) {
		t.Fatalf("merged steer %q not replayed to the model: %+v", merged, rec.get(1).Messages)
	}
}

// TestSteer_AppendedDrainMerged (R5-5): an appended bundle drains as ONE
// merged user continuation at the boundary — recordContinuation records the
// merged text exactly once, the EvSteer echo carries the SAME merged text (the
// authoritative committed version), the echo is sequenced BEFORE the turn it
// feeds, and the record is provider-legal (ValidateToolPairing holds).
func TestSteer_AppendedDrainMerged(t *testing.T) {
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
	// Three sends into the same pending window: accept + two appends — ONE
	// merged bundle of three lines.
	steerEnqueueOutcome(t, r, "steer: alpha")
	steerEnqueueOutcome(t, r, "steer: beta")
	steerEnqueueOutcome(t, r, "steer: gamma")
	merged := "steer: alpha\n\nsteer: beta\n\nsteer: gamma"
	gate.release()
	evs := drainObserving(t, r, nil)

	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("terminal stop = %q (err %q)", res.Stop, res.Error)
	}
	// The merged bundle is recorded EXACTLY ONCE as a user continuation.
	recorded := 0
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleUser && m.Text == merged {
			recorded++
		}
	}
	if recorded != 1 {
		t.Fatalf("merged bundle recorded %d times, want exactly 1 (one user continuation per drain): %+v", recorded, sess.Conversation.Messages)
	}
	// The EvSteer echo carries the SAME merged text — recorded == streamed.
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
	if echo.Steer.Text != merged {
		t.Fatalf("EvSteer echo text = %q, want the merged %q", echo.Steer.Text, merged)
	}
	// The echo is sequenced BEFORE the turn it feeds (turn 1's turn.start).
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
	// The recorded continuation keeps history provider-legal.
	if err := session.ValidateToolPairing(sess.Conversation.Messages); err != nil {
		t.Fatalf("ValidateToolPairing after the merged drain: %v", err)
	}
}

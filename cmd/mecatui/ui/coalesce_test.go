package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// newCoalesceModel builds a bare Model (no transport gate) sized and connected,
// ready to receive streamed deltas. applyAll discards the commands every Update
// returns, so the frame-cadence renderTickMsg never auto-fires here — flushes
// happen only when the test sends a renderTickMsg explicitly, which is exactly
// what makes the delta-vs-flush counting deterministic.
func newCoalesceModel(t *testing.T) Model {
	t.Helper()
	m := newTestModelFromDeps(Deps{
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001"},
		client.TurnStartMsg{Turn: 1},
	)
	m.phase = phaseRunning
	return m
}

// deltaTexts returns N streamed assistant-delta fragments that grow a markdown
// list — the live-block shape whose per-token re-render the coalescing elides.
func deltaTexts(n int) []tea.Msg {
	frags := []string{
		"Working through the task. ", "Here is the plan:\n\n",
		"1. read the file\n", "2. make the edit\n", "3. run the tests\n",
		"\nMore detail: ", "the edit ", "touches ", "a single ", "function ",
		"and ", "the ", "tests ", "should ", "stay ", "green ",
		"after ", "the ", "change ", "lands.",
	}
	msgs := make([]tea.Msg, 0, n)
	for i := 0; i < n; i++ {
		msgs = append(msgs, client.AssistantDeltaMsg{Turn: 1, Text: frags[i%len(frags)]})
	}
	return msgs
}

// TestDeltasCoalesceNoGlamourPerToken proves the core win: N streamed deltas run
// ZERO glamour renders (they only append + mark dirty), and a single frame-cadence
// flush runs exactly one (the live block re-renders once).
func TestDeltasCoalesceNoGlamourPerToken(t *testing.T) {
	const n = 20
	m := newCoalesceModel(t)
	if m.rend.mdRenders != 0 {
		t.Fatalf("precondition: expected 0 glamour renders after setup, got %d", m.rend.mdRenders)
	}

	m = applyAll(m, deltaTexts(n)...)
	if m.rend.mdRenders != 0 {
		t.Fatalf("expected 0 glamour renders across %d coalesced deltas, got %d", n, m.rend.mdRenders)
	}
	if !m.viewDirty {
		t.Fatal("expected viewDirty=true after deltas with no flush")
	}

	before := m.rend.mdRenders
	m = applyAll(m, renderTickMsg{})
	if got := m.rend.mdRenders - before; got != 1 {
		t.Fatalf("expected exactly 1 glamour render on the flush, got %d", got)
	}
}

// TestCoalescedFinalContentIdentical proves the coalescing is behaviour-preserving:
// the rendered conversation after N coalesced deltas + one flush is byte-identical
// to the old path (refreshView after EVERY delta).
func TestCoalescedFinalContentIdentical(t *testing.T) {
	const n = 20

	// Coalesced path: append all, flush once.
	coalesced := newCoalesceModel(t)
	coalesced = applyAll(coalesced, deltaTexts(n)...)
	coalesced = applyAll(coalesced, renderTickMsg{})

	// Un-coalesced (old) path: refreshView after each delta. Same model shape, same
	// deltas, but a flush between every append.
	uncoalesced := newCoalesceModel(t)
	for _, msg := range deltaTexts(n) {
		uncoalesced = applyAll(uncoalesced, msg)
		uncoalesced.refreshView()
	}

	gotConv := coalesced.rend.renderConversation(&coalesced.conv, coalesced.expandTools)
	wantConv := uncoalesced.rend.renderConversation(&uncoalesced.conv, uncoalesced.expandTools)
	if gotConv != wantConv {
		t.Errorf("renderConversation differs between coalesced and un-coalesced paths:\n got %q\nwant %q",
			stripANSIstr(gotConv), stripANSIstr(wantConv))
	}

	if got, want := coalesced.View().Content, uncoalesced.View().Content; got != want {
		t.Errorf("View().Content differs between coalesced and un-coalesced paths:\n got %q\nwant %q",
			stripANSIstr(got), stripANSIstr(want))
	}
}

// TestTailFlushedAtTurnEnd proves a turn boundary force-flushes the tail even when
// no renderTickMsg fired between the deltas and the boundary: the conversation
// rendered after TurnEndMsg contains the full streamed text.
func TestTailFlushedAtTurnEnd(t *testing.T) {
	m := newCoalesceModel(t)
	m = applyAll(m,
		client.AssistantDeltaMsg{Turn: 1, Text: "Hello "},
		client.AssistantDeltaMsg{Turn: 1, Text: "world"},
		client.TurnEndMsg{Turn: 1},
	)
	got := stripANSIstr(m.rend.renderConversation(&m.conv, m.expandTools))
	if !strings.Contains(got, "Hello world") {
		t.Errorf("turn-end flush lost the tail; want 'Hello world' in:\n%s", got)
	}
	if m.viewDirty {
		t.Error("expected viewDirty=false after the turn-end force-flush")
	}
}

// TestTailFlushedAtResult proves the run-completion path (ResultMsg → endRun)
// force-flushes the tail even with no tick between the deltas and the result.
func TestTailFlushedAtResult(t *testing.T) {
	m := newCoalesceModel(t)
	m = applyAll(m,
		client.AssistantDeltaMsg{Turn: 1, Text: "Hello "},
		client.AssistantDeltaMsg{Turn: 1, Text: "world"},
		client.ResultMsg{Stop: "end_turn"},
	)
	got := stripANSIstr(m.rend.renderConversation(&m.conv, m.expandTools))
	if !strings.Contains(got, "Hello world") {
		t.Errorf("result flush lost the tail; want 'Hello world' in:\n%s", got)
	}
	if m.viewDirty {
		t.Error("expected viewDirty=false after the result force-flush")
	}
}

// TestTailFlushedAtPermissionAsk proves the permission.ask gate force-flushes the
// pending coalesced tail — the one delta-follower boundary the other TestTailFlushedAt*
// cases don't cover. A delta marks the view dirty; the very next message is the ask
// (NO renderTickMsg in between), so without the afterEvent flush on PermissionAskMsg
// the tail would render only if an already-armed one-shot tick happened to survive
// the phase change. The invariant is "only deltas defer; every other transition
// flushes (including the gate)" — content must be current before a gate (cf. the
// toolcall-card-before-gate value).
func TestTailFlushedAtPermissionAsk(t *testing.T) {
	m := newCoalesceModel(t)
	m = applyAll(m, client.AssistantDeltaMsg{Turn: 1, Text: "pending tail before the ask"})
	if !m.viewDirty {
		t.Fatal("precondition: the delta should have left viewDirty=true with no flush")
	}

	// The ask arrives with NO renderTickMsg between it and the delta.
	m = applyAll(m, client.PermissionAskMsg{
		AskID: "ask-1", Tool: "Write", Args: `{"path":"note.txt"}`, Reason: "approval required",
	})

	if m.phase != phaseAwaitingApproval {
		t.Fatalf("expected phaseAwaitingApproval after the ask, got %d", m.phase)
	}
	if m.viewDirty {
		t.Error("expected viewDirty=false: the permission.ask boundary must force-flush the tail")
	}
	got := stripANSIstr(m.rend.renderConversation(&m.conv, m.expandTools))
	if !strings.Contains(got, "pending tail before the ask") {
		t.Errorf("permission.ask flush lost the tail; want it in:\n%s", got)
	}
	// And the modal still renders over the (now-current) conversation.
	if !strings.Contains(stripANSIstr(m.View().Content), "Permission required") {
		t.Error("permission modal should render for phaseAwaitingApproval")
	}
}

// TestTickQuiescentNoRearmAndFinalFlush guards the ticker's self-termination at
// quiescence: a delta dirties the view, the run then LEAVES phaseRunning (a terminal
// path / endRun), and a renderTickMsg fires. The handler must NOT re-arm (tickArmed
// stays false — no free-running idle ticker) AND must have flushed any final dirty
// frame (viewDirty=false, content present). A regression that re-armed forever while
// idle, or failed to flush a final dirty frame at quiescence, would otherwise pass.
func TestTickQuiescentNoRearmAndFinalFlush(t *testing.T) {
	m := newCoalesceModel(t)

	// A delta dirties the view and arms the one-shot (markDirty returns the model).
	m.conv.appendAssistant("final dirty frame")
	m, _ = m.markDirty()
	if !m.viewDirty || !m.tickArmed {
		t.Fatalf("precondition: delta should set viewDirty=true tickArmed=true, got dirty=%v armed=%v",
			m.viewDirty, m.tickArmed)
	}

	// The run leaves phaseRunning before the tick fires (e.g. endRun via a terminal
	// path). endRun itself force-flushes, so simulate the harder case the guard is
	// about: the phase is no longer running but a dirty frame is still pending when
	// the (already-armed) tick lands.
	m.phase = phaseIdle
	m.viewDirty = true // a frame is still pending at the moment the stale tick fires

	mm, cmd := m.Update(renderTickMsg{})
	m = mm.(Model)

	if m.tickArmed {
		t.Error("renderTickMsg must NOT re-arm once the run has left phaseRunning (idle ticker would spin forever)")
	}
	if cmd != nil {
		t.Error("a quiescent tick must return no command (no re-arm)")
	}
	if m.viewDirty {
		t.Error("the quiescent tick must still flush the final dirty frame (viewDirty=false)")
	}
	got := stripANSIstr(m.rend.renderConversation(&m.conv, m.expandTools))
	if !strings.Contains(got, "final dirty frame") {
		t.Errorf("final dirty frame lost at quiescence; want it in:\n%s", got)
	}
}

// TestTickArmedOneShotPerBurst proves a burst of deltas arms exactly ONE pending
// tick (tickArmed), not one per delta, and that the renderTickMsg handler disarms.
// It drives markDirty directly (Update discards the command in applyAll, so we must
// invoke the arming seam to observe tickArmed).
func TestTickArmedOneShotPerBurst(t *testing.T) {
	m := newCoalesceModel(t)
	if m.tickArmed {
		t.Fatal("precondition: tickArmed should be false before any delta")
	}

	// First delta arms the one-shot. markDirty returns the updated model (the
	// well-defined form), so reassign to observe the armed flag.
	m.conv.appendAssistant("a")
	m, _ = m.markDirty()
	if !m.tickArmed {
		t.Fatal("first delta should arm the tick")
	}

	// Subsequent deltas in the same burst must NOT arm a second tick.
	for i := 0; i < 5; i++ {
		m.conv.appendAssistant("b")
		m, _ = m.markDirty()
		if !m.tickArmed {
			t.Fatalf("delta %d cleared tickArmed unexpectedly", i)
		}
	}

	// The tick fires: handler disarms and flushes (burst settled → no re-arm).
	mm, _ := m.Update(renderTickMsg{})
	m = mm.(Model)
	if m.tickArmed {
		t.Error("renderTickMsg handler should disarm tickArmed when the burst has settled")
	}
	if m.viewDirty {
		t.Error("renderTickMsg should have flushed the dirty view")
	}
}

// TestDirtyFlagInvariant locks the dirty-flag lifecycle: a delta sets it, a flush
// clears it and bumps mdRenders, and a SECOND flush with no intervening delta is a
// no-op (clean tick does not re-render).
func TestDirtyFlagInvariant(t *testing.T) {
	m := newCoalesceModel(t)

	m = applyAll(m, client.AssistantDeltaMsg{Turn: 1, Text: "some prose to render"})
	if !m.viewDirty {
		t.Fatal("expected viewDirty=true after a delta")
	}

	m = applyAll(m, renderTickMsg{})
	if m.viewDirty {
		t.Fatal("expected viewDirty=false after the flush")
	}
	rendersAfterFirst := m.rend.mdRenders
	if rendersAfterFirst == 0 {
		t.Fatal("expected mdRenders to bump on the flush")
	}

	// A second tick with nothing dirty must not re-render.
	m = applyAll(m, renderTickMsg{})
	if m.viewDirty {
		t.Error("clean tick should leave viewDirty false")
	}
	if m.rend.mdRenders != rendersAfterFirst {
		t.Errorf("clean tick re-rendered: mdRenders %d, want %d", m.rend.mdRenders, rendersAfterFirst)
	}
}

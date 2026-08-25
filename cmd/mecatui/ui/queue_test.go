package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// newQueueModel builds a connected, idle Model wired to a fakeConv whose
// OpenConverse hands a FRESH recver per call (round-robin), so each submitPrompt —
// the manual one and any auto-drained follow-up — opens its own stream as in
// production. The recvers default to a single empty script (an immediate EOF →
// StreamClosed), which is enough for the queue tests that drive ResultMsg/Stream*
// msgs by hand. applyAll discards the per-update commands, so the SendPrompt frame
// only fires when a test runs the returned command via runBatchLeaves.
func newQueueModel(t *testing.T, recvers ...*fakeRecver) (Model, *fakeConv) {
	t.Helper()
	if len(recvers) == 0 {
		recvers = []*fakeRecver{{}}
	}
	send := &fakeSender{}
	conv := &fakeConv{recv: recvers[0], send: send, recvers: recvers}
	m := New(Deps{
		Session:     conv,
		Conv:        conv,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001"},
	)
	return m, conv
}

// startRunning drives the model into phaseRunning by submitting an initial prompt
// through the real submitPrompt path (mirrors production: a run is already
// streaming before the user can enqueue a follow-up). It runs the returned batch so
// the first SendPrompt frame fires.
func startRunning(t *testing.T, m Model, prompt string) Model {
	t.Helper()
	m = typeText(t, m, prompt)
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	runBatchLeaves(cmd)
	if m.phase != phaseRunning {
		t.Fatalf("expected phaseRunning after submit, got %d", m.phase)
	}
	return m
}

// enqueue types text and presses enter while running, returning the updated model.
func enqueue(t *testing.T, m Model, text string) Model {
	t.Helper()
	m = typeText(t, m, text)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	return mm.(Model)
}

// TestEnqueueWhileRunning: enter mid-run stages the trimmed input, clears the
// textarea, and sets the "queued (N)" status — it does NOT submit a second run.
func TestEnqueueWhileRunning(t *testing.T) {
	m, _ := newQueueModel(t)
	m = startRunning(t, m, "first")

	m = enqueue(t, m, "  second  ")
	if len(m.queued) != 1 || m.queued[0] != "second" {
		t.Fatalf("queued = %v, want [second] (trimmed)", m.queued)
	}
	if strings.TrimSpace(m.ta.Value()) != "" {
		t.Errorf("textarea should be reset after enqueue, got %q", m.ta.Value())
	}
	if m.phase != phaseRunning {
		t.Errorf("enqueue must not change phase, got %d", m.phase)
	}
	if !strings.Contains(stripANSIstr(m.statusMsg), "queued (1)") {
		t.Errorf("status = %q, want it to contain 'queued (1)'", stripANSIstr(m.statusMsg))
	}
}

// TestEnqueueEmptyRejected: enter on a blank (or whitespace-only) line mid-run is a
// no-op — nothing is staged.
func TestEnqueueEmptyRejected(t *testing.T) {
	m, _ := newQueueModel(t)
	m = startRunning(t, m, "first")

	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // blank input
	m = mm.(Model)
	if len(m.queued) != 0 {
		t.Fatalf("blank enter should stage nothing, got %v", m.queued)
	}

	m = typeText(t, m, "   ")
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if len(m.queued) != 0 {
		t.Fatalf("whitespace-only enter should stage nothing, got %v", m.queued)
	}
}

// TestEnqueueCapEnforced: at maxQueued the next enqueue is rejected with a "queue
// full" status and the input is KEPT (not reset, not staged).
func TestEnqueueCapEnforced(t *testing.T) {
	m, _ := newQueueModel(t)
	m = startRunning(t, m, "first")

	for i := 0; i < maxQueued; i++ {
		m = enqueue(t, m, "item")
	}
	if len(m.queued) != maxQueued {
		t.Fatalf("expected %d staged at cap, got %d", maxQueued, len(m.queued))
	}

	m = typeText(t, m, "overflow")
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if len(m.queued) != maxQueued {
		t.Fatalf("over-cap enqueue grew the queue to %d, want %d", len(m.queued), maxQueued)
	}
	if m.ta.Value() != "overflow" {
		t.Errorf("over-cap enqueue should KEEP the input, got %q", m.ta.Value())
	}
	if !strings.Contains(stripANSIstr(m.statusMsg), "queue full") {
		t.Errorf("status = %q, want 'queue full'", stripANSIstr(m.statusMsg))
	}
}

// TestEnqueueAllowsDuplicates: identical follow-ups are staged independently (no
// de-dup — the user may legitimately want the same prompt twice).
func TestEnqueueAllowsDuplicates(t *testing.T) {
	m, _ := newQueueModel(t)
	m = startRunning(t, m, "first")

	m = enqueue(t, m, "again")
	m = enqueue(t, m, "again")
	if len(m.queued) != 2 || m.queued[0] != "again" || m.queued[1] != "again" {
		t.Fatalf("queued = %v, want two identical 'again' entries", m.queued)
	}
}

// promptTexts extracts the text of every Prompt frame recorded by the sender.
func promptTexts(send *fakeSender) []string {
	var out []string
	for _, fr := range send.frames() {
		if p := fr.GetPrompt(); p != nil {
			out = append(out, p.GetText())
		}
	}
	return out
}

// TestDrainOneOnCompletion: a clean ResultMsg{end_turn} pops one staged item and
// submits it, producing a Prompt frame with the queued text.
func TestDrainOneOnCompletion(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")

	mm, cmd := m.Update(client.ResultMsg{Stop: "end_turn"})
	m = mm.(Model)
	runBatchLeaves(cmd)

	if len(m.queued) != 0 {
		t.Fatalf("drain should have popped the only item, queued = %v", m.queued)
	}
	if m.phase != phaseRunning {
		t.Errorf("drain should reopen a run (phaseRunning), got %d", m.phase)
	}
	got := promptTexts(conv.send)
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("prompt frames = %v, want [first second]", got)
	}
}

// TestDrainMergesMultiple: multiple staged follow-ups MERGE into ONE prompt (joined
// by queueMergeSep) on the next clean ResultMsg{end_turn}, and the queue empties in a
// SINGLE step — not one-at-a-time. This pins the merge-always decision (issue #228): a
// regression to a per-item FIFO drain would send three frames instead of two, and
// would leave the queue non-empty after the first completion.
func TestDrainMergesMultiple(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")
	m = enqueue(t, m, "third")

	mm, cmd := m.Update(client.ResultMsg{Stop: "end_turn"})
	m = mm.(Model)
	runBatchLeaves(cmd)

	// The whole queue drained in one step.
	if len(m.queued) != 0 {
		t.Fatalf("merge-drain should empty the queue in one step, got %v", m.queued)
	}
	if m.phase != phaseRunning {
		t.Errorf("merge-drain should reopen a run (phaseRunning), got %d", m.phase)
	}

	got := promptTexts(conv.send)
	want := []string{"first", "second" + queueMergeSep + "third"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("prompt frames = %v, want %v (staged items merged into one prompt)", got, want)
	}
}

// TestDrainMergePausesOnPendingMode: when a mode switch is pending, the merged queue
// is placed in the textarea and NOT submitted (queuePaused=="mode") so the mode
// applies before the user sends it. Pins that popAndSubmit preserves the pendingMode
// guard under merge-always.
func TestDrainMergePausesOnPendingMode(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")
	m = enqueue(t, m, "third")
	m.pendingMode = "plan" // a mode switch is in flight

	mm, cmd := m.Update(client.ResultMsg{Stop: "end_turn"})
	m = mm.(Model)
	runBatchLeaves(cmd)

	if m.queuePaused != "mode" {
		t.Fatalf("pending mode must pause the merged drain, queuePaused=%q", m.queuePaused)
	}
	if got := m.ta.Value(); got != "second"+queueMergeSep+"third" {
		t.Fatalf("merged text must sit in the textarea, got %q", got)
	}
	if len(m.queued) != 0 {
		t.Fatalf("merge clears the queue even when it pauses on mode, got %v", m.queued)
	}
	// No second Prompt frame — the merged text was NOT submitted.
	if got := promptTexts(conv.send); len(got) != 1 {
		t.Fatalf("pending-mode merge must not submit, frames = %v", got)
	}
}

// TestNoDrainOnError: an error stop (ResultMsg{Stop:"error"} AND StreamErrMsg) must
// PAUSE the drain — the queue is kept intact and no new prompt frame is sent.
func TestNoDrainOnError(t *testing.T) {
	t.Run("result error", func(t *testing.T) {
		m, conv := newQueueModel(t)
		m = startRunning(t, m, "first")
		m = enqueue(t, m, "second")

		mm, cmd := m.Update(client.ResultMsg{Stop: stopError, Error: "boom"})
		m = mm.(Model)
		runBatchLeaves(cmd)

		if len(m.queued) != 1 || m.queued[0] != "second" {
			t.Fatalf("error must keep the queue, got %v", m.queued)
		}
		if got := promptTexts(conv.send); len(got) != 1 {
			t.Fatalf("error must not auto-submit, prompt frames = %v", got)
		}
	})
	t.Run("stream error", func(t *testing.T) {
		m, conv := newQueueModel(t)
		m = startRunning(t, m, "first")
		m = enqueue(t, m, "second")

		mm, cmd := m.Update(client.StreamErrMsg{Err: errors.New("transport down")})
		m = mm.(Model)
		runBatchLeaves(cmd)

		if len(m.queued) != 1 || m.queued[0] != "second" {
			t.Fatalf("stream error must keep the queue, got %v", m.queued)
		}
		if got := promptTexts(conv.send); len(got) != 1 {
			t.Fatalf("stream error must not auto-submit, prompt frames = %v", got)
		}
	})
}

// TestNoDrainOnCancel: a user-cancel terminal result (Stop:"cancelled") PAUSES the
// drain — the queue is retained, no auto-submit.
func TestNoDrainOnCancel(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")

	mm, cmd := m.Update(client.ResultMsg{Stop: "cancelled"})
	m = mm.(Model)
	runBatchLeaves(cmd)

	if len(m.queued) != 1 || m.queued[0] != "second" {
		t.Fatalf("cancel must keep the queue, got %v", m.queued)
	}
	if got := promptTexts(conv.send); len(got) != 1 {
		t.Fatalf("cancel must not auto-submit, prompt frames = %v", got)
	}
}

// TestDrainOnSizeLimit: the SIZE-bound stop reasons (max_turns / max_tool_calls /
// budget) DRAIN — the model was healthy and merely ran out of per-run budget, so a queued
// follow-up ("continue") is exactly what the user lined up. This pins shouldDrain's
// widened healthy-stop set (the fix for the silent pause-on-limit that read as a
// hang): a regression narrowing it back to end_turn-only would fail here.
func TestDrainOnSizeLimit(t *testing.T) {
	for _, stop := range []string{"max_turns", "max_tool_calls", "budget"} {
		t.Run(stop, func(t *testing.T) {
			m, conv := newQueueModel(t)
			m = startRunning(t, m, "first")
			m = enqueue(t, m, "second")

			mm, cmd := m.Update(client.ResultMsg{Stop: stop})
			m = mm.(Model)
			runBatchLeaves(cmd)

			if len(m.queued) != 0 {
				t.Fatalf("%s must drain the queue, got %v", stop, m.queued)
			}
			if m.queuePaused != "" {
				t.Fatalf("%s drains, must not mark paused, got %q", stop, m.queuePaused)
			}
			if m.phase != phaseRunning {
				t.Errorf("%s drain should reopen a run (phaseRunning), got %d", stop, m.phase)
			}
			if got := promptTexts(conv.send); len(got) != 2 || got[1] != "second" {
				t.Fatalf("%s must auto-submit the staged follow-up, prompt frames = %v", stop, got)
			}
		})
	}
}

// TestPauseOnConsecutiveFailures: max_consecutive_failures is NOT a healthy stop —
// the run was failing repeatedly, so the queue PAUSES (kept, marked with the reason)
// rather than piling a follow-up onto a failing run. Pins the one limit reason that
// stays out of the drain set.
func TestPauseOnConsecutiveFailures(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")

	mm, cmd := m.Update(client.ResultMsg{Stop: "max_consecutive_failures"})
	m = mm.(Model)
	runBatchLeaves(cmd)

	if len(m.queued) != 1 || m.queued[0] != "second" {
		t.Fatalf("max_consecutive_failures must keep the queue, got %v", m.queued)
	}
	if m.queuePaused != "max_consecutive_failures" {
		t.Fatalf("must mark queue paused with the stop reason, got %q", m.queuePaused)
	}
	if got := promptTexts(conv.send); len(got) != 1 {
		t.Fatalf("must not auto-submit, prompt frames = %v", got)
	}
}

// TestQueuePreviewBounded: renderQueue's per-frame preview flatten is bounded to
// queuePreviewBound leading runes, so an enqueue-expanded multi-KB payload is
// never whitespace-scanned in full on every rendered frame while queued. Two
// behavioural pins: (1) the bounded pipeline is byte-identical to the unbounded
// one for a short (multi-line) string; (2) two huge payloads that agree on their
// first queuePreviewBound runes but differ wildly after render the SAME preview
// line — the function's output cannot depend on anything past the bound, the
// observable form of "does not scan past it". Plus a rune-correctness pin on
// runePrefix itself.
func TestQueuePreviewBounded(t *testing.T) {
	if got := runePrefix("漢字abc", 2); got != "漢字" {
		t.Fatalf("runePrefix counts bytes, not runes: %q", got)
	}
	// The length pin is the bound's mutation-killer: output equality alone cannot
	// catch a runePrefix that returns s whole, because identical preview output IS
	// the requirement — only the slice length observes the bound directly.
	huge := strings.Repeat("z", 10*queuePreviewBound)
	if got := runePrefix(huge, queuePreviewBound); len(got) != queuePreviewBound {
		t.Fatalf("runePrefix returned %d bytes of a huge payload, want the %d-rune bound", len(got), queuePreviewBound)
	}

	short := "alpha\nbeta\tgamma"
	if got, want := oneLine(runePrefix(short, queuePreviewBound)), oneLine(short); got != want {
		t.Fatalf("bounded preview differs for a short string: %q vs %q", got, want)
	}

	head := strings.Repeat("h", queuePreviewBound)
	a := head + strings.Repeat("\n\ttail-a", 4000)
	b := head + strings.Repeat(" tail-b!", 4000)
	pa := truncate(oneLine(runePrefix(a, queuePreviewBound)), queuePreviewWidth)
	pb := truncate(oneLine(runePrefix(b, queuePreviewBound)), queuePreviewWidth)
	if pa != pb {
		t.Fatalf("preview depends on content past the bound:\n%q\n%q", pa, pb)
	}
	if want := truncate(oneLine(head), queuePreviewWidth); pa != want {
		t.Errorf("bounded preview = %q, want the bounded-prefix flatten %q", pa, want)
	}
}

// TestPausedQueueRendersLoud: a paused queue must render the loud "⏸ … paused"
// header and the resume/clear hint — not the silent "⏳ N queued" — so a held queue
// never looks like a hang. (The whole reason the pause affordance exists.)
func TestPausedQueueRendersLoud(t *testing.T) {
	m, _ := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")
	mm, cmd := m.Update(client.ResultMsg{Stop: stopError, Error: "boom"})
	m = mm.(Model)
	runBatchLeaves(cmd)

	card := m.renderQueue()
	if !strings.Contains(card, "paused") || !strings.Contains(card, "enter sends") ||
		!strings.Contains(card, "↑ edit") || !strings.Contains(card, "esc clears") {
		t.Fatalf("paused queue card must say paused + show resume/edit/clear keys, got:\n%s", card)
	}
}

// TestResumePausedQueueOnEnter: enter on an EMPTY input while paused fires the head
// staged prompt manually (the resume key the paused card advertises), reopening a
// run and clearing the pause.
func TestResumePausedQueueOnEnter(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")
	mm, cmd := m.Update(client.ResultMsg{Stop: "cancelled"})
	m = mm.(Model)
	runBatchLeaves(cmd)
	if m.queuePaused == "" || m.phase != phaseIdle {
		t.Fatalf("precondition: expected paused+idle, got paused=%q phase=%d", m.queuePaused, m.phase)
	}

	mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	runBatchLeaves(cmd)

	if len(m.queued) != 0 {
		t.Fatalf("resume must pop the head, got %v", m.queued)
	}
	if m.queuePaused != "" || m.phase != phaseRunning {
		t.Fatalf("resume must clear pause + reopen a run, got paused=%q phase=%d", m.queuePaused, m.phase)
	}
	if got := promptTexts(conv.send); len(got) != 2 || got[1] != "second" {
		t.Fatalf("resume must submit the staged prompt, frames = %v", got)
	}
}

// TestEscClearsPausedQueueIdle: esc while idle+paused (empty input) drops the queue —
// the clear key the paused card advertises, mirroring the running-phase esc layering.
func TestEscClearsPausedQueueIdle(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")
	mm, cmd := m.Update(client.ResultMsg{Stop: "cancelled"})
	m = mm.(Model)
	runBatchLeaves(cmd)

	mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mm.(Model)
	runBatchLeaves(cmd)

	if len(m.queued) != 0 || m.queuePaused != "" {
		t.Fatalf("esc must clear the paused queue, got queued=%v paused=%q", m.queued, m.queuePaused)
	}
	if got := promptTexts(conv.send); len(got) != 1 {
		t.Fatalf("clearing the queue must not submit, frames = %v", got)
	}
}

// TestEscWhitespaceInputClearsQueue pins the esc whitespace boundary: a
// whitespace-only input ("   ") + esc while running with a non-empty queue must NOT
// be treated as "clear input" — both enqueuePrompt and the esc-clear-input branch
// gate on strings.TrimSpace(...) != "", so a blank-but-present input falls through
// to CLEAR THE QUEUE. A second esc (now input and queue both empty) then cancels.
func TestEscWhitespaceInputClearsQueue(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")
	m = typeText(t, m, "   ") // whitespace-only "input"

	// First esc: trimmed input is empty → falls through to clear the queue.
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if len(m.queued) != 0 {
		t.Fatalf("whitespace input + esc should clear the queue, got %v", m.queued)
	}
	if m.phase != phaseRunning {
		t.Errorf("clearing the queue must NOT cancel the run, phase=%d", m.phase)
	}
	if !strings.Contains(stripANSIstr(m.statusMsg), "queue cleared") {
		t.Errorf("status = %q, want 'queue cleared'", stripANSIstr(m.statusMsg))
	}
	for _, fr := range conv.send.frames() {
		if fr.GetCancel() != nil {
			t.Fatal("queue-clear esc must not send a Cancel frame")
		}
	}

	// Second esc: input is whitespace-only and the queue is now empty → cancel.
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	runBatchLeaves(cmd)
	var sawCancel bool
	for _, fr := range conv.send.frames() {
		if fr.GetCancel() != nil {
			sawCancel = true
		}
	}
	if !sawCancel {
		t.Error("a follow-up esc with empty queue + blank input must cancel the run")
	}
}

// TestEscClearsQueueWhenInputEmpty: with no live input but a non-empty queue, esc
// drops the queue (status "queue cleared") and does NOT cancel the run.
func TestEscClearsQueueWhenInputEmpty(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")

	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)

	if len(m.queued) != 0 {
		t.Fatalf("esc should clear the queue, got %v", m.queued)
	}
	if m.phase != phaseRunning {
		t.Errorf("esc on a non-empty queue must NOT cancel the run, phase=%d", m.phase)
	}
	if !strings.Contains(stripANSIstr(m.statusMsg), "queue cleared") {
		t.Errorf("status = %q, want 'queue cleared'", stripANSIstr(m.statusMsg))
	}
	// No Cancel frame should have been sent (only the initial Prompt).
	for _, fr := range conv.send.frames() {
		if fr.GetCancel() != nil {
			t.Error("esc-clears-queue must not send a Cancel frame")
		}
	}
}

// TestEscClearsInputBeforeQueue: with BOTH live input and a queue, esc clears the
// input first (the queue and run survive).
func TestEscClearsInputBeforeQueue(t *testing.T) {
	m, _ := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")
	m = typeText(t, m, "draft follow-up")

	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)

	if strings.TrimSpace(m.ta.Value()) != "" {
		t.Errorf("esc should clear the live input first, got %q", m.ta.Value())
	}
	if len(m.queued) != 1 {
		t.Errorf("esc should leave the queue intact when input was non-empty, got %v", m.queued)
	}
	if m.phase != phaseRunning {
		t.Errorf("esc must not cancel here, phase=%d", m.phase)
	}
}

// TestEscCancelsWhenEmptyEmpty: with no input and no queue, esc cancels the run (a
// Cancel frame is sent).
func TestEscCancelsWhenEmptyEmpty(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	runBatchLeaves(cmd)

	var sawCancel bool
	for _, fr := range conv.send.frames() {
		if fr.GetCancel() != nil {
			sawCancel = true
		}
	}
	if !sawCancel {
		t.Error("esc with empty input + empty queue must send a Cancel frame")
	}
}

// TestCtrlCDoublePressQuitsWhileRunning: while running with an empty prompt the
// first ctrl+c ARMS the quit guard (does not quit — issue #17's graceful exit), and
// the second ctrl+c then quits with a QuitMsg.
func TestCtrlCDoublePressQuitsWhileRunning(t *testing.T) {
	m, _ := newQueueModel(t)
	m = startRunning(t, m, "first")

	mm, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = mm.(Model)
	if !m.quitArmed {
		t.Fatal("first ctrl+c while running (empty input) should arm, not quit")
	}
	if isQuitCmd(cmd) {
		t.Fatal("first ctrl+c should not yield a QuitMsg")
	}

	_, cmd = m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !isQuitCmd(cmd) {
		t.Fatal("second ctrl+c while armed should quit (QuitMsg)")
	}
}

// isQuitCmd reports whether cmd resolves to a tea.QuitMsg (i.e. the program will
// exit). A nil cmd or any other message is not a quit.
//
// tea.Quit resolves its message synchronously and instantly, whereas a timer
// command (e.g. the quit guard's quitDisarmCmd, a tea.Tick) only yields its message
// after its full wall-clock window. So we resolve cmd on a goroutine and race it
// against a short bound: a quit is observed at once; anything that has not produced
// a QuitMsg within the bound is treated as "not a quit" — keeping the assertion
// clock-free (no test ever blocks on a real 3s disarm tick). This is sound because
// the ONLY command this codebase resolves to a QuitMsg is the bare tea.Quit.
func isQuitCmd(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	got := make(chan tea.Msg, 1)
	go func() { got <- cmd() }()
	select {
	case msg := <-got:
		_, ok := msg.(tea.QuitMsg)
		return ok
	case <-time.After(50 * time.Millisecond):
		return false // a slow (timer) command — by construction never the quit
	}
}

// TestBuiltinQueuedThenRunsAtDrain: a "/clear" staged mid-run is dispatched as a
// built-in at DRAIN time (phase is idle then, satisfying /clear's idle-guard) — the
// conversation empties and NO prompt frame is sent for it.
func TestBuiltinQueuedThenRunsAtDrain(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")
	// Seed some assistant content so /clear has something to wipe.
	m = applyAll(m, client.AssistantDeltaMsg{Turn: 1, Text: "some assistant prose"})
	m = enqueue(t, m, "/clear")
	if len(m.queued) != 1 || m.queued[0] != "/clear" {
		t.Fatalf("expected /clear staged, got %v", m.queued)
	}

	mm, cmd := m.Update(client.ResultMsg{Stop: "end_turn"})
	m = mm.(Model)
	ready := clearMsgFromCmd(t, cmd)
	// The queued built-in has started its create-first handoff. Reduce its actual
	// successful replacement message before checking the cleared state.
	mm, _ = m.Update(ready)
	m = mm.(Model)

	if !m.conv.isEmpty() {
		t.Error("queued /clear should have emptied the conversation at drain")
	}
	if len(m.queued) != 0 {
		t.Errorf("queue should be empty after draining /clear, got %v", m.queued)
	}
	// Only the initial "first" prompt frame — /clear is a built-in, never a Prompt.
	got := promptTexts(conv.send)
	if len(got) != 1 || got[0] != "first" {
		t.Fatalf("prompt frames = %v, want only [first] (/clear must not send a prompt)", got)
	}
}

// TestClearEmptiesQueue: /clear (via runClear → resetSession) drops any staged
// follow-ups, so they don't drain into a freshly-cleared transcript.
func TestClearEmptiesQueue(t *testing.T) {
	m, _ := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")
	m = enqueue(t, m, "third")

	// Finish the run cleanly so /clear's idle-guard passes — but the drain will
	// reopen a run for "second". Instead, exercise resetSession directly (the seam
	// /clear funnels through) to assert the queue is dropped.
	m = m.resetSession()
	if len(m.queued) != 0 {
		t.Fatalf("resetSession should drop the queue, got %v", m.queued)
	}
}

// TestDrainDoesNotBreakTick: after a drain re-enters phaseRunning, a streamed delta
// still arms EXACTLY ONE one-shot tick (the ITEM-3 coalescing is intact across a
// drain boundary). Mirrors TestTickArmedOneShotPerBurst's assertions.
func TestDrainDoesNotBreakTick(t *testing.T) {
	m, _ := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")

	mm, cmd := m.Update(client.ResultMsg{Stop: "end_turn"})
	m = mm.(Model)
	runBatchLeaves(cmd)
	if m.phase != phaseRunning {
		t.Fatalf("expected a reopened run after drain, phase=%d", m.phase)
	}
	if m.tickArmed {
		t.Fatal("precondition: tickArmed should be false right after the drain reopen")
	}

	// First delta arms the one-shot.
	m.conv.appendAssistant("a")
	m, _ = m.markDirty()
	if !m.tickArmed {
		t.Fatal("first delta after drain should arm the tick")
	}
	// Subsequent deltas in the burst must NOT arm a second.
	for i := 0; i < 5; i++ {
		m.conv.appendAssistant("b")
		m, _ = m.markDirty()
		if !m.tickArmed {
			t.Fatalf("delta %d cleared tickArmed unexpectedly", i)
		}
	}
	// The tick fires: handler disarms and flushes.
	mm2, _ := m.Update(renderTickMsg{})
	m = mm2.(Model)
	if m.tickArmed {
		t.Error("renderTickMsg should disarm tickArmed when the burst settled")
	}
	if m.viewDirty {
		t.Error("renderTickMsg should have flushed the dirty view")
	}
}

// TestNoQueueDuringApproval: enter during phaseAwaitingApproval does NOT enqueue
// (the modal owns the keyboard — locked: queueing is running-only) and the modal
// still resolves the focused choice.
func TestNoQueueDuringApproval(t *testing.T) {
	m, _ := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = applyAll(m, client.PermissionAskMsg{
		AskID: "ask-1", Tool: "Write", Args: `{"path":"note.txt"}`, Reason: "approval required",
	})
	if m.phase != phaseAwaitingApproval {
		t.Fatalf("expected phaseAwaitingApproval, got %d", m.phase)
	}

	// Typing then enter must NOT stage anything — the modal claims enter (resolves
	// the default-focused "allow"), returning to phaseRunning. The probe text avoids
	// the modal's own keys (a/y/d/n and w for always-allow) so it can't accidentally
	// resolve the modal mid-typing; the modal swallows it regardless (it owns the keyboard).
	m = typeText(t, m, "xqz")
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)

	if len(m.queued) != 0 {
		t.Fatalf("approval-phase enter must not enqueue, got %v", m.queued)
	}
	if m.phase != phaseRunning {
		t.Errorf("enter on the modal should resolve it (back to running), phase=%d", m.phase)
	}
}

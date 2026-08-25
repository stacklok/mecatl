package ui

// Tests for the client-side FIFO permission-ask queue: concurrent subagents (team
// members, parallel Subagent calls) can surface asks concurrently, each parking its
// child server-side until answered. approvalSurfaceOf(t, m).ask is always the visible head; later asks
// queue behind it (never clobber it), answering/denying/retracting the head
// advances the queue, a queued ask can be retracted in place, duplicates are
// dropped, and a run's end clears everything. The spinner re-arm (be8fa37) fires
// only when the modal actually CLOSES (awaitingApproval → running) — never when a
// queued successor takes the head.

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

const (
	askA = "subagent-a1:1:k1"
	askB = "subagent-b1:1:k2"
	askC = "subagent-c1:1:k3"
)

// queuedAskModel builds an awaiting-approval model with ask A visible and ask B
// queued behind it, its stream's sends recorded by the returned fakeSender.
func queuedAskModel(t *testing.T) (Model, *fakeSender) {
	t.Helper()
	m := approvalModel(t, pendingAsk{AskID: askA, Tool: "Bash"})
	send := &fakeSender{}
	m.stream = client.NewStream(&fakeRecver{}, send)
	m = applyAll(m, client.PermissionAskMsg{AskID: askB, Tool: "Write"})
	return m, send
}

// resumeApprovalAskIDs extracts the ask ids of all ResumeApproval frames sent.
func resumeApprovalAskIDs(send *fakeSender) []string {
	var out []string
	for _, f := range send.frames() {
		if ra := f.GetResumeApproval(); ra != nil {
			out = append(out, ra.GetAskId())
		}
	}
	return out
}

// TestAskEnqueuedWhileModalOpen: a second PermissionAskMsg while a modal is open
// ENQUEUES behind the visible head (never clobbers it), and both the footer and
// the modal title advertise the queue with a "(1 of 2)" badge.
func TestAskEnqueuedWhileModalOpen(t *testing.T) {
	m, _ := queuedAskModel(t)
	if approvalSurfaceOf(t, m).ask.AskID != askA {
		t.Fatalf("the visible ask must stay the first one, got %q", approvalSurfaceOf(t, m).ask.AskID)
	}
	if len(approvalSurfaceOf(t, m).queue) != 1 || approvalSurfaceOf(t, m).queue[0].AskID != askB {
		t.Fatalf("the second ask must queue FIFO behind the head, got %+v", approvalSurfaceOf(t, m).queue)
	}
	if m.phase != phaseAwaitingApproval {
		t.Fatalf("phase = %v, want phaseAwaitingApproval", m.phase)
	}
	if footer := stripANSIstr(m.renderFooter()); !strings.Contains(footer, "(1 of 2)") {
		t.Errorf("footer must carry the queue badge, got %q", footer)
	}
	modal := stripANSIstr(approvalSurfaceOf(t, m).renderPermissionModal(100, 24))
	if !strings.Contains(modal, "Permission required (1 of 2)") {
		t.Errorf("modal title must carry the queue badge, got %q", modal)
	}
}

// TestResolveAskAdvancesQueueNoSpinnerRearm: answering the visible ask with a
// queued successor sends the approval for the ANSWERED ask, pops the successor
// into the modal, STAYS awaitingApproval, and does NOT re-arm the spinner (the
// spinner is still off-screen under the successor modal — keep in sync with
// TestSpinnerVisibleMatchesFooterRender).
func TestResolveAskAdvancesQueueNoSpinnerRearm(t *testing.T) {
	m, send := queuedAskModel(t)
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	// containsSpinnerTick resolves the cmd's leaves, which also executes the send —
	// so the frames assertion below doubles as an exactly-once check.
	if containsSpinnerTick(cmd) {
		t.Error("advancing to a queued successor must NOT re-arm the spinner (phase stays awaitingApproval)")
	}
	if got := resumeApprovalAskIDs(send); len(got) != 1 || got[0] != askA {
		t.Fatalf("answering the head must send exactly one ResumeApproval for it, got %v", got)
	}
	if approvalSurfaceOf(t, m).ask.AskID != askB {
		t.Fatalf("the queued successor must take the head, got %q", approvalSurfaceOf(t, m).ask.AskID)
	}
	if len(approvalSurfaceOf(t, m).queue) != 0 {
		t.Fatalf("queue must be drained, got %+v", approvalSurfaceOf(t, m).queue)
	}
	if m.phase != phaseAwaitingApproval {
		t.Fatalf("phase must STAY awaitingApproval with a successor, got %v", m.phase)
	}
}

// TestResolveAskFIFOOrderTwoDeep pins the FIFO order with a ≥2-deep queue — a
// LIFO pop mutant (Q[len-1] instead of Q[0]) survives every 1-deep test: with A
// visible and B then C queued, answering must advance B (not C), then C, with the
// badge counting down and VANISHING at queue-empty (a "(1 of 1)" badge must never
// exist — the badge renders only when queued>0), and the spinner re-armed only on
// the LAST answer.
func TestResolveAskFIFOOrderTwoDeep(t *testing.T) {
	m, send := queuedAskModel(t) // A visible, B queued
	m = applyAll(m, client.PermissionAskMsg{AskID: askC, Tool: "Bash"})
	if len(approvalSurfaceOf(t, m).queue) != 2 {
		t.Fatalf("precondition: want a 2-deep queue, got %+v", approvalSurfaceOf(t, m).queue)
	}

	// Answer A: B — enqueued FIRST — must take the head; C still queued → "(1 of 2)".
	// (containsSpinnerTick resolves the cmd's leaves, which also executes the send.)
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if containsSpinnerTick(cmd) {
		t.Error("advancing to a queued successor must NOT re-arm the spinner")
	}
	if approvalSurfaceOf(t, m).ask.AskID != askB {
		t.Fatalf("FIFO violated: head = %q, want %q (B before C)", approvalSurfaceOf(t, m).ask.AskID, askB)
	}
	if footer := stripANSIstr(m.renderFooter()); !strings.Contains(footer, "(1 of 2)") {
		t.Errorf("with one ask still queued the footer badge must read (1 of 2), got %q", footer)
	}

	// Answer B: C heads, queue now EMPTY → no badge anywhere (never "(1 of 1)").
	m, cmd = pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if containsSpinnerTick(cmd) {
		t.Error("advancing to a queued successor must NOT re-arm the spinner")
	}
	if approvalSurfaceOf(t, m).ask.AskID != askC || len(approvalSurfaceOf(t, m).queue) != 0 {
		t.Fatalf("want C visible with an empty queue, got head %q queue %+v", approvalSurfaceOf(t, m).ask.AskID, approvalSurfaceOf(t, m).queue)
	}
	if footer := stripANSIstr(m.renderFooter()); strings.Contains(footer, "(1 of") {
		t.Errorf("the badge must vanish at queue-empty, footer = %q", footer)
	}
	modal := stripANSIstr(approvalSurfaceOf(t, m).renderPermissionModal(100, 24))
	if strings.Contains(modal, "(1 of") {
		t.Errorf("the modal title badge must vanish at queue-empty, got %q", modal)
	}

	// Answer C (the last): modal closes, running, spinner re-armed.
	m, cmd = pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if m.phase != phaseRunning {
		t.Fatalf("answering the last ask must return to running, got %v", m.phase)
	}
	if !containsSpinnerTick(cmd) {
		t.Error("the awaitingApproval→running transition must re-arm m.sp.Tick")
	}
	if got := resumeApprovalAskIDs(send); !slices.Equal(got, []string{askA, askB, askC}) {
		t.Fatalf("approvals must go out in FIFO order, got %v", got)
	}
}

// TestResolveLastAskRearmsSpinner: answering the LAST ask (empty queue) closes the
// modal, returns to running, and re-arms the spinner tick — the be8fa37
// regression pin, now framed explicitly as the empty-queue path.
func TestResolveLastAskRearmsSpinner(t *testing.T) {
	m := approvalModel(t, pendingAsk{AskID: askA, Tool: "Bash"})
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if m.phase != phaseRunning {
		t.Fatalf("answering the last ask must return to running, got %v", m.phase)
	}
	if m.modal != nil {
		t.Fatal("answering the last ask must close the approval surface")
	}
	if !containsSpinnerTick(cmd) {
		t.Error("the awaitingApproval→running transition must re-arm m.sp.Tick (be8fa37)")
	}
}

// TestRetractVisibleAdvancesQueue: retracting the VISIBLE ask with a queued
// successor dismisses it with a withdrawn notice and pops the successor into the
// modal — still awaitingApproval, no spinner re-arm.
func TestRetractVisibleAdvancesQueue(t *testing.T) {
	m, _ := queuedAskModel(t)
	mm, cmd := m.Update(client.PermissionRetractMsg{AskID: askA})
	m = mm.(Model)
	if approvalSurfaceOf(t, m).ask.AskID != askB {
		t.Fatalf("the queued successor must take the head, got %q", approvalSurfaceOf(t, m).ask.AskID)
	}
	if m.phase != phaseAwaitingApproval {
		t.Fatalf("phase must STAY awaitingApproval with a successor, got %v", m.phase)
	}
	if notice := lastNotice(m); !strings.Contains(notice, "withdrawn") {
		t.Errorf("retracting the visible ask must leave a withdrawn notice, got %q", notice)
	}
	if containsSpinnerTick(cmd) {
		t.Error("advancing to a queued successor must NOT re-arm the spinner")
	}
}

// TestRetractVisibleLastGoesRunning: retracting the visible ask with an EMPTY
// queue is the pre-queue path verbatim — modal closes, back to running, spinner
// re-armed.
func TestRetractVisibleLastGoesRunning(t *testing.T) {
	m := approvalModel(t, pendingAsk{AskID: askA, Tool: "Bash"})
	mm, cmd := m.Update(client.PermissionRetractMsg{AskID: askA})
	m = mm.(Model)
	if m.phase != phaseRunning {
		t.Fatalf("retracting the last ask must return to running, got %v", m.phase)
	}
	if m.modal != nil {
		t.Fatal("retracting the last ask must close the approval surface")
	}
	if !containsSpinnerTick(cmd) {
		t.Error("the awaitingApproval→running transition must re-arm m.sp.Tick")
	}
}

// TestRetractQueuedRemovesSilently: retracting a QUEUED (not-yet-visible) ask
// removes it in place — the visible modal and phase are untouched, and a notice
// explains the disappearing count badge.
func TestRetractQueuedRemovesSilently(t *testing.T) {
	m, _ := queuedAskModel(t)
	modal := m.modal
	phase := m.phase
	focused := m.ta.Focused()
	spinnerVisible := m.spinnerVisible()

	mm, cmd := m.Update(client.PermissionRetractMsg{AskID: askB})
	m = mm.(Model)

	if m.modal != modal {
		t.Fatal("retracting a queued ask must retain the visible approval modal")
	}
	if approvalSurfaceOf(t, m).ask.AskID != askA {
		t.Fatalf("retracting a queued ask must leave the visible ask, got %q", approvalSurfaceOf(t, m).ask.AskID)
	}
	if len(approvalSurfaceOf(t, m).queue) != 0 {
		t.Fatalf("the queued ask must be removed, got %+v", approvalSurfaceOf(t, m).queue)
	}
	if m.phase != phase {
		t.Fatalf("phase changed: got %v, want %v", m.phase, phase)
	}
	if m.ta.Focused() != focused {
		t.Fatal("retracting a queued ask must not change textarea focus")
	}
	if m.spinnerVisible() != spinnerVisible {
		t.Fatal("retracting a queued ask must not change spinner visibility")
	}
	if cmd != nil || containsSpinnerTick(cmd) {
		t.Fatal("approvalQueueUnchanged must return no command or spinner re-arm")
	}
	if notice := lastNotice(m); !strings.Contains(notice, "withdrawn") {
		t.Errorf("removing a queued ask must leave a notice, got %q", notice)
	}
}

// TestEndRunClearsAskQueue: a run's end — terminal ResultMsg or a clean stream
// close — must not leak asks into idle: the visible modal, the queue, and the
// answered-set are all dropped. And the NEXT run must open cleanly: a fresh ask
// after the terminal — even one whose askID was resolved in the DEAD run — opens
// the modal with no phantom badge and is not swallowed by a stale answered-set.
func TestEndRunClearsAskQueue(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  tea.Msg
	}{
		{"resultMsg", client.ResultMsg{Stop: "cancelled"}},
		{"streamClosed", client.StreamClosedMsg{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := queuedAskModel(t)
			// Retract the queued ask first so resolvedAsks is non-nil, then queue a
			// third — the terminal msg must clear visible + queue + answered-set.
			m = applyAll(m,
				client.PermissionRetractMsg{AskID: askB},
				client.PermissionAskMsg{AskID: askC, Tool: "Bash"},
			)
			if approvalSurfaceOf(t, m).resolvedAsks == nil || len(approvalSurfaceOf(t, m).queue) != 1 {
				t.Fatalf("precondition: want a non-nil answered-set and one queued ask, got %v / %+v", approvalSurfaceOf(t, m).resolvedAsks, approvalSurfaceOf(t, m).queue)
			}
			m = applyAll(m, tc.msg)
			if m.phase != phaseIdle {
				t.Fatalf("phase = %v, want phaseIdle", m.phase)
			}
			if m.modal != nil {
				t.Error("a dead run must not retain an approval surface")
			}

			// Next-run cleanliness: re-deliver an askID the DEAD run resolved (askB,
			// retracted above). A new run's askIDs carry a fresh run-serial in
			// production, but even a recycled-looking id must open cleanly — the
			// answered-set died with the run.
			m = applyAll(m, client.PermissionAskMsg{AskID: askB, Tool: "Bash"})
			if m.phase != phaseAwaitingApproval || approvalSurfaceOf(t, m).ask.AskID != askB {
				t.Fatalf("a fresh ask after run end must open the modal (not be swallowed by a stale answered-set), got phase=%v ask=%+v", m.phase, approvalSurfaceOf(t, m).ask)
			}
			if footer := stripANSIstr(m.renderFooter()); strings.Contains(footer, "(1 of") {
				t.Errorf("a fresh single ask must carry no phantom queue badge, footer = %q", footer)
			}
		})
	}
}

// TestResetSessionDropsAskQueue mirrors TestClearEmptiesQueue: resetSession (the
// seam /clear and the restart-now handoff funnel through) owns ALL session-derived
// state, so it must drop the visible ask, the FIFO queue behind it, and the
// answered-set dedupe — their askIDs correlate to runs on the OLD session.
func TestResetSessionDropsAskQueue(t *testing.T) {
	m, _ := queuedAskModel(t)
	approvalSurfaceOf(t, m).markAskResolved("subagent-old:1:k0")
	if len(approvalSurfaceOf(t, m).queue) == 0 || approvalSurfaceOf(t, m).resolvedAsks == nil {
		t.Fatalf("precondition: want a populated queue and answered-set, got %+v / %v", approvalSurfaceOf(t, m).queue, approvalSurfaceOf(t, m).resolvedAsks)
	}
	m = m.resetSession()
	if m.modal != nil {
		t.Error("resetSession must close the approval surface")
	}
}

// TestAskQueueBadgeWithLongArgsHeadAsk: a long-args Bash head ask with a queued
// successor carries the (1 of 2) badge in BOTH the modal title and the args
// view title — the wrap/cap work must not eat the badge.
func TestAskQueueBadgeWithLongArgsHeadAsk(t *testing.T) {
	m, _ := queuedAskModel(t) // A (Bash) visible, B (Write) queued
	approvalSurfaceOf(t, m).ask.Args = longBashArgs
	if footer := stripANSIstr(m.renderFooter()); !strings.Contains(footer, "(1 of 2)") {
		t.Errorf("footer must carry the queue badge, got %q", footer)
	}
	modal := stripANSIstr(m.renderBody())
	if !strings.Contains(modal, "Permission required (1 of 2)") {
		t.Errorf("the long-args modal title must carry the queue badge, got %q", modal)
	}
	// Open the args view: the badge rides its title too.
	m = openArgsView(t, m)
	view := stripANSIstr(m.renderBody())
	if !strings.Contains(view, "Ask args: Bash (1 of 2)") {
		t.Errorf("the args view title must carry the queue badge, got %q", view)
	}
}

// TestAskIDDedupe: a re-delivered PermissionAskMsg whose askID is already visible
// or queued is dropped — one visible + one queued, never duplicated.
func TestAskIDDedupe(t *testing.T) {
	m := approvalModel(t, pendingAsk{AskID: askA, Tool: "Bash"})
	m = applyAll(m,
		client.PermissionAskMsg{AskID: askA, Tool: "Bash"},
		client.PermissionAskMsg{AskID: askA, Tool: "Bash"},
		client.PermissionAskMsg{AskID: askB, Tool: "Write"},
		client.PermissionAskMsg{AskID: askB, Tool: "Write"},
	)
	if approvalSurfaceOf(t, m).ask.AskID != askA {
		t.Fatalf("visible ask = %q, want %q", approvalSurfaceOf(t, m).ask.AskID, askA)
	}
	if len(approvalSurfaceOf(t, m).queue) != 1 || approvalSurfaceOf(t, m).queue[0].AskID != askB {
		t.Fatalf("duplicates must be dropped (one queued ask), got %+v", approvalSurfaceOf(t, m).queue)
	}
}

// TestLateDuplicateOfAnsweredAskIgnored: a late re-delivery of an ALREADY-ANSWERED
// askID (same stream) must not re-open the modal — the resolvedAsks dedupe.
func TestLateDuplicateOfAnsweredAskIgnored(t *testing.T) {
	m := approvalModel(t, pendingAsk{AskID: askA, Tool: "Bash"})
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"}) // answer → running
	if m.phase != phaseRunning {
		t.Fatalf("precondition: answering must return to running, got %v", m.phase)
	}
	m = applyAll(m, client.PermissionAskMsg{AskID: askA, Tool: "Bash"})
	if m.phase != phaseRunning {
		t.Fatalf("a late duplicate of an answered ask must not re-open the modal, got %v", m.phase)
	}
	if m.modal != nil {
		t.Fatal("a late duplicate must not recreate the approval surface")
	}
}

// TestConcurrentAsksWireRoundTrip crosses the client-layer seam end-to-end:
// fakeRecver → the production Stream.ReadLoop → EventToMsg → Update, with a
// scripted stream that surfaces TWO permission.ask events back-to-back before
// either is answered, then both answered and the run completed.
//
// Determinism note: the gated-Recver TEATEST harness cannot deterministically
// order the second ask against the first approval keypress (the reader goroutine
// and program.Send race through the program loop's msg channel), so this test
// consumes the production fan-in channel DIRECTLY in the test goroutine — the
// same single-reader discipline waitCmd implements — sequencing on channel
// receives, never polling. That makes the STRONG invariant assertable: both asks
// reduced (one visible + one queued, no clobber) BEFORE the first keypress, then
// both answered with a ResumeApproval frame per askID, FIFO order.
func TestConcurrentAsksWireRoundTrip(t *testing.T) {
	script := []*mecatlv1.ConverseResponse{
		ev(&mecatlv1.Event{Type: "session.init", Seq: 1}),
		ev(&mecatlv1.Event{Type: "turn.start", Seq: 2, Turn: 1}),
		ev(&mecatlv1.Event{Type: "permission.ask", Seq: 3, Turn: 1, Ask: &mecatlv1.PermissionAsk{
			AskId: askA, Tool: "Bash", Args: `{"command":"cat a"}`, Reason: "subagent request",
		}}),
		ev(&mecatlv1.Event{Type: "permission.ask", Seq: 4, Turn: 1, Ask: &mecatlv1.PermissionAsk{
			AskId: askB, Tool: "Bash", Args: `{"command":"cat b"}`, Reason: "subagent request",
		}}),
		ev(&mecatlv1.Event{Type: "result", Seq: 5, Turn: 1, Result: &mecatlv1.Result{Stop: "end_turn", Text: "done"}}),
	}
	recv := &fakeRecver{script: script}
	send := &fakeSender{}
	st := client.NewStream(recv, send)
	ch := make(chan tea.Msg, 1)
	go st.ReadLoop(context.Background(), ch)

	m := New(Deps{Theme: aztec(), Ctx: context.Background()})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.sessionID = "sess-test-0001"
	m.stream = st
	m.phase = phaseRunning

	next := func() tea.Msg {
		t.Helper()
		select {
		case msg, ok := <-ch:
			if !ok {
				t.Fatal("stream channel closed before the script was consumed")
			}
			return msg
		case <-time.After(scaleWait(3 * time.Second)):
			t.Fatal("timed out waiting for a stream msg")
			return nil
		}
	}

	// Reduce session.init, turn.start, ask A, ask B — both asks land before any key.
	for range 4 {
		m = applyAll(m, next())
	}
	if m.phase != phaseAwaitingApproval || approvalSurfaceOf(t, m).ask.AskID != askA {
		t.Fatalf("the first wire ask must open the modal, got phase=%v ask=%+v", m.phase, approvalSurfaceOf(t, m).ask)
	}
	if len(approvalSurfaceOf(t, m).queue) != 1 || approvalSurfaceOf(t, m).queue[0].AskID != askB {
		t.Fatalf("the second wire ask must queue (not clobber), got %+v", approvalSurfaceOf(t, m).queue)
	}

	// Answer A: B takes the head. Answer B: modal closes. flattenLeafMsgs resolves
	// each cmd's leaves, executing the SendApproval so the frames record.
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	_ = flattenLeafMsgs(cmd, scaleWait(time.Second))
	if approvalSurfaceOf(t, m).ask.AskID != askB || m.phase != phaseAwaitingApproval {
		t.Fatalf("answering the first wire ask must advance to the second, got phase=%v ask=%+v", m.phase, approvalSurfaceOf(t, m).ask)
	}
	m, cmd = pressKey(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	_ = flattenLeafMsgs(cmd, scaleWait(time.Second))
	if m.phase != phaseRunning {
		t.Fatalf("answering the last wire ask must resume the run, got %v", m.phase)
	}

	// Tail: the terminal result, then the clean close (a no-op at idle), then the
	// production ReadLoop closes the channel.
	m = applyAll(m, next()) // result
	if m.phase != phaseIdle {
		t.Fatalf("the terminal result must settle the run, got %v", m.phase)
	}
	m = applyAll(m, next()) // StreamClosedMsg
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("unexpected extra stream msg after the clean close")
		}
	case <-time.After(scaleWait(3 * time.Second)):
		t.Fatal("ReadLoop did not close the fan-in channel after EOF")
	}

	if got := resumeApprovalAskIDs(send); !slices.Equal(got, []string{askA, askB}) {
		t.Fatalf("both wire asks must be answered, FIFO order, got %v", got)
	}
	if m.modal != nil {
		t.Fatal("no approval surface may survive the run end")
	}
}

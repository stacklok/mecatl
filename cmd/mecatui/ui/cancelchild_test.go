package ui

// Tests for the per-subagent cancel affordance: the f6 overlay's `x` key sends
// a CancelChild frame for the selected/focused NON-terminal lane (and only then),
// and a permission.retract from the server dismisses the approval modal iff the
// pending askID matches the VISIBLE ask (advancing the FIFO ask queue — see
// ask_queue_test.go for the queued-match and queue-advance cases).

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// subagentOverlayModel builds a connected model with one RUNNING and one DONE
// subagent lane and the f6 overlay open on the Subagents tab, its stream's
// sends recorded by the returned fakeSender.
func subagentOverlayModel(t *testing.T) (Model, *fakeSender) {
	t.Helper()
	m := newMCPModel(t, aztec(), nil)
	send := &fakeSender{}
	m.stream = client.NewStream(nil, send)
	m.phase = phaseRunning
	m = applyAll(m,
		client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "p1", ChildID: "subagent-p1", Goal: "audit auth"},
		client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "p2", ChildID: "subagent-p2", Goal: "map coverage"},
		client.SubagentMsg{Kind: client.SubagentEnd, ParentCallID: "p2", ChildID: "subagent-p2", Stop: "end_turn"},
	)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	if m.team.view == teamNone || m.agentsTab != tabSubagents {
		t.Fatalf("overlay did not open on the Subagents tab (view=%v tab=%v)", m.team.view, m.agentsTab)
	}
	return m, send
}

// cancelChildFrames extracts the child ids of all CancelChild frames sent.
func cancelChildFrames(send *fakeSender) []string {
	var out []string
	for _, f := range send.frames() {
		if cc := f.GetCancelChild(); cc != nil {
			out = append(out, cc.GetChildId())
		}
	}
	return out
}

// TestSubagentRosterCancelKeySendsFrame: x on the selected RUNNING lane sends a
// CancelChild frame carrying that lane's ChildID verbatim.
func TestSubagentRosterCancelKeySendsFrame(t *testing.T) {
	m, send := subagentOverlayModel(t)
	mm, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = mm.(Model)
	runCmd(cmd)
	if got := cancelChildFrames(send); len(got) != 1 || got[0] != "subagent-p1" {
		t.Fatalf("x on the running lane must send one CancelChild{subagent-p1}, got %v", got)
	}
	if m.team.view == teamNone {
		t.Fatalf("x must not close the overlay")
	}
}

// TestSubagentRosterCancelKeyDoneLaneNoOp: x on a DONE lane sends nothing (the key
// is only live for non-terminal lanes).
func TestSubagentRosterCancelKeyDoneLaneNoOp(t *testing.T) {
	m, send := subagentOverlayModel(t)
	// Move the selection to the second (done) lane.
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = mm.(Model)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	runCmd(cmd)
	if got := cancelChildFrames(send); len(got) != 0 {
		t.Fatalf("x on a done lane must send nothing, got %v", got)
	}
}

// TestSubagentFocusCancelKeySendsFrame: x inside the focused child pane cancels the
// focused (running) child.
func TestSubagentFocusCancelKeySendsFrame(t *testing.T) {
	m, send := subagentOverlayModel(t)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // focus the running lane
	m = mm.(Model)
	if m.subagents.view != subagentFocus || m.subagents.child != "subagent-p1" {
		t.Fatalf("enter did not focus the running lane: %+v", m.subagents)
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	runCmd(cmd)
	if got := cancelChildFrames(send); len(got) != 1 || got[0] != "subagent-p1" {
		t.Fatalf("x in the focus pane must send one CancelChild{subagent-p1}, got %v", got)
	}
}

// TestPermissionRetractDismissesMatchingModal: a retract whose AskID matches the
// pending modal dismisses it (back to running, with a notice); the run keeps
// streaming.
func TestPermissionRetractDismissesMatchingModal(t *testing.T) {
	m := approvalModel(t, pendingAsk{AskID: "subagent-p1:1:k1", Tool: "Bash", Reason: "subagent request"})
	m = applyAll(m, client.PermissionRetractMsg{AskID: "subagent-p1:1:k1"})
	if m.phase != phaseRunning {
		t.Fatalf("matching retract must dismiss the modal back to running, got phase %v", m.phase)
	}
	if m.modal != nil {
		t.Fatal("matching retract must close the approval surface")
	}
	if notice := lastNotice(m); notice == "" {
		t.Fatalf("dismissing the modal should leave a notice explaining why")
	}
}

// TestPermissionRetractNonMatchingIgnored: a retract for a DIFFERENT askID — one
// neither visible nor in the FIFO ask queue (the queue is empty here, so the
// queued-match lookup misses too) — leaves the open modal untouched (idempotent
// stale-retract handling).
func TestPermissionRetractNonMatchingIgnored(t *testing.T) {
	m := approvalModel(t, pendingAsk{AskID: "subagent-p1:1:k1", Tool: "Bash"})
	m = applyAll(m, client.PermissionRetractMsg{AskID: "subagent-OTHER:9:z9"})
	if m.phase != phaseAwaitingApproval {
		t.Fatalf("non-matching retract must not dismiss the modal, got phase %v", m.phase)
	}
	if approvalSurfaceOf(t, m).ask.AskID != "subagent-p1:1:k1" {
		t.Fatalf("non-matching retract must leave the pending ask, got %+v", approvalSurfaceOf(t, m).ask)
	}
}

// TestPermissionRetractWhileIdleIgnored: a retract with NO modal open is a no-op
// (the user already answered, or the ask resolved another way).
func TestPermissionRetractWhileIdleIgnored(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m.phase = phaseRunning
	m = applyAll(m, client.PermissionRetractMsg{AskID: "subagent-p1:1:k1"})
	if m.phase != phaseRunning || m.modal != nil {
		t.Fatalf("retract with no modal must be a no-op, got phase=%v modal=%T", m.phase, m.modal)
	}
}

package ui

// Tests for the I2 per-child cancel affordances: the f6 overlay's `x` key on
// PARALLEL-BRANCH lanes (inside a focused group) and TEAM-MEMBER lanes (roster +
// focus pane) sends a CancelChild frame carrying the lane's child id — the D16
// wire handle (Parallel.child_id / Team.member_session_id), never a derived id —
// and no-ops on terminal lanes / lanes that never learned their handle.

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// parallelOverlayModel builds a connected model with one LIVE Parallel group (one
// running + one done branch) and the f6 overlay open on the Parallel tab,
// stream sends recorded by the returned fakeSender.
func parallelOverlayModel(t *testing.T) (Model, *fakeSender) {
	t.Helper()
	m := newMCPModel(t, aztec(), nil)
	send := &fakeSender{}
	m.stream = client.NewStream(nil, send)
	m.phase = phaseRunning
	m = applyAll(m,
		client.ParallelMsg{Kind: client.ParallelStart, ParentCallID: "p1", Join: "all", BranchCount: 2},
		client.ParallelMsg{Kind: client.ParallelBranchStart, ParentCallID: "p1", BranchIndex: 0,
			ChildID: "parallel-p1-0", BranchLabel: "branch-1", Goal: "explore alpha"},
		client.ParallelMsg{Kind: client.ParallelBranchStart, ParentCallID: "p1", BranchIndex: 1,
			ChildID: "parallel-p1-1", BranchLabel: "branch-2", Goal: "explore beta"},
		client.ParallelMsg{Kind: client.ParallelBranchEnd, ParentCallID: "p1", BranchIndex: 1,
			ChildID: "parallel-p1-1", Stop: "end_turn"},
	)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	if m.team.view == teamNone || m.agentsTab != tabParallel {
		t.Fatalf("overlay did not open on the Parallel tab (view=%v tab=%v)", m.team.view, m.agentsTab)
	}
	// Focus the (single) group: branch lanes live inside the group focus view.
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.parallel.view != parallelGroupView {
		t.Fatalf("enter did not focus the group: %+v", m.parallel)
	}
	return m, send
}

// TestParallelBranchCancelKeySendsFrame: x on the selected RUNNING branch lane sends
// one CancelChild frame carrying the branch's child id (the D16 handle) verbatim.
func TestParallelBranchCancelKeySendsFrame(t *testing.T) {
	m, send := parallelOverlayModel(t)
	mm, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = mm.(Model)
	runCmd(cmd)
	if got := cancelChildFrames(send); len(got) != 1 || got[0] != "parallel-p1-0" {
		t.Fatalf("x on the running branch must send one CancelChild{parallel-p1-0}, got %v", got)
	}
	if m.team.view == teamNone || m.parallel.view != parallelGroupView {
		t.Fatalf("x must not close the overlay or leave the group focus")
	}
}

// TestParallelBranchCancelKeyDoneLaneNoOp: x on a DONE branch lane sends nothing.
func TestParallelBranchCancelKeyDoneLaneNoOp(t *testing.T) {
	m, send := parallelOverlayModel(t)
	// Move the branch selection to the second (done) branch.
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = mm.(Model)
	if m.parallel.branchCursor != 1 {
		t.Fatalf("down did not move the branch cursor: %+v", m.parallel)
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	runCmd(cmd)
	if got := cancelChildFrames(send); len(got) != 0 {
		t.Fatalf("x on a done branch must send nothing, got %v", got)
	}
}

// TestParallelBranchCancelNoChildIDNoOp: a branch lane that never learned its child
// id (older server — the D16 field absent) is not cancellable; x sends nothing.
func TestParallelBranchCancelNoChildIDNoOp(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	send := &fakeSender{}
	m.stream = client.NewStream(nil, send)
	m.phase = phaseRunning
	m = applyAll(m,
		client.ParallelMsg{Kind: client.ParallelStart, ParentCallID: "p1", Join: "all", BranchCount: 1},
		client.ParallelMsg{Kind: client.ParallelBranchStart, ParentCallID: "p1", BranchIndex: 0, BranchLabel: "branch-1"},
	)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	runCmd(cmd)
	if got := cancelChildFrames(send); len(got) != 0 {
		t.Fatalf("x on a handle-less branch must send nothing, got %v", got)
	}
}

// teamOverlayCancelModel builds a connected model with a LIVE team (lead + worker,
// the worker's lane carrying its member session id off a team.member event) and
// the f6 overlay open on the Teams tab.
func teamOverlayCancelModel(t *testing.T) (Model, *fakeSender) {
	t.Helper()
	m := newMCPModel(t, aztec(), nil)
	send := &fakeSender{}
	m.stream = client.NewStream(nil, send)
	m.phase = phaseRunning
	m.conv.addTool("t1", "Team", `{"goal":"ship the feature"}`)
	m = applyAll(m,
		client.TeamMsg{Kind: client.TeamStart, ParentCallID: "t1", TeamID: "t1", Roster: []client.TeamMemberSpec{
			{Name: "lead", Role: "coordinator", Lead: true},
			{Name: "worker", Role: "investigator"},
		}},
		client.TeamMsg{Kind: client.TeamMember, ParentCallID: "t1", TeamID: "t1", Member: "worker",
			MemberSessionID: "team-t1-worker", InnerKind: "tool.call", ToolName: "Read"},
	)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	if m.team.view != teamRoster || m.agentsTab != tabTeams {
		t.Fatalf("overlay did not open on the Teams tab (view=%v tab=%v)", m.team.view, m.agentsTab)
	}
	return m, send
}

// TestTeamRosterCancelKeySendsFrame: x on a selected member lane that learned its
// session id sends one CancelChild frame carrying it verbatim. The roster renders
// lead-first, so the second row is the worker.
func TestTeamRosterCancelKeySendsFrame(t *testing.T) {
	m, send := teamOverlayCancelModel(t)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // lead-first order: row 1 = worker
	m = mm.(Model)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	runCmd(cmd)
	if got := cancelChildFrames(send); len(got) != 1 || got[0] != "team-t1-worker" {
		t.Fatalf("x on the worker lane must send one CancelChild{team-t1-worker}, got %v", got)
	}
}

// TestTeamFocusCancelKeySendsFrame: x inside the focused member pane cancels the
// focused member by its session id.
func TestTeamFocusCancelKeySendsFrame(t *testing.T) {
	m, send := teamOverlayCancelModel(t)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // focus the worker
	m = mm.(Model)
	if m.team.view != teamFocus || m.team.member != "worker" {
		t.Fatalf("enter did not focus the worker: %+v", m.team)
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	runCmd(cmd)
	if got := cancelChildFrames(send); len(got) != 1 || got[0] != "team-t1-worker" {
		t.Fatalf("x in the focus pane must send one CancelChild{team-t1-worker}, got %v", got)
	}
}

// TestTeamCancelKeyNoSessionIDNoOp: a member lane that never learned its session id
// (no member event yet / older server) is not cancellable.
func TestTeamCancelKeyNoSessionIDNoOp(t *testing.T) {
	m, send := teamOverlayCancelModel(t)
	// Row 0 is the lead, which produced no member event — no session id learned.
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	runCmd(cmd)
	if got := cancelChildFrames(send); len(got) != 0 {
		t.Fatalf("x on a handle-less lane must send nothing, got %v", got)
	}
}

// TestTeamCancelKeyDoneTeamNoOp: once the team ENDED, x sends nothing for any lane
// (members are all terminal; the affordance is gone).
func TestTeamCancelKeyDoneTeamNoOp(t *testing.T) {
	m, send := teamOverlayCancelModel(t)
	m = applyAll(m, client.TeamMsg{Kind: client.TeamEnd, ParentCallID: "t1", TeamID: "t1", Rounds: 2, Stop: "end_turn"})
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // the worker lane (has a session id)
	m = mm.(Model)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	runCmd(cmd)
	if got := cancelChildFrames(send); len(got) != 0 {
		t.Fatalf("x on a finished team must send nothing, got %v", got)
	}
}

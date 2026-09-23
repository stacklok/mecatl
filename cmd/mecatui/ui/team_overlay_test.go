package ui

// Tests for the f6 live agent-team hierarchy OVERLAY (the /team built-in): a
// full-screen, uncapped view of the most-recent Team tool card's roster, plus a
// per-member focus pane. It is additive over the inline Team card (which stays
// capped at maxTeamLanes with a "· +K more" roll-up) — the overlay is the
// overflow home that shows the WHOLE team. It reads the live lanes already
// accumulated in the conversation (no new events / RPCs). It opens while idle OR
// mid-run (Gap B), and stays inert under a permission modal. Distinct from the
// /agents definition inventory (agents_inventory_test.go).

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// seedTeam appends a Team tool block to the model's conversation and applies the
// given team.* projection via the conversation accumulators, so the model has a
// populated team for the overlay to open over. It mirrors teamCard's build seam
// but operates on the live Model conversation.
func seedTeam(m Model, build func(c *conversation)) Model {
	m.conv.addTool("t1", "Team", `{"goal":"ship the feature"}`)
	build(&m.conv)
	m.refreshView()
	return m
}

// bigRoster builds an N-member roster (a lead + N-1 numbered members) for the
// uncapped/windowed overlay tests — deliberately larger than maxTeamLanes so the
// inline cap and the overlay's windowed behaviour diverge observably. Members
// carry a role so the roster-row role rendering is exercised at scale.
func bigRoster(n int) []client.TeamMemberSpec {
	r := []client.TeamMemberSpec{{Name: "lead", Role: "coordinator", Lead: true, Mutating: true}}
	for i := 0; i < n-1; i++ {
		r = append(r, client.TeamMemberSpec{Name: "member-" + string(rune('a'+i)), Role: "worker"})
	}
	return r
}

func TestNavigateRosterCursor(t *testing.T) {
	keys := defaultKeys()
	for _, tc := range []struct {
		name    string
		msg     tea.KeyPressMsg
		cursor  int
		total   int
		page    int
		want    int
		handled bool
	}{
		{name: "up", msg: tea.KeyPressMsg{Code: tea.KeyUp}, cursor: 3, total: 8, page: 2, want: 2, handled: true},
		{name: "down", msg: tea.KeyPressMsg{Code: tea.KeyDown}, cursor: 3, total: 8, page: 2, want: 4, handled: true},
		{name: "up clamps at first", msg: tea.KeyPressMsg{Code: tea.KeyUp}, cursor: 0, total: 8, page: 2, want: 0, handled: true},
		{name: "down clamps at last", msg: tea.KeyPressMsg{Code: tea.KeyDown}, cursor: 7, total: 8, page: 2, want: 7, handled: true},
		{name: "page up", msg: tea.KeyPressMsg{Code: tea.KeyPgUp}, cursor: 6, total: 12, page: 4, want: 2, handled: true},
		{name: "page down", msg: tea.KeyPressMsg{Code: tea.KeyPgDown}, cursor: 2, total: 12, page: 4, want: 6, handled: true},
		{name: "home", msg: tea.KeyPressMsg{Code: tea.KeyHome}, cursor: 6, total: 8, page: 2, want: 0, handled: true},
		{name: "end", msg: tea.KeyPressMsg{Code: tea.KeyEnd}, cursor: 1, total: 8, page: 2, want: 7, handled: true},
		{name: "empty roster", msg: tea.KeyPressMsg{Code: tea.KeyDown}, cursor: 4, total: 0, page: 2, want: 0, handled: true},
		{name: "unhandled", msg: tea.KeyPressMsg{Code: 'z', Text: "z"}, cursor: 4, total: 8, page: 2, want: 4, handled: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, handled := navigateRosterCursor(tc.msg, keys, tc.cursor, tc.total, tc.page)
			if got != tc.want || handled != tc.handled {
				t.Errorf("navigateRosterCursor() = (%d, %t), want (%d, %t)", got, handled, tc.want, tc.handled)
			}
		})
	}
}

// TestAgentsOpensRoster asserts f6 over a populated team opens the roster.
func TestAgentsOpensRoster(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) {
		c.setTeamStart("t1", "", roster())
		c.addTeamMember(member("scout", "tool.call", client.TeamMsg{ToolName: "Grep"}))
	})
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	if m.team.view != teamRoster {
		t.Fatalf("view = %v, want teamRoster", m.team.view)
	}
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "agents · 2 members") {
		t.Errorf("roster header missing, got %q", out)
	}
	if !strings.Contains(out, "[lead]") || !strings.Contains(out, "scout") {
		t.Errorf("roster should list lead + scout, got %q", out)
	}
	if !strings.Contains(out, "enter focus") {
		t.Errorf("footer hint missing, got %q", out)
	}
}

// TestAgentsNoTeamIsNoOp asserts f6 with no team is a no-op (overlay stays
// closed) and surfaces a caps-aware hint: with teams NOT advertised the copy
// names that, distinguishing "not enabled" from "no team yet" (Gap D).
func TestAgentsNoTeamIsNoOp(t *testing.T) {
	m := newMCPModel(t, aztec(), nil) // zero caps → Teams false
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	if m.team.view != teamNone {
		t.Fatalf("overlay opened with no team: %v", m.team.view)
	}
	if m.statusMsg != "agent teams are not enabled on this server" {
		t.Errorf("status = %q, want the not-enabled hint", m.statusMsg)
	}
}

// TestAgentsNoTeamWithTeamsEnabled asserts the empty-state hint distinguishes
// "teams enabled but none run yet" from "teams not enabled" (Gap D).
func TestAgentsNoTeamWithTeamsEnabled(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m.caps.Teams = true // teams advertised, but no team has run
	mm, _ := m.openTeam()
	m = mm.(Model)
	if m.team.view != teamNone {
		t.Fatalf("overlay opened with no team: %v", m.team.view)
	}
	if m.statusMsg != "no team has run yet" {
		t.Errorf("status = %q, want the no-team-yet hint", m.statusMsg)
	}
}

// TestAgentsOpensWhileRunning asserts the live overlay CAN open mid-run (Gap B):
// the deep view is most useful while the team streams. It still rejects the
// permission-modal phase (TestAgentsGatedWhileAwaitingApproval).
func TestAgentsOpensWhileRunning(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) {
		c.setTeamStart("t1", "", roster())
		c.addTeamMember(member("scout", "tool.call", client.TeamMsg{ToolName: "Grep"}))
	})
	m.phase = phaseRunning
	mm, _ := m.openTeam()
	if mm.(Model).team.view != teamRoster {
		t.Error("overlay did not open while running (Gap B)")
	}
}

// TestAgentsGatedWhileAwaitingApproval asserts the overlay still refuses to open
// while a permission modal owns the keyboard.
func TestAgentsGatedWhileAwaitingApproval(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) { c.setTeamStart("t1", "", roster()) })
	m.phase = phaseAwaitingApproval
	mm, _ := m.openTeam()
	if mm.(Model).team.view != teamNone {
		t.Error("overlay opened while awaiting approval")
	}
}

// TestAgentsMidRunKeysDriveOverlayNotInput is the Gap-B end-to-end: with a live
// team streaming (phaseRunning), f6 opens the overlay, and the roster nav
// keys (↑/enter/t/esc) drive the OVERLAY rather than enqueuing a follow-up or
// cancelling the run. It asserts: the overlay opens to the roster; the queue
// stays empty across the navigation (no enqueuePrompt); no cancel frame is sent
// (the run keeps streaming); and esc from the roster closes the overlay,
// returning to the running phase with the input refocused.
func TestAgentsMidRunKeysDriveOverlayNotInput(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) {
		c.setTeamStart("t1", "", roster())
		c.addTeamMember(member("scout", "tool.call", client.TeamMsg{ToolName: "Grep"}))
	})
	send := &fakeSender{}
	m.stream = client.NewStream(nil, send) // a stream whose Send records frames
	m.phase = phaseRunning

	// f6 mid-run opens the overlay (Gap B), pre-empting the textarea default.
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	if m.team.view != teamRoster {
		t.Fatalf("f6 mid-run did not open the roster: view=%v", m.team.view)
	}

	// Each nav key must be claimed by the overlay (onTeamKey) before the running
	// phase switch — it must NOT enqueue a follow-up nor send a cancel frame.
	for _, k := range []tea.KeyPressMsg{
		{Code: tea.KeyDown},  // move selection
		{Code: tea.KeyEnter}, // focus the selected member (NOT enqueue)
		{Code: tea.KeyEsc},   // focus → roster
	} {
		mm, _ = m.Update(k)
		m = mm.(Model)
		if len(m.queued) != 0 {
			t.Fatalf("overlay key %v enqueued a follow-up: queue=%v", k.Code, m.queued)
		}
		if m.phase != phaseRunning {
			t.Fatalf("overlay key %v changed the phase to %v", k.Code, m.phase)
		}
	}
	// 't' opens the task sub-view (still overlay-owned).
	mm, _ = m.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	m = mm.(Model)
	if m.team.view != teamTasks {
		t.Fatalf("'t' did not open the task sub-view mid-run: %v", m.team.view)
	}
	// 't' again → roster, esc → close.
	mm, _ = m.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	m = mm.(Model)
	if m.team.view != teamRoster {
		t.Fatalf("'t' did not toggle back to the roster mid-run: %v", m.team.view)
	}
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mm.(Model)
	if m.team.view != teamNone {
		t.Fatalf("esc from roster did not close the overlay mid-run: %v", m.team.view)
	}
	if m.phase != phaseRunning {
		t.Fatalf("closing the overlay changed the phase to %v, want phaseRunning", m.phase)
	}
	// No cancel frame must have been sent across the whole interaction.
	for _, f := range send.frames() {
		if f.GetCancel() != nil {
			t.Fatalf("a cancel frame was sent while driving the mid-run overlay: %+v", f)
		}
	}
}

// TestTeamOverlaySanitizesMemberContent locks the terminaltext.Sanitize wrappers on
// the LIVE /team overlay: member content is CLAUDE.md-trust-class (it flows from
// TeamMsg, which a member model emits), so a member NAME or tool Detail carrying
// an escape sequence must render inert (no raw 0x1b) in BOTH the roster and the
// per-member focus pane. Per the repo's no-destructive-test-literals rule the
// literal is an innocuous ANSI/OSC escape, never a destructive-looking command.
func TestTeamOverlaySanitizesMemberContent(t *testing.T) {
	const evilName = "\x1b]0;pwned\x07scout"
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) {
		c.setTeamStart("t1", "", []client.TeamMemberSpec{
			{Name: "lead", Role: "coordinator", Lead: true, Mutating: true},
			{Name: evilName, Role: "\x1b[31mresearcher\x1b[0m"},
		})
		c.addTeamMember(member(evilName, "tool.call", client.TeamMsg{
			ToolName: "Grep",
			Detail:   "\x1b[31mpattern: handleErr\x1b[0m",
		}))
	})

	// Roster view: the member name + role are sanitized.
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	if m.team.view != teamRoster {
		t.Fatalf("view = %v, want teamRoster", m.team.view)
	}
	roster := stripANSIstr(m.View().Content)
	if strings.ContainsRune(roster, 0x1b) {
		t.Errorf("raw ESC (0x1b) leaked into the roster overlay; terminaltext.Sanitize not applied:\n%q", roster)
	}
	if !strings.Contains(roster, "]0;pwnedscout") {
		t.Errorf("sanitized member name not rendered as inert text in the roster:\n%q", roster)
	}

	// Focus pane: the member's lane sub-header + tool-call Detail are sanitized.
	m.team.view = teamFocus
	m.team.member = evilName
	focus := stripANSIstr(m.View().Content)
	if strings.ContainsRune(focus, 0x1b) {
		t.Errorf("raw ESC (0x1b) leaked into the focus pane; terminaltext.Sanitize not applied:\n%q", focus)
	}
	if !strings.Contains(focus, "]0;pwnedscout") {
		t.Errorf("sanitized member name not rendered as inert text in the focus pane:\n%q", focus)
	}
	if !strings.Contains(focus, "pattern: handleErr") {
		t.Errorf("sanitized tool Detail not rendered as inert text in the focus pane:\n%q", focus)
	}
}

// TestAgentsMidRunNoTeamNoCapsIsNoOp guards the onRunningKey Agents-branch
// ordering: f6 pressed MID-RUN with teams NOT enabled and no live team must
// be a clean no-op — it does NOT open the overlay, does NOT enqueue a follow-up,
// does NOT cancel the run, and the key is NOT swallowed into the textarea (no
// stray 'a'/control rune leaks into the input). The Agents case sits BEFORE
// Cancel/Submit/default in onRunningKey, so a regression that reordered it (or
// dropped it) would let the keypress fall through to the textarea default.
func TestAgentsMidRunNoTeamNoCapsIsNoOp(t *testing.T) {
	m := newMCPModel(t, aztec(), nil) // zero caps → Teams false
	send := &fakeSender{}
	m.stream = client.NewStream(nil, send)
	m.phase = phaseRunning
	before := m.prompt.Value()

	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)

	if m.team.view != teamNone {
		t.Fatalf("f6 mid-run with no team opened the overlay: view=%v", m.team.view)
	}
	if len(m.queued) != 0 {
		t.Errorf("f6 mid-run no-op enqueued a follow-up: queue=%v", m.queued)
	}
	if m.phase != phaseRunning {
		t.Errorf("f6 mid-run no-op changed the phase to %v", m.phase)
	}
	if m.prompt.Value() != before {
		t.Errorf("f6 leaked into the textarea: %q (was %q)", m.prompt.Value(), before)
	}
	for _, f := range send.frames() {
		if f.GetCancel() != nil {
			t.Fatalf("f6 mid-run no-op sent a cancel frame: %+v", f)
		}
	}
}

// TestAgentsSelectionAndFocus asserts ↑/↓ move the selection, enter focuses the
// selected member (rendering its trace), esc returns to the roster, and esc again
// closes the overlay.
func TestAgentsSelectionAndFocus(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) {
		c.setTeamStart("t1", "", roster())
		c.addTeamMember(member("scout", "message.delta", client.TeamMsg{Text: "searching the codebase"}))
		c.addTeamMember(member("scout", "tool.call", client.TeamMsg{ToolName: "Grep", Detail: "pattern: handleErr"}))
		c.addTeamMember(member("scout", "tool.result", client.TeamMsg{ToolName: "Grep", Detail: "3 matches"}))
	})
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)

	// Lead sorts first (cursor 0). Down → cursor 1 (scout).
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = mm.(Model)
	if m.team.cursor != 1 {
		t.Fatalf("cursor = %d after down, want 1", m.team.cursor)
	}

	// Enter → focus scout.
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.team.view != teamFocus || m.team.member != "scout" {
		t.Fatalf("focus state = %v/%q, want focus/scout", m.team.view, m.team.member)
	}
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "agent · scout") {
		t.Errorf("focus header missing, got %q", out)
	}
	if !strings.Contains(out, "searching the codebase") || !strings.Contains(out, "Grep") {
		t.Errorf("focus pane should show the member's trace, got %q", out)
	}
	if !strings.Contains(out, "3 matches") {
		t.Errorf("focus pane should show the bounded Detail preview, got %q", out)
	}

	// esc → back to roster.
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mm.(Model)
	if m.team.view != teamRoster || m.team.member != "" {
		t.Fatalf("esc from focus = %v/%q, want roster/empty", m.team.view, m.team.member)
	}

	// esc → closed.
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mm.(Model)
	if m.team.view != teamNone {
		t.Fatalf("esc from roster did not close: %v", m.team.view)
	}
}

// TestAgentsRosterUncapped asserts the overlay shows EVERY member (uncapped),
// even well beyond the inline maxTeamLanes cap — it is the overflow home the
// inline card defers to. A 9-member roster shows all 9 lanes (no "+K more").
func TestAgentsRosterUncapped(t *testing.T) {
	const n = 9
	if n <= maxTeamLanes {
		t.Fatalf("test premise broken: n=%d must exceed maxTeamLanes=%d", n, maxTeamLanes)
	}
	big := bigRoster(n)
	m := newMCPModel(t, aztec(), nil)
	// Give the overlay enough vertical room for all n lanes: it windows to the body
	// height (terminal minus chrome — header/footer/input + the input top-pad row), so
	// size up generously rather than depend on the exact chrome height.
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 80})
	m = seedTeam(m, func(c *conversation) { c.setTeamStart("t1", "", big) })
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	out := stripANSIstr(m.View().Content)

	// Every member name is present.
	for _, mem := range big {
		if !strings.Contains(out, mem.Name) {
			t.Errorf("member %q missing from uncapped overlay, got %q", mem.Name, out)
		}
	}
	// Count lane glyph lines — all n must render (no inline-style roll-up).
	lanes := 0
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "◆") || strings.Contains(ln, "○") {
			lanes++
		}
	}
	if lanes != n {
		t.Errorf("overlay rendered %d lanes, want all %d (uncapped)", lanes, n)
	}
	if strings.Contains(out, "more") {
		t.Errorf("overlay should not roll up overflow (it is uncapped), got %q", out)
	}
	if !strings.Contains(out, "agents · 9 members") {
		t.Errorf("header should report 9 members, got %q", out)
	}
}

// TestAgentsRosterLeadFirst asserts the roster anchors the lead at the top even
// when the server sent it last, reusing teamLaneOrder.
func TestAgentsRosterLeadFirst(t *testing.T) {
	leadLast := []client.TeamMemberSpec{
		{Name: "scout"},
		{Name: "builder", Mutating: true},
		{Name: "lead", Lead: true, Mutating: true},
	}
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) { c.setTeamStart("t1", "", leadLast) })
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	out := stripANSIstr(m.View().Content)
	var laneLines []string
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "◆") || strings.Contains(ln, "○") {
			laneLines = append(laneLines, ln)
		}
	}
	if len(laneLines) < 1 || !strings.Contains(laneLines[0], "lead") {
		t.Errorf("lead lane should render first in the overlay, got %q", laneLines)
	}
}

// TestAgentsShowsLatestTeam asserts that with two Team cards in the conversation,
// the overlay shows the MOST-RECENT one (latestTeamBlock scans from the end).
func TestAgentsShowsLatestTeam(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m.conv.addTool("ta", "Team", `{}`)
	m.conv.setTeamStart("ta", "", []client.TeamMemberSpec{{Name: "alpha", Lead: true}})
	m.conv.addTool("tb", "Team", `{}`)
	m.conv.setTeamStart("tb", "", []client.TeamMemberSpec{{Name: "bravo", Lead: true}})
	m.refreshView()

	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "bravo") {
		t.Errorf("overlay should show the latest team (bravo), got %q", out)
	}
	if strings.Contains(out, "alpha") {
		t.Errorf("overlay should NOT show the earlier team (alpha), got %q", out)
	}
}

// TestAgentsResolvedSubhead asserts the roster sub-header shows the round count +
// stop reason once the team has ended.
func TestAgentsResolvedSubhead(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) {
		c.setTeamStart("t1", "", roster())
		c.setTeamEnd("t1", "", 4, "end_turn", client.Usage{InputTokens: 5200, OutputTokens: 410}, nil)
	})
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "4 rounds") || !strings.Contains(out, "stop:done") {
		t.Errorf("resolved roster sub-header missing rounds/stop, got %q", out)
	}
	if !strings.Contains(out, "↑5.2K") {
		t.Errorf("resolved sub-header should show summed usage, got %q", out)
	}
	// Also assert the lane ROWS render terminal once the team has ended: a member row
	// must carry the ✓ glyph and the "done" label. This only happens when the overlay
	// correctly threads b.teamDone into teamRosterLine → teamLaneLine; a wrong
	// (false) teamDone arg would render "◆ … working…" and this fails. (The sub-header
	// is block-derived and would pass regardless, so it cannot cover the row threading.)
	if !strings.Contains(out, "✓") || !strings.Contains(out, "done") {
		t.Errorf("ended team's lane rows must render terminal (✓ / \"done\"), got %q", out)
	}
}

// TestAgentsRosterRetriedDisposition is the LAST HOP of the ErrorRounds honesty chain
// (issue #318). Every earlier hop is pinned end-to-end — supervisor → MemberOutcome →
// EvTeamEnd.Dispositions through the real Team tool → proto → client.TeamMemberDisposition
// → teamLaneState — but the assignment that copies the count onto the lane
// (setTeamEnd: `ln.errorRounds = d.ErrorRounds`) had no oracle at all: team_test.go
// constructs a &teamLane{errorRounds: 1} DIRECTLY, and no client.TeamMemberDisposition
// literal in this package ever set the field. Delete that assignment and the whole suite
// stayed green while the overlay rendered a bare "✓ done" for a member the supervisor
// reports as retried — the exact disposition lie the feature exists to prevent.
//
// It drives a real client.TeamMsg through m.Update (not setTeamEnd directly), so the
// msgs → update → setTeamEnd hop is covered too.
func TestAgentsRosterRetriedDisposition(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) { c.setTeamStart("t1", "", roster()) })
	mm, _ := m.Update(client.TeamMsg{
		Kind: client.TeamEnd, ParentCallID: "t1", TeamID: "t1", Rounds: 4, Stop: "end_turn",
		Dispositions: []client.TeamMemberDisposition{
			{Name: "lead"},
			// Not stopped, but it lost a round to a run-level failure and was retried.
			{Name: "scout", ErrorRounds: 1},
		},
	})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	out := stripANSIstr(m.View().Content)

	if !strings.Contains(out, "done (retried)") {
		t.Errorf("a retried member's lane must render \"done (retried)\", got %q", out)
	}
	// The clean member must NOT pick up the label — otherwise the assertion above would
	// pass on a build that labelled every lane retried.
	if n := strings.Count(out, "done (retried)"); n != 1 {
		t.Errorf("exactly ONE lane may read \"done (retried)\", got %d occurrences: %q", n, out)
	}
	// A retried member is not stopped: the roster must not bench it.
	if strings.Contains(out, "stopped") {
		t.Errorf("a retried member finished — the overlay must not report it stopped, got %q", out)
	}

	// A member benched AT the cap keeps the more specific stopped label even though its
	// count is non-zero: the stopped arm must win, or a benched member reads as recovered.
	mb := newMCPModel(t, aztec(), nil)
	mb = seedTeam(mb, func(c *conversation) { c.setTeamStart("t1", "", roster()) })
	mmb, _ := mb.Update(client.TeamMsg{
		Kind: client.TeamEnd, ParentCallID: "t1", TeamID: "t1", Rounds: 2, Stop: "end_turn",
		Dispositions: []client.TeamMemberDisposition{
			{Name: "lead"},
			{Name: "scout", Stopped: true, Reason: "error", ErrorRounds: 2},
		},
	})
	mb = mmb.(Model)
	mmb, _ = mb.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	mb = mmb.(Model)
	benched := stripANSIstr(mb.View().Content)
	if !strings.Contains(benched, "stopped — error") {
		t.Errorf("a member benched at the retry cap must render \"stopped — error\", got %q", benched)
	}
	if strings.Contains(benched, "done (retried)") {
		t.Errorf("a benched member must not read as one that recovered, got %q", benched)
	}
}

// TestAgentsRosterStoppedGolden locks the roster overlay for an ENDED team carrying a
// per-member terminal disposition snapshot: a budget-stopped member must render
// "✗ stopped — budget", clean members must render "✓ done", and the sub-header must
// carry the "N stopped" count. This is the regression guard for the "overlay no longer
// contradicts the supervisor" fix: without the disposition snapshot every lane flipped
// to ✓ done. The companion sub-test renders the SAME team with all-done dispositions
// and asserts the two frames DIFFER (the stopped frame shows ✗ / "stopped"; the done
// frame does not).
func TestAgentsRosterStoppedGolden(t *testing.T) {
	stopped := []client.TeamMemberDisposition{
		{Name: "lead"},
		{Name: "scout", Stopped: true, Reason: "budget"},
	}
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) {
		c.setTeamStart("t1", "", roster())
		c.setTeamEnd("t1", "", 4, "end_turn", client.Usage{InputTokens: 5200, OutputTokens: 410}, stopped)
	})
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	got := stripANSI([]byte(m.View().Content))
	assertFitsViewport(t, got, m.width)
	compareGolden(t, "team_roster_stopped.golden", got)

	out := string(got)
	if !strings.Contains(out, "✗") || !strings.Contains(out, "stopped — budget") {
		t.Errorf("stopped lane must render \"✗ stopped — budget\", got %q", out)
	}
	if !strings.Contains(out, "✓") || !strings.Contains(out, "done") {
		t.Errorf("clean lane must still render \"✓ done\", got %q", out)
	}
	if !strings.Contains(out, "1 stopped") {
		t.Errorf("roster sub-header must carry the \"1 stopped\" count, got %q", out)
	}

	// The SAME team rendered with all-done dispositions must NOT show ✗ / "stopped":
	// this is the contradiction the fix removes.
	allDone := []client.TeamMemberDisposition{{Name: "lead"}, {Name: "scout"}}
	md := newMCPModel(t, aztec(), nil)
	md = seedTeam(md, func(c *conversation) {
		c.setTeamStart("t1", "", roster())
		c.setTeamEnd("t1", "", 4, "end_turn", client.Usage{InputTokens: 5200, OutputTokens: 410}, allDone)
	})
	mmd, _ := md.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	md = mmd.(Model)
	doneOut := stripANSIstr(md.View().Content)
	if strings.Contains(doneOut, "✗") || strings.Contains(doneOut, "stopped") {
		t.Errorf("all-done team must NOT render ✗ / stopped, got %q", doneOut)
	}
	if doneOut == out {
		t.Errorf("stopped render must DIFFER from the all-done render")
	}
}

// TestTeamStopReasonLabelAndLaneState covers every stop-reason render path (the golden
// only exercised "budget"): each known reason maps to its calm label and teamLaneState
// renders "stopped — <reason>", while an empty/unknown reason falls back to a bare
// "stopped" (the render.go default branch). teamLaneState is only asserted at terminal
// (teamDone), where the stopped state is honoured.
func TestTeamStopReasonLabelAndLaneState(t *testing.T) {
	cases := []struct {
		reason    string
		wantLabel string
		wantState string
	}{
		{"error", "error", "stopped — error"},
		{"cancelled", "cancelled", "stopped — cancelled"},
		{"budget", "budget", "stopped — budget"},
		{"", "", "stopped"},      // a done member's empty reason → bare "stopped"
		{"weird", "", "stopped"}, // an unknown/future reason → bare "stopped"
	}
	for _, tc := range cases {
		t.Run("reason="+tc.reason, func(t *testing.T) {
			if got := teamStopReasonLabel(tc.reason); got != tc.wantLabel {
				t.Errorf("teamStopReasonLabel(%q) = %q, want %q", tc.reason, got, tc.wantLabel)
			}
			ln := &teamLane{name: "m", stopped: true, stopReason: tc.reason}
			if got := teamLaneState(ln, true); got != tc.wantState {
				t.Errorf("teamLaneState(stopped, reason=%q) = %q, want %q", tc.reason, got, tc.wantState)
			}
		})
	}
}

// TestTeamCardStoppedCountInline asserts the calm inline Team card resolved line gains
// the "N stopped" tell when a member stopped (the per-member glyph lives in the modal
// overlay, so the inline card needs the count tell).
func TestTeamCardStoppedCountInline(t *testing.T) {
	r := newTestRenderer()
	c := &conversation{}
	c.addTool("t1", "Team", `{"goal":"ship the feature"}`)
	c.setTeamStart("t1", "", roster())
	c.setTeamEnd("t1", "", 4, "end_turn", client.Usage{InputTokens: 5200, OutputTokens: 410},
		[]client.TeamMemberDisposition{{Name: "lead"}, {Name: "scout", Stopped: true, Reason: "budget"}})
	out := stripANSIstr(r.renderBlock(0, &c.blocks[0], false))
	if !strings.Contains(out, "1 stopped") {
		t.Errorf("inline resolved Team line must show \"1 stopped\", got %q", out)
	}
}

// TestTeamStoppedCountMultiple asserts the "N stopped" tell sums correctly at N>=2 (the
// other count tests only cover N=1): two stopped members + one clean → "2 stopped" on
// both the inline resolved Team line and the f6 roster sub-header. This exercises
// teamStoppedCount summing across lanes (not just a boolean tell).
func TestTeamStoppedCountMultiple(t *testing.T) {
	threeRoster := []client.TeamMemberSpec{
		{Name: "lead", Role: "coordinator", Lead: true, Mutating: true},
		{Name: "scout", Role: "researcher"},
		{Name: "fixer", Role: "patcher"},
	}
	disps := []client.TeamMemberDisposition{
		{Name: "lead"}, // clean / done
		{Name: "scout", Stopped: true, Reason: "budget"},
		{Name: "fixer", Stopped: true, Reason: "error"},
	}

	// Inline resolved Team card line.
	r := newTestRenderer()
	c := &conversation{}
	c.addTool("t1", "Team", `{"goal":"ship the feature"}`)
	c.setTeamStart("t1", "", threeRoster)
	c.setTeamEnd("t1", "", 4, "end_turn", client.Usage{InputTokens: 5200, OutputTokens: 410}, disps)
	inline := stripANSIstr(r.renderBlock(0, &c.blocks[0], false))
	if !strings.Contains(inline, "2 stopped") {
		t.Errorf("inline resolved Team line must show \"2 stopped\", got %q", inline)
	}

	// f6 roster sub-header.
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(cv *conversation) {
		cv.setTeamStart("t1", "", threeRoster)
		cv.setTeamEnd("t1", "", 4, "end_turn", client.Usage{InputTokens: 5200, OutputTokens: 410}, disps)
	})
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	overlay := stripANSIstr(m.View().Content)
	if !strings.Contains(overlay, "2 stopped") {
		t.Errorf("roster sub-header must show \"2 stopped\", got %q", overlay)
	}
}

// resize sends a WindowSizeMsg so a test can pick the overlay height (the window
// capacity derives from vp.Height() = h - 8). Returns the resized model.
func resize(m Model, w, h int) Model {
	mm, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return mm.(Model)
}

// countLanes counts roster lane lines (rows carrying a state glyph) in a stripped
// overlay frame.
func countLanes(out string) int {
	n := 0
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "◆") || strings.Contains(ln, "○") {
			n++
		}
	}
	return n
}

// TestAgentsRosterWindowed asserts a roster larger than the available height is
// WINDOWED: only as many rows as fit render, the footer hint stays visible, and
// the hidden rows are surfaced via the "+K below"/"+K above" tails. This is the
// height-safety property the inline card has and the uncapped overlay was missing.
func TestAgentsRosterWindowed(t *testing.T) {
	const n = 20
	big := bigRoster(n)
	m := newMCPModel(t, aztec(), nil)
	m = resize(m, 100, 24) // vp height 16 → ~6 lane rows
	m = seedTeam(m, func(c *conversation) { c.setTeamStart("t1", "", big) })
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	out := stripANSIstr(m.View().Content)

	th, hk, width, height := m.agentsListGeometry()
	rows := agentsListPageSize(th, height, teamSelectableList(th, m.team, m.conv.latestTeamBlock(), hk, width))
	if rows >= n {
		t.Fatalf("test premise broken: window %d must be smaller than roster %d", rows, n)
	}
	if got := countLanes(out); got != rows {
		t.Errorf("windowed roster rendered %d lanes, want exactly the window (%d)", got, rows)
	}
	// Cursor at the top → there is more below, nothing above.
	if !strings.Contains(out, "below") {
		t.Errorf("a windowed roster with the cursor at the top should show a '+K below' tail, got %q", out)
	}
	if strings.Contains(out, "above") {
		t.Errorf("cursor at the top should NOT show a '+K above' tail, got %q", out)
	}
	// The footer hint is ALWAYS visible (never clipped by the window/tails).
	if !strings.Contains(out, "enter focus") {
		t.Errorf("footer hint clipped by the window, got %q", out)
	}
}

// TestAgentsWindowFollowsCursor asserts the window scrolls to keep the selected
// row visible: jumping to the end shows the last member and an "+K above" tail,
// and the selected row (the last member) is in-window.
func TestAgentsWindowFollowsCursor(t *testing.T) {
	const n = 20
	big := bigRoster(n)
	m := newMCPModel(t, aztec(), nil)
	m = resize(m, 100, 24)
	m = seedTeam(m, func(c *conversation) { c.setTeamStart("t1", "", big) })
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)

	// end/G jumps to the last member.
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	m = mm.(Model)
	if m.team.cursor != n-1 {
		t.Fatalf("end did not jump to last: cursor=%d want %d", m.team.cursor, n-1)
	}
	out := stripANSIstr(m.View().Content)
	// The last member is the alphabetically-last numbered one ("member-s" for n=20:
	// lead + members a..s).
	last := "member-" + string(rune('a'+(n-2)))
	if !strings.Contains(out, last) {
		t.Errorf("last member %q not in window after jump-to-end, got %q", last, out)
	}
	if !strings.Contains(out, "above") {
		t.Errorf("jump-to-end should show a '+K above' tail, got %q", out)
	}
	if strings.Contains(out, "below") {
		t.Errorf("jump-to-end should NOT show a '+K below' tail, got %q", out)
	}
	// The selected (highlighted) row must be the last member — find the › row.
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "›") && !strings.Contains(ln, last) {
			t.Errorf("selected row is not the last member: %q", ln)
		}
	}

	// home/g jumps back to the first.
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	m = mm.(Model)
	if m.team.cursor != 0 {
		t.Fatalf("home did not jump to first: cursor=%d", m.team.cursor)
	}
}

// TestAgentsPageKeys asserts pgup/pgdn move the selection by a window's worth
// (clamped to the ends).
func TestAgentsPageKeys(t *testing.T) {
	const n = 20
	big := bigRoster(n)
	m := newMCPModel(t, aztec(), nil)
	m = resize(m, 100, 24)
	m = seedTeam(m, func(c *conversation) { c.setTeamStart("t1", "", big) })
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)

	th, hk, width, height := m.agentsListGeometry()
	page := agentsListPageSize(th, height, teamSelectableList(th, m.team, m.conv.latestTeamBlock(), hk, width))
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	m = mm.(Model)
	if m.team.cursor != page {
		t.Errorf("pgdn moved cursor to %d, want one page (%d)", m.team.cursor, page)
	}
	w := teamSelectableList(th, m.team, m.conv.latestTeamBlock(), hk, width).window(th, height)
	wantUp := max(0, m.team.cursor-(w.end-w.start))
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	m = mm.(Model)
	if m.team.cursor != wantUp {
		t.Errorf("pgup moved cursor to %d, want current physical-window move to %d", m.team.cursor, wantUp)
	}
}

// TestAgentsRosterShowsRole asserts the member role is surfaced on the roster row
// (the detail the calm inline card omits).
func TestAgentsRosterShowsRole(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) {
		c.setTeamStart("t1", "", []client.TeamMemberSpec{
			{Name: "lead", Role: "coordinator", Lead: true, Mutating: true},
			{Name: "scout", Role: "researcher"},
		})
	})
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "coordinator") || !strings.Contains(out, "researcher") {
		t.Errorf("roster rows should show member roles, got %q", out)
	}
}

// TestInlineTeamRollupAdvertisesOverlay asserts the INLINE Team card's "+K more"
// roll-up (shown only when the roster exceeds maxTeamLanes) advertises f6 so a
// capped inline card is the discovery point for the full overlay.
func TestInlineTeamRollupAdvertisesOverlay(t *testing.T) {
	var big []client.TeamMemberSpec
	big = append(big, client.TeamMemberSpec{Name: "lead", Lead: true})
	for i := 0; i < maxTeamLanes+2; i++ {
		big = append(big, client.TeamMemberSpec{Name: "m" + string(rune('a'+i))})
	}
	out := teamCard(t, false, func(c *conversation) { c.setTeamStart("t1", "", big) })
	if !strings.Contains(out, "more · f6") {
		t.Errorf("inline roll-up should advertise the f6 overlay, got %q", out)
	}

	// A small team (no roll-up) must NOT carry the hint — it has nothing to overflow.
	small := teamCard(t, false, func(c *conversation) { c.setTeamStart("t1", "", roster()) })
	if strings.Contains(small, "f6") {
		t.Errorf("a non-overflowing inline card should not advertise f6, got %q", small)
	}
}

// ctxTurnEnd builds a turn.end team.member msg carrying the per-member context
// meter fields (used input tokens + the engine window) for member name on team t1.
func ctxTurnEnd(name string, used, window int64) client.TeamMsg {
	return member(name, "turn.end", client.TeamMsg{
		Usage:         client.Usage{InputTokens: used},
		ContextUsed:   used,
		ContextWindow: window,
	})
}

func TestTeamRosterRowUsesIdentityWorkAndRuntimeLines(t *testing.T) {
	ln := &teamLane{
		name:           "overlay-reader",
		role:           "Inspect the Agents overlay rendering and report problems",
		current:        "RecordFinding",
		ctxUsed:        33800,
		ctxWindow:      1100000,
		routedCategory: "medium",
		routedModel:    "gpt-5.6-terra",
		usage:          client.Usage{InputTokens: 100600, OutputTokens: 626},
	}
	row := stripANSIstr(renderTeamRosterRow(aztec().Style("spinner"), "▶ ", ln, 0, false, 100))
	lines := strings.Split(row, "\n")
	if len(lines) != 3 {
		t.Fatalf("team row has %d lines, want identity/work/runtime: %q", len(lines), row)
	}
	if !strings.HasPrefix(lines[0], "▶ ◆ · overlay-reader") {
		t.Fatalf("identity line lost selection or member identity: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "    RecordFinding… · Inspect the Agents") {
		t.Fatalf("work line should group current action and role: %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "    ↑100.6K ↓626 · ctx ") || !strings.Contains(lines[2], "medium → gpt-5.6-terra") {
		t.Fatalf("runtime line should group tokens, context, and route: %q", lines[2])
	}
}

// TestAgentsRosterContextMeter asserts each roster lane shows the per-member
// context band (the footer's renderContextMeter vocabulary) once a turn.end has
// carried a known window: a low-pressure member reads "ctx … NN%" with no ⚠, a
// danger-band member appends the ⚠ marker (which survives ANSI stripping), and a
// member whose window is still unknown shows its ↑/↓ usage but NO ctx/% meter.
func TestAgentsRosterContextMeter(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) {
		c.setTeamStart("t1", "", []client.TeamMemberSpec{
			{Name: "lead", Role: "coordinator", Lead: true, Mutating: true},
			{Name: "low", Role: "worker"},
			{Name: "danger", Role: "worker"},
			{Name: "nowin", Role: "worker"},
		})
		c.addTeamMember(ctxTurnEnd("low", 40000, 200000))     // 20% → ok, no ⚠
		c.addTeamMember(ctxTurnEnd("danger", 190000, 200000)) // 95% → danger ⚠
		// "nowin" gets a turn.end with a 0 window: usage lands, but no meter.
		c.addTeamMember(member("nowin", "turn.end", client.TeamMsg{
			Usage: client.Usage{InputTokens: 1200}}))
	})
	m = resize(m, 100, 80)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	out := stripANSIstr(m.View().Content)

	rosterLine := func(name string) string {
		lines := strings.Split(out, "\n")
		for i, ln := range lines {
			if strings.Contains(ln, name) {
				return strings.Join(lines[i:min(i+3, len(lines))], "\n")
			}
		}
		return ""
	}

	low := rosterLine("low")
	if strings.Contains(low, "[38;") || strings.Contains(low, "[m") {
		t.Errorf("roster context meter must not leak stripped ANSI control bytes, got %q", low)
	}
	if !strings.Contains(low, "ctx ") || !strings.Contains(low, "20%") {
		t.Errorf("low-pressure lane should show 'ctx … 20%%', got %q", low)
	}
	if strings.Contains(low, ctxDangerMark) {
		t.Errorf("low-pressure lane must NOT show the ⚠ marker, got %q", low)
	}
	if !strings.Contains(low, "40K/200K") {
		t.Errorf("low lane should show used/window, got %q", low)
	}

	danger := rosterLine("danger")
	if !strings.Contains(danger, "95%") || !strings.Contains(danger, ctxDangerMark) {
		t.Errorf("danger lane should show '95%% ⚠' (⚠ surviving ANSI strip), got %q", danger)
	}
	if !strings.Contains(danger, ctxGlyphDanger) {
		t.Errorf("danger lane should use the danger fill glyph, got %q", danger)
	}

	nowin := rosterLine("nowin")
	if strings.Contains(nowin, "ctx ") || strings.Contains(nowin, "%") {
		t.Errorf("unknown-window lane must NOT draw a ctx meter, got %q", nowin)
	}
	if !strings.Contains(nowin, "↑") || !strings.Contains(nowin, "↓") {
		t.Errorf("unknown-window lane should still show ↑/↓ usage, got %q", nowin)
	}
}

// TestAgentsFocusContextMeter asserts the per-member focus pane sub-header also
// carries the context band when the member's window is known.
func TestAgentsFocusContextMeter(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) {
		c.setTeamStart("t1", "", []client.TeamMemberSpec{{Name: "scout", Role: "researcher", Lead: true}})
		c.addTeamMember(ctxTurnEnd("scout", 176000, 200000)) // 88% → warn band
	})
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.team.view != teamFocus {
		t.Fatalf("view = %v, want teamFocus", m.team.view)
	}
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "ctx ") || !strings.Contains(out, "88%") {
		t.Errorf("focus sub-header should carry the context meter (88%%), got %q", out)
	}
	if !strings.Contains(out, "176K/200K") {
		t.Errorf("focus meter should show used/window, got %q", out)
	}
}

// --- task sub-view ---------------------------------------------------------

// tasksTeam builds a team whose shared task list exercises every state the task
// sub-view distinguishes: a completed task, an in-progress task with an assignee,
// a pending task blocked by the incomplete task, and a pending-unblocked task.
func tasksTeam(c *conversation) {
	c.setTeamStart("t1", "", roster())
	c.setTeamTasks("t1", []client.TeamTask{
		{ID: "task-1", Description: "investigate", State: "completed", Assignee: "scout"},
		{ID: "task-2", Description: "implement fix", State: "in_progress", Assignee: "lead"},
		{ID: "task-3", Description: "review", State: "pending", Deps: []string{"task-2"}},
		{ID: "task-4", Description: "lint", State: "pending"},
	})
}

// TestSetTeamTasksAttribution asserts setTeamTasks attributes the snapshot to the
// matching Team block (returns true) and is a no-op miss (returns false) for an
// unknown parent call id — the same attribution contract setTeamEnd has.
func TestSetTeamTasksAttribution(t *testing.T) {
	c := &conversation{}
	c.addTool("t1", "Team", `{}`)
	if !c.setTeamTasks("t1", []client.TeamTask{{ID: "task-1", State: "pending"}}) {
		t.Fatal("setTeamTasks should attribute to the Team card and return true")
	}
	if got := c.blocks[0].teamTasks; len(got) != 1 || got[0].id != "task-1" {
		t.Errorf("task snapshot not stored on the block: %+v", got)
	}
	if c.setTeamTasks("nope", []client.TeamTask{{ID: "x"}}) {
		t.Error("setTeamTasks should return false for an unknown parent call id")
	}
}

// TestAgentsTasksToggle asserts t flips the roster to the task sub-view, and t/esc
// both return to the roster; esc from the roster still closes the overlay.
func TestAgentsTasksToggle(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, tasksTeam)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	if m.team.view != teamRoster {
		t.Fatalf("view = %v, want teamRoster", m.team.view)
	}

	// t → task sub-view.
	mm, _ = m.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	m = mm.(Model)
	if m.team.view != teamTasks {
		t.Fatalf("t did not open the task sub-view: %v", m.team.view)
	}

	// t → back to roster.
	mm, _ = m.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	m = mm.(Model)
	if m.team.view != teamRoster {
		t.Fatalf("t did not toggle back to the roster: %v", m.team.view)
	}

	// t → tasks, then esc → back to roster.
	mm, _ = m.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mm.(Model)
	if m.team.view != teamRoster {
		t.Fatalf("esc from tasks should return to the roster, got %v", m.team.view)
	}

	// esc from the roster closes the overlay.
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mm.(Model)
	if m.team.view != teamNone {
		t.Fatalf("esc from roster did not close: %v", m.team.view)
	}
}

// TestTaskRowShowsDescription: a task with a description renders the 6-field row
// (glyph · id · desc · state · assignee · deps) with the truncated description.
func TestTaskRowShowsDescription(t *testing.T) {
	row := taskRow(teamTask{id: "task-1", desc: "wire the gRPC echo", state: "in-progress", assignee: "researcher"}, nil)
	if !strings.Contains(row, "task-1") || !strings.Contains(row, "wire the gRPC echo") {
		t.Errorf("row must show id + description, got %q", row)
	}
	if !strings.Contains(row, "in-progress") || !strings.Contains(row, "researcher") {
		t.Errorf("row must still show state + assignee, got %q", row)
	}
	// desc sits between id and state.
	di := strings.Index(row, "wire the gRPC echo")
	si := strings.Index(row, "in-progress")
	if di < 0 || si < 0 || di > si {
		t.Errorf("description must render before state, got %q", row)
	}
}

// TestTaskRowDescLessKeepsFiveFields: a description-less task keeps the original
// 5-field row with no empty desc cell.
func TestTaskRowDescLessKeepsFiveFields(t *testing.T) {
	row := taskRow(teamTask{id: "task-2", state: "pending"}, nil)
	want := "task-2 · pending · — · deps:—"
	if !strings.HasSuffix(stripANSIstr(row), want) {
		t.Errorf("desc-less row = %q, want suffix %q (no empty desc cell)", row, want)
	}
}

// TestTaskRowTruncatesLongDesc: a description longer than maxTaskDescLen is
// ellipsised so the row stays one line.
func TestTaskRowTruncatesLongDesc(t *testing.T) {
	long := strings.Repeat("x", maxTaskDescLen+20)
	row := taskRow(teamTask{id: "task-3", desc: long, state: "pending"}, nil)
	if strings.Contains(row, long) {
		t.Errorf("over-cap description must be truncated, got %q", row)
	}
	if !strings.Contains(row, "…") {
		t.Errorf("truncated description should carry an ellipsis, got %q", row)
	}
}

// TestAgentsTasksEmpty asserts a team with no tasks reads as a muted "(no tasks)"
// with a zeroed summary and never panics.
func TestAgentsTasksEmpty(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) { c.setTeamStart("t1", "", roster()) })
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	m = mm.(Model)
	if m.team.view != teamTasks {
		t.Fatalf("view = %v, want teamTasks", m.team.view)
	}
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "(no tasks)") {
		t.Errorf("empty task list should show '(no tasks)', got %q", out)
	}
	if !strings.Contains(out, "0 done · 0 in-progress · 0 pending") {
		t.Errorf("empty summary should report zero counts, got %q", out)
	}
}

// TestAgentsTasksSummary asserts the summary line counts each state and flags the
// blocked subset of pending tasks.
func TestAgentsTasksSummary(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, tasksTeam)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	m = mm.(Model)
	out := stripANSIstr(m.View().Content)
	// 1 completed, 1 in-progress, 2 pending of which 1 (task-3, dep on the
	// in-progress task-2) is blocked.
	if !strings.Contains(out, "1 done · 1 in-progress · 2 pending(1 blocked)") {
		t.Errorf("task summary mismatch, got %q", out)
	}
	// The blocked pending task uses the ⊘ glyph (survives ANSI strip).
	if !strings.Contains(out, "⊘") {
		t.Errorf("a blocked pending task should render the ⊘ glyph, got %q", out)
	}
}

// --- findings sub-view -----------------------------------------------------

// findingsTeam builds a team whose shared findings ledger carries two members'
// findings, exercising the member-grouped row rendering and the summary roll-up.
func findingsTeam(c *conversation) {
	c.setTeamStart("t1", "", roster())
	c.setTeamFindings("t1", []client.TeamFinding{
		{Member: "scout", Body: "the cache key omits the tenant id"},
		{Member: "lead", Body: "fix applied; tests green"},
	})
}

// TestSetTeamFindingsAttribution asserts setTeamFindings stores the snapshot on the
// matching Team block and is a no-op for an unknown parent call id (the same
// attribution contract setTeamTasks has).
func TestSetTeamFindingsAttribution(t *testing.T) {
	c := &conversation{}
	c.addTool("t1", "Team", `{}`)
	c.setTeamFindings("t1", []client.TeamFinding{{Member: "scout", Body: "found it"}})
	if got := c.blocks[0].teamFindings; len(got) != 1 || got[0].member != "scout" || got[0].body != "found it" {
		t.Errorf("findings snapshot not stored on the block: %+v", got)
	}
	// A miss must not panic and must not touch the block's ledger.
	c.setTeamFindings("nope", []client.TeamFinding{{Member: "x", Body: "y"}})
	if got := c.blocks[0].teamFindings; len(got) != 1 || got[0].member != "scout" {
		t.Errorf("a miss must leave the matched block's ledger unchanged: %+v", got)
	}
}

// TestAgentsFindingsToggle asserts f flips the roster to the findings sub-view, and
// f/esc both return to the roster; esc from the roster still closes the overlay.
func TestAgentsFindingsToggle(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, findingsTeam)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	if m.team.view != teamRoster {
		t.Fatalf("view = %v, want teamRoster", m.team.view)
	}

	// f → findings sub-view.
	mm, _ = m.Update(tea.KeyPressMsg{Code: 'f', Text: "f"})
	m = mm.(Model)
	if m.team.view != teamFindings {
		t.Fatalf("f did not open the findings sub-view: %v", m.team.view)
	}

	// f → back to roster.
	mm, _ = m.Update(tea.KeyPressMsg{Code: 'f', Text: "f"})
	m = mm.(Model)
	if m.team.view != teamRoster {
		t.Fatalf("f did not toggle back to the roster: %v", m.team.view)
	}

	// f → findings, then esc → back to roster.
	mm, _ = m.Update(tea.KeyPressMsg{Code: 'f', Text: "f"})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mm.(Model)
	if m.team.view != teamRoster {
		t.Fatalf("esc from findings should return to the roster, got %v", m.team.view)
	}
}

// TestAgentsFindingsShowsBody is the production-gap regression: pushing a
// TeamFindings TeamMsg through the model and opening the findings sub-view must
// actually RENDER the finding body (member + body), proving EvTeamFindings no longer
// dies at the model field. It drives the real EventToMsg→applyTeam→render path by
// pushing the TeamMsg through Update, mirroring how the wire delivers it.
func TestAgentsFindingsShowsBody(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	// Seed a Team card + roster so the overlay has a block to attribute to.
	m = seedTeam(m, func(c *conversation) { c.setTeamStart("t1", "", roster()) })
	// Deliver the findings snapshot the way the wire does: a TeamFindings TeamMsg
	// routed through Update → applyTeam → setTeamFindings.
	mm, _ := m.Update(client.TeamMsg{
		Kind:         client.TeamFindings,
		ParentCallID: "t1",
		TeamID:       "t1",
		Findings: []client.TeamFinding{
			{Member: "scout", Body: "UNIQUE_FINDING_BODY the cache key omits the tenant id"},
		},
	})
	m = mm.(Model)

	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: 'f', Text: "f"})
	m = mm.(Model)
	if m.team.view != teamFindings {
		t.Fatalf("view = %v, want teamFindings", m.team.view)
	}
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "UNIQUE_FINDING_BODY the cache key omits the tenant id") {
		t.Fatalf("findings sub-view did not render the finding body, got:\n%s", out)
	}
	if !strings.Contains(out, "scout") {
		t.Errorf("findings row should name the recording member, got:\n%s", out)
	}
	if !strings.Contains(out, "1 finding(s) from 1 member(s)") {
		t.Errorf("findings summary mismatch, got:\n%s", out)
	}
}

// TestTeamEndSetsTransientNotice (WI-4) asserts a team.end sets the transient footer
// status "team done · N rounds" and adds NO durable scrollback notice.
func TestTeamEndSetsTransientNotice(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) { c.setTeamStart("t1", "team-x", roster()) })
	before := len(m.conv.blocks)
	mm, _ := m.Update(client.TeamMsg{
		Kind:         client.TeamEnd,
		ParentCallID: "t1",
		TeamID:       "team-x",
		Rounds:       3,
		Stop:         "end_turn",
	})
	m = mm.(Model)
	if got := stripANSIstr(m.statusMsg); got != "team done · 3 rounds" {
		t.Errorf("team.end status = %q, want %q", got, "team done · 3 rounds")
	}
	// No durable notice block was added by the team.end (the card + ResultMsg carry
	// the durable signal); only the team card may have updated, never a new notice.
	for i := before; i < len(m.conv.blocks); i++ {
		if m.conv.blocks[i].kind == blockNotice {
			t.Errorf("team.end must NOT add a scrollback notice; got %q", m.conv.blocks[i].raw)
		}
	}
}

// TestAgentsFindingsEmpty asserts a team with no findings reads as a muted
// "(no findings)" with a zeroed summary and never panics.
func TestAgentsFindingsEmpty(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) { c.setTeamStart("t1", "", roster()) })
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: 'f', Text: "f"})
	m = mm.(Model)
	if m.team.view != teamFindings {
		t.Fatalf("view = %v, want teamFindings", m.team.view)
	}
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "(no findings)") {
		t.Errorf("empty ledger should show '(no findings)', got %q", out)
	}
	if !strings.Contains(out, "0 finding(s) from 0 member(s)") {
		t.Errorf("empty summary should report zero counts, got %q", out)
	}
}

// --- goldens ---------------------------------------------------------------

// agentsGoldenTeam builds a representative team for the overlay goldens: a lead +
// three members with mixed activity (a mutating builder, a read-only scout, a
// tester), so the roster exercises lead-first ordering, the ✎/· mutating cue, and
// the … heartbeat.
func agentsGoldenTeam(c *conversation) {
	c.setTeamStart("t1", "", []client.TeamMemberSpec{
		{Name: "lead", Role: "coordinator", Lead: true, Mutating: true},
		{Name: "scout", Role: "researcher", Mutating: false},
		{Name: "builder", Role: "implementer", Mutating: true},
		{Name: "tester", Role: "verifier", Mutating: true},
	})
	c.addTeamMember(member("scout", "message.delta", client.TeamMsg{Text: "searching for the failing path"}))
	c.addTeamMember(member("scout", "tool.call", client.TeamMsg{ToolName: "Grep", Detail: "pattern: handleErr"}))
	c.addTeamMember(member("scout", "tool.result", client.TeamMsg{ToolName: "Grep", Detail: "3 matches in dispatch.go"}))
	c.addTeamMember(member("scout", "turn.end", client.TeamMsg{Usage: client.Usage{InputTokens: 1200, OutputTokens: 80}}))
	c.addTeamMember(member("builder", "tool.call", client.TeamMsg{ToolName: "Edit"}))
	c.addTeamMember(member("builder", "turn.end", client.TeamMsg{Usage: client.Usage{InputTokens: 3400, OutputTokens: 220}}))
	c.addTeamMember(member("lead", "turn.end", client.TeamMsg{Usage: client.Usage{InputTokens: 900, OutputTokens: 40}}))
}

// TestAgentsRosterGolden locks the roster overlay.
func TestAgentsRosterGolden(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, agentsGoldenTeam)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	if m.team.view != teamRoster {
		t.Fatalf("view = %v, want teamRoster", m.team.view)
	}
	got := stripANSI([]byte(m.View().Content))
	assertFitsViewport(t, got, m.width)
	compareGolden(t, "team_roster.golden", got)
}

// agentsIdleTeam builds a team caught MID-RUN between rounds: the scout has fired a
// per-round result (so it is IDLE — finished its round, awaiting the next) while the
// lead is still working a tool. The team is NOT ended (no setTeamEnd), so the roster
// must show the scout as "○ ... idle" (not "done") and the lead as "◆ ... Edit…".
// This is the human-visible proof the idle state renders distinctly mid-run.
func agentsIdleTeam(c *conversation) {
	c.setTeamStart("t1", "", roster()) // lead (mutating) + scout (read-only)
	c.addTeamMember(member("scout", "tool.call", client.TeamMsg{ToolName: "Grep", Detail: "pattern: handleErr"}))
	c.addTeamMember(member("scout", "turn.end", client.TeamMsg{Usage: client.Usage{InputTokens: 1200, OutputTokens: 80}}))
	c.addTeamMember(member("scout", "result", client.TeamMsg{})) // scout finished round 0 → IDLE
	c.addTeamMember(member("lead", "tool.call", client.TeamMsg{ToolName: "Edit"}))
}

// TestAgentsRosterMidRunIdleGolden locks the mid-run roster overlay: a member that
// finished its round shows "○ ... idle" while the team is still live, visibly
// DIFFERENT from the all-"working" team_roster.golden. Fail-on-regression: with the
// permanent-flag bug the scout would render "○ ... done", a different golden.
func TestAgentsRosterMidRunIdleGolden(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, agentsIdleTeam)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	if m.team.view != teamRoster {
		t.Fatalf("view = %v, want teamRoster", m.team.view)
	}
	got := stripANSI([]byte(m.View().Content))
	assertFitsViewport(t, got, m.width)
	compareGolden(t, "team_roster_midrun_idle.golden", got)
}

// TestAgentsTasksView locks the task sub-view golden: a team with completed,
// in-progress (assignee), pending-blocked (deps), and pending-unblocked tasks; the
// summary line with counts + the blocked annotation; one glyphed row per task.
func TestAgentsTasksView(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, tasksTeam)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	m = mm.(Model)
	if m.team.view != teamTasks {
		t.Fatalf("view = %v, want teamTasks", m.team.view)
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "team_tasks.golden", got)
}

// TestAgentsFindingsView locks the findings sub-view golden: a team with two
// members' findings, the summary roll-up, and one member·body row per finding.
func TestAgentsFindingsView(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, findingsTeam)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: 'f', Text: "f"})
	m = mm.(Model)
	if m.team.view != teamFindings {
		t.Fatalf("view = %v, want teamFindings", m.team.view)
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "team_findings.golden", got)
}

// TestAgentsRosterWindowedGolden locks a 20-member roster WINDOWED at a ~24-row
// terminal: a bounded window of rows, the "+K below" tail, and the footer hint
// all visible (no clipping). The cursor sits a few rows in so both tails show.
func TestAgentsRosterWindowedGolden(t *testing.T) {
	big := bigRoster(20)
	m := newMCPModel(t, aztec(), nil)
	m = resize(m, 100, 24)
	m = seedTeam(m, func(c *conversation) { c.setTeamStart("t1", "", big) })
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	// Move the cursor down past the first window so both "+K above" and "+K below"
	// tails render at once (cursor centred in the windowed slice).
	for i := 0; i < 8; i++ {
		mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		m = mm.(Model)
	}
	got := stripANSI([]byte(m.View().Content))
	assertFitsViewport(t, got, m.width)
	compareGolden(t, "team_roster_windowed.golden", got)
}

// TestAgentsFocusGolden locks the per-member focus pane (the scout, which has a
// message line + a resolved tool chip with a Detail preview).
func TestAgentsFocusGolden(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, agentsGoldenTeam)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	// Lead is row 0; scout is row 1.
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.team.view != teamFocus || m.team.member != "scout" {
		t.Fatalf("focus = %v/%q, want focus/scout", m.team.view, m.team.member)
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "team_focus.golden", got)
}

// verboseFocusTeam builds a single-member team whose lane carries a long trace
// (many tool chips, each with a Detail preview so each is its own line) — enough
// to overflow a short terminal's focus pane and exercise the height bound.
func verboseFocusTeam(c *conversation) {
	c.setTeamStart("t1", "", []client.TeamMemberSpec{{Name: "scout", Role: "researcher", Lead: true}})
	c.addTeamMember(member("scout", "message.delta", client.TeamMsg{Text: "investigating the whole subsystem"}))
	for i := 0; i < maxTeamTrace; i++ {
		c.addTeamMember(member("scout", "tool.call", client.TeamMsg{
			ToolName: "Read", Detail: fmt.Sprintf("file-%02d.go", i),
		}))
		c.addTeamMember(member("scout", "tool.result", client.TeamMsg{
			ToolName: "Read", Detail: fmt.Sprintf("%d lines", 100+i),
		}))
	}
}

// TestAgentsFocusWindowed asserts a verbose member's focus pane is BOUNDED to the
// available height (the same height-safety the roster window has): the trace is
// capped to the rows that fit, a "… +N more lines" tail surfaces the overflow,
// and the header + "esc back" footer are ALWAYS visible (never clipped by
// lipgloss.Place). Mirrors TestAgentsRosterWindowed.
func TestAgentsFocusWindowed(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = resize(m, 100, 24) // vp height 16 → ~6 trace rows
	m = seedTeam(m, verboseFocusTeam)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	// scout is the only (lead) member → row 0; enter focuses it.
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.team.view != teamFocus {
		t.Fatalf("view = %v, want teamFocus", m.team.view)
	}

	rows := teamFocusRows(m.vp.Height())
	// The unbounded trace would be > rows (message + maxTeamTrace chip lines).
	if rows >= maxTeamTrace {
		t.Fatalf("test premise broken: focus window %d must be smaller than the trace", rows)
	}
	out := stripANSIstr(m.View().Content)

	// Header + footer always visible (never pushed off-screen).
	if !strings.Contains(out, "agent · scout") {
		t.Errorf("focus header clipped by the height bound, got %q", out)
	}
	if !strings.Contains(out, "esc back") {
		t.Errorf("focus footer clipped by the height bound, got %q", out)
	}
	// The overflow is surfaced by the accurate rendered-line range.
	if !strings.Contains(out, "lines 1–") {
		t.Errorf("a bounded focus pane should show an accurate visible range, got %q", out)
	}
	// On a TALL terminal the same trace fits with no tail (the bound is min(cap, fit)).
	tall := resize(m, 100, 80)
	tallOut := stripANSIstr(tall.View().Content)
	if strings.Contains(tallOut, " of 12") {
		t.Errorf("a tall terminal should not truncate the trace, got %q", tallOut)
	}
}

// TestAgentsFocusWindowedGolden locks the bounded focus pane at a ~24-row
// terminal: a capped trace, its accurate visible range, and the header + "esc
// back" footer all visible (no clipping).
func TestAgentsFocusWindowedGolden(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = resize(m, 100, 24)
	m = seedTeam(m, verboseFocusTeam)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.team.view != teamFocus {
		t.Fatalf("view = %v, want teamFocus", m.team.view)
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "team_focus_windowed.golden", got)
}

// TestTeamRosterRoutedMetadata asserts the opt-in model router's bare metadata
// (category + model, ADR 0034) surfaces on a member's roster row in the f6 Teams
// tab as a muted "routed: <category> → <model>" cue — and is absent for an unrouted
// member (a DEFINED member that pinned its own model, or no router). It carries no
// member content (gauntlet #7).
func TestTeamRosterRoutedMetadata(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) {
		c.setTeamStart("t1", "", []client.TeamMemberSpec{
			{Name: "lead", Role: "coordinator", Lead: true, Model: "openai/gpt-4.5"},
			{Name: "deep", Role: "investigate", RoutedCategory: "large", RoutedModel: "anthropic/claude-opus-4", Model: "anthropic/claude-opus-4"},
		})
	})
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "routed: large → anthropic/claude-opus-4") {
		t.Errorf("routed member roster row should carry the routed cue:\n%s", out)
	}
	// The plain (non-routed) lead member shows its inherited model as a "model:" cue
	// (issue #112 / ADR 0035) — not a routed cue.
	if !strings.Contains(out, "model: openai/gpt-4.5") {
		t.Errorf("non-routed lead roster row should carry the plain model: cue:\n%s", out)
	}
	// Exactly one "routed:" occurrence (the routed member only); the lead carries model: instead.
	if n := strings.Count(out, "routed:"); n != 1 {
		t.Errorf("exactly one routed cue expected (the routed member only), got %d:\n%s", n, out)
	}
	// Exactly one plain "model:" occurrence (the lead only); the routed member shows the
	// model inside its routed cue, not as a standalone "model:" line.
	if n := strings.Count(out, "model:"); n != 1 {
		t.Errorf("exactly one plain model: cue expected (the non-routed lead only), got %d:\n%s", n, out)
	}
}

// TestTeamFocusRendersFailureCauseOnStoppedError asserts the focus pane renders the
// per-round failure cause for a member benched on an error (issue #331, mirroring the
// subagent focus pane). It drives a real client.TeamMsg "result" carrying a Cause
// through addTeamMember, then a team.end that stops the member for an error, then the
// focus render — asserting "failed: <cause>" appears. A done member with a prior cause
// does NOT render it (it recovered), and a later result's cause overwrites an earlier
// one (last non-empty wins).
func TestTeamFocusRendersFailureCauseOnStoppedError(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) {
		c.setTeamStart("t1", "", roster())
		// scout's round failed with a known cause.
		c.addTeamMember(member("scout", "result", client.TeamMsg{Cause: "upstream 503: model overloaded"}))
		// team.end benches scout on an error.
		c.setTeamEnd("t1", "", 1, "end_turn", client.Usage{},
			[]client.TeamMemberDisposition{{Name: "lead"}, {Name: "scout", Stopped: true, Reason: "error"}})
	})
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	// Move to scout (cursor 1) and focus.
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "failed: upstream 503: model overloaded") {
		t.Errorf("focus pane for a stopped-on-error member must render the failure cause:\n%s", out)
	}
	// The failure line must WRAP to the card's text budget (teamFailureLine receives
	// the viewport WIDTH, not the height — the issue-#331 review caught the height
	// being passed, which disabled wrapping and let a long cause overflow the card).
	// "failed: upstream 503: model overloaded" is 41 runes, so it wraps at any
	// realistic card budget (cardTextWidth caps at min(terminal-10, 100)).
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "failed:") && len([]rune(ln)) > 104 {
			t.Errorf("the failure line must wrap to the card text budget, got a %d-rune line %q", len([]rune(ln)), ln)
		}
	}
}

// TestTeamFocusNoFailureCauseForDoneMember asserts a DONE member that carried a prior
// (recovered) cause does NOT render "failed:" — it recovered, so the cause is not
// surfaced. It uses a direct lane + teamFailureLine check (the roster focus render
// path is exercised above; this isolates the gate).
func TestTeamFocusNoFailureCauseForDoneMember(t *testing.T) {
	ln := &teamLane{name: "scout", stopped: false, stopReason: "", cause: "upstream 503"}
	// teamFailureLine is self-defensive (mirroring subagentFailureLine): a done member
	// (stopped=false) returns "" from the helper itself, independent of the caller's
	// gate in renderTeamFocus.
	if got := teamFailureLine(ln, 80); got != "" {
		t.Errorf("a done member must not render a failure line, got %q", got)
	}
	if got := teamLaneState(ln, true); strings.Contains(got, "failed") {
		t.Errorf("a done member must not surface the cause in its lane state, got %q", got)
	}
	// A member stopped for a NON-error reason (e.g. cancelled) with a stale cause
	// renders nothing either.
	ln2 := &teamLane{name: "scout", stopped: true, stopReason: "cancelled", cause: "upstream 503"}
	if got := teamFailureLine(ln2, 80); got != "" {
		t.Errorf("a non-error stop must not render a failure line, got %q", got)
	}
}

// TestTeamLaneCauseLastNonEmptyWins asserts a later result's cause overwrites an
// earlier one (the last failed round's cause is what the lane keeps).
func TestTeamLaneCauseLastNonEmptyWins(t *testing.T) {
	c := &conversation{}
	c.addTool("t1", "Team", `{}`)
	c.setTeamStart("t1", "", roster())
	c.addTeamMember(member("scout", "result", client.TeamMsg{Cause: "first failure C1"}))
	c.addTeamMember(member("scout", "result", client.TeamMsg{Cause: "second failure C2"}))
	ln := c.blocks[0].lane("scout")
	if ln.cause != "second failure C2" {
		t.Errorf("last non-empty cause must win, got %q", ln.cause)
	}
	// A later empty cause (clean round) does NOT erase a prior one — last NON-EMPTY wins.
	c.addTeamMember(member("scout", "result", client.TeamMsg{}))
	if ln.cause != "second failure C2" {
		t.Errorf("an empty cause must not erase a prior non-empty one, got %q", ln.cause)
	}
}

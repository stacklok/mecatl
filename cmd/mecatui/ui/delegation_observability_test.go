package ui

// Scenario tests for the delegation observability convergence (ADR 0079, plan
// delegation-observability-convergence Scenario 3): the mecatui Subagent and
// Parallel surfaces render the BOUNDED previews the wire now carries, converge on
// the Team trace format, and stop claiming content is hidden. Team-unique
// structures stay Team-only.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// TestDelegationObservability_Scenario3_CollapsedCardShowsCurrentTool pins AC3.1: a
// collapsed, RUNNING Subagent card extends its existing counts line with the
// child's live current-tool name — `subagent · <tool> · ↑<in> ↓<out> · N tools ·
// ctrl+t trace` — no heartbeat ticker (this is a render, never a ticking
// animation). The tool name is the LATEST tool's name, not a stale one.
func TestDelegationObservability_Scenario3_CollapsedCardShowsCurrentTool(t *testing.T) {
	r := newTestRenderer()
	c := &conversation{}
	c.addTool("p1", "Subagent", `{"prompt":"investigate the loop"}`)
	c.setSubagentStart("p1", "investigate the loop", "", "", "", "")
	addSubTool(c, "p1", "Grep", false, 1)
	addSubTool(c, "p1", "Read", false, 2)
	out := stripANSIstr(r.renderBlock(0, &c.blocks[0], false))

	if !strings.Contains(out, "subagent · Read ·") {
		t.Errorf("collapsed running card must name the live current tool (the latest one), got %q", out)
	}
	if strings.Contains(out, "subagent · Grep ·") {
		t.Errorf("collapsed card must track the LATEST tool, not an earlier one, got %q", out)
	}
	if !strings.Contains(out, "↑0 ↓0") || !strings.Contains(out, "2 tools") || !strings.Contains(out, "ctrl+t trace") {
		t.Errorf("collapsed card must keep the token/tool counts and the trace affordance, got %q", out)
	}
	if strings.Contains(out, "Read…") {
		t.Errorf("collapsed card carries no heartbeat ticker, got %q", out)
	}
	// A resolved card shows the stat line, NOT the live tool name.
	c.setSubagentEnd("p1", client.Usage{InputTokens: 1200, OutputTokens: 80}, 2, "end_turn", 2500)
	resolved := stripANSIstr(r.renderBlock(0, &c.blocks[0], false))
	if !strings.Contains(resolved, "stop:done") {
		t.Errorf("resolved card should show the stat line, got %q", resolved)
	}
	if strings.Contains(resolved, "subagent · Read ·") {
		t.Errorf("resolved card must not render the live current-tool slot, got %q", resolved)
	}
}

// TestDelegationObservability_Scenario3_ExpandedCardShowsBoundedPreviews pins AC3.2:
// ctrl+t on a Subagent card shows bounded args/result previews per child tool call
// (Team chip format: `✓ Grep — pattern: foo`) plus capped child message lines, and
// the TUI applies its secondary caps on top of the engine's clamp.
func TestDelegationObservability_Scenario3_ExpandedCardShowsBoundedPreviews(t *testing.T) {
	r := newTestRenderer()
	c := &conversation{}
	c.addTool("p1", "Subagent", `{"prompt":"investigate"}`)
	applySubagentTo(c, client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "p1", ChildID: "c1", Goal: "investigate"})
	applySubagentTo(c, client.SubagentMsg{
		Kind: client.SubagentTool, ParentCallID: "p1", ChildID: "c1",
		InnerKind: "tool.call", ToolName: "Grep", Detail: `pattern: foo`, ToolCount: 1,
	})
	applySubagentTo(c, client.SubagentMsg{
		Kind: client.SubagentTool, ParentCallID: "p1", ChildID: "c1",
		InnerKind: "tool.result", ToolName: "Grep", Detail: "3 matches found", ToolCount: 1,
	})
	applySubagentTo(c, client.SubagentMsg{
		Kind: client.SubagentTool, ParentCallID: "p1", ChildID: "c1",
		InnerKind: "message.delta", Text: "looking into the loop", ToolCount: 1,
	})
	out := stripANSIstr(r.renderBlock(0, &c.blocks[0], true))

	if !strings.Contains(out, "✓ Grep") {
		t.Errorf("expanded card should show the Team-format tool chip, got %q", out)
	}
	// The result preview replaces the call's arg preview on the chip.
	if !strings.Contains(out, "— 3 matches found") {
		t.Errorf("expanded card should show the bounded result preview next to the chip, got %q", out)
	}
	if strings.Contains(out, "— pattern: foo") {
		t.Errorf("a result preview supersedes the call's arg preview (the Team discipline), got %q", out)
	}
	if !strings.Contains(out, "looking into the loop") {
		t.Errorf("expanded card should show the capped child message line, got %q", out)
	}

	// The TUI's secondary cap bounds a long detail even when the engine cap let it through.
	longDetail := strings.Repeat("x", maxTraceDetailLen+40)
	applySubagentTo(c, client.SubagentMsg{
		Kind: client.SubagentTool, ParentCallID: "p1", ChildID: "c1",
		InnerKind: "tool.call", ToolName: "Read", Detail: longDetail, ToolCount: 2,
	})
	out = stripANSIstr(r.renderBlock(0, &c.blocks[0], true))
	if strings.Contains(out, strings.Repeat("x", maxTraceDetailLen+40)) {
		t.Errorf("expanded card must bound the detail preview to maxTraceDetailLen, got %q", out)
	}
	if !strings.Contains(out, strings.Repeat("x", maxTraceDetailLen-1)) {
		t.Errorf("expanded card should show the truncated detail preview, got %q", out)
	}
}

// TestDelegationObservability_Scenario3_ParallelViewsShowBoundedPreviews pins AC3.3:
// the Parallel ctrl+a group focus renders each branch's interleaved trace (tool
// chips with bounded previews + capped message lines) below its roster line, in
// the Team focus format — not just flat roster rows.
func TestDelegationObservability_Scenario3_ParallelViewsShowBoundedPreviews(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedParallel(m, "p1",
		startPar("p1", "all", 1),
		branchStartPar("p1", 0, "branch-1", "explore"),
		client.ParallelMsg{
			Kind: client.ParallelBranchTool, ParentCallID: "p1", BranchIndex: 0,
			InnerKind: "tool.call", ToolName: "Grep", Detail: `pattern: foo`, ToolCount: 1,
		},
		client.ParallelMsg{
			Kind: client.ParallelBranchTool, ParentCallID: "p1", BranchIndex: 0,
			InnerKind: "message.delta", Text: "branch note", ToolCount: 1,
		},
	)
	mm, _ := m.Update(ctrlKey('a'))
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.parallel.view != parallelGroupView {
		t.Fatalf("enter should focus the group, view = %v", m.parallel.view)
	}
	out := stripANSIstr(m.View().Content)

	// The branch roster line survives as the header for its trace.
	if !strings.Contains(out, "branch-1") {
		t.Errorf("group focus should show the branch roster line, got %q", out)
	}
	// Below it, the interleaved trace: the Team chip with its bounded preview…
	if !strings.Contains(out, "✓ Grep — pattern: foo") {
		t.Errorf("group focus should show the branch tool chip with its bounded preview, got %q", out)
	}
	// …and the capped branch message line.
	if !strings.Contains(out, "branch note") {
		t.Errorf("group focus should show the branch message line, got %q", out)
	}
}

// TestDelegationObservability_Scenario3_HonestyNoteIsBoundedPreviews pins AC3.4: no
// "args/results hidden" string remains in the TUI; every Subagent/Parallel trace
// surface carries the accurate "bounded previews" note instead. The Surfaces sweep
// greps the ui package source so a NEW renderer that re-introduces the stale claim
// fails here without waiting for a behavioural regression.
func TestDelegationObservability_Scenario3_HonestyNoteIsBoundedPreviews(t *testing.T) {
	assertNoBannedRenderString(t, "args/results hidden")
	assertNoBannedRenderString(t, "hidden (context-isolated)")

	// Expanded Subagent card.
	r := newTestRenderer()
	c := &conversation{}
	c.addTool("p1", "Subagent", `{"prompt":"investigate"}`)
	applySubagentTo(c, client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "p1", ChildID: "c1", Goal: "investigate"})
	card := stripANSIstr(r.renderBlock(0, &c.blocks[0], true))
	if !strings.Contains(card, "bounded previews") {
		t.Errorf("expanded Subagent card should carry the bounded-previews note, got %q", card)
	}

	// Subagent focus pane.
	m := newMCPModel(t, aztec(), nil)
	m = seedSubagents(m, "p1",
		startSub("p1", "c1", "audit auth"),
		toolSub("p1", "c1", "Grep", false, 1),
	)
	mm, _ := m.Update(ctrlKey('a'))
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	focus := stripANSIstr(m.View().Content)
	if !strings.Contains(focus, "bounded previews") {
		t.Errorf("Subagent focus pane should carry the bounded-previews note, got %q", focus)
	}

	// Parallel group focus.
	mp := newMCPModel(t, aztec(), nil)
	mp = seedParallel(mp, "p1",
		startPar("p1", "all", 1),
		branchStartPar("p1", 0, "branch-1", "explore"),
		branchToolPar("p1", 0, "Grep", false, 1),
	)
	mm, _ = mp.Update(ctrlKey('a'))
	mp = mm.(Model)
	mm, _ = mp.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mp = mm.(Model)
	group := stripANSIstr(mp.View().Content)
	if !strings.Contains(group, "bounded previews") {
		t.Errorf("Parallel group focus should carry the bounded-previews note, got %q", group)
	}
}

// TestDelegationObservability_Scenario3_TeamUniqueStructuresStayTeamOnly pins AC3.5:
// the Team-unique structures — the task board, findings ledger, per-member
// dispositions, the mutating cue ✎, and the context meter — never render on a
// Subagent or Parallel surface, even when the team data exists in the conversation.
func TestDelegationObservability_Scenario3_TeamUniqueStructuresStayTeamOnly(t *testing.T) {
	// POSITIVE control: a seeded Team surface DOES render these structures, so their
	// absence below is attributable to the Subagent/Parallel render path, not to
	// broken seeds.
	tm := newMCPModel(t, aztec(), nil)
	tm = seedTeam(tm, agentsGoldenTeam)
	mm, _ := tm.Update(ctrlKey('a'))
	tm = mm.(Model)
	for i := 0; i < 3 && tm.agentsTab != tabTeams; i++ {
		mm, _ = tm.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		tm = mm.(Model)
	}
	if tm.agentsTab != tabTeams {
		t.Fatalf("expected Teams tab, got %v", tm.agentsTab)
	}
	teamOut := stripANSIstr(tm.View().Content)
	if !strings.Contains(teamOut, "✎") {
		t.Fatalf("control broken: the Teams surface should render the mutating cue, got %q", teamOut)
	}
	if !strings.Contains(teamOut, "task") && !strings.Contains(teamOut, "finding") {
		t.Fatalf("control broken: the Teams surface should render the task/findings affordances, got %q", teamOut)
	}

	// Seed a subagent (in the SAME conversation as the team, so the structures'
	// absence is not explained by an empty surface), then reach the Subagents tab.
	tm = seedSubagents(tm, "s1", startSub("s1", "c1", "audit auth"))
	mm, _ = tm.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	tm = mm.(Model)
	for i := 0; i < 3 && tm.agentsTab != tabSubagents; i++ {
		mm, _ = tm.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		tm = mm.(Model)
	}
	if tm.agentsTab != tabSubagents {
		t.Fatalf("expected Subagents tab, got %v", tm.agentsTab)
	}
	subOut := stripANSIstr(tm.View().Content)
	for _, banned := range []string{"✎", "task board", "findings", "ledger"} {
		if strings.Contains(subOut, banned) {
			t.Errorf("Subagent surface must not render the Team-unique %q, got %q", banned, subOut)
		}
	}
	if strings.Contains(subOut, "stopped —") {
		t.Errorf("Subagent surface must not render a Team disposition, got %q", subOut)
	}

	// The Subagent child focus pane.
	mm, _ = tm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	tm = mm.(Model)
	if tm.subagents.view != subagentFocus {
		t.Fatalf("enter should focus the child, view = %v", tm.subagents.view)
	}
	subFocus := stripANSIstr(tm.View().Content)
	for _, banned := range []string{"✎", "task board", "findings", "ledger", "stopped —"} {
		if strings.Contains(subFocus, banned) {
			t.Errorf("Subagent focus must not render the Team-unique %q, got %q", banned, subFocus)
		}
	}
	// The per-member context meter carries a used/window fraction ("ctx <bar> · N/M")
	// — no Subagent surface renders a member window, so no such fraction appears.
	assertNoContextMeter(t, "Subagent focus", subFocus)

	// The Parallel group focus.
	pm := newMCPModel(t, aztec(), nil)
	pm = seedTeam(pm, agentsGoldenTeam) // team data present in the conversation
	pm = seedParallel(pm, "p1",
		startPar("p1", "judge", 2),
		branchStartPar("p1", 0, "branch-1", "approach A"),
		branchStartPar("p1", 1, "branch-2", "approach B"),
		branchEndPar("p1", 0, 100, 20, 2, "end_turn", false, "/fork/branch-1"),
		branchEndPar("p1", 1, 120, 25, 3, "end_turn", false, "/fork/branch-2"),
		endPar("p1", "judge", 2, 1, "/fork/branch-2", "end_turn"),
	)
	mm, _ = pm.Update(ctrlKey('a'))
	pm = mm.(Model)
	for i := 0; i < 3 && pm.agentsTab != tabParallel; i++ {
		mm, _ = pm.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		pm = mm.(Model)
	}
	mm, _ = pm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	pm = mm.(Model)
	if pm.parallel.view != parallelGroupView {
		t.Fatalf("enter should focus the group, view = %v", pm.parallel.view)
	}
	parFocus := stripANSIstr(pm.View().Content)
	for _, banned := range []string{"✎", "task board", "findings", "ledger", "stopped —"} {
		if strings.Contains(parFocus, banned) {
			t.Errorf("Parallel focus must not render the Team-unique %q, got %q", banned, parFocus)
		}
	}
	assertNoContextMeter(t, "Parallel focus", parFocus)
}

// assertNoContextMeter asserts the rendered surface carries no used/window context
// fraction — the per-member context meter's distinguishing mark ("ctx <bar> ·
// N/M"). A bare "ctx N" (the footer's window-less degrade) is NOT a meter, so the
// assertion keys on the fraction, not the "ctx" prefix.
func assertNoContextMeter(t *testing.T, surface, out string) {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		i := strings.Index(line, "ctx ")
		if i < 0 {
			continue
		}
		if strings.Contains(line[i:], "/") {
			t.Errorf("%s must not render a per-member context meter (used/window fraction), got %q", surface, line)
		}
	}
}

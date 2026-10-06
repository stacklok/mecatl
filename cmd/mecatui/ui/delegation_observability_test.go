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
// f6 agents` — no heartbeat ticker (this is a render, never a ticking
// animation). The tool name is the LATEST tool's name, not a stale one.
func TestDelegationObservability_Scenario3_CollapsedCardShowsCurrentTool(t *testing.T) {
	r := newTestRenderer()
	c := &conversation{}
	c.addTool("p1", "Subagent", `{"prompt":"investigate the loop"}`)
	c.startSubagentCard("p1", "investigate the loop", "", "", "", "")
	addSubTool(c, "p1", "Grep", false, 1)
	addSubTool(c, "p1", "Read", false, 2)
	out := stripANSIstr(r.renderSnapshot(0, c.testBlocks()[0], false))

	if !strings.Contains(out, "subagent · Read ·") {
		t.Errorf("collapsed running card must name the live current tool (the latest one), got %q", out)
	}
	if strings.Contains(out, "subagent · Grep ·") {
		t.Errorf("collapsed card must track the LATEST tool, not an earlier one, got %q", out)
	}
	if !strings.Contains(out, "↑0 ↓0") || !strings.Contains(out, "2 tools") || !strings.Contains(out, "f6 agents") {
		t.Errorf("collapsed card must keep the token/tool counts and the trace affordance, got %q", out)
	}
	if strings.Contains(out, "Read…") {
		t.Errorf("collapsed card carries no heartbeat ticker, got %q", out)
	}
	// A resolved card shows the stat line, NOT the live tool name.
	c.finishSubagentCard("p1", client.Usage{InputTokens: 1200, OutputTokens: 80}, 2, "end_turn", 2500)
	resolved := stripANSIstr(r.renderSnapshot(0, c.testBlocks()[0], false))
	if !strings.Contains(resolved, "stop:done") {
		t.Errorf("resolved card should show the stat line, got %q", resolved)
	}
	if strings.Contains(resolved, "subagent · Read ·") {
		t.Errorf("resolved card must not render the live current-tool slot, got %q", resolved)
	}
}

// TestDelegationObservability_Scenario3_ExpandedCardShowsBoundedPreviews retains the
// historical AC3.2 name after the newer mecatui-quieter-conversation-tool-calls plan
// retired ctrl+t card expansion. Bounded preview content remains observable in the
// actual f6 Agents focus, which is the newer plan's replacement route.
func TestDelegationObservability_Scenario3_ExpandedCardShowsBoundedPreviews(t *testing.T) {
	const rawMessage = "child message must stay out of the parent conversation"
	longMessage := strings.Repeat("m", maxTraceMessageLen+40)
	m := newMCPModel(t, aztec(), nil)
	m = seedSubagents(m, "p1",
		startSub("p1", "c1", "audit auth"),
		toolSubPreview("p1", "c1", "tool.call", "Grep", "pattern: auth", 1),
		toolSubPreview("p1", "c1", "tool.call", "Read", "file: auth.go", 2),
		toolSubPreview("p1", "c1", "tool.result", "Read", "found the auth boundary", 2),
		toolSubPreview("p1", "c1", "message.delta", "", rawMessage+longMessage, 2),
	)

	// Before f6 opens the Agents overlay, child content must not spill into the parent
	// conversation. This absence assertion is mutation-proven below.
	conversation := stripANSIstr(m.View().Content)
	if strings.Contains(conversation, rawMessage) {
		t.Errorf("child content spilled into the parent conversation: %q", conversation)
	}

	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.subagents.view != subagentFocus {
		t.Fatalf("enter should focus the child in the f6 Agents view, view = %v", m.subagents.view)
	}
	focus := stripANSIstr(m.View().Content)
	if !strings.Contains(focus, "✓ Grep — pattern: auth") {
		t.Errorf("Agents focus should show the child tool args preview, got %q", focus)
	}
	if !strings.Contains(focus, "✓ Read — found the auth boundary") {
		t.Errorf("Agents focus should show the child tool result preview, got %q", focus)
	}
	if strings.Contains(focus, rawMessage+longMessage) {
		t.Errorf("Agents focus must cap child message lines, got %q", focus)
	}
	if !strings.Contains(focus, rawMessage+strings.Repeat("m", 16)) {
		t.Errorf("Agents focus should retain the capped child message prefix, got %q", focus)
	}
}

// TestDelegationObservability_Scenario3_ParallelViewsShowBoundedPreviews pins AC3.3:
// the Parallel f6 group focus renders each branch's interleaved trace (tool
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
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
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

	// Subagent focus pane.
	m := newMCPModel(t, aztec(), nil)
	m = seedSubagents(m, "p1",
		startSub("p1", "c1", "audit auth"),
		toolSub("p1", "c1", "Grep", false, 1),
	)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
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
	mm, _ = mp.Update(tea.KeyPressMsg{Code: tea.KeyF6})
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
	mm, _ := tm.Update(tea.KeyPressMsg{Code: tea.KeyF6})
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
	mm, _ = pm.Update(tea.KeyPressMsg{Code: tea.KeyF6})
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

package ui

// Tests for the unified f6 "agents" overlay (Package C, iteration 7) and the
// fleet status footer segment (iteration 6). The overlay is ONE surface with two
// tabs — Subagents (the flat Subagent-child fleet) and Teams (the former team overlay,
// reused verbatim). `tab` switches tabs, `enter` focuses a row, `esc` steps back /
// closes. The default tab is context-sensitive (Teams when a team is live, else
// Subagents when subagents ran). Everything renders from the REDACTED, metadata-only
// subagent.* / team.* event projection — no child content (gauntlet #7).

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// startSub / toolSub / endSub build the three subagent.* projections for a child.
func startSub(parent, child, goal string) client.SubagentMsg {
	return client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: parent, ChildID: child, Goal: goal}
}

func toolSub(parent, child, tool string, isErr bool, count int) client.SubagentMsg {
	return client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: parent, ChildID: child, ToolName: tool, IsError: isErr, ToolCount: count}
}

func endSub(parent, child string, in, out int64, count int, stop string) client.SubagentMsg {
	return client.SubagentMsg{
		Kind: client.SubagentEnd, ParentCallID: parent, ChildID: child,
		Usage: client.Usage{InputTokens: in, OutputTokens: out}, ToolCount: count, Stop: stop, DurationMs: 1200,
	}
}

// toolSubPreview builds a subagent.tool projection carrying an InnerKind + bounded
// content (Detail for a tool.call/tool.result, Text for a message.delta) — the
// ADR-0079 widened wire. count is the running tool total the event carries.
func toolSubPreview(parent, child, innerKind, tool, content string, count int) client.SubagentMsg {
	msg := toolSub(parent, child, tool, false, count)
	msg.InnerKind = innerKind
	if innerKind == "message.delta" {
		msg.Text = content
	} else {
		msg.Detail = content
	}
	return msg
}

// branchToolParPreview builds a parallel.branch_tool projection carrying an InnerKind
// + bounded content (Detail / Text) — the ADR-0079 widened wire. count is the running
// tool total the event carries.
func branchToolParPreview(parent string, idx int, innerKind, tool, content string, count int) client.ParallelMsg {
	msg := branchToolPar(parent, idx, tool, false, count)
	msg.InnerKind = innerKind
	if innerKind == "message.delta" {
		msg.Text = content
	} else {
		msg.Detail = content
	}
	return msg
}

// seedSubagents applies a sequence of subagent.* msgs through the real Update path so
// the model's fleet collection is built exactly as it would be at runtime. It seeds a
// Subagent tool card for the inline-card routing first (the fleet routing keys on ChildID
// and is independent, but a card keeps the inline path realistic).
func seedSubagents(m Model, parent string, msgs ...client.SubagentMsg) Model {
	m.conv.addTool(parent, "Subagent", `{"prompt":"investigate"}`)
	for _, msg := range msgs {
		mm, _ := m.Update(msg)
		m = mm.(Model)
	}
	return m
}

// --- iteration 6: fleet footer segment ------------------------------------

// TestSubagentFleetCounts locks the (running, done) classification the footer segment
// and the Subagents-tab header derive from the fleet: a child is done once its
// subagent.end arrived, the rest are running.
func TestSubagentFleetCounts(t *testing.T) {
	c := &conversation{}
	c.addTool("p1", "Subagent", `{}`)
	c.fleetStart("c1", "audit auth", "", "", "", "", false)
	c.fleetStart("c2", "map coverage", "", "", "", "", false)
	c.fleetStart("c3", "trace config", "", "", "", "", false)
	c.fleetEnd("c3", client.Usage{}, 4, "end_turn", "", 1000)
	running, done := c.subagentFleetCounts()
	if running != 2 || done != 1 {
		t.Errorf("subagentFleetCounts = (%d, %d), want (2, 1)", running, done)
	}
	if !c.hasSubagents() {
		t.Error("hasSubagents should be true after a fleetStart")
	}
}

// TestSubagentFleetLiveUsage locks the live-usage projection: a turn.end subagent.tool
// carrying the child's cumulative usage updates the fleet lane (and the inline card's
// subUsage) MID-RUN, before subagent.end lands, so the roster/live line read a live ↑↓
// instead of a zero until the terminal. The wiring runs through the REAL Update path
// (applySubagent → fleetTool / addSubagentTo).
func TestSubagentFleetLiveUsage(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	// A mid-run turn.end projection carrying the cumulative usage up to this turn.
	liveUsage := client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "p1", ChildID: "c1", InnerKind: "turn.end",
		Usage: client.Usage{InputTokens: 1200, OutputTokens: 340}, ToolCount: 1}
	m = seedSubagents(m, "p1",
		startSub("p1", "c1", "audit auth"),
		toolSubPreview("p1", "c1", "tool.result", "Grep", "matches", 1),
		liveUsage,
	)
	ln := findFleetLane(m.conv.subagentFleet, "c1")
	if ln == nil {
		t.Fatal("fleet lane c1 missing")
		return
	}
	// The lane shows the live usage BEFORE any subagent.end — done is still false.
	if ln.done {
		t.Fatal("lane unexpectedly done before subagent.end")
	}
	if ln.usage.InputTokens != 1200 || ln.usage.OutputTokens != 340 {
		t.Fatalf("fleet lane live usage = %+v, want {1200 340} mid-run", ln.usage)
	}
	// The inline card's live usage (subagentLiveLine reads b.subUsage) advances too.
	if b := m.conv.subagentBlock("p1"); b == nil {
		t.Fatal("inline subagent block p1 missing")
	} else if b.subUsage.InputTokens != 1200 || b.subUsage.OutputTokens != 340 {
		t.Fatalf("inline card subUsage = %+v, want {1200 340} mid-run", b.subUsage)
	}
}

// TestSubagentLiveUsageDoesNotResetToolCount pins the cumulative-totals contract:
// ToolCount and Usage are stamped on EVERY projection (always current), so assigning
// them unconditionally can never reset the lane mid-run. The lane counts toolsStarted
// monotonically (the count advances at the call, ahead of the result) and the usage
// climbs on every projection that carries a fresh cumulative figure.
func TestSubagentLiveUsageDoesNotResetToolCount(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	// helper: a projection carrying the current cumulative totals (as the wire now does).
	proj := func(innerKind string, toolCount int, in, out int64) client.SubagentMsg {
		msg := client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "p1", ChildID: "c1",
			InnerKind: innerKind, ToolCount: toolCount, Usage: client.Usage{InputTokens: in, OutputTokens: out}}
		return msg
	}
	m = seedSubagents(m, "p1",
		startSub("p1", "c1", "audit auth"),
		proj("tool.call", 1, 0, 0),     // Read starts (count 1, no usage yet)
		proj("tool.result", 1, 0, 0),   // Read resolves (count unchanged)
		proj("message.delta", 1, 0, 0), // text, totals unchanged
		proj("turn.end", 1, 500, 120),  // turn 1 usage lands
		proj("tool.call", 2, 500, 120), // Grep starts (count 2, usage still 500/120)
		proj("tool.result", 2, 500, 120),
	)
	ln := findFleetLane(m.conv.subagentFleet, "c1")
	if ln == nil {
		t.Fatal("fleet lane c1 missing")
		return
	}
	if ln.toolCount != 2 {
		t.Fatalf("fleet lane toolCount = %d, want 2 (monotonic, current on every projection)", ln.toolCount)
	}
	if ln.usage.InputTokens != 500 || ln.usage.OutputTokens != 120 {
		t.Fatalf("fleet lane usage = %+v, want the cumulative {500 120}", ln.usage)
	}
	if b := m.conv.subagentBlock("p1"); b == nil {
		t.Fatal("inline card p1 missing")
	} else if b.subToolCount != 2 {
		t.Fatalf("inline card subToolCount = %d, want 2 (monotonic)", b.subToolCount)
	}
}

// TestSubagentFleetEmpty asserts no fleet → no footer segment + no overlay-enabling.
func TestSubagentFleetEmpty(t *testing.T) {
	c := &conversation{}
	if c.hasSubagents() {
		t.Error("an empty fleet must not report hasSubagents")
	}
	r, d := c.subagentFleetCounts()
	if r != 0 || d != 0 {
		t.Errorf("empty fleet counts = (%d, %d), want (0, 0)", r, d)
	}
}

// TestFleetMissingChildIDDropped asserts a subagent event with no ChildID is dropped
// from the fleet (the fleet keys on ChildID); the inline card still routes by
// ParentCallID.
func TestFleetMissingChildIDDropped(t *testing.T) {
	c := &conversation{}
	c.fleetStart("", "no id", "", "", "", "", false)
	if c.hasSubagents() {
		t.Error("a childID-less start must not create a fleet lane")
	}
}

// TestFooterHiddenWithoutSubagents asserts the footer has NO fleet segment when no
// subagent has run — the no-subagent footer stays byte-identical to before.
func TestFooterHiddenWithoutSubagents(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	out := stripANSIstr(m.fitFooter(m.deps.Theme.Style("muted").Render("connected"), 120))
	if strings.Contains(out, "subagents") || strings.Contains(out, subagentFleetGlyph) {
		t.Errorf("footer should carry no fleet segment with zero subagents, got %q", out)
	}
}

// TestFooterShowsRunningAndDone asserts the footer fleet segment shows the
// running/done counts and the f6 cue once subagents have run (mixed live+done).
func TestFooterShowsRunningAndDone(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedSubagents(m, "p1",
		startSub("p1", "c1", "audit auth"),
		toolSub("p1", "c1", "Grep", false, 1),
		startSub("p1", "c2", "map coverage"),
		startSub("p1", "c3", "trace config"),
		endSub("p1", "c3", 15000, 4000, 9, "end_turn"),
	)
	out := stripANSIstr(m.fitFooter(m.deps.Theme.Style("muted").Render("connected"), 160))
	if !strings.Contains(out, "subagents") {
		t.Errorf("footer should show the fleet segment, got %q", out)
	}
	// 2 running (c1, c2), 1 done (c3).
	if !strings.Contains(out, "2"+subagentRunGlyph) || !strings.Contains(out, "1"+subagentDoneGlyph) {
		t.Errorf("footer should show 2 running / 1 done, got %q", out)
	}
	if !strings.Contains(out, "f6") {
		t.Errorf("footer fleet segment should advertise f6, got %q", out)
	}
}

// TestFooterFleetTiers asserts the three fleet footer tiers degrade cleanly at
// narrowing widths (full → medium → compact → dropped), like the team segment.
func TestFooterFleetTiers(t *testing.T) {
	th := aztec()
	full := stripANSIstr(subagentFooterFull(th, 3, 1, defaultHelpKeys().agents))
	medium := stripANSIstr(subagentFooterMedium(3, 1, defaultHelpKeys().agents))
	compact := stripANSIstr(subagentFooterCompact(3, 1))
	if !strings.Contains(full, "subagents") {
		t.Errorf("full tier should name 'subagents', got %q", full)
	}
	if strings.Contains(medium, "subagents") {
		t.Errorf("medium tier should drop the word 'subagents', got %q", medium)
	}
	if !strings.Contains(medium, "f6") {
		t.Errorf("medium tier should keep the f6 cue, got %q", medium)
	}
	if strings.Contains(compact, "f6") {
		t.Errorf("compact tier should drop the f6 cue, got %q", compact)
	}
	for _, s := range []string{full, medium, compact} {
		if !strings.Contains(s, "3"+subagentRunGlyph) || !strings.Contains(s, "1"+subagentDoneGlyph) {
			t.Errorf("tier %q should carry the counts", s)
		}
	}
}

// TestFooterFleetTierSelection proves the SELECTION wiring (not just the builders):
// fitFooter picks the medium tier (drops the "subagents" word, keeps "f6") when the
// full tier won't fit, and the compact tier (drops "f6") when even medium won't fit.
//
// The candidate widths are reconstructed EXACTLY as fitFooter builds them (agents prefix
// + sep + the matching ctx-meter tier), so the chosen test widths are deterministic
// regardless of glyph widths. With no team and no ctx window the agents prefix is the
// fleet segment alone and the meter tiers collapse to a short "ctx <n>" form, so the
// agents tier drives the choice.
func TestFooterFleetTierSelection(t *testing.T) {
	th := aztec()
	m := newMCPModel(t, aztec(), nil)
	m = seedSubagents(m, "p1",
		startSub("p1", "c1", "audit auth"),
		startSub("p1", "c2", "map coverage"),
		startSub("p1", "c3", "trace config"),
	) // 3 running, 0 done
	left := th.Style("muted").Render("connected")
	leftW := lipgloss.Width(left)

	// Rebuild the four agents-bearing candidates fitFooter forms (see view.go fitFooter).
	const sep = "  "
	meter := renderContextMeter(th, m.contextTokens, m.contextWindow())
	meterCompact := renderContextMeterCompact(th, m.contextTokens, m.contextWindow())
	meterMinimal := renderContextMeterMinimal(th, m.contextTokens, m.contextWindow())
	full := subagentFooterFull(th, 3, 0, defaultHelpKeys().agents)
	medium := th.Style("spinner").Render(subagentFooterMedium(3, 0, defaultHelpKeys().agents))
	compact := th.Style("spinner").Render(subagentFooterCompact(3, 0))
	cand0 := full + sep + meter + " · " + renderUsageFacets(m.usage) // richest
	cand1 := full + sep + meter
	cand2 := medium + sep + meterCompact
	cand3 := compact + sep + meterMinimal
	w := func(s string) int { return leftW + lipgloss.Width(s) + footerGapPad }

	// Sanity: the candidates strictly narrow, so a between-width selects a single tier.
	if w(cand0) <= w(cand1) || w(cand1) <= w(cand2) || w(cand2) <= w(cand3) {
		t.Fatalf("test premise broken: candidate widths not strictly decreasing: %d %d %d %d",
			w(cand0), w(cand1), w(cand2), w(cand3))
	}

	// At a width that fits cand2 (medium tier) but NOT cand1 (full tier): medium chosen —
	// the "subagents" word is dropped, the f6 cue survives.
	out := stripANSIstr(m.fitFooter(left, w(cand2)))
	if strings.Contains(out, "subagents") {
		t.Errorf("medium-width footer should drop the 'subagents' word, got %q", out)
	}
	if !strings.Contains(out, "f6") {
		t.Errorf("medium-width footer should keep the f6 cue, got %q", out)
	}

	// At a width that fits cand3 (compact tier) but NOT cand2 (medium tier): compact
	// chosen — the f6 cue is dropped, the counts survive.
	out = stripANSIstr(m.fitFooter(left, w(cand3)))
	if strings.Contains(out, "f6") {
		t.Errorf("compact-width footer should drop the f6 cue, got %q", out)
	}
	if !strings.Contains(out, "3"+subagentRunGlyph) {
		t.Errorf("compact-width footer should still render the counts, got %q", out)
	}
}

// TestFooterFleetAndTeamCoexist asserts a session running BOTH a live team and
// subagents shows BOTH segments in the footer at full width (the unified-overlay
// premise: both are reachable).
func TestFooterFleetAndTeamCoexist(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) {
		c.setTeamStart("t1", "team-x", roster())
		c.addTeamMember(member("scout", "tool.call", client.TeamMsg{ToolName: "Grep"}))
	})
	m = seedSubagents(m, "p1", startSub("p1", "c1", "audit auth"))
	out := stripANSIstr(m.fitFooter(m.deps.Theme.Style("muted").Render("connected"), 200))
	if !strings.Contains(out, "team-x") {
		t.Errorf("footer should keep the live-team segment, got %q", out)
	}
	if !strings.Contains(out, "subagents") {
		t.Errorf("footer should also show the fleet segment, got %q", out)
	}
}

// --- iteration 7: unified tabbed overlay ----------------------------------

// TestF6OpensSubagentsTabWhenNoTeam asserts the context-sensitive default: with
// subagents running and no team, f6 opens the overlay on the Subagents tab.
func TestF6OpensSubagentsTabWhenNoTeam(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedSubagents(m, "p1",
		startSub("p1", "c1", "audit auth"),
		toolSub("p1", "c1", "Grep", false, 1),
	)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	if m.team.view == teamNone {
		t.Fatal("f6 should open the agents overlay")
	}
	if m.agentsTab != tabSubagents {
		t.Errorf("default tab = %v, want tabSubagents (subagents live, no team)", m.agentsTab)
	}
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "subagents · 1 running · 0 done") {
		t.Errorf("Subagents roster header missing, got %q", out)
	}
	if !strings.Contains(out, "audit auth") {
		t.Errorf("fleet row goal missing, got %q", out)
	}
}

// TestF6OpensTeamsTabWhenTeamLive asserts the context-sensitive default: with a
// LIVE team, f6 opens on the Teams tab even if subagents also ran.
func TestF6OpensTeamsTabWhenTeamLive(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) {
		c.setTeamStart("t1", "team-x", roster())
		c.addTeamMember(member("scout", "tool.call", client.TeamMsg{ToolName: "Grep"}))
	})
	m = seedSubagents(m, "p1", startSub("p1", "c1", "audit auth"))
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	if m.agentsTab != tabTeams {
		t.Errorf("default tab = %v, want tabTeams (team live)", m.agentsTab)
	}
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "agents · 2 members") {
		t.Errorf("Teams roster header missing, got %q", out)
	}
}

// TestPreferredAgentsTab pins the context-sensitive default-tab predicate in isolation.
func TestPreferredAgentsTab(t *testing.T) {
	var m Model
	cases := []struct {
		name                                                    string
		teamLive, haveTeam, haveSub, parallelLive, haveParallel bool
		want                                                    agentsTab
	}{
		{"team live wins over parallel live", true, true, true, true, true, tabTeams},
		{"team live, no sub", true, true, false, false, false, tabTeams},
		{"subs only", false, false, true, false, false, tabSubagents},
		{"finished team only", false, true, false, false, false, tabTeams},
		{"both present, team not live → subs", false, true, true, false, false, tabSubagents},
		{"parallel live wins over haveSub", false, false, true, true, true, tabParallel},
		{"parallel live, no sub", false, false, false, true, true, tabParallel},
		{"finished parallel only", false, false, false, false, true, tabParallel},
		{"haveSub beats finished parallel", false, false, true, false, true, tabSubagents},
		{"finished parallel beats finished team", false, true, false, false, true, tabParallel},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := m.preferredAgentsTab(c.teamLive, c.haveTeam, c.haveSub, c.parallelLive, c.haveParallel); got != c.want {
				t.Errorf("preferredAgentsTab(%v,%v,%v,%v,%v) = %v, want %v",
					c.teamLive, c.haveTeam, c.haveSub, c.parallelLive, c.haveParallel, got, c.want)
			}
		})
	}
}

// TestTabSwitchesSubagentsToTeams asserts `tab` flips the active tab both ways.
func TestTabSwitchesSubagentsToTeams(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, func(c *conversation) {
		c.setTeamStart("t1", "team-x", roster())
		c.addTeamMember(member("scout", "tool.call", client.TeamMsg{ToolName: "Grep"}))
	})
	m = seedSubagents(m, "p1", startSub("p1", "c1", "audit auth"))
	// Open: team live → Teams tab.
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	if m.agentsTab != tabTeams {
		t.Fatalf("expected Teams tab on open, got %v", m.agentsTab)
	}
	// tab cycles Teams → Subagents (the default case wraps from the last tab).
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = mm.(Model)
	if m.agentsTab != tabSubagents {
		t.Fatalf("tab did not switch to Subagents, got %v", m.agentsTab)
	}
	if !strings.Contains(stripANSIstr(m.View().Content), "subagents · 1 running") {
		t.Errorf("Subagents tab body not shown after switch")
	}
	// tab → Parallel (the new third tab between Subagents and Teams).
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = mm.(Model)
	if m.agentsTab != tabParallel {
		t.Fatalf("tab did not switch to Parallel, got %v", m.agentsTab)
	}
	// tab → back to Teams.
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = mm.(Model)
	if m.agentsTab != tabTeams {
		t.Fatalf("tab did not switch back to Teams, got %v", m.agentsTab)
	}
}

// TestEnterFocusesSubagentChild asserts enter on a fleet row opens the child's focus
// pane (its redacted chip trace), and esc steps back to the roster.
func TestEnterFocusesSubagentChild(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedSubagents(m, "p1",
		startSub("p1", "c1", "audit auth"),
		toolSub("p1", "c1", "Grep", false, 1),
		toolSub("p1", "c1", "Read", true, 2),
	)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	if m.agentsTab != tabSubagents {
		t.Fatalf("expected Subagents tab, got %v", m.agentsTab)
	}
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.subagents.view != subagentFocus {
		t.Fatalf("enter should focus a child, view = %v", m.subagents.view)
	}
	if m.subagents.child != "c1" {
		t.Errorf("focused child = %q, want c1", m.subagents.child)
	}
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "bounded previews") {
		t.Errorf("focus pane should carry the bounded-previews note, got %q", out)
	}
	if !strings.Contains(out, "Grep") || !strings.Contains(out, "Read") {
		t.Errorf("focus pane should show the child's tool chips, got %q", out)
	}
	// esc steps back to the roster (does NOT close the overlay).
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if m.subagents.view != subagentRoster {
		t.Fatalf("esc from focus should return to the roster, view = %v", m.subagents.view)
	}
	if m.team.view == teamNone {
		t.Fatal("esc from focus must NOT close the overlay")
	}
}

// TestEscClosesSubagentOverlay asserts esc from the Subagents roster closes the
// overlay entirely.
func TestEscClosesSubagentOverlay(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedSubagents(m, "p1", startSub("p1", "c1", "audit auth"))
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if m.team.view != teamNone {
		t.Errorf("esc from the subagents roster should close the overlay, view = %v", m.team.view)
	}
}

// TestSubagentOverlayBoundsChildContent is the client-side boundedness guard for the
// Subagents tab under ADR 0079: the subagent.* projection now forwards child content
// ONLY as bounded previews (the engine clamp-scrubs them; the wire ≤ 200 runes), and
// the overlay must render them SANITIZED + TUI-capped — never raw, never unbounded.
// It seeds a child whose goal, tool name, and PREVIEW fields carry a SENTINEL plus
// control bytes, then asserts in BOTH the roster and the focused child's trace that:
//   - the raw 0x1b ESC / 0x07 BEL never reach the rendered output (sanitizeTerminal), and
//   - the bounded-previews honesty note is present (the content is bounded, not hidden), and
//   - the previews are truncated at the TUI's secondary cap (maxTraceDetailLen), and
//   - the child content never enters the parent conversation surface — it renders only
//     inside the overlay card / child trace (gauntlet #7: isolation is about the
//     CONVERSATION, not what a client may observe).
//
// A regression that forwarded a child's args/result UNBOUNDED or UNSCRUBBED fails here
// (the oversize preview renders past its cap, or the raw escape survives).
func TestSubagentOverlayBoundsChildContent(t *testing.T) {
	// Per the no-destructive-test-literals rule the control byte is an innocuous ANSI/OSC
	// escape, and the sentinel is a plain marker — never a destructive-looking command.
	const sentinel = "SECRETCONTENT"
	const evilTool = "\x1b]0;" + sentinel + "\x07Grep"
	// A preview that (a) carries a control byte and (b) far exceeds the TUI cap.
	longPreview := strings.Repeat("z", maxTraceDetailLen*3)
	m := newMCPModel(t, aztec(), nil)
	m = seedSubagents(m, "p1",
		startSub("p1", "c1", "\x1b[31m"+sentinel+" goal\x1b[0m"),
		client.SubagentMsg{
			Kind: client.SubagentTool, ParentCallID: "p1", ChildID: "c1",
			InnerKind: "tool.call", ToolName: evilTool, Detail: longPreview + "\x1b[31m", ToolCount: 1,
		},
	)

	// Roster: the goal + tool-derived state are sanitized — no raw ESC, and the sentinel
	// only ever appears as inert text (never as a control sequence).
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	roster := stripANSIstr(m.View().Content)
	if strings.ContainsRune(roster, 0x1b) {
		t.Errorf("raw ESC (0x1b) leaked into the subagents roster; sanitizeTerminal not applied:\n%q", roster)
	}

	// Focus pane: the trace renders the bounded preview sanitized + capped.
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.subagents.view != subagentFocus {
		t.Fatalf("enter should focus the child, view = %v", m.subagents.view)
	}
	focus := stripANSIstr(m.View().Content)
	if strings.ContainsRune(focus, 0x1b) {
		t.Errorf("raw ESC (0x1b) leaked into the subagent focus pane; sanitizeTerminal not applied:\n%q", focus)
	}
	if strings.Contains(focus, "\x07") {
		t.Errorf("raw BEL (0x07) leaked into the subagent focus pane:\n%q", focus)
	}
	// The bounded-previews note must be present (PRESENCE half of the guard).
	if !strings.Contains(focus, "bounded previews") {
		t.Errorf("focus pane missing the bounded-previews note:\n%q", focus)
	}
	// The preview is rendered — but CAPPED at the TUI's secondary bound: the full
	// oversize run never appears, the truncated head does.
	if strings.Contains(focus, strings.Repeat("z", maxTraceDetailLen*3)) {
		t.Errorf("an unbounded preview leaked into the focus pane (past maxTraceDetailLen):\n%q", focus)
	}
	if got := strings.Count(focus, "z"); got < maxTraceDetailLen-1 {
		t.Errorf("the bounded preview should retain its truncated source text, got %d z runes:\n%q", got, focus)
	}
	// The sanitized tool name renders as inert text (the OSC payload stripped).
	if !strings.Contains(focus, "]0;"+sentinel+"Grep") {
		t.Errorf("sanitized tool name not rendered as inert text in the focus chip trace:\n%q", focus)
	}
}

// TestSubagentFocusDisambiguatesByHash asserts two children with the SAME goal are
// disambiguated by the #<hash> ChildID suffix on the fleet rows (F2 §1.4 item 6).
func TestSubagentFocusDisambiguatesByHash(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedSubagents(m, "p1",
		startSub("p1", "explorer-aaa111", "audit auth"),
		startSub("p1", "explorer-bbb222", "audit auth"),
	)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "#aaa111") || !strings.Contains(out, "#bbb222") {
		t.Errorf("identical goals should be disambiguated by the #hash suffix, got %q", out)
	}
}

// TestSubagentNewTerminalReasonsRender asserts a child that ended via budget /
// structured-output / a limit renders a sensible glyph + label, not a blank or the
// raw token. The error-family stops get ✗; the cap-family stops get ✓ (a partial is
// still usable) with the cap named.
func TestSubagentNewTerminalReasonsRender(t *testing.T) {
	cases := []struct {
		stop      string
		wantLabel string
		wantGlyph string
	}{
		{"budget", "budget", "✓"},
		{"structured_output", "schema", "✗"},
		{"max_turns", "max-turns", "✓"},
		{"max_tool_calls", "max-tools", "✓"},
		{"error", "error", "✗"},
		{"cancelled", "cancelled", "✗"},
		{"no_progress", "no-progress", "✓"},
		{"end_turn", "done", "✓"},
	}
	for _, c := range cases {
		t.Run(c.stop, func(t *testing.T) {
			ln := &subagentLane{childID: "c1", goal: "g", done: true, stop: c.stop, toolCount: 3}
			if got := subagentLaneGlyph(ln); got != c.wantGlyph {
				t.Errorf("glyph for stop=%q = %q, want %q", c.stop, got, c.wantGlyph)
			}
			line := subagentRosterLine(ln)
			if !strings.Contains(line, c.wantLabel) {
				t.Errorf("roster line for stop=%q = %q, want label %q", c.stop, line, c.wantLabel)
			}
		})
	}
}

// TestSubagentBudgetStopThroughWire drives a `budget` terminal through the REAL path
// (endSub → Update → applySubagent → fleetEnd) — NOT a struct literal — so a regression
// in fleetEnd's stop threading (e.g. dropping the Stop field) is caught. It asserts the
// rendered roster row carries the "budget" label and the ✓ glyph (a budget-stopped
// child produced a usable partial), proving the stop reason survives the wire→lane→render
// chain end-to-end.
func TestSubagentBudgetStopThroughWire(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedSubagents(m, "p1",
		startSub("p1", "c1", "audit auth"),
		toolSub("p1", "c1", "Grep", false, 4),
		endSub("p1", "c1", 200000, 50000, 4, "budget"),
	)
	// The lane built from the wire must carry the budget stop verbatim.
	ln := findFleetLane(m.conv.subagentFleet, "c1")
	if ln == nil {
		t.Fatal("fleet lane c1 missing after the wire end event")
		return
	}
	if ln.stop != "budget" {
		t.Fatalf("fleetEnd did not thread the stop reason: lane.stop = %q, want \"budget\"", ln.stop)
	}
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "audit auth") || !strings.Contains(out, "budget") {
		t.Errorf("roster should show the budget-stopped child's label, got %q", out)
	}
	// The cap-family budget stop reads as a ✓ (usable partial), not a ✗.
	if !strings.Contains(out, "✓") {
		t.Errorf("a budget-stopped child should render the ✓ glyph, got %q", out)
	}
	if got := subagentLaneGlyph(ln); got != "✓" {
		t.Errorf("subagentLaneGlyph for a wire-budget lane = %q, want ✓", got)
	}
}

func TestSubagentRosterRowSeparatesTitleAndDetails(t *testing.T) {
	ln := &subagentLane{
		childID:       "explorer-abcdef",
		goal:          "inspect the subagent roster layout carefully while checking every visible presentation detail",
		background:    true,
		routedModel:   "gpt-5-mini",
		routingReason: "fast investigation",
		current:       "Grep",
		toolCount:     2,
		usage:         client.Usage{InputTokens: 1200, OutputTokens: 340},
	}

	selected := stripANSIstr(renderSubagentRosterRow(aztec().Style("spinner"), "▶ ", ln, 80))
	unselected := stripANSIstr(renderSubagentRosterRow(aztec().Style("muted"), "  ", ln, 80))
	selectedRows := strings.Split(selected, "\n")
	unselectedRows := strings.Split(unselected, "\n")
	if len(selectedRows) != 2 || len(unselectedRows) != 2 {
		t.Fatalf("normal-width rows = %q / %q, want title plus one details line", selected, unselected)
	}
	if !strings.HasPrefix(selectedRows[0], "▶ ◐ ") || !strings.HasPrefix(unselectedRows[0], "  ◐ ") {
		t.Fatalf("selection markers changed title semantics: %q / %q", selectedRows[0], unselectedRows[0])
	}
	if selectedRows[1] != unselectedRows[1] || !strings.HasPrefix(selectedRows[1], "    ") {
		t.Fatalf("details should retain one shared four-column indent: %q / %q", selectedRows[1], unselectedRows[1])
	}
	if !strings.Contains(selectedRows[0], "…") || !strings.Contains(selectedRows[0], "#abcdef ⇢ bg") {
		t.Fatalf("title was not explicitly clipped while retaining child identity: %q", selectedRows[0])
	}
	if !strings.Contains(selectedRows[1], "gpt-5-mini") || !strings.Contains(selectedRows[1], "Grep… · 2 tools · ↑1.2K ↓340") {
		t.Fatalf("details omitted readable metadata: %q", selectedRows[1])
	}

	narrow := stripANSIstr(renderSubagentRosterRow(aztec().Style("spinner"), "▶ ", ln, 28))
	for _, row := range strings.Split(narrow, "\n") {
		if lipgloss.Width(row) > 28 {
			t.Fatalf("narrow row overflows body width: %d: %q", lipgloss.Width(row), row)
		}
	}
	if len(strings.Split(narrow, "\n")) < 3 || !strings.Contains(narrow, "\n    ") {
		t.Fatalf("narrow details did not wrap with the required indent: %q", narrow)
	}

	wide := *ln
	wide.goal = "調査🙂調査🙂調査🙂調査🙂"
	wideRow := stripANSIstr(renderSubagentRosterRow(aztec().Style("spinner"), "▶ ", &wide, 24))
	if title := strings.Split(wideRow, "\n")[0]; lipgloss.Width(title) > 24 || !strings.Contains(title, "…") {
		t.Fatalf("wide-rune title should be clipped to one physical line: %q", title)
	}
}

// TestSubagentRosterWindowed asserts a fleet larger than the available height windows
// like the team roster: only the rows that fit render, the footer hint stays visible,
// and hidden rows surface via "+K below".
func TestSubagentRosterWindowed(t *testing.T) {
	const n = 20
	m := newMCPModel(t, aztec(), nil)
	m = resize(m, 100, 24)
	msgs := make([]client.SubagentMsg, 0, n)
	for i := 0; i < n; i++ {
		child := "child-" + string(rune('a'+i))
		msgs = append(msgs, startSub("p1", child, "explore "+child))
	}
	m = seedSubagents(m, "p1", msgs...)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	if m.agentsTab != tabSubagents {
		t.Fatalf("expected Subagents tab, got %v", m.agentsTab)
	}
	out := stripANSIstr(m.View().Content)
	rows := m.subagentRosterPageSize(m.conv.subagentFleet)
	if rows >= n {
		t.Fatalf("test premise broken: window %d must be < fleet %d", rows, n)
	}
	if !strings.Contains(out, "below") {
		t.Errorf("a windowed fleet with the cursor at the top should show a '+K below' tail, got %q", out)
	}
	if !strings.Contains(out, "enter focus") {
		t.Errorf("footer hint clipped by the window, got %q", out)
	}
}

// TestSubagentRosterFooterSentinel stays a single fitting row when a live rebound
// key label is unusually long. The footer is dynamic card chrome, not a roster row:
// it must truncate rather than wrap and accidentally widen the centred overlay.
func TestSubagentRosterFooterSentinel(t *testing.T) {
	const viewportWidth = 32
	th, hk := aztec(), defaultHelpKeys()
	hk.navUp = "rebound-key-label-with-an-unusually-long-live-value\nand-another-line"

	_, _, bodyWidth := agentsCardLayout(th, viewportWidth)
	out := stripANSIstr(renderSubagentRoster(th, subagentState{}, []subagentLane{{childID: "child", goal: "inspect"}}, hk, 0, bodyWidth))
	footer := out[strings.LastIndex(out, "\n")+1:]
	if strings.ContainsRune(footer, '\n') {
		t.Fatalf("footer rendered more than one row: %q", footer)
	}
	if got := lipgloss.Width(footer); got > focusCardTextWidth(viewportWidth) {
		t.Fatalf("footer width = %d, want <= %d: %q", got, focusCardTextWidth(viewportWidth), footer)
	}
	if !strings.Contains(footer, "...") {
		t.Fatalf("footer did not use the literal ellipsis sentinel: %q", footer)
	}
	if strings.Contains(footer, "\x1b") || strings.Contains(footer, "\nand-another") {
		t.Fatalf("footer retained unsafe or multi-row key content: %q", footer)
	}
}

// TestSubagentRosterFooterSentinelNormalWidth preserves the established default
// footer when its live key markings fit the available card body.
func TestSubagentRosterFooterSentinelNormalWidth(t *testing.T) {
	th, hk := aztec(), defaultHelpKeys()
	out := stripANSIstr(renderSubagentRoster(th, subagentState{}, []subagentLane{{childID: "child", goal: "inspect"}}, hk, 0, 160))
	footer := out[strings.LastIndex(out, "\n")+1:]
	want := "↑/↓ select · pgup/pgdn · home/end · enter focus · x cancel · tab switch · esc close"
	if footer != want {
		t.Fatalf("normal-width footer = %q, want existing output %q", footer, want)
	}
}

// TestDynamicCardChromeLine reserves its prefix and sanitizes before it truncates
// the raw dynamic text into exactly one display row.
func TestDynamicCardChromeLine(t *testing.T) {
	got := stripANSIstr(renderDynamicCardChromeLine(aztec().Style("muted"), "› ", "first\nsecond\x1b[2J", 12))
	if strings.ContainsRune(got, '\n') || strings.Contains(got, "\x1b") {
		t.Fatalf("chrome line retained a row break or terminal escape: %q", got)
	}
	if !strings.HasPrefix(got, "› ") || !strings.Contains(got, "...") {
		t.Fatalf("chrome line did not preserve prefix or ellipsis: %q", got)
	}
	if width := lipgloss.Width(got); width > 12 {
		t.Fatalf("chrome line width = %d, want <= 12: %q", width, got)
	}
}

// TestRosterRouteNavigation verifies each top-level roster delegates navigation
// to navigateRosterCursor, including live key overrides and page-sized movement.
func TestRosterRouteNavigation(t *testing.T) {
	rosters := []struct {
		name    string
		seed    func(Model) Model
		cursor  func(Model) int
		wantTab agentsTab
	}{
		{
			name: "team",
			seed: func(m Model) Model {
				return seedTeam(m, func(c *conversation) { c.setTeamStart("t1", "", roster()) })
			},
			cursor:  func(m Model) int { return m.team.cursor },
			wantTab: tabTeams,
		},
		{
			name: "subagent",
			seed: func(m Model) Model {
				return seedSubagents(m, "p1", startSub("p1", "c1", "first"), startSub("p1", "c2", "second"))
			},
			cursor:  func(m Model) int { return m.subagents.cursor },
			wantTab: tabSubagents,
		},
		{
			name: "parallel",
			seed: func(m Model) Model {
				return seedParallel(m, "p1", startPar("p1", "all", 1), startPar("p2", "all", 1))
			},
			cursor:  func(m Model) int { return m.parallel.cursor },
			wantTab: tabParallel,
		},
	}

	for _, tc := range rosters {
		t.Run(tc.name+" custom down", func(t *testing.T) {
			m := tc.seed(newMCPModel(t, aztec(), nil))
			m.keys = applyKeyOverrides(m.keys, map[string][]string{"Down": {"n"}})
			mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
			m = mm.(Model)
			if m.agentsTab != tc.wantTab {
				t.Fatalf("tab = %v, want %v", m.agentsTab, tc.wantTab)
			}

			mm, _ = m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
			m = mm.(Model)
			if got := tc.cursor(m); got != 1 {
				t.Fatalf("custom Down cursor = %d, want 1", got)
			}
			mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			m = mm.(Model)
			if got := tc.cursor(m); got != 1 {
				t.Errorf("default Down cursor = %d after override, want 1", got)
			}
		})
	}

	for _, tc := range rosters[1:] { // Team already has route-level page coverage.
		t.Run(tc.name+" page down", func(t *testing.T) {
			const n = 20
			m := resize(newMCPModel(t, aztec(), nil), 100, 24)
			if tc.name == "subagent" {
				msgs := make([]client.SubagentMsg, 0, n)
				for i := 0; i < n; i++ {
					child := "child-" + string(rune('a'+i))
					msgs = append(msgs, startSub("p1", child, child))
				}
				m = seedSubagents(m, "p1", msgs...)
			} else {
				msgs := make([]client.ParallelMsg, 0, n)
				for i := 0; i < n; i++ {
					parent := "p" + string(rune('a'+i))
					msgs = append(msgs, startPar(parent, "all", 1))
				}
				m = seedParallel(m, "p1", msgs...)
			}
			mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
			m = mm.(Model)
			page := m.subagentRosterPageSize(m.conv.subagentFleet)
			if tc.name == "parallel" {
				page = m.parallelRosterPageSize(m.conv.parallelGroups)
			}
			if page >= n {
				t.Fatalf("test premise broken: page %d must be < roster %d", page, n)
			}
			mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
			m = mm.(Model)
			if got := tc.cursor(m); got <= 0 || got >= n {
				t.Errorf("pgdown cursor = %d, want a later bounded item", got)
			}
		})
	}
}

// --- footer goldens -------------------------------------------------------

// TestFooterFleetGolden locks the three footer fleet states (no subagents / N running
// / mixed running+done) as a single stripped golden of the fitFooter output, so a
// regression in the segment format or the tiering is caught.
func TestFooterFleetGolden(t *testing.T) {
	left := aztec().Style("muted").Render("connected")

	none := newMCPModel(t, aztec(), nil)
	running := seedSubagents(newMCPModel(t, aztec(), nil), "p1",
		startSub("p1", "c1", "audit auth"),
		startSub("p1", "c2", "map coverage"),
		startSub("p1", "c3", "trace config"),
	)
	mixed := seedSubagents(newMCPModel(t, aztec(), nil), "p1",
		startSub("p1", "c1", "audit auth"),
		startSub("p1", "c2", "map coverage"),
		startSub("p1", "c3", "trace config"),
		endSub("p1", "c3", 15000, 4000, 9, "end_turn"),
	)

	var b strings.Builder
	b.WriteString("no subagents:\n")
	b.WriteString(stripANSIstr(none.fitFooter(left, 160)) + "\n\n")
	b.WriteString("3 running:\n")
	b.WriteString(stripANSIstr(running.fitFooter(left, 160)) + "\n\n")
	b.WriteString("mixed 2 running + 1 done:\n")
	b.WriteString(stripANSIstr(mixed.fitFooter(left, 160)) + "\n")
	compareGolden(t, "footer_fleet.golden", []byte(b.String()))
}

// TestFooterCtxMeterGolden locks the footer context-meter END-TO-END through the
// client→ui relay (issue #65): a SessionReadyMsg carrying a 200K ResolvedModel
// window + a TurnEndMsg setting the 40K numerator must render the bar + "40K/200K"
// with NO --context-window override. The snapshot is the stripped fitFooter output
// at a wide width so the full-fidelity tier survives. It proves the new default
// denominator source (the server-echoed window) reaches the meter via the reducer.
func TestFooterCtxMeterGolden(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 160, Height: 30},
		client.SessionReadyMsg{
			SessionID:     "sess-ctx-0001",
			ResolvedModel: client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5", ContextWindow: 200000},
		},
		client.TurnEndMsg{Turn: 1, Usage: client.Usage{InputTokens: 40000, OutputTokens: 1200}},
	)
	left := aztec().Style("muted").Render("connected")
	compareGolden(t, "footer_ctx_meter.golden", []byte(stripANSIstr(m.fitFooter(left, 160))+"\n"))
}

// TestFooterCtxUnknownGolden locks the unknown-window degrade: with NEITHER an
// override nor a server-echoed window (older server / no resolved_model), the same
// 40K numerator renders the bare "ctx 40K" with no bar and no denominator.
func TestFooterCtxUnknownGolden(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 160, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-ctx-0002"}, // zero ResolvedModel → unknown window
		client.TurnEndMsg{Turn: 1, Usage: client.Usage{InputTokens: 40000, OutputTokens: 1200}},
	)
	left := aztec().Style("muted").Render("connected")
	compareGolden(t, "footer_ctx_unknown.golden", []byte(stripANSIstr(m.fitFooter(left, 160))+"\n"))
}

// --- overlay goldens ------------------------------------------------------

// goldenFleet builds a representative mixed fleet for the overlay goldens: three
// running children (with varied current tools) + one done + one errored, so the
// roster exercises the ◐/✓/✗ glyphs, the current-tool column, and the #hash suffix.
func goldenFleet(m Model) Model {
	return seedSubagents(m, "p1",
		startSub("p1", "explorer-a3f1", "audit auth flow"),
		toolSub("p1", "explorer-a3f1", "Grep", false, 6),
		toolSubPreview("p1", "explorer-a3f1", "tool.call", "Grep", "pattern: auth", 7),
		toolSubPreview("p1", "explorer-a3f1", "message.delta", "", "checking the login flow", 7),
		startSub("p1", "explorer-b2e2", "find dead code"),
		toolSub("p1", "explorer-b2e2", "Read", false, 4),
		startSub("p1", "explorer-c1d3", "trace config loading"),
		toolSub("p1", "explorer-c1d3", "Shell", false, 11),
		startSub("p1", "explorer-d0c4", "map test coverage"),
		toolSub("p1", "explorer-d0c4", "Glob", false, 9),
		endSub("p1", "explorer-d0c4", 15000, 4000, 9, "end_turn"),
		startSub("p1", "explorer-e9b5", "check error handling"),
		endSub("p1", "explorer-e9b5", 3000, 500, 2, "error"),
	)
}

// assertFitsViewport fails if any rendered (ANSI-stripped) line exceeds the
// viewport width — the overflow guard for the centred overlay card, whose widest
// line (the footer hint) directly sets its width and is NOT wrapped by centerCard.
// A golden refresh alone can silently absorb an overflow; this keeps it loud.
func assertFitsViewport(t *testing.T, got []byte, width int) {
	t.Helper()
	for i, line := range strings.Split(string(got), "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("rendered line %d overflows the %d-col viewport (width %d): %q", i, width, w, line)
		}
	}
}

func TestFocusTraceWidthStaysBoundedOnTinyViewports(t *testing.T) {
	trace := []teamTrace{
		{name: strings.Repeat("x", 200)},
		{name: strings.Repeat("y", 200)},
	}
	for width := 1; width < 20; width++ {
		t.Run("width-"+strconv.Itoa(width), func(t *testing.T) {
			budget := focusCardTextWidth(width)
			if budget < 1 {
				t.Fatalf("focusCardTextWidth(%d) = %d, want positive", width, budget)
			}
			r := &renderer{th: aztec(), traceWidth: budget}
			assertFitsViewport(t, stripANSI([]byte(r.renderTrace(trace))), budget)
		})
	}
}

func TestParallelFocusLongBranchLabelFitsViewport(t *testing.T) {
	const viewportWidth = 90
	label := strings.Repeat("x", 200)
	m := newMCPModel(t, aztec(), nil)
	m = seedParallel(m, "p1",
		startPar("p1", "all", 1),
		branchStartPar("p1", 0, label, "inspect"),
		endPar("p1", "all", 1, 0, "", "end_turn"),
	)
	m.agentsTab = tabParallel
	m.parallel = parallelState{view: parallelGroupView, group: "p1"}
	mm, _ := m.Update(tea.WindowSizeMsg{Width: viewportWidth, Height: 30})
	m = mm.(Model)
	out := stripANSI([]byte(m.View().Content))
	assertFitsViewport(t, out, viewportWidth)
	if strings.Contains(string(out), label) {
		t.Error("parallel focus rendered the unbounded branch label")
	}
}

// TestDelegationFocusLongToolDataFitsViewport exercises the bounded-card rendering
// path for every delegation inspector. Tool projections are server metadata and can
// contain no-break identifiers, so both names and details must fit the physical
// canvas rather than widening the centered overlay.
func TestDelegationFocusLongToolDataFitsViewport(t *testing.T) {
	const viewportWidth = 90
	long := strings.Repeat("x", 200)
	bareTools := []string{long + "a", long + "b", long + "c", long + "d"}
	resize := func(m Model) Model {
		mm, _ := m.Update(tea.WindowSizeMsg{Width: viewportWidth, Height: 30})
		return mm.(Model)
	}

	cases := []struct {
		name  string
		build func(Model) Model
	}{
		{
			name: "subagent",
			build: func(m Model) Model {
				m = seedSubagents(m, "p1", startSub("p1", "child-1", "inspect"))
				for i, tool := range bareTools {
					mm, _ := m.Update(toolSub("p1", "child-1", tool, false, i+1))
					m = mm.(Model)
				}
				mm, _ := m.Update(toolSubPreview("p1", "child-1", "tool.call", long, long, len(bareTools)+1))
				m = mm.(Model)
				m.agentsTab = tabSubagents
				m.team.view = teamRoster
				m.subagents = subagentState{view: subagentFocus, child: "child-1"}
				return m
			},
		},
		{
			name: "team",
			build: func(m Model) Model {
				m = seedTeam(m, func(c *conversation) {
					c.setTeamStart("t1", "", roster())
					for _, tool := range bareTools {
						c.addTeamMember(member("scout", "tool.call", client.TeamMsg{ToolName: tool}))
					}
					c.addTeamMember(member("scout", "tool.call", client.TeamMsg{ToolName: long, Detail: long}))
				})
				m.agentsTab = tabTeams
				m.team = teamState{view: teamFocus, member: "scout"}
				return m
			},
		},
		{
			name: "parallel",
			build: func(m Model) Model {
				events := []client.ParallelMsg{
					startPar("p1", "all", 1),
					branchStartPar("p1", 0, long, "inspect"),
				}
				for i, tool := range bareTools {
					events = append(events, branchToolPar("p1", 0, tool, false, i+1))
				}
				events = append(events, branchToolParPreview("p1", 0, "tool.call", long, long, len(bareTools)+1))
				m = seedParallel(m, "p1", events...)
				m.agentsTab = tabParallel
				m.team.view = teamRoster
				m.parallel = parallelState{view: parallelGroupView, group: "p1"}
				return m
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := resize(tc.build(newMCPModel(t, aztec(), nil)))
			assertFitsViewport(t, stripANSI([]byte(m.View().Content)), viewportWidth)
		})
	}
}

// TestSubagentRosterGolden locks the Subagents-tab fleet roster overlay.
func TestSubagentRosterGolden(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = goldenFleet(m)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	if m.agentsTab != tabSubagents {
		t.Fatalf("expected Subagents tab, got %v", m.agentsTab)
	}
	got := stripANSI([]byte(m.View().Content))
	assertFitsViewport(t, got, m.width)
	compareGolden(t, "subagent_roster.golden", got)
}

// TestSubagentFocusGolden locks one child's focus pane (its redacted chip trace + the
// context-isolation note).
func TestSubagentFocusGolden(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = goldenFleet(m)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.subagents.view != subagentFocus {
		t.Fatalf("expected subagentFocus, got %v", m.subagents.view)
	}
	got := stripANSI([]byte(m.View().Content))
	assertFitsViewport(t, got, m.width)
	compareGolden(t, "subagent_focus.golden", got)
}

// goldenBackgroundFleet builds the background-variant fleet for the overlay goldens:
// one RUNNING detached child seeded FIRST (so cursor 0 / enter focuses it), one DONE
// background child (its result still with the registry — uncollected), and the
// representative foreground mix in between. It exercises the ⇢ bg marker against
// plain lanes, the done-background row, and (via the done child's subagent.end) the
// transient footer notice — all on the REAL styled render path.
func goldenBackgroundFleet(m Model) Model {
	m = seedSubagents(m, "p1",
		startBgSub("p1", "explorer-f8a6", "background deep audit"),
		toolSub("p1", "explorer-f8a6", "Shell", false, 3),
	)
	m = goldenFleet(m)
	return seedSubagents(m, "p2",
		startBgSub("p2", "explorer-g7b7", "background doc sweep"),
		endSub("p2", "explorer-g7b7", 8000, 1500, 5, "end_turn"),
	)
}

// TestSubagentRosterBackgroundGolden locks the fleet roster with background lanes in
// the mix: the styled bytes + layout around the ⇢ bg marker (running AND done
// background rows) and the background-done transient footer notice are pinned, with
// the same viewport-fit guard as the plain roster golden.
func TestSubagentRosterBackgroundGolden(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = goldenBackgroundFleet(m)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	if m.agentsTab != tabSubagents {
		t.Fatalf("expected Subagents tab, got %v", m.agentsTab)
	}
	got := stripANSI([]byte(m.View().Content))
	assertFitsViewport(t, got, m.width)
	compareGolden(t, "subagent_roster_background.golden", got)
}

// TestSubagentFocusBackgroundGolden locks a RUNNING background child's focus pane:
// the ⇢ bg marker on the header line, the honest delivery note ("runs detached …
// SubagentStatus"), and the cancel hint, with the viewport-fit guard.
func TestSubagentFocusBackgroundGolden(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = goldenBackgroundFleet(m)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // cursor 0 = the running background lane
	m = mm.(Model)
	if m.subagents.view != subagentFocus || m.subagents.child != "explorer-f8a6" {
		t.Fatalf("expected focus on the background child, got view=%v child=%q", m.subagents.view, m.subagents.child)
	}
	got := stripANSI([]byte(m.View().Content))
	assertFitsViewport(t, got, m.width)
	compareGolden(t, "subagent_focus_background.golden", got)
}

// TestSubagentFocusBoundedPreviewsNoteHangsInFinalCard guards the nested explanatory
// note at the golden fixture's real width: its continuation must not return to the
// parent detail lane.
func TestSubagentFocusBoundedPreviewsNoteHangsInFinalCard(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = goldenBackgroundFleet(m)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)

	out := stripANSIstr(m.View().Content)
	lines := strings.Split(out, "\n")
	parent := -1
	for i, line := range lines {
		if strings.Contains(line, "bounded previews") {
			parent = i
			break
		}
	}
	if parent < 0 || parent+1 >= len(lines) {
		t.Fatalf("missing wrapped bounded-previews note:\n%s", out)
	}
	cardIndent := func(line string) int {
		t.Helper()
		content, ok := strings.CutPrefix(strings.TrimLeft(line, " "), "┃")
		if !ok {
			t.Fatalf("expected card row, got %q", line)
		}
		return len(content) - len(strings.TrimLeft(content, " "))
	}
	parentIndent := cardIndent(lines[parent])
	continuationIndent := cardIndent(lines[parent+1])
	if continuationIndent <= parentIndent {
		t.Errorf("bounded-previews continuation indent = %d, want > parent indent %d:\n%s", continuationIndent, parentIndent, out)
	}
}

// TestSubagentFocusBackgroundNoteHangsInFinalCard verifies the delivery note wraps in
// the final centred card with continuation rows deeper than its parent lane.
func TestSubagentFocusBackgroundNoteHangsInFinalCard(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = goldenBackgroundFleet(m)
	m = applyAll(m, tea.WindowSizeMsg{Width: 52, Height: 40})
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)

	out := stripANSIstr(m.View().Content)
	assertFitsViewport(t, []byte(out), m.width)
	lines := strings.Split(out, "\n")
	parent := -1
	for i, line := range lines {
		if strings.Contains(line, "background: runs detached") {
			parent = i
			break
		}
	}
	if parent < 0 || parent+1 >= len(lines) {
		t.Fatalf("missing wrapped background note:\n%s", out)
	}
	var noteLines, noteRows []string
	for _, line := range lines[parent:] {
		content, ok := strings.CutPrefix(strings.TrimLeft(line, " "), "┃")
		content = strings.TrimSpace(strings.Trim(content, "┃ "))
		if !ok || content == "" {
			break
		}
		noteRows = append(noteRows, line)
		noteLines = append(noteLines, content)
	}
	note := strings.Join(noteLines, " ")
	if !strings.Contains(note, "background: runs detached; the agent collects its result via SubagentStatus") {
		t.Fatalf("background note lost content: %q\n%s", note, out)
	}
	laneIndent := func(line string) int {
		t.Helper()
		content, ok := strings.CutPrefix(strings.TrimLeft(line, " "), "┃")
		if !ok {
			t.Fatalf("expected card row, got %q", line)
		}
		return len(content) - len(strings.TrimLeft(content, " "))
	}
	parentIndent := laneIndent(noteRows[0])
	for i, row := range noteRows[1:] {
		if continuationIndent := laneIndent(row); continuationIndent <= parentIndent {
			t.Errorf("background continuation %d indent = %d, want > parent indent %d:\n%s", i+1, continuationIndent, parentIndent, out)
		}
	}
}

// TestAgentsTeamsTabGolden locks the Teams tab of the unified overlay (the tab bar +
// the former team roster), reached by `tab` from the Subagents-default view.
func TestAgentsTeamsTabGolden(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedTeam(m, agentsGoldenTeam)
	m = seedSubagents(m, "p1", startSub("p1", "explorer-a3f1", "audit auth flow"))
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	// The team is not live in this seed; cycle `tab` (now Subagents→Parallel→Teams) until
	// the Teams tab is active to lock its golden regardless of default.
	for i := 0; i < 3 && m.agentsTab != tabTeams; i++ {
		mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		m = mm.(Model)
	}
	if m.agentsTab != tabTeams {
		t.Fatalf("expected Teams tab, got %v", m.agentsTab)
	}
	got := stripANSI([]byte(m.View().Content))
	assertFitsViewport(t, got, m.width)
	compareGolden(t, "agents_teams_tab.golden", got)
}

// goldenParallel builds a representative finished Parallel run for the overlay goldens: a
// judge join over three branches where branch-2 (a non-zero index) wins, exercising the
// ◐/✓/✗ branch glyphs, the winner highlight, and the preserved fork path.
func goldenParallel(m Model) Model {
	return seedParallel(m, "par-1",
		startPar("par-1", "judge", 3),
		branchStartPar("par-1", 0, "branch-1", "refactor with a map"),
		branchStartPar("par-1", 1, "branch-2", "refactor with a slice"),
		branchStartPar("par-1", 2, "branch-3", "refactor inline"),
		branchToolPar("par-1", 0, "Edit", false, 2),
		branchToolPar("par-1", 1, "Edit", false, 3),
		branchToolParPreview("par-1", 1, "tool.call", "Edit", "file: svc.go", 3),
		branchToolParPreview("par-1", 1, "message.delta", "", "slice approach is cleaner", 3),
		branchToolPar("par-1", 2, "Shell", true, 1),
		branchEndPar("par-1", 0, 12000, 3000, 2, "end_turn", false, "/fork/branch-1"),
		branchEndPar("par-1", 1, 15000, 4200, 3, "end_turn", false, "/fork/branch-2"),
		branchEndPar("par-1", 2, 4000, 600, 1, "error", true, "/fork/branch-3"),
		endPar("par-1", "judge", 3, 1, "/fork/branch-2", "end_turn"),
	)
}

// TestParallelRosterGolden locks the Parallel-tab group roster overlay.
func TestParallelRosterGolden(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = goldenParallel(m)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	if m.agentsTab != tabParallel {
		t.Fatalf("expected Parallel tab, got %v", m.agentsTab)
	}
	got := stripANSI([]byte(m.View().Content))
	assertFitsViewport(t, got, m.width)
	compareGolden(t, "parallel_roster.golden", got)
}

// TestParallelRosterFooterPacksSemanticSegmentsInFinalCard verifies the actual
// golden viewport keeps complete high-priority actions on one footer row.
func TestParallelRosterFooterPacksSemanticSegmentsInFinalCard(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = goldenParallel(m)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	out := stripANSIstr(m.View().Content)
	assertFitsViewport(t, []byte(out), m.width)
	for _, want := range []string{"esc close", "↑/↓ select", "enter focus", "tab switch"} {
		if !strings.Contains(out, want) {
			t.Errorf("parallel roster footer omitted high-priority segment %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "esc ...") {
		t.Errorf("parallel roster footer split a semantic segment:\n%s", out)
	}
}

// TestParallelRosterFooterLongReboundLabelsInFinalCard verifies the final card
// omits whole lower-priority segments rather than splitting rebound labels.
func TestParallelRosterFooterLongReboundLabelsInFinalCard(t *testing.T) {
	const width = 48
	long := strings.Repeat("rebound-key-label-", 8)
	hk := defaultHelpKeys()
	hk.closeOnly, hk.navUp, hk.navDown = long+"close", long+"up", long+"down"
	hk.choose, hk.nextTab = long+"focus", long+"switch"
	hk.scroll, hk.jumpTopFull, hk.jumpEndFull = long+"page", long+"first", long+"last"
	out := stripANSIstr(renderAgentsOverlay(aztec(), tabParallel, subagentState{}, parallelState{}, teamState{view: teamRoster}, nil, nil, []parallelGroup{{parentCallID: "p1", branchCount: 1}}, hk, width, 20))
	assertFitsViewport(t, []byte(out), width)
	if !strings.Contains(out, "...") {
		t.Fatalf("long rebound footer should signal omitted segments:\n%s", out)
	}
	for _, fragment := range []string{"rebound-key-label-...", "rebound-key-label-…"} {
		if strings.Contains(out, fragment) {
			t.Errorf("footer split rebound key/action label %q:\n%s", fragment, out)
		}
	}
}

// TestParallelGroupFocusGolden locks one Parallel group's focus pane (the branches inline,
// the winner highlight, the preserved fork path, the context-isolation note).
func TestParallelGroupFocusGolden(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = goldenParallel(m)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.parallel.view != parallelGroupView {
		t.Fatalf("expected parallelGroupView, got %v", m.parallel.view)
	}
	got := stripANSI([]byte(m.View().Content))
	assertFitsViewport(t, got, m.width)
	compareGolden(t, "parallel_group_focus.golden", got)
}

// TestFooterParallelGolden locks the footer when a Parallel run is present, alongside the
// fleet segment, across tiers (a Parallel run + a subagent fleet both advertised).
func TestFooterParallelGolden(t *testing.T) {
	left := aztec().Style("muted").Render("connected")

	parOnly := goldenParallel(newMCPModel(t, aztec(), nil))
	parPlusSub := seedSubagents(goldenParallel(newMCPModel(t, aztec(), nil)), "s1",
		startSub("s1", "c1", "audit auth"))

	var b strings.Builder
	b.WriteString("parallel only (judge, 3 done):\n")
	b.WriteString(stripANSIstr(parOnly.fitFooter(left, 160)) + "\n\n")
	b.WriteString("parallel + subagent fleet:\n")
	b.WriteString(stripANSIstr(parPlusSub.fitFooter(left, 160)) + "\n\n")
	b.WriteString("parallel + sub, narrow (medium tier):\n")
	b.WriteString(stripANSIstr(parPlusSub.fitFooter(left, 70)) + "\n")
	compareGolden(t, "footer_parallel.golden", []byte(b.String()))
}

// TestParallelFooterTiers asserts the Parallel footer segment renders at each tier and the
// counts are correct, mirroring the subagent fleet footer tier test.
func TestParallelFooterTiers(t *testing.T) {
	th := aztec()
	full := stripANSIstr(parallelFooterFull(th, 1, 2, defaultHelpKeys().agents))
	medium := stripANSIstr(parallelFooterMedium(1, 2, defaultHelpKeys().agents))
	compact := stripANSIstr(parallelFooterCompact(1, 2))
	if !strings.Contains(full, "parallel") || !strings.Contains(full, "f6") {
		t.Errorf("full tier should name parallel + f6: %q", full)
	}
	for _, s := range []string{full, medium, compact} {
		if !strings.Contains(s, "1◐") || !strings.Contains(s, "2✓") {
			t.Errorf("tier missing running/done counts: %q", s)
		}
	}
	if strings.Contains(compact, "f6") {
		t.Errorf("compact tier should drop f6: %q", compact)
	}
}

// TestAgentsOverlayNothingRanHint asserts f6 with neither a team nor subagents
// surfaces the honest "nothing ran" hint and does NOT open the overlay.
func TestAgentsOverlayNothingRanHint(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m.caps.Teams = true
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	if m.team.view != teamNone {
		t.Errorf("f6 with nothing running should not open the overlay, view = %v", m.team.view)
	}
	if !strings.Contains(m.statusMsg, "no team, subagent, or parallel run has run yet") {
		t.Errorf("expected the nothing-ran hint, got %q", m.statusMsg)
	}
}

func TestMecatuiAgentsOverlayFit_Scenario1_SelectedRowsUseSessionsTreatment(t *testing.T) {
	th, hk := aztec(), defaultHelpKeys()
	assertRows := func(name, out, selected, unselected string) {
		t.Helper()
		accentOpen, _, _ := strings.Cut(th.Style("spinner").Render("x"), "x")
		if !strings.Contains(out, accentOpen+"▶ "+selected) {
			t.Errorf("%s selected row lacks unbordered accent treatment: %q", name, out)
		}
		plain := stripANSIstr(out)
		if !strings.Contains(plain, "  "+unselected) {
			t.Errorf("%s unselected row lost two-cell prefix: %q", name, plain)
		}
	}

	assertRows("subagent", renderSubagentRoster(th, subagentState{}, []subagentLane{{childID: "one", goal: "selected"}, {childID: "two", goal: "unselected"}}, hk, 0, 120), "◐ selected", "◐ unselected")
	assertRows("parallel group", renderParallelRoster(th, parallelState{}, []parallelGroup{{parentCallID: "one", branchCount: 1}, {parentCallID: "two", branchCount: 1}}, hk, 0, 120), "◐ all", "◐ all")
	assertRows("team", renderTeamRoster(th, teamState{}, &block{teamLanes: []teamLane{{name: "selected"}, {name: "unselected"}}}, hk, 0, 120), "◆ · selected", "◆ · unselected")

	branches := []parallelBranch{{index: 0, label: "selected"}, {index: 1, label: "unselected"}}
	assertRows("parallel branch", renderParallelBranchRow(th, &branches[0], -1, true, 120)+renderParallelBranchRow(th, &branches[1], -1, false, 120), "◐ selected", "◐ unselected")

	accentOpen, _, _ := strings.Cut(th.Style("spinner").Render("x"), "x")
	finalViews := []struct {
		name  string
		setup func(Model) Model
		want  string
	}{
		{"subagent", func(m Model) Model {
			m.team.view, m.agentsTab = teamRoster, tabSubagents
			m.conv.subagentFleet = []subagentLane{{childID: "one", goal: "selected"}, {childID: "two", goal: "unselected"}}
			return m
		}, "▶ ◐ selected"},
		{"parallel group", func(m Model) Model {
			m.team.view, m.agentsTab = teamRoster, tabParallel
			m.conv.parallelGroups = []parallelGroup{{parentCallID: "one", branchCount: 1}, {parentCallID: "two", branchCount: 1}}
			return m
		}, "▶ ◐ all"},
		{"parallel branch", func(m Model) Model {
			m.team.view, m.agentsTab = teamRoster, tabParallel
			m.parallel = parallelState{view: parallelGroupView, group: "one"}
			m.conv.parallelGroups = []parallelGroup{{parentCallID: "one", winner: -1, branches: branches}}
			return m
		}, "▶ ◐ selected"},
		{"team", func(m Model) Model {
			m.team, m.agentsTab = teamState{view: teamRoster}, tabTeams
			m.conv.blocks = append(m.conv.blocks, block{kind: blockTool, team: true, teamLanes: []teamLane{{name: "selected"}, {name: "unselected"}}})
			return m
		}, "▶ ◆ · selected"},
	}
	for _, tc := range finalViews {
		m := tc.setup(resize(newMCPModel(t, th, nil), 80, 40))
		out := m.View().Content
		plain := stripANSIstr(out)
		if !strings.Contains(plain, tc.want) || !strings.Contains(out, accentOpen+"▶ ") {
			t.Errorf("%s final View lacks selected unbordered accent row %q:\n%s", tc.name, tc.want, plain)
		}
	}
}

func TestMecatuiAgentsOverlayFit_Scenario1_SelectedWrappedRowHasNoButtonChrome(t *testing.T) {
	th := aztec()
	branch := &parallelBranch{index: 0, label: strings.Repeat("long label ", 8)}
	selected := stripANSIstr(renderParallelBranchRow(th, branch, -1, true, 20))
	unselected := stripANSIstr(renderParallelBranchRow(th, branch, -1, false, 20))
	if got, want := strings.Count(selected, "\n"), strings.Count(unselected, "\n"); got != want {
		t.Fatalf("selected wrapped row has %d physical rows, want unselected row's %d: %q", got, want, selected)
	}
	if got, want := strings.Replace(selected, "▶ ", "  ", 1), unselected; got != want {
		t.Fatalf("selected wrapped row differs from unselected beyond its marker (button chrome):\nselected: %q\nunselected: %q", selected, unselected)
	}
	m := resize(newMCPModel(t, th, nil), 32, 40)
	m.team, m.agentsTab = teamState{view: teamRoster}, tabParallel
	m.parallel = parallelState{view: parallelGroupView, group: "wrapped"}
	m.conv.parallelGroups = []parallelGroup{{parentCallID: "wrapped", winner: -1, branches: []parallelBranch{*branch}}}
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "▶ ◐ long label") {
		t.Fatalf("wrapped selected branch did not retain its selection in final View:\n%s", out)
	}
}

func TestMecatuiAgentsOverlayFit_Scenario1_SelectedWinnerRetainsBothMarkers(t *testing.T) {
	th := aztec()
	branch := &parallelBranch{index: 1, label: "winner"}
	out := stripANSIstr(renderParallelBranchRow(th, branch, 1, true, 120))
	if !strings.HasPrefix(out, "▶ ★ ") {
		t.Fatalf("selected winning branch markers = %q, want both ▶ and ★", out)
	}
	bar := stripANSIstr(agentsTabBar(th, tabParallel))
	if !strings.Contains(bar, "▸ Parallel") || strings.Contains(bar, "▶ Parallel") {
		t.Fatalf("active tab marker changed or conflated: %q", bar)
	}
}

// TestMecatuiAgentsOverlayFit_Scenario2_CompactFallbackFitsShortViewport pins the
// short-terminal fallback: it is one unframed, selectable line rather than a clipped card.
func TestMecatuiAgentsOverlayFit_Scenario2_CompactFallbackFitsShortViewport(t *testing.T) {
	type compactCase struct {
		name, want string
		setup      func(Model) Model
	}
	teamBlock := func() block {
		return block{
			kind: blockTool, team: true,
			teamLanes:    []teamLane{{name: "ann"}},
			teamTasks:    []teamTask{{id: "task-focus", state: taskStatePending}},
			teamFindings: []teamFinding{{member: "ann", body: "finding-focus"}},
		}
	}
	cases := []compactCase{
		{"subagent roster", "▶ Subagents · esc close", func(m Model) Model {
			m.team.view, m.agentsTab = teamRoster, tabSubagents
			m.conv.subagentFleet = []subagentLane{{childID: "child-roster"}}
			return m
		}},
		{"subagent empty", "▶ Subagents · esc close", func(m Model) Model { m.team.view, m.agentsTab = teamRoster, tabSubagents; return m }},
		{"subagent focus", "▶ subagent nt-kid · esc back", func(m Model) Model {
			m.team.view, m.agentsTab = teamRoster, tabSubagents
			m.subagents = subagentState{view: subagentFocus, child: "subagent-kid", detail: boundedViewport{offset: 3}}
			m.conv.subagentFleet = []subagentLane{{childID: "subagent-kid"}}
			return m
		}},
		{"subagent missing focus", "▶ subagent t-gone · esc back", func(m Model) Model {
			m.team.view, m.agentsTab = teamRoster, tabSubagents
			m.subagents = subagentState{view: subagentFocus, child: "subagent-gone", detail: boundedViewport{offset: 3}}
			return m
		}},
		{"parallel roster", "▶ Parallel · esc close", func(m Model) Model {
			m.team.view, m.agentsTab = teamRoster, tabParallel
			m.conv.parallelGroups = []parallelGroup{{parentCallID: "group-roster"}}
			return m
		}},
		{"parallel empty", "▶ Parallel · esc close", func(m Model) Model { m.team.view, m.agentsTab = teamRoster, tabParallel; return m }},
		{"parallel group focus", "▶ parallel group-a · esc back", func(m Model) Model {
			m.team.view, m.agentsTab = teamRoster, tabParallel
			m.parallel = parallelState{view: parallelGroupView, group: "group-a", branchCursor: 1}
			m.conv.parallelGroups = []parallelGroup{{parentCallID: "group-a", branches: []parallelBranch{{label: "first"}, {label: "branch-focus"}}}}
			return m
		}},
		{"parallel missing focus", "▶ parallel group-x · esc back", func(m Model) Model {
			m.team.view, m.agentsTab = teamRoster, tabParallel
			m.parallel = parallelState{view: parallelGroupView, group: "group-x"}
			return m
		}},
		{"team roster", "▶ Teams · esc close", func(m Model) Model {
			b := teamBlock()
			m.conv.blocks = append(m.conv.blocks, b)
			m.team, m.agentsTab = teamState{view: teamRoster}, tabTeams
			return m
		}},
		{"team empty", "▶ Teams · esc close", func(m Model) Model { m.team, m.agentsTab = teamState{view: teamRoster}, tabTeams; return m }},
		{"team focus", "▶ agent ann · esc back", func(m Model) Model {
			b := teamBlock()
			m.conv.blocks = append(m.conv.blocks, b)
			m.team, m.agentsTab = teamState{view: teamFocus, member: "ann", detail: boundedViewport{offset: 3}}, tabTeams
			return m
		}},
		{"team missing focus", "▶ agent bob · esc back", func(m Model) Model {
			b := teamBlock()
			m.conv.blocks = append(m.conv.blocks, b)
			m.team, m.agentsTab = teamState{view: teamFocus, member: "bob", detail: boundedViewport{offset: 3}}, tabTeams
			return m
		}},
		{"team tasks", "▶ Tasks · esc back", func(m Model) Model {
			b := teamBlock()
			m.conv.blocks = append(m.conv.blocks, b)
			m.team, m.agentsTab = teamState{view: teamTasks, detail: boundedViewport{offset: 3}}, tabTeams
			return m
		}},
		{"team tasks empty", "▶ Tasks · esc back", func(m Model) Model {
			b := teamBlock()
			b.teamTasks = nil
			m.conv.blocks = append(m.conv.blocks, b)
			m.team, m.agentsTab = teamState{view: teamTasks}, tabTeams
			return m
		}},
		{"team findings", "▶ Findings · esc back", func(m Model) Model {
			b := teamBlock()
			m.conv.blocks = append(m.conv.blocks, b)
			m.team, m.agentsTab = teamState{view: teamFindings, detail: boundedViewport{offset: 3}}, tabTeams
			return m
		}},
		{"team findings empty", "▶ Findings · esc back", func(m Model) Model {
			b := teamBlock()
			b.teamFindings = nil
			m.conv.blocks = append(m.conv.blocks, b)
			m.team, m.agentsTab = teamState{view: teamFindings}, tabTeams
			return m
		}},
	}
	for height := 1; height <= 23; height++ {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("h%d/%s", height, tc.name), func(t *testing.T) {
				m := tc.setup(resize(newMCPModel(t, aztec(), nil), 32, height))
				before := []int{m.subagents.cursor, m.subagents.detail.offset, m.parallel.cursor, m.parallel.branchCursor, m.team.cursor, m.team.detail.offset}
				_ = m.View() // exercise the final view assembly before inspecting its overlay region.
				body := stripANSIstr(m.renderBody())
				if got := lipgloss.Height(body); got != 1 {
					t.Fatalf("compact body height=%d, want 1: %q", got, body)
				}
				if got := lipgloss.Width(body); got > 32 {
					t.Fatalf("compact body width=%d, want <=32: %q", got, body)
				}
				if !strings.Contains(body, tc.want) {
					t.Fatalf("compact body=%q, want %q", body, tc.want)
				}
				mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
				m = mm.(Model)
				after := []int{m.subagents.cursor, m.subagents.detail.offset, m.parallel.cursor, m.parallel.branchCursor, m.team.cursor, m.team.detail.offset}
				if !slices.Equal(before, after) {
					t.Fatalf("compact navigation changed state: before=%v after=%v", before, after)
				}
			})
		}
	}
}

// TestMecatuiAgentsOverlayFit_Scenario2_AllSubviewsFitViewport checks that normal
// overlays budget their framed physical lines, including when the viewport is shorter
// than the terminal that selected normal mode.
func TestMecatuiAgentsOverlayFit_Scenario2_AllSubviewsFitViewport(t *testing.T) {
	type normalCase struct {
		name, want, footer string
		setup              func(Model) Model
	}
	trace := make([]teamTrace, 20)
	tasks := make([]teamTask, 20)
	findings := make([]teamFinding, 20)
	for i := range trace {
		text := fmt.Sprintf("detail-%02d %s", i, strings.Repeat("wrapped ", 8))
		trace[i] = teamTrace{kind: teamTraceMessage, text: text}
		tasks[i] = teamTask{id: fmt.Sprintf("task-%02d-%s", i, strings.Repeat("wide", 8)), state: taskStatePending}
		findings[i] = teamFinding{member: "ann", body: fmt.Sprintf("finding-%02d-%s", i, strings.Repeat("wide", 8))}
	}
	teamBlock := func() block {
		return block{kind: blockTool, team: true, teamLanes: []teamLane{{name: "ann", role: strings.Repeat("role ", 12), trace: trace}}, teamTasks: tasks, teamFindings: findings}
	}
	cases := []normalCase{
		{"subagent roster", "▶ ◐ selected", "esc close|↑/↓ select|lines 1", func(m Model) Model {
			m.team.view, m.agentsTab = teamRoster, tabSubagents
			m.conv.subagentFleet = []subagentLane{{childID: "subagent-selected", goal: "selected-subagent " + strings.Repeat("goal ", 12)}, {childID: "other", goal: "other"}}
			return m
		}},
		{"subagent focus", "selected", "esc back|lines 1", func(m Model) Model {
			m.team.view, m.agentsTab = teamRoster, tabSubagents
			m.subagents = subagentState{view: subagentFocus, child: "subagent-selected"}
			m.conv.subagentFleet = []subagentLane{{childID: "subagent-selected", goal: "selected-subagent", trace: trace, done: true, stop: stopError, cause: strings.Repeat("failure ", 30), background: true}}
			return m
		}},
		{"subagent missing focus", "subagent #issing", "esc back|lines 1", func(m Model) Model {
			m.team.view, m.agentsTab = teamRoster, tabSubagents
			m.subagents = subagentState{view: subagentFocus, child: "missing"}
			return m
		}},
		{"subagent empty", "(no entries)|(no subagents)", "esc close|↑/↓ select|lines 1", func(m Model) Model { m.team.view, m.agentsTab = teamRoster, tabSubagents; return m }},
		{"parallel roster", "judge", "esc close|↑/↓ select|lines 1", func(m Model) Model {
			m.team.view, m.agentsTab = teamRoster, tabParallel
			m.conv.parallelGroups = []parallelGroup{{parentCallID: "p", join: "judge", branchCount: 2, branches: []parallelBranch{{index: 0, label: "selected-branch"}}}}
			return m
		}},
		{"parallel focus", "selected-branch", "esc back|lines 1", func(m Model) Model {
			m.team.view, m.agentsTab = teamRoster, tabParallel
			m.parallel = parallelState{view: parallelGroupView, group: "p"}
			m.conv.parallelGroups = []parallelGroup{{parentCallID: "p", join: "judge", branchCount: 2, branches: []parallelBranch{{index: 0, label: "selected-branch", goal: strings.Repeat("goal ", 12), trace: trace}}}}
			return m
		}},
		{"parallel missing focus", "this parallel run", "esc back|lines 1", func(m Model) Model {
			m.team.view, m.agentsTab = teamRoster, tabParallel
			m.parallel = parallelState{view: parallelGroupView, group: "missing"}
			return m
		}},
		{"parallel empty", "(no entries)|(no parallel runs)", "esc close|↑/↓ select|lines 1", func(m Model) Model { m.team.view, m.agentsTab = teamRoster, tabParallel; return m }},
		{"team roster", "ann", "esc close|↑/↓ select|lines 1", func(m Model) Model {
			b := teamBlock()
			m.conv.blocks = append(m.conv.blocks, b)
			m.team, m.agentsTab = teamState{view: teamRoster}, tabTeams
			return m
		}},
		{"team focus", "ann", "esc back|lines 1", func(m Model) Model {
			b := teamBlock()
			m.conv.blocks = append(m.conv.blocks, b)
			m.team, m.agentsTab = teamState{view: teamFocus, member: "ann"}, tabTeams
			return m
		}},
		{"team missing focus", "member missing", "esc back|lines 1", func(m Model) Model {
			b := teamBlock()
			m.conv.blocks = append(m.conv.blocks, b)
			m.team, m.agentsTab = teamState{view: teamFocus, member: "missing"}, tabTeams
			return m
		}},
		{"team tasks", "task-00", "esc close|lines 1|t roster", func(m Model) Model {
			b := teamBlock()
			m.conv.blocks = append(m.conv.blocks, b)
			m.team, m.agentsTab = teamState{view: teamTasks}, tabTeams
			return m
		}},
		{"team tasks empty", "(no entries)|(no tasks)", "esc close|↑/↓ select|lines 1", func(m Model) Model {
			b := teamBlock()
			b.teamTasks = nil
			m.conv.blocks = append(m.conv.blocks, b)
			m.team, m.agentsTab = teamState{view: teamTasks}, tabTeams
			return m
		}},
		{"team findings", "finding-00", "esc close|lines 1|f roster", func(m Model) Model {
			b := teamBlock()
			m.conv.blocks = append(m.conv.blocks, b)
			m.team, m.agentsTab = teamState{view: teamFindings}, tabTeams
			return m
		}},
		{"team findings empty", "(no entries)|(no findings)", "esc close|↑/↓ select|lines 1", func(m Model) Model {
			b := teamBlock()
			b.teamFindings = nil
			m.conv.blocks = append(m.conv.blocks, b)
			m.team, m.agentsTab = teamState{view: teamFindings}, tabTeams
			return m
		}},
		{"team empty", "no team has run", "esc close|↑/↓ select|lines 1", func(m Model) Model { m.team, m.agentsTab = teamState{view: teamRoster}, tabTeams; return m }},
	}
	containsAny := func(s, alternatives string) bool {
		for _, alternative := range strings.Split(alternatives, "|") {
			if strings.Contains(s, alternative) {
				return true
			}
		}
		return false
	}
	for _, width := range []int{32, 80, 120} {
		for height := 24; height <= 80; height++ {
			for _, tc := range cases {
				m := tc.setup(resize(newMCPModel(t, aztec(), nil), width, height))
				view := stripANSIstr(m.View().Content)
				body := stripANSIstr(m.renderBody())
				if got := lipgloss.Height(body); got > m.vp.Height() {
					t.Fatalf("%s %dx%d: body=%d exceeds offered viewport=%d:\n%s", tc.name, width, height, got, m.vp.Height(), body)
				}
				wantVisible := strings.Contains(view, "vp short")
				for _, want := range strings.Split(tc.want, "|") {
					wantVisible = wantVisible || strings.Contains(view, want)
				}
				if !wantVisible || !containsAny(view, tc.footer) {
					t.Fatalf("%s %dx%d: selected content/footer clipped from final View:\n%s", tc.name, width, height, view)
				}
				for i, line := range strings.Split(body, "\n") {
					if got := lipgloss.Width(line); got > width {
						t.Fatalf("%s %dx%d line %d width=%d: %q", tc.name, width, height, i, got, line)
					}
				}
			}
		}
	}
}

// TestMecatuiAgentsOverlayFit_Scenario2_RosterPagingMatchesRenderedWindow drives
// every selectable Agents view through the real Model.Update/View path. Narrow,
// variably wrapped rows make a logical-row page size observably wrong.
func TestMecatuiAgentsOverlayFit_Scenario2_RosterPagingMatchesRenderedWindow(t *testing.T) {
	const total = 12
	type rosterCase struct {
		name     string
		labels   []string
		seed     func(Model) Model
		cursor   func(Model) int
		cancelID func(int) string
	}
	labels := func(prefix string) []string {
		out := make([]string, total)
		for i := range out {
			out[i] = fmt.Sprintf("%s%02d", prefix, i)
		}
		return out
	}
	cases := []rosterCase{
		{name: "subagent roster", labels: labels("S"), seed: func(m Model) Model {
			m.conv.subagentFleet = make([]subagentLane, total)
			for i := range m.conv.subagentFleet {
				m.conv.subagentFleet[i] = subagentLane{childID: fmt.Sprintf("child-%02d", i), goal: fmt.Sprintf("S%02d long wrapped goal", i), current: "very-long-tool-name"}
			}
			m.team, m.agentsTab = teamState{view: teamRoster}, tabSubagents
			return m
		}, cursor: func(m Model) int { return m.subagents.cursor }, cancelID: func(i int) string { return fmt.Sprintf("child-%02d", i) }},
		{name: "parallel group roster", labels: labels("G"), seed: func(m Model) Model {
			m.conv.parallelGroups = make([]parallelGroup, total)
			for i := range m.conv.parallelGroups {
				m.conv.parallelGroups[i] = parallelGroup{parentCallID: fmt.Sprintf("group-%02d", i), done: true, winner: 0, branchCount: 1, branches: []parallelBranch{{index: 0, label: fmt.Sprintf("G%02d-long-winner-label", i), done: true}}}
			}
			m.team, m.agentsTab = teamState{view: teamRoster}, tabParallel
			return m
		}, cursor: func(m Model) int { return m.parallel.cursor }},
		{name: "team roster", labels: labels("T"), seed: func(m Model) Model {
			m.conv.addTool("t1", "Team", `{}`)
			members := make([]client.TeamMemberSpec, total)
			for i := range members {
				members[i] = client.TeamMemberSpec{Name: fmt.Sprintf("T%02d-long-member", i), Role: "long wrapped worker role"}
			}
			m.conv.setTeamStart("t1", "", members)
			for i := range m.conv.blocks[0].teamLanes {
				m.conv.blocks[0].teamLanes[i].sessionID = fmt.Sprintf("team-child-%02d", i)
			}
			m.team, m.agentsTab = teamState{view: teamRoster}, tabTeams
			return m
		}, cursor: func(m Model) int { return m.team.cursor }, cancelID: func(i int) string { return fmt.Sprintf("team-child-%02d", i) }},
		{name: "focused parallel branches", labels: labels("B"), seed: func(m Model) Model {
			branches := make([]parallelBranch, total)
			for i := range branches {
				branches[i] = parallelBranch{index: i, childID: fmt.Sprintf("branch-child-%02d", i), label: fmt.Sprintf("B%02d-long-branch-label", i), goal: "long wrapped branch goal"}
			}
			m.conv.parallelGroups = []parallelGroup{{parentCallID: "focused", branchCount: total, branches: branches, winner: -1}}
			m.team, m.agentsTab = teamState{view: teamRoster}, tabParallel
			m.parallel = parallelState{view: parallelGroupView, group: "focused"}
			return m
		}, cursor: func(m Model) int { return m.parallel.branchCursor }, cancelID: func(i int) string { return fmt.Sprintf("branch-child-%02d", i) }},
	}
	press := func(t *testing.T, m Model, code rune) Model {
		t.Helper()
		mm, _ := m.Update(tea.KeyPressMsg{Code: code, Text: string(code)})
		return mm.(Model)
	}
	visible := func(out string, labels []string) int {
		n := 0
		for _, label := range labels {
			if strings.Contains(out, label) {
				n++
			}
		}
		return n
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := resize(newMCPModel(t, aztec(), nil), 32, 40)
			send := &fakeSender{}
			m.stream = client.NewStream(nil, send)
			m = tc.seed(m)
			m.keys = applyKeyOverrides(m.keys, map[string][]string{
				"Up": {"u"}, "Down": {"d"}, "ScrollU": {"p"}, "ScrollD": {"n"},
				"JumpTop": {"h"}, "JumpEnd": {"e"},
			})
			first := stripANSIstr(m.View().Content)
			page := visible(first, tc.labels)
			if page < 1 || page >= total {
				t.Fatalf("test premise: initial physical window contains %d entries:\n%s", page, first)
			}
			m = press(t, m, 'n')
			selected := tc.cursor(m)
			if selected <= 0 || selected >= total {
				t.Fatalf("custom Page Down cursor = %d, want a later bounded item; initial:\n%s", selected, first)
			}
			paged := stripANSIstr(m.View().Content)
			if !strings.Contains(paged, "▶") || !strings.Contains(paged, tc.labels[selected]) {
				t.Fatalf("paged selection is not wholly visible:\n%s", paged)
			}
			if tc.cancelID != nil {
				mm, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
				m = mm.(Model)
				runCmd(cmd)
				if got, want := cancelChildFrames(send), tc.cancelID(selected); len(got) != 1 || got[0] != want {
					t.Fatalf("cancel after paging targeted %v, want [%s]", got, want)
				}
				m.statusMsg = ""
			}
			m = press(t, m, 'd')
			if got := tc.cursor(m); got != selected+1 {
				t.Fatalf("custom Down cursor = %d, want %d", got, selected+1)
			}
			m = press(t, m, 'u')
			if got := tc.cursor(m); got != selected {
				t.Fatalf("custom Up cursor = %d, want %d", got, selected)
			}
			m = press(t, m, 'e')
			if got := tc.cursor(m); got != total-1 {
				t.Fatalf("custom JumpEnd cursor = %d, want %d", got, total-1)
			}
			m = press(t, m, 'p')
			if got := tc.cursor(m); got >= total-1 || got < 0 {
				t.Fatalf("custom Page Up cursor = %d, want an earlier bounded item", got)
			}
			if out := stripANSIstr(m.View().Content); !strings.Contains(out, "▶") {
				t.Fatalf("Page Up selected segment has no cursor marker:\n%s", out)
			}
			m = press(t, m, 'h')
			if got := tc.cursor(m); got != 0 {
				t.Fatalf("custom JumpTop cursor = %d, want 0", got)
			}
		})
	}
}

// TestMecatuiAgentsOverlayFit_Scenario2_OverflowContentReachable pins the existing
// remappable navigation surface and live range indicator used for overflowed rosters.
func TestMecatuiAgentsOverlayFit_Scenario2_OverflowContentReachable(t *testing.T) {
	testAgentsDetailScrolling(t)
}

// TestMecatuiAgentsOverlayFit_Scenario2_WrappedDynamicContentFitsViewport pins that
// long dynamic roster metadata is charged before framing, leaving the footer visible.
func TestMecatuiAgentsOverlayFit_Scenario2_WrappedDynamicContentFitsViewport(t *testing.T) {
	th := aztec()
	lane := subagentLane{childID: "child", goal: strings.Repeat("unbreakable", 40), current: strings.Repeat("metadata", 40), done: true, stop: stopError, cause: strings.Repeat("failure ", 80), background: true}
	out := stripANSIstr(renderAgentsOverlay(th, tabSubagents, subagentState{view: subagentFocus, child: "child"}, parallelState{}, teamState{}, nil, []subagentLane{lane}, nil, defaultHelpKeys(), 32, 20, 24))
	if got := lipgloss.Height(out); got > 20 {
		t.Fatalf("wrapped dynamic content is %d lines, want <= 20", got)
	}
	if !strings.Contains(out, "esc back") {
		t.Fatalf("footer was hidden by wrapped dynamic content:\n%s", out)
	}

	m := resize(newMCPModel(t, th, nil), 32, 40)
	m.conv.subagentFleet = make([]subagentLane, 8)
	for i := range m.conv.subagentFleet {
		m.conv.subagentFleet[i] = subagentLane{
			childID: fmt.Sprintf("wrapped-%02d", i),
			goal:    fmt.Sprintf("selected-%02d-%s", i, strings.Repeat("unbreakable", 8)),
			current: strings.Repeat("metadata", 8),
		}
	}
	m.team, m.agentsTab = teamState{view: teamRoster}, tabSubagents
	m.subagents.cursor = 4
	listTh, hk, width, height := m.agentsListGeometry()
	list := subagentSelectableList(listTh, m.subagents, m.conv.subagentFleet, hk, width)
	w := list.window(listTh, height)
	if w.start > m.subagents.cursor || w.end <= m.subagents.cursor {
		t.Fatalf("wrapped selected row %d is outside physical window [%d,%d)", m.subagents.cursor, w.start, w.end)
	}
	body := stripANSIstr(list.render(listTh, height))
	selected := stripANSIstr(list.rows[m.subagents.cursor])
	if !strings.Contains(body, selected) {
		t.Fatalf("wrapped selected row was split or cropped:\nwant complete:\n%s\nbody:\n%s", selected, body)
	}
	if got := lipgloss.Height(m.renderBody()); got > m.vp.Height() {
		t.Fatalf("real Model view body with wrapped selectable metadata is %d lines, want <= %d", got, m.vp.Height())
	}
}

func TestAgentsOverlayLayoutBoundaryExactFitAndOneLineShort(t *testing.T) {
	th, hk := aztec(), defaultHelpKeys()
	const width = 80
	// The complete card, tab strip, separator, and empty Teams body fit in
	// eleven rows once all rendered frame rows are charged.
	exact := stripANSIstr(renderAgentsOverlay(th, tabTeams, subagentState{}, parallelState{}, teamState{}, nil, nil, nil, hk, width, 11, 24))
	if !strings.Contains(exact, "┏") || !strings.Contains(exact, "no team has run this session") || !strings.Contains(exact, "esc close") {
		t.Fatalf("exact-fit normal card lost its frame or essential body:\n%s", exact)
	}

	short := stripANSIstr(renderAgentsOverlay(th, tabTeams, subagentState{}, parallelState{}, teamState{}, nil, nil, nil, hk, width, 10, 24))
	if strings.Contains(short, "┏") || !strings.Contains(short, "vp short") || !strings.Contains(short, "esc close") {
		t.Fatalf("one-line-short viewport must use the unframed viewport fallback:\n%s", short)
	}
}

func TestAgentsOverlayLayoutBoundaryChargesWrappedTabStrip(t *testing.T) {
	th, hk := aztec(), defaultHelpKeys()
	const width, height = 32, 12
	out := stripANSIstr(renderAgentsOverlay(th, tabTeams, subagentState{}, parallelState{}, teamState{}, nil, nil, nil, hk, width, height, 24))
	if !strings.Contains(out, "┏") || !strings.Contains(out, "no team has run") || !strings.Contains(out, "esc close") {
		t.Fatalf("wrapped fixed chrome was not charged as complete rows:\n%s", out)
	}
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("line %d width = %d, want <= %d: %q", i, got, width, line)
		}
	}
}

func TestAgentsOverlayLayoutBoundaryTinyConversationViewportUsesDistinctFallback(t *testing.T) {
	th, hk := aztec(), defaultHelpKeys()
	out := stripANSIstr(renderAgentsOverlay(th, tabSubagents, subagentState{}, parallelState{}, teamState{}, nil,
		[]subagentLane{{childID: "child", goal: "audit"}}, nil, hk, 32, 1, 24))
	if strings.Contains(out, "┏") || strings.Contains(out, "▶") || !strings.Contains(out, "vp short") || !strings.Contains(out, "esc close") {
		t.Fatalf("normal-mode tiny viewport fallback must be unframed and distinct from compact mode: %q", out)
	}
	if got := lipgloss.Height(out); got > 1 {
		t.Fatalf("tiny viewport fallback height = %d, want <= 1", got)
	}
}

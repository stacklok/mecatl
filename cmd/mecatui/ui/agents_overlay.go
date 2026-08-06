package ui

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/platform"
)

// agentsTab selects which body the unified ctrl+a "agents" overlay renders. The
// overlay is ONE surface with two tabs — Subagents (the flat Subagent-child fleet) and
// Teams (the in-process agent-team roster) — matching the field's "one consolidated
// agents window" convergence (Cursor's Agents Window, Claude Code's Agent View). The
// container's open/closed flag and the Teams-tab state still live on m.team
// (teamState) — the existing, tested team overlay becomes the Teams tab verbatim — so
// `m.team.view != teamNone` remains the single "overlay is open" predicate. The
// Subagents-tab state lives on m.subagents (subagentState).
type agentsTab int

const (
	tabSubagents agentsTab = iota // the flat Subagent-child fleet roster + per-child focus
	tabParallel                   // the Parallel fork-join GROUP roster + per-group (branch) focus
	tabTeams                      // the agent-team roster + per-member focus (the former team overlay)
)

// parallelView is the active Parallel-tab sub-view: the GROUP roster (one row per Parallel
// call) or one focused group showing its branches inline (ONE level — plan Q4). A Parallel
// run is a fan-out group, so the focus shows ALL branches of a group at once (winner
// highlighted) rather than drilling into a single branch.
type parallelView int

const (
	parallelRoster    parallelView = iota // the group roster (one row per Parallel call)
	parallelGroupView                     // one focused group's branches inline (winner highlighted)
)

// parallelState holds the Parallel-tab overlay state on the Model. Like subagentState it
// is value-embedded and holds only a sub-view, a selection cursor, and the focused group's
// ParentCallID (never a copy of the groups — the panel reads the live groups off the
// conversation each render). Focus is keyed by ParentCallID (not index) so a group list
// that grows under the overlay can't shift focus onto the wrong group.
type parallelState struct {
	view   parallelView
	cursor int    // selected row in the group roster (index into the group order)
	group  string // the focused group's ParentCallID (parallelGroupView)
	// branchCursor is the selected BRANCH row inside the focused group (an index into
	// the by-index render order, branchesByIndex) — the selection the `x` cancel key
	// addresses. Reset on focus enter/exit.
	branchCursor int
}

// subagentView is the active Subagents-tab sub-view (parallel to teamView): the flat
// fleet roster or one focused child's redacted chip trace. There is no none state —
// the tab is only reachable while the container (m.team.view) is open; the container's
// open flag owns "closed".
type subagentView int

const (
	subagentRoster subagentView = iota // the flat fleet roster (one row per child)
	subagentFocus                      // one selected child's redacted tool-chip trace
)

// subagentState holds the Subagents-tab overlay state on the Model. Like teamState it
// is value-embedded and holds only a sub-view, a selection cursor, and the focused
// child's ID (never a copy of the lanes — the panel reads the live fleet off the
// conversation each render). Focus is keyed by ChildID (not index) so a fleet that
// grows under the overlay can't shift focus onto the wrong child.
type subagentState struct {
	view   subagentView
	cursor int    // selected row in the fleet roster (index into the fleet order)
	child  string // the focused child's ChildID (subagentFocus)
}

// openAgents opens the unified ctrl+a agents overlay. It picks the CONTEXT-SENSITIVE
// default tab: Teams when a team is live (the team is the richer, watch-worthy
// surface), else Subagents when ≥1 subagent has run, else falls back to the team
// overlay's honest empty-state hint (so "teams not enabled" vs "nothing running yet"
// still reads). Both tabs are always reachable via `tab` once the overlay is open. It
// opens while idle OR mid-run (the deep view is most useful while agents stream) and
// stays inert under a permission modal / connecting / fatal phase.
func (m Model) openAgents() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle && m.phase != phaseRunning {
		return m, nil
	}
	teamLive := m.conv.liveTeamBlock() != nil
	haveTeam := m.conv.latestTeamBlock() != nil
	haveSub := m.conv.hasSubagents()
	parallelLive := m.conv.liveParallel()
	haveParallel := m.conv.hasParallel()
	if !haveTeam && !haveSub && !haveParallel {
		// Nothing to show — surface a brief hint rather than opening an empty overlay,
		// distinguishing "teams not enabled on this server" from "nothing has run yet".
		if !m.caps.Teams {
			m.statusMsg = "agent teams are not enabled on this server"
		} else {
			m.statusMsg = "no team, subagent, or parallel run has run yet"
		}
		return m, nil
	}
	m.ta.Blur() // the overlay owns the keyboard while open
	// Context-sensitive default tab (preferredAgentsTab is the single predicate, tested in
	// isolation): the richest LIVE surface wins, else the tab that has content.
	m.agentsTab = m.preferredAgentsTab(teamLive, haveTeam, haveSub, parallelLive, haveParallel)
	m.team = teamState{view: teamRoster} // container open flag (+ Teams-tab state)
	m.subagents = subagentState{view: subagentRoster}
	m.parallel = parallelState{view: parallelRoster}
	return m, nil
}

// preferredAgentsTab is the context-sensitive default-tab predicate (its own function so
// it is testable in isolation). Precedence (plan Q5, "richest live surface wins"):
// teamLive > parallelLive > haveSub > haveParallel > haveTeam > Subagents.
func (Model) preferredAgentsTab(teamLive, haveTeam, haveSub, parallelLive, haveParallel bool) agentsTab {
	switch {
	case teamLive:
		return tabTeams
	case parallelLive:
		return tabParallel
	case haveSub:
		return tabSubagents
	case haveParallel:
		return tabParallel
	case haveTeam:
		return tabTeams
	default:
		return tabSubagents
	}
}

// closeAgents dismisses the overlay and returns focus to the prompt input.
func (m Model) closeAgents() (tea.Model, tea.Cmd) {
	m.team = teamState{}
	m.subagents = subagentState{}
	m.parallel = parallelState{}
	cmd := m.ta.Focus()
	return m, cmd
}

// switchAgentsTab cycles the active tab (Subagents→Parallel→Teams→Subagents) and resets
// the incoming tab's sub-view to its roster, so `tab` is always a clean tab switch (never
// lands mid-focus on another tab). It does NOT close the overlay.
func (m Model) switchAgentsTab() Model {
	switch m.agentsTab {
	case tabSubagents:
		m.agentsTab = tabParallel
		m.parallel.view = parallelRoster
	case tabParallel:
		m.agentsTab = tabTeams
		m.team.view = teamRoster
	default:
		m.agentsTab = tabSubagents
		m.subagents.view = subagentRoster
	}
	return m
}

// onAgentsKey is the unified overlay's key router, installed in onOverlayKey ahead of
// the legacy per-overlay handlers. It owns the container chrome: `tab` switches tabs,
// then it delegates to the active tab's handler (the Teams tab reuses onTeamKey
// verbatim; the Subagents tab uses onSubagentKey). Returns handled=false only when the
// overlay is closed, so the caller falls through to normal key handling.
func (m Model) onAgentsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.team.view == teamNone {
		return m, nil, false
	}
	// `tab` switches tabs from EITHER tab's roster (not mid-focus — a focus pane's esc
	// steps back to its own roster first, matching the team overlay's esc semantics).
	if key.Matches(msg, m.keys.NextTab) && m.atAgentsRoster() {
		return m.switchAgentsTab(), nil, true
	}
	switch m.agentsTab {
	case tabSubagents:
		return m.onSubagentKey(msg)
	case tabParallel:
		return m.onParallelKey(msg)
	default:
		return m.onTeamKey(msg)
	}
}

// atAgentsRoster reports whether the active tab is showing its top-level roster (not a
// focus/sub-view), so `tab` only switches tabs from a roster — a focus pane's esc must
// step back to its own roster first (consistent across all tabs).
func (m Model) atAgentsRoster() bool {
	switch m.agentsTab {
	case tabSubagents:
		return m.subagents.view == subagentRoster
	case tabParallel:
		return m.parallel.view == parallelRoster
	default:
		return m.team.view == teamRoster
	}
}

// onSubagentKey routes keys while the Subagents tab is active. It mirrors onTeamKey:
// esc steps back from focus to the roster, then closes the overlay; the roster handler
// drives selection/focus; x cancels the focused child (non-terminal only). Returns
// handled=true (the overlay owns the keyboard).
func (m Model) onSubagentKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.subagents.view == subagentFocus {
		switch {
		case key.Matches(msg, m.keys.Close):
			m.subagents.view = subagentRoster
			m.subagents.child = ""
		case key.Matches(msg, m.keys.CancelChild):
			ln := findFleetLane(m.conv.subagentFleet, m.subagents.child)
			mm, cmd := m.cancelSubagentLane(ln)
			return mm, cmd, true
		}
		return m, nil, true
	}
	mm, cmd := m.onSubagentRosterKey(msg)
	return mm, cmd, true
}

// cancelSubagentLane sends a CancelChild frame for the given lane's child over the
// run's stream. It is a no-op for a nil or already-done lane (the key is only shown
// for non-terminal lanes; the finished-as-you-pressed race is benign — the server
// ignores a done id). Confirm-less single keypress, matching esc's confirm-less
// whole-run cancel: the cancel is recoverable (the child is persisted + resumable).
// The send is wrapped in a command so a send error surfaces as a StreamErrMsg.
func (m Model) cancelSubagentLane(ln *subagentLane) (tea.Model, tea.Cmd) {
	if ln == nil || ln.done {
		return m, nil
	}
	return m.cancelChildByID(ln.childID, "subagent #"+shortChildID(ln.childID))
}

// cancelChildByID sends a CancelChild frame for one child id (any family — the
// single-handle convention) over the run's stream, with a transient "cancelling …"
// status naming what. It is the shared sender behind the subagent / parallel-branch /
// team-member x keys; an empty id (older server — the lane never learned its handle)
// is a no-op. The send is wrapped in a command so a send error surfaces as a
// StreamErrMsg.
func (m Model) cancelChildByID(childID, what string) (tea.Model, tea.Cmd) {
	if childID == "" {
		return m, nil
	}
	stream := m.stream
	m.statusMsg = m.deps.Theme.Style("muted").Render("cancelling " + what + "…")
	return m, func() tea.Msg {
		if stream == nil {
			return nil
		}
		if err := stream.SendCancelChild(childID); err != nil {
			return client.StreamErrMsg{Err: err}
		}
		return nil
	}
}

// onSubagentRosterKey drives the fleet roster: up/down move the selection, pgup/pgdn
// page it, home/g·end/G jump to first/last, enter focuses the selected child by
// ChildID, x cancels the selected child (non-terminal lanes only), esc closes the
// overlay. It mirrors onTeamRosterKey one-for-one (cursor is the single source of
// truth; the visible window is derived at render time).
func (m Model) onSubagentRosterKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	fleet := m.conv.subagentFleet
	n := len(fleet)
	page := teamRosterRows(m.vp.Height())
	switch {
	case key.Matches(msg, m.keys.Close):
		return m.closeAgents()
	case key.Matches(msg, m.keys.Up):
		m.subagents.cursor = clampCursor(m.subagents.cursor-1, n)
		return m, nil
	case key.Matches(msg, m.keys.Down):
		m.subagents.cursor = clampCursor(m.subagents.cursor+1, n)
		return m, nil
	case key.Matches(msg, m.keys.ScrollU):
		m.subagents.cursor = clampCursor(m.subagents.cursor-page, n)
		return m, nil
	case key.Matches(msg, m.keys.ScrollD):
		m.subagents.cursor = clampCursor(m.subagents.cursor+page, n)
		return m, nil
	case key.Matches(msg, m.keys.JumpTop):
		m.subagents.cursor = 0
		return m, nil
	case key.Matches(msg, m.keys.JumpEnd):
		m.subagents.cursor = clampCursor(n-1, n)
		return m, nil
	case key.Matches(msg, m.keys.Choose):
		if m.subagents.cursor < 0 || m.subagents.cursor >= n {
			return m, nil
		}
		m.subagents.child = fleet[m.subagents.cursor].childID
		m.subagents.view = subagentFocus
		return m, nil
	case key.Matches(msg, m.keys.CancelChild):
		if m.subagents.cursor < 0 || m.subagents.cursor >= n {
			return m, nil
		}
		return m.cancelSubagentLane(&fleet[m.subagents.cursor])
	}
	return m, nil
}

// onParallelKey routes keys while the Parallel tab is active. It mirrors onSubagentKey:
// esc steps back from group focus to the roster, then closes; the roster handler drives
// selection/focus. ONE level of focus (plan Q4) — a focused group shows all its branches
// inline (with a branch SELECTION cursor), so there is no deeper branch focus to step
// back through; `x` cancels the selected non-terminal branch by its child id (D16).
func (m Model) onParallelKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.parallel.view == parallelGroupView {
		mm, cmd := m.onParallelGroupKey(msg)
		return mm, cmd, true
	}
	mm, cmd := m.onParallelRosterKey(msg)
	return mm, cmd, true
}

// onParallelGroupKey drives the focused group's inline branch list: up/down move the
// branch selection (over the by-index render order), x cancels the selected branch
// (non-terminal lanes with a known child id only — the key no-ops otherwise), esc steps
// back to the group roster.
func (m Model) onParallelGroupKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	g := findParallelGroup(m.conv.parallelGroups, m.parallel.group)
	n := 0
	if g != nil {
		n = len(g.branches)
	}
	switch {
	case key.Matches(msg, m.keys.Close):
		m.parallel.view = parallelRoster
		m.parallel.group = ""
		m.parallel.branchCursor = 0
	case key.Matches(msg, m.keys.Up):
		m.parallel.branchCursor = clampCursor(m.parallel.branchCursor-1, n)
	case key.Matches(msg, m.keys.Down):
		m.parallel.branchCursor = clampCursor(m.parallel.branchCursor+1, n)
	case key.Matches(msg, m.keys.CancelChild):
		if g == nil {
			return m, nil
		}
		ordered := branchesByIndex(g.branches)
		cursor := clampCursor(m.parallel.branchCursor, len(ordered))
		if cursor >= len(ordered) {
			return m, nil
		}
		br := &ordered[cursor]
		if br.done {
			return m, nil
		}
		return m.cancelChildByID(br.childID, "branch "+branchHumanLabel(g, br.index))
	}
	return m, nil
}

// onParallelRosterKey drives the Parallel group roster: up/down move the selection,
// pgup/pgdn page it, home/g·end/G jump to first/last, enter focuses the selected group by
// ParentCallID, esc closes the overlay. It mirrors onSubagentRosterKey one-for-one.
func (m Model) onParallelRosterKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	groups := m.conv.parallelGroups
	n := len(groups)
	page := teamRosterRows(m.vp.Height())
	switch {
	case key.Matches(msg, m.keys.Close):
		return m.closeAgents()
	case key.Matches(msg, m.keys.Up):
		m.parallel.cursor = clampCursor(m.parallel.cursor-1, n)
		return m, nil
	case key.Matches(msg, m.keys.Down):
		m.parallel.cursor = clampCursor(m.parallel.cursor+1, n)
		return m, nil
	case key.Matches(msg, m.keys.ScrollU):
		m.parallel.cursor = clampCursor(m.parallel.cursor-page, n)
		return m, nil
	case key.Matches(msg, m.keys.ScrollD):
		m.parallel.cursor = clampCursor(m.parallel.cursor+page, n)
		return m, nil
	case key.Matches(msg, m.keys.JumpTop):
		m.parallel.cursor = 0
		return m, nil
	case key.Matches(msg, m.keys.JumpEnd):
		m.parallel.cursor = clampCursor(n-1, n)
		return m, nil
	case key.Matches(msg, m.keys.Choose):
		if m.parallel.cursor < 0 || m.parallel.cursor >= n {
			return m, nil
		}
		m.parallel.group = groups[m.parallel.cursor].parentCallID
		m.parallel.view = parallelGroupView
		m.parallel.branchCursor = 0
		return m, nil
	}
	return m, nil
}

// renderAgentsOverlay draws the active unified agents overlay centred over the
// conversation region. It prepends a one-line tab bar (Subagents | Parallel | Teams,
// active tab highlighted) above the active tab's body, then frames the whole thing in the
// shared card. The team block may be nil (no team yet) — the Teams tab then shows an
// honest empty note rather than borrowing another tab's body.
func renderAgentsOverlay(th theme.Theme, tab agentsTab, sub subagentState, par parallelState, team teamState, b *block, fleet []subagentLane, groups []parallelGroup, width, height int) string {
	bar := agentsTabBar(th, tab)
	// The body gets the height MINUS the tab bar + its blank line (agentsTabBarLines),
	// so the window math in the tab bodies still keeps the footer hint on-screen.
	bodyHeight := agentsBodyHeight(height)
	var body string
	switch tab {
	case tabSubagents:
		body = renderSubagentTab(th, sub, fleet, width, bodyHeight)
	case tabParallel:
		body = renderParallelTab(th, par, groups, bodyHeight)
	default:
		body = renderTeamsTab(th, team, b, width, bodyHeight)
	}
	return centerCard(th, bar+"\n\n"+body, width, height)
}

// agentsTabBarLines is how many vertical lines the unified overlay's tab strip costs
// (the tab bar itself + the blank line under it). The tab bodies are given the OUTER
// height minus this so their height-window math (teamRosterRows/teamFocusRows/…) keeps
// the footer hint on-screen under the tab bar.
const agentsTabBarLines = 2

// agentsBodyHeight is the height available to the active tab's body: the outer
// overlay height minus the tab bar. A non-positive (unknown) height passes through so
// the bodies' "size unknown → show all rows" path is preserved.
func agentsBodyHeight(height int) int {
	if height <= 0 {
		return height
	}
	return height - agentsTabBarLines
}

// agentsTabBar renders the "Subagents | Parallel | Teams" tab strip: the active tab in the
// title style, the inactive ones in muted. It is glyph-free for label text so it reads
// identically with ANSI stripped; the active tab is distinguished by a leading "▸" marker
// (not colour alone) so a golden's stripANSI still shows which is active.
func agentsTabBar(th theme.Theme, tab agentsTab) string {
	active := th.Style("askTitle")
	muted := th.Style("muted")
	seg := func(t agentsTab, label string) string {
		if tab == t {
			return active.Render("▸ " + label)
		}
		return muted.Render("  " + label)
	}
	return seg(tabSubagents, "Subagents") + muted.Render("  ") +
		seg(tabParallel, "Parallel") + muted.Render("  ") +
		seg(tabTeams, "Teams")
}

// renderTeamsTab renders the Teams tab body — the EXISTING team overlay roster /
// focus / tasks / findings sub-views verbatim, via the team.go renderers. A nil team
// block (no team has run) reads as an honest empty note so the tab is never blank.
// width is the OUTER viewport width, forwarded to the focus pane so its failure line
// can wrap to the card's text budget (see teamFailureLine, mirroring the subagent
// tab's width forwarding); the roster's own lines are all rune-bounded already.
func renderTeamsTab(th theme.Theme, st teamState, b *block, width, height int) string {
	if b == nil {
		muted := th.Style("muted")
		return muted.Render("no team has run this session") + "\n\n" +
			muted.Render("tab switch · esc close")
	}
	switch st.view {
	case teamFocus:
		return renderTeamFocus(th, b, st.member, width, height)
	case teamTasks:
		return renderTeamTasks(th, b, height)
	case teamFindings:
		return renderTeamFindings(th, b, height)
	default:
		return renderTeamRoster(th, st, b, height)
	}
}

// renderSubagentTab renders the Subagents tab body: the flat fleet roster, or one
// focused child's redacted chip trace. width is the OUTER viewport width, forwarded to the
// focus pane so its failure line can wrap to the card's text budget (see
// subagentFailureLine); the roster's own lines are all rune-bounded already.
func renderSubagentTab(th theme.Theme, st subagentState, fleet []subagentLane, width, height int) string {
	if st.view == subagentFocus {
		return renderSubagentFocus(th, fleet, st.child, width, height)
	}
	return renderSubagentRoster(th, st, fleet, height)
}

// renderSubagentRoster renders the flat fleet roster WINDOWED to the available height,
// mirroring renderTeamRoster: a header (running/done counts), the slice of rows that
// fits with the selected row highlighted, "+K above/below" tails, and an always-visible
// footer hint. An empty fleet reads as a muted "(no subagents)". height<=0 shows all.
func renderSubagentRoster(th theme.Theme, st subagentState, fleet []subagentLane, height int) string {
	muted := th.Style("muted")
	var out strings.Builder

	running, done := fleetCounts(fleet)
	out.WriteString(th.Style("askTitle").Render(subagentRosterHeader(running, done)))
	out.WriteString("\n\n")

	if len(fleet) == 0 {
		out.WriteString(muted.Render("(no subagents)"))
		out.WriteString("\n\n" + muted.Render("tab switch · esc close"))
		return out.String()
	}

	cursor := clampCursor(st.cursor, len(fleet))
	start, end, above, below := teamWindow(cursor, len(fleet), teamRosterRows(height))
	if above > 0 {
		out.WriteString(muted.Render(fmt.Sprintf("  · +%d above", above)) + "\n")
	}
	for row := start; row < end; row++ {
		line := subagentRosterLine(&fleet[row])
		if row == cursor {
			out.WriteString(th.Style("askButtonActive").Render("› "+line) + "\n")
		} else {
			out.WriteString(muted.Render("  "+line) + "\n")
		}
	}
	if below > 0 {
		out.WriteString(muted.Render(fmt.Sprintf("  · +%d below", below)) + "\n")
	}

	// SHORTER than the team/parallel roster hint: the extra "x cancel" segment would
	// otherwise push this card past a 100-col terminal (the hint is the card's widest
	// line, so it directly sets the overlay width — the centred card does not wrap).
	// home/g·end/G and pgup/pgdn paging still work; the hint names the primary chords.
	out.WriteString("\n" + muted.Render("↑/↓ select · "+platform.ScrollKeysMarking()+" · home/end · enter focus · x cancel · tab switch · esc close"))
	return out.String()
}

// subagentRosterHeader is the fleet roster title: "subagents · N running · M done".
func subagentRosterHeader(running, done int) string {
	return fmt.Sprintf("subagents · %d running · %d done", running, done)
}

// fleetCounts classifies the fleet slice into (running, done) — the standalone form of
// conversation.subagentFleetCounts, used by the render path which holds only the slice.
func fleetCounts(fleet []subagentLane) (running, done int) {
	return countDone(fleet, func(ln subagentLane) bool { return ln.done })
}

// subagentRosterLine is one fleet row: a state glyph (◐ running / ✓ done / ✗ error),
// the goal label, a short ChildID hash suffix (so two similar goals are unambiguous),
// the background marker (detached children only), the current/last child tool, the
// running tool count, and token usage. It is the Subagents analogue of
// teamRosterLine, holding only redacted metadata.
func subagentRosterLine(ln *subagentLane) string {
	goal := truncate(sanitizeTerminal(ln.goal), maxSubagentGoalLen)
	if goal == "" {
		goal = "subagent"
	}
	marker := ""
	if ln.background {
		marker = " " + subagentBackgroundMarker
	}
	routed := ""
	if r := subagentModelLabel(ln.routedCategory, ln.routedModel, ln.routingReason, ln.model); r != "" {
		routed = " · " + r
	}
	return fmt.Sprintf("%s %s #%s%s%s · %s · %s · ↑%s ↓%s",
		subagentLaneGlyph(ln),
		goal,
		shortChildID(ln.childID),
		marker,
		routed,
		subagentLaneState(ln),
		plural(ln.toolCount, "tool"),
		humanizeTokens(ln.usage.InputTokens),
		humanizeTokens(ln.usage.OutputTokens))
}

// maxSubagentCauseWidth caps how many RUNES of a child's failure cause the focus pane
// renders. It bounds the pane's HEIGHT cost (the cause is word-wrapped, so this is the
// "at most ~2 wrapped lines" budget), NOT its width — width is bounded independently by
// wrapping to the card's text budget, because a rune cap alone cannot know the terminal.
// The server already clamps the field to 400; this is the display-side bound.
const maxSubagentCauseWidth = 160

// subagentFailureLine renders the focus pane's failure block for a child that ended on
// an ERROR-family terminal and carried a cause: "  failed: <cause>", word-wrapped and
// indented to the overlay card's text budget at the given viewport width. It returns ""
// for a running child, a benign terminal, or an errored child whose server did not send a
// cause (an older server, or a StopError with no loop detail) — the pane then reads
// exactly as before. The cause is server-derived harness/provider metadata (never
// child-authored output, so gauntlet #7 holds) and is sanitized like every other
// server-derived string the overlay renders.
//
// WRAPPING IS LOAD-BEARING, not cosmetic. renderSubagentFocus's output is framed by
// centerCard → lipgloss.Place, which CANNOT shrink content: the widest line directly sets
// the card's width, so a single ~170-column line mangles the card border and mis-centres
// the whole overlay on an 80- or 100-col terminal. And an ordinary provider error is that
// long ("upstream connect error or disconnect/reset before headers. reset reason:
// connection termination" is ~95 chars before the prefix). Every peer bound in these
// overlays (maxSubagentGoalLen 24, maxTeamNameWidth 16, maxTaskDescLen 40) is far narrower
// precisely because of this. So the cause goes through the same cardTextWidth +
// indentWrap pair the /skills and /agents inventory panels use — ansi.Wrap breaks
// over-long tokens too, so even a space-free error string cannot overflow. A width of 0
// (unknown size) yields budget 0, which degrades to the bare unwrapped card exactly as
// those panels do — and centerCard does not Place at width 0, so there is nothing to
// overflow.
func subagentFailureLine(ln *subagentLane, width int) string {
	if !ln.done || ln.cause == "" || !subagentStopErrored(ln.stop) {
		return ""
	}
	// Collapse the cause to ONE logical line before clamping: a provider error body is
	// often multi-line, and its own newlines would defeat the width budget below. The
	// server already normalises the field at its emit site, so this is idempotent against a
	// CURRENT peer — it stays because the TUI is a gRPC CLIENT and must not depend on the
	// peer's version for a display bound.
	//
	// It deliberately uses strings.Fields rather than the package `oneLine` helper, which
	// splits on {\n, \r, \t} only: against an OLDER mecated — the only peer this call
	// exists for — that would leave runs of spaces, NBSP and U+2028/U+2029 uncollapsed,
	// i.e. it would no longer provide the bound this comment claims. strings.Fields splits
	// on every unicode.IsSpace, which is what "one logical line" has to mean for an
	// untrusted peer string.
	return indentWrap("failed: "+truncate(sanitizeTerminal(strings.Join(strings.Fields(ln.cause), " ")), maxSubagentCauseWidth), cardTextWidth(width))
}

// subagentBackgroundMarker flags a detached-delivery (background: true) child on its
// fleet roster row and focus header. Like the rest of the lane vocabulary it is a
// glyph-PLUS-text cue (⇢ "moves on without waiting" + the literal "bg") so it reads
// with ANSI stripped, and a STATIC literal so it never forces a per-tick re-render.
const subagentBackgroundMarker = "⇢ bg"

// maxSubagentGoalLen caps how many runes of a child's goal show on a fleet row so a
// long goal can't blow out the row width (the ChildID hash + columns follow it).
const maxSubagentGoalLen = 24

// subagentLaneGlyph is the per-child state glyph (glyph-not-colour): "✗" for a child
// that ended on an ERROR-family terminal (its stop reason maps to a hard error), "✓"
// for a clean/benign DONE, and "◐" for one still in flight. Errored-while-running
// (the last tool errored but the child has not ended) stays "◐" — a transient tool
// error is not a terminal disposition.
func subagentLaneGlyph(ln *subagentLane) string {
	if !ln.done {
		return "◐"
	}
	if subagentStopErrored(ln.stop) {
		return "✗"
	}
	return "✓"
}

// subagentLaneState derives a child's current state label for the fleet row: once
// done, the stop label (done / max-turns / budget / structured-output / error / …);
// while running, the latest child tool name (with a "…" heartbeat) or "working…" when
// none has run yet. The tool name is sanitized (server-derived).
func subagentLaneState(ln *subagentLane) string {
	if ln.done {
		return subagentStopLabel(ln.stop)
	}
	if ln.current != "" {
		return sanitizeTerminal(ln.current) + "…"
	}
	return "working…"
}

// shortChildID renders a stable short suffix of a ChildID for the fleet row, so two
// children with identical goal labels are still visually distinct (F2 §1.4 item 6).
// It takes the LAST up-to-childIDHashLen runes of the id (the id's tail carries the
// per-call discriminator, e.g. "explorer-<callID>"), sanitized.
func shortChildID(id string) string {
	id = sanitizeTerminal(id)
	r := []rune(id)
	if len(r) <= childIDHashLen {
		return id
	}
	return string(r[len(r)-childIDHashLen:])
}

// childIDHashLen is how many trailing runes of a ChildID the fleet row shows as its
// "#<hash>" disambiguator.
const childIDHashLen = 6

// renderSubagentFocus renders ONE child's detail: a header line (glyph + goal +
// current/last tool + count + usage), the bounded-previews honesty note (the
// previews are bounded + scrubbed + client-only per ADR 0079 — gauntlet #7 is about
// the conversation, not the client), and the interleaved child trace in the Team
// focus format (tool chips with bounded previews + capped message lines),
// height-bounded to the rows that fit. A focused ChildID with no matching lane (the
// child vanished — defensive) reads as a muted note. It mirrors renderTeamFocus.
func renderSubagentFocus(th theme.Theme, fleet []subagentLane, child string, width, height int) string {
	muted := th.Style("muted")
	ln := findFleetLane(fleet, child)
	if ln == nil {
		return th.Style("askTitle").Render("subagent") + "\n\n" +
			muted.Render("subagent #"+shortChildID(child)+" is no longer in the fleet") + "\n\n" +
			muted.Render("esc back")
	}

	var out strings.Builder
	goal := truncate(sanitizeTerminal(ln.goal), maxTeamNameWidth*2)
	if goal == "" {
		goal = "subagent"
	}
	out.WriteString(th.Style("askTitle").Render("subagent · " + goal))
	out.WriteString("\n")
	out.WriteString(muted.Render(subagentRosterLine(ln)))
	out.WriteString("\n")
	out.WriteString(muted.Render(indentWrap(boundedPreviewsSubNote, cardTextWidth(width))))
	if fail := subagentFailureLine(ln, width); fail != "" {
		// The ONE place the fleet answers "why did it fail". The inline Subagent card
		// already carries the cause inside the tool result the agent received, but a
		// roster/focus row otherwise shows only "stop:error", and a BACKGROUND child's
		// failure never reaches an inline card at all (issue #319).
		out.WriteString("\n" + muted.Render(fail))
	}
	if ln.background {
		// Honest limitation: the events carry background + done only — whether the
		// AGENT has collected the result (the registry's delivered state) is not on
		// the wire, so the pane states the delivery channel without claiming a state
		// it cannot know.
		note := "  background: runs detached; the agent collects its result via SubagentStatus"
		if ln.done {
			note = "  background: done — result ready for the agent (SubagentStatus)"
		}
		out.WriteString("\n" + muted.Render(note))
	}
	out.WriteString("\n\n")

	r := &renderer{th: th} // width-0 renderer: chips don't wrap, trace renders full
	trace := r.renderTrace(ln.trace)
	if trace == "" {
		out.WriteString(muted.Render("(no activity yet)"))
	} else {
		out.WriteString(capRenderedLines(th, trace, teamFocusRows(height)))
	}

	// The cancel hint is shown only for a NON-terminal child (the key no-ops on a
	// done lane).
	hint := "esc back"
	if !ln.done {
		hint = "x cancel · esc back"
	}
	out.WriteString("\n\n" + muted.Render(hint))
	return out.String()
}

// findFleetLane returns the lane with the given ChildID off the fleet slice, or nil.
func findFleetLane(fleet []subagentLane, child string) *subagentLane {
	for i := range fleet {
		if fleet[i].childID == child {
			return &fleet[i]
		}
	}
	return nil
}

// subagentStopErrored reports whether a child's stop reason maps to a HARD error glyph
// (✗) rather than a benign done (✓). It mirrors subagentStopLabel's error case: error /
// cancelled / max-consecutive-failures / structured-output-retries-exhausted read as
// "✗". The budget / max-turns / max-tools / no-progress terminals are NON-error stops
// (the child still produced a usable partial), so they read as "✓" with the stop label
// naming the cap. An empty / unknown reason is benign.
func subagentStopErrored(stop string) bool {
	switch stop {
	case stopError, teamStopReasonCancelled, "max_consecutive_failures", "structured_output":
		return true
	default:
		return false
	}
}

// --- Parallel tab -------------------------------------------------------------------

// renderParallelTab renders the Parallel tab body: the GROUP roster (one row per Parallel
// call) or one focused group's branches inline.
func renderParallelTab(th theme.Theme, st parallelState, groups []parallelGroup, height int) string {
	if st.view == parallelGroupView {
		return renderParallelGroupFocus(th, st, groups, height)
	}
	return renderParallelRoster(th, st, groups, height)
}

// renderParallelRoster renders the Parallel group roster WINDOWED to the available height,
// mirroring renderSubagentRoster: a header (running/done group counts), the slice of rows
// that fits with the selected row highlighted, "+K above/below" tails, and a footer hint.
// An empty group list reads as a muted "(no parallel runs)". height<=0 shows all.
func renderParallelRoster(th theme.Theme, st parallelState, groups []parallelGroup, height int) string {
	muted := th.Style("muted")
	var out strings.Builder

	running, done := parallelCounts(groups)
	out.WriteString(th.Style("askTitle").Render(fmt.Sprintf("parallel · %d running · %d done", running, done)))
	out.WriteString("\n\n")

	if len(groups) == 0 {
		out.WriteString(muted.Render("(no parallel runs)"))
		out.WriteString("\n\n" + muted.Render("tab switch · esc close"))
		return out.String()
	}

	cursor := clampCursor(st.cursor, len(groups))
	start, end, above, below := teamWindow(cursor, len(groups), teamRosterRows(height))
	if above > 0 {
		out.WriteString(muted.Render(fmt.Sprintf("  · +%d above", above)) + "\n")
	}
	for row := start; row < end; row++ {
		line := parallelRosterLine(&groups[row])
		if row == cursor {
			out.WriteString(th.Style("askButtonActive").Render("› "+line) + "\n")
		} else {
			out.WriteString(muted.Render("  "+line) + "\n")
		}
	}
	if below > 0 {
		out.WriteString(muted.Render(fmt.Sprintf("  · +%d below", below)) + "\n")
	}

	out.WriteString("\n" + muted.Render("↑/↓ select · "+platform.ScrollKeysMarking()+" page · home/g·end/G first/last · enter focus · tab switch · esc close"))
	return out.String()
}

// parallelCounts classifies the group slice into (running, done) — a group is done once
// its parallel.end arrived.
func parallelCounts(groups []parallelGroup) (running, done int) {
	return countDone(groups, func(g parallelGroup) bool { return g.done })
}

// parallelRosterLine is one group row: a state glyph (◐ running / ✓ done), the join
// strategy, the running/total branch tally, and the winner (for first/judge once
// resolved). It holds only redacted run-level metadata.
func parallelRosterLine(g *parallelGroup) string {
	glyph := "◐"
	if g.done {
		glyph = "✓"
	}
	join := g.join
	if join == "" {
		join = "all"
	}
	branchesDone := 0
	for i := range g.branches {
		if g.branches[i].done {
			branchesDone++
		}
	}
	total := g.branchCount
	if total < len(g.branches) {
		total = len(g.branches)
	}
	line := fmt.Sprintf("%s %s · %d/%d branches", glyph, join, branchesDone, total)
	if w := parallelWinnerLabel(g); w != "" {
		line += " · winner " + w
	}
	return line
}

// parallelWinnerLabel renders the winner's branch label once a group resolves a winner
// (join=first/judge). join=all (winner=-1) and an unresolved run return "".
func parallelWinnerLabel(g *parallelGroup) string {
	if g.winner < 0 {
		return ""
	}
	return branchHumanLabel(g, g.winner)
}

// branchHumanLabel returns the human label for a branch index within a group, preferring
// the branch's own label (from branch_start) and falling back to the 1-based "branch-N".
func branchHumanLabel(g *parallelGroup, index int) string {
	for i := range g.branches {
		if g.branches[i].index == index && g.branches[i].label != "" {
			return sanitizeTerminal(g.branches[i].label)
		}
	}
	return fmt.Sprintf("branch-%d", index+1)
}

// renderParallelGroupFocus renders ONE Parallel group's detail (ONE level — plan Q4): a
// header (join + branch tally + run stop), the bounded-previews honesty note (the
// previews are bounded + scrubbed + client-only per ADR 0079 — gauntlet #7 is about the
// conversation, not the client), every branch inline (glyph + label + goal +
// current/last tool + count + usage; the SELECTED row carries the "›" cursor the `x`
// cancel key addresses, the WINNER row a "★") with its interleaved trace in the Team
// focus format below its roster line, and the preserved winner fork path. A focused
// ParentCallID with no matching group reads as a muted note.
func renderParallelGroupFocus(th theme.Theme, st parallelState, groups []parallelGroup, height int) string {
	muted := th.Style("muted")
	g := findParallelGroup(groups, st.group)
	if g == nil {
		return th.Style("askTitle").Render("parallel") + "\n\n" +
			muted.Render("this parallel run is no longer tracked") + "\n\n" +
			muted.Render("esc back")
	}

	var out strings.Builder
	join := g.join
	if join == "" {
		join = "all"
	}
	out.WriteString(th.Style("askTitle").Render("parallel · join=" + join))
	out.WriteString("\n")
	out.WriteString(muted.Render(parallelRosterLine(g)))
	// Run-level stop, focus-only (NOT on the shared parallelRosterLine). Empty-guarded:
	// a join=all run carries no winner-bearing stop by contract, so it renders no line.
	if g.done && g.stop != "" {
		out.WriteString("\n" + muted.Render("run stop: "+subagentStopLabel(g.stop)))
	}
	out.WriteString("\n")
	out.WriteString(muted.Render("  " + boundedPreviewsParNote))
	out.WriteString("\n\n")

	// Branch events arrive concurrently and OUT OF ORDER on the wire (branch-2's events can
	// precede branch-0's), so g.branches is in first-seen order. Render BY INDEX so the
	// roster reads branch-1, branch-2, branch-3 deterministically regardless of arrival.
	ordered := branchesByIndex(g.branches)
	cursor := clampCursor(st.branchCursor, len(ordered))
	rows := teamFocusRows(height)
	used := 0
	cancellable := false
	r := &renderer{th: th} // a width-0 renderer: chips don't wrap, traces render full
	for i := range ordered {
		br := &ordered[i]
		if !br.done && br.childID != "" {
			cancellable = true
		}
		// The branch roster line + its trace block cost rows; a "+K more" tail costs one.
		// Height-bounded so the header/footer are never pushed off.
		remaining := rows - used
		if rows > 0 && remaining < 1+1 { // the roster line itself + at least the tail
			out.WriteString(muted.Render(fmt.Sprintf("  · +%d more branch(es)", len(ordered)-i)) + "\n")
			break
		}
		out.WriteString(renderParallelBranchRow(th, br, g.winner, i == cursor))
		used += 1 + renderParallelBranchTrace(&out, th, r, br, remaining)
	}

	if g.winnerWorkspace != "" {
		out.WriteString("\n" + muted.Render("winner fork (preserved): "+sanitizeTerminal(g.winnerWorkspace)))
	}
	// The cancel hint shows only while some branch is still cancellable (running with a
	// known child id); the selection arrows are always live on a populated list.
	hint := "↑/↓ select · esc back"
	if cancellable {
		hint = "↑/↓ select · x cancel · esc back"
	}
	if len(ordered) == 0 {
		hint = "esc back"
	}
	out.WriteString("\n\n" + muted.Render(hint))
	return out.String()
}

// renderParallelBranchRow renders one branch's roster line within a focused group: the
// "›" cursor on the SELECTED row (the one the `x` cancel key addresses), the "★" on the
// WINNER row, else a muted plain row. It always ends with a newline.
func renderParallelBranchRow(th theme.Theme, br *parallelBranch, winner int, selected bool) string {
	line := parallelBranchLine(br)
	switch {
	case selected:
		return th.Style("askButtonActive").Render("› "+line) + "\n"
	case br.index == winner:
		return th.Style("askButtonActive").Render("★ "+line) + "\n"
	default:
		return th.Style("muted").Render("  "+line) + "\n"
	}
}

// renderParallelBranchTrace appends a branch's interleaved trace (the Team focus format)
// below its roster line, height-bounded to the rows that remain: when the budget leaves
// no row (remaining-1 <= 0) the trace is omitted — a trace can never render in zero
// lines; when bounded it is clamped ANSI-safely with a "+N more lines" tail
// (capRenderedLines does not sanitizeTerminal the styling). It returns the number of
// rows the trace consumed (0 when omitted / empty).
func renderParallelBranchTrace(out *strings.Builder, th theme.Theme, r *renderer, br *parallelBranch, remaining int) int {
	trace := r.renderTrace(br.trace)
	if trace == "" {
		return 0
	}
	traceLines := strings.Count(trace, "\n") + 1
	if remaining <= 0 { // height unbounded (rows<=0): render full
		out.WriteString(trace + "\n")
		return traceLines
	}
	if remaining-1 <= 0 { // no row left for the trace: omit it
		return 0
	}
	if traceLines > remaining-1 {
		trace = capRenderedLines(th, trace, remaining-1)
		traceLines = remaining - 1
	}
	out.WriteString(trace + "\n")
	return traceLines
}

// parallelBranchLine is one branch row within a focused group: a state glyph (◐ running /
// ✓ done / ✗ failed), the branch label, its goal, the current/last tool, the tool count,
// and token usage. It holds only redacted metadata; the branch's bounded previews live
// on the trace rendered below the row, not on the row itself.
func parallelBranchLine(br *parallelBranch) string {
	glyph := "◐"
	if br.done {
		if br.failed {
			glyph = "✗"
		} else {
			glyph = "✓"
		}
	}
	label := br.label
	if label == "" {
		label = fmt.Sprintf("branch-%d", br.index+1)
	}
	goal := truncate(sanitizeTerminal(br.goal), maxSubagentGoalLen)
	state := "working…"
	if br.done {
		state = parallelBranchStopLabel(br)
		if br.durationMs > 0 {
			state += " · " + humanizeDuration(br.durationMs)
		}
	} else if br.current != "" {
		state = sanitizeTerminal(br.current) + "…"
	}
	routed := ""
	if r := subagentModelLabel(br.routedCategory, br.routedModel, br.routingReason, br.model); r != "" {
		routed = " · " + r
	}
	return fmt.Sprintf("%s %s · %s%s · %s · %s · ↑%s ↓%s",
		glyph, sanitizeTerminal(label), goal, routed, state,
		plural(br.toolCount, "tool"),
		humanizeTokens(br.usage.InputTokens),
		humanizeTokens(br.usage.OutputTokens))
}

// parallelBranchStopLabel labels a finished branch: "failed" (with the stop reason when
// it adds signal) for a failed branch, else the benign stop label.
func parallelBranchStopLabel(br *parallelBranch) string {
	if br.failed {
		if br.stop != "" {
			return "failed · " + subagentStopLabel(br.stop)
		}
		return "failed"
	}
	return subagentStopLabel(br.stop)
}

// findParallelGroup returns the group with the given ParentCallID off the slice, or nil.
func findParallelGroup(groups []parallelGroup, parentCallID string) *parallelGroup {
	for i := range groups {
		if groups[i].parentCallID == parentCallID {
			return &groups[i]
		}
	}
	return nil
}

// branchesByIndex returns a copy of the group's branches sorted by BranchIndex, so the
// focus view renders deterministically BY INDEX (branch-1, branch-2, …) even though the
// branches arrive — and are stored — in first-seen (concurrent, out-of-order) order. It
// copies so the conversation's insertion-ordered slice is never mutated.
func branchesByIndex(branches []parallelBranch) []parallelBranch {
	ordered := make([]parallelBranch, len(branches))
	copy(ordered, branches)
	slices.SortFunc(ordered, func(a, b parallelBranch) int { return a.index - b.index })
	return ordered
}

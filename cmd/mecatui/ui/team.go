package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// teamView is the active agent-team overlay (none = closed). The overlay is a
// read-only deep view layered over the conversation, mirroring the MCP overlay:
// it does not change the run phase, opens only over a populated Team card, and is
// dismissed with esc. It ships two states: the full (uncapped) roster and a
// per-member focus pane.
//
// It is the OVERFLOW HOME the inline Team card's maxTeamLanes cap defers to — the
// inline card stays the calm default (capped, with a "· +K more" roll-up), and
// f6 is the opt-in deep view showing the WHOLE team plus per-member detail.
type teamView int

const (
	teamNone     teamView = iota // overlay closed
	teamRoster                   // the full (uncapped) member roster
	teamFocus                    // one selected member's full trace
	teamTasks                    // the shared team task list (id · state · assignee · deps)
	teamFindings                 // the shared team findings ledger (member · body)
)

// teamState holds the agent-team overlay state on the Model. It is value-
// embedded so the Model stays a plain struct that Update copies. It holds only a
// view, a selection cursor, and the focused member NAME — never a copy of the
// lanes. The panel reads the live lanes straight off the latest Team block each
// render (see latestTeamBlock), so it always reflects the accumulating team
// without duplicating or risking a stale snapshot. Focus is keyed by name (not
// index) so a roster that grows under the overlay can't shift focus onto the
// wrong member.
type teamState struct {
	view   teamView
	cursor int    // selected row in the roster (an index into the render order)
	member string // the focused member's name (teamFocus)
	scroll int    // rendered-line offset in focus/tasks/findings
	roster boundedList
	detail boundedViewport
}

// openTeam opens the roster overlay over the most-recent populated Team card.
// It is openable while idle OR while a team is streaming (Gap B): f6 is the
// opt-in deep view, and the most useful time to open it is mid-run. It still
// rejects phaseAwaitingApproval (a permission modal owns the keyboard) and any
// fatal/connecting phase. It is a no-op when no team has run yet — the empty
// state distinguishes "teams not enabled" from "no team yet" via caps. Unlike
// the MCP overlays it fires no RPC: it reads the team lanes already accumulated
// in the conversation.
func (m Model) openTeam() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle && m.phase != phaseRunning {
		return m, nil
	}
	if m.conv.latestTeamBlock() == nil {
		// No team to show — surface a brief hint rather than opening an empty panel.
		// Distinguish "teams not enabled on this server" from "no team has run yet"
		// using the relayed caps, the same honest-affordance treatment the inventory
		// panels use. (The unified f6 still opens onto the Subagents tab when only
		// subagents ran; the /team command is team-specific, so it hints here.)
		if !m.caps.Teams {
			m.statusMsg = "agent teams are not enabled on this server"
		} else {
			m.statusMsg = "no team has run yet"
		}
		return m, nil
	}
	m.prompt.Blur() // overlay owns the keyboard while open
	// The /team command opens the unified overlay pinned to the Teams tab (its
	// team-specific entry point); f6 uses openAgents for the context-sensitive tab.
	m.agentsTab = tabTeams
	m.team = teamState{view: teamRoster}
	m.subagents = subagentState{view: subagentRoster}
	return m, nil
}

// closeTeam dismisses the unified agents overlay (the Teams tab's esc path) and
// returns focus to the prompt input. It delegates to closeAgents so the container's
// full state (both tabs) is cleared uniformly however the overlay is closed.
func (m Model) closeTeam() (tea.Model, tea.Cmd) {
	return m.closeAgents()
}

// onTeamKey routes key presses while the agent-team overlay is open. esc steps
// back from focus to the roster, then closes the roster; up/down move the roster
// selection; enter focuses the selected member. Returns handled=false when the
// overlay is closed so the caller falls through to normal idle key handling.
func (m Model) onTeamKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.team.view == teamNone {
		return m, nil, false
	}
	b := m.conv.latestTeamBlock()
	if b == nil {
		// The team vanished from under the overlay (defensive — blocks only grow,
		// but never trust it): close cleanly.
		mm, cmd := m.closeTeam()
		return mm, cmd, true
	}
	if m.team.view == teamFocus {
		if next, handled := m.navigateAgentsDetail(msg, m.team.scroll); handled {
			m.team.scroll = next
			return m, nil, true
		}
		// Focus pane: esc returns to the roster; x cancels the focused member (live
		// teams only — the key no-ops once the team ended or the member stopped). The
		// trace is height-bounded (no live viewport), so no other key is consumed.
		switch {
		case key.Matches(msg, m.keys.Close):
			m.team.view = teamRoster
			m.team.member = ""
			m.team.scroll = 0
		case key.Matches(msg, m.keys.CancelChild):
			mm, cmd := m.cancelTeamLane(b, teamFindLane(b, m.team.member))
			return mm, cmd, true
		}
		return m, nil, true
	}
	if m.team.view == teamTasks {
		if next, handled := m.navigateAgentsDetail(msg, m.team.scroll); handled {
			m.team.scroll = next
			return m, nil, true
		}
		// Subagent sub-view: BOTH esc and 't' return to the roster (t toggles, esc steps
		// back). It is a calm read-only surface like the focus pane — no other key is
		// consumed (the list is height-windowed, no live viewport).
		if key.Matches(msg, m.keys.Close) || key.Matches(msg, m.keys.Tasks) {
			m.team.view = teamRoster
			m.team.scroll = 0
		}
		return m, nil, true
	}
	if m.team.view == teamFindings {
		if next, handled := m.navigateAgentsDetail(msg, m.team.scroll); handled {
			m.team.scroll = next
			return m, nil, true
		}
		// Findings sub-view: BOTH esc and 'f' return to the roster (f toggles, esc
		// steps back), mirroring the task sub-view. Read-only, height-windowed, no
		// live viewport.
		if key.Matches(msg, m.keys.Close) || key.Matches(msg, m.keys.Findings) {
			m.team.view = teamRoster
			m.team.scroll = 0
		}
		return m, nil, true
	}
	mm, cmd := m.onTeamRosterKey(msg, b)
	return mm, cmd, true
}

// onTeamRosterKey drives the roster: up/down move the selection by one (the
// render window follows the cursor), pgup/pgdn move it by a window's worth,
// home/g and end/G jump to the first/last member, enter focuses the selected
// member by name, esc closes. Selection is over the render ORDER (lead first),
// matching what the user sees. The cursor is the single source of truth — the
// visible window is derived from it at render time (teamWindow), so a roster
// that grows under the overlay never desyncs a stored scroll offset.
func (m Model) onTeamRosterKey(msg tea.KeyPressMsg, b *block) (tea.Model, tea.Cmd) {
	th, hk, width, _ := m.agentsListGeometry()
	switch {
	case key.Matches(msg, m.keys.Close):
		return m.closeTeam()
	case key.Matches(msg, m.keys.Tasks):
		m.team.view = teamTasks
		m.team.scroll = 0
		return m, nil
	case key.Matches(msg, m.keys.Findings):
		m.team.view = teamFindings
		m.team.scroll = 0
		return m, nil
	}
	if next, control, handled := m.navigateAgentsList(msg, teamSelectableList(th, m.team, b, hk, width)); handled {
		m.team.cursor, m.team.roster = next, control
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keys.Choose):
		order := teamLaneOrder(b.teamLanes)
		if m.team.cursor < 0 || m.team.cursor >= len(order) {
			return m, nil
		}
		m.team.member = b.teamLanes[order[m.team.cursor]].name
		m.team.view = teamFocus
		m.team.scroll = 0
		return m, nil
	case key.Matches(msg, m.keys.CancelChild):
		order := teamLaneOrder(b.teamLanes)
		if m.team.cursor < 0 || m.team.cursor >= len(order) {
			return m, nil
		}
		return m.cancelTeamLane(b, &b.teamLanes[order[m.team.cursor]])
	}
	return m, nil
}

// navigateRosterCursor applies the shared roster navigation keys to cursor.
func navigateRosterCursor(msg tea.KeyPressMsg, keys keyMap, cursor, total, page int) (next int, handled bool) {
	switch {
	case key.Matches(msg, keys.Up):
		return clampCursor(cursor-1, total), true
	case key.Matches(msg, keys.Down):
		return clampCursor(cursor+1, total), true
	case key.Matches(msg, keys.ScrollU):
		return clampCursor(cursor-page, total), true
	case key.Matches(msg, keys.ScrollD):
		return clampCursor(cursor+page, total), true
	case key.Matches(msg, keys.JumpTop):
		return clampCursor(0, total), true
	case key.Matches(msg, keys.JumpEnd):
		return clampCursor(total-1, total), true
	}
	return cursor, false
}

// cancelTeamLane sends a CancelChild frame for one team member's session id (the
// member's MemberSessionID handle, arriving on team.member events). It is a no-op for
// a nil lane, a finished team, an already-stopped member, or a lane that never learned
// its session id (older server / member yet to produce an event) — the x key is only
// hinted for live teams; the finished-as-you-pressed race is benign (the server
// ignores a done id). Confirm-less, mirroring cancelSubagentLane: a cancelled member
// is de-scheduled, its tasks released, and its session persists for inspection.
func (m Model) cancelTeamLane(b *block, ln *teamLane) (tea.Model, tea.Cmd) {
	if ln == nil || b == nil || b.teamDone || ln.stopped {
		return m, nil
	}
	return m.cancelChildByID(ln.sessionID, "member "+sanitizeTerminal(ln.name))
}

// clampCursor clamps a candidate cursor index to [0, n-1] (and to 0 when the
// roster is empty), so all the jump/page math can be written without per-call
// bounds checks.
func clampCursor(i, n int) int {
	if n <= 0 {
		return 0
	}
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

// maxTeamFocusLines is the ABSOLUTE ceiling on how many lines of a focused
// member's trace the focus pane renders — a sane upper bound so one verbose member
// can't grow the overlay without limit (a DoS-by-output guard) on a very tall
// terminal. The EFFECTIVE cap is min(maxTeamFocusLines, fits-in-height); see
// teamFocusRows.
const maxTeamFocusLines = 40

// teamFocusChromeLines is the number of NON-trace lines the focus card always
// spends: the title, the member lane sub-header, the blank line under it, the
// blank line above the footer, and the "esc back" footer (5).
const teamFocusChromeLines = 5

// teamMinFocusRows is the floor on visible trace lines so even an absurdly short
// terminal still shows a usable slice of the member's activity (the pane just gets
// tight) rather than collapsing to zero trace lines.
const teamMinFocusRows = 3

// teamFocusRows is the effective trace-line cap for a focus card of the given
// OUTER height (the conversation region height passed to the overlay): the rows
// that actually fit, so the header + "esc back" footer are NEVER pushed off-screen
// and lipgloss.Place cannot clip them. It subtracts the card border+padding (4),
// the fixed chrome lines, and reserves one line for the "… +N more lines" tail,
// then clamps to [teamMinFocusRows, maxTeamFocusLines]. A non-positive height
// (size unknown) yields maxTeamFocusLines (the pre-bound behaviour, safe when we
// can't measure).
func teamFocusRows(height int) int {
	if height <= 0 {
		return maxTeamFocusLines
	}
	const cardChrome = 4 // border (2) + vertical padding (2)
	const tailReserve = 1
	rows := height - cardChrome - teamFocusChromeLines - tailReserve
	if rows < teamMinFocusRows {
		rows = teamMinFocusRows
	}
	if rows > maxTeamFocusLines {
		rows = maxTeamFocusLines
	}
	return rows
}

// The Teams tab of the unified f6 overlay is rendered by renderTeamsTab
// (agents_overlay.go), which dispatches to the renderTeamRoster/Focus/Tasks/Findings
// builders below. The former standalone renderTeamOverlay (which framed the body in
// its own centerCard) is gone — the unified container owns the framing now.

// teamMinRosterRows is the minimum logical-row allowance used by the
// non-selectable task and findings windows.
const teamMinRosterRows = 3

// renderTeamRoster renders the member roster WINDOWED to the available height:
// a header (member count + resolved round/stop summary), then the slice of lanes
// that fits — lead first — with the selected row marked with the unbordered ▶ treatment and the window
// following the cursor, bracketed by "· +K above" / "· +K below" affordances when
// rows are hidden, and a footer hint that is ALWAYS visible. This gives the
// uncapped overlay the same height-safety the inline card has (cap + roll-up):
// at 20–32 members the card never grows taller than the terminal and clips its
// footer or the selected row. height<=0 (size unknown) shows all rows.
func teamSelectableList(th theme.Theme, st teamState, b *block, hk helpKeys, bodyWidth int) agentsSelectableList {
	muted := th.Style("muted")
	header := renderDelegationRows(th.Style("askTitle"), "", teamRosterHeader(b), bodyWidth)
	if sub := teamRosterSubhead(b); sub != "" {
		header += "\n" + renderDelegationRows(muted, "", sub, bodyWidth)
	}
	order := teamLaneOrder(b.teamLanes)
	nameW := teamNameWidth(b.teamLanes, order)
	cursor := clampCursor(st.cursor, len(order))
	list := agentsSelectableList{
		header: header,
		footer: renderDynamicCardChromeLine(muted, "", hk.navUp+"/"+hk.navDown+" select · "+hk.choose+" focus · "+hk.cancelChild+" cancel · "+hk.tasks+" tasks · "+hk.findings+" findings · "+agentsEmptyHint(hk), bodyWidth),
		cursor: cursor, muted: muted, noun: "rows",
		bodyWidth: bodyWidth, control: st.roster,
	}
	for _, laneIndex := range order {
		list.ids = append(list.ids, b.teamLanes[laneIndex].sessionID)
		if list.ids[len(list.ids)-1] == "" {
			list.ids[len(list.ids)-1] = b.teamLanes[laneIndex].name
		}
		lane := &b.teamLanes[laneIndex]
		list.rows = append(list.rows, teamRosterTitle(lane, nameW, b.teamDone)+"\n    "+teamRosterWork(lane, b.teamDone)+"\n    "+teamRosterRuntime(lane))
	}
	return list
}

func renderTeamRoster(th theme.Theme, st teamState, b *block, hk helpKeys, height int, widths ...int) string {
	bodyWidth := 0
	if len(widths) > 0 {
		bodyWidth = widths[0]
	}
	return teamSelectableList(th, st, b, hk, bodyWidth).render(th, height)
}

// renderTeamRosterRow uses three fixed physical lines: identity, work summary, and
// runtime metadata. Each line is clipped to its own budget instead of allowing an
// arbitrary wrap to split related fields across rows.
func renderTeamRosterRow(style lipgloss.Style, prefix string, ln *teamLane, nameW int, teamDone bool, bodyWidth int) string {
	title := teamRosterTitle(ln, nameW, teamDone)
	work, runtime := teamRosterWork(ln, teamDone), teamRosterRuntime(ln)
	if bodyWidth > 0 {
		title = truncateDisplayWidth(title, max(1, bodyWidth-lipgloss.Width(prefix)))
		work = truncateDisplayWidth(work, max(1, bodyWidth-4))
		runtime = truncateDisplayWidth(runtime, max(1, bodyWidth-4))
	}
	return style.Render(prefix + title + "\n    " + work + "\n    " + runtime)
}

func teamRosterTitle(ln *teamLane, nameW int, teamDone bool) string {
	name := truncate(sanitizeTerminal(ln.name), maxTeamNameWidth)
	if pad := nameW - len([]rune(name)); pad > 0 {
		name += strings.Repeat(" ", pad)
	}
	if ln.lead {
		name += " [lead]"
	}
	return teamGlyph(ln, teamDone) + " " + teamMutCue(ln) + " " + name
}

func teamRosterWork(ln *teamLane, teamDone bool) string {
	parts := []string{teamLaneState(ln, teamDone)}
	if ln.role != "" {
		parts = append(parts, truncate(sanitizeTerminal(ln.role), maxTeamRoleLen))
	}
	return strings.Join(parts, " · ")
}

func teamRosterRuntime(ln *teamLane) string {
	parts := []string{"↑" + humanizeTokens(ln.usage.InputTokens) + " ↓" + humanizeTokens(ln.usage.OutputTokens)}
	if ln.ctxWindow > 0 {
		parts = append(parts, renderContextMeterPlain(ln.ctxUsed, ln.ctxWindow))
	}
	if routed := subagentModelLabel(ln.routedCategory, ln.routedModel, ln.routingReason, ln.model); routed != "" {
		parts = append(parts, routed)
	}
	return strings.Join(parts, " · ")
}

// teamRosterLine remains the compact, unstyled form used by the essential fallback.
func teamRosterLine(_ theme.Theme, ln *teamLane, nameW int, teamDone bool) string {
	return teamRosterTitle(ln, nameW, teamDone) + " · " + teamRosterWork(ln, teamDone) + " · " + teamRosterRuntime(ln)
}

// renderContextMeterPlain is the ANSI-free representation required before generic
// roster wrapping; renderContextMeter's styled bar must not be sanitized as raw text.
func renderContextMeterPlain(used, window int64) string {
	if used < 0 {
		used = 0
	}
	if window <= 0 {
		return "ctx " + humanizeTokens(used)
	}
	frac := ctxFraction(used, window)
	return "ctx " + ctxBar(frac) + " " + ctxLabel(frac) + " · " + humanizeTokens(used) + "/" + humanizeTokens(window)
}

// maxTeamRoleLen caps how many runes of a member's role show on a roster row so
// a verbose role never blows out the row width.
const maxTeamRoleLen = 20

// teamRosterHeader is the roster's title line: "agents · N members".
func teamRosterHeader(b *block) string {
	return "agents · " + plural(len(b.teamLanes), "member")
}

// teamRosterSubhead is the muted sub-header: the resolved round count + stop
// reason once the team has ended, else empty (the team is still live).
func teamRosterSubhead(b *block) string {
	if !b.teamDone {
		return ""
	}
	line := fmt.Sprintf("%s · ↑%s ↓%s · stop:%s",
		plural(b.teamRounds, "round"),
		humanizeTokens(b.teamUsage.InputTokens),
		humanizeTokens(b.teamUsage.OutputTokens),
		subagentStopLabel(b.teamStop))
	if n := teamStoppedCount(b); n > 0 {
		line += fmt.Sprintf(" · %d stopped", n)
	}
	return line
}

// renderTeamFocus renders ONE member's full detail: a header (state glyph +
// mutating cue + name + [lead] + current tool/state + usage) and its trace
// (message lines + tool chips with bounded Detail previews), height-bounded to the
// rows that FIT in the available height (teamFocusRows) so the header and the
// "esc back" footer are never pushed off-screen — the same height-safety the
// roster window has. width is the OUTER viewport width, used ONLY to wrap the
// benched member's failure line to the card's text budget (teamFailureLine) — the
// header and trace are height-bounded, not width-wrapped. A focused name with no
// matching lane (the member vanished — defensive) falls back to a muted note. All
// text is sanitized.
func renderTeamFocus(th theme.Theme, b *block, member string, hk helpKeys, bodyWidth, height int) string {
	return renderTeamFocusAt(th, b, member, 0, hk, bodyWidth, height)
}

func renderTeamFocusAt(th theme.Theme, b *block, member string, scroll int, hk helpKeys, bodyWidth, height int) string {
	muted := th.Style("muted")
	ln := teamFindLane(b, member)
	if ln == nil {
		return renderDynamicCardChromeLine(th.Style("askTitle"), "", "agents", bodyWidth) + "\n\n" +
			renderDynamicCardChromeLine(muted, "", "member "+sanitizeTerminal(member)+" is no longer in the roster", bodyWidth) + "\n\n" +
			renderDynamicCardChromeLine(muted, "", focusBackHint(hk), bodyWidth)
	}

	var out strings.Builder
	out.WriteString(th.Style("askTitle").Render(wrapFocusMetadataAtWidth("agent · "+truncate(sanitizeTerminal(ln.name), maxTeamNameWidth), bodyWidth)))
	out.WriteString("\n")
	// The member's own lane line (reusing the inline vocabulary) as a sub-header so
	// the focus pane is self-describing: glyph, mutating cue, name, [lead], state,
	// usage — plus the per-member context meter band when the member's window is
	// known (same gating as the roster row: no window → no meter).
	subhead := teamLaneLine(ln, 0, b.teamDone)
	if ln.ctxWindow > 0 {
		subhead += " · " + renderContextMeter(th, ln.ctxUsed, ln.ctxWindow)
	}
	out.WriteString(muted.Render(wrapFocusMetadataAtWidth(subhead, bodyWidth)))
	out.WriteString("\n\n")

	r := &renderer{th: th, marks: hk, traceWidth: bodyWidth}
	trace := r.renderTrace(ln.trace)
	if trace == "" {
		out.WriteString(muted.Render("(no activity yet)"))
	} else {
		// The trace is ALREADY rendered (carries ANSI; its text was sanitized at the
		// source in renderTrace). It must NOT go through truncateLines, which
		// sanitizeTerminal-strips ESC bytes and would mangle the styling — cap it by
		// line count ANSI-safely instead, to the rows that fit the terminal height.
		lines := strings.Split(trace, "\n")
		w := renderedLineWindow(scroll, len(lines), teamFocusRows(height))
		out.WriteString(strings.Join(lines[w.start:w.end], "\n"))
	}

	// A benched-on-error member surfaces WHY its last failed round failed, mirroring
	// the subagent focus pane's failure block (issue #331). Rendered ONLY when the
	// team ended with the member stopped for an error and a cause was carried; a done
	// (possibly retried) member does not render it (it recovered).
	if b.teamDone && ln.stopped && ln.stopReason == teamStopReasonError && ln.cause != "" {
		out.WriteString("\n")
		out.WriteString(teamFailureLineAtWidth(ln, bodyWidth))
	}

	// The cancel hint shows only for a CANCELLABLE member: a live team, a lane not
	// already stopped, and a known session id (the CancelChild handle). The chords
	// read the LIVE CancelChild/Close markings (issue #457).
	traceLines := renderedTraceLines(th, hk, bodyWidth, ln.trace)
	w := renderedLineWindow(scroll, len(traceLines), teamFocusRows(height))
	lead := focusBackHint(hk)
	if !b.teamDone && !ln.stopped && ln.sessionID != "" {
		lead = hk.cancelChild + " cancel · " + lead
	}
	hint := agentsDetailHint(hk, w, lead)
	out.WriteString("\n\n" + renderDynamicCardChromeLine(muted, "", hint, bodyWidth))
	return out.String()
}

// teamFailureLine renders the focus pane's failure block for a team member that ended
// benched on an error and carried a per-round cause: "  failed: <cause>", word-wrapped
// and indented to the overlay card's text budget at the given viewport width. It
// mirrors subagentFailureLine (agents_overlay.go) and reuses its maxSubagentCauseWidth
// display bound — the same per-pane rune cap, no team-specific const — because the team
// focus pane and the subagent focus pane have the SAME height/width budget for a
// failure line. The cause is server-derived harness/provider metadata (never
// member-authored output, so gauntlet #7 holds) and is sanitized like every other
// server-derived string. WRAPPING IS LOAD-BEARING for the same reason as
// subagentFailureLine: centerCard → lipgloss.Place cannot shrink content, so the widest
// line directly sets the card's width. Returns "" for a lane without a cause, one not
// benched, or one benched for a non-error reason — self-defensive like
// subagentFailureLine, so a future caller without the renderTeamFocus gate cannot
// render a stale cause on a recovered (done) member or a non-error stop.
func teamFailureLine(ln *teamLane, width int) string {
	return teamFailureLineAtWidth(ln, focusCardTextWidth(width))
}

func teamFailureLineAtWidth(ln *teamLane, bodyWidth int) string {
	if !ln.stopped || ln.stopReason != teamStopReasonError || ln.cause == "" {
		return ""
	}
	return indentWrap("failed: "+truncate(sanitizeTerminal(strings.Join(strings.Fields(ln.cause), " ")), maxSubagentCauseWidth), bodyWidth)
}

// teamFindLane returns the lane named member off the team block, or nil. Names
// are unique per team (lanes are keyed by name when routing events), so the first
// match is the lane.
func teamFindLane(b *block, member string) *teamLane {
	for i := range b.teamLanes {
		if b.teamLanes[i].name == member {
			return &b.teamLanes[i]
		}
	}
	return nil
}

// Subagent-list state strings (mirror team.TaskState / TeamTask.State on the wire).
const (
	taskStatePending    = "pending"
	taskStateInProgress = "in_progress"
	taskStateCompleted  = "completed"
)

// teamTasksChromeLines is the number of NON-row lines the task sub-view always
// spends: the title, the summary sub-head, the blank line under it, the blank line
// above the footer, and the footer (5). It mirrors the roster's chrome accounting
// so the height-window math stays consistent.
const teamTasksChromeLines = 5

// teamTasksRows is how many task rows fit in the task sub-view for a card of the
// given OUTER height. It subtracts the card border+padding
// and the fixed chrome, reserve one line for the "+N more" tail, floor at
// teamMinRosterRows. A non-positive height (size unknown) shows all rows.
func teamTasksRows(height int) int {
	if height <= 0 {
		return 0
	}
	const cardChrome = 4 // border (2) + vertical padding (2)
	const tailReserve = 1
	rows := height - cardChrome - teamTasksChromeLines - tailReserve
	if rows < teamMinRosterRows {
		return teamMinRosterRows
	}
	return rows
}

// taskBlocked reports whether a PENDING task is blocked: at least one of its
// dependencies is not yet completed. byID maps task id → state for the lookup. A
// missing dependency id counts as not-completed (it cannot be satisfied), so the
// task reads as blocked rather than silently claimable. Non-pending tasks are never
// "blocked" by this predicate (in-progress/completed have moved past the gate).
func taskBlocked(t teamTask, byID map[string]string) bool {
	if t.state != taskStatePending {
		return false
	}
	for _, d := range t.deps {
		if byID[d] != taskStateCompleted {
			return true
		}
	}
	return false
}

// taskGlyph maps a task's state (and blocked-ness, for pending tasks) to a single
// ANSI-strip-safe glyph — colour is never the sole signal, so the sub-view reads
// the same through a golden's stripANSI:
//   - completed            → ✓ (done)
//   - in_progress          → ◆ (claimed, being worked; same glyph as a working lane)
//   - pending, unblocked   → ○ (claimable, waiting for a worker)
//   - pending, blocked     → ⊘ (waiting on an unmet dependency)
func taskGlyph(state string, blocked bool) string {
	switch state {
	case taskStateCompleted:
		return "✓"
	case taskStateInProgress:
		return "◆"
	default: // pending (or any unknown state — treat as not-yet-done)
		if blocked {
			return "⊘"
		}
		return "○"
	}
}

// teamSubViewHint is the "<flip> roster · <close> close" footer used by the team
// tasks/findings sub-views. The flip chord (Tasks/Findings) and the close chord
// (Close) read the LIVE keyMap markings so an override propagates (issue #457);
// with defaults it is byte-identical to the historical literal.
func teamSubViewHint(hk helpKeys, flip string) string {
	return flip + " roster · " + hk.closeOnly + " close"
}

// renderTeamTasks draws the shared team task list: a title, a one-line summary
// (N done · N in-progress · N pending(N blocked)), then one height-windowed row per
// task (glyph · id · state · assignee · deps). An empty list reads as a muted
// "(no tasks)". All task-derived strings are terminal-sanitized. It mirrors the
// roster's height-window math so a long task list never clips the footer.
func renderedTaskLines(th theme.Theme, b *block, bodyWidth int) []string {
	byID := make(map[string]string, len(b.teamTasks))
	for _, task := range b.teamTasks {
		byID[task.id] = task.state
	}
	var lines []string
	for _, task := range b.teamTasks {
		lines = append(lines, strings.Split(renderDynamicCardChromeLine(th.Style("muted"), "  ", taskRow(task, byID), bodyWidth), "\n")...)
	}
	return lines
}

func agentsDetailHint(hk helpKeys, w renderedLineWindowBounds, lead string) string {
	hint := ""
	if w.total > w.window {
		hint = fmt.Sprintf("lines %d–%d of %d · ", w.start+1, w.end, w.total)
	}
	return hint + lead + " · " + hk.navUp + "/" + hk.navDown + " scroll · " + hk.scroll + " · " + hk.jumpTop + "/" + hk.jumpEnd
}

func renderTeamTasks(th theme.Theme, b *block, hk helpKeys, height int, widths ...int) string {
	return renderTeamTasksAt(th, b, 0, hk, height, widths...)
}

func renderTeamTasksAt(th theme.Theme, b *block, scroll int, hk helpKeys, height int, widths ...int) string {
	muted := th.Style("muted")
	bodyWidth := 0
	if len(widths) > 0 {
		bodyWidth = widths[0]
	}
	var out strings.Builder

	out.WriteString(renderDelegationRows(th.Style("askTitle"), "", "tasks", bodyWidth))
	out.WriteString("\n")
	out.WriteString(renderDelegationRows(muted, "", teamTasksSummary(b.teamTasks), bodyWidth))
	out.WriteString("\n\n")

	if len(b.teamTasks) == 0 {
		out.WriteString(renderDynamicCardChromeLine(muted, "", "(no tasks)", bodyWidth))
		out.WriteString("\n\n" + renderDynamicCardChromeLine(muted, "", teamSubViewHint(hk, hk.tasks), bodyWidth))
		return out.String()
	}

	byID := make(map[string]string, len(b.teamTasks))
	for _, t := range b.teamTasks {
		byID[t.id] = t.state
	}

	lines := renderedTaskLines(th, b, bodyWidth)
	w := renderedLineWindow(scroll, len(lines), teamTasksRows(height))
	out.WriteString(strings.Join(lines[w.start:w.end], "\n"))
	out.WriteString("\n\n" + renderDynamicCardChromeLine(muted, "", agentsDetailHint(hk, w, teamSubViewHint(hk, hk.tasks)), bodyWidth))
	return out.String()
}

// maxTaskDescLen bounds the inline task description so a row stays one line (the
// task sub-view's window-height math counts one line per task).
const maxTaskDescLen = 40

// taskRow renders one task row: glyph · id · [desc ·] state · assignee (or "—") ·
// deps. All task-derived strings are sanitized; the description is truncated to
// maxTaskDescLen so the row stays a single scannable line. A task with no
// description keeps the 5-field form (no empty cell).
func taskRow(t teamTask, byID map[string]string) string {
	assignee := "—"
	if t.assignee != "" {
		assignee = sanitizeTerminal(t.assignee)
	}
	deps := "—"
	if len(t.deps) > 0 {
		sane := make([]string, 0, len(t.deps))
		for _, d := range t.deps {
			sane = append(sane, sanitizeTerminal(d))
		}
		deps = strings.Join(sane, ",")
	}
	glyph := taskGlyph(t.state, taskBlocked(t, byID))
	id := sanitizeTerminal(t.id)
	state := sanitizeTerminal(t.state)
	if t.desc == "" {
		return fmt.Sprintf("%s %s · %s · %s · deps:%s", glyph, id, state, assignee, deps)
	}
	desc := truncate(sanitizeTerminal(t.desc), maxTaskDescLen)
	return fmt.Sprintf("%s %s · %s · %s · %s · deps:%s", glyph, id, desc, state, assignee, deps)
}

// teamTasksSummary renders the one-line task roll-up: "N done · N in-progress ·
// N pending(N blocked)". The blocked count is the subset of pending tasks with an
// unmet dependency. It is the at-a-glance header of the task sub-view.
func teamTasksSummary(tasks []teamTask) string {
	byID := make(map[string]string, len(tasks))
	for _, t := range tasks {
		byID[t.id] = t.state
	}
	var done, inProgress, pending, blocked int
	for _, t := range tasks {
		switch t.state {
		case taskStateCompleted:
			done++
		case taskStateInProgress:
			inProgress++
		default:
			pending++
			if taskBlocked(t, byID) {
				blocked++
			}
		}
	}
	return fmt.Sprintf("%d done · %d in-progress · %d pending(%d blocked)",
		done, inProgress, pending, blocked)
}

// teamFindingsChromeLines is the number of NON-row lines the findings sub-view
// always spends: the title, the summary sub-head, the blank line under it, the
// blank line above the footer, and the footer (5). It mirrors teamTasksChromeLines
// so the height-window math stays consistent across the two ledger sub-views.
const teamFindingsChromeLines = 5

// teamFindingsRows is how many finding rows fit in the findings sub-view for a card
// of the given OUTER height. It mirrors teamTasksRows: subtract the card
// border+padding and the fixed chrome, reserve one line for the "+N more" tail,
// floor at teamMinRosterRows. A non-positive height (size unknown) shows all rows.
func teamFindingsRows(height int) int {
	if height <= 0 {
		return 0
	}
	const cardChrome = 4 // border (2) + vertical padding (2)
	const tailReserve = 1
	rows := height - cardChrome - teamFindingsChromeLines - tailReserve
	if rows < teamMinRosterRows {
		return teamMinRosterRows
	}
	return rows
}

// renderTeamFindings draws the shared team findings ledger: a title, a one-line
// summary (N findings from M members), then one height-windowed row per finding
// (member · body). An empty ledger reads as a muted "(no findings)". All
// finding-derived strings are terminal-sanitized. It mirrors renderTeamTasks's
// chrome and height-window math so a long ledger never clips the footer.
func renderedFindingLines(th theme.Theme, b *block, bodyWidth int) []string {
	var lines []string
	for _, finding := range b.teamFindings {
		lines = append(lines, strings.Split(renderDynamicCardChromeLine(th.Style("muted"), "  ", findingRow(finding), bodyWidth), "\n")...)
	}
	return lines
}

func renderTeamFindings(th theme.Theme, b *block, hk helpKeys, height int, widths ...int) string {
	return renderTeamFindingsAt(th, b, 0, hk, height, widths...)
}

func renderTeamFindingsAt(th theme.Theme, b *block, scroll int, hk helpKeys, height int, widths ...int) string {
	muted := th.Style("muted")
	bodyWidth := 0
	if len(widths) > 0 {
		bodyWidth = widths[0]
	}
	var out strings.Builder

	out.WriteString(renderDelegationRows(th.Style("askTitle"), "", "findings", bodyWidth))
	out.WriteString("\n")
	out.WriteString(renderDelegationRows(muted, "", teamFindingsSummary(b.teamFindings), bodyWidth))
	out.WriteString("\n\n")

	if len(b.teamFindings) == 0 {
		out.WriteString(renderDynamicCardChromeLine(muted, "", "(no findings)", bodyWidth))
		out.WriteString("\n\n" + renderDynamicCardChromeLine(muted, "", teamSubViewHint(hk, hk.findings), bodyWidth))
		return out.String()
	}

	lines := renderedFindingLines(th, b, bodyWidth)
	w := renderedLineWindow(scroll, len(lines), teamFindingsRows(height))
	out.WriteString(strings.Join(lines[w.start:w.end], "\n"))
	out.WriteString("\n\n" + renderDynamicCardChromeLine(muted, "", agentsDetailHint(hk, w, teamSubViewHint(hk, hk.findings)), bodyWidth))
	return out.String()
}

// findingRow renders one finding row: "member · body". Both the member name and the
// body are terminal-sanitized (member-authored, already bounded server-side by
// clampPreview); newlines in the body are collapsed so a multi-line finding stays on
// one scannable row.
func findingRow(f teamFinding) string {
	body := sanitizeTerminal(strings.ReplaceAll(f.body, "\n", " "))
	return fmt.Sprintf("%s · %s", sanitizeTerminal(f.member), body)
}

// teamFindingsSummary renders the one-line ledger roll-up: "N finding(s) from M
// member(s)". It is the at-a-glance header of the findings sub-view.
func teamFindingsSummary(findings []teamFinding) string {
	members := make(map[string]struct{}, len(findings))
	for _, f := range findings {
		members[f.member] = struct{}{}
	}
	return fmt.Sprintf("%d finding(s) from %d member(s)", len(findings), len(members))
}

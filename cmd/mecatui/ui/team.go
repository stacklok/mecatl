package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

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
// ctrl+a is the opt-in deep view showing the WHOLE team plus per-member detail.
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
}

// openTeam opens the roster overlay over the most-recent populated Team card.
// It is openable while idle OR while a team is streaming (Gap B): ctrl+a is the
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
		// panels use. (The unified ctrl+a still opens onto the Subagents tab when only
		// subagents ran; the /team command is team-specific, so it hints here.)
		if !m.caps.Teams {
			m.statusMsg = "agent teams are not enabled on this server"
		} else {
			m.statusMsg = "no team has run yet"
		}
		return m, nil
	}
	m.ta.Blur() // overlay owns the keyboard while open
	// The /team command opens the unified overlay pinned to the Teams tab (its
	// team-specific entry point); ctrl+a uses openAgents for the context-sensitive tab.
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
		// Focus pane: esc returns to the roster; x cancels the focused member (live
		// teams only — the key no-ops once the team ended or the member stopped). The
		// trace is height-bounded (no live viewport), so no other key is consumed.
		switch {
		case key.Matches(msg, m.keys.Close):
			m.team.view = teamRoster
			m.team.member = ""
		case key.Matches(msg, m.keys.CancelChild):
			mm, cmd := m.cancelTeamLane(b, teamFindLane(b, m.team.member))
			return mm, cmd, true
		}
		return m, nil, true
	}
	if m.team.view == teamTasks {
		// Subagent sub-view: BOTH esc and 't' return to the roster (t toggles, esc steps
		// back). It is a calm read-only surface like the focus pane — no other key is
		// consumed (the list is height-windowed, no live viewport).
		if key.Matches(msg, m.keys.Close) || key.Matches(msg, m.keys.Tasks) {
			m.team.view = teamRoster
		}
		return m, nil, true
	}
	if m.team.view == teamFindings {
		// Findings sub-view: BOTH esc and 'f' return to the roster (f toggles, esc
		// steps back), mirroring the task sub-view. Read-only, height-windowed, no
		// live viewport.
		if key.Matches(msg, m.keys.Close) || key.Matches(msg, m.keys.Findings) {
			m.team.view = teamRoster
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
	n := len(b.teamLanes)
	page := teamRosterRows(m.vp.Height())
	switch {
	case key.Matches(msg, m.keys.Close):
		return m.closeTeam()
	case key.Matches(msg, m.keys.Tasks):
		m.team.view = teamTasks
		return m, nil
	case key.Matches(msg, m.keys.Findings):
		m.team.view = teamFindings
		return m, nil
	case key.Matches(msg, m.keys.Up):
		m.team.cursor = clampCursor(m.team.cursor-1, n)
		return m, nil
	case key.Matches(msg, m.keys.Down):
		m.team.cursor = clampCursor(m.team.cursor+1, n)
		return m, nil
	case key.Matches(msg, m.keys.ScrollU):
		m.team.cursor = clampCursor(m.team.cursor-page, n)
		return m, nil
	case key.Matches(msg, m.keys.ScrollD):
		m.team.cursor = clampCursor(m.team.cursor+page, n)
		return m, nil
	case key.Matches(msg, m.keys.JumpTop):
		m.team.cursor = 0
		return m, nil
	case key.Matches(msg, m.keys.JumpEnd):
		m.team.cursor = clampCursor(n-1, n)
		return m, nil
	case key.Matches(msg, m.keys.Choose):
		order := teamLaneOrder(b.teamLanes)
		if m.team.cursor < 0 || m.team.cursor >= len(order) {
			return m, nil
		}
		m.team.member = b.teamLanes[order[m.team.cursor]].name
		m.team.view = teamFocus
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

// The Teams tab of the unified ctrl+a overlay is rendered by renderTeamsTab
// (agents_overlay.go), which dispatches to the renderTeamRoster/Focus/Tasks/Findings
// builders below. The former standalone renderTeamOverlay (which framed the body in
// its own centerCard) is gone — the unified container owns the framing now.

// teamRosterChromeLines is the number of NON-lane lines the roster card always
// spends on chrome: the title, the blank line under the header, the blank line
// above the footer, and the footer hint (4). The optional resolved sub-header and
// the +K above/below tails are accounted for separately by teamRosterRows.
const teamRosterChromeLines = 4

// teamMinRosterRows is the floor on visible lane rows, so even an absurdly short
// terminal still shows a usable slice of the roster (the window just gets tight)
// rather than collapsing to zero rows.
const teamMinRosterRows = 3

// teamRosterRows is how many lane rows fit in the roster window for a card of
// the given OUTER height (the conversation region height passed to the overlay).
// It subtracts the card border+padding (4) and the fixed chrome lines, reserving
// two extra lines for the potential +K above / +K below tails so the footer hint
// is NEVER pushed off-screen by the tails. Floors at teamMinRosterRows. A
// non-positive height (size unknown) yields 0 → renderTeamRoster shows all rows
// (the pre-windowing behaviour, safe when we can't measure).
func teamRosterRows(height int) int {
	if height <= 0 {
		return 0
	}
	const cardChrome = 4 // border (2) + vertical padding (2)
	const tailReserve = 2
	rows := height - cardChrome - teamRosterChromeLines - tailReserve
	if rows < teamMinRosterRows {
		return teamMinRosterRows
	}
	return rows
}

// teamWindow computes the [start, end) slice of the render order to show so the
// cursor stays visible, scrolling the window to follow it. rows is the visible
// capacity (0 = show all). The window is anchored to keep the cursor in view with
// a stable, minimal scroll: it only moves when the cursor would fall outside the
// current span, and clamps to the ends so the last page is full. It returns the
// slice bounds plus how many rows are hidden above/below (for the +K tails).
func teamWindow(cursor, total, rows int) (start, end, above, below int) {
	if rows <= 0 || total <= rows {
		return 0, total, 0, 0
	}
	// Center-ish: place the cursor with a little context above where possible, then
	// clamp so the window never runs past either end (a full last page).
	start = cursor - rows/2
	if start < 0 {
		start = 0
	}
	if start > total-rows {
		start = total - rows
	}
	end = start + rows
	return start, end, start, total - end
}

// renderTeamRoster renders the member roster WINDOWED to the available height:
// a header (member count + resolved round/stop summary), then the slice of lanes
// that fits — lead first — with the selected row highlighted and the window
// following the cursor, bracketed by "· +K above" / "· +K below" affordances when
// rows are hidden, and a footer hint that is ALWAYS visible. This gives the
// uncapped overlay the same height-safety the inline card has (cap + roll-up):
// at 20–32 members the card never grows taller than the terminal and clips its
// footer or the selected row. height<=0 (size unknown) shows all rows.
func renderTeamRoster(th theme.Theme, st teamState, b *block, height int) string {
	muted := th.Style("muted")
	var out strings.Builder

	out.WriteString(th.Style("askTitle").Render(teamRosterHeader(b)))
	out.WriteString("\n")
	if sub := teamRosterSubhead(b); sub != "" {
		out.WriteString(muted.Render(sub) + "\n")
	}
	out.WriteString("\n")

	order := teamLaneOrder(b.teamLanes)
	nameW := teamNameWidth(b.teamLanes, order)
	cursor := clampCursor(st.cursor, len(order))
	start, end, above, below := teamWindow(cursor, len(order), teamRosterRows(height))

	if above > 0 {
		out.WriteString(muted.Render(fmt.Sprintf("  · +%d above", above)) + "\n")
	}
	for row := start; row < end; row++ {
		ln := &b.teamLanes[order[row]]
		line := teamRosterLine(th, ln, nameW, b.teamDone)
		if row == cursor {
			out.WriteString(th.Style("askButtonActive").Render("› "+line) + "\n")
		} else {
			out.WriteString(muted.Render("  "+line) + "\n")
		}
	}
	if below > 0 {
		out.WriteString(muted.Render(fmt.Sprintf("  · +%d below", below)) + "\n")
	}

	// SHORTER than the old roster hint (the paging chords still work, unnamed): the
	// added "x cancel" segment would otherwise push this card past a 100-col terminal
	// — the hint is the card's widest line, so it directly sets the overlay width
	// (centerCard does not wrap). Same discipline as the Subagents-tab hint.
	out.WriteString("\n" + muted.Render("↑/↓ select · enter focus · x cancel · t tasks · f findings · tab switch · esc close"))
	return out.String()
}

// teamRosterLine is one roster row: the inline lane line (state glyph + mutating
// cue + name + [lead] + current tool/state + usage), then the per-member context
// meter band (only when the member's window is known), then the member's ROLE
// appended as a dim suffix when present, then the opt-in model router's "routed:
// <category> → <model>" cue when the member was routed — the detail the calm inline
// card omits, surfaced here in the dedicated deep view. The context meter reuses the
// footer's renderContextMeter so the band/percentage/⚠ vocabulary matches the main
// meter exactly. It is GATED on a known window (ctxWindow>0): with no window there is
// no denominator, so a bare "ctx <size>" with no band is suppressed entirely. The
// role is sanitized (roster-derived) and truncated so a long role can't blow out the
// row. The routed cue is bare metadata (a label + a model id), never member content.
func teamRosterLine(th theme.Theme, ln *teamLane, nameW int, teamDone bool) string {
	line := teamLaneLine(ln, nameW, teamDone)
	if ln.ctxWindow > 0 {
		line += " · " + renderContextMeter(th, ln.ctxUsed, ln.ctxWindow)
	}
	if ln.role != "" {
		line += " · " + truncate(sanitizeTerminal(ln.role), maxTeamRoleLen)
	}
	if r := subagentModelLabel(ln.routedCategory, ln.routedModel, ln.model); r != "" {
		line += " · " + r
	}
	return line
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
// roster window has. A focused name with no matching lane (the member vanished —
// defensive) falls back to a muted note. All text is sanitized.
func renderTeamFocus(th theme.Theme, b *block, member string, height int) string {
	muted := th.Style("muted")
	ln := teamFindLane(b, member)
	if ln == nil {
		return th.Style("askTitle").Render("agents") + "\n\n" +
			muted.Render("member "+sanitizeTerminal(member)+" is no longer in the roster") + "\n\n" +
			muted.Render("esc back")
	}

	var out strings.Builder
	out.WriteString(th.Style("askTitle").Render("agent · " + truncate(sanitizeTerminal(ln.name), maxTeamNameWidth)))
	out.WriteString("\n")
	// The member's own lane line (reusing the inline vocabulary) as a sub-header so
	// the focus pane is self-describing: glyph, mutating cue, name, [lead], state,
	// usage — plus the per-member context meter band when the member's window is
	// known (same gating as the roster row: no window → no meter).
	subhead := teamLaneLine(ln, 0, b.teamDone)
	if ln.ctxWindow > 0 {
		subhead += " · " + renderContextMeter(th, ln.ctxUsed, ln.ctxWindow)
	}
	out.WriteString(muted.Render(subhead))
	out.WriteString("\n\n")

	r := &renderer{th: th} // a width-0 renderer: chips don't wrap, traces render full
	trace := r.renderTeamTrace(ln)
	if trace == "" {
		out.WriteString(muted.Render("(no activity yet)"))
	} else {
		// The trace is ALREADY rendered (carries ANSI; its text was sanitized at the
		// source in renderTeamTrace). It must NOT go through truncateLines, which
		// sanitizeTerminal-strips ESC bytes and would mangle the styling — cap it by
		// line count ANSI-safely instead, to the rows that fit the terminal height.
		out.WriteString(capRenderedLines(th, trace, teamFocusRows(height)))
	}

	// The cancel hint shows only for a CANCELLABLE member: a live team, a lane not
	// already stopped, and a known session id (the CancelChild handle).
	hint := "esc back"
	if !b.teamDone && !ln.stopped && ln.sessionID != "" {
		hint = "x cancel · esc back"
	}
	out.WriteString("\n\n" + muted.Render(hint))
	return out.String()
}

// capRenderedLines clamps an ALREADY-RENDERED (ANSI-carrying) string to maxLines,
// appending a muted "+N more lines" tail when it overflows. Unlike truncateLines,
// it does NOT sanitizeTerminal the input (that would strip the ESC bytes of the
// embedded styling), so it is the right cap for content whose text was already
// sanitized at render time (the team trace chips/messages). The tail does not
// reference ctrl+t (the focus pane has no expand toggle).
func capRenderedLines(th theme.Theme, s string, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	extra := len(lines) - maxLines
	kept := strings.Join(lines[:maxLines], "\n")
	return kept + "\n" + th.Style("muted").Render(fmt.Sprintf("  … +%s", plural(extra, "more line")))
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
// given OUTER height. It mirrors teamRosterRows: subtract the card border+padding
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

// renderTeamTasks draws the shared team task list: a title, a one-line summary
// (N done · N in-progress · N pending(N blocked)), then one height-windowed row per
// task (glyph · id · state · assignee · deps). An empty list reads as a muted
// "(no tasks)". All task-derived strings are terminal-sanitized. It mirrors the
// roster's height-window math so a long task list never clips the footer.
func renderTeamTasks(th theme.Theme, b *block, height int) string {
	muted := th.Style("muted")
	var out strings.Builder

	out.WriteString(th.Style("askTitle").Render("tasks"))
	out.WriteString("\n")
	out.WriteString(muted.Render(teamTasksSummary(b.teamTasks)))
	out.WriteString("\n\n")

	if len(b.teamTasks) == 0 {
		out.WriteString(muted.Render("(no tasks)"))
		out.WriteString("\n\n" + muted.Render("t roster · esc close"))
		return out.String()
	}

	byID := make(map[string]string, len(b.teamTasks))
	for _, t := range b.teamTasks {
		byID[t.id] = t.state
	}

	// Window the rows to the available height (cursor-free: the task sub-view has no
	// selection, so it always anchors at the top, surfacing only a "+N more" tail).
	rows := teamTasksRows(height)
	start, end, _, below := teamWindow(0, len(b.teamTasks), rows)
	for i := start; i < end; i++ {
		out.WriteString("  " + muted.Render(taskRow(b.teamTasks[i], byID)) + "\n")
	}
	if below > 0 {
		out.WriteString(muted.Render(fmt.Sprintf("  · +%d more", below)) + "\n")
	}

	out.WriteString("\n" + muted.Render("t roster · esc close"))
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
func renderTeamFindings(th theme.Theme, b *block, height int) string {
	muted := th.Style("muted")
	var out strings.Builder

	out.WriteString(th.Style("askTitle").Render("findings"))
	out.WriteString("\n")
	out.WriteString(muted.Render(teamFindingsSummary(b.teamFindings)))
	out.WriteString("\n\n")

	if len(b.teamFindings) == 0 {
		out.WriteString(muted.Render("(no findings)"))
		out.WriteString("\n\n" + muted.Render("f roster · esc close"))
		return out.String()
	}

	// Window the rows to the available height (cursor-free: the findings sub-view has
	// no selection, so it always anchors at the top, surfacing only a "+N more" tail).
	rows := teamFindingsRows(height)
	start, end, _, below := teamWindow(0, len(b.teamFindings), rows)
	for i := start; i < end; i++ {
		out.WriteString("  " + muted.Render(findingRow(b.teamFindings[i])) + "\n")
	}
	if below > 0 {
		out.WriteString(muted.Render(fmt.Sprintf("  · +%d more", below)) + "\n")
	}

	out.WriteString("\n" + muted.Render("f roster · esc close"))
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

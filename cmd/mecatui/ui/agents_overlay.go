package ui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

// agentsTab selects which body the unified f6 "agents" overlay renders. The
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
	view     parallelView
	group    string // the focused group's ParentCallID (parallelGroupView)
	roster   *bounded.List
	branches *bounded.List
}

func teamBlockIdentity(b *teamOverlaySnapshot) string {
	if b == nil {
		return ""
	}
	// The tool call and block identities exist when the block is created. teamID is
	// stream metadata that may arrive later, so it must never participate in UI state.
	if b.callID != "" {
		return aggregateScopedID("call", b.callID)
	}
	return aggregateScopedID("block", strconv.FormatUint(uint64(b.cardID), 10))
}

func (m Model) teamBlockForOverlay() *teamOverlaySnapshot {
	if m.team.aggregate == "" {
		return m.conv.latestTeamBlock()
	}
	for i := 0; i < m.conv.scrollback.Len(); i++ {
		if m.conv.scrollback.MetadataAt(i).Kind != scrollback.KindTeam {
			continue
		}
		snapshot := m.conv.scrollback.SnapshotAt(i)
		payload, ok := snapshot.Payload.(scrollback.TeamCardSnapshot)
		if !ok {
			continue
		}
		b := teamOverlaySnapshotFromSnapshot(snapshot.ID, payload)
		if teamBlockIdentity(b) == m.team.aggregate {
			return b
		}
	}
	return nil
}

func parallelBranchID(parentCallID string, index int) string {
	return aggregateScopedID(parentCallID, strconv.Itoa(index))
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
	child  string // the focused child's ChildID (subagentFocus)
	roster *bounded.List
	detail *bounded.Viewport
}

func newParallelState() parallelState {
	return parallelState{view: parallelRoster, roster: new(bounded.List), branches: new(bounded.List)}
}

func newSubagentState() subagentState {
	return subagentState{view: subagentRoster, roster: new(bounded.List), detail: new(bounded.Viewport)}
}

// openAgents opens the unified f6 agents overlay. It picks the CONTEXT-SENSITIVE
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
	haveTeam := m.teamBlockForOverlay() != nil
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
	m.prompt.Blur() // the overlay owns the keyboard while open
	// Context-sensitive default tab (preferredAgentsTab is the single predicate, tested in
	// isolation): the richest LIVE surface wins, else the tab that has content.
	m.agentsTab = m.preferredAgentsTab(teamLive, haveTeam, haveSub, parallelLive, haveParallel)
	m.team = newTeamState() // container open flag (+ Teams-tab state)
	m.subagents = newSubagentState()
	m.parallel = newParallelState()
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
	cmd := m.prompt.Focus()
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
		m.team.detail.Reset()
	default:
		m.agentsTab = tabSubagents
		m.subagents.view = subagentRoster
		m.subagents.detail.Reset()
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
	if m.height > 0 && m.height < 24 {
		// Compact mode intentionally exposes no normal-content navigation: only
		// escape is meaningful, and it first returns a focus pane to its roster.
		if key.Matches(msg, m.keys.Close) {
			if !m.atAgentsRoster() {
				switch m.agentsTab {
				case tabSubagents:
					m.subagents.view, m.subagents.child = subagentRoster, ""
					m.subagents.detail.Reset()
				case tabParallel:
					m.parallel.view, m.parallel.group = parallelRoster, ""
				default:
					m.team.view, m.team.member, m.team.aggregate = teamRoster, "", ""
					m.team.detail.Reset()
				}
				return m, nil, true
			}
			mm, cmd := m.closeAgents()
			return mm, cmd, true
		}
		return m, nil, true
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
		if next, handled := m.navigateAgentsDetail(msg, m.subagents.detail); handled {
			m.subagents.detail = next
			return m, nil, true
		}
		switch {
		case key.Matches(msg, m.keys.Close):
			m.subagents.view = subagentRoster
			m.subagents.child = ""
			m.subagents.detail.Reset()
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
	if key.Matches(msg, m.keys.Close) {
		return m.closeAgents()
	}
	th, hk, width, _ := m.agentsListGeometry()
	if control, handled := m.navigateAgentsList(msg, subagentSelectableList(th, m.subagents, fleet, hk, width)); handled {
		m.subagents.roster = control
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keys.Choose):
		ids := make([]string, len(fleet))
		for i := range fleet {
			ids[i] = fleet[i].childID
		}
		if lane := findFleetLane(fleet, selectedListID(m.subagents.roster, ids)); lane != nil {
			m.subagents.child = lane.childID
			m.subagents.view = subagentFocus
			m.subagents.detail.Reset()
		}
		return m, nil
	case key.Matches(msg, m.keys.CancelChild):
		ids := make([]string, len(fleet))
		for i := range fleet {
			ids[i] = fleet[i].childID
		}
		return m.cancelSubagentLane(findFleetLane(fleet, selectedListID(m.subagents.roster, ids)))
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
	if key.Matches(msg, m.keys.Close) {
		m.parallel.view = parallelRoster
		m.parallel.group = ""
		return m, nil
	}
	if g != nil {
		th, hk, width, _ := m.agentsListGeometry()
		if control, handled := m.navigateAgentsList(msg, parallelBranchSelectableList(th, m.parallel, g, hk, width)); handled {
			m.parallel.branches = control
			return m, nil
		}
	}
	if key.Matches(msg, m.keys.CancelChild) {
		if g == nil {
			return m, nil
		}
		ordered := branchesByIndex(g.branches)
		ids := make([]string, len(ordered))
		for i := range ordered {
			ids[i] = parallelBranchID(g.parentCallID, ordered[i].index)
		}
		selected := selectedListID(m.parallel.branches, ids)
		for i := range g.branches {
			if parallelBranchID(g.parentCallID, g.branches[i].index) == selected {
				if g.branches[i].done {
					return m, nil
				}
				return m.cancelChildByID(g.branches[i].childID, "branch "+branchHumanLabel(g, g.branches[i].index))
			}
		}
		return m, nil
	}
	return m, nil
}

// onParallelRosterKey drives the Parallel group roster: up/down move the selection,
// pgup/pgdn page it, home/g·end/G jump to first/last, enter focuses the selected group by
// ParentCallID, esc closes the overlay. It mirrors onSubagentRosterKey one-for-one.
func (m Model) onParallelRosterKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	groups := m.conv.parallelGroups
	if key.Matches(msg, m.keys.Close) {
		return m.closeAgents()
	}
	th, hk, width, _ := m.agentsListGeometry()
	if control, handled := m.navigateAgentsList(msg, parallelSelectableList(th, m.parallel, groups, hk, width)); handled {
		m.parallel.roster = control
		return m, nil
	}
	if key.Matches(msg, m.keys.Choose) {
		ids := make([]string, len(groups))
		for i := range groups {
			ids[i] = groups[i].parentCallID
		}
		if group := findParallelGroup(groups, selectedListID(m.parallel.roster, ids)); group != nil {
			m.parallel.group = group.parentCallID
			m.parallel.view = parallelGroupView
		}
		return m, nil
	}
	return m, nil
}

func boundedListCursor(list *bounded.List) int {
	if list == nil {
		return 0
	}
	return list.Cursor()
}

// selectedListID reads the semantic cursor when the list has been rendered, and
// falls back to its initial numeric position before the first render builds items.
func selectedListID(list *bounded.List, ids []string) string {
	if list != nil && list.CursorID() != "" {
		return list.CursorID()
	}
	if len(ids) == 0 {
		return ""
	}
	return ids[clampBounded(boundedListCursor(list), len(ids))]
}

func (m Model) navigateAgentsList(msg tea.KeyPressMsg, list agentsSelectableList) (*bounded.List, bool) {
	var move bounded.Move
	switch {
	case key.Matches(msg, m.keys.Up):
		move = bounded.LineUp
	case key.Matches(msg, m.keys.Down):
		move = bounded.LineDown
	case key.Matches(msg, m.keys.ScrollU):
		move = bounded.PageUp
	case key.Matches(msg, m.keys.ScrollD):
		move = bounded.PageDown
	case key.Matches(msg, m.keys.JumpTop):
		move = bounded.Top
	case key.Matches(msg, m.keys.JumpEnd):
		move = bounded.End
	default:
		return list.control, false
	}
	th, _, _, height := m.agentsListGeometry()
	control, capacity, reveal := list.configuredControl(th, height)
	control.ViewWithIndicators(capacity, reveal)
	control.Move(move)
	return control, true
}

func (m Model) agentsListGeometry() (theme.Theme, helpKeys, int, int) {
	th := m.deps.Theme
	layout := newAgentsOverlayLayout(th, m.agentsTab, m.width, m.vp.Height())
	height := 0
	if layout.bounded {
		height = layout.bodyCapacity + layout.frameRows
	}
	return th, m.helpKeyMarkings(), layout.bodyWidth, height
}

// reconcileAgentsLists persists stable cursor and viewport anchors when streamed
// delegation collections change. Renderers receive Model state by value, so they
// cannot be the owner of this update.
func (m *Model) reconcileAgentsLists() {
	if m.team.view == teamNone {
		return
	}
	th, hk, width, height := m.agentsListGeometry()
	if height <= 0 {
		return
	}
	reconcile := func(list agentsSelectableList) *bounded.List {
		control, _ := list.indicatorAdjustedControl(th, height)
		return control
	}
	m.subagents.roster = reconcile(subagentSelectableList(th, m.subagents, m.conv.subagentFleet, hk, width))
	m.parallel.roster = reconcile(parallelSelectableList(th, m.parallel, m.conv.parallelGroups, hk, width))
	if group := findParallelGroup(m.conv.parallelGroups, m.parallel.group); group != nil {
		m.parallel.branches = reconcile(parallelBranchSelectableList(th, m.parallel, group, hk, width))
	}
	if team := m.teamBlockForOverlay(); team != nil {
		m.team.roster = reconcile(teamSelectableList(th, m.team, team, hk, width))
	}
}

func (m Model) onAgentsWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	// Agents predates the modal surface lifecycle. Keep this narrow owner branch
	// until that overlay migrates; it must run before conversation scrolling.
	if m.height > 0 && m.height < 24 {
		return m, nil
	}
	th, hk, width, height := m.agentsListGeometry()
	layout := newAgentsOverlayLayout(th, m.agentsTab, m.width, m.vp.Height())
	if height == 0 || !m.agentsNormalBodyFits(layout, hk) {
		return m, nil
	}
	move := bounded.LineDown
	if msg.Mouse().Button == tea.MouseWheelUp {
		move = bounded.LineUp
	}
	scrollList := func(list agentsSelectableList) *bounded.List {
		control, capacity, reveal := list.configuredControl(th, height)
		control.ViewWithIndicators(capacity, reveal)
		control.Scroll(move)
		control.ViewWithIndicators(capacity, false)
		return control
	}
	switch m.agentsTab {
	case tabSubagents:
		if m.subagents.view == subagentFocus {
			m.subagents.detail, _ = m.moveAgentsDetail(m.subagents.detail, move)
		} else {
			m.subagents.roster = scrollList(subagentSelectableList(th, m.subagents, m.conv.subagentFleet, hk, width))
		}
	case tabParallel:
		if m.parallel.view == parallelGroupView {
			if g := findParallelGroup(m.conv.parallelGroups, m.parallel.group); g != nil {
				m.parallel.branches = scrollList(parallelBranchSelectableList(th, m.parallel, g, hk, width))
			}
		} else {
			m.parallel.roster = scrollList(parallelSelectableList(th, m.parallel, m.conv.parallelGroups, hk, width))
		}
	case tabTeams:
		if m.team.view == teamRoster {
			if b := m.teamBlockForOverlay(); b != nil {
				m.team.roster = scrollList(teamSelectableList(th, m.team, b, hk, width))
			}
		} else {
			m.team.detail, _ = m.moveAgentsDetail(m.team.detail, move)
		}
	}
	return m, nil
}

func (m Model) agentsNormalBodyFits(layout agentsOverlayLayout, hk helpKeys) bool {
	build := prepareAgentsTabBody(
		m.deps.Theme, m.agentsTab, m.subagents, m.parallel, m.team,
		m.teamBlockForOverlay(), m.conv.subagentFleet, m.conv.parallelGroups,
		hk, layout.bodyWidth,
	)
	_, ok := layout.renderBody(build, func() string {
		return renderEssentialAgentsBody(m.deps.Theme, m.agentsTab, m.subagents, m.parallel, m.team, m.teamBlockForOverlay(), m.conv.subagentFleet, m.conv.parallelGroups, hk, layout.bodyWidth)
	})
	return ok
}

func (m Model) navigateAgentsDetail(msg tea.KeyPressMsg, control *bounded.Viewport) (*bounded.Viewport, bool) {
	var move bounded.Move
	switch {
	case key.Matches(msg, m.keys.Up):
		move = bounded.LineUp
	case key.Matches(msg, m.keys.Down):
		move = bounded.LineDown
	case key.Matches(msg, m.keys.ScrollU):
		move = bounded.PageUp
	case key.Matches(msg, m.keys.ScrollD):
		move = bounded.PageDown
	case key.Matches(msg, m.keys.JumpTop):
		move = bounded.Top
	case key.Matches(msg, m.keys.JumpEnd):
		move = bounded.End
	default:
		return control, false
	}
	return m.moveAgentsDetail(control, move)
}

func (m Model) moveAgentsDetail(control *bounded.Viewport, move bounded.Move) (*bounded.Viewport, bool) {
	if control == nil {
		control = new(bounded.Viewport)
	}
	total, window := m.agentsDetailMetrics()
	_, _, width, _ := m.agentsListGeometry()
	control.SetGeometry(width, window, 0, bounded.Clip)
	control.Move(move, total)
	return control, true
}

func (m *Model) clampAgentsDetailScroll() {
	if m.team.view == teamNone {
		return
	}
	total, window := m.agentsDetailMetrics()
	_, _, width, _ := m.agentsListGeometry()
	clamp := func(control *bounded.Viewport) *bounded.Viewport {
		if control == nil {
			control = new(bounded.Viewport)
		}
		control.SetGeometry(width, window, 0, bounded.Clip)
		control.Clamp(total)
		return control
	}
	switch m.agentsTab {
	case tabSubagents:
		if m.subagents.view == subagentFocus {
			m.subagents.detail = clamp(m.subagents.detail)
		}
	case tabTeams:
		if m.team.view == teamFocus || m.team.view == teamTasks || m.team.view == teamFindings {
			m.team.detail = clamp(m.team.detail)
		}
	}
}

func (m Model) agentsDetailMetrics() (int, int) {
	th, hk := m.deps.Theme, m.helpKeyMarkings()
	layout := newAgentsOverlayLayout(th, m.agentsTab, m.width, m.vp.Height())
	windowFor := func(total int, rows func(int) int, build func(int) string) (int, int) {
		if !layout.bounded {
			return total, max(1, rows(0))
		}
		for capacity := layout.bodyCapacity; capacity > 0; capacity-- {
			height := capacity + layout.frameRows
			if lipgloss.Height(build(height)) <= layout.bodyCapacity {
				return total, max(1, rows(height))
			}
		}
		return total, 1
	}
	switch m.agentsTab {
	case tabSubagents:
		if ln := findFleetLane(m.conv.subagentFleet, m.subagents.child); ln != nil {
			total := len(renderedTraceLines(th, hk, layout.bodyWidth, ln.trace))
			build := prepareSubagentFocusAt(th, m.conv.subagentFleet, m.subagents.child, m.subagents.detail, hk, layout.bodyWidth)
			return windowFor(total, teamFocusRows, build)
		}
	case tabTeams:
		if b := m.teamBlockForOverlay(); b != nil {
			switch m.team.view {
			case teamFocus:
				if ln := teamFindLane(b, m.team.member); ln != nil {
					total := len(renderedTraceLines(th, hk, layout.bodyWidth, ln.trace))
					build := prepareTeamFocusAt(th, b, m.team.member, m.team.detail, hk, layout.bodyWidth)
					return windowFor(total, teamFocusRows, build)
				}
			case teamTasks:
				total := len(renderedTaskLines(th, b, layout.bodyWidth))
				build := prepareTeamTasksAt(th, b, m.team.detail, hk, layout.bodyWidth)
				return windowFor(total, teamTasksRows, build)
			case teamFindings:
				total := len(renderedFindingLines(th, b, layout.bodyWidth))
				build := prepareTeamFindingsAt(th, b, m.team.detail, hk, layout.bodyWidth)
				return windowFor(total, teamFindingsRows, build)
			}
		}
	}
	return 0, 1
}

func renderedTraceLines(th theme.Theme, hk helpKeys, width int, trace []teamTrace) []string {
	r := &renderer{th: th, marks: hk, traceWidth: width}
	if rendered := r.renderTrace(trace); rendered != "" {
		return strings.Split(rendered, "\n")
	}
	return nil
}

// renderAgentsOverlay draws the active unified agents overlay centred over the
// conversation region. It prepends a one-line tab bar (Subagents | Parallel | Teams,
// active tab highlighted) above the active tab's body, then frames the whole thing in the
// shared card. The team block may be nil (no team yet) — the Teams tab then shows an
// honest empty note rather than borrowing another tab's body.
func renderAgentsOverlay(th theme.Theme, tab agentsTab, sub subagentState, par parallelState, team teamState, b *teamOverlaySnapshot, fleet []subagentLane, groups []parallelGroup, hk helpKeys, width, height int, terminalHeight ...int) string {
	// The conversation viewport can be shorter than the terminal because of the
	// surrounding chrome. Compact is a terminal-height fallback, not a viewport
	// fallback: a 24-row terminal still receives the normal layout attempt.
	terminal := height
	if len(terminalHeight) > 0 {
		terminal = terminalHeight[0]
	}
	if terminal > 0 && terminal < 24 {
		return renderCompactAgentsOverlay(th, tab, sub, par, team, hk, width)
	}

	layout := newAgentsOverlayLayout(th, tab, width, height)
	build := prepareAgentsTabBody(th, tab, sub, par, team, b, fleet, groups, hk, layout.bodyWidth)
	body, ok := layout.renderBody(build, func() string {
		return renderEssentialAgentsBody(th, tab, sub, par, team, b, fleet, groups, hk, layout.bodyWidth)
	})
	if !ok {
		return renderViewportAgentsFallback(th, tab, sub, par, team, hk, width)
	}
	return centerAgentsCard(th, layout.tabStrip+"\n"+body, layout.outerWidth, width, height)
}

// agentsOverlayLayout is the overlay's single physical-line boundary. It derives
// the usable body budget from askCard's actual frame and the already-rendered,
// ANSI-safe tab strip. Tab bodies receive only that remaining budget; framing is
// attempted only after their complete rendered rows fit it.
type agentsOverlayLayout struct {
	tabStrip                string
	outerWidth, bodyWidth   int
	bodyCapacity, frameRows int
	bounded                 bool
}

func newAgentsOverlayLayout(th theme.Theme, tab agentsTab, width, height int) agentsOverlayLayout {
	card, outerWidth, bodyWidth := agentsCardLayout(th, width)
	tabStrip := agentsTabBar(th, tab)
	if bodyWidth > 0 {
		tabStrip = ansi.Hardwrap(tabStrip, bodyWidth, true)
	}
	layout := agentsOverlayLayout{
		tabStrip:   tabStrip + "\n", // the blank separator is fixed tab chrome
		outerWidth: outerWidth,
		bodyWidth:  bodyWidth,
		frameRows:  card.GetVerticalFrameSize(),
		bounded:    height > 0,
	}
	if layout.bounded {
		// lipgloss height ignores a trailing newline, so count the tab-bar rows
		// plus the explicit blank separator independently.
		tabChromeRows := lipgloss.Height(layout.tabStrip) + 1
		layout.bodyCapacity = height - layout.frameRows - tabChromeRows
	}
	return layout
}

// renderBody lets the existing section renderers reduce their content window
// against the real physical result. It never crops assembled output: a body is
// accepted whole, or the normal card is declined. The added frameRows translate
// the body-only capacity to the historical renderer-height convention while R2/R3
// replace the current cursor windows.
func (l agentsOverlayLayout) renderBody(build func(int) string, essential func() string) (string, bool) {
	if !l.bounded {
		return build(0), true
	}
	if l.bodyCapacity <= 0 {
		return "", false
	}
	for capacity := l.bodyCapacity; capacity > 0; capacity-- {
		body := build(capacity + l.frameRows)
		if lipgloss.Height(body) <= l.bodyCapacity {
			return body, true
		}
	}
	body := essential()
	if lipgloss.Height(body) <= l.bodyCapacity {
		return body, true
	}
	return "", false
}

// renderEssentialAgentsBody is the normal card's smallest honest body. Each
// section is rendered as a complete, width-bounded physical line before joining:
// context, the selected/current row when one exists, and the live footer. It is
// used only when optional metadata cannot fit the offered body capacity.
type agentsEssentialBody struct {
	title, selected, footer string
	extra                   []string
}

type agentsEssentialLine func(lipgloss.Style, string, string) string

func renderEssentialAgentsBody(th theme.Theme, tab agentsTab, sub subagentState, par parallelState, team teamState, b *teamOverlaySnapshot, fleet []subagentLane, groups []parallelGroup, hk helpKeys, width int) string {
	line := func(style lipgloss.Style, prefix, text string) string {
		return renderDynamicCardChromeLine(style, prefix, text, width)
	}
	var body agentsEssentialBody
	switch tab {
	case tabSubagents:
		body = essentialSubagentBody(th, sub, fleet, hk, line, width)
	case tabParallel:
		body = essentialParallelBody(th, par, groups, hk, line)
	default:
		body = essentialTeamBody(th, team, b, hk, line)
	}
	if body.selected == "" {
		body.selected = line(th.Style("muted"), "", "(no entries)")
	}
	lines := []string{body.title, body.selected}
	lines = append(lines, body.extra...)
	lines = append(lines, body.footer)
	return strings.Join(lines, "\n")
}

func essentialSubagentBody(th theme.Theme, st subagentState, fleet []subagentLane, hk helpKeys, line agentsEssentialLine, bodyWidth int) agentsEssentialBody {
	body := agentsEssentialBody{
		title:  line(th.Style("askTitle"), "", "subagents"),
		footer: line(th.Style("muted"), "", agentsEmptyHint(hk)),
	}
	if st.view != subagentFocus {
		if len(fleet) > 0 {
			body.selected = renderSubagentRosterTitle(th.Style("spinner"), "▶ ", &fleet[clampBounded(boundedListCursor(st.roster), len(fleet))], bodyWidth)
		}
		return body
	}
	body.footer = line(th.Style("muted"), "", focusBackHint(hk))
	if lane := findFleetLane(fleet, st.child); lane != nil {
		body.title = line(th.Style("askTitle"), "", "subagent")
		body.selected = renderEssentialSubagentFocus(th.Style("muted"), lane, bodyWidth)
		if len(lane.trace) > 0 {
			body.extra = append(body.extra, line(th.Style("muted"), "  ", fmt.Sprintf("… +%d more lines", len(lane.trace))))
		}
	} else {
		body.selected = line(th.Style("muted"), "", "subagent #"+shortChildID(st.child)+" is no longer in the fleet")
	}
	return body
}

func essentialParallelBody(th theme.Theme, st parallelState, groups []parallelGroup, hk helpKeys, line agentsEssentialLine) agentsEssentialBody {
	body := agentsEssentialBody{
		title:  line(th.Style("askTitle"), "", "parallel"),
		footer: line(th.Style("muted"), "", agentsEmptyHint(hk)),
	}
	if st.view != parallelGroupView {
		if len(groups) > 0 {
			body.selected = line(th.Style("spinner"), "▶ ", parallelRosterLine(&groups[clampBounded(boundedListCursor(st.roster), len(groups))]))
		}
		return body
	}
	body.footer = line(th.Style("muted"), "", focusBackHint(hk))
	group := findParallelGroup(groups, st.group)
	if group == nil {
		body.selected = line(th.Style("muted"), "", "this parallel run is no longer tracked")
		return body
	}
	body.title = line(th.Style("askTitle"), "", "parallel · join="+parallelJoinMode(group.join))
	ordered := branchesByIndex(group.branches)
	if len(ordered) > 0 {
		body.selected = line(th.Style("spinner"), "▶ ", parallelBranchLine(&ordered[clampBounded(boundedListCursor(st.branches), len(ordered))]))
		body.extra = append(body.extra, line(th.Style("muted"), "  ", fmt.Sprintf("· +%d more branch(es)", max(0, len(ordered)-1))))
	}
	return body
}

func essentialTeamBody(th theme.Theme, st teamState, b *teamOverlaySnapshot, hk helpKeys, line agentsEssentialLine) agentsEssentialBody {
	body := agentsEssentialBody{
		title:  line(th.Style("askTitle"), "", "agents"),
		footer: line(th.Style("muted"), "", agentsEmptyHint(hk)),
	}
	if b == nil {
		body.selected = line(th.Style("muted"), "", "no team has run this session")
		return body
	}
	switch st.view {
	case teamFocus:
		body.footer = line(th.Style("muted"), "", focusBackHint(hk))
		if lane := teamFindLane(b, st.member); lane != nil {
			body.title = line(th.Style("askTitle"), "", "agent · "+truncate(terminaltext.Sanitize(lane.name), maxTeamNameWidth))
			body.selected = line(th.Style("muted"), "", teamLaneLine(lane, 0, b.teamDone))
			if len(lane.trace) > 0 {
				body.extra = append(body.extra, line(th.Style("muted"), "  ", fmt.Sprintf("… +%d more lines", len(lane.trace))))
			}
		} else {
			body.selected = line(th.Style("muted"), "", "member "+terminaltext.Sanitize(st.member)+" is no longer in the roster")
		}
	case teamTasks:
		body.title = line(th.Style("askTitle"), "", "tasks")
		body.footer = line(th.Style("muted"), "", teamSubViewHint(hk, hk.tasks))
		if len(b.teamTasks) > 0 {
			byID := make(map[string]string, len(b.teamTasks))
			for _, task := range b.teamTasks {
				byID[task.id] = task.state
			}
			body.selected = line(th.Style("muted"), "  ", taskRow(b.teamTasks[0], byID))
		}
	case teamFindings:
		body.title = line(th.Style("askTitle"), "", "findings")
		body.footer = line(th.Style("muted"), "", teamSubViewHint(hk, hk.findings))
		if len(b.teamFindings) > 0 {
			body.selected = line(th.Style("muted"), "  ", findingRow(b.teamFindings[0]))
		}
	default:
		order := teamLaneOrder(b.teamLanes)
		if len(order) > 0 {
			lane := &b.teamLanes[order[clampBounded(boundedListCursor(st.roster), len(order))]]
			body.selected = line(th.Style("spinner"), "▶ ", teamRosterLine(th, lane, teamNameWidth(b.teamLanes, order), b.teamDone))
		}
	}
	return body
}

// renderViewportAgentsFallback is distinct from terminal-height compact mode: it
// has no selection marker and explicitly says that the offered conversation area,
// rather than the terminal, is too short for the normal frame and essential footer.
func renderViewportAgentsFallback(th theme.Theme, tab agentsTab, sub subagentState, par parallelState, team teamState, hk helpKeys, width int) string {
	label, focus := "Subagents", sub.view == subagentFocus
	switch tab {
	case tabParallel:
		label, focus = "Parallel", par.view == parallelGroupView
	case tabTeams:
		label, focus = "Teams", team.view == teamFocus || team.view == teamTasks || team.view == teamFindings
	}
	hint := hk.closeOnly + " close"
	if focus {
		hint = focusBackHint(hk)
	}
	return renderDynamicCardChromeLine(th.Style("askTitle"), "", label+" · vp short · "+hint, width)
}

// renderCompactAgentsOverlay is deliberately unframed: on a short terminal a
// card cannot honestly fit. It retains only the active context and escape action.
func renderCompactAgentsOverlay(th theme.Theme, tab agentsTab, sub subagentState, par parallelState, team teamState, hk helpKeys, width int) string {
	label := "Subagents"
	focus := false
	switch tab {
	case tabParallel:
		label, focus = "Parallel", par.view == parallelGroupView
		if focus && par.group != "" {
			label = "parallel " + terminaltext.Sanitize(par.group)
		}
	case tabTeams:
		label, focus = "Teams", team.view != teamRoster
		switch team.view {
		case teamTasks:
			label = "Tasks"
		case teamFindings:
			label = "Findings"
		}
	default:
		focus = sub.view == subagentFocus
	}
	if focus {
		if tab == tabSubagents && sub.child != "" {
			label = "subagent " + shortChildID(sub.child)
		}
		if tab == tabTeams && team.member != "" {
			label = "agent " + terminaltext.Sanitize(team.member)
		}
		return renderDynamicCardChromeLine(th.Style("askTitle"), "", "▶ "+label+" · "+focusBackHint(hk), width)
	}
	return renderDynamicCardChromeLine(th.Style("askTitle"), "", "▶ "+label+" · "+hk.closeOnly+" close", width)
}

// agentsCardLayout derives the final card's outer and usable body widths from
// askCard's actual frame. Rows receive bodyWidth; only the frame receives
// outerWidth, so neither Lipgloss nor the frame has a second wrap to perform.
func agentsCardLayout(th theme.Theme, width int) (card lipgloss.Style, outerWidth, bodyWidth int) {
	card = th.Style("askCard")
	if width <= 0 {
		return card, 0, 0
	}
	const (
		margin  = 4
		maxBody = 100
	)
	frame := card.GetHorizontalFrameSize()
	outerWidth = min(max(1, width-margin), maxBody+frame)
	if outerWidth <= frame {
		card = lipgloss.NewStyle().Width(outerWidth)
		return card, outerWidth, outerWidth
	}
	return card, outerWidth, outerWidth - frame
}

// centerAgentsCard frames an already width-bounded delegation overlay.
func centerAgentsCard(th theme.Theme, body string, outerWidth, width, height int) string {
	card, _, _ := agentsCardLayout(th, 0)
	if outerWidth > 0 {
		card = card.Width(outerWidth)
	}
	out := card.Render(body)
	if width <= 0 || height <= 0 {
		return out
	}
	// Do not use lipgloss.Place here: Place silently clips an oversized child.
	// The physical layout boundary guarantees height; this only adds centering whitespace.
	rows := strings.Split(out, "\n")
	for i, row := range rows {
		pad := max(0, (width-lipgloss.Width(row))/2)
		rows[i] = strings.Repeat(" ", pad) + row
	}
	out = strings.Join(rows, "\n")
	remaining := max(0, height-lipgloss.Height(out))
	top := remaining / 2
	bottom := remaining - top
	return strings.Repeat("\n", top) + out + strings.Repeat("\n", bottom)
}

// agentsEmptyHint is the "tab switch · esc close" footer used by the empty
// subagent/parallel/team-tab states. The chords read the LIVE NextTab/Close
// markings so an override propagates (issue #457); with defaults it is
// byte-identical to the historical literal.
func agentsEmptyHint(hk helpKeys) string {
	return hk.nextTab + " switch · " + hk.closeOnly + " close"
}

// focusBackHint is the "esc back" footer used by the subagent/team/parallel
// focus panes. The chord reads the LIVE Close marking (the focus handlers
// drive back-to-roster via key.Matches(m.keys.Close)) so an override propagates
// (issue #457); with the default it is byte-identical to "esc back".
func focusBackHint(hk helpKeys) string {
	return hk.closeOnly + " back"
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

type agentsBodyRenderer func(height int) string

func prepareAgentsTabBody(th theme.Theme, tab agentsTab, sub subagentState, par parallelState, team teamState, b *teamOverlaySnapshot, fleet []subagentLane, groups []parallelGroup, hk helpKeys, bodyWidth int) agentsBodyRenderer {
	switch tab {
	case tabSubagents:
		return prepareSubagentTab(th, sub, fleet, hk, bodyWidth)
	case tabParallel:
		return prepareParallelTab(th, par, groups, hk, bodyWidth)
	default:
		return prepareTeamsTab(th, team, b, hk, bodyWidth)
	}
}

// renderTeamsTab renders the Teams tab body — the EXISTING team overlay roster /
// focus / tasks / findings sub-views verbatim, via the team.go renderers. A nil team
// block (no team has run) reads as an honest empty note so the tab is never blank.
// bodyWidth is the final card's usable row budget; callers derive it once from
// askCard and never ask a row renderer to subtract card chrome again.
func renderTeamsTab(th theme.Theme, st teamState, b *teamOverlaySnapshot, hk helpKeys, bodyWidth, height int) string {
	return prepareTeamsTab(th, st, b, hk, bodyWidth)(height)
}

func prepareTeamsTab(th theme.Theme, st teamState, b *teamOverlaySnapshot, hk helpKeys, bodyWidth int) agentsBodyRenderer {
	if b == nil {
		muted := th.Style("muted")
		body := renderDynamicCardChromeLine(muted, "", "no team has run this session", bodyWidth) + "\n\n" +
			renderDynamicCardChromeLine(muted, "", agentsEmptyHint(hk), bodyWidth)
		return func(int) string { return body }
	}
	switch st.view {
	case teamFocus:
		return prepareTeamFocusAt(th, b, st.member, st.detail, hk, bodyWidth)
	case teamTasks:
		return prepareTeamTasksAt(th, b, st.detail, hk, bodyWidth)
	case teamFindings:
		return prepareTeamFindingsAt(th, b, st.detail, hk, bodyWidth)
	default:
		list := teamSelectableList(th, st, b, hk, bodyWidth)
		return func(height int) string { return list.render(th, height) }
	}
}

// renderSubagentTab renders the Subagents tab body: the flat fleet roster, or one
// bodyWidth is the final card's usable row budget, shared by every focus and
// roster renderer beneath the already-framed overlay.
func renderSubagentTab(th theme.Theme, st subagentState, fleet []subagentLane, hk helpKeys, bodyWidth, height int) string {
	return prepareSubagentTab(th, st, fleet, hk, bodyWidth)(height)
}

func prepareSubagentTab(th theme.Theme, st subagentState, fleet []subagentLane, hk helpKeys, bodyWidth int) agentsBodyRenderer {
	if st.view == subagentFocus {
		return prepareSubagentFocusAt(th, fleet, st.child, st.detail, hk, bodyWidth)
	}
	if len(fleet) == 0 {
		body := renderSubagentRoster(th, st, fleet, hk, 0, bodyWidth)
		return func(int) string { return body }
	}
	list := subagentSelectableList(th, st, fleet, hk, bodyWidth)
	return func(height int) string { return list.render(th, height) }
}

// renderSubagentRoster renders the flat fleet roster WINDOWED to the available height,
// mirroring renderTeamRoster: a header (running/done counts), the slice of rows that
// fits with the selected row marked with the unbordered ▶ treatment, "+K above/below" tails, and an always-visible
// footer hint. An empty fleet reads as a muted "(no subagents)". height<=0 shows all.
type agentsSelectableList struct {
	header, footer string
	rows           []string
	ids            []string
	statusCells    [][2]string
	gutterCells    int
	cursor         int
	muted          lipgloss.Style
	noun           string
	bodyWidth      int
	control        *bounded.List
}

const agentsRosterSectionSeparators = 2 // blank lines between header/list and list/footer

func (l agentsSelectableList) configuredControl(th theme.Theme, height int) (*bounded.List, int, bool) {
	capacity := 1 << 20
	if height > 0 {
		capacity = height - th.Style("askCard").GetVerticalFrameSize() -
			lipgloss.Height(l.header) - lipgloss.Height(l.footer) - agentsRosterSectionSeparators
	}
	items := make([]bounded.ListItem, len(l.rows))
	for i := range l.rows {
		id := fmt.Sprintf("row-%d", i)
		if i < len(l.ids) && l.ids[i] != "" {
			id = l.ids[i]
		}
		status := [2]string{}
		if i < len(l.statusCells) {
			status = l.statusCells[i]
		}
		items[i] = bounded.ListItem{ID: id, Text: strings.ReplaceAll(l.rows[i], "\n    ", "\n  "), StatusCells: status}
	}
	if l.control == nil {
		l.control = new(bounded.List)
	}
	hadCursor := l.control.CursorID() != ""
	width := l.bodyWidth
	if width <= 0 {
		width = 1 << 20
	}
	l.control.SetGeometry(width, max(0, capacity), max(1, l.gutterCells), bounded.Wrap)
	l.control.SetItems(items)
	reveal := !hadCursor || l.control.RevealPending()
	if !hadCursor {
		l.control.SetCursor(l.cursor)
	}
	return l.control, max(0, capacity), reveal
}

func (l agentsSelectableList) indicatorAdjustedControl(th theme.Theme, height int) (*bounded.List, bounded.ListView) {
	control, capacity, reveal := l.configuredControl(th, height)
	return control, control.ViewWithIndicators(capacity, reveal)
}

func (l agentsSelectableList) boundedView(th theme.Theme, height int) bounded.ListView {
	_, view := l.indicatorAdjustedControl(th, height)
	return view
}

func (l agentsSelectableList) render(th theme.Theme, height int) string {
	view := l.boundedView(th, height)
	var middle []string
	if len(view.Rows) == 0 && len(l.rows) > 0 {
		index := clampBounded(l.cursor, len(l.rows))
		status := [2]string{}
		if index < len(l.statusCells) {
			status = l.statusCells[index]
		}
		presentation := presentListRow(bounded.ListRow{
			Text: l.rows[index], Selected: true, CursorMarker: true,
			StatusCells: status, GutterCells: l.gutterCells,
		}, th.Style("spinner"), l.muted)
		middle = append(middle, presentation.Style.Render(presentation.Text))
	}
	if view.Above > 0 {
		count, noun := view.Rows[0].ItemIndex, l.noun
		if count == 0 {
			count, noun = view.Above, "lines"
		}
		middle = append(middle, renderDelegationRows(l.muted, "  ", fmt.Sprintf("· +%d %s above", count, noun), 0))
	}
	for _, row := range view.Rows {
		presentation := presentListRow(row, th.Style("spinner"), l.muted)
		middle = append(middle, presentation.Style.Render(presentation.Text))
	}
	if view.Below > 0 {
		count, noun := len(l.rows)-view.Rows[len(view.Rows)-1].ItemIndex-1, l.noun
		if count == 0 {
			count, noun = view.Below, "lines"
		}
		middle = append(middle, renderDelegationRows(l.muted, "  ", fmt.Sprintf("· +%d %s below", count, noun), 0))
	}
	return l.header + "\n\n" + strings.Join(middle, "\n") + "\n\n" + l.footer
}

func subagentSelectableList(th theme.Theme, st subagentState, fleet []subagentLane, hk helpKeys, bodyWidth int) agentsSelectableList {
	muted := th.Style("muted")
	running, done := fleetCounts(fleet)
	list := agentsSelectableList{
		header: renderDelegationRows(th.Style("askTitle"), "", subagentRosterHeader(running, done), bodyWidth),
		footer: renderDynamicCardChromeLine(muted, "", hk.navUp+"/"+hk.navDown+" select · "+hk.scroll+" · "+hk.jumpTop+"/"+hk.jumpEnd+" · "+hk.choose+" focus · "+hk.cancelChild+" cancel · "+agentsEmptyHint(hk), bodyWidth),
		cursor: clampBounded(boundedListCursor(st.roster), len(fleet)), muted: muted, noun: "rows",
		bodyWidth: bodyWidth, control: st.roster,
	}
	for row := range fleet {
		list.ids = append(list.ids, fleet[row].childID)
		list.rows = append(list.rows, subagentRosterText(&fleet[row], bodyWidth, 2))
	}
	return list
}

func renderSubagentRoster(th theme.Theme, st subagentState, fleet []subagentLane, hk helpKeys, height int, widths ...int) string {
	bodyWidth := 0
	if len(widths) > 0 {
		bodyWidth = widths[0]
	}
	if len(fleet) == 0 {
		muted := th.Style("muted")
		return renderDelegationRows(th.Style("askTitle"), "", subagentRosterHeader(0, 0), bodyWidth) + "\n\n" +
			renderDynamicCardChromeLine(muted, "", "(no subagents)", bodyWidth) + "\n\n" +
			renderDynamicCardChromeLine(muted, "", agentsEmptyHint(hk), bodyWidth)
	}
	return subagentSelectableList(th, st, fleet, hk, bodyWidth).render(th, height)
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

// subagentRosterLine returns the unstyled two-line fleet row. The roster and focus
// callers supply their physical width through subagentRosterText; this form keeps the
// metadata useful to narrow, unframed callers too.
func subagentRosterLine(ln *subagentLane) string {
	return subagentRosterText(ln, 0, 0)
}

func renderSubagentRosterTitle(style lipgloss.Style, prefix string, ln *subagentLane, bodyWidth int) string {
	return style.Render(prefix + subagentRosterTitle(ln, bodyWidth, lipgloss.Width(prefix)))
}

func renderEssentialSubagentFocus(style lipgloss.Style, ln *subagentLane, bodyWidth int) string {
	goal := terminaltext.Sanitize(ln.goal)
	if goal == "" {
		goal = "subagent"
	}
	if bodyWidth > 0 {
		goal = truncateDisplayWidth(goal, max(1, bodyWidth-lipgloss.Width(subagentLaneGlyph(ln))-1))
	} else {
		goal = truncate(goal, maxSubagentGoalLen)
	}
	return style.Render(subagentLaneGlyph(ln) + " " + goal)
}

func subagentRosterText(ln *subagentLane, bodyWidth, titlePrefixWidth int) string {
	return subagentRosterTitle(ln, bodyWidth, titlePrefixWidth) + "\n" +
		hangingIndentWrap(subagentRosterDetails(ln), "    ", bodyWidth)
}

func subagentRosterTitle(ln *subagentLane, bodyWidth, titlePrefixWidth int) string {
	marker := ""
	if ln.background {
		marker = " " + subagentBackgroundMarker
	}
	suffix := " #" + shortChildID(ln.childID) + marker
	goal := terminaltext.Sanitize(ln.goal)
	if goal == "" {
		goal = "subagent"
	}
	if bodyWidth > 0 {
		goal = truncateDisplayWidth(goal, max(1, bodyWidth-titlePrefixWidth-lipgloss.Width(subagentLaneGlyph(ln)+suffix)-1))
	} else {
		goal = truncate(goal, maxSubagentGoalLen)
	}

	title := subagentLaneGlyph(ln) + " " + goal + suffix
	if bodyWidth > 0 {
		return truncateDisplayWidth(title, max(1, bodyWidth-titlePrefixWidth))
	}
	return title
}

func subagentRosterDetails(ln *subagentLane) string {
	details := []string{}
	if routed := delegationModelLabel(ln.routedCategory, ln.routedModel, ln.routingReason, ln.model, ln.routingDecision); routed != "" {
		details = append(details, routed)
	}
	details = append(details,
		subagentLaneState(ln),
		plural(ln.toolCount, "tool"),
		"↑"+humanizeTokens(ln.usage.InputTokens)+" ↓"+humanizeTokens(ln.usage.OutputTokens))
	return strings.Join(details, " · ")
}

// truncateDisplayWidth clips a terminal-sanitized title without allowing a wide rune
// to push the title line past the physical card width.
func truncateDisplayWidth(s string, width int) string {
	return ansi.Truncate(s, width, "…")
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
	return subagentFailureLineAtWidth(ln, focusCardTextWidth(width))
}

func subagentFailureLineAtWidth(ln *subagentLane, bodyWidth int) string {
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
	return indentWrap("failed: "+truncate(terminaltext.Sanitize(strings.Join(strings.Fields(ln.cause), " ")), maxSubagentCauseWidth), bodyWidth)
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
		return truncate(terminaltext.Sanitize(ln.current), maxTraceToolNameLen) + "…"
	}
	return "working…"
}

// shortChildID renders a stable short suffix of a ChildID for the fleet row, so two
// children with identical goal labels are still visually distinct (F2 §1.4 item 6).
// It takes the LAST up-to-childIDHashLen runes of the id (the id's tail carries the
// per-call discriminator, e.g. "explorer-<callID>"), sanitized.
func shortChildID(id string) string {
	id = terminaltext.Sanitize(id)
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
func renderSubagentFocus(th theme.Theme, fleet []subagentLane, child string, hk helpKeys, bodyWidth, height int) string {
	return renderSubagentFocusAt(th, fleet, child, new(bounded.Viewport), hk, bodyWidth, height)
}

func renderSubagentFocusAt(th theme.Theme, fleet []subagentLane, child string, detail *bounded.Viewport, hk helpKeys, bodyWidth, height int) string {
	return prepareSubagentFocusAt(th, fleet, child, detail, hk, bodyWidth)(height)
}

func prepareSubagentFocusAt(th theme.Theme, fleet []subagentLane, child string, detail *bounded.Viewport, hk helpKeys, bodyWidth int) agentsBodyRenderer {
	if detail == nil {
		detail = new(bounded.Viewport)
	}
	muted := th.Style("muted")
	ln := findFleetLane(fleet, child)
	if ln == nil {
		body := renderDynamicCardChromeLine(th.Style("askTitle"), "", "subagent", bodyWidth) + "\n\n" +
			renderDynamicCardChromeLine(muted, "", "subagent #"+shortChildID(child)+" is no longer in the fleet", bodyWidth) + "\n\n" +
			renderDynamicCardChromeLine(muted, "", focusBackHint(hk), bodyWidth)
		return func(int) string { return body }
	}

	var out strings.Builder
	goal := truncate(terminaltext.Sanitize(ln.goal), maxTeamNameWidth*2)
	if goal == "" {
		goal = "subagent"
	}
	out.WriteString(th.Style("askTitle").Render(wrapFocusMetadataAtWidth("subagent · "+goal, bodyWidth)))
	out.WriteString("\n")
	out.WriteString(muted.Render(subagentRosterText(ln, bodyWidth, 0)))
	if detail := routingDecisionDetail(ln.routingDecision, ln.model, ln.routingReason); detail != "" {
		out.WriteString("\n")
		out.WriteString(muted.Render(hangingIndentWrap(detail, "  ", bodyWidth)))
	}
	out.WriteString("\n")
	out.WriteString(muted.Render(hangingIndentWrap(boundedPreviewsSubNote, "  ", bodyWidth)))
	if fail := subagentFailureLineAtWidth(ln, bodyWidth); fail != "" {
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
		note := "background: runs detached; the agent collects its result via SubagentStatus"
		if ln.done {
			note = "background: done — result ready for the agent (SubagentStatus)"
		}
		for _, row := range strings.Split(hangingIndentWrap(note, "  ", bodyWidth), "\n") {
			out.WriteString("\n")
			out.WriteString(muted.Render(row))
		}
	}
	out.WriteString("\n\n")
	prefix := out.String()

	traceLines := renderedTraceLines(th, hk, bodyWidth, ln.trace)
	lead := focusBackHint(hk)
	if !ln.done {
		lead = hk.cancelChild + " cancel · " + lead
	}
	return func(height int) string {
		control := *detail
		control.SetGeometry(bodyWidth, teamFocusRows(height), 0, bounded.Clip)
		view := control.View(traceLines)
		rows, above, below := view.Rows, view.Above, view.Below
		body := prefix
		if len(traceLines) == 0 {
			body += muted.Render("(no activity yet)")
		} else {
			body += strings.Join(rows, "\n")
		}
		w := renderedLineWindowBounds{start: above, end: above + len(rows), total: above + len(rows) + below, window: control.Height()}
		hint := agentsDetailHint(hk, w, lead)
		return body + "\n\n" + renderDynamicCardChromeLine(muted, "", hint, bodyWidth)
	}
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
func renderParallelTab(th theme.Theme, st parallelState, groups []parallelGroup, hk helpKeys, width, height int) string {
	return prepareParallelTab(th, st, groups, hk, width)(height)
}

func prepareParallelTab(th theme.Theme, st parallelState, groups []parallelGroup, hk helpKeys, width int) agentsBodyRenderer {
	if st.view == parallelGroupView {
		return prepareParallelGroupFocus(th, st, groups, hk, width)
	}
	if len(groups) == 0 {
		body := renderParallelRoster(th, st, groups, hk, 0, width)
		return func(int) string { return body }
	}
	list := parallelSelectableList(th, st, groups, hk, width)
	return func(height int) string { return list.render(th, height) }
}

// renderParallelRoster renders the Parallel group roster WINDOWED to the available height,
// mirroring renderSubagentRoster: a header (running/done group counts), the slice of rows
// that fits with the selected row marked with the unbordered ▶ treatment, "+K above/below" tails, and a footer hint.
// An empty group list reads as a muted "(no parallel runs)". height<=0 shows all.
func parallelSelectableList(th theme.Theme, st parallelState, groups []parallelGroup, hk helpKeys, bodyWidth int) agentsSelectableList {
	muted := th.Style("muted")
	running, done := parallelCounts(groups)
	list := agentsSelectableList{
		header: renderDelegationRows(th.Style("askTitle"), "", fmt.Sprintf("parallel · %d running · %d done", running, done), bodyWidth),
		footer: renderCardChromeSegments(muted, []string{hk.closeOnly + " close", hk.navUp + "/" + hk.navDown + " select", hk.choose + " focus", hk.nextTab + " switch", hk.scroll + " page", hk.jumpTopFull + "·" + hk.jumpEndFull + " first/last"}, bodyWidth),
		cursor: clampBounded(boundedListCursor(st.roster), len(groups)), muted: muted, noun: "rows",
		bodyWidth: bodyWidth, control: st.roster,
	}
	for row := range groups {
		list.ids = append(list.ids, groups[row].parentCallID)
		list.rows = append(list.rows, parallelRosterLine(&groups[row]))
	}
	return list
}

func renderParallelRoster(th theme.Theme, st parallelState, groups []parallelGroup, hk helpKeys, height int, widths ...int) string {
	bodyWidth := 0
	if len(widths) > 0 {
		bodyWidth = widths[0]
	}
	if len(groups) == 0 {
		muted := th.Style("muted")
		return renderDelegationRows(th.Style("askTitle"), "", "parallel · 0 running · 0 done", bodyWidth) + "\n\n" +
			renderDynamicCardChromeLine(muted, "", "(no parallel runs)", bodyWidth) + "\n\n" +
			renderDynamicCardChromeLine(muted, "", agentsEmptyHint(hk), bodyWidth)
	}
	return parallelSelectableList(th, st, groups, hk, bodyWidth).render(th, height)
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
	join := parallelJoinMode(g.join)
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
			return truncate(terminaltext.Sanitize(g.branches[i].label), maxParallelBranchLabelLen)
		}
	}
	return fmt.Sprintf("branch-%d", index+1)
}

// renderParallelGroupFocus renders ONE Parallel group's detail (ONE level — plan Q4): a
// header (join + branch tally + run stop), the bounded-previews honesty note (the
// previews are bounded + scrubbed + client-only per ADR 0079 — gauntlet #7 is about the
// conversation, not the client), every branch inline (glyph + label + goal +
// current/last tool + count + usage; the SELECTED row carries the "▶" cursor the `x`
// cancel key addresses, the WINNER row a "★") with its interleaved trace in the same
// physical cursor window. A focused ParentCallID with no matching
// group reads as a muted note.
func parallelBranchSelectableList(th theme.Theme, st parallelState, g *parallelGroup, hk helpKeys, bodyWidth int) agentsSelectableList {
	muted := th.Style("muted")
	join := parallelJoinMode(g.join)
	if join == "" {
		join = "all"
	}
	header := th.Style("askTitle").Render(wrapFocusMetadataAtWidth("parallel · join="+join, bodyWidth)) + "\n" +
		muted.Render(wrapFocusMetadataAtWidth(parallelRosterLine(g), bodyWidth))
	if g.done && g.stop != "" {
		header += "\n" + muted.Render(wrapFocusMetadataAtWidth("run stop: "+subagentStopLabel(g.stop), bodyWidth))
	}
	header += "\n" + muted.Render(indentWrap(boundedPreviewsParNote, bodyWidth))
	ordered := branchesByIndex(g.branches)
	cursor := clampBounded(boundedListCursor(st.branches), len(ordered))
	cancellable := false
	list := agentsSelectableList{header: header, cursor: cursor, muted: muted, noun: "branches", bodyWidth: bodyWidth, control: st.branches, gutterCells: 2}
	r := &renderer{th: th, marks: hk, traceWidth: max(1, bodyWidth-lipgloss.Width(parallelBranchTraceGutter)-3)}
	for i := range ordered {
		br := &ordered[i]
		if !br.done && br.childID != "" {
			cancellable = true
		}
		row := parallelBranchText(br, bodyWidth, 3)
		if trace := r.renderTrace(br.trace); trace != "" {
			row += "\n" + indentParallelBranchTrace(trace, bodyWidth)
		}
		list.ids = append(list.ids, parallelBranchID(g.parentCallID, br.index))
		marker := [2]string{}
		if br.index == g.winner {
			marker[0] = "★"
		}
		list.statusCells = append(list.statusCells, marker)
		list.rows = append(list.rows, row)
	}
	list.footer = focusBackHint(hk) + " · " + hk.navUp + "/" + hk.navDown + " select · " + hk.scroll + " page · " + hk.jumpTop + "/" + hk.jumpEnd + " first/last"
	if cancellable {
		list.footer = focusBackHint(hk) + " · " + hk.cancelChild + " cancel · " + hk.navUp + "/" + hk.navDown + " select · " + hk.scroll + " page · " + hk.jumpTop + "/" + hk.jumpEnd + " first/last"
	}
	if len(ordered) == 0 {
		list.footer = focusBackHint(hk)
	}
	list.footer = renderDynamicCardChromeLine(muted, "", list.footer, bodyWidth)
	return list
}

func renderParallelGroupFocus(th theme.Theme, st parallelState, groups []parallelGroup, hk helpKeys, bodyWidth, height int) string {
	return prepareParallelGroupFocus(th, st, groups, hk, bodyWidth)(height)
}

func prepareParallelGroupFocus(th theme.Theme, st parallelState, groups []parallelGroup, hk helpKeys, bodyWidth int) agentsBodyRenderer {
	g := findParallelGroup(groups, st.group)
	if g == nil {
		muted := th.Style("muted")
		body := renderDynamicCardChromeLine(th.Style("askTitle"), "", "parallel", bodyWidth) + "\n\n" +
			renderDynamicCardChromeLine(muted, "", "this parallel run is no longer tracked", bodyWidth) + "\n\n" +
			renderDynamicCardChromeLine(muted, "", focusBackHint(hk), bodyWidth)
		return func(int) string { return body }
	}
	list := parallelBranchSelectableList(th, st, g, hk, bodyWidth)
	return func(height int) string { return list.render(th, height) }
}

const parallelBranchTraceGutter = "  │ "

func indentParallelBranchTrace(trace string, bodyWidth int) string {
	if bodyWidth <= lipgloss.Width(parallelBranchTraceGutter) {
		return trace
	}
	lines := strings.Split(trace, "\n")
	for i, line := range lines {
		lines[i] = parallelBranchTraceGutter + line
	}
	return strings.Join(lines, "\n")
}

func parallelBranchText(br *parallelBranch, bodyWidth, titlePrefixWidth int) string {
	text := parallelBranchTitle(br, bodyWidth, titlePrefixWidth) + "\n" +
		hangingIndentWrap(parallelBranchDetails(br), "    ", bodyWidth)
	if detail := routingDecisionDetail(br.routingDecision, br.model, br.routingReason); detail != "" {
		text += "\n" + hangingIndentWrap(detail, "    ", bodyWidth)
	}
	return text
}

func parallelBranchTitle(br *parallelBranch, bodyWidth, titlePrefixWidth int) string {
	label := br.label
	if label == "" {
		label = fmt.Sprintf("branch-%d", br.index+1)
	}
	title := parallelBranchGlyph(br) + " " + terminaltext.Sanitize(label)
	if goal := terminaltext.Sanitize(br.goal); goal != "" {
		title += " · " + goal
	}
	if bodyWidth > 0 {
		return truncateDisplayWidth(title, max(1, bodyWidth-titlePrefixWidth))
	}
	return truncate(title, maxParallelBranchLabelLen+maxSubagentGoalLen)
}

func parallelBranchDetails(br *parallelBranch) string {
	parts := []string{}
	if routed := delegationModelLabel(br.routedCategory, br.routedModel, br.routingReason, br.model, br.routingDecision); routed != "" {
		parts = append(parts, routed)
	}
	parts = append(parts, parallelBranchState(br), plural(br.toolCount, "tool"), "↑"+humanizeTokens(br.usage.InputTokens)+" ↓"+humanizeTokens(br.usage.OutputTokens))
	return strings.Join(parts, " · ")
}

func parallelBranchGlyph(br *parallelBranch) string {
	if !br.done {
		return "◐"
	}
	if br.failed {
		return "✗"
	}
	return "✓"
}

func parallelBranchState(br *parallelBranch) string {
	if br.done {
		state := parallelBranchStopLabel(br)
		if br.durationMs > 0 {
			state += " · " + humanizeDuration(br.durationMs)
		}
		return state
	}
	if br.current != "" {
		return truncate(terminaltext.Sanitize(br.current), maxTraceToolNameLen) + "…"
	}
	return "working…"
}

// maxParallelBranchLabelLen caps a server-provided branch label in the focus row,
// which has no independent card wrapper.
const maxParallelBranchLabelLen = 24

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
	label = truncate(terminaltext.Sanitize(label), maxParallelBranchLabelLen)
	goal := truncate(terminaltext.Sanitize(br.goal), maxSubagentGoalLen)
	state := "working…"
	if br.done {
		state = parallelBranchStopLabel(br)
		if br.durationMs > 0 {
			state += " · " + humanizeDuration(br.durationMs)
		}
	} else if br.current != "" {
		state = truncate(terminaltext.Sanitize(br.current), maxTraceToolNameLen) + "…"
	}
	routed := ""
	if r := delegationModelLabel(br.routedCategory, br.routedModel, br.routingReason, br.model, br.routingDecision); r != "" {
		routed = " · " + r
	}
	return fmt.Sprintf("%s %s · %s%s · %s · %s · ↑%s ↓%s",
		glyph, terminaltext.Sanitize(label), goal, routed, state,
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

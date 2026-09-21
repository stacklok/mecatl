package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	customization "github.com/stacklok/mecatl/cmd/mecatui/customization"
)

type statusLineChangedMsg struct {
	line customization.Result
}

type statusContextMsg struct {
	sessionID string
	root      string
}

func (m Model) refreshStatusContextCmd() tea.Cmd {
	if m.deps.StatusSource == nil || m.sessionID == "" {
		return nil
	}
	id := m.sessionID
	customization.ClearCommandCWD(m.deps.StatusSource)
	if m.deps.LocalSessionContext == nil {
		return nil
	}
	getter, ctx := m.deps.LocalSessionContext, m.deps.Ctx
	return func() tea.Msg {
		root, err := getter.GetLocalSessionContext(ctx, id)
		if err != nil {
			return statusContextMsg{sessionID: id}
		}
		return statusContextMsg{sessionID: id, root: root}
	}
}

// statusLineWaitCmd is the UI's sole source listener.
func (m Model) statusLineWaitCmd() tea.Cmd {
	if m.deps.StatusSource == nil {
		return nil
	}
	ctx, source := m.deps.Ctx, m.deps.StatusSource
	return func() tea.Msg {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-source.Changed():
			if !ok {
				return nil
			}
			return statusLineChangedMsg{line: source.Latest()}
		}
	}
}

func usageAtom(raw int64) customization.UsageAtom {
	return customization.UsageAtom{Raw: raw, Human: humanizeTokens(raw)}
}

func contextAtom(raw int64) customization.ContextAtom {
	return customization.ContextAtom{Raw: raw, Human: humanizeTokens(raw)}
}

func delegationCountsFor(done bool, failed bool, stop string) customization.DelegationStateCounts {
	if !done {
		return customization.DelegationStateCounts{Running: 1}
	}
	switch stop {
	case teamStopReasonCancelled:
		return customization.DelegationStateCounts{Cancelled: 1}
	case "error":
		return customization.DelegationStateCounts{Failed: 1}
	case "stopped":
		return customization.DelegationStateCounts{Stopped: 1}
	}
	if failed {
		return customization.DelegationStateCounts{Failed: 1}
	}
	return customization.DelegationStateCounts{Completed: 1}
}
func addDelegationCounts(a, b customization.DelegationStateCounts) customization.DelegationStateCounts {
	return customization.DelegationStateCounts{Running: a.Running + b.Running, AwaitingApproval: a.AwaitingApproval + b.AwaitingApproval, Completed: a.Completed + b.Completed, Failed: a.Failed + b.Failed, Cancelled: a.Cancelled + b.Cancelled, Stopped: a.Stopped + b.Stopped}
}
func delegationDisplay(counts customization.DelegationStateCounts) customization.DelegationSummary {
	return customization.DelegationSummary{
		Running:  counts.Running + counts.AwaitingApproval,
		Finished: counts.Completed + counts.Failed + counts.Cancelled + counts.Stopped,
	}
}

func (m Model) statusDelegation() customization.Delegation {
	var d customization.Delegation
	for _, lane := range m.conv.subagentFleet {
		d.DirectSubagent = addDelegationCounts(d.DirectSubagent, delegationCountsFor(lane.done, false, lane.stop))
	}
	for _, group := range m.conv.parallelGroups {
		for _, branch := range group.branches {
			d.ParallelBranch = addDelegationCounts(d.ParallelBranch, delegationCountsFor(branch.done, branch.failed, branch.stop))
		}
	}
	if team := m.conv.latestTeamBlock(); team != nil {
		for _, lane := range team.teamLanes {
			d.TeamMember = addDelegationCounts(d.TeamMember, delegationCountsFor(team.teamDone, lane.stopped, lane.stopReason))
		}
	}
	d.Total = addDelegationCounts(addDelegationCounts(d.DirectSubagent, d.TeamMember), d.ParallelBranch)
	d.Subagents = delegationDisplay(d.DirectSubagent)
	d.Parallel = delegationDisplay(d.ParallelBranch)
	if team := m.conv.liveTeamBlock(); team != nil {
		working, total := teamWorkingCounts(team.teamLanes)
		d.Team = customization.LiveTeam{ID: team.teamID, Working: working, Total: total}
	}
	return d
}

type statusLineGeometry struct {
	headerAvailable int
	footerAvailable int
}

// statusLineGeometry is the single source of status-surface lane reservations.
// It uses renderer-owned header safety/navigation and footer activity lanes, but
// does not render or submit anything.
func (m Model) statusLineGeometry() statusLineGeometry {
	badge, badgeWidth, _ := m.postureBadgeRender()
	tail := m.scrollIndicator()
	if tail == "" {
		tail = m.changedFilesIndicator()
	}
	left := m.footerActivity()
	return statusLineGeometry{
		headerAvailable: m.statusHeaderAvailable(badge, badgeWidth, tail),
		footerAvailable: max(0, m.widthOr()-2-lipgloss.Width(left)-footerGapPad),
	}
}

func (m Model) statusLineSnapshot() customization.Input {
	return m.statusLineInput(time.Time{})
}

func (m Model) submitStatusLine() {
	if m.deps.StatusSource != nil {
		m.deps.StatusSource.Submit(m.statusLineInput(time.Now()))
	}
}

func (m Model) statusLineInput(now time.Time) customization.Input {
	geometry := m.statusLineGeometry()
	window := m.resolvedSessionModel.ContextWindow
	contextPercent := 0
	if window > 0 {
		contextPercent = int(m.contextTokens * 100 / window)
	}
	cachePercent := 0
	if m.usage.InputTokens > 0 {
		cachePercent = int(m.usage.CacheReadTokens * 100 / m.usage.InputTokens)
	}
	workspace := customization.Workspace{Location: unknownLabel}
	if m.activePlacement.Kind != "" || m.activePlacement.Label != "" {
		workspace.Location = "local"
		if m.deps.ConnectionMode == connectCommand {
			workspace.Location = "remote"
		}
		workspace.Name = m.activePlacement.Label
	}
	if workspace.Location == "local" && m.deps.ConnectionMode != "connect" && m.statusContextRoot != "" {
		workspace.Path = m.statusContextRoot
	}
	state, activity, approval := "idle", "", "none"
	mode := m.activeMode
	if mode == "" {
		mode = m.deps.Mode
	}
	if m.pendingMode != "" {
		mode = m.pendingMode + " pending"
	}
	handle := client.SessionHandle(m.sessionID)
	switch m.phase {
	case phaseConnecting:
		state = "connecting"
	case phaseRunning:
		state = "thinking"
		if m.activeTool != "" {
			state, activity = "running_tool", m.activeTool
			if m.toolProgress != "" {
				activity = m.toolProgress
			}
		}
	case phaseAwaitingApproval:
		state, approval = "awaiting_approval", "awaiting"
	case phaseFatal:
		state = "failed"
	}
	return customization.Input{
		Version: customization.ProtocolVersion,
		Server:  customization.ServerTarget{DisplayTarget: m.deps.Server, ConnectionMode: m.deps.ConnectionMode},
		Session: customization.Session{Title: m.sessionTitle, Handle: handle, Mode: mode, ReasoningEffort: m.resolvedSessionModel.ReasoningEffort},
		Model:   customization.Model{ProviderID: m.resolvedSessionModel.ProviderID, ID: m.resolvedSessionModel.ModelID, DisplayName: m.headerModelLabel(), Route: m.providerRoute, ContextWindow: contextAtom(window)},
		Usage:   customization.Usage{Input: usageAtom(m.usage.InputTokens), Output: usageAtom(m.usage.OutputTokens), CacheRead: usageAtom(m.usage.CacheReadTokens), CacheWrite: usageAtom(m.usage.CacheWriteTokens), CacheReadPercent: cachePercent},
		Context: customization.Context{Used: contextAtom(m.contextTokens), Window: contextAtom(window), Percent: contextPercent}, Workspace: workspace,
		MainAgent:  customization.MainAgent{State: state, Activity: activity, Approval: approval},
		Delegation: m.statusDelegation(),
		Clock:      customization.Clock{Now: now},
		Terminal:   customization.Terminal{Rows: m.height, Cols: m.widthOr(), HeaderAvailCols: geometry.headerAvailable, FooterAvailCols: geometry.footerAvailable},
	}
}

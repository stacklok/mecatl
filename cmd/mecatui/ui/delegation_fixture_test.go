package ui

import (
	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func (c *conversation) testBlocks() []scrollback.BlockSnapshot {
	cards := make([]scrollback.BlockSnapshot, c.scrollback.Len())
	for i := range cards {
		cards[i] = c.scrollback.SnapshotAt(i)
	}
	return cards
}

func (c *conversation) testSubagentCard(callID string) *subagentCardPresentation {
	s, ok := c.scrollback.SnapshotForCall(callID)
	if !ok {
		return nil
	}
	p, ok := s.Payload.(scrollback.SubagentCardSnapshot)
	if !ok {
		return nil
	}
	out := subagentCardPresentationFromSnapshot(p)
	return &out
}

func (c *conversation) addTeamFixture(in teamOverlaySnapshot) {
	callID := in.callID
	if callID == "" {
		callID = "team-fixture"
	}
	c.addTool(callID, "Team", `{}`)
	lanes := make([]scrollback.TeamLane, len(in.teamLanes))
	for i, lane := range in.teamLanes {
		lanes[i] = scrollback.TeamLane{Name: lane.name, SessionID: lane.sessionID, Role: lane.role, Mutating: lane.mutating, Lead: lane.lead, RoutedCategory: lane.routedCategory, RoutedModel: lane.routedModel, RoutingReason: lane.routingReason, Model: lane.model, Current: lane.current, ToolCount: lane.toolCount, Usage: scrollUsage(lane.usage), Trace: scrollTrace(lane.trace), Idle: lane.idle, Stopped: lane.stopped, StopReason: lane.stopReason, Cause: lane.cause, ErrorRounds: lane.errorRounds, ContextUsed: lane.ctxUsed, ContextWindow: lane.ctxWindow}
	}
	if !c.scrollback.Teams().Start(callID, scrollback.TeamStart{TeamID: in.teamID, Lanes: lanes}) {
		return
	}
	tasks := make([]scrollback.Task, len(in.teamTasks))
	for i, task := range in.teamTasks {
		tasks[i] = scrollback.Task{ID: task.id, Description: task.desc, State: task.state, Assignee: task.assignee, Dependencies: append([]string(nil), task.deps...)}
	}
	findings := make([]scrollback.Finding, len(in.teamFindings))
	for i, finding := range in.teamFindings {
		findings[i] = scrollback.Finding{Member: finding.member, Body: finding.body}
	}
	c.scrollback.Teams().Update(callID, scrollback.TeamUpdate{TeamID: in.teamID, Lanes: lanes, Tasks: tasks, Findings: findings, Rounds: in.teamRounds, Stop: in.teamStop, Usage: scrollUsage(in.teamUsage), Done: in.teamDone})
}

func testCardText(s scrollback.BlockSnapshot) string {
	switch p := s.Payload.(type) {
	case scrollback.UserCardSnapshot:
		return p.Text
	case scrollback.AssistantCardSnapshot:
		return p.Text
	case scrollback.NoticeCardSnapshot:
		return p.Text
	case scrollback.TurnStatCardSnapshot:
		return p.Text
	case scrollback.ErrorCardSnapshot:
		return p.Text
	case scrollback.HookCardSnapshot:
		return p.Text
	case scrollback.DeliveryCardSnapshot:
		return p.Text
	default:
		return ""
	}
}

func (c *conversation) testChangedFiles() []string {
	appendix, ok := c.scrollback.AppendixSnapshot()
	if !ok {
		return nil
	}
	return appendix.Files
}
func (c *conversation) testAppendixID() uint64 {
	appendix, ok := c.scrollback.AppendixSnapshot()
	if !ok {
		return 0
	}
	return uint64(appendix.ID)
}
func (c *conversation) startSubagentCard(parentCallID, goal, routedCategory, routedModel, routingReason, model string) bool {
	return c.scrollback.Subagents().Start(parentCallID, scrollback.SubagentStart{Goal: goal, RoutedCategory: routedCategory, RoutedModel: routedModel, RoutingReason: routingReason, Model: model})
}
func (c *conversation) setSubagentCardRouting(parentCallID, childID string, decision *client.RoutingDecision) {
	if p, ok := c.subagentCard(parentCallID); ok {
		p.Start.Routing = scrollRouting(decision)
		c.scrollback.Subagents().UpdateStart(parentCallID, p.Start)
	}
	if childID != "" {
		c.fleetLane(childID).routingDecision = cloneRoutingDecision(decision)
	}
}
func (c *conversation) updateSubagentCard(msg client.SubagentMsg) bool {
	if _, ok := c.subagentCard(msg.ParentCallID); !ok {
		return false
	}
	c.applySubagentTyped(msg)
	return true
}
func (c *conversation) finishSubagentCard(parentCallID string, usage client.Usage, toolCount int, stop string, durationMS int64) bool {
	if _, ok := c.subagentCard(parentCallID); !ok {
		return false
	}
	c.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentEnd, ParentCallID: parentCallID, Usage: usage, ToolCount: toolCount, Stop: stop, DurationMs: durationMS})
	return true
}
func (c *conversation) startTeamCard(parentCallID, teamID string, roster []client.TeamMemberSpec) bool {
	if !c.ensureTeamCard(parentCallID, teamID, roster) {
		return false
	}
	c.applyTeamTyped(client.TeamMsg{Kind: client.TeamStart, ParentCallID: parentCallID, TeamID: teamID, Roster: roster})
	return true
}
func (c *conversation) updateTeamCardMember(msg client.TeamMsg) bool {
	if !c.ensureTeamCard(msg.ParentCallID, msg.TeamID, nil) {
		return false
	}
	msg.Kind = client.TeamMember
	c.applyTeamTyped(msg)
	return true
}
func (c *conversation) finishTeamCard(parentCallID, teamID string, rounds int, stop string, usage client.Usage, dispositions []client.TeamMemberDisposition) bool {
	p, ok := c.teamCard(parentCallID)
	if !ok {
		return false
	}
	u := p.Update
	if teamID != "" {
		u.TeamID = teamID
	}
	u.Rounds, u.Stop, u.Usage, u.Done = rounds, stop, scrollUsage(usage), true
	for _, d := range dispositions {
		var i int
		u.Lanes, i = teamLaneFor(u.Lanes, d.Name)
		u.Lanes[i].Stopped, u.Lanes[i].StopReason, u.Lanes[i].ErrorRounds = d.Stopped, d.Reason, d.ErrorRounds
	}
	return c.scrollback.Teams().Update(parentCallID, u)
}
func (c *conversation) updateTeamCardTasks(parentCallID string, tasks []client.TeamTask) bool {
	if !c.ensureTeamCard(parentCallID, "", nil) {
		return false
	}
	p, ok := c.teamCard(parentCallID)
	if !ok {
		return false
	}
	p.Update.Tasks = scrollTasks(tasks)
	return c.scrollback.Teams().Update(parentCallID, p.Update)
}
func (c *conversation) updateTeamCardFindings(parentCallID string, findings []client.TeamFinding) {
	if !c.ensureTeamCard(parentCallID, "", nil) {
		return
	}
	p, ok := c.teamCard(parentCallID)
	if !ok {
		return
	}
	p.Update.Findings = scrollFindings(findings)
	c.scrollback.Teams().Update(parentCallID, p.Update)
}

func (c *conversation) testTeamOverlay(i int) *teamOverlaySnapshot {
	s := c.scrollback.SnapshotAt(i)
	p, ok := s.Payload.(scrollback.TeamCardSnapshot)
	if !ok {
		return nil
	}
	return teamOverlaySnapshotFromSnapshot(s.ID, p)
}

func (c *conversation) testSubagentPresentationAt(i int) *subagentCardPresentation {
	s := c.scrollback.SnapshotAt(i)
	p, ok := s.Payload.(scrollback.SubagentCardSnapshot)
	if !ok {
		return nil
	}
	out := subagentCardPresentationFromSnapshot(p)
	return &out
}

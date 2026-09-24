package ui

import (
	"fmt"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func (c *conversation) testBlocks() []block {
	blocks := make([]block, 0, c.scrollback.Len())
	for i := 0; i < c.scrollback.Len(); i++ {
		if b, ok := blockFromSnapshot(c.scrollback.SnapshotAt(i)); ok {
			blocks = append(blocks, b)
		}
	}
	return blocks
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

func (c *conversation) testSubagentBlock(callID string) *block {
	snapshot, ok := c.scrollback.SnapshotForCall(callID)
	if !ok {
		return nil
	}
	b, ok := delegationBlockFromSnapshot(snapshot)
	if !ok || !b.subagent {
		return nil
	}
	return &b
}

// addTeamFixture adapts presentation fixtures through the typed model.
func (c *conversation) addTeamFixture(b block) {
	callID := b.toolID
	if callID == "" {
		callID = fmt.Sprintf("team-fixture-%d", c.scrollback.Len())
	}
	c.addTool(callID, b.toolName, b.toolArgs)
	roster := make([]client.TeamMemberSpec, len(b.teamLanes))
	for i, lane := range b.teamLanes {
		roster[i] = client.TeamMemberSpec{Name: lane.name, Role: lane.role, Mutating: lane.mutating, Lead: lane.lead, RoutedCategory: lane.routedCategory, RoutedModel: lane.routedModel, RoutingReason: lane.routingReason, Model: lane.model, RoutingDecision: lane.routingDecision}
	}
	c.setTeamStart(callID, b.teamID, roster)
	p, ok := c.teamCard(callID)
	if !ok {
		return
	}
	u := p.Update
	u.Rounds, u.Stop, u.Usage, u.Done = b.teamRounds, b.teamStop, scrollUsage(b.teamUsage), b.teamDone
	u.Lanes = make([]scrollback.TeamLane, len(b.teamLanes))
	for i, lane := range b.teamLanes {
		u.Lanes[i] = scrollback.TeamLane{Name: lane.name, SessionID: lane.sessionID, Role: lane.role, Mutating: lane.mutating, Lead: lane.lead, RoutedCategory: lane.routedCategory, RoutedModel: lane.routedModel, RoutingReason: lane.routingReason, Model: lane.model, Routing: scrollRouting(lane.routingDecision), Current: lane.current, ToolCount: lane.toolCount, Usage: scrollUsage(lane.usage), Trace: scrollTrace(lane.trace), Idle: lane.idle, Stopped: lane.stopped, StopReason: lane.stopReason, ErrorRounds: lane.errorRounds, Cause: lane.cause, ContextUsed: lane.ctxUsed, ContextWindow: lane.ctxWindow}
	}
	u.Tasks = make([]scrollback.Task, len(b.teamTasks))
	for i, task := range b.teamTasks {
		u.Tasks[i] = scrollback.Task{ID: task.id, Description: task.desc, State: task.state, Assignee: task.assignee, Dependencies: append([]string(nil), task.deps...)}
	}
	u.Findings = make([]scrollback.Finding, len(b.teamFindings))
	for i, finding := range b.teamFindings {
		u.Findings[i] = scrollback.Finding{Member: finding.member, Body: finding.body}
	}
	c.scrollback.Teams().Update(callID, u)
}

// Focused transition helpers belong to tests rather than the production adapter.

// setSubagentStart is a typed transition seam retained for focused presentation tests.
func (c *conversation) setSubagentStart(parentCallID, goal, routedCategory, routedModel, routingReason, model string) bool {
	ok := c.scrollback.Subagents().Start(parentCallID, scrollback.SubagentStart{Goal: goal, RoutedCategory: routedCategory, RoutedModel: routedModel, RoutingReason: routingReason, Model: model})
	return ok
}

func (c *conversation) setSubagentRoutingDecision(parentCallID, childID string, decision *client.RoutingDecision) {
	if p, ok := c.subagentCard(parentCallID); ok {
		p.Start.Routing = scrollRouting(decision)
		c.scrollback.Subagents().UpdateStart(parentCallID, p.Start)
	}
	if childID != "" {
		c.fleetLane(childID).routingDecision = cloneRoutingDecision(decision)
	}
}

// addSubagentTool is a typed transition seam retained for focused presentation tests.
func (c *conversation) addSubagentTool(msg client.SubagentMsg) bool {
	if _, ok := c.subagentCard(msg.ParentCallID); !ok {
		return false
	}
	c.applySubagentTyped(msg)
	return true
}

// setSubagentEnd is a typed transition seam retained for focused presentation tests.
func (c *conversation) setSubagentEnd(parentCallID string, usage client.Usage, toolCount int, stop string, durationMs int64) bool {
	if _, ok := c.subagentCard(parentCallID); !ok {
		return false
	}
	c.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentEnd, ParentCallID: parentCallID, Usage: usage, ToolCount: toolCount, Stop: stop, DurationMs: durationMs})
	return true
}

func (c *conversation) setTeamStart(parentCallID, teamID string, roster []client.TeamMemberSpec) bool {
	if !c.ensureTeamCard(parentCallID, teamID, roster) {
		return false
	}
	c.applyTeamTyped(client.TeamMsg{Kind: client.TeamStart, ParentCallID: parentCallID, TeamID: teamID, Roster: roster})
	return true
}

func (c *conversation) addTeamMember(msg client.TeamMsg) bool {
	if !c.ensureTeamCard(msg.ParentCallID, msg.TeamID, nil) {
		return false
	}
	msg.Kind = client.TeamMember
	c.applyTeamTyped(msg)
	return true
}

func (c *conversation) setTeamEnd(parentCallID, teamID string, rounds int, stop string, usage client.Usage, dispositions []client.TeamMemberDisposition) bool {
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

func (c *conversation) setTeamTasks(parentCallID string, tasks []client.TeamTask) bool {
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

func (c *conversation) setTeamFindings(parentCallID string, findings []client.TeamFinding) {
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

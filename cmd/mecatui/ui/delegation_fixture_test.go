package ui

import (
	"fmt"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

// addTeamFixture adapts legacy presentation fixtures through the typed model.
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
	c.syncCall(callID)
}

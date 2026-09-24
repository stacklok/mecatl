package ui

import (
	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func scrollUsage(in client.Usage) scrollback.Usage {
	return scrollback.Usage{InputTokens: in.InputTokens, OutputTokens: in.OutputTokens, CacheReadTokens: in.CacheReadTokens, CacheWriteTokens: in.CacheWriteTokens, ReasoningTokens: in.ReasoningTokens}
}

func scrollRouting(in *client.RoutingDecision) scrollback.RoutingDecision {
	if in == nil {
		return scrollback.RoutingDecision{}
	}
	out := scrollback.RoutingDecision{Backend: in.Backend, ClassifierModel: in.ClassifierModel, CandidateCategory: in.CandidateCategory, CandidateModel: in.CandidateModel, Outcome: in.Outcome, ConsecutiveMisses: in.ConsecutiveMisses, MissLimit: in.MissLimit, BreakerOpen: in.BreakerOpen}
	if in.Confidence != nil {
		out.Confidence = scrollback.Float64(*in.Confidence)
	}
	if in.MinimumConfidence != nil {
		out.MinimumConfidence = scrollback.Float64(*in.MinimumConfidence)
	}
	return out
}

func scrollTrace(in []teamTrace) []scrollback.TraceEntry {
	out := make([]scrollback.TraceEntry, len(in))
	for i, t := range in {
		out[i] = scrollback.TraceEntry{Text: t.text, ToolName: t.name, Detail: t.detail, Error: t.isError}
		if t.kind == teamTraceTool {
			out[i].Kind = "tool"
		} else {
			out[i].Kind = "message"
		}
	}
	return out
}

func traceFromScroll(in []scrollback.TraceEntry) []teamTrace {
	out := make([]teamTrace, len(in))
	for i, t := range in {
		out[i] = teamTrace{text: t.Text, name: t.ToolName, detail: t.Detail, isError: t.Error}
		if t.Kind == "tool" {
			out[i].kind = teamTraceTool
		} else {
			out[i].kind = teamTraceMessage
		}
	}
	return out
}

func (c *conversation) subagentCard(callID string) (scrollback.SubagentCardSnapshot, bool) {
	snapshot, ok := c.scrollback.SnapshotForCall(callID)
	if !ok {
		return scrollback.SubagentCardSnapshot{}, false
	}
	payload, ok := snapshot.Payload.(scrollback.SubagentCardSnapshot)
	return payload, ok
}

func (c *conversation) applySubagentTyped(msg client.SubagentMsg) {
	switch msg.Kind {
	case client.SubagentStart:
		c.scrollback.Subagents().Start(msg.ParentCallID, scrollback.SubagentStart{ChildID: msg.ChildID, Goal: msg.Goal, Model: msg.Model, RoutedCategory: msg.RoutedCategory, RoutedModel: msg.RoutedModel, RoutingReason: msg.RoutingReason, Background: msg.Background, Routing: scrollRouting(msg.RoutingDecision)})
	case client.SubagentTool:
		p, ok := c.subagentCard(msg.ParentCallID)
		if !ok {
			return
		}
		u := p.Update
		u.ToolCount, u.Usage = msg.ToolCount, scrollUsage(msg.Usage)
		trace, current := routeTraceEvent(traceFromScroll(u.Trace), u.Current, msg.InnerKind, msg.ToolName, msg.Detail, msg.Text, msg.IsError)
		u.Trace, u.Current = scrollTrace(trace), current
		c.scrollback.Subagents().Update(msg.ParentCallID, u)
	case client.SubagentEnd:
		p, ok := c.subagentCard(msg.ParentCallID)
		if !ok {
			return
		}
		u := p.Update
		u.Done, u.Usage, u.ToolCount, u.Stop, u.Cause, u.DurationMS = true, scrollUsage(msg.Usage), msg.ToolCount, msg.Stop, msg.Cause, msg.DurationMs
		c.scrollback.Subagents().Update(msg.ParentCallID, u)
	}
}

func scrollTeamLane(in client.TeamMemberSpec) scrollback.TeamLane {
	return scrollback.TeamLane{Name: in.Name, Role: in.Role, Mutating: in.Mutating, Lead: in.Lead, RoutedCategory: in.RoutedCategory, RoutedModel: in.RoutedModel, RoutingReason: in.RoutingReason, Model: in.Model, Routing: scrollRouting(in.RoutingDecision)}
}

func teamLaneFor(lanes []scrollback.TeamLane, name string) ([]scrollback.TeamLane, int) {
	for i := range lanes {
		if lanes[i].Name == name {
			return lanes, i
		}
	}
	return append(lanes, scrollback.TeamLane{Name: name}), len(lanes)
}

func (c *conversation) teamCard(callID string) (scrollback.TeamCardSnapshot, bool) {
	snapshot, ok := c.scrollback.SnapshotForCall(callID)
	if !ok {
		return scrollback.TeamCardSnapshot{}, false
	}
	payload, ok := snapshot.Payload.(scrollback.TeamCardSnapshot)
	return payload, ok
}

func (c *conversation) ensureTeamCard(callID, teamID string, roster []client.TeamMemberSpec) bool {
	if _, ok := c.teamCard(callID); ok {
		return true
	}
	lanes := make([]scrollback.TeamLane, len(roster))
	for i := range roster {
		lanes[i] = scrollTeamLane(roster[i])
	}
	return c.scrollback.Teams().Start(callID, scrollback.TeamStart{TeamID: teamID, Lanes: lanes})
}

func (c *conversation) applyTeamTyped(msg client.TeamMsg) {
	if msg.Kind == client.TeamStart {
		if !c.ensureTeamCard(msg.ParentCallID, msg.TeamID, msg.Roster) {
			return
		}
	}
	p, ok := c.teamCard(msg.ParentCallID)
	if !ok {
		return
	}
	u := p.Update
	if msg.TeamID != "" {
		u.TeamID = msg.TeamID
	}
	switch msg.Kind {
	case client.TeamStart:
		u.Lanes = make([]scrollback.TeamLane, len(msg.Roster))
		for i := range msg.Roster {
			u.Lanes[i] = scrollTeamLane(msg.Roster[i])
		}
	case client.TeamMember:
		applyTeamMemberUpdate(&u, msg)
	case client.TeamTasks:
		u.Tasks = scrollTasks(msg.Tasks)
	case client.TeamFindings:
		u.Findings = scrollFindings(msg.Findings)
	case client.TeamEnd:
		u.Rounds, u.Stop, u.Usage, u.Tasks, u.Findings, u.Done = msg.Rounds, msg.Stop, scrollUsage(msg.Usage), scrollTasks(msg.Tasks), scrollFindings(msg.Findings), true
		for _, d := range msg.Dispositions {
			var i int
			u.Lanes, i = teamLaneFor(u.Lanes, d.Name)
			u.Lanes[i].Stopped, u.Lanes[i].StopReason, u.Lanes[i].ErrorRounds = d.Stopped, d.Reason, d.ErrorRounds
		}
	}
	c.scrollback.Teams().Update(msg.ParentCallID, u)
}

func applyTeamMemberUpdate(update *scrollback.TeamUpdate, msg client.TeamMsg) {
	var i int
	update.Lanes, i = teamLaneFor(update.Lanes, msg.Member)
	lane := &update.Lanes[i]
	if msg.MemberSessionID != "" {
		lane.SessionID = msg.MemberSessionID
	}
	switch msg.InnerKind {
	case "message.delta":
		lane.Idle = false
		lane.Trace = scrollTrace(traceAppendMessage(traceFromScroll(lane.Trace), msg.Text))
	case "tool.call":
		lane.Idle, lane.Current, lane.ToolCount = false, msg.ToolName, lane.ToolCount+1
		lane.Trace = scrollTrace(traceAppendTool(traceFromScroll(lane.Trace), msg.ToolName, msg.Detail, false))
	case "tool.result":
		lane.Trace = scrollTrace(traceMarkToolResult(traceFromScroll(lane.Trace), msg.ToolName, msg.Detail, msg.IsError))
	case "turn.end":
		lane.Idle, lane.Usage, lane.ContextUsed = false, sumScrollUsage(lane.Usage, msg.Usage), msg.Usage.InputTokens
		if msg.ContextWindow > 0 {
			lane.ContextWindow = msg.ContextWindow
		}
	case "result":
		lane.Idle, lane.Current = true, ""
		if msg.Usage != (client.Usage{}) {
			lane.Usage = sumScrollUsage(lane.Usage, msg.Usage)
		}
		if msg.Text != "" {
			lane.Trace = scrollTrace(traceAppendMessage(traceFromScroll(lane.Trace), msg.Text))
		}
		if msg.Cause != "" {
			lane.Cause = msg.Cause
		}
	}
}

func sumScrollUsage(current scrollback.Usage, next client.Usage) scrollback.Usage {
	return scrollUsage(sumUsage(clientUsage(current), next))
}
func scrollTasks(tasks []client.TeamTask) []scrollback.Task {
	out := make([]scrollback.Task, len(tasks))
	for i, task := range tasks {
		out[i] = scrollback.Task{ID: task.ID, Description: task.Description, State: task.State, Assignee: task.Assignee, Dependencies: append([]string(nil), task.Deps...)}
	}
	return out
}
func scrollFindings(findings []client.TeamFinding) []scrollback.Finding {
	out := make([]scrollback.Finding, len(findings))
	for i, finding := range findings {
		out[i] = scrollback.Finding{Member: finding.Member, Body: finding.Body}
	}
	return out
}

func clientUsage(in scrollback.Usage) client.Usage {
	return client.Usage{InputTokens: in.InputTokens, OutputTokens: in.OutputTokens, CacheReadTokens: in.CacheReadTokens, CacheWriteTokens: in.CacheWriteTokens, ReasoningTokens: in.ReasoningTokens}
}

func clientRouting(in scrollback.RoutingDecision) *client.RoutingDecision {
	if in == (scrollback.RoutingDecision{}) {
		return nil
	}
	out := &client.RoutingDecision{Backend: in.Backend, ClassifierModel: in.ClassifierModel, CandidateCategory: in.CandidateCategory, CandidateModel: in.CandidateModel, Outcome: in.Outcome, ConsecutiveMisses: in.ConsecutiveMisses, MissLimit: in.MissLimit, BreakerOpen: in.BreakerOpen}
	if in.Confidence != nil {
		v := *in.Confidence
		out.Confidence = &v
	}
	if in.MinimumConfidence != nil {
		v := *in.MinimumConfidence
		out.MinimumConfidence = &v
	}
	return out
}

// subagentCardPresentation is the renderer-owned, immutable adaptation of a
// Subagent snapshot. It intentionally contains only the fields its card renders.
type subagentCardPresentation struct {
	name, arguments, result string
	resolved, isError       bool
	artifacts               []client.ContentBlock
	goal, current           string
	trace                   []teamTrace
	toolCount               int
	usage                   client.Usage
	stop                    string
	durationMS              int64
	done                    bool
	routedCategory          string
	routedModel             string
	routingReason           string
	model                   string
	routing                 *client.RoutingDecision
}

func subagentCardPresentationFromSnapshot(p scrollback.SubagentCardSnapshot) subagentCardPresentation {
	return subagentCardPresentation{
		name: p.Call.Name, arguments: p.Call.Arguments, resolved: p.Resolved,
		result: p.Result.Body, isError: p.Result.IsError, artifacts: contentBlocks(p.Result.Artifacts),
		goal: p.Start.Goal, current: p.Update.Current, trace: traceFromScroll(p.Update.Trace),
		toolCount: p.Update.ToolCount, usage: clientUsage(p.Update.Usage), stop: p.Update.Stop,
		durationMS: p.Update.DurationMS, done: p.Update.Done,
		routedCategory: p.Start.RoutedCategory, routedModel: p.Start.RoutedModel,
		routingReason: p.Start.RoutingReason, model: p.Start.Model, routing: clientRouting(p.Start.Routing),
	}
}

// teamCardPresentation is the renderer-owned, immutable adaptation of a Team
// snapshot. Team overlay consumers deliberately retain their separate ui.block path.
type teamCardPresentation struct {
	name, arguments, result string
	resolved, isError       bool
	artifacts               []client.ContentBlock
	teamID                  string
	lanes                   []teamLane
	tasks                   []teamTask
	findings                []teamFinding
	rounds                  int
	stop                    string
	usage                   client.Usage
	done                    bool
}

func teamCardPresentationFromSnapshot(p scrollback.TeamCardSnapshot) teamCardPresentation {
	out := teamCardPresentation{
		name: p.Call.Name, arguments: p.Call.Arguments, resolved: p.Resolved,
		result: p.Result.Body, isError: p.Result.IsError, artifacts: contentBlocks(p.Result.Artifacts),
		teamID: p.Update.TeamID, rounds: p.Update.Rounds, stop: p.Update.Stop,
		usage: clientUsage(p.Update.Usage), done: p.Update.Done,
		lanes: make([]teamLane, len(p.Update.Lanes)), tasks: make([]teamTask, len(p.Update.Tasks)), findings: make([]teamFinding, len(p.Update.Findings)),
	}
	for i, lane := range p.Update.Lanes {
		out.lanes[i] = teamLane{name: lane.Name, sessionID: lane.SessionID, role: lane.Role, mutating: lane.Mutating, lead: lane.Lead, routedCategory: lane.RoutedCategory, routedModel: lane.RoutedModel, routingReason: lane.RoutingReason, routingDecision: clientRouting(lane.Routing), model: lane.Model, current: lane.Current, toolCount: lane.ToolCount, usage: clientUsage(lane.Usage), trace: traceFromScroll(lane.Trace), idle: lane.Idle, stopped: lane.Stopped, stopReason: lane.StopReason, errorRounds: lane.ErrorRounds, cause: lane.Cause, ctxUsed: lane.ContextUsed, ctxWindow: lane.ContextWindow}
	}
	for i, task := range p.Update.Tasks {
		out.tasks[i] = teamTask{id: task.ID, desc: task.Description, state: task.State, assignee: task.Assignee, deps: append([]string(nil), task.Dependencies...)}
	}
	for i, finding := range p.Update.Findings {
		out.findings[i] = teamFinding{member: finding.Member, body: finding.Body}
	}
	return out
}

func subagentBlockFromSnapshot(s scrollback.BlockSnapshot, p scrollback.SubagentCardSnapshot) *block {
	b := &block{id: uint64(s.ID), rev: rendererRevision(s.Revision), kind: blockTool}
	b.toolID, b.toolName, b.toolArgs, b.resolved, b.resultBody, b.resultError = p.Call.ID, p.Call.Name, p.Call.Arguments, p.Resolved, p.Result.Body, p.Result.IsError
	b.resultBlocks = contentBlocks(p.Result.Artifacts)
	b.subagent, b.subGoal, b.subCurrent, b.subTrace = true, p.Start.Goal, p.Update.Current, traceFromScroll(p.Update.Trace)
	b.subToolCount, b.subUsage, b.subStop, b.subDurationMs, b.subDone = p.Update.ToolCount, clientUsage(p.Update.Usage), p.Update.Stop, p.Update.DurationMS, p.Update.Done
	b.subRoutedCategory, b.subRoutedModel, b.subRoutingReason, b.subModel = p.Start.RoutedCategory, p.Start.RoutedModel, p.Start.RoutingReason, p.Start.Model
	b.subRoutingDecision = clientRouting(p.Start.Routing)
	return b
}

func teamBlockFromSnapshot(s scrollback.BlockSnapshot, p scrollback.TeamCardSnapshot) *block {
	b := &block{id: uint64(s.ID), rev: rendererRevision(s.Revision), kind: blockTool}
	b.toolID, b.toolName, b.toolArgs, b.resolved, b.resultBody, b.resultError = p.Call.ID, p.Call.Name, p.Call.Arguments, p.Resolved, p.Result.Body, p.Result.IsError
	b.resultBlocks = contentBlocks(p.Result.Artifacts)
	b.team, b.teamID, b.teamRounds, b.teamStop, b.teamUsage, b.teamDone = true, p.Update.TeamID, p.Update.Rounds, p.Update.Stop, clientUsage(p.Update.Usage), p.Update.Done
	b.teamLanes = make([]teamLane, len(p.Update.Lanes))
	for i, lane := range p.Update.Lanes {
		b.teamLanes[i] = teamLane{name: lane.Name, sessionID: lane.SessionID, role: lane.Role, mutating: lane.Mutating, lead: lane.Lead, routedCategory: lane.RoutedCategory, routedModel: lane.RoutedModel, routingReason: lane.RoutingReason, routingDecision: clientRouting(lane.Routing), model: lane.Model, current: lane.Current, toolCount: lane.ToolCount, usage: clientUsage(lane.Usage), trace: traceFromScroll(lane.Trace), idle: lane.Idle, stopped: lane.Stopped, stopReason: lane.StopReason, errorRounds: lane.ErrorRounds, cause: lane.Cause, ctxUsed: lane.ContextUsed, ctxWindow: lane.ContextWindow}
	}
	b.teamTasks = make([]teamTask, len(p.Update.Tasks))
	for i, task := range p.Update.Tasks {
		b.teamTasks[i] = teamTask{id: task.ID, desc: task.Description, state: task.State, assignee: task.Assignee, deps: append([]string(nil), task.Dependencies...)}
	}
	b.teamFindings = make([]teamFinding, len(p.Update.Findings))
	for i, finding := range p.Update.Findings {
		b.teamFindings[i] = teamFinding{member: finding.Member, body: finding.Body}
	}
	return b
}

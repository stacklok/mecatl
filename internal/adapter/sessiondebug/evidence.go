package sessiondebug

import (
	"context"

	"github.com/stacklok/mecatl/engine/session"
)

type delegationEvidence struct {
	View               string          `json:"view"`
	Authoritative      bool            `json:"authoritative"`
	Source             string          `json:"source"`
	ProjectionComplete bool            `json:"projection_complete"`
	ScanComplete       bool            `json:"scan_complete"`
	RetentionComplete  bool            `json:"retention_complete"`
	Offset             int             `json:"offset"`
	Limit              int             `json:"limit"`
	NextOffset         *int            `json:"next_offset,omitempty"`
	Error              string          `json:"error,omitempty"`
	Rows               []delegationRow `json:"rows"`
}

type delegationRow struct {
	Type              string                `json:"type"`
	Event             session.EventType     `json:"event"`
	CallID            string                `json:"call_id,omitempty"`
	ScopeHandle       string                `json:"scope_handle,omitempty"`
	Retention         string                `json:"retention,omitempty"`
	Conclusion        string                `json:"parent_conclusion,omitempty"`
	ParentResultError *bool                 `json:"parent_result_error,omitempty"`
	Background        *bool                 `json:"background,omitempty"`
	Collection        string                `json:"collection,omitempty"`
	Stop              session.StopReason    `json:"stop,omitempty"`
	Cause             string                `json:"cause,omitempty"`
	Join              string                `json:"join,omitempty"`
	BranchIndex       *int                  `json:"branch_index,omitempty"`
	Winner            *int                  `json:"winner,omitempty"`
	Role              string                `json:"role,omitempty"`
	Member            string                `json:"member,omitempty"`
	Disposition       string                `json:"disposition,omitempty"`
	DispositionReason string                `json:"disposition_reason,omitempty"`
	ErrorRounds       int                   `json:"error_rounds,omitempty"`
	Tasks             []teamTaskEvidence    `json:"tasks,omitempty"`
	Findings          []teamFindingEvidence `json:"findings,omitempty"`
	ScheduleName      string                `json:"schedule_name,omitempty"`
	ScheduleKind      string                `json:"schedule_kind,omitempty"`
}
type teamTaskEvidence struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	State       string   `json:"state"`
	Assignee    string   `json:"assignee,omitempty"`
	Deps        []string `json:"deps,omitempty"`
}
type teamFindingEvidence struct {
	Member string `json:"member"`
	Body   string `json:"body"`
}

func (t *inspectTool) delegationView(ctx context.Context, s *session.Session, graph lineageGraph, offset, requested int) delegationEvidence {
	limit := boundedLimit(requested, maxDelegationRows)
	out := delegationEvidence{View: "delegation", Authoritative: true, Source: "typed event payloads and current parent snapshot", ProjectionComplete: true, ScanComplete: t.log != nil, RetentionComplete: false, Offset: offset, Limit: limit, Rows: []delegationRow{}}
	if t.log == nil {
		out.Error = errLogNotConfigured
		out.Authoritative = false
		return out
	}
	byID := map[string]lineageNode{}
	for _, n := range graph.Nodes {
		byID[string(n.ID)] = n
	}
	results := parentResults(s)
	matched := 0
	pageEnd := offset
	scanned := 0
	for ev, err := range t.log.Read(ctx, s.ID) {
		if err != nil {
			out.Error = errLogReadFailed
			out.ScanComplete = false
			break
		}
		if scanned == maxPerformanceScan {
			out.Error = "event scan bound reached"
			out.ScanComplete = false
			break
		}
		scanned++
		rows := projectDelegationEvent(ev, byID, results)
		for _, row := range rows {
			if matched >= offset && len(out.Rows) < limit {
				pageEnd = matched + 1
				out.Rows = append(out.Rows, row)
				if !fitsEvidence(out) {
					out.Rows = out.Rows[:len(out.Rows)-1]
					out.ProjectionComplete = false
				}
			}
			matched++
		}
	}
	if pageEnd < matched {
		out.NextOffset = &pageEnd
	}
	return out
}

func parentResults(s *session.Session) map[string]bool {
	out := map[string]bool{}
	for _, m := range s.Conversation.Messages {
		if m.ToolResult != nil {
			out[string(m.ToolResult.CallID)] = m.ToolResult.IsError
		}
	}
	return out
}

func childFields(id string, nodes map[string]lineageNode) (string, string) {
	n, ok := nodes[id]
	if !ok {
		return "", "not_retained"
	}
	return n.Handle, n.State
}
func conclusion(call string, results map[string]bool) (string, *bool) {
	failed, ok := results[call]
	if !ok {
		return "absent", nil
	}
	v := failed
	if failed {
		return "tool_error", &v
	}
	return "tool_result", &v
}

func projectDelegationEvent(ev session.Event, nodes map[string]lineageNode, results map[string]bool) []delegationRow {
	if p := ev.Subagent; p != nil {
		h, ret := childFields(p.ChildID, nodes)
		c, e := conclusion(p.ParentCallID, results)
		r := delegationRow{Type: "subagent", Event: ev.Type, CallID: safeLine(p.ParentCallID), ScopeHandle: h, Retention: ret, Conclusion: c, ParentResultError: e, Stop: p.Stop, Cause: safeLine(p.Cause)}
		if ev.Type == session.EvSubagentStart {
			b := p.Background
			r.Background = &b
			if b {
				r.Collection = "not recorded durably"
			}
		}
		return []delegationRow{r}
	}
	if p := ev.Parallel; p != nil {
		h, ret := childFields(p.ChildID, nodes)
		c, e := conclusion(p.ParentCallID, results)
		r := delegationRow{Type: "parallel", Event: ev.Type, CallID: safeLine(p.ParentCallID), ScopeHandle: h, Retention: ret, Conclusion: c, ParentResultError: e, Join: safeLine(p.Join), Stop: p.Stop}
		if ev.Type == session.EvParallelBranch {
			i := p.BranchIndex
			r.BranchIndex = &i
		}
		if ev.Type == session.EvParallelEnd {
			w := p.Winner
			r.Winner = &w
		}
		return []delegationRow{r}
	}
	if p := ev.Team; p != nil {
		h, ret := childFields(p.MemberSessionID, nodes)
		c, e := conclusion(p.ParentCallID, results)
		base := delegationRow{Type: "team", Event: ev.Type, CallID: safeLine(p.ParentCallID), ScopeHandle: h, Retention: ret, Conclusion: c, ParentResultError: e, Member: safeLine(p.Member), Stop: p.Stop, Cause: safeLine(p.Cause)}
		for _, task := range p.Tasks {
			base.Tasks = append(base.Tasks, teamTaskEvidence{safeLine(task.ID), safeLine(task.Description), safeLine(task.State), safeLine(task.Assignee), safeLines(task.Deps)})
		}
		for _, finding := range p.Findings {
			base.Findings = append(base.Findings, teamFindingEvidence{safeLine(finding.Member), safeLine(finding.Body)})
		}
		if ev.Type == session.EvTeamStart {
			rows := make([]delegationRow, 0, len(p.Roster))
			for _, m := range p.Roster {
				r := base
				r.Member = safeLine(m.Name)
				r.Role = safeLine(m.Role)
				rows = append(rows, r)
			}
			if len(rows) > 0 {
				return rows
			}
		}
		if ev.Type == session.EvTeamEnd && len(p.Dispositions) > 0 {
			rows := make([]delegationRow, 0, len(p.Dispositions))
			for _, d := range p.Dispositions {
				r := base
				r.Member = safeLine(d.Name)
				r.Disposition = safeLine(d.Disposition)
				r.DispositionReason = safeLine(d.Reason)
				r.ErrorRounds = d.ErrorRounds
				rows = append(rows, r)
			}
			return rows
		}
		return []delegationRow{base}
	}
	if p := ev.Schedule; p != nil {
		return []delegationRow{{Type: "schedule", Event: ev.Type, ScheduleName: safeLine(p.ScheduleName), ScheduleKind: safeLine(p.Kind), Stop: p.Stop, Cause: safeLine(p.Err)}}
	}
	return nil
}

func safeLines(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = safeLine(s)
	}
	return out
}

type manifestEvidence struct {
	View               string        `json:"view"`
	Available          bool          `json:"available"`
	Authoritative      bool          `json:"authoritative"`
	Source             string        `json:"source"`
	ProjectionComplete bool          `json:"projection_complete"`
	ScanComplete       bool          `json:"scan_complete"`
	RetentionComplete  bool          `json:"retention_complete"`
	Offset             int           `json:"offset"`
	Limit              int           `json:"limit"`
	NextOffset         *int          `json:"next_offset,omitempty"`
	Error              string        `json:"error,omitempty"`
	Rows               []manifestRow `json:"rows"`
}
type manifestRow struct {
	Provider        string                           `json:"provider,omitempty"`
	Model           string                           `json:"model,omitempty"`
	ReasoningEffort string                           `json:"reasoning_effort,omitempty"`
	ContextWindow   int                              `json:"context_window,omitempty"`
	MessageCount    int                              `json:"message_count"`
	MessageBytes    int                              `json:"message_bytes"`
	Tools           []string                         `json:"tools"`
	Decisions       []session.RequestToolDecision    `json:"decisions"`
	Components      []session.RequestPromptComponent `json:"components"`
}

func (t *inspectTool) manifestView(ctx context.Context, id session.SessionID, offset, requested int) manifestEvidence {
	limit := boundedLimit(requested, maxManifestRows)
	out := manifestEvidence{View: "manifest", Available: t.log != nil, Authoritative: t.log != nil, Source: "request.manifest events", ProjectionComplete: true, ScanComplete: t.log != nil, RetentionComplete: false, Offset: offset, Limit: limit, Rows: []manifestRow{}}
	if t.log == nil {
		out.Error = errLogNotConfigured
		return out
	}
	matched := 0
	pageEnd := offset
	scanned := 0
	for ev, err := range t.log.Read(ctx, id) {
		if err != nil {
			out.Error = errLogReadFailed
			out.ScanComplete = false
			break
		}
		if scanned == maxPerformanceScan {
			out.Error = "event scan bound reached"
			out.ScanComplete = false
			break
		}
		scanned++
		if ev.Type != session.EvRequestManifest || ev.RequestManifest == nil {
			continue
		}
		p := ev.RequestManifest
		if matched >= offset && len(out.Rows) < limit {
			pageEnd = matched + 1
			out.Rows = append(out.Rows, projectManifest(*p))
			if !fitsEvidence(out) {
				out.Rows = out.Rows[:len(out.Rows)-1]
				out.ProjectionComplete = false
			}
		}
		matched++
	}
	if pageEnd < matched {
		out.NextOffset = &pageEnd
	}
	return out
}
func projectManifest(p session.RequestManifestPayload) manifestRow {
	r := manifestRow{
		Provider: safeLine(p.Provider), Model: safeLine(p.Model), ReasoningEffort: safeLine(p.ReasoningEffort),
		ContextWindow: p.ContextWindow, MessageCount: p.MessageCount, MessageBytes: p.MessageBytes,
		Tools: safeLines(p.ToolNames),
	}
	for _, d := range p.ToolDecisions {
		r.Decisions = append(r.Decisions, session.RequestToolDecision{Name: safeLine(d.Name), Source: safeLine(d.Source), Decision: safeLine(d.Decision)})
	}
	for _, c := range p.Prompt {
		r.Components = append(r.Components, session.RequestPromptComponent{Kind: safeLine(c.Kind), Provenance: safeLine(c.Provenance), Bytes: c.Bytes})
	}
	return r
}

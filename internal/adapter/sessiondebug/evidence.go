package sessiondebug

import (
	"context"
	"math"
	"strings"
	"unicode"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const delegationTeam = "team"

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
	Type              string                   `json:"type"`
	Event             session.EventType        `json:"event"`
	CallID            string                   `json:"call_id,omitempty"`
	ScopeHandle       string                   `json:"scope_handle,omitempty"`
	Retention         string                   `json:"retention,omitempty"`
	Conclusion        string                   `json:"parent_conclusion,omitempty"`
	ParentResultError *bool                    `json:"parent_result_error,omitempty"`
	Background        *bool                    `json:"background,omitempty"`
	Collection        string                   `json:"collection,omitempty"`
	Stop              session.StopReason       `json:"stop,omitempty"`
	Cause             string                   `json:"cause,omitempty"`
	Join              string                   `json:"join,omitempty"`
	BranchIndex       *int                     `json:"branch_index,omitempty"`
	Winner            *int                     `json:"winner,omitempty"`
	Role              string                   `json:"role,omitempty"`
	Member            string                   `json:"member,omitempty"`
	Disposition       string                   `json:"disposition,omitempty"`
	DispositionReason string                   `json:"disposition_reason,omitempty"`
	ErrorRounds       int                      `json:"error_rounds,omitempty"`
	Tasks             []teamTaskEvidence       `json:"tasks,omitempty"`
	Findings          []teamFindingEvidence    `json:"findings,omitempty"`
	ActualModel       string                   `json:"actual_model,omitempty"`
	RoutedCategory    string                   `json:"routed_category,omitempty"`
	RoutedModel       string                   `json:"routed_model,omitempty"`
	RoutingReason     string                   `json:"routing_reason,omitempty"`
	RoutingDecision   *routingDecisionEvidence `json:"routing_decision,omitempty"`
	ScheduleName      string                   `json:"schedule_name,omitempty"`
	ScheduleKind      string                   `json:"schedule_kind,omitempty"`
}
type routingDecisionEvidence struct {
	Backend           string   `json:"backend"`
	ClassifierModel   string   `json:"classifier_model"`
	CandidateCategory string   `json:"candidate_category"`
	CandidateModel    string   `json:"candidate_model"`
	Confidence        *float64 `json:"confidence,omitempty"`
	MinimumConfidence *float64 `json:"minimum_confidence,omitempty"`
	Outcome           string   `json:"outcome"`
	ConsecutiveMisses int      `json:"consecutive_misses"`
	MissLimit         int      `json:"miss_limit"`
	BreakerOpen       bool     `json:"breaker_open"`
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
	out := delegationEvidence{
		View: viewDelegation, Authoritative: true, Source: "typed root events joined to direct lineage records",
		ProjectionComplete: true, ScanComplete: t.log != nil && graph.ScanComplete,
		RetentionComplete: graph.Supported && graph.ScanComplete && !graph.Truncated,
		Offset:            offset, Limit: limit, Rows: []delegationRow{}, Error: graph.Error,
	}
	if t.log == nil {
		out.Error = errLogNotConfigured
		out.Authoritative = false
		return out
	}
	byLifetime := map[string]lineageNode{}
	for _, n := range graph.Nodes {
		key := lineageLifetimeKey(string(n.ID), session.IncarnationID(n.Incarnation))
		if _, duplicate := byLifetime[key]; duplicate {
			byLifetime[key] = lineageNode{}
			continue
		}
		byLifetime[key] = n
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
		rows := projectDelegationEvent(ev, byLifetime, results, s)
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

func lineageLifetimeKey(id string, incarnation session.IncarnationID) string {
	return id + "\x00" + string(incarnation)
}

func childFields(id string, incarnation session.IncarnationID, nodes map[string]lineageNode, root *session.Session) (lineageNode, bool) {
	if id == "" || !incarnation.Valid() {
		return lineageNode{}, false
	}
	n, ok := nodes[lineageLifetimeKey(id, incarnation)]
	if !ok || n.Incarnation != string(incarnation) || n.Handle == "" || n.State != string(port.SessionLineageRetained) || n.OwnerScope != session.PrincipalScopeHash(root.Owner) {
		return lineageNode{}, false
	}
	return n, true
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

//nolint:gocyclo // Each delegation family keeps its fail-closed typed-edge checks local.
func projectDelegationEvent(ev session.Event, nodes map[string]lineageNode, results map[string]bool, root *session.Session) []delegationRow {
	if p := ev.Subagent; p != nil {
		n, proven := childFields(p.ChildID, p.ChildIncarnation, nodes, root)
		validEvent := ev.Type == session.EvSubagentStart || ev.Type == session.EvSubagentTool || ev.Type == session.EvSubagentEnd
		if !proven || !validEvent || n.Kind != session.SessionKindSubagent || n.Edge != "subagent" || string(n.Relationship.CallID) != p.ParentCallID {
			return nil
		}
		c, e := conclusion(p.ParentCallID, results)
		r := delegationRow{Type: "subagent", Event: ev.Type, CallID: safeLine(p.ParentCallID), ScopeHandle: n.Handle, Retention: n.State, Conclusion: c, ParentResultError: e, Stop: p.Stop, Cause: safeLine(p.Cause)}
		if ev.Type == session.EvSubagentStart {
			b := p.Background
			r.Background = &b
			r.ActualModel, r.RoutedCategory, r.RoutedModel = safeRoutingLine(p.Model), safeRoutingLine(p.RoutedCategory), safeRoutingLine(p.RoutedModel)
			r.RoutingReason, r.RoutingDecision = safeRoutingReason(p.RoutingReason), projectRoutingDecision(p.RoutingDecision)
			if b {
				r.Collection = "not recorded durably"
			}
		}
		return []delegationRow{r}
	}
	if p := ev.Parallel; p != nil {
		n, proven := childFields(p.ChildID, p.ChildIncarnation, nodes, root)
		if !proven || ev.Type != session.EvParallelBranch || n.Kind != session.SessionKindParallelBranch || n.Edge != "parallel" || string(n.Relationship.CallID) != p.ParentCallID || n.Relationship.BranchIndex == nil || *n.Relationship.BranchIndex != p.BranchIndex {
			return nil
		}
		c, e := conclusion(p.ParentCallID, results)
		r := delegationRow{Type: "parallel", Event: ev.Type, CallID: safeLine(p.ParentCallID), ScopeHandle: n.Handle, Retention: n.State, Conclusion: c, ParentResultError: e, Join: safeLine(p.Join), Stop: p.Stop}
		if p.Kind == session.ParallelBranchStart {
			r.ActualModel, r.RoutedCategory, r.RoutedModel = safeRoutingLine(p.Model), safeRoutingLine(p.RoutedCategory), safeRoutingLine(p.RoutedModel)
			r.RoutingReason, r.RoutingDecision = safeRoutingReason(p.RoutingReason), projectRoutingDecision(p.RoutingDecision)
		}
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
		c, e := conclusion(p.ParentCallID, results)
		if ev.Type == session.EvTeamStart {
			if !parentToolCallExists(root, p.ParentCallID, "Team") {
				return nil
			}
			rows := make([]delegationRow, 0, len(p.Roster))
			for _, m := range p.Roster {
				n, proven := childFields(string(m.MemberSessionID), m.MemberIncarnation, nodes, root)
				if !proven || !validTeamMemberNode(n, root, p.ParentCallID, p.TeamID, m.Name) {
					continue
				}
				rows = append(rows, delegationRow{
					Type: delegationTeam, Event: ev.Type, CallID: safeLine(p.ParentCallID), ScopeHandle: n.Handle,
					Retention: n.State, Conclusion: c, ParentResultError: e, Member: safeLine(m.Name), Role: safeLine(m.Role),
					ActualModel: safeRoutingLine(m.Model), RoutedCategory: safeRoutingLine(m.RoutedCategory),
					RoutedModel: safeRoutingLine(m.RoutedModel), RoutingReason: safeRoutingReason(m.RoutingReason),
					RoutingDecision: projectRoutingDecision(m.RoutingDecision),
				})
			}
			return rows
		}
		n, proven := childFields(p.MemberSessionID, p.MemberIncarnation, nodes, root)
		if !proven || ev.Type != session.EvTeamMember || !parentToolCallExists(root, p.ParentCallID, "Team") ||
			!validTeamMemberNode(n, root, p.ParentCallID, p.TeamID, p.Member) {
			return nil
		}
		base := delegationRow{Type: delegationTeam, Event: ev.Type, CallID: safeLine(p.ParentCallID), ScopeHandle: n.Handle, Retention: n.State, Conclusion: c, ParentResultError: e, Member: safeLine(p.Member), Stop: p.Stop, Cause: safeLine(p.Cause)}
		for _, task := range p.Tasks {
			base.Tasks = append(base.Tasks, teamTaskEvidence{safeLine(task.ID), safeLine(task.Description), safeLine(task.State), safeLine(task.Assignee), safeLines(task.Deps)})
		}
		for _, finding := range p.Findings {
			base.Findings = append(base.Findings, teamFindingEvidence{safeLine(finding.Member), safeLine(finding.Body)})
		}
		return []delegationRow{base}
	}
	return nil
}

func validTeamMemberNode(n lineageNode, root *session.Session, callID, teamID, member string) bool {
	return n.Kind == session.SessionKindTeamMember && n.Edge == delegationTeam &&
		n.Relationship.ParentSessionID == root.ID && n.Relationship.ParentIncarnation == root.Incarnation() &&
		string(n.Relationship.CallID) == callID && n.Relationship.TeamID == teamID && n.Relationship.MemberName == member
}

func parentToolCallExists(root *session.Session, callID, toolName string) bool {
	if root == nil || callID == "" {
		return false
	}
	for _, message := range root.Conversation.Messages {
		for _, call := range message.ToolCalls {
			if string(call.ID) == callID && call.Name == toolName {
				return true
			}
		}
	}
	return false
}

func projectRoutingDecision(in *session.RoutingDecision) *routingDecisionEvidence {
	if in == nil {
		return nil
	}
	out := &routingDecisionEvidence{
		ClassifierModel: safeRoutingLine(in.ClassifierModel), CandidateCategory: safeRoutingLine(in.CandidateCategory),
		CandidateModel: safeRoutingLine(in.CandidateModel), ConsecutiveMisses: in.ConsecutiveMisses,
		MissLimit: in.MissLimit, BreakerOpen: in.BreakerOpen,
	}
	if in.Backend == "llm" || in.Backend == "jev" {
		out.Backend = in.Backend
	}
	if in.Outcome == "routed" || in.Outcome == "fallback" || in.Outcome == "skipped" {
		out.Outcome = in.Outcome
	}
	out.Confidence = safeProbability(in.Confidence)
	out.MinimumConfidence = safeProbability(in.MinimumConfidence)
	return out
}

func safeProbability(in *float64) *float64 {
	if in == nil || math.IsNaN(*in) || math.IsInf(*in, 0) || *in < 0 || *in > 1 {
		return nil
	}
	value := *in
	return &value
}

func safeRoutingLine(in string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, session.ToValidUTF8(in))
	runes := []rune(strings.TrimSpace(clean))
	if len(runes) > 200 {
		runes = runes[:200]
	}
	return string(runes)
}

func safeRoutingReason(in string) string {
	clean := strings.TrimSpace(in)
	switch clean {
	case "", "pinned-model", "agent-def-pinned-model", "resume", "fork", "router-disabled", "route-target-unavailable", "breaker-open", "aborted",
		"degenerate-input", "classifier-error", "cancelled", "bad-verdict", "unknown-category", "timeout", "low-confidence", "input-over-limit", "capacity-timeout",
		"empty-model", "category-selector-empty", "category-target-unresolvable", "routing-miss":
		return clean
	default:
		return "routing-miss"
	}
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
	Provider                  string                           `json:"provider,omitempty"`
	Model                     string                           `json:"model,omitempty"`
	ReasoningEffort           string                           `json:"reasoning_effort,omitempty"`
	ContextWindow             int                              `json:"context_window,omitempty"`
	MessageCount              int                              `json:"message_count"`
	MessageBytes              int                              `json:"message_bytes"`
	AdvertisedToolSchemaBytes int                              `json:"advertised_tool_schema_bytes"`
	Tools                     []string                         `json:"tools"`
	Decisions                 []session.RequestToolDecision    `json:"decisions"`
	Components                []session.RequestPromptComponent `json:"components"`
}

func projectManifest(p session.RequestManifestPayload) manifestRow {
	r := manifestRow{
		Provider: safeLine(p.Provider), Model: safeLine(p.Model), ReasoningEffort: safeLine(p.ReasoningEffort),
		ContextWindow: p.ContextWindow, MessageCount: p.MessageCount, MessageBytes: p.MessageBytes,
		AdvertisedToolSchemaBytes: p.AdvertisedToolSchemaBytes,
		Tools:                     safeLines(p.ToolNames),
	}
	for _, d := range p.ToolDecisions {
		r.Decisions = append(r.Decisions, session.RequestToolDecision{Name: safeLine(d.Name), Source: safeLine(d.Source), Decision: safeLine(d.Decision)})
	}
	for _, c := range p.Prompt {
		r.Components = append(r.Components, session.RequestPromptComponent{Kind: safeLine(c.Kind), Provenance: safeLine(c.Provenance), Bytes: c.Bytes})
	}
	return r
}

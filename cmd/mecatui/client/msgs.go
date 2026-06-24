// Package client is the gRPC-facing layer of mecatui: it dials mecated, creates
// sessions, opens the bidi Converse stream, and translates proto Events into the
// plain Go tea.Msg structs the ui consumes. It is the ONLY mecatui package that
// imports contracts/gen + grpc; the ui never sees a proto type. This boundary is
// deliberate: it keeps the Elm model rendering "pure data" and makes the whole
// event pipeline testable from a scripted fake (see Recver / fakeStream in
// tests) with no network.
package client

import (
	tea "charm.land/bubbletea/v2"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// The msg taxonomy: one struct per proto Event type, plus transport/lifecycle
// msgs that don't originate from the stream. These are plain data — no proto,
// no grpc — so ui can switch over them freely.

// SessionInitMsg marks the run stream as live (proto type "session.init").
type SessionInitMsg struct{ Seq int64 }

// TurnStartMsg opens a new assistant turn; the spinner starts here.
type TurnStartMsg struct{ Turn int32 }

// AssistantDeltaMsg is a streamed chunk of assistant markdown to append+rerender.
type AssistantDeltaMsg struct {
	Turn int32
	Text string
}

// ReasoningDeltaMsg is a streamed chunk of the model's human-readable reasoning
// summary for a turn. Display-only and clearly subordinate to the assistant
// text; the ui renders it collapsed by default.
type ReasoningDeltaMsg struct {
	Turn int32
	Text string
}

// TurnEndMsg closes a turn's model exchange, carrying that turn's token usage
// and the elapsed model-call time. DurationMs is 0 when the server had no clock.
type TurnEndMsg struct {
	Turn       int32
	Usage      Usage
	DurationMs int64
}

// ToolCallMsg announces a tool invocation (status: running until its result).
type ToolCallMsg struct {
	ID   string
	Name string
	Args string // raw JSON
}

// ToolResultMsg resolves the matching ToolCallMsg by CallID.
type ToolResultMsg struct {
	CallID  string
	Content string
	IsError bool
}

// ToolProgressMsg is a transient, human-readable progress line from a
// long-running tool (proto type "tool.progress"). It carries no call id and no
// content — only advisory Text. The ui shows it as a transient status line while
// a tool runs and clears it on the next tool.result (or turn boundary); it is
// never persisted and never enters the conversation transcript.
type ToolProgressMsg struct {
	Text string
}

// PermissionAskMsg opens the approval modal; AskID is the exact correlation key
// echoed back in ResumeApproval — never inferred from the tool name.
type PermissionAskMsg struct {
	AskID  string
	Tool   string
	Args   string // raw JSON
	Reason string
}

// PermissionRetractMsg withdraws a previously surfaced permission ask: the
// owning subagent was cancelled while parked on it, so there is nothing left to
// approve. The ui keeps a FIFO queue of surfaced asks behind the visible modal
// (concurrent subagents can surface asks concurrently): a retract matching the
// VISIBLE ask dismisses the modal and advances the queue; a retract matching a
// QUEUED ask removes it in place; an unknown/stale id is ignored (idempotent).
type PermissionRetractMsg struct {
	AskID string
}

// HookDecision is the outcome a hook fire produced, as plain data the ui colours
// and ranks without touching proto. Mirrors mecatlv1.HookDecision.
type HookDecision string

const (
	// HookInfo is a benign, informational hook notice (the default).
	HookInfo HookDecision = "info"
	// HookBlocked means the hook vetoed the action — the most severe notice.
	HookBlocked HookDecision = "blocked"
	// HookModified means the hook rewrote the action's payload without blocking.
	HookModified HookDecision = "modified"
	// HookAdvisory means the hook flagged content as a finding but did NOT alter
	// the call/result (advisory guardrail). Client-visible warning, model-invisible.
	HookAdvisory HookDecision = "advisory"
)

// HookMsg is an inline hook notice. Beyond the human-readable Text it carries the
// structured Phase (lifecycle point, e.g. "PreToolUse"), the related Tool (for
// per-tool phases), and the Decision (info/blocked/modified/advisory) so the ui can
// render it distinctly from a compaction notice and colour a blocked or advisory hook.
type HookMsg struct {
	Text     string
	Phase    string
	Tool     string
	Decision HookDecision
}

// SubagentKind discriminates the three subagent.* event kinds carried by a
// SubagentMsg, so the ui switches on a plain value rather than re-deriving it.
type SubagentKind string

const (
	// SubagentStart marks a Subagent tool run beginning (Goal set).
	SubagentStart SubagentKind = "start"
	// SubagentTool marks a child tool call resolving (ToolName/IsError/ToolCount set).
	SubagentTool SubagentKind = "tool"
	// SubagentEnd marks a Subagent tool run finishing (ToolCount/Usage/Stop/DurationMs set).
	SubagentEnd SubagentKind = "end"
)

// SubagentMsg is the REDACTED, metadata-only projection of a Subagent tool's
// child run. It carries NO child content — only ids, a goal label, child tool
// names/counts, usage, stop, and duration — so the ui can render a subagent's
// activity under its Subagent card while the child's content stays isolated.
// ParentCallID attributes the msg to the originating Subagent tool block.
// Background marks a detached-delivery (background: true) child; the server sets
// it on subagent.start only, and an older server yields false (no marker).
// RoutedCategory/RoutedModel are the OPT-IN semantic model router's bare
// metadata (a category label + a model id) on subagent.start, empty when no
// router classified the delegation (ADR 0031) — never child content, so
// gauntlet #7 holds.
type SubagentMsg struct {
	Kind           SubagentKind
	ParentCallID   string
	ChildID        string
	Goal           string
	Background     bool
	RoutedCategory string
	RoutedModel    string
	// Model is the concrete model id the child ACTUALLY ran on (subagent.start only),
	// regardless of how it was chosen — inherited default, agent-def pin, per-call
	// override, or the opt-in router (issue #112 / ADR 0035). When routed, Model ==
	// RoutedModel. Bare metadata, never child content, so gauntlet #7 holds.
	Model      string
	ToolName   string
	IsError    bool
	ToolCount  int
	Usage      Usage
	Stop       string
	DurationMs int64
}

// TeamKind discriminates the three team.* event kinds carried by a TeamMsg, so
// the ui switches on a plain value rather than re-deriving it from the proto.
type TeamKind string

const (
	// TeamStart marks a Team run beginning (Roster set).
	TeamStart TeamKind = "start"
	// TeamMember marks one forwarded member-session event (Member/InnerKind set,
	// plus the subset of Text/ToolName/Detail/IsError/Usage relevant to InnerKind).
	TeamMember TeamKind = "member"
	// TeamEnd marks a Team run finishing (Rounds/Stop/Usage set).
	TeamEnd TeamKind = "end"
	// TeamTasks marks a snapshot of the team's shared task list (Tasks set). It maps
	// 1:1 from the first-class team.tasks proto Event.Type — a team-wide event with no
	// Member — so the ui switches on a clean discriminant.
	TeamTasks TeamKind = "tasks"
	// TeamFindings marks a snapshot of the team's shared findings ledger (Findings
	// set). It maps 1:1 from the first-class team.findings proto Event.Type — a
	// team-wide event with no Member — mirroring TeamTasks.
	TeamFindings TeamKind = "findings"
)

// TeamTask is one entry in the team's shared task list, as plain data the ctrl+a
// agents task sub-view renders. Mirrors mecatlv1.TeamTask; carries only task
// metadata, never member content. Deps are the task ids this task waits on.
type TeamTask struct {
	ID          string
	Description string
	State       string
	Assignee    string
	Deps        []string
}

// TeamFinding is one entry in the team's shared findings ledger, as plain data the
// ctrl+a agents findings view renders. Mirrors mecatlv1.TeamFinding; carries only
// the recording member's name and a bounded body preview, never the raw finding.
type TeamFinding struct {
	Member string
	Body   string
}

// TeamMemberDisposition is one member's TERMINAL disposition, set on a TeamEnd msg.
// Mirrors mecatlv1.TeamMemberDisposition; closed-enum supervisor verdicts only, never
// member content. Stopped distinguishes a non-resumable/budget-exhausted member from a
// clean one; Reason refines a stop ("error"/"cancelled"/"budget"; empty when not
// stopped).
type TeamMemberDisposition struct {
	Name    string
	Stopped bool
	Reason  string
}

// TeamMemberSpec is one roster entry forwarded on team.start, as plain data.
// Mirrors mecatlv1.TeamMemberSpec; carries only member metadata, never content.
type TeamMemberSpec struct {
	Name     string
	Role     string
	Mutating bool
	Lead     bool
	// RoutedCategory/RoutedModel are the OPT-IN semantic model router's bare metadata
	// (a category label + a model id) for a routed member, empty when no router
	// classified the member (ADR 0031 / ADR 0034) — never member content, so gauntlet
	// #7 holds.
	RoutedCategory string
	RoutedModel    string
	// Model is the concrete model id the member's engine ACTUALLY runs on (team.start
	// roster only), regardless of how it was chosen (issue #112 / ADR 0035). When routed,
	// Model == RoutedModel. Bare metadata, never member content, so gauntlet #7 holds.
	Model string
}

// TeamMsg is the BOUNDED projection of an in-process team's run, as plain data
// the ui renders on the Team tool card. Unlike the metadata-only SubagentMsg, a
// team.member event carries BOUNDED member CONTENT (Text / a capped Detail
// preview) — the team is meant to be watched. It is still bounded and redacted
// server-side, and never enters the parent conversation. ParentCallID attributes
// the msg to the originating Team tool block.
type TeamMsg struct {
	Kind         TeamKind
	ParentCallID string
	TeamID       string
	// Roster is set on TeamStart.
	Roster []TeamMemberSpec
	// Member / InnerKind and the per-event content are set on TeamMember.
	Member string
	// MemberSessionID is the member's child SESSION id ("team-<teamID>-<member>") —
	// the CancelChild handle, set on TeamMember msgs. Carried explicitly so the ui
	// never derives the (server-internal) id grammar; empty from an older server.
	MemberSessionID string
	InnerKind       string
	Text            string
	ToolName        string
	Detail          string
	IsError         bool
	// Rounds / Stop are set on TeamEnd.
	Rounds int
	Stop   string
	// Usage is a member's per-event usage (TeamMember turn.end/result) or, on
	// TeamEnd, the summed team total.
	Usage Usage
	// ContextUsed / ContextWindow are the per-member context-meter numerator
	// (current context occupancy — the most recent turn's input tokens) and
	// denominator (the member engine's context window), set on TeamMember turn.end;
	// 0 when unknown. They drive the band bar on each member lane in the ctrl+a
	// agents overlay.
	ContextUsed   int64
	ContextWindow int64
	// Tasks is the team's shared task-list snapshot, set on a TeamTasks msg (the
	// first-class team.tasks event) and on TeamEnd. It feeds the ctrl+a agents task
	// sub-view.
	Tasks []TeamTask
	// Findings is the team's shared findings-ledger snapshot, set on a TeamFindings
	// msg (the first-class team.findings event) and on TeamEnd. It feeds the ctrl+a
	// agents findings view.
	Findings []TeamFinding
	// Dispositions is the per-member terminal disposition snapshot, set on a TeamEnd
	// msg. It lets the overlay render a stopped member distinctly from a clean "done".
	Dispositions []TeamMemberDisposition
}

// ParallelKind discriminates the parallel.* event kinds carried by a ParallelMsg, so
// the ui switches on a plain value rather than re-deriving it from the proto. The
// run-level start/end are their own kinds; the per-branch events carry the branch_*
// kinds (from the proto Parallel.kind discriminant).
type ParallelKind string

const (
	// ParallelStart marks a Parallel fork-join run beginning (Join/BranchCount set).
	ParallelStart ParallelKind = "start"
	// ParallelBranchStart marks one branch beginning (BranchIndex/BranchLabel/Goal set).
	ParallelBranchStart ParallelKind = "branch_start"
	// ParallelBranchTool marks one branch's child tool resolving (ToolName/IsError/ToolCount set).
	ParallelBranchTool ParallelKind = "branch_tool"
	// ParallelBranchEnd marks one branch finishing (Stop/Usage/DurationMs/Failed/Workspace set).
	ParallelBranchEnd ParallelKind = "branch_end"
	// ParallelEnd marks a Parallel run finishing (Join/Winner/WinnerWorkspace/Usage/Stop set).
	ParallelEnd ParallelKind = "end"
)

// ParallelMsg is the REDACTED, metadata-only projection of a Parallel fork-join run, as
// plain data the ui renders in the ctrl+a Parallel tab. Unlike the FLAT SubagentMsg, a
// Parallel run is a GROUP: N branches of ONE call (keyed by ParentCallID) sharing a join
// strategy + a single winner + preserved per-branch fork paths. It carries NO branch
// content — only ids, a goal label, child tool names/counts, usage, stop, duration, the
// join strategy, the winner index, and the fork-root PATHS (handles already in the result
// text, not branch content). ParentCallID is the group key.
type ParallelMsg struct {
	Kind         ParallelKind
	ParentCallID string
	// Join / BranchCount are set on ParallelStart and ParallelEnd (run-level).
	Join        string
	BranchCount int
	// BranchIndex is the stable per-branch key, set on every branch_* kind.
	BranchIndex int
	// ChildID is the branch's child SESSION id ("parallel-<callID>-<i>") — the
	// CancelChild handle, set on branch_start/branch_end. Carried explicitly so the
	// ui never derives the (server-internal) id grammar; empty from an older server.
	ChildID string
	// BranchLabel / Goal are set on ParallelBranchStart.
	BranchLabel string
	Goal        string
	// RoutedCategory/RoutedModel are the OPT-IN semantic model router's bare metadata
	// (a category label + a model id) for a routed branch, set on branch_start only,
	// empty when no router classified the branch (ADR 0031 / ADR 0034) — never branch
	// content, so gauntlet #7 holds.
	RoutedCategory string
	RoutedModel    string
	// Model is the concrete model id the branch ACTUALLY ran on (branch_start only),
	// regardless of how it was chosen (issue #112 / ADR 0035). When routed, Model ==
	// RoutedModel. Bare metadata, never branch content, so gauntlet #7 holds.
	Model string
	// ToolName / IsError / ToolCount carry per-branch tool activity (branch_tool;
	// ToolCount is also final on branch_end).
	ToolName  string
	IsError   bool
	ToolCount int
	// Failed / Workspace are set on ParallelBranchEnd (Workspace is the branch's fork root).
	Failed    bool
	Workspace string
	// Stop is the branch terminal (branch_end) or the run-level stop (end).
	Stop string
	// Usage is the branch's cumulative usage (branch_end) or the run total (end).
	Usage Usage
	// DurationMs is the branch's wall-clock duration (branch_end).
	DurationMs int64
	// Winner / WinnerWorkspace are set on ParallelEnd: the real winning branch index
	// (-1 for join=all / none-succeeded) and its preserved fork root.
	Winner          int
	WinnerWorkspace string
}

// CompactionMsg is a muted "history compacted" notice.
type CompactionMsg struct{ Text string }

// NoProgressMsg is a muted advisory notice emitted when a completed turn produced
// no tool call and no meaningful text and the loop is nudging the model to continue
// (or giving up after the budget). It is rendered as a transient status line, like
// CompactionMsg; it carries only the harness-authored reason Text (no model content).
type NoProgressMsg struct{ Text string }

// ResultMsg is the terminal event: stop reason, final text, error, usage.
type ResultMsg struct {
	Stop  string
	Text  string
	Error string
	Usage Usage
}

// Usage is the token accounting carried by ResultMsg (and usage-bearing events).
// Duplicated as a plain struct so ui stays proto-free.
type Usage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
}

// Transport / lifecycle msgs (NOT from the proto stream).

// SessionReadyMsg carries the session id AND the server's capabilities from the
// async CreateSession. Capabilities drives the ui's honest discoverability
// affordances; an older server yields the all-false zero value.
type SessionReadyMsg struct {
	SessionID    string
	Capabilities Capabilities
	// ResolvedModel is the EFFECTIVE provider+model the session resolved to (server-
	// owned, echoed verbatim). The ui shows it in the header from turn zero; an older
	// server yields the zero value (no model segment).
	ResolvedModel ResolvedModel
	// Mode is the server-confirmed permission posture; empty means older create path /
	// default.
	Mode string
}

// ConnectErrMsg reports a dial/CreateSession failure.
type ConnectErrMsg struct{ Err error }

// StreamErrMsg reports a non-EOF Recv error on the Converse stream.
type StreamErrMsg struct{ Err error }

// StreamClosedMsg reports a clean stream close (io.EOF) without a result event
// (e.g. server closed early). Normal completion arrives as ResultMsg first.
type StreamClosedMsg struct{}

// hookDecisionFrom converts a proto HookDecision enum to the plain HookDecision
// string the ui keys off. An unspecified/unknown value (including a nil Hook,
// since GetDecision is nil-safe) maps to HookInfo, the benign baseline.
func hookDecisionFrom(d mecatlv1.HookDecision) HookDecision {
	switch d {
	case mecatlv1.HookDecision_HOOK_DECISION_BLOCKED:
		return HookBlocked
	case mecatlv1.HookDecision_HOOK_DECISION_MODIFIED:
		return HookModified
	case mecatlv1.HookDecision_HOOK_DECISION_ADVISORY:
		return HookAdvisory
	default:
		return HookInfo
	}
}

// subagentMsg builds a SubagentMsg of the given kind from a proto Subagent
// payload (nil-safe via the generated getters). It is the single translation
// point for the three subagent.* event kinds.
func subagentMsg(kind SubagentKind, s *mecatlv1.Subagent) SubagentMsg {
	return SubagentMsg{
		Kind:           kind,
		ParentCallID:   s.GetParentCallId(),
		ChildID:        s.GetChildId(),
		Goal:           s.GetGoal(),
		Background:     s.GetBackground(),
		RoutedCategory: s.GetRoutedCategory(),
		RoutedModel:    s.GetRoutedModel(),
		Model:          s.GetModel(),
		ToolName:       s.GetToolName(),
		IsError:        s.GetIsError(),
		ToolCount:      int(s.GetToolCount()),
		Usage:          usageFrom(s.GetUsage()),
		Stop:           s.GetStop(),
		DurationMs:     s.GetDurationMs(),
	}
}

// parallelMsg builds a ParallelMsg from a proto Parallel payload (nil-safe via the
// generated getters). The kind is derived from the event type (start/end) or, for a
// parallel.branch event, the proto kind discriminant (branch_start/tool/end). It is the
// single translation point for the parallel.* event family.
func parallelMsg(kind ParallelKind, p *mecatlv1.Parallel) ParallelMsg {
	return ParallelMsg{
		Kind:            kind,
		ParentCallID:    p.GetParentCallId(),
		Join:            p.GetJoin(),
		BranchCount:     int(p.GetBranchCount()),
		BranchIndex:     int(p.GetBranchIndex()),
		ChildID:         p.GetChildId(),
		BranchLabel:     p.GetBranchLabel(),
		Goal:            p.GetGoal(),
		RoutedCategory:  p.GetRoutedCategory(),
		RoutedModel:     p.GetRoutedModel(),
		Model:           p.GetModel(),
		ToolName:        p.GetToolName(),
		IsError:         p.GetIsError(),
		ToolCount:       int(p.GetToolCount()),
		Failed:          p.GetFailed(),
		Workspace:       p.GetWorkspace(),
		Stop:            p.GetStop(),
		Usage:           usageFrom(p.GetUsage()),
		DurationMs:      p.GetDurationMs(),
		Winner:          int(p.GetWinner()),
		WinnerWorkspace: p.GetWinnerWorkspace(),
	}
}

// parallelBranchKind maps the proto parallel.branch kind discriminant to its
// ParallelKind. An unknown/empty kind falls back to ParallelBranchTool (the benign
// metadata-only kind) so a future kind never crashes the reader.
func parallelBranchKind(protoKind string) ParallelKind {
	switch protoKind {
	case "branch_start":
		return ParallelBranchStart
	case "branch_end":
		return ParallelBranchEnd
	default:
		return ParallelBranchTool
	}
}

// teamMsg builds a TeamMsg of the given kind from a proto Team payload (nil-safe
// via the generated getters). It is the single translation point for the team.*
// event kinds; the roster is converted to plain TeamMemberSpec values and the task
// snapshot to plain TeamTask values.
func teamMsg(kind TeamKind, t *mecatlv1.Team) TeamMsg {
	msg := TeamMsg{
		Kind:            kind,
		ParentCallID:    t.GetParentCallId(),
		TeamID:          t.GetTeamId(),
		Member:          t.GetMember(),
		MemberSessionID: t.GetMemberSessionId(),
		InnerKind:       t.GetInnerKind(),
		Text:            t.GetText(),
		ToolName:        t.GetToolName(),
		Detail:          t.GetDetail(),
		IsError:         t.GetIsError(),
		Rounds:          int(t.GetRounds()),
		Stop:            t.GetStop(),
		Usage:           usageFrom(t.GetUsage()),
		ContextUsed:     t.GetContextUsed(),
		ContextWindow:   t.GetContextWindow(),
	}
	for _, r := range t.GetRoster() {
		msg.Roster = append(msg.Roster, TeamMemberSpec{
			Name:           r.GetName(),
			Role:           r.GetRole(),
			Mutating:       r.GetMutating(),
			Lead:           r.GetLead(),
			RoutedCategory: r.GetRoutedCategory(),
			RoutedModel:    r.GetRoutedModel(),
			Model:          r.GetModel(),
		})
	}
	for _, tk := range t.GetTasks() {
		msg.Tasks = append(msg.Tasks, TeamTask{
			ID:          tk.GetId(),
			Description: tk.GetDescription(),
			State:       tk.GetState(),
			Assignee:    tk.GetAssignee(),
			Deps:        tk.GetDeps(),
		})
	}
	for _, f := range t.GetFindings() {
		msg.Findings = append(msg.Findings, TeamFinding{Member: f.GetMember(), Body: f.GetBody()})
	}
	for _, d := range t.GetDispositions() {
		msg.Dispositions = append(msg.Dispositions, TeamMemberDisposition{
			Name:    d.GetName(),
			Stopped: d.GetStopped(),
			Reason:  reasonString(d.GetReason()),
		})
	}
	return msg
}

// reasonString maps a proto TeamMemberStopReason enum to the plain reason string the
// ui consumes ("error"/"cancelled"/"budget"; "" for UNSPECIFIED/done). Keeping the
// enum→string switch here keeps the ui layer proto-free.
func reasonString(r mecatlv1.TeamMemberStopReason) string {
	switch r {
	case mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_ERROR:
		return "error"
	case mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_CANCELLED:
		return "cancelled"
	case mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_BUDGET:
		return "budget"
	default:
		return ""
	}
}

// usageFrom converts a proto Usage (nil-safe) to the plain struct.
func usageFrom(u *mecatlv1.Usage) Usage {
	if u == nil {
		return Usage{}
	}
	return Usage{
		InputTokens:      u.GetInputTokens(),
		OutputTokens:     u.GetOutputTokens(),
		CacheReadTokens:  u.GetCacheReadTokens(),
		CacheWriteTokens: u.GetCacheWriteTokens(),
	}
}

// EventToMsg maps a single proto Event onto its tea.Msg. It is a total function
// over the documented type strings; an unknown/empty type returns nil (the
// reader skips nil so unknown future event kinds are ignored, not fatal). This
// is the single translation point between the proto schema and the ui model and
// is unit-tested over every type.
func EventToMsg(ev *mecatlv1.Event) tea.Msg {
	if ev == nil {
		return nil
	}
	switch ev.GetType() {
	case "session.init":
		return SessionInitMsg{Seq: ev.GetSeq()}
	case "turn.start":
		return TurnStartMsg{Turn: ev.GetTurn()}
	case "turn.end":
		te := ev.GetTurnEnd()
		return TurnEndMsg{Turn: ev.GetTurn(), Usage: usageFrom(te.GetUsage()), DurationMs: te.GetDurationMs()}
	case "message.delta":
		return AssistantDeltaMsg{Turn: ev.GetTurn(), Text: ev.GetText()}
	case "reasoning.delta":
		return ReasoningDeltaMsg{Turn: ev.GetTurn(), Text: ev.GetText()}
	case "tool.call":
		tc := ev.GetToolCall()
		return ToolCallMsg{ID: tc.GetId(), Name: tc.GetName(), Args: tc.GetArgs()}
	case "tool.result":
		tr := ev.GetToolResult()
		return ToolResultMsg{CallID: tr.GetCallId(), Content: tr.GetContent(), IsError: tr.GetIsError()}
	case "tool.progress":
		return ToolProgressMsg{Text: ev.GetText()}
	case "permission.ask":
		a := ev.GetAsk()
		return PermissionAskMsg{AskID: a.GetAskId(), Tool: a.GetTool(), Args: a.GetArgs(), Reason: a.GetReason()}
	case "permission.retract":
		// The retraction payload rides the same ask field, carrying the AskID only
		// (server-authored; no tool/args/reason).
		return PermissionRetractMsg{AskID: ev.GetAsk().GetAskId()}
	case "hook":
		h := ev.GetHook()
		return HookMsg{
			Text:     ev.GetText(),
			Phase:    h.GetPhase(),
			Tool:     h.GetTool(),
			Decision: hookDecisionFrom(h.GetDecision()),
		}
	case "compaction":
		return CompactionMsg{Text: ev.GetText()}
	case "no_progress":
		return NoProgressMsg{Text: ev.GetText()}
	case "result":
		r := ev.GetResult()
		return ResultMsg{
			Stop:  r.GetStop(),
			Text:  r.GetText(),
			Error: r.GetError(),
			Usage: usageFrom(r.GetUsage()),
		}
	default:
		// The subagent.* / team.* delegation projections are mapped by
		// delegationEventToMsg (a second switch) to keep this dispatcher under the
		// cyclomatic-complexity bound. The two switches are total over the documented
		// type strings ONLY together: a new delegation case must be added there, not
		// here. An unknown/empty type returns nil so future event kinds are ignored,
		// not fatal.
		return delegationEventToMsg(ev)
	}
}

// delegationEventToMsg maps the subagent.* and team.* delegation-projection event
// types to their tea.Msg. It is the second half of EventToMsg, split out only so
// neither dispatcher grows past the cyclomatic-complexity bound; together they
// remain a total function over the documented type strings. An unmatched type
// returns nil (skipped by the reader).
func delegationEventToMsg(ev *mecatlv1.Event) tea.Msg {
	switch ev.GetType() {
	case "subagent.start":
		return subagentMsg(SubagentStart, ev.GetSubagent())
	case "subagent.tool":
		return subagentMsg(SubagentTool, ev.GetSubagent())
	case "subagent.end":
		return subagentMsg(SubagentEnd, ev.GetSubagent())
	case "team.start":
		return teamMsg(TeamStart, ev.GetTeam())
	case "team.member":
		return teamMsg(TeamMember, ev.GetTeam())
	case "team.tasks":
		return teamMsg(TeamTasks, ev.GetTeam())
	case "team.findings":
		return teamMsg(TeamFindings, ev.GetTeam())
	case "team.end":
		return teamMsg(TeamEnd, ev.GetTeam())
	case "parallel.start":
		return parallelMsg(ParallelStart, ev.GetParallel())
	case "parallel.branch":
		return parallelMsg(parallelBranchKind(ev.GetParallel().GetKind()), ev.GetParallel())
	case "parallel.end":
		return parallelMsg(ParallelEnd, ev.GetParallel())
	default:
		return nil
	}
}

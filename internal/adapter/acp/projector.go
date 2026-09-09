package acp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
)

// projector.go is the ACP analogue of internal/adapter/server/mapper.go: it
// translates a domain session.Event into the ACP session/update payload(s) the
// editor renders. It is PURE (no I/O, no shared state) so it is exhaustively
// table-testable, and it is the single place that decides which events map, which
// fold, and which drop this phase.
//
// PROJECTION TABLE (Phase 2):
//
//	EvMessageDelta   -> agent_message_chunk{ content: text }
//	EvReasoningDelta -> agent_thought_chunk{ content: text }   (display summary
//	                    only — NEVER the encrypted_content replay blob, which the
//	                    loop never emits as a reasoning.delta anyway)
//	EvToolCall       -> tool_call{ toolCallId, title, kind, rawInput, status: pending,
//	                    content: [diff] for Edit/Write (synthesized from the args) }
//	EvToolResult     -> tool_call_update{ toolCallId, status: completed|failed,
//	                    content: [text] }
//	EvHook           -> for a blocked PreToolUse hook carrying the originating
//	                    tool-call id, a tool_call_update marking that exact call
//	                    FAILED with the reason (the card was opened before the gate);
//	                    PostToolUse blocks and all others -> an agent_thought_chunk note.
//	EvSubagentTool/  -> tool_call_update on the PARENT Subagent call (keyed by
//	  EvSubagentEnd      SubagentPayload.ParentCallID): progress content lines while
//	                     the child runs; completed/failed on end.
//	EvTeamMember/    -> tool_call_update on the PARENT Team call (keyed by
//	  EvTeamEnd          TeamPayload.ParentCallID): member activity lines; on end.
//	EvPermissionAsk  -> handled out of band (an OUTBOUND request_permission), not a
//	                    session/update; see permissionRequest below.
//	EvResult         -> the prompt's terminal stopReason (see stopReasonFor).
//
//	DROPPED/FOLDED this phase (no clean ACP mapping yet, see the ADR):
//	  - turn.start / turn.end      -> dropped (lifecycle bookkeeping).
//	  - compaction                 -> dropped.
//	  - subagent.start / team.start -> dropped (the parent tool_call already names
//	                                   the delegated work; the roster/goal would add
//	                                   noise before the first progress line).
//	  - session.init               -> dropped.
//	  - tool.progress              -> dropped (transient, no clean ACP surface: it
//	                                   carries no call id, and the tool's own
//	                                   tool_call already shows it as pending/running;
//	                                   a benign drop, never an error).

// projectUpdate maps a domain Event to the session/update variant value to send,
// or (nil,false) when the event has no session/update projection this phase
// (dropped, folded elsewhere, or handled out of band like permission.ask and the
// terminal result). The returned value is one of the union variant types
// (chunkUpdate / toolCallUpdate) ready to marshal as the notification's "update".
func projectUpdate(ev session.Event) (any, bool) {
	switch ev.Type {
	case session.EvMessageDelta:
		if ev.Text == "" {
			return nil, false
		}
		return chunkUpdate{SessionUpdate: updateAgentMessageChunk, Content: textBlock(ev.Text)}, true

	case session.EvReasoningDelta:
		if ev.Text == "" {
			return nil, false
		}
		return chunkUpdate{SessionUpdate: updateAgentThoughtChunk, Content: textBlock(ev.Text)}, true

	case session.EvToolCall:
		if ev.ToolCall == nil {
			return nil, false
		}
		return toolCallUpdate{
			SessionUpdate: updateToolCall,
			ToolCallID:    string(ev.ToolCall.ID),
			Title:         ev.ToolCall.Name,
			Kind:          toolKindFor(ev.ToolCall.Name),
			RawInput:      rawInput(ev.ToolCall.Args),
			Status:        toolStatusPending,
			// For Edit/Write, synthesize an ACP diff block from the call's args so the
			// editor can render a native inline diff at the moment the call is shown
			// (before it even runs). Malformed args -> no diff (nil), and the
			// tool_call_update result will still carry the text content later.
			Content: diffContentFor(ev.ToolCall.Name, ev.ToolCall.Args),
		}, true

	case session.EvAuthorizationRequired:
		if ev.Authorization == nil {
			return nil, false
		}
		text := "MCP authorization" + authorizationDisplayTarget(ev.Authorization) + " is unavailable for ACP sessions"
		return toolCallUpdate{SessionUpdate: updateToolCallUpdate, ToolCallID: string(ev.Authorization.Call), Status: toolStatusFailed, Content: textToolContent(text)}, true

	case session.EvAuthorizationResolved:
		if ev.Authorization == nil {
			return nil, false
		}
		text := "MCP authorization" + authorizationDisplayTarget(ev.Authorization) + " status: " + string(ev.Authorization.Status)
		return chunkUpdate{SessionUpdate: updateAgentThoughtChunk, Content: textBlock(text)}, true

	case session.EvToolResult:
		if ev.ToolResult == nil {
			return nil, false
		}
		status := toolStatusCompleted
		if ev.ToolResult.IsError {
			status = toolStatusFailed
		}
		return toolCallUpdate{
			SessionUpdate: updateToolCallUpdate,
			ToolCallID:    string(ev.ToolResult.CallID),
			Status:        status,
			Content:       textToolContent(ev.ToolResult.Content),
		}, true

	case session.EvHook:
		return projectHook(ev)

	case session.EvSubagentTool, session.EvSubagentEnd:
		return projectSubagent(ev)

	case session.EvTeamMember, session.EvTeamEnd:
		return projectTeam(ev)

	default:
		// turn.*, compaction, session.init, subagent.start, team.start, team.tasks,
		// tool.progress, result, permission.ask: no session/update projection here.
		// (The shared task list is a TUI-overlay affordance, not an ACP editor card
		// line; tool.progress is a transient advisory drop, never an error.)
		return nil, false
	}
}

func authorizationDisplayTarget(p *session.AuthorizationPayload) string {
	if p == nil || p.DisplayName == "" || !p.Valid() {
		return ""
	}
	return " for " + p.DisplayName
}

// editArgs / writeArgs mirror the JSON arg shapes of internal/adapter/tools'
// Edit and Write tools (path/old_string/new_string and path/content). They are
// duplicated here deliberately — the projector is a domain-adjacent adapter that
// must NOT import the tools adapter — and kept minimal (only the fields a diff
// needs).
type editArgs struct {
	Path      string `json:"path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// diffContentFor synthesizes an ACP "diff" ToolCallContent for an Edit/Write tool
// call from its raw JSON args, so an editor renders a native inline diff. For any
// other tool, or when the args are malformed or missing the path, it returns nil
// (the caller falls back to the plain-text result content). Mapping:
//
//	Edit  -> oldText = old_string, newText = new_string, path = path.
//	Write -> newText = content,    oldText omitted (a new/overwritten file).
func diffContentFor(name string, args []byte) []toolCallContent {
	switch name {
	case "Edit":
		var a editArgs
		if err := json.Unmarshal(args, &a); err != nil || a.Path == "" {
			return nil
		}
		old := a.OldString
		return diffToolContent(a.Path, &old, a.NewString)
	case "Write":
		var a writeArgs
		if err := json.Unmarshal(args, &a); err != nil || a.Path == "" {
			return nil
		}
		// A Write creates or fully replaces the file; ACP models "no prior content"
		// as an absent oldText, so we pass nil.
		return diffToolContent(a.Path, nil, a.Content)
	default:
		return nil
	}
}

// projectHook maps an EvHook to the right ACP surface:
//
//   - A BLOCKED PreToolUse hook that carries the originating tool-call id marks that
//     exact tool_call_update FAILED with the veto reason. The veto lands ON the tool
//     card the loop opened BEFORE the permission/hook gate (see openCard in
//     engine/agent/dispatch.go), so the id is always one the client has seen. The
//     PreToolUse block also emits a synthesized error EvToolResult on the same id,
//     which projects to its own FAILED update — two `failed` updates settle the same
//     already-open card, which is harmless (the hook update carries the veto reason,
//     the result update the synthesized error body).
//   - Every other hook projects to an agent_thought_chunk note carrying the reason.
//     In particular a PostToolUse block is annotate-only by domain semantics — the
//     tool already ran and its (successful) EvToolResult settles the card; surfacing
//     the block as a thought avoids overwriting that with a spurious `failed`.
//
// The phase guard is a STRING compare against "PreToolUse" because the acp package
// must not import engine/governance; that value is string(governance.PhasePreToolUse).
func projectHook(ev session.Event) (any, bool) {
	if ev.Hook == nil {
		return nil, false
	}
	if ev.Hook.Decision == session.HookBlocked && ev.Hook.CallID != "" && ev.Hook.Phase == "PreToolUse" {
		return toolCallUpdate{
			SessionUpdate: updateToolCallUpdate,
			ToolCallID:    string(ev.Hook.CallID),
			Status:        toolStatusFailed,
			Content:       textToolContent(hookNote(ev.Hook, ev.Text)),
		}, true
	}
	return chunkUpdate{SessionUpdate: updateAgentThoughtChunk, Content: textBlock(hookNote(ev.Hook, ev.Text))}, true
}

// hookNote renders a short, human-readable line for a hook event, preferring the
// event's own Text and falling back to a phase/decision summary.
func hookNote(h *session.HookPayload, text string) string {
	if strings.TrimSpace(text) != "" {
		return text
	}
	phase := h.Phase
	if phase == "" {
		phase = "hook"
	}
	if h.Tool != "" {
		return fmt.Sprintf("%s %s: %s", phase, h.Tool, h.Decision)
	}
	return fmt.Sprintf("%s: %s", phase, h.Decision)
}

// projectSubagent maps a subagent.tool / subagent.end event to a tool_call_update
// on the PARENT Subagent tool call (keyed by SubagentPayload.ParentCallID), so the
// child's redacted activity surfaces as progress on the editor's Subagent card.
// subagent.tool appends an in_progress progress line (the child tool name); the
// terminal subagent.end finalizes the Subagent call status. Nothing here carries child
// content beyond the already-redacted metadata on the payload.
func projectSubagent(ev session.Event) (any, bool) {
	p := ev.Subagent
	if p == nil || p.ParentCallID == "" {
		return nil, false
	}
	switch ev.Type {
	case session.EvSubagentTool:
		line := fmt.Sprintf("subagent: %s (call #%d)", p.ToolName, p.ToolCount)
		if p.IsError {
			line += " [error]"
		}
		return toolCallUpdate{
			SessionUpdate: updateToolCallUpdate,
			ToolCallID:    p.ParentCallID,
			Status:        toolStatusInProgress,
			Content:       textToolContent(line),
		}, true
	case session.EvSubagentEnd:
		status := toolStatusInProgress
		if p.Stop == session.StopError {
			status = toolStatusFailed
		}
		line := fmt.Sprintf("subagent finished: %d tool call(s), %s", p.ToolCount, p.Stop)
		if p.Cause != "" {
			// WHY it failed, not just that it did (issue #319). Without this an ACP
			// client sees a failed delegation card with no reason while a mecatui user
			// sees the cause — two projections of one event disagreeing about how much
			// of the contract they surface. The value arrives already clamped AND
			// whitespace-collapsed (agent.subagentCausePayload at the emit site, the one
			// place that projection is built), and it is harness/provider metadata —
			// never child-authored output — so gauntlet #7 holds.
			line += ": " + p.Cause
		}
		return toolCallUpdate{
			SessionUpdate: updateToolCallUpdate,
			ToolCallID:    p.ParentCallID,
			Status:        status,
			Content:       textToolContent(line),
		}, true
	default:
		return nil, false
	}
}

// projectTeam maps a team.member / team.end event to a tool_call_update on the
// PARENT Team tool call (keyed by TeamPayload.ParentCallID), surfacing the team's
// bounded member activity as progress on the editor's Team card. team.member
// appends an in_progress line tagged by member name; team.end finalizes. Every
// preview is already bounded/redacted at source (see TeamPayload).
func projectTeam(ev session.Event) (any, bool) {
	p := ev.Team
	if p == nil || p.ParentCallID == "" {
		return nil, false
	}
	switch ev.Type {
	case session.EvTeamMember:
		line := teamMemberLine(p)
		if line == "" {
			return nil, false
		}
		return toolCallUpdate{
			SessionUpdate: updateToolCallUpdate,
			ToolCallID:    p.ParentCallID,
			Status:        toolStatusInProgress,
			Content:       textToolContent(line),
		}, true
	case session.EvTeamEnd:
		status := toolStatusInProgress
		if p.Stop == session.StopError {
			status = toolStatusFailed
		}
		line := fmt.Sprintf("team finished: %d round(s), %s", p.Rounds, p.Stop)
		return toolCallUpdate{
			SessionUpdate: updateToolCallUpdate,
			ToolCallID:    p.ParentCallID,
			Status:        status,
			Content:       textToolContent(line),
		}, true
	default:
		return nil, false
	}
}

// teamMemberLine renders one progress line for a team.member event from the
// already-bounded payload, choosing the relevant fields by the inner kind. It
// returns "" when there is nothing to show (so the caller drops the update).
func teamMemberLine(p *session.TeamPayload) string {
	switch p.InnerKind {
	case session.EvToolCall:
		if p.ToolName == "" {
			return ""
		}
		line := fmt.Sprintf("%s: %s", p.Member, p.ToolName)
		if p.Detail != "" {
			line += " " + p.Detail
		}
		return line
	case session.EvToolResult:
		line := fmt.Sprintf("%s: %s ->", p.Member, p.ToolName)
		if p.IsError {
			line += " [error]"
		}
		if p.Detail != "" {
			line += " " + p.Detail
		}
		return line
	default:
		// message.delta / result / turn.end: surface the member's text if any.
		// On a per-round result that ended StopError, lead with WHY it failed
		// (issue #331) — the harness/provider error, never member-authored
		// output (gauntlet #7, same footing as Subagent.cause). A clean/empty
		// result still drops when there is no text.
		if p.Cause != "" {
			return fmt.Sprintf("%s failed: %s", p.Member, p.Cause)
		}
		if strings.TrimSpace(p.Text) == "" {
			return ""
		}
		return fmt.Sprintf("%s: %s", p.Member, p.Text)
	}
}

// rawInput normalizes a tool call's raw JSON args for the ACP rawInput field. An
// empty payload becomes nil (omitted) so the field is absent rather than an empty
// blob.
func rawInput(args []byte) []byte {
	if len(args) == 0 {
		return nil
	}
	return args
}

// toolKindFor maps a mecatl tool name to the nearest ACP ToolKind so the editor
// can pick an icon. Unknown tools (including MCP tools) fall back to "other".
func toolKindFor(name string) string {
	switch name {
	case "Read":
		return "read"
	case "Edit", "Write":
		return "edit"
	case "Grep", "Glob":
		return "search"
	case "Shell":
		return "execute"
	case "WebFetch":
		return "fetch"
	case "WebSearch":
		return "search"
	case "Subagent", "Team", "Parallel":
		return "think"
	default:
		return "other"
	}
}

// stopReasonFor maps the domain terminal StopReason (carried on EvResult) to the
// ACP PromptResponse.stopReason. The two limit reasons collapse to ACP's single
// max_turn_requests; a domain error has no clean ACP terminal, so it maps to
// end_turn (the editor still receives any error text via the preceding message
// chunks) — this is the documented "clean terminal" for an error this phase.
func stopReasonFor(r session.StopReason) string {
	switch r {
	case session.StopEndTurn:
		return stopEndTurn
	case session.StopCancelled:
		return stopCancelled
	case session.StopMaxTurns, session.StopMaxToolCalls, session.StopMaxConsecutiveFailures:
		return stopMaxTurnRequests
	case session.StopError:
		return stopEndTurn
	default:
		return stopEndTurn
	}
}

// permissionRequestFor builds the OUTBOUND session/request_permission params for
// a domain permission.ask. It offers the four standard options; the title/kind
// reflect the tool awaiting approval, and rawInput carries the proposed args so
// the editor can show what is about to run. The toolCall has no sessionUpdate
// field (it is the request's toolCall, not a notification).
func permissionRequestFor(sessionID string, ask session.PendingAsk) requestPermissionRequest {
	return requestPermissionRequest{
		SessionID: sessionID,
		ToolCall: toolCallUpdate{
			ToolCallID: ask.AskID,
			Title:      ask.Tool,
			Kind:       toolKindFor(ask.Tool),
			Status:     toolStatusPending,
			RawInput:   rawInput(ask.Args),
		},
		Options: []permissionOption{
			{OptionID: permAllowOnce, Name: "Allow once", Kind: permAllowOnce},
			{OptionID: permAllowAlways, Name: "Allow always", Kind: permAllowAlways},
			{OptionID: permRejectOnce, Name: "Reject once", Kind: permRejectOnce},
			{OptionID: permRejectAlways, Name: "Reject always", Kind: permRejectAlways},
		},
	}
}

// approvalFor maps a request_permission outcome to the three-way verdict
// run.Approve takes. A "selected" allow_once approves this call only;
// allow_always approves AND asks the harness to LEARN a per-session rule for the
// matching tool + exact pattern (issue #3); reject_* and a "cancelled" outcome
// (the editor aborted the turn) deny. An unknown/unselected outcome fails safe to
// deny (the zero value).
func approvalFor(outcome permissionOutcome) session.ApprovalVerdict {
	if outcome.Outcome != outcomeSelected {
		return session.VerdictDeny // cancelled (or unknown) -> deny
	}
	switch outcome.OptionID {
	case permAllowAlways:
		return session.VerdictAllowAlways
	case permAllowOnce:
		return session.VerdictAllowOnce
	default:
		return session.VerdictDeny
	}
}

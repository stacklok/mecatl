package acp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestProjectUpdateMessageDelta(t *testing.T) {
	got, ok := projectUpdate(session.Event{Type: session.EvMessageDelta, Text: "hi"})
	if !ok {
		t.Fatal("expected a projection")
	}
	cu, isChunk := got.(chunkUpdate)
	if !isChunk {
		t.Fatalf("want chunkUpdate, got %T", got)
	}
	if cu.SessionUpdate != updateAgentMessageChunk {
		t.Errorf("sessionUpdate = %q", cu.SessionUpdate)
	}
	if cu.Content.Type != "text" || cu.Content.Text != "hi" {
		t.Errorf("content = %+v", cu.Content)
	}
}

func TestProjectUpdateReasoningDelta(t *testing.T) {
	got, ok := projectUpdate(session.Event{Type: session.EvReasoningDelta, Text: "thinking"})
	if !ok {
		t.Fatal("expected a projection")
	}
	cu := got.(chunkUpdate)
	if cu.SessionUpdate != updateAgentThoughtChunk {
		t.Errorf("sessionUpdate = %q, want %q", cu.SessionUpdate, updateAgentThoughtChunk)
	}
	if cu.Content.Text != "thinking" {
		t.Errorf("text = %q", cu.Content.Text)
	}
}

func TestProjectUpdateToolCall(t *testing.T) {
	call := session.NewToolCall("call-1", "Read", json.RawMessage(`{"path":"/x"}`))
	got, ok := projectUpdate(session.Event{Type: session.EvToolCall, ToolCall: &call})
	if !ok {
		t.Fatal("expected a projection")
	}
	tc := got.(toolCallUpdate)
	if tc.SessionUpdate != updateToolCall {
		t.Errorf("sessionUpdate = %q", tc.SessionUpdate)
	}
	if tc.ToolCallID != "call-1" {
		t.Errorf("toolCallId = %q", tc.ToolCallID)
	}
	if tc.Title != "Read" || tc.Kind != "read" {
		t.Errorf("title/kind = %q/%q", tc.Title, tc.Kind)
	}
	if tc.Status != toolStatusPending {
		t.Errorf("status = %q, want pending", tc.Status)
	}
	if string(tc.RawInput) != `{"path":"/x"}` {
		t.Errorf("rawInput = %s", tc.RawInput)
	}
}

func TestProjectUpdateToolResult(t *testing.T) {
	tests := []struct {
		name       string
		result     session.ToolResult
		wantStatus string
	}{
		{"success", session.NewToolResult("call-2", "done"), toolStatusCompleted},
		{"error", session.NewToolError("call-3", "boom"), toolStatusFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := projectUpdate(session.Event{Type: session.EvToolResult, ToolResult: &tc.result})
			if !ok {
				t.Fatal("expected a projection")
			}
			u := got.(toolCallUpdate)
			if u.SessionUpdate != updateToolCallUpdate {
				t.Errorf("sessionUpdate = %q", u.SessionUpdate)
			}
			if u.ToolCallID != string(tc.result.CallID) {
				t.Errorf("toolCallId = %q", u.ToolCallID)
			}
			if u.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", u.Status, tc.wantStatus)
			}
			if len(u.Content) != 1 || u.Content[0].Type != "content" || u.Content[0].Content.Text != tc.result.Content {
				t.Errorf("content = %+v", u.Content)
			}
		})
	}
}

// TestProjectUpdateDropped asserts the events with no session/update projection
// this phase are dropped. subagent.start/team.start are dropped (the parent
// tool_call already names the work); subagent.tool/end and team.member/end DO
// project (see their dedicated tests below). EvHook with a nil payload is also
// dropped (a well-formed EvHook projects — see TestProjectHook).
func TestProjectUpdateDropped(t *testing.T) {
	dropped := []session.EventType{
		session.EvSessionInit,
		session.EvTurnStart,
		session.EvTurnEnd,
		session.EvCompaction,
		session.EvSubagentStart,
		session.EvTeamStart,
		session.EvResult,        // handled out of band (stopReason)
		session.EvPermissionAsk, // handled out of band (request_permission)
	}
	for _, et := range dropped {
		if _, ok := projectUpdate(session.Event{Type: et}); ok {
			t.Errorf("event %q should not project to a session/update", et)
		}
	}
	// An EvHook / EvSubagentTool / EvTeamMember with no payload (or no parent id)
	// has nothing to project and is dropped.
	if _, ok := projectUpdate(session.Event{Type: session.EvHook}); ok {
		t.Error("EvHook with nil payload should not project")
	}
	if _, ok := projectUpdate(session.Event{Type: session.EvSubagentTool, Subagent: &session.SubagentPayload{}}); ok {
		t.Error("subagent.tool with empty ParentCallID should not project")
	}
	if _, ok := projectUpdate(session.Event{Type: session.EvTeamMember, Team: &session.TeamPayload{}}); ok {
		t.Error("team.member with empty ParentCallID should not project")
	}
}

// TestProjectUpdateEditDiff asserts an Edit tool.call carries an ACP diff content
// block synthesized from the args (oldText=old_string, newText=new_string).
func TestProjectUpdateEditDiff(t *testing.T) {
	call := session.NewToolCall("c-edit", "Edit",
		json.RawMessage(`{"path":"main.go","old_string":"old","new_string":"new"}`))
	got, ok := projectUpdate(session.Event{Type: session.EvToolCall, ToolCall: &call})
	if !ok {
		t.Fatal("expected a projection")
	}
	tc := got.(toolCallUpdate)
	if tc.Kind != "edit" {
		t.Errorf("kind = %q, want edit", tc.Kind)
	}
	if len(tc.Content) != 1 || tc.Content[0].Type != "diff" {
		t.Fatalf("content = %+v, want one diff block", tc.Content)
	}
	d := tc.Content[0]
	if d.Path != "main.go" || d.OldText == nil || *d.OldText != "old" || d.NewText != "new" {
		t.Errorf("diff = path=%q oldText=%v newText=%q", d.Path, d.OldText, d.NewText)
	}
}

// TestProjectUpdateWriteDiff asserts a Write tool.call carries a diff block with
// newText=content and NO oldText (a new/overwritten file).
func TestProjectUpdateWriteDiff(t *testing.T) {
	call := session.NewToolCall("c-write", "Write",
		json.RawMessage(`{"path":"new.txt","content":"hello\n"}`))
	got, _ := projectUpdate(session.Event{Type: session.EvToolCall, ToolCall: &call})
	tc := got.(toolCallUpdate)
	if len(tc.Content) != 1 || tc.Content[0].Type != "diff" {
		t.Fatalf("content = %+v, want one diff block", tc.Content)
	}
	d := tc.Content[0]
	if d.Path != "new.txt" || d.NewText != "hello\n" {
		t.Errorf("diff = path=%q newText=%q", d.Path, d.NewText)
	}
	if d.OldText != nil {
		t.Errorf("oldText = %v, want nil (omitted) for a Write", *d.OldText)
	}
	// And it must marshal with no "oldText" key at all.
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "oldText") {
		t.Errorf("Write diff marshalled with oldText key: %s", b)
	}
}

// TestProjectUpdateEditMalformedArgsFallsBack asserts that an Edit with malformed
// args produces NO diff (nil content), so the loop falls back to text content.
func TestProjectUpdateEditMalformedArgsFallsBack(t *testing.T) {
	tests := []struct {
		name string
		args string
	}{
		{"not json", `{not json`},
		{"missing path", `{"old_string":"a","new_string":"b"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			call := session.NewToolCall("c", "Edit", json.RawMessage(tc.args))
			got, ok := projectUpdate(session.Event{Type: session.EvToolCall, ToolCall: &call})
			if !ok {
				t.Fatal("expected a projection (tool_call) even with bad args")
			}
			if u := got.(toolCallUpdate); u.Content != nil {
				t.Errorf("expected nil content (text fallback), got %+v", u.Content)
			}
		})
	}
}

// TestProjectUpdateNonEditNoDiff asserts a read/execute tool call carries no diff.
func TestProjectUpdateNonEditNoDiff(t *testing.T) {
	call := session.NewToolCall("c", "Bash", json.RawMessage(`{"cmd":"ls"}`))
	got, _ := projectUpdate(session.Event{Type: session.EvToolCall, ToolCall: &call})
	if u := got.(toolCallUpdate); u.Content != nil {
		t.Errorf("Bash call should have no diff content, got %+v", u.Content)
	}
}

// TestProjectHook asserts a hook event projects to an agent_thought_chunk carrying
// the reason (a blocked hook with no addressable tool-call id cannot key a
// tool_call_update — see projectHook).
func TestProjectHook(t *testing.T) {
	ev := session.Event{
		Type: session.EvHook,
		Text: "blocked by PreToolUse hook",
		Hook: &session.HookPayload{Phase: "PreToolUse", Tool: "Bash", Decision: session.HookBlocked},
	}
	got, ok := projectUpdate(ev)
	if !ok {
		t.Fatal("expected a projection")
	}
	cu, isChunk := got.(chunkUpdate)
	if !isChunk {
		t.Fatalf("want chunkUpdate, got %T", got)
	}
	if cu.SessionUpdate != updateAgentThoughtChunk {
		t.Errorf("sessionUpdate = %q, want thought chunk", cu.SessionUpdate)
	}
	if cu.Content.Text != "blocked by PreToolUse hook" {
		t.Errorf("text = %q", cu.Content.Text)
	}
}

// TestProjectHookNoText asserts a hook with no Text falls back to a phase/decision
// summary line.
func TestProjectHookNoText(t *testing.T) {
	got, _ := projectUpdate(session.Event{
		Type: session.EvHook,
		Hook: &session.HookPayload{Phase: "PreToolUse", Tool: "Bash", Decision: session.HookBlocked},
	})
	cu := got.(chunkUpdate)
	if cu.Content.Text != "PreToolUse Bash: blocked" {
		t.Errorf("summary = %q", cu.Content.Text)
	}
}

// TestProjectHookBlockedWithCallID asserts a blocked hook carrying the originating
// tool-call id projects to a FAILED tool_call_update keyed by that id (the veto
// lands ON the tool card the editor already opened), not a thought chunk.
func TestProjectHookBlockedWithCallID(t *testing.T) {
	ev := session.Event{
		Type: session.EvHook,
		Text: "blocked by PreToolUse hook",
		Hook: &session.HookPayload{Phase: "PreToolUse", Tool: "Bash", Decision: session.HookBlocked, CallID: "call-7"},
	}
	got, ok := projectUpdate(ev)
	if !ok {
		t.Fatal("expected a projection")
	}
	u, isUpdate := got.(toolCallUpdate)
	if !isUpdate {
		t.Fatalf("want toolCallUpdate, got %T", got)
	}
	if u.SessionUpdate != updateToolCallUpdate {
		t.Errorf("sessionUpdate = %q, want tool_call_update", u.SessionUpdate)
	}
	if u.ToolCallID != "call-7" {
		t.Errorf("toolCallId = %q, want call-7", u.ToolCallID)
	}
	if u.Status != toolStatusFailed {
		t.Errorf("status = %q, want failed", u.Status)
	}
	if len(u.Content) != 1 || u.Content[0].Content.Text != "blocked by PreToolUse hook" {
		t.Errorf("content = %+v, want the veto reason", u.Content)
	}
}

// TestProjectHookModifiedWithCallID asserts a NON-blocked hook (a modified/info
// notice) still projects to a thought chunk even when it carries a call id — only a
// BLOCKED hook fails the card.
func TestProjectHookModifiedWithCallID(t *testing.T) {
	ev := session.Event{
		Type: session.EvHook,
		Text: "PreToolUse hook rewrote tool arguments for Bash",
		Hook: &session.HookPayload{Phase: "PreToolUse", Tool: "Bash", Decision: session.HookModified, CallID: "call-7"},
	}
	got, ok := projectUpdate(ev)
	if !ok {
		t.Fatal("expected a projection")
	}
	cu, isChunk := got.(chunkUpdate)
	if !isChunk {
		t.Fatalf("want chunkUpdate (a modified hook must not fail the card), got %T", got)
	}
	if cu.SessionUpdate != updateAgentThoughtChunk {
		t.Errorf("sessionUpdate = %q, want thought chunk", cu.SessionUpdate)
	}
}

// TestProjectHookAdvisoryWithCallID asserts an advisory guardrail finding (a
// non-blocked, non-modified hook) projects to a thought chunk — it is client-visible
// but model-invisible, so it must NOT fail the originating tool card.
func TestProjectHookAdvisoryWithCallID(t *testing.T) {
	ev := session.Event{
		Type: session.EvHook,
		Text: "guardrail advisory: borderline content",
		Hook: &session.HookPayload{Phase: "PostToolUse", Tool: "WebFetch", Decision: session.HookAdvisory, CallID: "call-9"},
	}
	got, ok := projectUpdate(ev)
	if !ok {
		t.Fatal("expected a projection")
	}
	cu, isChunk := got.(chunkUpdate)
	if !isChunk {
		t.Fatalf("want chunkUpdate (an advisory hook must not fail the card), got %T", got)
	}
	if cu.SessionUpdate != updateAgentThoughtChunk {
		t.Errorf("sessionUpdate = %q, want thought chunk", cu.SessionUpdate)
	}
}

// TestProjectHookPostToolUseBlockedWithCallID asserts a blocked PostToolUse hook —
// even carrying a call id — projects to a thought chunk, NOT a failed
// tool_call_update. PostToolUse is annotate-only (the tool already ran and its
// successful EvToolResult settles the card); failing the card would overwrite that.
func TestProjectHookPostToolUseBlockedWithCallID(t *testing.T) {
	ev := session.Event{
		Type: session.EvHook,
		Text: "PostToolUse flagged the output",
		Hook: &session.HookPayload{Phase: "PostToolUse", Tool: "Bash", Decision: session.HookBlocked, CallID: "call-7"},
	}
	got, ok := projectUpdate(ev)
	if !ok {
		t.Fatal("expected a projection")
	}
	cu, isChunk := got.(chunkUpdate)
	if !isChunk {
		t.Fatalf("want chunkUpdate (a PostToolUse block is annotate-only), got %T", got)
	}
	if cu.SessionUpdate != updateAgentThoughtChunk {
		t.Errorf("sessionUpdate = %q, want thought chunk", cu.SessionUpdate)
	}
}

// TestProjectSubagent asserts subagent.tool/end project as tool_call_update on the
// PARENT Subagent call id.
func TestProjectSubagent(t *testing.T) {
	toolEv := session.Event{
		Type:     session.EvSubagentTool,
		Subagent: &session.SubagentPayload{ParentCallID: "task-1", ChildID: "child-9", ToolName: "Read", ToolCount: 2},
	}
	got, ok := projectUpdate(toolEv)
	if !ok {
		t.Fatal("expected a projection")
	}
	u := got.(toolCallUpdate)
	if u.SessionUpdate != updateToolCallUpdate || u.ToolCallID != "task-1" {
		t.Errorf("update = %+v, want tool_call_update on task-1", u)
	}
	if u.Status != toolStatusInProgress {
		t.Errorf("status = %q, want in_progress", u.Status)
	}
	if len(u.Content) != 1 || !strings.Contains(u.Content[0].Content.Text, "Read") {
		t.Errorf("content = %+v, want a line mentioning Read", u.Content)
	}

	endEv := session.Event{
		Type:     session.EvSubagentEnd,
		Subagent: &session.SubagentPayload{ParentCallID: "task-1", ToolCount: 3, Stop: session.StopEndTurn},
	}
	endGot, _ := projectUpdate(endEv)
	eu := endGot.(toolCallUpdate)
	if eu.ToolCallID != "task-1" || eu.Status != toolStatusInProgress {
		t.Errorf("end update = %+v", eu)
	}
}

// TestProjectSubagentEndError asserts a subagent that ended in error marks the
// parent Subagent call failed.
func TestProjectSubagentEndError(t *testing.T) {
	got, _ := projectUpdate(session.Event{
		Type:     session.EvSubagentEnd,
		Subagent: &session.SubagentPayload{ParentCallID: "task-2", Stop: session.StopError},
	})
	if u := got.(toolCallUpdate); u.Status != toolStatusFailed {
		t.Errorf("status = %q, want failed", u.Status)
	}
}

// TestProjectSubagentEndCarriesCause asserts the ACP projection surfaces WHY a delegation
// failed, not just that it did (issue #319). An ACP client is a developer-facing surface
// (an editor / agent client), and it read the same session.SubagentPayload the mecatui
// fleet pane reads — so dropping the cause here left the two projections of ONE event
// disagreeing about how much of the field's contract they surface. The cause is
// harness/provider metadata (never child-authored output), so gauntlet #7 holds.
//
// The value arrives already clamped AND whitespace-collapsed: session.SubagentPayload.Cause
// is LINE-ORIENTED by contract and every emit site normalises it through one helper
// (agent.subagentCausePayload), which is what lets a consumer render it as-is. The
// collapse itself is pinned at that emit site, through the real loop, by
// engine/agent's TestSubagentEndEventCarriesClampedCause — so the fixture here is what the
// engine actually emits. What this asserts is the projector's own half: it appends the
// cause to the status line and adds no newline of its own.
//
// The negative half matters just as much: a benign terminal carries no cause, and the line
// must then read exactly as it did before.
func TestProjectSubagentEndCarriesCause(t *testing.T) {
	got, _ := projectUpdate(session.Event{
		Type: session.EvSubagentEnd,
		Subagent: &session.SubagentPayload{
			ParentCallID: "task-3", ToolCount: 4, Stop: session.StopError,
			Cause: "upstream 503: model overloaded",
		},
	})
	u := got.(toolCallUpdate)
	if len(u.Content) != 1 {
		t.Fatalf("want one content line, got %+v", u.Content)
	}
	line := u.Content[0].Content.Text
	if !strings.Contains(line, "upstream 503: model overloaded") {
		t.Errorf("the failed-delegation line must carry the cause, got %q", line)
	}
	if strings.Contains(line, "\n") {
		t.Errorf("the projection must stay ONE line for a line-oriented surface, got %q", line)
	}

	// Benign terminal: no cause, so the line is unchanged from the pre-#319 shape.
	benign, _ := projectUpdate(session.Event{
		Type:     session.EvSubagentEnd,
		Subagent: &session.SubagentPayload{ParentCallID: "task-4", ToolCount: 2, Stop: session.StopEndTurn},
	})
	bl := benign.(toolCallUpdate).Content[0].Content.Text
	if bl != "subagent finished: 2 tool call(s), end_turn" {
		t.Errorf("a clean terminal's line must be unchanged, got %q", bl)
	}
}

// TestProjectTeam asserts team.member/end project as tool_call_update on the
// PARENT Team call id.
func TestProjectTeam(t *testing.T) {
	memberEv := session.Event{
		Type: session.EvTeamMember,
		Team: &session.TeamPayload{
			ParentCallID: "team-1", TeamID: "t9", Member: "alice",
			InnerKind: session.EvToolCall, ToolName: "Grep", Detail: "{q}",
		},
	}
	got, ok := projectUpdate(memberEv)
	if !ok {
		t.Fatal("expected a projection")
	}
	u := got.(toolCallUpdate)
	if u.ToolCallID != "team-1" || u.Status != toolStatusInProgress {
		t.Errorf("update = %+v", u)
	}
	if len(u.Content) != 1 || !strings.Contains(u.Content[0].Content.Text, "alice") {
		t.Errorf("content = %+v, want a line mentioning alice", u.Content)
	}

	// A message.delta member event surfaces the member's text.
	msgEv := session.Event{
		Type: session.EvTeamMember,
		Team: &session.TeamPayload{ParentCallID: "team-1", Member: "bob", InnerKind: session.EvMessageDelta, Text: "hi"},
	}
	mg, _ := projectUpdate(msgEv)
	if mu := mg.(toolCallUpdate); !strings.Contains(mu.Content[0].Content.Text, "hi") {
		t.Errorf("message member line = %+v", mu.Content)
	}

	// An empty-text non-tool member event is dropped (nothing to show).
	if _, ok := projectUpdate(session.Event{
		Type: session.EvTeamMember,
		Team: &session.TeamPayload{ParentCallID: "team-1", Member: "bob", InnerKind: session.EvTurnEnd},
	}); ok {
		t.Error("empty member event should not project")
	}

	endGot, _ := projectUpdate(session.Event{
		Type: session.EvTeamEnd,
		Team: &session.TeamPayload{ParentCallID: "team-1", Rounds: 2, Stop: session.StopEndTurn},
	})
	if eu := endGot.(toolCallUpdate); eu.ToolCallID != "team-1" {
		t.Errorf("team end update = %+v", eu)
	}
}

// TestProjectTeamMemberResultCarriesCause asserts the ACP projection surfaces WHY a
// member's round failed (issue #331) — the Team mirror of TestProjectSubagentEndCarriesCause.
// A team.member result event with a Cause leads with "<member> failed: <cause>"; a clean
// result with no cause and no text is dropped as before.
func TestProjectTeamMemberResultCarriesCause(t *testing.T) {
	got, _ := projectUpdate(session.Event{
		Type: session.EvTeamMember,
		Team: &session.TeamPayload{
			ParentCallID: "team-1", TeamID: "t9", Member: "worker",
			InnerKind: session.EvResult, Stop: session.StopError,
			Cause: "upstream 503: model overloaded",
			Text:  "partial",
		},
	})
	u := got.(toolCallUpdate)
	if len(u.Content) != 1 {
		t.Fatalf("want one content line, got %+v", u.Content)
	}
	line := u.Content[0].Content.Text
	if want := "worker failed: upstream 503: model overloaded"; line != want {
		t.Errorf("the failed-round line must lead with the cause, got %q, want %q", line, want)
	}
	if strings.Contains(line, "\n") {
		t.Errorf("the projection must stay ONE line for a line-oriented surface, got %q", line)
	}

	// Benign empty result: no cause, no text → dropped (nothing to show).
	if _, ok := projectUpdate(session.Event{
		Type: session.EvTeamMember,
		Team: &session.TeamPayload{ParentCallID: "team-1", Member: "worker", InnerKind: session.EvResult, Stop: session.StopEndTurn},
	}); ok {
		t.Error("a clean empty result must not project (no cause, no text)")
	}
}

func TestStopReasonFor(t *testing.T) {
	tests := []struct {
		in   session.StopReason
		want string
	}{
		{session.StopEndTurn, stopEndTurn},
		{session.StopCancelled, stopCancelled},
		{session.StopMaxTurns, stopMaxTurnRequests},
		{session.StopMaxToolCalls, stopMaxTurnRequests},
		{session.StopMaxConsecutiveFailures, stopMaxTurnRequests},
		{session.StopError, stopEndTurn},
	}
	for _, tc := range tests {
		if got := stopReasonFor(tc.in); got != tc.want {
			t.Errorf("stopReasonFor(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestApprovalFor(t *testing.T) {
	tests := []struct {
		outcome permissionOutcome
		want    session.ApprovalVerdict
	}{
		{permissionOutcome{Outcome: outcomeSelected, OptionID: permAllowOnce}, session.VerdictAllowOnce},
		{permissionOutcome{Outcome: outcomeSelected, OptionID: permAllowAlways}, session.VerdictAllowAlways},
		{permissionOutcome{Outcome: outcomeSelected, OptionID: permRejectOnce}, session.VerdictDeny},
		{permissionOutcome{Outcome: outcomeSelected, OptionID: permRejectAlways}, session.VerdictDeny},
		{permissionOutcome{Outcome: outcomeCancelled}, session.VerdictDeny},
		{permissionOutcome{Outcome: "weird"}, session.VerdictDeny},
	}
	for _, tc := range tests {
		if got := approvalFor(tc.outcome); got != tc.want {
			t.Errorf("approvalFor(%+v) = %v, want %v", tc.outcome, got, tc.want)
		}
	}
}

func TestPermissionRequestFor(t *testing.T) {
	ask := session.PendingAsk{AskID: "ask-1", Tool: "Bash", Args: json.RawMessage(`{"cmd":"ls"}`), Reason: "mutating"}
	req := permissionRequestFor("sess-1", ask)
	if req.SessionID != "sess-1" {
		t.Errorf("sessionId = %q", req.SessionID)
	}
	if req.ToolCall.ToolCallID != "ask-1" || req.ToolCall.Title != "Bash" || req.ToolCall.Kind != "execute" {
		t.Errorf("toolCall = %+v", req.ToolCall)
	}
	if len(req.Options) != 4 {
		t.Fatalf("want 4 options, got %d", len(req.Options))
	}
	wantKinds := []string{permAllowOnce, permAllowAlways, permRejectOnce, permRejectAlways}
	for i, o := range req.Options {
		if o.Kind != wantKinds[i] || o.OptionID != wantKinds[i] {
			t.Errorf("option %d = %+v", i, o)
		}
	}
}

// TestChunkUpdateMarshalShape verifies the on-wire JSON of a chunk update matches
// the ACP SessionUpdate shape (discriminator "sessionUpdate", a single nested
// "content" ContentBlock).
func TestChunkUpdateMarshalShape(t *testing.T) {
	b, _ := json.Marshal(chunkUpdate{SessionUpdate: updateAgentMessageChunk, Content: textBlock("hi")})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["sessionUpdate"] != "agent_message_chunk" {
		t.Errorf("sessionUpdate = %v", m["sessionUpdate"])
	}
	content, ok := m["content"].(map[string]any)
	if !ok || content["type"] != "text" || content["text"] != "hi" {
		t.Errorf("content = %v", m["content"])
	}
}

func TestProjectUpdateAuthorizationIsSafeStatusOnly(t *testing.T) {
	pending := session.AuthorizationPayload{
		AuthorizationID: "auth-1",
		DisplayName:     "GitHub Enterprise",
		Call:            "call-1",
		ExpiresAt:       time.Unix(1, 0),
		Status:          session.AuthorizationPending,
	}
	got, ok := projectUpdate(session.Event{Type: session.EvAuthorizationRequired, Authorization: &pending})
	if !ok {
		t.Fatal("required authorization was not projected")
	}
	card := got.(toolCallUpdate)
	if card.ToolCallID != "call-1" || card.Status != toolStatusFailed || len(card.Content) != 1 {
		t.Fatalf("required projection = %+v", card)
	}
	if card.Content[0].Content.Text != "MCP authorization for GitHub Enterprise is unavailable for ACP sessions" {
		t.Fatalf("required projection text = %q", card.Content[0].Content.Text)
	}
	if strings.Contains(card.Content[0].Content.Text, "auth-1") {
		t.Fatalf("required projection exposed authorization correlation: %+v", card)
	}

	resolved := pending
	resolved.Status = session.AuthorizationGranted
	got, ok = projectUpdate(session.Event{Type: session.EvAuthorizationResolved, Authorization: &resolved})
	if !ok {
		t.Fatal("resolved authorization was not projected")
	}
	note := got.(chunkUpdate)
	if note.Content.Text != "MCP authorization for GitHub Enterprise status: granted" {
		t.Fatalf("resolved projection = %+v", note)
	}

	unsafe := pending
	unsafe.DisplayName = "GitHub\nforged"
	got, ok = projectUpdate(session.Event{Type: session.EvAuthorizationRequired, Authorization: &unsafe})
	if !ok {
		t.Fatal("authorization with unsafe display text was not projected")
	}
	if text := got.(toolCallUpdate).Content[0].Content.Text; strings.Contains(text, "GitHub") {
		t.Fatalf("unsafe display text was projected: %q", text)
	}
}

package client

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// TestEventToMsg covers the mapper over every documented event type, asserting
// both the msg variant and a representative carried field. This is the single
// translation point between proto and the ui model, so it gets exhaustive
// coverage.
func TestEventToMsg(t *testing.T) {
	cases := []struct {
		name string
		ev   *mecatlv1.Event
		want tea.Msg
	}{
		{"nil", nil, nil},
		{"unknown", &mecatlv1.Event{Type: "future.kind"}, nil},
		{"session.init", &mecatlv1.Event{Type: "session.init", Seq: 7}, SessionInitMsg{Seq: 7}},
		{"turn.start", &mecatlv1.Event{Type: "turn.start", Turn: 2}, TurnStartMsg{Turn: 2}},
		{
			"turn.end",
			&mecatlv1.Event{Type: "turn.end", Turn: 2, TurnEnd: &mecatlv1.TurnEnd{
				DurationMs: 4100, Usage: &mecatlv1.Usage{InputTokens: 1200, OutputTokens: 340}}},
			TurnEndMsg{Turn: 2, Usage: Usage{InputTokens: 1200, OutputTokens: 340}, DurationMs: 4100},
		},
		{"message.delta", &mecatlv1.Event{Type: "message.delta", Turn: 2, Text: "hi"}, AssistantDeltaMsg{Turn: 2, Text: "hi"}},
		{"reasoning.delta", &mecatlv1.Event{Type: "reasoning.delta", Turn: 2, Text: "pondering"}, ReasoningDeltaMsg{Turn: 2, Text: "pondering"}},
		{
			"tool.call",
			&mecatlv1.Event{Type: "tool.call", ToolCall: &mecatlv1.ToolCall{Id: "c1", Name: "Read", Args: `{"path":"x"}`}},
			ToolCallMsg{ID: "c1", Name: "Read", Args: `{"path":"x"}`},
		},
		{
			"tool.result",
			&mecatlv1.Event{Type: "tool.result", ToolResult: &mecatlv1.ToolResult{CallId: "c1", Content: "ok", IsError: true}},
			ToolResultMsg{CallID: "c1", Content: "ok", IsError: true},
		},
		{
			"tool.result blocks",
			&mecatlv1.Event{Type: "tool.result", ToolResult: &mecatlv1.ToolResult{
				CallId: "c2", Content: "Created issue #24",
				Blocks: []*mecatlv1.ContentBlock{
					{Kind: mecatlv1.ContentBlock_KIND_RESOURCE_LINK, Name: "issue-24", Url: "https://github.com/stacklok/mecatl/issues/24"},
					{Kind: mecatlv1.ContentBlock_KIND_IMAGE, MimeType: "image/png"},
				},
				StructuredContent: `{"id":24}`,
			}},
			ToolResultMsg{
				CallID:  "c2",
				Content: "Created issue #24",
				Blocks: []ContentBlock{
					{Kind: ContentBlockResourceLink, Name: "issue-24", URL: "https://github.com/stacklok/mecatl/issues/24"},
					{Kind: ContentBlockImage, MimeType: "image/png"},
				},
				StructuredContent: `{"id":24}`,
			},
		},
		{
			"tool.progress",
			&mecatlv1.Event{Type: "tool.progress", Text: "scanned 64/512 files"},
			ToolProgressMsg{Text: "scanned 64/512 files"},
		},
		{
			"authorization.required",
			&mecatlv1.Event{Type: "authorization.required", Authorization: &mecatlv1.Authorization{AuthorizationId: "authorization-1", DisplayName: "GitHub Enterprise", CallId: "call-1", Status: "pending"}},
			MCPAuthorizationMsg{AuthorizationID: "authorization-1", DisplayName: "GitHub Enterprise", CallID: "call-1", Status: "pending"},
		},
		{
			"permission.ask",
			&mecatlv1.Event{Type: "permission.ask", Ask: &mecatlv1.PermissionAsk{AskId: "a1", Tool: "Write", Args: "{}", Reason: "why"}},
			PermissionAskMsg{AskID: "a1", Tool: "Write", Args: "{}", Reason: "why"},
		},
		{
			"permission.retract",
			&mecatlv1.Event{Type: "permission.retract", Ask: &mecatlv1.PermissionAsk{AskId: "a1"}},
			PermissionRetractMsg{AskID: "a1"},
		},
		{
			"hook blocked",
			&mecatlv1.Event{Type: "hook", Text: "blocked by policy", Hook: &mecatlv1.Hook{
				Phase: "PreToolUse", Tool: "Shell", Decision: mecatlv1.HookDecision_HOOK_DECISION_BLOCKED}},
			HookMsg{Text: "blocked by policy", Phase: "PreToolUse", Tool: "Shell", Decision: HookBlocked},
		},
		{
			"hook nil payload defaults to info",
			&mecatlv1.Event{Type: "hook", Text: "ran hook"},
			HookMsg{Text: "ran hook", Decision: HookInfo},
		},
		{
			"subagent.start routed",
			&mecatlv1.Event{Type: "subagent.start", Subagent: &mecatlv1.Subagent{
				ParentCallId: "p1", ChildId: "subagent-p1", Goal: "investigate main.go", Model: "openai/gpt-4.5",
				RoutedCategory: "large", RoutedModel: "openai/gpt-4.5"}},
			SubagentMsg{Kind: SubagentStart, ParentCallID: "p1", ChildID: "subagent-p1", Goal: "investigate main.go", Model: "openai/gpt-4.5",
				RoutedCategory: "large", RoutedModel: "openai/gpt-4.5"},
		},
		{
			"subagent.start routing miss reason",
			&mecatlv1.Event{Type: "subagent.start", Subagent: &mecatlv1.Subagent{
				ParentCallId: "p1r", ChildId: "subagent-p1r", Goal: "inherits default", Model: "openai/gpt-4.5", RoutingReason: "router-disabled"}},
			SubagentMsg{Kind: SubagentStart, ParentCallID: "p1r", ChildID: "subagent-p1r", Goal: "inherits default", Model: "openai/gpt-4.5", RoutingReason: "router-disabled"},
		},
		{
			"subagent.start background",
			&mecatlv1.Event{Type: "subagent.start", Subagent: &mecatlv1.Subagent{
				ParentCallId: "p2", ChildId: "subagent-p2", Goal: "long audit", Background: true}},
			SubagentMsg{Kind: SubagentStart, ParentCallID: "p2", ChildID: "subagent-p2", Goal: "long audit", Background: true},
		},
		{
			"subagent.tool",
			&mecatlv1.Event{Type: "subagent.tool", Subagent: &mecatlv1.Subagent{
				ParentCallId: "p1", ChildId: "subagent-p1", ToolName: "Grep", IsError: true, ToolCount: 3}},
			SubagentMsg{Kind: SubagentTool, ParentCallID: "p1", ChildID: "subagent-p1", ToolName: "Grep", IsError: true, ToolCount: 3},
		},
		{
			"subagent.end",
			&mecatlv1.Event{Type: "subagent.end", Subagent: &mecatlv1.Subagent{
				ParentCallId: "p1", ChildId: "subagent-p1", ToolCount: 5, Stop: "max_tool_calls", DurationMs: 1234,
				Usage: &mecatlv1.Usage{InputTokens: 90, OutputTokens: 12}}},
			SubagentMsg{Kind: SubagentEnd, ParentCallID: "p1", ChildID: "subagent-p1", ToolCount: 5,
				Stop: "max_tool_calls", DurationMs: 1234, Usage: Usage{InputTokens: 90, OutputTokens: 12}},
		},
		{
			// Issue #319: a failed child's cause must survive the proto→msg relay, or the
			// fleet pane can only ever show "stop:error" with no WHY.
			"subagent.end carries the failure cause",
			&mecatlv1.Event{Type: "subagent.end", Subagent: &mecatlv1.Subagent{
				ParentCallId: "p1", ChildId: "subagent-p1", Stop: "error",
				Cause: "upstream 503: model overloaded"}},
			SubagentMsg{Kind: SubagentEnd, ParentCallID: "p1", ChildID: "subagent-p1",
				Stop: "error", Cause: "upstream 503: model overloaded"},
		},
		{
			"parallel.start",
			&mecatlv1.Event{Type: "parallel.start", Parallel: &mecatlv1.Parallel{
				ParentCallId: "p1", Join: "judge", BranchCount: 3}},
			ParallelMsg{Kind: ParallelStart, ParentCallID: "p1", Join: "judge", BranchCount: 3},
		},
		{
			"parallel.branch branch_start",
			&mecatlv1.Event{Type: "parallel.branch", Parallel: &mecatlv1.Parallel{
				ParentCallId: "p1", Kind: "branch_start", BranchIndex: 1, ChildId: "parallel-p1-1", BranchLabel: "branch-2", Goal: "explore beta", Model: "anthropic/claude-3.5"}},
			ParallelMsg{Kind: ParallelBranchStart, ParentCallID: "p1", BranchIndex: 1, ChildID: "parallel-p1-1", BranchLabel: "branch-2", Goal: "explore beta", Model: "anthropic/claude-3.5"},
		},
		{
			"parallel.branch branch_start routing miss reason",
			&mecatlv1.Event{Type: "parallel.branch", Parallel: &mecatlv1.Parallel{
				ParentCallId: "p1r", Kind: "branch_start", BranchIndex: 0, ChildId: "parallel-p1r-0", BranchLabel: "branch-1", Goal: "explore alpha", Model: "anthropic/claude-3.5", RoutingReason: "breaker-open"}},
			ParallelMsg{Kind: ParallelBranchStart, ParentCallID: "p1r", BranchIndex: 0, ChildID: "parallel-p1r-0", BranchLabel: "branch-1", Goal: "explore alpha", Model: "anthropic/claude-3.5", RoutingReason: "breaker-open"},
		},
		{
			"parallel.branch branch_tool",
			&mecatlv1.Event{Type: "parallel.branch", Parallel: &mecatlv1.Parallel{
				ParentCallId: "p1", Kind: "branch_tool", BranchIndex: 0, ToolName: "Grep", IsError: true, ToolCount: 2}},
			ParallelMsg{Kind: ParallelBranchTool, ParentCallID: "p1", BranchIndex: 0, ToolName: "Grep", IsError: true, ToolCount: 2},
		},
		{
			"parallel.branch branch_end",
			&mecatlv1.Event{Type: "parallel.branch", Parallel: &mecatlv1.Parallel{
				ParentCallId: "p1", Kind: "branch_end", BranchIndex: 2, ChildId: "parallel-p1-2", ToolCount: 4, Failed: true,
				Stop: "error", DurationMs: 555,
				Usage: &mecatlv1.Usage{InputTokens: 12, OutputTokens: 3}}},
			ParallelMsg{Kind: ParallelBranchEnd, ParentCallID: "p1", BranchIndex: 2, ChildID: "parallel-p1-2", ToolCount: 4, Failed: true,
				Stop: "error", DurationMs: 555,
				Usage: Usage{InputTokens: 12, OutputTokens: 3}},
		},
		{
			"parallel.end winner",
			&mecatlv1.Event{Type: "parallel.end", Parallel: &mecatlv1.Parallel{
				ParentCallId: "p1", Join: "judge", BranchCount: 3, Winner: 1,
				Stop: "end_turn", Usage: &mecatlv1.Usage{InputTokens: 100, OutputTokens: 20}}},
			ParallelMsg{Kind: ParallelEnd, ParentCallID: "p1", Join: "judge", BranchCount: 3, Winner: 1,
				Stop: "end_turn", Usage: Usage{InputTokens: 100, OutputTokens: 20}},
		},
		{
			"parallel.end join=all no winner",
			&mecatlv1.Event{Type: "parallel.end", Parallel: &mecatlv1.Parallel{
				ParentCallId: "p1", Join: "all", BranchCount: 2, Winner: -1}},
			ParallelMsg{Kind: ParallelEnd, ParentCallID: "p1", Join: "all", BranchCount: 2, Winner: -1},
		},
		{"compaction", &mecatlv1.Event{Type: "compaction", Text: "compacted"}, CompactionMsg{Text: "compacted"}},
		{"no_progress", &mecatlv1.Event{Type: "no_progress", Text: "nudging to continue"}, NoProgressMsg{Text: "nudging to continue"}},
		{"provider.route", &mecatlv1.Event{Type: "provider.route", Text: "anthropic"}, ProviderRouteMsg{Text: "anthropic"}},
		{"recover_notice", &mecatlv1.Event{Type: "recover_notice", Text: "permanent failure advisory"}, RecoverNoticeMsg{Text: "permanent failure advisory"}},
		{
			"result",
			&mecatlv1.Event{Type: "result", Result: &mecatlv1.Result{
				Stop: "end_turn", Text: "done", Usage: &mecatlv1.Usage{InputTokens: 10, OutputTokens: 5},
			}},
			ResultMsg{Stop: "end_turn", Text: "done", Usage: Usage{InputTokens: 10, OutputTokens: 5}},
		},
		{
			"result permanent error",
			&mecatlv1.Event{Type: "result", Result: &mecatlv1.Result{
				Stop: "error", Error: "invalid_encrypted_content: the blob is malformed", Permanent: true,
				Usage: &mecatlv1.Usage{InputTokens: 20, OutputTokens: 2},
			}},
			ResultMsg{Stop: "error", Error: "invalid_encrypted_content: the blob is malformed",
				Usage: Usage{InputTokens: 20, OutputTokens: 2}, Permanent: true, Transient: false},
		},
		{
			// The three log-only kinds are relayed ONLY by the replay (the live
			// Converse relay skips them). EventToMsg must still map them so the
			// shared readEventLoop path projects them for a transcript viewer.
			"approval",
			&mecatlv1.Event{Type: "approval", Approval: &mecatlv1.Approval{
				AskId: "a1", Verdict: "allow_always", Tool: "Write", CallId: "c1", AllowAlways: true,
			}},
			ApprovalMsg{AskID: "a1", Verdict: "allow_always", Tool: "Write", CallID: "c1", AllowAlways: true},
		},
		{
			"user_prompt with parts",
			&mecatlv1.Event{Type: "user_prompt", UserPrompt: &mecatlv1.UserPrompt{
				Text: "look at this",
				Parts: []*mecatlv1.Content{
					{Kind: mecatlv1.Content_KIND_IMAGE, MimeType: "image/png", Data: []byte{0x89}},
				},
			}},
			UserPromptMsg{Text: "look at this", Parts: []ContentBlock{
				{Kind: ContentBlockImage, MimeType: "image/png", Data: []byte{0x89}},
			}},
		},
		{
			"compaction_archive with replaced",
			&mecatlv1.Event{Type: "compaction.archive", CompactionArchive: &mecatlv1.CompactionArchive{
				Replaced: []*mecatlv1.ConversationMessage{
					{Role: "user", Text: "old task"},
				},
			}},
			CompactionArchiveMsg{Replaced: []ConversationMessage{
				{Role: "user", Text: "old task"},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EventToMsg(tc.ev)
			// reflect.DeepEqual, not !=: ToolResultMsg carries a []ContentBlock slice
			// (the typed tool-result blocks), which is not comparable with ==.
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("EventToMsg(%s) = %#v, want %#v", tc.name, got, tc.want)
			}
		})
	}
}

// TestEventToMsgTeam covers the three team.* mappers separately because TeamMsg
// carries a roster slice and so is not comparable with == (the table test uses
// !=). It asserts each kind's discriminant, attribution ids, and carried fields —
// including the roster translation to plain TeamMemberSpec values (lead/mutating).
func TestEventToMsgTeam(t *testing.T) {
	cases := []struct {
		name string
		ev   *mecatlv1.Event
		want TeamMsg
	}{
		{
			"team.start",
			&mecatlv1.Event{Type: "team.start", Team: &mecatlv1.Team{
				ParentCallId: "t1", TeamId: "team-t1", Roster: []*mecatlv1.TeamMemberSpec{
					{Name: "lead", Role: "coordinator", Lead: true, Mutating: true, Model: "openai/gpt-4.5"},
					{Name: "scout", Role: "researcher", Model: "anthropic/claude-3.5"},
				}}},
			TeamMsg{Kind: TeamStart, ParentCallID: "t1", TeamID: "team-t1", Roster: []TeamMemberSpec{
				{Name: "lead", Role: "coordinator", Lead: true, Mutating: true, Model: "openai/gpt-4.5"},
				{Name: "scout", Role: "researcher", Model: "anthropic/claude-3.5"},
			}},
		},
		{
			"team.start roster routing miss reason",
			&mecatlv1.Event{Type: "team.start", Team: &mecatlv1.Team{
				ParentCallId: "t1r", TeamId: "team-t1r", Roster: []*mecatlv1.TeamMemberSpec{
					{Name: "lead", Role: "coordinator", Lead: true, Mutating: true, Model: "openai/gpt-4.5", RoutingReason: "agent-def-pinned-model"},
					{Name: "scout", Role: "researcher", Model: "anthropic/claude-3.5"},
				}}},
			TeamMsg{Kind: TeamStart, ParentCallID: "t1r", TeamID: "team-t1r", Roster: []TeamMemberSpec{
				{Name: "lead", Role: "coordinator", Lead: true, Mutating: true, Model: "openai/gpt-4.5", RoutingReason: "agent-def-pinned-model"},
				{Name: "scout", Role: "researcher", Model: "anthropic/claude-3.5"},
			}},
		},
		{
			"team.member",
			&mecatlv1.Event{Type: "team.member", Team: &mecatlv1.Team{
				ParentCallId: "t1", TeamId: "team-t1", Member: "scout",
				MemberSessionId: "team-team-t1-scout", InnerKind: "turn.end",
				Usage:       &mecatlv1.Usage{InputTokens: 40000, OutputTokens: 80},
				ContextUsed: 40000, ContextWindow: 200000}},
			TeamMsg{Kind: TeamMember, ParentCallID: "t1", TeamID: "team-t1", Member: "scout",
				MemberSessionID: "team-team-t1-scout", InnerKind: "turn.end",
				Usage:       Usage{InputTokens: 40000, OutputTokens: 80},
				ContextUsed: 40000, ContextWindow: 200000},
		},
		{
			// Issue #331: a member result that ended StopError carries the per-round
			// failure cause (mirroring SubagentMsg.Cause); teamMsg maps it verbatim.
			"team.member result carries cause",
			&mecatlv1.Event{Type: "team.member", Team: &mecatlv1.Team{
				ParentCallId: "t1", TeamId: "team-t1", Member: "scout",
				InnerKind: "result", Stop: "error",
				Cause: "upstream 503: model overloaded"}},
			TeamMsg{Kind: TeamMember, ParentCallID: "t1", TeamID: "team-t1", Member: "scout",
				InnerKind: "result", Stop: "error",
				Cause: "upstream 503: model overloaded"},
		},
		{
			"team.end",
			&mecatlv1.Event{Type: "team.end", Team: &mecatlv1.Team{
				ParentCallId: "t1", TeamId: "team-t1", Rounds: 3, Stop: "end_turn",
				Usage: &mecatlv1.Usage{InputTokens: 4200, OutputTokens: 350}}},
			TeamMsg{Kind: TeamEnd, ParentCallID: "t1", TeamID: "team-t1", Rounds: 3, Stop: "end_turn",
				Usage: Usage{InputTokens: 4200, OutputTokens: 350}},
		},
		{
			// The first-class team.tasks event carries the shared task list (no Member)
			// and maps directly to the TeamTasks discriminant; the tasks decode
			// id/state/assignee/deps.
			"team.tasks snapshot",
			&mecatlv1.Event{Type: "team.tasks", Team: &mecatlv1.Team{
				ParentCallId: "t1", TeamId: "team-t1",
				Tasks: []*mecatlv1.TeamTask{
					{Id: "task-1", Description: "investigate", State: "completed", Assignee: "scout"},
					{Id: "task-2", Description: "fix", State: "pending", Deps: []string{"task-1"}},
				}}},
			TeamMsg{Kind: TeamTasks, ParentCallID: "t1", TeamID: "team-t1",
				Tasks: []TeamTask{
					{ID: "task-1", Description: "investigate", State: "completed", Assignee: "scout"},
					{ID: "task-2", Description: "fix", State: "pending", Deps: []string{"task-1"}},
				}},
		},
		{
			// The first-class team.findings event carries the shared findings ledger (no
			// Member) and maps directly to the TeamFindings discriminant; each entry
			// decodes member + body.
			"team.findings snapshot",
			&mecatlv1.Event{Type: "team.findings", Team: &mecatlv1.Team{
				ParentCallId: "t1", TeamId: "team-t1",
				Findings: []*mecatlv1.TeamFinding{
					{Member: "scout", Body: "the cache key omits the tenant id"},
					{Member: "fixer", Body: "patched the key"},
				}}},
			TeamMsg{Kind: TeamFindings, ParentCallID: "t1", TeamID: "team-t1",
				Findings: []TeamFinding{
					{Member: "scout", Body: "the cache key omits the tenant id"},
					{Member: "fixer", Body: "patched the key"},
				}},
		},
		{
			// The terminal disposition snapshot rides team.end: a done member decodes
			// stopped=false / reason="", and each stop reason enum decodes to its plain
			// string. This is what lets the overlay stop contradicting the supervisor.
			"team.end disposition snapshot",
			&mecatlv1.Event{Type: "team.end", Team: &mecatlv1.Team{
				ParentCallId: "t1", TeamId: "team-t1", Rounds: 2, Stop: "end_turn",
				Dispositions: []*mecatlv1.TeamMemberDisposition{
					{Name: "lead"},
					{Name: "scout", Stopped: true, Reason: mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_BUDGET},
					{Name: "fixer", Stopped: true, Reason: mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_ERROR},
					{Name: "probe", Stopped: true, Reason: mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_CANCELLED},
					// Retried-then-finished (issue #318): not stopped, no reason, so
					// error_rounds is the only field that says the run was not clean —
					// dropping it here would make the lane render a bare "done".
					{Name: "medic", ErrorRounds: 1},
				}}},
			TeamMsg{Kind: TeamEnd, ParentCallID: "t1", TeamID: "team-t1", Rounds: 2, Stop: "end_turn",
				Dispositions: []TeamMemberDisposition{
					{Name: "lead"},
					{Name: "scout", Stopped: true, Reason: "budget"},
					{Name: "fixer", Stopped: true, Reason: "error"},
					{Name: "probe", Stopped: true, Reason: "cancelled"},
					{Name: "medic", ErrorRounds: 1},
				}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := EventToMsg(tc.ev).(TeamMsg)
			if !ok {
				t.Fatalf("EventToMsg(%s) = %T, want TeamMsg", tc.name, EventToMsg(tc.ev))
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("EventToMsg(%s) = %#v, want %#v", tc.name, got, tc.want)
			}
		})
	}
}

// TestConversationMessagesFromProto covers the ConversationMessage projection
// beyond the simple single-field table cases: an assistant message carrying
// ToolCalls + a tool-role ToolResult + the opaque replay blobs (Reasoning /
// ProviderPhase / ReasoningItemID), and a user message carrying media Parts.
// Nil-safe (nil slice → nil; nil entries → zero).
func TestConversationMessagesFromProto(t *testing.T) {
	in := []*mecatlv1.ConversationMessage{
		{
			Role:            "assistant",
			Text:            "I'll read x then write y.",
			Reasoning:       "<reasoning blob>",
			ProviderPhase:   "commentary",
			ReasoningItemId: "rs_1",
			ToolCalls: []*mecatlv1.ToolCall{
				{Id: "c1", Name: "Read", Args: `{"path":"x"}`},
				{Id: "c2", Name: "Write", Args: `{"path":"y"}`},
			},
			ToolResult: &mecatlv1.ToolResult{
				CallId: "c1", Content: "x contents", IsError: false,
				Blocks:            []*mecatlv1.ContentBlock{{Kind: mecatlv1.ContentBlock_KIND_RESOURCE_LINK, Name: "res", Url: "file://x"}},
				StructuredContent: `{"ok":true}`,
			},
		},
		{
			Role: "user",
			Text: "see this",
			Parts: []*mecatlv1.Content{
				{Kind: mecatlv1.Content_KIND_IMAGE, MimeType: "image/png", Data: []byte{1, 2}},
			},
		},
		nil,
	}
	got := conversationMessagesFromProto(in)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}

	// Assistant message: ToolCalls + ToolResult + blobs.
	a := got[0]
	if a.Role != "assistant" || a.Text != "I'll read x then write y." || a.Reasoning != "<reasoning blob>" || a.ProviderPhase != "commentary" || a.ReasoningItemID != "rs_1" {
		t.Errorf("assistant base = %#v", a)
	}
	if len(a.ToolCalls) != 2 || a.ToolCalls[0] != (ConvToolCall{ID: "c1", Name: "Read", Args: `{"path":"x"}`}) {
		t.Errorf("assistant ToolCalls = %#v", a.ToolCalls)
	}
	if a.ToolResult == nil || a.ToolResult.CallID != "c1" || a.ToolResult.Content != "x contents" || a.ToolResult.StructuredContent != `{"ok":true}` {
		t.Errorf("assistant ToolResult = %#v", a.ToolResult)
	}
	if len(a.ToolResult.Blocks) != 1 || a.ToolResult.Blocks[0].Kind != ContentBlockResourceLink || a.ToolResult.Blocks[0].Name != "res" {
		t.Errorf("assistant ToolResult Blocks = %#v", a.ToolResult.Blocks)
	}

	// User message: Parts projected to ContentBlock (image).
	u := got[1]
	if u.Role != "user" || u.Text != "see this" {
		t.Errorf("user base = %#v", u)
	}
	if len(u.Parts) != 1 || u.Parts[0].Kind != ContentBlockImage || u.Parts[0].MimeType != "image/png" || string(u.Parts[0].Data) != "\x01\x02" {
		t.Errorf("user Parts = %#v", u.Parts)
	}

	// nil entry → zero value (nil-safe).
	if !reflect.DeepEqual(got[2], ConversationMessage{}) {
		t.Errorf("nil entry = %#v, want zero", got[2])
	}

	// nil slice → nil.
	if conversationMessagesFromProto(nil) != nil {
		t.Error("nil slice should map to nil")
	}
}

// drain collects all msgs from a closed channel.
func drain(ch <-chan tea.Msg) []tea.Msg {
	var out []tea.Msg
	for m := range ch {
		out = append(out, m)
	}
	return out
}

// TestReadLoopEOF asserts ReadLoop translates the script in order and ends with
// StreamClosedMsg on a clean EOF, then closes the channel.
func TestReadLoopEOF(t *testing.T) {
	fs := newFakeStream(scriptedRunResult()...)
	st := NewStream(fs, fs)
	ch := make(chan tea.Msg, 64)
	go st.ReadLoop(context.Background(), ch)

	msgs := drain(ch)
	if len(msgs) == 0 {
		t.Fatal("no msgs")
	}
	if _, ok := msgs[0].(SessionInitMsg); !ok {
		t.Errorf("first msg = %T, want SessionInitMsg", msgs[0])
	}
	last := msgs[len(msgs)-1]
	if _, ok := last.(StreamClosedMsg); !ok {
		t.Errorf("last msg = %T, want StreamClosedMsg", last)
	}
	// The terminal result must appear just before the close.
	res, ok := msgs[len(msgs)-2].(ResultMsg)
	if !ok || res.Stop != "end_turn" {
		t.Errorf("penultimate msg = %#v, want ResultMsg{end_turn}", msgs[len(msgs)-2])
	}
}

// TestReadLoopError asserts a non-EOF Recv error becomes StreamErrMsg.
func TestReadLoopError(t *testing.T) {
	boom := errors.New("boom")
	fs := newFakeStream(resp(&mecatlv1.Event{Type: "session.init"}))
	fs.endErr = boom
	st := NewStream(fs, fs)
	ch := make(chan tea.Msg, 8)
	go st.ReadLoop(context.Background(), ch)

	msgs := drain(ch)
	last := msgs[len(msgs)-1]
	se, ok := last.(StreamErrMsg)
	if !ok {
		t.Fatalf("last msg = %T, want StreamErrMsg", last)
	}
	if !errors.Is(se.Err, boom) {
		t.Errorf("err = %v, want boom", se.Err)
	}
}

// TestReadLoopCancelUnblocks asserts the reader goroutine exits when its context
// is cancelled even though nobody is draining the (unbuffered) channel — the
// no-leak property is structural via the ctx select, not just reasoned.
func TestReadLoopCancelUnblocks(t *testing.T) {
	// A script with one event and an unbuffered, never-drained channel: the
	// reader will block trying to emit until the context is cancelled.
	fs := newFakeStream(resp(&mecatlv1.Event{Type: "message.delta", Text: "hi"}))
	st := NewStream(fs, fs)
	ch := make(chan tea.Msg) // unbuffered, never drained

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		st.ReadLoop(ctx, ch)
		close(done)
	}()

	cancel()
	select {
	case <-done:
		// reader exited (and closed ch via defer) — good
	case <-time.After(2 * time.Second):
		t.Fatal("ReadLoop did not exit after context cancel (goroutine leak)")
	}
}

// TestAskRoundTrip asserts SendApproval emits a ResumeApproval frame carrying the
// EXACT ask_id (the only correlation) and sets BOTH the verdict enum AND the legacy
// allow bool (so an older server that ignores the verdict still resolves correctly,
// and a newer server can learn an always-allow rule). This is the load-bearing
// round-trip the permission flow depends on.
func TestAskRoundTrip(t *testing.T) {
	tests := []struct {
		name        string
		verdict     Verdict
		wantAllow   bool
		wantVerdict mecatlv1.ApprovalVerdict
	}{
		{"allow once", VerdictAllowOnce, true, mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ONCE},
		{"allow always", VerdictAllowAlways, true, mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ALWAYS},
		{"deny", VerdictDeny, false, mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_DENY},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeStream()
			st := NewStream(fs, fs)
			if err := st.SendApproval("ask-write-1", tc.verdict); err != nil {
				t.Fatalf("SendApproval: %v", err)
			}
			frames := fs.sentFrames()
			if len(frames) != 1 {
				t.Fatalf("sent %d frames, want 1", len(frames))
			}
			ra := frames[0].GetResumeApproval()
			if ra == nil {
				t.Fatalf("frame is not a ResumeApproval: %#v", frames[0])
			}
			if ra.GetAskId() != "ask-write-1" {
				t.Errorf("ask_id = %q, want ask-write-1", ra.GetAskId())
			}
			if ra.GetAllow() != tc.wantAllow {
				t.Errorf("allow = %v, want %v", ra.GetAllow(), tc.wantAllow)
			}
			if ra.GetVerdict() != tc.wantVerdict {
				t.Errorf("verdict = %v, want %v", ra.GetVerdict(), tc.wantVerdict)
			}
		})
	}

	fs := newFakeStream()
	st := NewStream(fs, fs)
	scope := &GuardrailApprovalScope{ReviewID: "review-release-1", Kind: "result_release"}
	if err := st.SendGuardrailApproval("ask-release-1", VerdictAllowOnce, scope); err != nil {
		t.Fatalf("SendGuardrailApproval: %v", err)
	}
	ra := fs.sentFrames()[0].GetResumeApproval()
	if ra.GetReviewId() != scope.ReviewID || ra.GetGuardrailKind() != mecatlv1.GuardrailApprovalKind_GUARDRAIL_APPROVAL_KIND_RESULT_RELEASE {
		t.Fatalf("guardrail acknowledgement = %+v", ra)
	}
}

// TestPromptAndCancelFrames asserts the prompt-first contract and the cancel
// frame shape.
func TestPromptAndCancelFrames(t *testing.T) {
	fs := newFakeStream()
	st := NewStream(fs, fs)
	if err := st.SendPrompt("sess-1", "hello", nil); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if err := st.SendCancel(); err != nil {
		t.Fatalf("SendCancel: %v", err)
	}
	frames := fs.sentFrames()
	if len(frames) != 2 {
		t.Fatalf("sent %d frames, want 2", len(frames))
	}
	p := frames[0].GetPrompt()
	if p == nil || p.GetSessionId() != "sess-1" || p.GetText() != "hello" {
		t.Errorf("first frame = %#v, want Prompt{sess-1,hello}", frames[0])
	}
	if frames[1].GetCancel() == nil {
		t.Errorf("second frame = %#v, want Cancel", frames[1])
	}
}

// TestSendCancelChildFrame asserts SendCancelChild emits a CancelChild frame
// carrying the child session id VERBATIM (the agentId/ChildID handle — the only
// correlation the server uses).
func TestSendCancelChildFrame(t *testing.T) {
	fs := newFakeStream()
	st := NewStream(fs, fs)
	if err := st.SendCancelChild("subagent-p1"); err != nil {
		t.Fatalf("SendCancelChild: %v", err)
	}
	frames := fs.sentFrames()
	if len(frames) != 1 {
		t.Fatalf("sent %d frames, want 1", len(frames))
	}
	cc := frames[0].GetCancelChild()
	if cc == nil {
		t.Fatalf("frame is not a CancelChild: %#v", frames[0])
	}
	if cc.GetChildId() != "subagent-p1" {
		t.Errorf("child_id = %q, want subagent-p1 (verbatim)", cc.GetChildId())
	}
}

// TestSendPromptCarriesParts asserts SendPrompt attaches the media parts to the
// Prompt frame (the multimodal send path), alongside the text.
func TestSendPromptCarriesParts(t *testing.T) {
	fs := newFakeStream()
	st := NewStream(fs, fs)
	parts := []*mecatlv1.Content{
		{Kind: mecatlv1.Content_KIND_IMAGE, MimeType: "image/png", Data: []byte{0x89, 0x50}},
	}
	if err := st.SendPrompt("sess-2", "look at this", parts); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	frames := fs.sentFrames()
	if len(frames) != 1 {
		t.Fatalf("sent %d frames, want 1", len(frames))
	}
	p := frames[0].GetPrompt()
	if p == nil {
		t.Fatalf("frame is not a Prompt: %#v", frames[0])
	}
	if p.GetText() != "look at this" {
		t.Errorf("text = %q, want %q", p.GetText(), "look at this")
	}
	got := p.GetParts()
	if len(got) != 1 {
		t.Fatalf("parts = %d, want 1", len(got))
	}
	if got[0].GetKind() != mecatlv1.Content_KIND_IMAGE || got[0].GetMimeType() != "image/png" {
		t.Errorf("part = %#v, want image/png IMAGE", got[0])
	}
}

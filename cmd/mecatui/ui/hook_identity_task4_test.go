package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func TestMigratedPendingOccurrenceDoesNotJoinOldLogCard(t *testing.T) {
	approval := client.PendingApproval{SessionID: "session", OriginSessionID: "session", ToolCallID: "legacy_tc_1", RunID: "run", AskID: "opaque", Tool: "Write", Args: `{}`}
	transcript := client.SessionTranscript{SessionID: "session", Complete: true, Messages: []client.ConversationMessage{{Role: "assistant", ToolCalls: []client.ConvToolCall{{ID: "legacy_event_tc_1", Name: "Write", Args: `{}`}}}}}
	m := newTestModelFromDeps(Deps{Theme: theme.New("aztec", theme.AztecPalette()), Ctx: t.Context(), Resume: &client.ResumeSelection{Row: client.SessionListItem{ID: "session", Kind: client.SessionKindMain}, Transcript: transcript, Pending: &approval}, NoAltScreen: true})
	defer m.pendingRecovery.cancel()
	if _, ok := m.conv.scrollback.SnapshotForCall("legacy_tc_1"); !ok {
		t.Fatal("missing explicit pending snapshot occurrence")
	}
	m.applyPendingApprovalEventMessage(client.ToolResultMsg{CallID: "legacy_tc_1", Content: "continued"}, m.pendingRecovery)
	old, _ := m.conv.scrollback.SnapshotForCall("legacy_event_tc_1")
	pending, _ := m.conv.scrollback.SnapshotForCall("legacy_tc_1")
	if old.Payload.(scrollback.ToolCardSnapshot).Resolved || !pending.Payload.(scrollback.ToolCardSnapshot).Resolved {
		t.Fatalf("snapshot/log IDs were merged: old=%+v pending=%+v", old, pending)
	}
}

func TestIdentifiedHookLiveAskBeforeHookAndReceipt(t *testing.T) {
	m := quietTestModel(t, nil, false)
	m = applyAll(m, client.ToolCallMsg{ID: "tc_1", Name: "Read"})
	ask := client.PermissionAskMsg{AskID: "opaque-ask", CallID: "tc_1", OriginSessionID: "session", Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: "review_tc_1_action", Kind: "action"}}
	m = applyAll(m, ask)
	initial := approvalSurfaceOf(t, m).ask.review
	if got := initial.snapshot(); got.ID != "review_tc_1_action" || got.CallID != "tc_1" || got.OriginSessionID != "session" {
		t.Fatalf("ask placeholder: %+v", got)
	}
	hook := client.HookMsg{HookID: "review_tc_1_action", Revision: 4, OriginSessionID: "session", CallID: "tc_1", RunID: "run", Seq: 1, Tool: "Read", Guardrail: &client.GuardrailReview{ReviewID: "review_tc_1_action", Job: "action", Disposition: "ask_action"}}
	m = applyAll(m, hook)
	if got := approvalSurfaceOf(t, m).ask.review; got != initial {
		t.Fatal("hook did not reuse the exact ask record")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := initial.snapshot().Detail.Receipt; got == "" {
		t.Fatal("receipt missing before approval transport returns")
	}
	hook.Revision, hook.Seq, hook.Guardrail.Disposition = 6, 2, "execute"
	m = applyAll(m, hook)
	got := initial.snapshot()
	if got.Revision != 6 || got.Detail.Receipt == "" || got.Review.Disposition != "execute" {
		t.Fatalf("machine update erased receipt: %+v", got)
	}
	tools, _ := hookCards(&m.conv)
	if len(tools) != 1 || len(tools[0].Hooks) != 1 || tools[0].Hooks[0].ID != got.ID {
		t.Fatalf("canonical card attachment: %+v", tools)
	}
}

func TestIdentifiedHookCandidateSurvivesConflictAndCleansUp(t *testing.T) {
	var s sessionsState
	s.selected.ID = "session"
	good := client.HookMsg{HookID: "hook_1", Revision: 1, OriginSessionID: "session", CallID: "tc_1", Text: "checked", Phase: "PreToolUse"}
	s.applyReplayEvent(good)
	bad := good
	bad.Revision, bad.CallID, bad.Text = 2, "tc_wrong", "conflicting"
	s.applyReplayEvent(bad)
	if len(s.transcript.pendingHookOrder) != 1 {
		t.Fatalf("conflict removed earlier candidate: %+v", s.transcript.pendingHookOrder)
	}
	s.applyReplayEvent(client.ToolCallMsg{ID: "tc_1", Name: "Read"})
	tools, _ := hookCards(&s.transcript)
	if len(tools) != 1 || len(tools[0].Hooks) != 1 || tools[0].Hooks[0].ID != "hook_1" || len(s.transcript.pendingHookOrder) != 0 {
		t.Fatalf("pending candidate lost: %+v pending=%v", tools, s.transcript.pendingHookOrder)
	}
	s.applyReplayEvent(client.ResultMsg{Stop: "end_turn"})
	if len(s.transcript.pendingHookOrder) != 0 {
		t.Fatal("run end retained candidates")
	}
}

func TestIdentifiedDetailRequiresRequestAndExactSource(t *testing.T) {
	state := guardrailDetailState{requestID: 7}
	msg := client.GuardrailReviewDetailMsg{SessionID: "session", ReviewID: "review", RequestID: 7, Detail: client.GuardrailReviewDetail{ReviewID: "review", OriginSessionID: "session", ToolCallID: "tc", Concern: "validated"}}
	for _, change := range []func(*client.GuardrailReviewDetailMsg){
		func(m *client.GuardrailReviewDetailMsg) { m.RequestID = 6 },
		func(m *client.GuardrailReviewDetailMsg) { m.SessionID = "child" },
		func(m *client.GuardrailReviewDetailMsg) { m.ReviewID = "other" },
	} {
		wrong := msg
		change(&wrong)
		if _, accepted := state.applyDetail(wrong, "session", "review", "session", "tc"); accepted || state.requestID != 7 {
			t.Fatal("stale detail consumed live request")
		}
	}
	wrong := msg
	wrong.Detail.ToolCallID = "other"
	if detail, accepted := state.applyDetail(wrong, "session", "review", "session", "tc"); !accepted || detail.State != scrollback.HookDetailMismatched {
		t.Fatalf("mismatched call accepted: %+v accepted=%v", detail, accepted)
	}
	if _, accepted := state.applyDetail(msg, "session", "review", "session", "tc"); accepted {
		t.Fatal("mismatched reply left token active")
	}
}

func TestAbsentAskOriginDoesNotInferRootOrChild(t *testing.T) {
	r := &guardrailDetailRecorder{}
	m := guardrailTestModel(t, r, false)
	m = applyAll(m, client.ToolCallMsg{ID: "call", Name: "Read"})
	ask := client.PermissionAskMsg{AskID: "session:1:call", CallID: "call", Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: "unknown", Kind: "action", RepeatAvailable: true}}
	m = applyAll(m, ask)
	s := approvalSurfaceOf(t, m)
	if s.ask.offerAlways || r.calls != 0 || s.ask.review.snapshot().OriginSessionID != "" {
		t.Fatalf("unknown origin was guessed or queried: %+v calls=%d", s.ask, r.calls)
	}
	m = applyAll(m, client.HookMsg{CallID: "call", RunID: "run", Seq: 1, Guardrail: &client.GuardrailReview{ReviewID: "unknown", Job: "action", Disposition: "ask_action"}})
	if s.ask.review.snapshot().CallID != "" {
		t.Fatal("origin-less ask adopted root hook")
	}
	review := s.ask.review
	applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if review.snapshot().Detail.Receipt == "" {
		t.Fatal("unknown-origin ask lost its exact verdict receipt")
	}
}

func TestExplicitChildOriginDoesNotJoinRootReviewOrNotify(t *testing.T) {
	m := quietTestModel(t, nil, false)
	m = applyAll(m, client.ToolCallMsg{ID: "tc_root", Name: "Read"})
	m = applyAll(m, client.HookMsg{HookID: "review_child", Revision: 1, OriginSessionID: "child", CallID: "tc_root", Guardrail: &client.GuardrailReview{ReviewID: "review_child", Job: "action", Disposition: "ask_action"}})
	ask := client.PermissionAskMsg{AskID: "opaque-ask", CallID: "tc_root", OriginSessionID: "child", Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: "review_child", Kind: "action"}}
	m = applyAll(m, ask)
	surface := approvalSurfaceOf(t, m)
	if surface.ask.offerAlways || surface.ask.review.snapshot().OriginSessionID != "child" {
		t.Fatalf("child ask was treated as root: %+v", surface.ask)
	}
	tools, _ := hookCards(&m.conv)
	if len(tools) != 1 || len(tools[0].Hooks) != 0 {
		t.Fatalf("child review attached to root card: %+v", tools)
	}
	wrong := ask
	wrong.AskID, wrong.OriginSessionID = "another-ask", "session"
	m = applyAll(m, wrong)
	if got := approvalSurfaceOf(t, m).queue[0].review.snapshot(); got.ID == "review_child" {
		t.Fatalf("mismatched ask joined child record: %+v", got)
	}
}

func TestHistoricalReceiptBeforeHookRequiresExactOriginAndCall(t *testing.T) {
	var s sessionsState
	s.selected.ID = "session"
	s.applyReplayEvent(client.ApprovalMsg{AskID: "opaque-ask", OriginSessionID: "wrong", CallID: "tc", ReviewID: "review_tc_action", Verdict: "deny"})
	s.applyReplayEvent(client.ApprovalMsg{AskID: "opaque-ask-2", OriginSessionID: "session", CallID: "tc", ReviewID: "review_tc_action", Verdict: "allow_once"})
	s.applyReplayEvent(client.HookMsg{HookID: "review_tc_action", Revision: 1, OriginSessionID: "session", CallID: "tc", Guardrail: &client.GuardrailReview{ReviewID: "review_tc_action", Job: "action"}})
	record, _ := s.transcript.scrollback.Hook("review_tc_action")
	if !strings.Contains(record.Detail.Receipt, "allowed once") || strings.Contains(record.Detail.Receipt, "denied") {
		t.Fatalf("receipt attached without proven triple: %+v", record)
	}
	if len(s.transcript.pendingReceipts) != 0 {
		t.Fatalf("pending receipt not cleared after hook: %+v", s.transcript.pendingReceipts)
	}
}

func TestIdentifiedReplayReceiptAndMixedLegacyCard(t *testing.T) {
	var s sessionsState
	s.selected.ID = "session"
	// Old event-log occurrences use legacy_event_tc_, whereas a translated pending
	// snapshot uses legacy_tc_. Matching provider names or ordinals are not a join.
	s.applyReplayEvent(client.ToolCallMsg{ID: "legacy_event_tc_1", Name: "Read"})
	s.applyReplayEvent(client.HookMsg{HookID: "review_legacy_tc_1_action", Revision: 2, OriginSessionID: "session", CallID: "legacy_tc_1", Guardrail: &client.GuardrailReview{ReviewID: "review_legacy_tc_1_action", Job: "action", Disposition: "ask_action"}})
	s.applyReplayEvent(client.ToolCallMsg{ID: "legacy_tc_1", Name: "Read"})
	s.applyReplayEvent(client.ApprovalMsg{AskID: "opaque-ask", CallID: "legacy_tc_1", OriginSessionID: "session", ReviewID: "review_legacy_tc_1_action", Verdict: "allow_once"})
	s.applyReplayEvent(client.ToolResultMsg{CallID: "legacy_tc_1", Content: "continued"})
	tools, _ := hookCards(&s.transcript)
	if len(tools) != 2 || len(tools[0].Hooks) != 0 || len(tools[1].Hooks) != 1 || !tools[1].Resolved || tools[0].Resolved || tools[1].Hooks[0].Detail.Receipt == "" {
		t.Fatalf("mixed replay incorrectly merged occurrences: %+v", tools)
	}
	if !strings.Contains(tools[1].Hooks[0].Detail.Receipt, "allowed once") {
		t.Fatalf("receipt: %+v", tools[1].Hooks[0])
	}
	// Legacy receipts lack a proven triple and stay standalone even with an AskID.
	s.applyReplayEvent(client.ApprovalMsg{AskID: "session:1:legacy_tc_1", Verdict: "deny"})
	stored, _ := s.transcript.scrollback.Hook(scrollback.HookID("review_legacy_tc_1_action"))
	if !strings.Contains(stored.Detail.Receipt, "allowed once") || strings.Contains(stored.Detail.Receipt, "denied") {
		t.Fatalf("old receipt overwrote identified record: %+v", stored)
	}
}

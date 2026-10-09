package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func TestGuardrailCanonicalReplayHasNoLiveDetail(t *testing.T) {
	m := guardrailTestModel(t, &guardrailDetailRecorder{}, false)
	hook := guardrailTestHook("review", "complete", "unresolved", "pass_advisory")
	hook.RunID, hook.Seq = "run", 1
	reply := updateGuardrail(&m, hook)[0]
	m = applyAll(m, reply)
	if m.conv.guardrailReview("review").snapshot().Detail.Concern == "" {
		t.Fatal("live detail missing")
	}
	replay := m.newSessionsSurface(false)
	m.closeModal()
	replay.applyReplayEvent(hook)
	got := replay.transcript.guardrailReview("review").snapshot()
	if got.Detail != (scrollback.HookLiveDetail{}) || got.Review.ReviewID != "review" || got.RunID != "run" || got.Seq != 1 {
		t.Fatalf("replay inherited live detail or lost source: %+v", got)
	}
}

func TestGuardrailCanonicalDetailRejectsMismatchedPayload(t *testing.T) {
	m := guardrailTestModel(t, &guardrailDetailRecorder{}, false)
	ask := client.PermissionAskMsg{AskID: "session:1:call", Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: "review", Kind: "action"}}
	reply := updateGuardrail(&m, ask)[0]
	wrong := reply
	wrong.Detail.ReviewID = "other"
	wrong.Detail.Concern = "must not enter canonical record"
	m = applyAll(m, wrong)
	r := approvalSurfaceOf(t, m).ask.review
	if got := r.snapshot().Detail; got.State != scrollback.HookDetailMismatched || got.Concern != "" || got.SourceDisplay != "" {
		t.Fatalf("mismatch retained data: %+v", got)
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter}, reply)
	if got := r.snapshot().Detail; got.Receipt == "" || got.Concern != "" || got.SourceDisplay != "" || got.State != scrollback.HookDetailMismatched {
		t.Fatalf("closed ask accepted mismatched/late detail: %+v", got)
	}
	if text := lastNotice(m); strings.Contains(text, "must not enter") || !strings.Contains(text, "identity mismatch") {
		t.Fatalf("mismatch receipt: %q", text)
	}
}

func TestGuardrailCanonicalQueuedDetailTokens(t *testing.T) {
	m := guardrailTestModel(t, &guardrailDetailRecorder{}, false)
	ask := func(id string) client.PermissionAskMsg {
		return client.PermissionAskMsg{AskID: "session:1:" + id, Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: id, Kind: "result_release"}}
	}
	first := updateGuardrail(&m, ask("first"))[0]
	second := updateGuardrail(&m, ask("second"))[0]
	bad := second
	bad.RequestID = first.RequestID
	m = applyAll(m, bad)
	s := approvalSurfaceOf(t, m)
	if s.queue[0].review.snapshot().Detail.Concern != "" {
		t.Fatal("wrong token changed queued record")
	}
	m = applyAll(m, second, first)
	if s.ask.review.snapshot().Detail.Concern == "" || s.queue[0].review.snapshot().Detail.Concern == "" || s.ask.review.hookID == s.queue[0].review.hookID {
		t.Fatal("queued and active detail did not stay on distinct canonical records")
	}
	firstReview := s.ask.review
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	late := first
	late.Detail.Concern = "late result"
	m = applyAll(m, late)
	if strings.Contains(firstReview.snapshot().Detail.Concern, "late result") {
		t.Fatal("closed ask token accepted late detail")
	}
}

func TestGuardrailCanonicalAskBeforeAndAfterHook(t *testing.T) {
	for _, hookFirst := range []bool{false, true} {
		m := guardrailTestModel(t, &guardrailDetailRecorder{}, false)
		hook := guardrailTestHook("review", "operational_failure", "unresolved", "ask_action")
		hook.CallID, hook.RunID, hook.Seq = "call", "run", 2
		ask := client.PermissionAskMsg{AskID: "session:1:call", CallID: "call", RunID: "run", Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: "review", Kind: "action"}}
		m = applyAll(m, client.ToolCallMsg{ID: "call", Name: "Read"})
		if hookFirst {
			m = applyAll(m, hook)
		}
		reply := updateGuardrail(&m, ask)[0]
		m = applyAll(m, reply)
		if !hookFirst {
			m = applyAll(m, hook)
		}
		r := approvalSurfaceOf(t, m).ask.review
		record := r.snapshot()
		if record.ID == 0 || record.Review.ReviewID != "review" || !strings.HasPrefix(record.Detail.Concern, "Test explanation") || strings.Contains(record.Detail.Concern, "\x1b") || strings.Contains(record.Detail.SourceDisplay, "\x1b") || record.CallID != "call" {
			t.Fatalf("hookFirst=%v canonical record: %+v", hookFirst, record)
		}
		tool := m.conv.scrollback.SnapshotAt(0).Payload.(scrollback.ToolCardSnapshot)
		if len(tool.Hooks) != 1 || tool.Hooks[0].ID != record.ID || tool.Hooks[0].Detail != record.Detail {
			t.Fatalf("hookFirst=%v attachment: %+v", hookFirst, tool.Hooks)
		}
		m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		record = r.snapshot()
		if record.Detail.State != scrollback.HookDetailReceipt || !strings.Contains(record.Detail.Receipt, "approved") || !strings.HasPrefix(record.Detail.Concern, "Test explanation") {
			t.Fatalf("receipt: %+v", record)
		}
		final := guardrailTestHook("review", "complete", "acceptable", "execute")
		final.CallID, final.RunID, final.Seq = "call", "run", 3
		m = applyAll(m, final, hook)
		record = r.snapshot()
		if record.Detail.Receipt == "" || !strings.HasPrefix(record.Detail.Concern, "Test explanation") || record.Review.Disposition != "execute" || !strings.Contains(lastNotice(m), "approved") {
			t.Fatalf("hookFirst=%v final lost receipt: %+v notice=%q", hookFirst, record, lastNotice(m))
		}
	}
}

func TestOlderGuardrailSourceRemainsVisibleConflict(t *testing.T) {
	for _, width := range []int{40, 80, 100, 120} {
		m := guardrailTestModel(t, nil, false)
		m = applyAll(m, tea.WindowSizeMsg{Width: width, Height: 30})
		m = applyAll(m, client.ToolCallMsg{ID: "call", Name: "Read"})
		newer := guardrailTestHook("review", "complete", "unresolved", "withhold_result")
		newer.CallID, newer.RunID, newer.Seq = "call", "run", 12
		m = applyAll(m, newer)
		older := guardrailTestHook("review", "operational_failure", "unresolved", "ask_action")
		older.CallID, older.RunID, older.Seq = "call", "run", 11
		older.Text = "older conflicting evidence"
		m = applyAll(m, older)
		if got := m.conv.guardrailReview("review").snapshot(); got.Seq != 12 || got.Review.Disposition != "withhold_result" {
			t.Fatalf("older source changed canonical review: %+v", got)
		}
		frame := strings.Join(strings.Fields(frameText(&m)), " ")
		if !strings.Contains(frame, "older conflicting evidence") {
			t.Fatalf("older conflict was not visible: %q", frame)
		}
		m = applyAll(m, older)
		if got := strings.Count(strings.Join(strings.Fields(frameText(&m)), " "), "older conflicting evidence"); got != 1 {
			t.Fatalf("older conflict replay added notice: %d", got)
		}
	}
}

func TestUnidentifiedGuardrailCannotChangeActiveApproval(t *testing.T) {
	for _, width := range []int{40, 80, 100, 120} {
		m := guardrailTestModel(t, nil, false)
		m = applyAll(m, tea.WindowSizeMsg{Width: width, Height: 30})
		m = applyAll(m, client.ToolCallMsg{ID: "call", Name: "Read"}, client.PermissionAskMsg{AskID: "session:1:call", Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: "review", Kind: "action"}})
		newer := guardrailTestHook("review", "complete", "unresolved", "withhold_result")
		newer.CallID, newer.RunID, newer.Seq = "call", "run", 12
		m = applyAll(m, newer)
		s := approvalSurfaceOf(t, m)
		reason := s.ask.Reason
		unknown := guardrailTestHook("review", "operational_failure", "unresolved", "ask_action")
		unknown.CallID, unknown.Text = "call", "unidentified conflicting evidence"
		m = applyAll(m, unknown)
		if got := s.ask.review.snapshot(); got.Seq != 12 || got.Review.Disposition != "withhold_result" || s.ask.Reason != reason {
			t.Fatalf("unidentified source changed approval: %+v", got)
		}
		frame := strings.Join(strings.Fields(frameText(&m)), " ")
		if !strings.Contains(frame, "unidentified conflicting evidence") {
			t.Fatalf("width=%d unidentified source evidence was hidden: %q", width, frame)
		}
	}
}

func TestGuardrailChildAskDoesNotAdoptRootReview(t *testing.T) {
	m := guardrailTestModel(t, &guardrailDetailRecorder{}, false)
	ask := client.PermissionAskMsg{AskID: "child:1:call", CallID: "call", Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: "review", Kind: "action"}}
	reply := updateGuardrail(&m, ask)[0]
	root := guardrailTestHook("review", "operational_failure", "unresolved", "ask_action")
	root.CallID, root.RunID, root.Seq = "call", "run", 2
	m = applyAll(m, client.ToolCallMsg{ID: "call", Name: "Read"}, root)
	s := approvalSurfaceOf(t, m)
	if s.ask.review.snapshot().CallID != "" || s.ask.Reason == hookText(hookSnapshot(root)) {
		t.Fatalf("root hook adopted child ask: %+v", s.ask.review.snapshot())
	}
	m = applyAll(m, reply)
	if !strings.HasPrefix(s.ask.review.snapshot().Detail.Concern, "Test explanation") {
		t.Fatalf("child detail not accepted for child session: reply=%+v detail=%+v", reply, s.ask.review.snapshot().Detail)
	}
	rootRecord := m.conv.guardrailReview("review").snapshot()
	if rootRecord.ID == s.ask.review.hookID || rootRecord.Detail.Concern != "" {
		t.Fatalf("child detail leaked into root: %+v", rootRecord)
	}
}

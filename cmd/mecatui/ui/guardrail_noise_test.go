package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

type guardrailDetailRecorder struct {
	calls int
	err   error
}

func (*guardrailDetailRecorder) ListGuardrailCoverage(context.Context, string) (client.GuardrailCoverage, error) {
	return client.GuardrailCoverage{}, nil
}
func (r *guardrailDetailRecorder) GetGuardrailReviewDetail(_ context.Context, _, id string) (client.GuardrailReviewDetail, error) {
	r.calls++
	if r.err != nil {
		return client.GuardrailReviewDetail{}, r.err
	}
	return client.GuardrailReviewDetail{ReviewID: id, Concern: "Test explanation\x1b[2J", SourceDisplay: "source\x1b[2J", NextAction: "Run once (not valid for an advisory)"}, nil
}

func guardrailTestModel(t *testing.T, r *guardrailDetailRecorder, debug bool) Model {
	t.Helper()
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()), func(d *Deps) { d.Guardrails, d.Debug = r, debug })
	m.sessionID = "session"
	return m
}

func guardrailTestHook(id, inspection, assessment, disposition string) client.HookMsg {
	return client.HookMsg{Tool: "Read", Phase: "PostToolUse", Guardrail: &client.GuardrailReview{ReviewID: id, Job: "inbound", Inspection: inspection, Assessment: assessment, Disposition: disposition, CheckerProviderID: "provider", CheckerModelID: "model"}}
}

// updateGuardrail runs the real reducer and its commands, leaving detail replies
// unapplied so each test controls their ordering and correlation.
func updateGuardrail(m *Model, msg tea.Msg) []client.GuardrailReviewDetailMsg {
	next, cmd := m.Update(msg)
	*m = next.(Model)
	return guardrailReplies(cmd)
}

func guardrailReplies(cmd tea.Cmd) []client.GuardrailReviewDetailMsg {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case client.GuardrailReviewDetailMsg:
		return []client.GuardrailReviewDetailMsg{msg}
	case tea.BatchMsg:
		var replies []client.GuardrailReviewDetailMsg
		for _, child := range msg {
			replies = append(replies, guardrailReplies(child)...)
		}
		return replies
	}
	return nil
}

// Routine reviews are retained but hidden while details are collapsed; debug
// mode keeps them visible with checker metadata.
func TestGuardrailRoutineReviewsQuiet(t *testing.T) {
	for _, debug := range []bool{false, true} {
		r := &guardrailDetailRecorder{}
		m := guardrailTestModel(t, r, debug)
		replay := m.newSessionsSurface(false)
		m.closeModal()
		for i, id := range []string{"one", "two", "three"} {
			msg := guardrailTestHook(id, "complete", "acceptable", "release_result")
			if i == 1 {
				msg.Guardrail.Job, msg.Guardrail.Disposition = "action", "execute"
			}
			updateGuardrail(&m, msg)
			replay.applyReplayEvent(msg)
		}
		if m.conv.scrollback.Len() != 3 || replay.transcript.scrollback.Len() != 3 || r.calls != 3 {
			t.Fatalf("debug=%v: live=%d replay=%d requests=%d", debug, m.conv.scrollback.Len(), replay.transcript.scrollback.Len(), r.calls)
		}
		for i := range 3 {
			live := m.conv.scrollback.SnapshotAt(i).Payload.(scrollback.NoticeCardSnapshot)
			stored := replay.transcript.scrollback.SnapshotAt(i).Payload.(scrollback.NoticeCardSnapshot)
			if live.Text != stored.Text || !live.BenignGuardrail || !stored.BenignGuardrail || !strings.Contains(live.Text, "check passed") ||
				debug != strings.Contains(live.Text, "checker provider/model") {
				t.Fatalf("debug=%v diagnostic: live=%+v replay=%+v", debug, live, stored)
			}
		}
		frame := stripANSIstr(strings.Join(m.rend.renderConversationLines(&m.conv.scrollback, false), "\n"))
		if debug != strings.Contains(frame, "check passed") {
			t.Fatalf("debug=%v collapsed visibility: %q", debug, frame)
		}
	}
}

func TestGuardrailWarningsVisibleLiveAndReplay(t *testing.T) {
	for _, tc := range []struct{ name, inspection, assessment, disposition, want string }{
		{"finding", "complete", "prohibited", "pass_advisory", "security finding"},
		{"unresolved", "complete", "unresolved", "pass_advisory", "could not determine"},
		{"outage continued", "operational_failure", "acceptable", "continue_warning", "Work continued"},
		{"outage withheld", "operational_failure", "unresolved", "withhold_result", "Result withheld"},
		{"outage stopped", "operational_failure", "unresolved", "deny", "Result withheld"},
		{"unknown inspection", "future", "acceptable", "release_result", "status is unknown"},
		{"unknown assessment", "complete", "future", "execute", "status is unknown"},
		{"unknown outcome", "complete", "acceptable", "future", "outcome is unknown"},
		{"advisory acceptable", "complete", "acceptable", "pass_advisory", "Work continued"},
		{"approval pending", "operational_failure", "unresolved", "ask_action", "Action paused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &guardrailDetailRecorder{}
			m := guardrailTestModel(t, r, false)
			msg := guardrailTestHook("review", tc.inspection, tc.assessment, tc.disposition)
			replies := updateGuardrail(&m, msg)
			replay := m.newSessionsSurface(false)
			m.closeModal()
			replay.applyReplayEvent(msg)
			if m.conv.scrollback.Len() != 1 || replay.transcript.scrollback.Len() != 1 {
				t.Fatal("warning hidden")
			}
			before := m.conv.scrollback.SnapshotAt(0)
			text := before.Payload.(scrollback.NoticeCardSnapshot).Text
			if text != replay.transcript.scrollback.SnapshotAt(0).Payload.(scrollback.NoticeCardSnapshot).Text || !strings.Contains(text, tc.want) {
				t.Fatalf("warning = %q", text)
			}
			for _, reply := range replies {
				m = applyAll(m, reply, reply)
			}
			updateGuardrail(&m, msg)
			wantCalls := 1
			if tc.disposition == "ask_action" {
				wantCalls = 0
			}
			after := m.conv.scrollback.SnapshotAt(0)
			if m.conv.scrollback.Len() != 1 || after.ID != before.ID || r.calls != wantCalls || after.Revision != uint64(wantCalls) {
				t.Fatalf("duplicate presentation or request: %+v calls=%d", after, r.calls)
			}
			text = after.Payload.(scrollback.NoticeCardSnapshot).Text
			if wantCalls == 1 && !strings.Contains(text, "Test explanation") {
				t.Fatal("detail did not enrich warning")
			}
			for _, forbidden := range []string{"Guardrail detail", "PostToolUse", "inbound", "release_result", "checker provider/model", "\x1b", "not valid for an advisory"} {
				if strings.Contains(text, forbidden) {
					t.Fatalf("unexpected %q in %q", forbidden, text)
				}
			}
		})
	}
}

func TestGuardrailDetailCorrelationAndLifecycle(t *testing.T) {
	for _, fail := range []bool{false, true} {
		r := &guardrailDetailRecorder{}
		if fail {
			r.err = errors.New("private failure")
		}
		m := guardrailTestModel(t, r, false)
		hook := guardrailTestHook("review", "operational_failure", "unresolved", "pass_advisory")
		reply := updateGuardrail(&m, hook)[0]
		for _, wrong := range []client.GuardrailReviewDetailMsg{
			{SessionID: "other", ReviewID: reply.ReviewID, RequestID: reply.RequestID, Err: r.err, Detail: reply.Detail},
			{SessionID: reply.SessionID, ReviewID: "other", RequestID: reply.RequestID, Err: r.err, Detail: reply.Detail},
			{SessionID: reply.SessionID, ReviewID: reply.ReviewID, RequestID: reply.RequestID + 1, Err: r.err, Detail: reply.Detail},
		} {
			m = applyAll(m, wrong)
		}
		if m.conv.scrollback.SnapshotAt(0).Revision != 0 {
			t.Fatal("uncorrelated reply changed warning")
		}
		m = applyAll(m, reply, reply)
		if m.conv.scrollback.SnapshotAt(0).Revision != 1 {
			t.Fatal("current reply not applied exactly once")
		}
		text := lastNotice(m)
		if fail && (!strings.Contains(text, "unavailable or expired") || strings.Contains(text, "private failure")) {
			t.Fatalf("failure = %q", text)
		}

		// A new conversation can reuse both session/review IDs. Its new request
		// must not accept a reply from the old conversation (including errors).
		m.conv = conversation{}
		fresh := updateGuardrail(&m, hook)[0]
		m = applyAll(m, reply)
		if m.conv.scrollback.SnapshotAt(0).Revision != 0 {
			t.Fatal("old UI lifecycle changed replacement warning")
		}
		m = applyAll(m, client.SessionReadyMsg{SessionID: "other"}, fresh)
		if m.conv.scrollback.SnapshotAt(0).Revision != 0 {
			t.Fatal("old session reply changed current conversation")
		}
	}
}

func TestGuardrailApprovalOwnsExplanationAndQueuedReplies(t *testing.T) {
	r := &guardrailDetailRecorder{}
	m := guardrailTestModel(t, r, false)
	hook := guardrailTestHook("action", "operational_failure", "unresolved", "ask_action")
	hook.Guardrail.Job = "action"
	m = applyAll(m, hook)
	ask := client.PermissionAskMsg{AskID: "session:1:action", Tool: "Shell\x1b[2J", Reason: "PreToolUse inbound checker metadata", Guardrail: &client.GuardrailApprovalScope{ReviewID: "action", Kind: "action", RepeatAvailable: true}}
	actionReply := updateGuardrail(&m, ask)[0]
	updateGuardrail(&m, ask)
	if r.calls != 1 || m.conv.scrollback.Len() != 0 {
		t.Fatal("approval did not take exclusive ownership")
	}
	queued := client.PermissionAskMsg{AskID: "child:2:result", Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: "result", Kind: "result_release"}}
	resultReply := updateGuardrail(&m, queued)[0]
	if resultReply.SessionID != "child" {
		t.Fatalf("child detail request = %+v", resultReply)
	}
	bad := resultReply
	bad.SessionID = "session"
	bad.Err = errors.New("wrong child failure")
	m = applyAll(m, bad)
	s := approvalSurfaceOf(t, m)
	if s.ask.unavailable || s.queue[0].unavailable {
		t.Fatal("wrong-session failure affected approval")
	}
	m = applyAll(m, resultReply, actionReply, actionReply)
	s = approvalSurfaceOf(t, m)
	if s.ask.detail.ReviewID != "action" || s.queue[0].detail.ReviewID != "result" {
		t.Fatal("queued detail lost or sent to wrong approval")
	}
	body, _ := s.permissionModalBodyParts(100, 80)
	if strings.Contains(body, "\x1b[2J") {
		t.Fatal("approval rendered an untrusted terminal escape")
	}
	body = stripANSIstr(body)
	if !strings.Contains(body, "Test explanation") || !strings.Contains(body, "outage") || !strings.Contains(body, "this exact action in this session") {
		t.Fatalf("approval explanation = %q", body)
	}
	if strings.Contains(body, "PreToolUse") || strings.Contains(body, "inbound") || strings.Contains(body, "\x1b") || strings.Contains(body, "al[w]ays") {
		t.Fatalf("unsafe or technical approval = %q", body)
	}
	s.openArgs(100, 80)
	if !strings.Contains(stripANSIstr(s.argsVP.View()), "Test explanation") {
		t.Fatal("expanded details omitted explanation")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	late := actionReply
	late.Err = errors.New("late failure")
	m = applyAll(m, late)
	s = approvalSurfaceOf(t, m)
	if s.ask.guardrail.ReviewID != "result" || s.ask.unavailable {
		t.Fatal("resolved prompt failure affected successor")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	count := m.conv.scrollback.Len()
	m = applyAll(m, resultReply, late, hook)
	if m.conv.scrollback.Len() != count {
		t.Fatal("resolved review emitted detached notice")
	}
}

func TestGuardrailApprovalDetailFailureCorrelation(t *testing.T) {
	r := &guardrailDetailRecorder{err: errors.New("private outage detail")}
	m := guardrailTestModel(t, r, false)
	ask := client.PermissionAskMsg{AskID: "session:1:result", Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: "result", Kind: "result_release"}}
	reply := updateGuardrail(&m, ask)[0]
	wrong := reply
	wrong.ReviewID = "other"
	m = applyAll(m, wrong)
	wrong = reply
	wrong.RequestID++
	m = applyAll(m, wrong)
	if approvalSurfaceOf(t, m).ask.unavailable {
		t.Fatal("uncorrelated error changed prompt")
	}
	m = applyAll(m, reply)
	s := approvalSurfaceOf(t, m)
	body, _ := s.permissionModalBodyParts(100, 80)
	body = stripANSIstr(body)
	if !s.ask.unavailable || !strings.Contains(body, "unavailable or expired") || !strings.Contains(body, "withheld from the model") || strings.Contains(body, "private outage detail") || strings.Contains(body, "Concern:") {
		t.Fatalf("failure prompt = %q", body)
	}

	// An unrelated warning reply must pass through the approval surface to its
	// own entry, rather than being swallowed or changing the active prompt.
	hook := guardrailTestHook("warning", "complete", "unresolved", "pass_advisory")
	warning := updateGuardrail(&m, hook)[0]
	m = applyAll(m, warning)
	if m.conv.scrollback.Len() != 1 || !strings.Contains(lastNotice(m), "unavailable or expired") {
		t.Fatal("approval swallowed unrelated warning detail")
	}

	// Reopen the same IDs in a new UI lifecycle; neither success nor error
	// from the previous prompt may modify it.
	m.closeModal()
	m.conv = conversation{}
	fresh := updateGuardrail(&m, ask)[0]
	m = applyAll(m, reply)
	reply.Err = nil
	reply.Detail = client.GuardrailReviewDetail{ReviewID: "result", Concern: "stale"}
	m = applyAll(m, reply)
	if s = approvalSurfaceOf(t, m); s.ask.unavailable || s.ask.detail.Concern != "" {
		t.Fatal("previous prompt changed reopened approval")
	}
	m = applyAll(m, fresh)
	if !approvalSurfaceOf(t, m).ask.unavailable {
		t.Fatal("current prompt error not applied")
	}
}

func TestGuardrailApprovalVerdictsKeepExactScope(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		repeat     bool
		key        rune
		verdict    client.Verdict
		buttons    int
	}{
		{"run", "action", true, 'a', client.VerdictAllowOnce, 3},
		{"repeat", "action", true, 'w', client.VerdictAllowAlways, 3},
		{"nonrepeatable", "action", false, 'a', client.VerdictAllowOnce, 2},
		{"release", "result_release", false, 'a', client.VerdictAllowOnce, 2},
		{"cancel result", "result_release", false, 'd', client.VerdictDeny, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := guardrailTestModel(t, &guardrailDetailRecorder{}, false)
			recorder := &authorizationControlRecorder{}
			m.authorization = mcpAuthorizationState{controlStream: client.NewAuthorizationEventStream(client.NewFakeEventStream(), recorder), controlGen: 1, runningControlGen: 1}
			scope := &client.GuardrailApprovalScope{ReviewID: "review", Kind: tc.kind, RepeatAvailable: tc.repeat, SessionOnly: true, GrantDigest: "exact-action"}
			m = applyAll(m, client.PermissionAskMsg{AskID: "session:1:call", Tool: "Shell", Guardrail: scope, ExpectedRunID: "run"})
			s := approvalSurfaceOf(t, m)
			if got := len(approvalButtons(s.deps.theme, s.deps.marks, s.ask, false)); got != tc.buttons {
				t.Fatalf("buttons=%d", got)
			}
			if !tc.repeat {
				m = applyAll(m, tea.KeyPressMsg{Code: 'w', Text: "w"})
				if approvalSurfaceFor(&m) == nil {
					t.Fatal("unavailable repeat choice resolved approval")
				}
			}
			_, cmd := m.Update(tea.KeyPressMsg{Code: tc.key, Text: string(tc.key)})
			runBatchLeaves(cmd)
			if recorder.askID != "session:1:call" || recorder.expectedRunID != "run" || recorder.guardrail != scope || recorder.verdict != tc.verdict {
				t.Fatalf("approval lost exact scope: %+v", recorder)
			}
		})
	}
}

package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

// resolveInbound surfaces the release ask before emitting the final review hook.
func TestGuardrailInboundOutcomeAfterApproval(t *testing.T) {
	for _, debug := range []bool{false, true} {
		for _, initialFailed := range []bool{false, true} {
			for _, freshFailed := range []bool{false, true} {
				for _, inspection := range []string{"operational_failure", "complete"} {
					t.Run(fmt.Sprintf("debug=%v/initialFailed=%v/freshFailed=%v/%s", debug, initialFailed, freshFailed, inspection), func(t *testing.T) {
						r := &guardrailDetailRecorder{}
						m := guardrailTestModel(t, r, debug)
						ask := client.PermissionAskMsg{AskID: "session:1:result", Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: "result", Kind: "result_release"}}
						delayed := updateGuardrail(&m, ask)[0]
						failure := delayed
						failure.Err = errors.New("private original failure")
						if initialFailed {
							m = applyAll(m, failure)
						}
						m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
						if m.conv.scrollback.Len() != 1 {
							t.Fatal("missing approval receipt")
						}
						before := m.conv.scrollback.SnapshotAt(0)
						if !initialFailed && strings.Contains(before.Payload.(scrollback.NoticeCardSnapshot).Text, "unavailable or expired") {
							t.Fatal("pending explanation was reported as expired")
						}
						m = applyAll(m, delayed, failure)
						if m.conv.scrollback.SnapshotAt(0) != before {
							t.Fatal("closed prompt reply changed the receipt before the final hook")
						}
						final := guardrailTestHook("result", inspection, "prohibited", "release_result")
						want := "security finding"
						if inspection == "operational_failure" {
							final.Guardrail.Assessment = "unresolved"
							want = "outage"
						}
						if freshFailed {
							r.err = errors.New("private fresh failure")
						}
						fresh := updateGuardrail(&m, final)
						if len(fresh) != 1 || fresh[0].RequestID == delayed.RequestID || fresh[0].SessionID != delayed.SessionID || fresh[0].ReviewID != delayed.ReviewID {
							t.Fatalf("missing fresh correlated request: %+v", fresh)
						}
						after := m.conv.scrollback.SnapshotAt(0)
						text := after.Payload.(scrollback.NoticeCardSnapshot).Text
						if !strings.Contains(text, want) || !strings.Contains(text, "Result released to the model") || strings.Contains(text, "unavailable or expired") {
							t.Fatalf("incorrect outcome while fetching explanation: %q", text)
						}
						if after.ID != before.ID || m.conv.scrollback.Len() != 1 {
							t.Fatalf("duplicate outcome: %+v", after)
						}
						m = applyAll(m, delayed, failure)
						updateGuardrail(&m, final)
						if got := m.conv.scrollback.SnapshotAt(0); got != after || r.calls != 2 {
							t.Fatalf("stale reply or duplicate final hook changed pending receipt: %+v calls=%d", got, r.calls)
						}
						m = applyAll(m, fresh[0])
						updated := m.conv.scrollback.SnapshotAt(0)
						text = updated.Payload.(scrollback.NoticeCardSnapshot).Text
						if updated.ID != before.ID || updated.Revision != after.Revision+1 || m.conv.scrollback.Len() != 1 {
							t.Fatalf("fresh explanation did not update the same receipt: %+v", updated)
						}
						if freshFailed {
							if !strings.Contains(text, "unavailable or expired") || !strings.Contains(text, want) || strings.Contains(text, r.err.Error()) {
								t.Fatalf("incorrect detail failure fallback: %q", text)
							}
						} else if !strings.Contains(text, "Test explanation") || strings.Contains(text, "unavailable or expired") {
							t.Fatalf("fresh explanation missing: %q", text)
						}
						m = applyAll(m, delayed, failure, fresh[0])
						updateGuardrail(&m, final)
						if got := m.conv.scrollback.SnapshotAt(0); got != updated || r.calls != 2 {
							t.Fatalf("completed receipt changed on stale/duplicate reply: %+v calls=%d", got, r.calls)
						}
						if debug != strings.Contains(text, "checker provider/model") {
							t.Fatalf("debug metadata visibility: %q", text)
						}
					})
				}
			}
		}
	}
}

func TestGuardrailFinalHookSkipsUnneededDetailFetch(t *testing.T) {
	for _, debug := range []bool{false, true} {
		for _, tc := range []struct{ routine, explained bool }{{true, false}, {true, true}, {false, true}} {
			r := &guardrailDetailRecorder{}
			m := guardrailTestModel(t, r, debug)
			original := updateGuardrail(&m, client.PermissionAskMsg{AskID: "ask", Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: "review", Kind: "result_release"}})[0]
			if tc.explained {
				m = applyAll(m, original)
			}
			m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
			before := m.conv.scrollback.SnapshotAt(0)
			assessment := "prohibited"
			if tc.routine {
				assessment = "acceptable"
			}
			final := guardrailTestHook("review", "complete", assessment, "release_result")
			for range 2 {
				updateGuardrail(&m, final)
			}
			after := m.conv.scrollback.SnapshotAt(0)
			if r.calls != 1 || m.conv.scrollback.Len() != 1 || after.ID != before.ID {
				t.Fatalf("unneeded request or extra receipt: debug=%v case=%+v calls=%d", debug, tc, r.calls)
			}
			if tc.explained && !strings.Contains(after.Payload.(scrollback.NoticeCardSnapshot).Text, "Test explanation") {
				t.Fatal("existing explanation was lost")
			}
			m = applyAll(m, original)
			if m.conv.scrollback.SnapshotAt(0) != after {
				t.Fatal("closed prompt request was reused")
			}
		}
	}
}

func TestGuardrailEmptyReviewIDsStayIndependent(t *testing.T) {
	r := &guardrailDetailRecorder{}
	m := guardrailTestModel(t, r, false)
	first := guardrailTestHook("", "operational_failure", "unresolved", "pass_advisory")
	m = applyAll(m, first, first)
	if m.conv.scrollback.Len() != 2 || r.calls != 0 || len(m.conv.guardrailReviews) != 0 {
		t.Fatal("unidentified warnings were merged, stored, or fetched")
	}
	for _, id := range []string{"one", "two"} {
		m = applyAll(m, client.PermissionAskMsg{AskID: id, Tool: "Read", Guardrail: &client.GuardrailApprovalScope{Kind: "result_release"}})
	}
	if m.conv.scrollback.Len() != 2 || r.calls != 0 {
		t.Fatal("unidentified asks changed warnings or issued detail RPCs")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.conv.scrollback.Len() != 3 {
		t.Fatal("first unidentified ask did not leave its own receipt")
	}
	firstReceipt := m.conv.scrollback.SnapshotAt(2)
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.conv.scrollback.Len() != 4 || m.conv.scrollback.SnapshotAt(2) != firstReceipt || len(m.conv.guardrailReviews) != 0 {
		t.Fatal("unidentified receipts merged or changed each other")
	}
	m = applyAll(m, guardrailTestHook("", "operational_failure", "unresolved", "release_result"))
	if m.conv.scrollback.Len() != 5 || m.conv.scrollback.SnapshotAt(2) != firstReceipt || r.calls != 0 {
		t.Fatal("unidentified final hook overwrote a receipt or fetched detail")
	}
}

func TestGuardrailRefusedSendRestoresAskWithoutRemovingOtherReceipts(t *testing.T) {
	m := guardrailTestModel(t, &guardrailDetailRecorder{}, false)
	for _, id := range []string{"first", "second"} {
		m = applyAll(m, client.PermissionAskMsg{AskID: id, Tool: "Read", Guardrail: &client.GuardrailApprovalScope{Kind: "result_release"}})
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	firstReceipt := m.conv.scrollback.SnapshotAt(0)
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.restoreControlRefused(client.ControlRefusedMsg{AskID: "second", Text: "rejected"}) {
		t.Fatal("rejected approval was not restored")
	}
	if m.conv.scrollback.Len() != 1 || m.conv.scrollback.SnapshotAt(0) != firstReceipt || approvalSurfaceOf(t, m).ask.AskID != "second" {
		t.Fatal("rejected send removed the wrong receipt or did not restore its ask")
	}
}

func TestGuardrailAbandonedApprovalCannotRestoreIntoReplacementDocument(t *testing.T) {
	for _, boundary := range []struct {
		name    string
		abandon func(Model) Model
	}{
		{"stream closes without result", func(m Model) Model { return applyAll(m, client.StreamClosedMsg{}) }},
		{"session reset", func(m Model) Model { return m.resetSession() }},
		{"session derived reset", func(m Model) Model { return m.resetSessionDerived() }},
		{"session switch", func(m Model) Model { return applyAll(m, client.SessionReadyMsg{SessionID: "replacement"}) }},
		{"same session rebind", func(m Model) Model { return applyAll(m, client.SessionReadyMsg{SessionID: m.sessionID}) }},
	} {
		for _, reviewID := range []string{"review", ""} {
			t.Run(boundary.name+"/"+reviewID, func(t *testing.T) {
				m := guardrailTestModel(t, &guardrailDetailRecorder{}, false)
				m.phase = phaseRunning
				control := &authorizationControlRecorder{err: errors.New("delayed approval send failure")}
				m.authorization = mcpAuthorizationState{controlStream: client.NewAuthorizationEventStream(client.NewFakeEventStream(), control), controlGen: 1, runningControlGen: 1}
				m = applyAll(m, client.PermissionAskMsg{AskID: "old-ask", Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: reviewID, Kind: "result_release"}})
				next, send := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				m = next.(Model)
				var delayed client.StreamErrMsg
				for _, msg := range collectLeaves(send) {
					if failure, ok := msg.(client.StreamErrMsg); ok {
						delayed = failure
					}
				}
				if delayed.Err == nil || control.askID != "old-ask" {
					t.Fatal("approval send did not produce its deferred failure")
				}
				if m.pendingApproval == nil || m.phase != phaseRunning {
					t.Fatal("approval did not remain pending while the run continued")
				}
				receiptID := m.pendingApproval.ask.review.blockID
				m = boundary.abandon(m)
				if m.pendingApproval != nil {
					t.Error("abandoned approval still permits restoration")
				}
				// Replace only the document so another reset cannot mask missing cleanup.
				m.conv = conversation{}
				if id := m.conv.scrollback.Notices().AddNotice("replacement document notice"); id != receiptID {
					t.Fatalf("fixture needs colliding block IDs: got %d, want %d", id, receiptID)
				}
				before := m.conv.scrollback.SnapshotAt(0)
				m.prompt.Rewrite("replacement draft")
				m = applyAll(m, delayed)
				if m.conv.scrollback.Len() == 0 || m.conv.scrollback.SnapshotAt(0) != before {
					t.Fatal("delayed failure removed or changed the replacement notice")
				}
				if approvalSurfaceFor(&m) != nil || m.phase == phaseAwaitingApproval || m.prompt.Value() != "replacement draft" {
					t.Fatal("delayed failure restored an abandoned ask or changed the new draft")
				}
			})
		}
	}
}

func TestGuardrailWithheldResultDoesNotSuggestRerunningTool(t *testing.T) {
	for _, disposition := range []string{"withhold_result", "deny"} {
		m := guardrailTestModel(t, &guardrailDetailRecorder{}, false)
		m = applyAll(m, guardrailTestHook("review", "operational_failure", "unresolved", disposition))
		text := lastNotice(m)
		if !strings.Contains(text, "Result withheld from the model") || !strings.Contains(text, "already run") || !strings.Contains(text, "side effects") || strings.Contains(text, "retry") {
			t.Fatalf("misleading withheld-result guidance: %q", text)
		}
	}
}

func TestGuardrailHookWhileApprovalActive(t *testing.T) {
	m := guardrailTestModel(t, &guardrailDetailRecorder{}, false)
	ask := client.PermissionAskMsg{AskID: "session:1:action", Tool: "Shell", Guardrail: &client.GuardrailApprovalScope{ReviewID: "action", Kind: "action"}}
	m = applyAll(m, ask)
	hook := guardrailTestHook("action", "operational_failure", "unresolved", "ask_action")
	hook.Guardrail.Job = "action"
	m = applyAll(m, hook)
	s := approvalSurfaceOf(t, m)
	body, _ := s.permissionModalBodyParts(100, 80)
	if m.conv.scrollback.Len() != 0 || !strings.Contains(stripANSIstr(body), "outage") {
		t.Fatalf("active prompt did not own hook: %q", body)
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	before := m.conv.scrollback.SnapshotAt(0)
	text := before.Payload.(scrollback.NoticeCardSnapshot).Text
	if !strings.Contains(text, "outage") || strings.Contains(text, "Action paused") {
		t.Fatalf("resolved action lost warning or retained stale outcome: %q", text)
	}
	m = applyAll(m, hook)
	if got := m.conv.scrollback.SnapshotAt(0); got != before {
		t.Fatalf("repeated pending hook overwrote resolution: %+v", got)
	}
}

func TestGuardrailSessionReadyInvalidatesPendingDetails(t *testing.T) {
	for _, approval := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("approval=%v/failed=%v", approval, failed), func(t *testing.T) {
				r := &guardrailDetailRecorder{}
				if failed {
					r.err = errors.New("old session failure")
				}
				m := guardrailTestModel(t, r, false)
				var event tea.Msg = guardrailTestHook("same-review", "complete", "unresolved", "pass_advisory")
				if approval {
					event = client.PermissionAskMsg{AskID: "ask", Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: "same-review", Kind: "result_release"}}
				}
				old := updateGuardrail(&m, event)[0]
				m = applyAll(m, client.SessionReadyMsg{SessionID: "other"}, old)
				if approvalSurfaceFor(&m) != nil {
					t.Fatal("old prompt survived SessionReady")
				}
				// Returning to the old ID must not revive its old request token.
				m = applyAll(m, client.SessionReadyMsg{SessionID: "session"}, old)
				for i := range m.conv.scrollback.Len() {
					if m.conv.scrollback.SnapshotAt(i).Revision != 0 {
						t.Fatal("old session reply mutated retained transcript")
					}
				}
				replies := updateGuardrail(&m, event)
				if len(replies) != 1 {
					t.Fatalf("new session reused stale request state: %d requests", len(replies))
				}
				current := replies[0]
				m = applyAll(m, old)
				if approval {
					s := approvalSurfaceOf(t, m)
					if s.ask.unavailable || s.ask.detail.Concern != "" {
						t.Fatal("old reply affected new prompt")
					}
				}
				m = applyAll(m, current)
				if approval {
					s := approvalSurfaceOf(t, m)
					if s.ask.unavailable != failed || (!failed && s.ask.detail.Concern == "") {
						t.Fatal("current prompt detail not applied")
					}
				} else if text := lastNotice(m); !strings.Contains(text, "Test explanation") && !strings.Contains(text, "unavailable or expired") {
					t.Fatalf("current warning detail missing: %q", text)
				}
			})
		}
	}
}

func TestGuardrailApprovalPurposeAndScope(t *testing.T) {
	for _, tc := range []struct {
		kind                 string
		repeat               bool
		title, action, scope string
	}{
		{"action", false, "Allow this action?", "Run once", "Approval for repeated actions is unavailable"},
		{"action", true, "Allow this action?", "Run once", "this exact action in this session"},
		{"result_release", false, "Share this tool result?", "Release once", "side effects are not run again"},
	} {
		m := guardrailTestModel(t, &guardrailDetailRecorder{}, false)
		m = applyAll(m, client.PermissionAskMsg{AskID: "ask", Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: "review", Kind: tc.kind, RepeatAvailable: tc.repeat}})
		s := approvalSurfaceOf(t, m)
		body, _ := s.permissionModalBodyParts(100, 80)
		text := strings.Join(strings.Fields(stripANSIstr(body)), " ")
		if !strings.Contains(text, tc.title) || !strings.Contains(text, tc.scope) || strings.Count(text, tc.action) != 1 || strings.Count(text, "Cancel") != 1 {
			t.Fatalf("approval purpose/scope or redundant choices: %q", text)
		}
	}
}

func TestGuardrailRetractedReceiptKeepsReviewIdentity(t *testing.T) {
	for _, retract := range []string{"head", "queued"} {
		m := guardrailTestModel(t, &guardrailDetailRecorder{}, false)
		ask := func(id string) client.PermissionAskMsg {
			return client.PermissionAskMsg{AskID: "session:1:" + id, Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: id, Kind: "result_release"}}
		}
		m = applyAll(m, ask("head"), ask("queued"), client.PermissionRetractMsg{AskID: ask(retract).AskID})
		before := m.conv.scrollback.SnapshotAt(0)
		m = applyAll(m, guardrailTestHook(retract, "operational_failure", "unresolved", "deny"))
		after := m.conv.scrollback.SnapshotAt(0)
		if m.conv.scrollback.Len() != 1 || after.ID != before.ID || !strings.Contains(after.Payload.(scrollback.NoticeCardSnapshot).Text, "Result withheld") {
			t.Fatalf("retracted %s lost its receipt: %+v", retract, after)
		}
		if approvalSurfaceOf(t, m).ask.guardrail.ReviewID == retract {
			t.Fatal("retracted prompt stayed active")
		}
	}
}

func TestGuardrailQueuedFailureStaysWithPromotedPrompt(t *testing.T) {
	r := &guardrailDetailRecorder{}
	m := guardrailTestModel(t, r, false)
	ask := func(id string) client.PermissionAskMsg {
		return client.PermissionAskMsg{AskID: "session:1:" + id, Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: id, Kind: "result_release"}}
	}
	head := updateGuardrail(&m, ask("head"))[0]
	queued := updateGuardrail(&m, ask("queued"))[0]
	failure := queued
	failure.Err = errors.New("private queued failure")
	m = applyAll(m, head, failure)
	s := approvalSurfaceOf(t, m)
	body, _ := s.permissionModalBodyParts(100, 80)
	if s.ask.unavailable || !s.queue[0].unavailable || strings.Contains(stripANSIstr(body), "unavailable or expired") {
		t.Fatal("queued error leaked into head prompt")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter}, queued, head)
	head.Err = errors.New("late head failure")
	m = applyAll(m, head)
	s = approvalSurfaceOf(t, m)
	body, _ = s.permissionModalBodyParts(100, 80)
	if s.ask.guardrail.ReviewID != "queued" || !s.ask.unavailable || s.ask.detail.Concern != "" || !strings.Contains(stripANSIstr(body), "unavailable or expired") {
		t.Fatalf("promoted prompt changed by stale reply: %q", body)
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	count := m.conv.scrollback.Len()
	m = applyAll(m, queued, failure, head)
	if m.conv.scrollback.Len() != count {
		t.Fatal("resolved queued replies created detached notices")
	}
}

func TestGuardrailDebugWarningReplayAndMetadataSanitization(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		m := guardrailTestModel(t, &guardrailDetailRecorder{}, true)
		replay := m.newSessionsSurface(false)
		m.closeModal()
		hook := guardrailTestHook("review", "operational_failure", "unresolved", "continue_warning")
		if corrupt {
			payload := "\x1b[2J\a\x1b]52;c;dGVzdA==\a"
			hook.Phase += payload
			hook.Guardrail.Job += payload
			hook.Guardrail.Inspection += payload
			hook.Guardrail.Assessment += payload
			hook.Guardrail.Disposition += payload
			hook.Guardrail.CheckerProviderID += payload
			hook.Guardrail.CheckerModelID += payload
		}
		m = applyAll(m, hook)
		replay.applyReplayEvent(hook)
		live := m.conv.scrollback.SnapshotAt(0).Payload.(scrollback.NoticeCardSnapshot).Text
		stored := replay.transcript.scrollback.SnapshotAt(0).Payload.(scrollback.NoticeCardSnapshot).Text
		if live != stored {
			t.Fatalf("debug warning differs: live=%q replay=%q", live, stored)
		}
		if strings.ContainsAny(live, "\x1b\a") {
			t.Fatalf("unsafe debug metadata: %q", live)
		}
		for _, field := range []string{"PostToolUse", "inbound", "operational_failure", "unresolved", "continue_warning", "provider", "model"} {
			if !strings.Contains(live, field) {
				t.Fatalf("missing debug field %q: %q", field, live)
			}
		}
		if !corrupt && (!strings.Contains(live, "outage") || !strings.Contains(live, "Work continued")) {
			t.Fatalf("warning outcome lost: %q", live)
		}
	}
}

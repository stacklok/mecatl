package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func hookCards(c *conversation) (tools []scrollback.ToolCardSnapshot, notices []scrollback.HookCardSnapshot) {
	for i := 0; i < c.scrollback.Len(); i++ {
		switch p := c.scrollback.SnapshotAt(i).Payload.(type) {
		case scrollback.ToolCardSnapshot:
			tools = append(tools, p)
		case scrollback.HookCardSnapshot:
			notices = append(notices, p)
		}
	}
	return
}

func TestCorrelatedHookVisibilityRemainsNoticeOwned(t *testing.T) {
	for _, correlated := range []bool{false, true} {
		for _, showBenign := range []bool{false, true} {
			m := quietTestModel(t, nil, showBenign)
			callID := ""
			if correlated {
				callID = "call"
				m = applyAll(m, client.ToolCallMsg{ID: callID, Name: "Read"})
			}
			seq := int64(0)
			add := func(h client.HookMsg) {
				seq++
				h.CallID, h.RunID, h.Seq = callID, "run", seq
				m = applyAll(m, h)
			}
			for _, phase := range []string{"PreToolUse", "PostToolUse"} {
				for _, generic := range []struct {
					text     string
					decision client.HookDecision
				}{
					{"generic info evidence", client.HookInfo},
					{"generic modified evidence", client.HookModified},
					{"generic blocked evidence", client.HookBlocked},
					{"generic advisory evidence", client.HookAdvisory},
				} {
					add(client.HookMsg{Phase: phase, Tool: "Read", Text: generic.text + " (" + phase + ")", Decision: generic.decision})
				}
			}
			for _, disposition := range []string{"pass_advisory", "deny", "withhold_result", "future_value", "execute"} {
				review := benignReview(disposition, disposition)
				if disposition == "deny" {
					review.Job = "action"
				}
				if disposition != "execute" {
					review.Assessment = "unresolved"
				}
				add(client.HookMsg{Tool: "Read", Guardrail: review})
			}
			frame := frameText(&m)
			for _, phase := range []string{"PreToolUse", "PostToolUse"} {
				for _, decision := range []string{"info", "modified", "blocked", "advisory"} {
					visible := "generic " + decision + " evidence (" + phase + ")"
					if !strings.Contains(frame, visible) {
						t.Errorf("correlated=%v show=%v missing %q: %q", correlated, showBenign, visible, frame)
					}
				}
			}
			for _, visible := range []string{"warning", "Action stopped", "Result withheld", "outcome is unknown"} {
				if !strings.Contains(frame, visible) {
					t.Errorf("correlated=%v show=%v missing %q: %q", correlated, showBenign, visible, frame)
				}
			}
			if strings.Contains(frame, benignSummary) != showBenign {
				t.Fatalf("correlated=%v show=%v benign visibility: %q", correlated, showBenign, frame)
			}
			if correlated {
				tools, _ := hookCards(&m.conv)
				if len(tools) != 1 || len(tools[0].Hooks) != 13 {
					t.Fatalf("correlated attachment: %+v", tools)
				}
			}
			m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyF9})
			if !strings.Contains(frameText(&m), benignSummary) {
				t.Fatal("F9 did not expose retained benign notice")
			}
		}
	}
}

func TestHookAttachmentIsDataOnlyAcrossToolPresentations(t *testing.T) {
	m := quietTestModel(t, nil, false)
	m = applyAll(m, client.ToolCallMsg{ID: "first", Name: "Read", Args: `{"path":"first"}`}, client.ToolCallMsg{ID: "second", Name: "Read", Args: `{"path":"second"}`})
	model, _ := m.runToolcalls()
	m = model.(Model)
	s := toolcallsForTest(t, m)
	s.listFollow = false
	s.selected = 0
	beforeID := s.entries[0].blockID
	before, _ := s.Render(120, 20)
	m = applyAll(m, client.HookMsg{CallID: "first", RunID: "run", Seq: 1, Phase: "PreToolUse", Tool: "Read", Text: "attachment-only evidence", Decision: client.HookBlocked})
	if s.entries[s.selected].blockID != beforeID || len(s.entries) != 2 || s.entries[0].revision == 0 {
		t.Fatalf("attachment lost selection or revision: %+v", s.entries)
	}
	if got, _ := s.Render(120, 20); got != before {
		t.Fatalf("hook changed toolcalls list: before=%q after=%q", stripANSIstr(before), stripANSIstr(got))
	}
	s.detail = true
	s.refreshDetail(&m.conv.scrollback)
	if detail, _ := s.Render(120, 20); strings.Contains(stripANSIstr(detail), "attachment-only evidence") {
		t.Fatalf("hook leaked into toolcalls detail: %q", stripANSIstr(detail))
	}
	if frame := frameText(&m); strings.Count(frame, "attachment-only evidence") != 1 {
		t.Fatalf("hook not shown exactly once as standalone notice: %q", frame)
	}
	m = applyAll(m, client.ToolResultMsg{CallID: "first", Content: "done"})
	if s.entries[s.selected].blockID != beforeID || len(s.entries) != 2 {
		t.Fatalf("result changed inspector selection: %+v", s.entries)
	}
}

func TestHookPendingAttachmentPreservesSourceOrder(t *testing.T) {
	for _, replay := range []bool{false, true} {
		m := guardrailTestModel(t, nil, false)
		s := m.newSessionsSurface(false)
		apply := func(msg tea.Msg) {
			if replay {
				s.applyReplayEvent(msg)
			} else {
				m = applyAll(m, msg)
			}
		}
		c := &m.conv
		if replay {
			c = &s.transcript
		}
		// An ask reserves a review ID before its source event, so allocation order
		// differs from arrival order. All three hook sources precede the call.
		if !replay {
			apply(client.PermissionAskMsg{AskID: "session:1:call", CallID: "call", Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: "action", Kind: "action"}})
		}
		inbound := guardrailTestHook("inbound", "complete", "unresolved", "withhold_result")
		inbound.CallID, inbound.RunID, inbound.Seq = "call", "run", 1
		action := guardrailTestHook("action", "complete", "unresolved", "ask_action")
		action.CallID, action.RunID, action.Seq = "call", "run", 2
		generic := client.HookMsg{CallID: "call", RunID: "run", Seq: 3, Text: "generic", Decision: client.HookBlocked}
		apply(inbound)
		apply(action)
		apply(generic)
		apply(inbound) // identical delivery must not move its slot
		if len(c.pendingHooks) != 3 {
			t.Fatalf("replay=%v pending: %v", replay, c.pendingHooks)
		}
		apply(client.ToolCallMsg{ID: "call", Name: "Read"})
		tools, _ := hookCards(c)
		if len(tools) != 1 || len(tools[0].Hooks) != 3 || tools[0].Hooks[0].Review.ReviewID != "inbound" || tools[0].Hooks[1].Review.ReviewID != "action" || tools[0].Hooks[2].Text != "generic" || len(c.pendingHooks) != 0 {
			t.Fatalf("replay=%v order: %+v pending=%v", replay, tools, c.pendingHooks)
		}
	}
}

func TestHookCorrelationLiveAndReplay(t *testing.T) {
	for _, replay := range []bool{false, true} {
		for _, beforeCall := range []bool{false, true} {
			m := guardrailTestModel(t, nil, false)
			s := m.newSessionsSurface(false)
			apply := func(msg tea.Msg) {
				if replay {
					s.applyReplayEvent(msg)
				} else {
					m = applyAll(m, msg)
				}
			}
			c := &m.conv
			if replay {
				c = &s.transcript
			}
			hook := client.HookMsg{CallID: "second", RunID: "run", Seq: 1, Tool: "Read", Phase: "PreToolUse", Text: "modified", Decision: "modified"}
			apply(client.ToolCallMsg{ID: "first", Name: "Read"})
			if !beforeCall {
				apply(client.ToolCallMsg{ID: "second", Name: "Read"})
				apply(client.ToolResultMsg{CallID: "second", Content: "done"})
			}
			apply(hook)
			apply(hook)
			if beforeCall {
				if len(c.pendingHooks) != 1 {
					t.Fatalf("replay=%v pending=%v", replay, c.pendingHooks)
				}
				apply(client.ToolCallMsg{ID: "second", Name: "Read"})
			}
			tools, notices := hookCards(c)
			if len(tools) != 2 || len(tools[0].Hooks) != 0 || len(tools[1].Hooks) != 1 || tools[1].Hooks[0].Text != "modified" || len(notices) != 1 || len(c.pendingHooks) != 0 {
				t.Fatalf("replay=%v before=%v: tools=%+v notices=%+v pending=%v", replay, beforeCall, tools, notices, c.pendingHooks)
			}
			// Text equality is not an event identity; distinct and identity-less evidence survives.
			hook.Seq = 2
			apply(hook)
			hook.RunID = ""
			apply(hook)
			apply(hook)
			tools, notices = hookCards(c)
			if len(tools[1].Hooks) != 4 || len(notices) != 4 {
				t.Fatalf("distinct identical hooks collapsed: %+v %+v", tools[1].Hooks, notices)
			}
		}
	}
}

func TestHookCorrelationRejectedEvidenceAndPendingLifetime(t *testing.T) {
	for _, replay := range []bool{false, true} {
		m := guardrailTestModel(t, nil, false)
		s := m.newSessionsSurface(false)
		apply := func(msg tea.Msg) {
			if replay {
				s.applyReplayEvent(msg)
			} else {
				m = applyAll(m, msg)
			}
		}
		c := &m.conv
		if replay {
			c = &s.transcript
		}
		apply(client.ToolCallMsg{ID: "same", Name: "Read"})
		apply(client.ToolCallMsg{ID: "same", Name: "Read"})
		for i, id := range []string{"", "unknown", "same", "later"} {
			apply(client.HookMsg{CallID: id, RunID: "run", Seq: int64(i + 1), Text: "evidence"})
		}
		tools, notices := hookCards(c)
		if len(notices) != 4 || len(tools[0].Hooks) != 0 || len(tools[1].Hooks) != 0 || len(c.pendingHooks) != 2 {
			t.Fatalf("replay=%v rejected/pending evidence: tools=%+v notices=%+v pending=%v", replay, tools, notices, c.pendingHooks)
		}
		apply(client.ToolCallMsg{ID: "later", Name: "Read"})
		tools, notices = hookCards(c)
		if len(tools[2].Hooks) != 1 || len(notices) != 4 || len(c.pendingHooks) != 1 {
			t.Fatalf("replay=%v late attachment: %+v %+v %v", replay, tools, notices, c.pendingHooks)
		}
		apply(client.ResultMsg{})
		if len(c.pendingHooks) != 0 {
			t.Fatalf("replay=%v pending after terminal: %v", replay, c.pendingHooks)
		}
		apply(client.ToolCallMsg{ID: "unknown", Name: "Read"})
		tools, notices = hookCards(c)
		if len(tools[3].Hooks) != 0 || len(notices) != 4 {
			t.Fatalf("replay=%v stale pending attached or notice lost: %+v %+v", replay, tools, notices)
		}
	}
}

func TestHookPendingClearsOnLiveErrorCancelAndSessionReplacement(t *testing.T) {
	for _, end := range []tea.Msg{client.ResultMsg{Stop: stopError}, client.StreamErrMsg{Err: errors.New("stream failed")}, client.StreamClosedMsg{}} {
		m := guardrailTestModel(t, nil, false)
		m.phase = phaseRunning
		m = applyAll(m, client.HookMsg{CallID: "late", RunID: "run", Seq: 1, Text: "evidence"})
		if len(m.conv.pendingHooks) != 1 {
			t.Fatal("missing candidate")
		}
		m = applyAll(m, end)
		if len(m.conv.pendingHooks) != 0 {
			t.Fatalf("pending after %T: %v", end, m.conv.pendingHooks)
		}
	}
	m := guardrailTestModel(t, nil, false)
	m = applyAll(m, client.HookMsg{CallID: "late", Text: "evidence"})
	m = m.resetSession()
	if len(m.conv.pendingHooks) != 0 {
		t.Fatal("session replacement kept pending candidate")
	}
}

func TestHookReplayReceiptsRemainStandaloneAndEndDropsPending(t *testing.T) {
	for _, withAsk := range []bool{false, true} {
		for _, end := range []tea.Msg{client.ResultMsg{Stop: stopError}, client.StreamErrMsg{Err: errors.New("stream failed")}, client.StreamClosedMsg{}} {
			m := guardrailTestModel(t, nil, false)
			s := m.newSessionsSurface(false)
			s.applyReplayEvent(client.HookMsg{CallID: "later", RunID: "run", Seq: 1, Text: "evidence"})
			if withAsk {
				s.applyReplayEvent(client.PermissionAskMsg{AskID: "child:1:later", CallID: "later", Guardrail: &client.GuardrailApprovalScope{ReviewID: "historical-review", Kind: "action"}})
			}
			s.applyReplayEvent(client.ApprovalMsg{AskID: "child:1:later"})
			if len(s.transcript.pendingHooks) != 1 || len(s.transcript.guardrailReviews) != 0 || s.transcript.scrollback.Len() != 2 {
				t.Fatalf("withAsk=%v historical receipt inferred a join: %+v", withAsk, s.transcript)
			}
			s.applyReplayEvent(end)
			s.applyReplayEvent(client.ToolCallMsg{ID: "later", Name: "Read"})
			tools, notices := hookCards(&s.transcript)
			if len(s.transcript.pendingHooks) != 0 || len(tools) != 1 || len(tools[0].Hooks) != 0 || len(notices) != 1 {
				t.Fatalf("withAsk=%v %T: historical receipt or expired hook joined call: %+v %+v", withAsk, end, tools, notices)
			}
		}
	}
}

func TestHookRecoveryReconciledCallAttachesPendingOnce(t *testing.T) {
	var c conversation
	// Recovery reconciles a snapshot call rather than appending a new one.
	c.addGuardrailHook(client.HookMsg{CallID: "call", RunID: "run", Seq: 1, Text: "pre-call"}, false)
	c.scrollback.Tools().Add(scrollback.ToolCall{ID: "call", Name: "Read"})
	if !c.reconcileUnresolvedTool("call", "Read", `{}`) {
		t.Fatal("recovery call did not reconcile")
	}
	tools, notices := hookCards(&c)
	if len(tools) != 1 || len(tools[0].Hooks) != 1 || len(notices) != 1 || len(c.pendingHooks) != 0 {
		t.Fatalf("recovery attachment: %+v %+v %v", tools, notices, c.pendingHooks)
	}
}

func TestHookReviewCorrelationAcrossResultAndQueuedApproval(t *testing.T) {
	m := guardrailTestModel(t, nil, false)
	for _, id := range []string{"first", "second"} {
		m = applyAll(m, client.PermissionAskMsg{AskID: "session:1:" + id, CallID: id, Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: id, Kind: "result_release"}})
	}
	s := approvalSurfaceOf(t, m)
	m = applyAll(m, client.ToolCallMsg{ID: "first", Name: "Read"}, client.ToolCallMsg{ID: "second", Name: "Read"})
	m = applyAll(m, client.ToolResultMsg{CallID: "second", Content: "result"})
	for i, id := range []string{"second", "first"} {
		h := guardrailTestHook(id, "complete", "unresolved", "withhold_result")
		h.CallID, h.RunID, h.Seq = id, "run", int64(i+1)
		m = applyAll(m, h)
	}
	tools, _ := hookCards(&m.conv)
	if len(tools) != 2 || len(tools[0].Hooks) != 1 || len(tools[1].Hooks) != 1 || tools[0].Hooks[0].ID != s.ask.review.hookID || tools[1].Hooks[0].ID != s.queue[0].review.hookID || s.ask.Reason == "" || s.queue[0].Reason == "" {
		t.Fatalf("queued/inbound identity lost: %+v active=%+v queued=%+v", tools, s.ask, s.queue)
	}
}

func TestHookConflictDoesNotChangeActiveApproval(t *testing.T) {
	m := guardrailTestModel(t, nil, false)
	ask := client.PermissionAskMsg{AskID: "session:1:call", CallID: "call", Tool: "Read", Guardrail: &client.GuardrailApprovalScope{ReviewID: "review", Kind: "action"}}
	m = applyAll(m, client.ToolCallMsg{ID: "call", Name: "Read"}, ask)
	hook := guardrailTestHook("review", "operational_failure", "unresolved", "ask_action")
	hook.CallID, hook.RunID, hook.Seq = "call", "run", 1
	m = applyAll(m, hook)
	s := approvalSurfaceOf(t, m)
	r := s.ask.review
	reason := s.ask.Reason
	hook.Text = "contradictory evidence"
	m = applyAll(m, hook)
	conflicts := m.conv.scrollback.Len()
	m = applyAll(m, hook)
	if m.conv.scrollback.Len() != conflicts {
		t.Fatal("identical conflicting delivery appended another notice")
	}
	if r.snapshot().Text == hook.Text || s.ask.Reason != reason || strings.Contains(lastNotice(m), hook.Text) == false {
		t.Fatalf("conflict overwrote approval or disappeared: record=%+v reason=%q notice=%q", r.snapshot(), s.ask.Reason, lastNotice(m))
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if r.snapshot().Detail.Receipt == "" {
		t.Fatal("conflict lost approval receipt")
	}
}

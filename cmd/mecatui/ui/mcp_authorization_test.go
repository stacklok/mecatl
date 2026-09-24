package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func TestInvariant_mecatui_mcp_authorization_is_not_permission_approval(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m = applyAll(m, client.MCPAuthorizationMsg{AuthorizationID: "auth-1", CallID: "call-1", Status: "pending"})
	if m.phase != phaseAuthorizing {
		t.Fatalf("phase = %v, want authorizing", m.phase)
	}
	view := m.View().Content
	for _, forbidden := range []string{"Allow", "Always", "Deny"} {
		if strings.Contains(view, forbidden) {
			t.Fatalf("authorization view exposed permission action %q: %s", forbidden, view)
		}
	}
	for _, required := range []string{"Complete connection", "Copy Link", "checked automatically", "Cancel"} {
		if !strings.Contains(view, required) {
			t.Fatalf("authorization view missing %q: %s", required, view)
		}
	}
	if strings.Contains(view, "Recheck") {
		t.Fatalf("authorization view retained manual recheck: %s", view)
	}
	if strings.Contains(view, "Refresh") {
		t.Fatalf("authorization view retained manual refresh: %s", view)
	}
}

func TestMCPAuthorizationDisplayNameIsSanitizedAndShown(t *testing.T) {
	msg := client.MCPAuthorizationMsg{
		AuthorizationID: "authorization-secret",
		DisplayName:     "GitHub\nEnterprise\x1b",
		CallID:          "call-1",
		Status:          "pending",
	}
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m = applyAll(m, msg)
	if m.authorization.displayName != "GitHub Enterprise" {
		t.Fatalf("stored display name = %q", m.authorization.displayName)
	}
	view := stripANSIstr(m.View().Content)
	if !strings.Contains(view, "GitHub Enterprise") || strings.Contains(view, "authorization-secret") {
		t.Fatalf("authorization card exposed unsafe or private text: %q", view)
	}
	notice := mcpAuthorizationNotice(msg)
	if !strings.Contains(notice, "GitHub Enterprise") || strings.Contains(notice, "authorization-secret") || strings.Contains(notice, "\n") || strings.Contains(notice, "\x1b") {
		t.Fatalf("authorization notice exposed unsafe or private text: %q", notice)
	}
}

func TestSessionMCPAuthorization_Scenario9_MecatuiReconnect(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m = applyAll(m, client.MCPAuthorizationMsg{AuthorizationID: "auth-1", CallID: "call-1", Status: "pending"})
	m = applyAll(m, client.MCPAuthorizationMsg{AuthorizationID: "auth-1", CallID: "call-1", Status: "pending"})
	if m.phase != phaseAuthorizing || m.authorization.authorizationID != "auth-1" {
		t.Fatalf("replayed safe status did not restore authorization state: %+v", m.authorization)
	}
}

// TestSessionMCPAuthorization_Scenario9_MecatuiControlStreamCleanup proves a
// completed recheck/cancel stream relinquishes its channel. Replayed status only
// restores the card; it never invokes the browser opener.
func TestSessionMCPAuthorization_Scenario9_MecatuiControlStreamCleanup(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	ch := make(chan tea.Msg)
	close(ch)
	m.sessionID = "session-1"
	m.authorization = mcpAuthorizationState{authorizationID: "auth-1", controlGen: 1}
	m.authorizationEvents = ch

	msg := m.waitMCPAuthorizationEvent("session-1", "auth-1", 1)()
	mm, _ := m.Update(msg)
	updated := mm.(Model)
	if updated.authorizationEvents != nil {
		t.Fatal("authorization event channel was not cleared")
	}
}

func TestMCPAuthorizationPollCompletesWithoutManualRecheck(t *testing.T) {
	control := &mcpAuthorizationControllerFake{}
	m := New(Deps{Ctx: context.Background(), MCPAuthorization: control})
	m.sessionID = "session-1"
	m = applyAll(m, client.MCPAuthorizationMsg{AuthorizationID: "auth-1", Status: mcpAuthorizationStatusPending})
	gen := m.authorization.controlGen
	m = applyAll(m, mcpAuthorizationActionMsg{sessionID: "session-1", authorizationID: "auth-1", gen: gen, text: "authorization page opened"})

	mm, cmd := m.applyMCPAuthorizationPollTick(mcpAuthorizationPollTickMsg{sessionID: "session-1", authorizationID: "auth-1", gen: gen})
	m = mm.(Model)
	if cmd == nil || !m.authorization.pollBusy || m.authorization.controlGen == gen {
		t.Fatal("automatic authorization poll did not start versioned control")
	}
	if _, duplicate := m.applyMCPAuthorizationPollTick(mcpAuthorizationPollTickMsg{sessionID: "session-1", authorizationID: "auth-1", gen: m.authorization.controlGen}); duplicate != nil {
		t.Fatal("duplicate authorization tick overlapped the in-flight control")
	}
	if m.authorization.controlGen == gen {
		t.Fatal("automatic authorization poll did not advance control generation")
	}
	m = applyAll(m, client.MCPAuthorizationMsg{AuthorizationID: "auth-1", Status: "granted"})
	if m.phase != phaseRunning || m.authorization.polling {
		t.Fatalf("automatic grant did not continue the current flow: phase=%v polling=%t", m.phase, m.authorization.polling)
	}
}

func TestMCPAuthorizationPollIgnoresStaleAndCancelledControls(t *testing.T) {
	m := New(Deps{Ctx: context.Background(), MCPAuthorization: &mcpAuthorizationControllerFake{}})
	m.sessionID = "session-current"
	m.authorization = mcpAuthorizationState{authorizationID: "auth-current", controlGen: 9, polling: true}
	m.phase = phaseAuthorizing
	for _, tick := range []mcpAuthorizationPollTickMsg{
		{sessionID: "session-old", authorizationID: "auth-current", gen: 9},
		{sessionID: "session-current", authorizationID: "auth-old", gen: 9},
		{sessionID: "session-current", authorizationID: "auth-current", gen: 8},
	} {
		if _, cmd := m.applyMCPAuthorizationPollTick(tick); cmd != nil {
			t.Fatalf("stale poll tick %#v started control", tick)
		}
	}
	m.authorization.polling = false // explicit cancel invalidates all scheduled ticks.
	if _, cmd := m.applyMCPAuthorizationPollTick(mcpAuthorizationPollTickMsg{sessionID: "session-current", authorizationID: "auth-current", gen: 9}); cmd != nil {
		t.Fatal("cancelled authorization continued polling")
	}
}

// TestMCPAuthorizationPollSelfHealsAfterLostStreamSignal reproduces a live
// stall: a poll's recheck opens a stream that never delivers an event and
// never closes (a dropped stream-close frame over a flaky tunnel is
// indistinguishable from this at the client). Without a bounded wait for the
// first event, pollBusy stays true forever and applyMCPAuthorizationPollTick's
// early-return path never reschedules — polling dies silently until something
// unrelated happens to reset state. The first-event watchdog must cancel the
// call, which surfaces as an ordinary stream error that resets pollBusy and
// reschedules the next tick.
func TestMCPAuthorizationPollSelfHealsAfterLostStreamSignal(t *testing.T) {
	orig := mcpAuthorizationFirstEventTimeout
	mcpAuthorizationFirstEventTimeout = 20 * time.Millisecond
	t.Cleanup(func() { mcpAuthorizationFirstEventTimeout = orig })

	control := &mcpAuthorizationControllerFake{blockOnCtx: true}
	m := New(Deps{Ctx: context.Background(), MCPAuthorization: control})
	m.sessionID = "session-1"
	m = applyAll(m, client.MCPAuthorizationMsg{AuthorizationID: "auth-1", Status: mcpAuthorizationStatusPending})
	gen := m.authorization.controlGen
	m = applyAll(m, mcpAuthorizationActionMsg{sessionID: "session-1", authorizationID: "auth-1", gen: gen, text: "authorization page opened"})

	mm, cmd := m.applyMCPAuthorizationPollTick(mcpAuthorizationPollTickMsg{sessionID: "session-1", authorizationID: "auth-1", gen: gen})
	m = mm.(Model)
	if !m.authorization.pollBusy || m.authorization.firstEventTimer == nil {
		t.Fatal("poll tick did not mark busy and arm the first-event watchdog")
	}

	// Run only the control-start leaf of the batch — the other leaf is the
	// self-perpetuating 3s reschedule tick, which must not fire here.
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatal("poll tick did not return a batched command")
	}
	streamMsg, ok := batch[0]().(mcpAuthorizationStreamMsg)
	if !ok {
		t.Fatal("control leaf did not open a stream")
	}
	m = applyAll(m, streamMsg)
	if m.authorizationEvents == nil {
		t.Fatal("stream open did not arm event reading")
	}

	waitCmd := m.waitMCPAuthorizationEvent(m.sessionID, m.authorization.authorizationID, m.authorization.controlGen)
	gotMsg := make(chan tea.Msg, 1)
	go func() { gotMsg <- waitCmd() }()

	var msg tea.Msg
	select {
	case msg = <-gotMsg:
	case <-time.After(2 * time.Second):
		t.Fatal("first-event watchdog never unblocked the stalled stream")
	}

	mm2, cmd2, handled := m.updateMCPAuthorizationMsg(msg)
	m = mm2.(Model)
	if !handled {
		t.Fatal("watchdog-triggered stream error was not handled")
	}
	if m.authorization.pollBusy {
		t.Fatal("pollBusy was not reset after the watchdog fired")
	}
	if m.authorization.firstEventTimer != nil {
		t.Fatal("first-event watchdog was not cleared once it fired")
	}
	if cmd2 == nil {
		t.Fatal("watchdog recovery did not reschedule the next poll tick")
	}
}

func TestMCPAuthorizationOpeningOrCopyingStartsPolling(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{name: "open", key: tea.KeyPressMsg{Code: tea.KeyEnter}},
		{name: "copy", key: tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				control := &mcpAuthorizationControllerFake{}
				m := New(Deps{
					Ctx:              t.Context(),
					MCPAuthorization: control,
					OpenURL:          func(context.Context, string) error { return nil },
					Clipboard:        &fakeClipboard{},
				})
				m.sessionID = "session-1"
				m = applyAll(m, client.MCPAuthorizationMsg{AuthorizationID: "auth-1", Status: mcpAuthorizationStatusPending})
				_, actionCmd := m.onMCPAuthorizationKey(tc.key)
				m = applyAll(m, actionCmd())
				gen := m.authorization.controlGen
				if !m.authorization.polling {
					t.Fatal("successful presentation did not arm polling")
				}
				mm, pollCmd := m.applyMCPAuthorizationPollTick(mcpAuthorizationPollTickMsg{sessionID: "session-1", authorizationID: "auth-1", gen: gen})
				m = mm.(Model)
				if pollCmd == nil {
					t.Fatal("presentation did not start an authorization observation")
				}
				runBatchLeaves(pollCmd)
				if control.recheck != 1 {
					t.Fatalf("rechecks = %d, want 1", control.recheck)
				}
			})
		})
	}
}

func TestMCPAuthorizationCancelInvalidatesScheduledPoll(t *testing.T) {
	m := New(Deps{Ctx: t.Context(), MCPAuthorization: &mcpAuthorizationControllerFake{}})
	m.sessionID = "session-1"
	m.authorization = mcpAuthorizationState{authorizationID: "auth-1", controlGen: 4, polling: true}
	m.phase = phaseAuthorizing

	mm, cancelCmd := m.onMCPAuthorizationKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = mm.(Model)
	if cancelCmd == nil || m.authorization.polling || m.authorization.controlGen != 5 {
		t.Fatalf("cancel did not supersede polling control: %+v", m.authorization)
	}
	if _, cmd := m.applyMCPAuthorizationPollTick(mcpAuthorizationPollTickMsg{sessionID: "session-1", authorizationID: "auth-1", gen: 4}); cmd != nil {
		t.Fatal("stale poll survived explicit cancellation")
	}
}

func TestMCPAuthorizationQueuedPresentationIsCancelledBeforeOpen(t *testing.T) {
	for _, supersede := range []bool{false, true} {
		t.Run(fmt.Sprintf("supersede=%t", supersede), func(t *testing.T) {
			control := &mcpAuthorizationControllerFake{}
			opened := 0
			m := New(Deps{
				Ctx:              t.Context(),
				MCPAuthorization: control,
				OpenURL:          func(context.Context, string) error { opened++; return nil },
			})
			m.sessionID = "session-1"
			m.authorization = mcpAuthorizationState{authorizationID: "auth-1", controlGen: 4}
			mm, presentationCmd := m.startMCPAuthorizationPresentation(false)
			m = mm.(Model)
			if supersede {
				mm, _ = m.startMCPAuthorizationControl(true)
				m = mm.(Model)
			} else {
				m = m.resetSessionDerived()
			}
			_ = presentationCmd()
			if opened != 0 {
				t.Fatalf("stale presentation opened browser %d times", opened)
			}
		})
	}
}

type mcpAuthorizationControllerFake struct {
	presentation, recheck, cancel int
	presentationErr, recheckErr   error
	controlStream                 *client.EventStream
	blockOnCtx                    bool // ignore controlStream; return a stream whose Recv blocks until ctx is cancelled.
}

func (f *mcpAuthorizationControllerFake) MCPAuthorizationPresentation(context.Context, string, string) (string, error) {
	f.presentation++
	return "https://authorization.example/", f.presentationErr
}

func (f *mcpAuthorizationControllerFake) RecheckMCPAuthorization(ctx context.Context, _, _ string) (*client.EventStream, error) {
	f.recheck++
	if f.blockOnCtx {
		return client.NewBlockedEventStream(ctx), nil
	}
	return f.controlStream, f.recheckErr
}

func (f *mcpAuthorizationControllerFake) CancelMCPAuthorization(context.Context, string, string) (*client.EventStream, error) {
	f.cancel++
	return nil, nil
}

type authorizationControlRecorder struct {
	askID         string
	verdict       client.Verdict
	guardrail     *client.GuardrailApprovalScope
	expectedRunID string
	err           error
	cancelled     bool
}

func (r *authorizationControlRecorder) SendApprovalForScope(id string, verdict client.Verdict, scope *client.GuardrailApprovalScope, expectedRunID string) error {
	r.askID, r.verdict, r.guardrail, r.expectedRunID = id, verdict, scope, expectedRunID
	return r.err
}
func (r *authorizationControlRecorder) SendCancel() error {
	r.cancelled = true
	return nil
}

func TestControlRefusedWireRoundTripRestoresCorrelatedAskAndPreservesQueue(t *testing.T) {
	const runID = "run-refusal"
	scope := &mecatlv1.GuardrailApprovalScope{
		ReviewId:        "review-1",
		Kind:            mecatlv1.GuardrailApprovalKind_GUARDRAIL_APPROVAL_KIND_RESULT_RELEASE,
		RepeatAvailable: false,
	}
	recv := &fakeRecver{script: []*mecatlv1.ConverseResponse{
		ev(&mecatlv1.Event{Type: "session.init", RunId: runID}),
		ev(&mecatlv1.Event{Type: "permission.ask", RunId: runID, Ask: &mecatlv1.PermissionAsk{AskId: "ask-a", Tool: "Read", Guardrail: scope}}),
		ev(&mecatlv1.Event{Type: "permission.ask", RunId: runID, Ask: &mecatlv1.PermissionAsk{AskId: "ask-b", Tool: "Write"}}),
		ev(&mecatlv1.Event{Type: "message.delta", RunId: runID, Text: "unrelated progress"}),
		ev(&mecatlv1.Event{Type: "control.refused", RunId: runID, Text: "intent mismatch", ControlRefused: &mecatlv1.ControlRefused{AskId: "ask-a", Category: "approval_intent_mismatch"}}),
	}}
	send := &fakeSender{}
	stream := client.NewStream(recv, send)
	ch := make(chan tea.Msg, 1)
	go stream.ReadLoop(context.Background(), ch)
	next := func() tea.Msg {
		t.Helper()
		select {
		case msg := <-ch:
			return msg
		case <-time.After(scaleWait(3 * time.Second)):
			t.Fatal("timed out waiting for wire event")
			return nil
		}
	}

	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m.sessionID = "session-1"
	m.stream = stream
	m.phase = phaseRunning
	m = applyAll(m, next())
	m = applyAll(m, next())
	m = applyAll(m, next())
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	runBatchLeaves(cmd)
	m = applyAll(m, next())
	if m.pendingApproval == nil {
		t.Fatal("unrelated progress settled submitted approval")
	}
	m = applyAll(m, next())
	s := approvalSurfaceFor(&m)
	if s == nil || s.ask.AskID != "ask-a" || len(s.queue) != 1 || s.queue[0].AskID != "ask-b" || m.phase != phaseAwaitingApproval {
		t.Fatalf("refusal did not restore exact ask and queue: phase=%v surface=%+v", m.phase, s)
	}
	if stream.ApprovalResolved("ask-a") {
		t.Fatal("refusal did not reset transport dedupe")
	}
	mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	runBatchLeaves(cmd)
	frames := send.frames()
	if len(frames) != 2 {
		t.Fatalf("approval frames=%d, want original plus one retry", len(frames))
	}
	for i, frame := range frames {
		ra := frame.GetResumeApproval()
		if ra == nil || ra.GetAskId() != "ask-a" || ra.GetExpectedRunId() != runID || ra.GetReviewId() != "review-1" || ra.GetGuardrailKind() != mecatlv1.GuardrailApprovalKind_GUARDRAIL_APPROVAL_KIND_RESULT_RELEASE {
			t.Fatalf("approval frame %d lost correlation: %+v", i, ra)
		}
	}
	if current := approvalSurfaceFor(&m); current == nil || current.ask.AskID != "ask-b" {
		t.Fatalf("valid retry did not advance to preserved ask: %+v", current)
	}
}

func TestRefusedScopedApprovalReopensExactAskWithoutFalseAllowedNotice(t *testing.T) {
	recorder := &authorizationControlRecorder{err: errors.New("intent mismatch")}
	stream := client.NewAuthorizationEventStream(client.NewFakeEventStream(), recorder)
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m.sessionID = "session-1"
	m.authorization = mcpAuthorizationState{controlStream: stream, controlGen: 3, runningControlGen: 3}
	m.phase = phaseRunning
	scope := &client.GuardrailApprovalScope{ReviewID: "review-1", Kind: "result_release"}
	m = applyAll(m, client.PermissionAskMsg{AskID: "ask-1", Tool: "Read", Guardrail: scope, ExpectedRunID: "run-1"})
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	m = applyAll(m, client.AssistantDeltaMsg{Text: "unrelated worker progress"})
	if m.pendingApproval == nil {
		t.Fatal("unrelated progress settled submitted approval")
	}
	stale := client.EventToMsg(&mecatlv1.Event{Type: "control.refused", RunId: "run-1", Text: "stale", ControlRefused: &mecatlv1.ControlRefused{AskId: "ask-other", Category: "approval_not_pending"}})
	m = applyAll(m, stale)
	if m.pendingApproval == nil {
		t.Fatal("refusal for another ask reopened or settled the submitted approval")
	}
	refused := client.EventToMsg(&mecatlv1.Event{Type: "control.refused", RunId: "run-1", Text: "intent mismatch", ControlRefused: &mecatlv1.ControlRefused{AskId: "ask-1", Category: "approval_intent_mismatch"}})
	if _, ok := refused.(client.ControlRefusedMsg); !ok {
		t.Fatalf("EventToMsg(control.refused) = %T, want ControlRefusedMsg", refused)
	}
	m = applyAll(m, refused)
	s := approvalSurfaceFor(&m)
	if s == nil || s.ask.AskID != "ask-1" || s.ask.guardrail != scope || s.ask.expectedRunID != "run-1" || m.phase != phaseAwaitingApproval {
		t.Fatalf("restored approval = phase=%v surface=%+v", m.phase, s)
	}
	if got := lastNotice(m); strings.Contains(got, "released") || strings.Contains(got, "allowed") || strings.Contains(got, "approved") {
		t.Fatalf("refused approval retained false success notice %q", got)
	}
	if stream.ApprovalResolved("ask-1") {
		t.Fatal("refused approval remained transport-deduped")
	}
}

func TestMCPAuthorizationContinuationPermissionUsesControlStream(t *testing.T) {
	recorder := &authorizationControlRecorder{}
	stream := client.NewAuthorizationEventStream(client.NewFakeEventStream(), recorder)
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m.sessionID = "session-1"
	m = applyAll(m, client.MCPAuthorizationMsg{AuthorizationID: "auth-1", CallID: "call-1", Status: "pending"})
	m.authorization.controlStream = stream
	m.authorization.controlGen = 7
	m.authorization.runningControlGen = 7
	m.phase = phaseRunning
	scope := &client.GuardrailApprovalScope{ReviewID: "review-1", Kind: "action", RepeatAvailable: true}
	m = applyAll(m, mcpAuthorizationEventMsg{sessionID: "session-1", authorizationID: "auth-1", gen: 7, msg: client.PermissionAskMsg{AskID: "session-1:1:followup", Tool: "protected", ExpectedRunID: "run-1", Guardrail: scope}})
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	runBatchLeaves(cmd)
	if recorder.askID != "session-1:1:followup" || recorder.verdict != client.VerdictAllowOnce || recorder.guardrail != scope || recorder.expectedRunID != "run-1" {
		t.Fatalf("authorization control approval = (%q, %v, %+v, %q)", recorder.askID, recorder.verdict, recorder.guardrail, recorder.expectedRunID)
	}
	if m.phase != phaseRunning {
		t.Fatalf("phase after approval = %v, want running", m.phase)
	}
	_, cancelCmd := m.onRunningCancel()
	runCmd(cancelCmd)
	if !recorder.cancelled {
		t.Fatal("continuation cancel did not use authorization control stream")
	}
	correlated := func(msg tea.Msg) mcpAuthorizationEventMsg {
		return mcpAuthorizationEventMsg{sessionID: "session-1", authorizationID: "auth-1", gen: 7, msg: msg}
	}
	m = applyAll(m, correlated(client.ToolResultMsg{CallID: "followup-call", Content: "approved"}))
	m = applyAll(m, correlated(client.ResultMsg{Stop: "end_turn"}))
	m = applyAll(m, mcpAuthorizationStreamClosedMsg{sessionID: "session-1", authorizationID: "auth-1", gen: 7})
	if m.phase != phaseIdle {
		t.Fatalf("phase after terminal authorization continuation = %v, want idle", m.phase)
	}
}

func TestMCPAuthorizationControlErrorWhileAwaitingApprovalEndsOwnedRun(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m.sessionID = "session-1"
	m.stream = client.NewStream(&fakeRecver{}, &fakeSender{})
	m.authorization = mcpAuthorizationState{
		authorizationID:   "auth-1",
		controlGen:        7,
		runningControlGen: 7,
		controlStream:     client.NewAuthorizationEventStream(client.NewFakeEventStream(), &authorizationControlRecorder{}),
	}
	m.authorizationEvents = make(chan tea.Msg)
	m.phase = phaseRunning
	correlated := func(msg tea.Msg) mcpAuthorizationEventMsg {
		return mcpAuthorizationEventMsg{sessionID: "session-1", authorizationID: "auth-1", gen: 7, msg: msg}
	}
	m = applyAll(m, correlated(client.PermissionAskMsg{AskID: "session-1:1:followup", Tool: "protected"}))
	m = applyAll(m, correlated(client.PermissionAskMsg{AskID: "session-1:1:queued", Tool: "protected"}))
	if m.phase != phaseAwaitingApproval || approvalSurfaceFor(&m) == nil {
		t.Fatal("precondition: authorization continuation did not open approval surface")
	}

	m = applyAll(m, correlated(client.StreamErrMsg{Err: errors.New("control lost\nforged")}))
	if m.phase != phaseIdle || m.modal != nil {
		t.Fatalf("control error left approval active: phase=%v modal=%T", m.phase, m.modal)
	}
	if m.authorization.controlStream != nil || m.authorization.controlCancel != nil || m.authorization.runningControlGen != 0 || m.authorizationEvents != nil {
		t.Fatalf("control ownership survived stream error: %+v", m.authorization)
	}
	if m.stream != nil || m.streamCh != nil {
		t.Fatal("control error retained the closed original Converse stream")
	}
	if strings.Contains(m.authorization.errorText, "\n") || !strings.Contains(m.authorization.errorText, "control lost") {
		t.Fatalf("authorization error was not sanitized: %q", m.authorization.errorText)
	}
	if !strings.Contains(m.statusMsg, "control lost") {
		t.Fatalf("visible status omitted control error: %q", m.statusMsg)
	}
}

func TestMCPAuthorizationControlErrorWhileRunningEndsOwnedRun(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m.sessionID = "session-1"
	m.stream = client.NewStream(&fakeRecver{}, &fakeSender{})
	cancelled := false
	m.authorization = mcpAuthorizationState{
		authorizationID:   "auth-1",
		controlGen:        7,
		runningControlGen: 7,
		controlCancel:     func() { cancelled = true },
		controlStream:     client.NewAuthorizationEventStream(client.NewFakeEventStream(), &authorizationControlRecorder{}),
	}
	m.authorizationEvents = make(chan tea.Msg)
	m.phase = phaseRunning

	m = applyAll(m, mcpAuthorizationEventMsg{
		sessionID: "session-1", authorizationID: "auth-1", gen: 7,
		msg: client.StreamErrMsg{Err: errors.New("control lost\nforged")},
	})
	if m.phase != phaseIdle || !cancelled {
		t.Fatalf("owned running continuation was not ended: phase=%v cancelled=%v", m.phase, cancelled)
	}
	if m.authorization.controlStream != nil || m.authorization.controlCancel != nil || m.authorization.runningControlGen != 0 || m.authorizationEvents != nil {
		t.Fatalf("control ownership survived stream error: %+v", m.authorization)
	}
	if m.stream != nil || m.streamCh != nil {
		t.Fatal("control error retained the closed original Converse stream")
	}
	if strings.Contains(m.authorization.errorText, "\n") || !strings.Contains(m.authorization.errorText, "control lost") || !strings.Contains(m.statusMsg, "control lost") {
		t.Fatalf("visible control error was not retained and sanitized: error=%q status=%q", m.authorization.errorText, m.statusMsg)
	}
}

func TestMCPAuthorizationResolutionRearmsSpinner(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m.phase = phaseAuthorizing
	m.authorization.authorizationID = "auth-1"

	mm, cmd := m.applyMCPAuthorization(client.MCPAuthorizationMsg{AuthorizationID: "auth-1", Status: "granted"})
	m = mm.(Model)
	if m.phase != phaseRunning || cmd == nil {
		t.Fatalf("resolution = phase %v, spinner command %v", m.phase, cmd)
	}
}

func TestMCPAuthorizationParkedConverseCloseInvalidatesSource(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m.sessionID = "session-1"
	m.phase = phaseAuthorizing
	m.stream = client.NewStream(&fakeRecver{}, &fakeSender{})
	m.streamCh = make(chan tea.Msg)
	m.streamGen = 4

	m = applyAll(m, streamMsg{gen: 4, msg: client.StreamClosedMsg{}})
	if m.stream != nil || m.streamCh != nil || m.streamGen != 5 {
		t.Fatalf("parked close retained stale Converse source: stream=%v channel=%v generation=%d", m.stream, m.streamCh, m.streamGen)
	}
}

func TestMCPAuthorizationControlOwnsContinuationStream(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m.sessionID = "session-1"
	m.phase = phaseRunning
	m.conv.addTool("call-1", "mcp__github__issues", `{}`)
	m = applyAll(m, client.MCPAuthorizationMsg{AuthorizationID: "auth-1", CallID: "call-1", Status: "pending"})
	m = applyAll(m, client.StreamClosedMsg{}) // original Converse stream closed after park
	if m.phase != phaseAuthorizing {
		t.Fatalf("original close changed phase to %v", m.phase)
	}

	control := make(chan tea.Msg, 4)
	m.authorization.controlGen = 7
	m.authorizationEvents = control
	correlated := func(msg tea.Msg) mcpAuthorizationEventMsg {
		return mcpAuthorizationEventMsg{sessionID: "session-1", authorizationID: "auth-1", gen: 7, msg: msg}
	}
	mm, cmd := m.Update(correlated(client.MCPAuthorizationMsg{AuthorizationID: "auth-1", CallID: "call-1", Status: "granted"}))
	m = mm.(Model)
	if m.phase != phaseRunning || cmd == nil {
		t.Fatalf("resolution did not resume continuation: phase=%v cmd=%v", m.phase, cmd)
	}
	control <- client.ToolResultMsg{CallID: "call-1", Content: "continued"}
	next := cmd()
	if _, ok := next.(tea.BatchMsg); !ok {
		t.Fatalf("resolution command = %T, want batched spinner and control reader", next)
	}
	m = applyAll(m, correlated(client.ToolResultMsg{CallID: "call-1", Content: "continued"}))
	m = applyAll(m, correlated(client.ResultMsg{Stop: "end_turn"}))
	m = applyAll(m, mcpAuthorizationStreamClosedMsg{sessionID: "session-1", authorizationID: "auth-1", gen: 7})
	if m.phase != phaseIdle || m.authorizationEvents != nil {
		t.Fatalf("terminal continuation state: phase=%v stream=%v", m.phase, m.authorizationEvents)
	}
}

func TestMCPAuthorizationStaleControlTerminalDoesNotResetNewConverseRun(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.Msg
	}{
		{"close", mcpAuthorizationStreamClosedMsg{sessionID: "session-1", authorizationID: "auth-1", gen: 7}},
		{"error", mcpAuthorizationEventMsg{sessionID: "session-1", authorizationID: "auth-1", gen: 7, msg: client.StreamErrMsg{Err: errors.New("stale control error")}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
			m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette()), Ctx: t.Context(), Conv: conv})
			m.sessionID = "session-1"
			m.authorization = mcpAuthorizationState{authorizationID: "auth-1", controlGen: 7, runningControlGen: 7}
			m.phase = phaseRunning
			m, _ = m.openRun(false, func(*client.Stream) error { return nil })
			if m.authorization.runningControlGen != 0 {
				t.Fatal("new Converse run did not take phase ownership")
			}
			streamGen := m.streamGen
			m = applyAll(m, tc.msg)
			if m.phase != phaseRunning || m.stream == nil || m.streamGen != streamGen {
				t.Fatalf("stale control terminal reset newer run: phase=%v stream=%v generation=%d want=%d", m.phase, m.stream, m.streamGen, streamGen)
			}
			if m.cancelRun != nil {
				m.cancelRun()
			}
		})
	}
}

func TestMCPAuthorizationStatusOnlyResolutionReturnsIdle(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m.sessionID = "session-1"
	m = applyAll(m, client.MCPAuthorizationMsg{AuthorizationID: "auth-1", CallID: "call-1", Status: "pending"})
	m.authorization.controlGen = 3
	m.authorizationEvents = make(chan tea.Msg)
	correlated := mcpAuthorizationEventMsg{sessionID: "session-1", authorizationID: "auth-1", gen: 3, msg: client.MCPAuthorizationMsg{AuthorizationID: "auth-1", CallID: "call-1", Status: "cancelled"}}
	m = applyAll(m, correlated)
	if m.phase != phaseRunning {
		t.Fatalf("resolved phase = %v, want running until stream close", m.phase)
	}
	m = applyAll(m, mcpAuthorizationStreamClosedMsg{sessionID: "session-1", authorizationID: "auth-1", gen: 3})
	if m.phase != phaseIdle || m.authorizationEvents != nil {
		t.Fatalf("status-only close state: phase=%v stream=%v", m.phase, m.authorizationEvents)
	}
}

func TestMCPAuthorizationErrorsPreservePendingAndAreCorrelated(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m.sessionID = "session-1"
	m = applyAll(m, client.MCPAuthorizationMsg{AuthorizationID: "auth-1", CallID: "call-1", Status: "pending"})
	m.authorization.controlGen = 2
	m.authorizationEvents = make(chan tea.Msg)
	m = applyAll(m, mcpAuthorizationEventMsg{sessionID: "session-1", authorizationID: "auth-1", gen: 2, msg: client.StreamErrMsg{Err: errors.New("failed\nforged")}})
	if m.phase != phaseAuthorizing || m.authorization.authorizationID != "auth-1" || m.authorizationEvents != nil {
		t.Fatalf("error destroyed pending authorization: %+v", m.authorization)
	}
	if strings.Contains(m.authorization.errorText, "\n") || !strings.Contains(m.authorization.errorText, "failed") {
		t.Fatalf("error was not terminal-sanitized: %q", m.authorization.errorText)
	}
	stale := mcpAuthorizationErrorMsg{sessionID: "other", authorizationID: "auth-1", gen: 2, err: errors.New("stale")}
	m = applyAll(m, stale)
	if strings.Contains(m.authorization.errorText, "stale") {
		t.Fatal("stale session error overwrote current authorization")
	}
}

func TestMCPAuthorizationControlGenerationAndSessionReset(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m.sessionID = "session-1"
	m = applyAll(m, client.MCPAuthorizationMsg{AuthorizationID: "auth-1", CallID: "call-1", Status: "pending"})
	m.authorization.controlGen = 4
	m = applyAll(m, mcpAuthorizationErrorMsg{sessionID: "session-1", authorizationID: "auth-1", gen: 3, err: errors.New("stale")})
	if m.authorization.errorText != "" {
		t.Fatal("stale control generation overwrote current state")
	}
	m = m.bindSessionID("session-2")
	if m.authorization.authorizationID != "" || m.authorizationEvents != nil {
		t.Fatalf("session switch retained authorization: %+v", m.authorization)
	}
}

func TestMCPAuthorizationOperationErrorsUseDedicatedState(t *testing.T) {
	controller := &mcpAuthorizationControllerFake{presentationErr: errors.New("presentation failed\nsecret")}
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette()), MCPAuthorization: controller, OpenURL: func(context.Context, string) error { return errors.New("browser failed") }})
	m.sessionID = "session-1"
	m = applyAll(m, client.MCPAuthorizationMsg{AuthorizationID: "auth-1", CallID: "call-1", Status: "pending"})

	_, cmd := m.onMCPAuthorizationKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	msg := cmd()
	if _, generic := msg.(client.StreamErrMsg); generic {
		t.Fatal("presentation error used generic stream error")
	}
	m = applyAll(m, msg)
	if m.phase != phaseAuthorizing || !strings.Contains(m.authorization.errorText, "presentation failed") || strings.Contains(m.authorization.errorText, "\n") {
		t.Fatalf("presentation error state = %+v phase=%v", m.authorization, m.phase)
	}

	controller.presentationErr = nil
	_, cmd = m.onMCPAuthorizationKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = applyAll(m, cmd())
	if !strings.Contains(m.authorization.errorText, "browser failed") {
		t.Fatalf("browser error was not retained: %+v", m.authorization)
	}

	controller.recheckErr = errors.New("recheck failed")
	mm, controlCmd := m.startMCPAuthorizationControl(false)
	m = mm.(Model)
	msg = controlCmd()
	if _, generic := msg.(client.StreamErrMsg); generic {
		t.Fatal("immediate control error used generic stream error")
	}
	m = applyAll(m, msg)
	if m.phase != phaseAuthorizing || !strings.Contains(m.authorization.errorText, "recheck failed") {
		t.Fatalf("control error destroyed pending state: %+v", m.authorization)
	}

	m.deps.Clipboard = &fakeClipboard{writeErr: errors.New("clipboard failed")}
	_, copyCmd := m.onMCPAuthorizationKey(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	msg = copyCmd()
	if _, generic := msg.(client.StreamErrMsg); generic {
		t.Fatal("clipboard error used generic stream error")
	}
	m = applyAll(m, msg)
	if m.phase != phaseAuthorizing || !strings.Contains(m.authorization.errorText, "clipboard failed") {
		t.Fatalf("clipboard error destroyed pending state: %+v", m.authorization)
	}
}

func TestSessionMCPAuthorization_Scenario9_MecatuiCommandsAndNoReplayOpen(t *testing.T) {
	controller := &mcpAuthorizationControllerFake{}
	opened := 0
	m := New(Deps{
		Theme:            theme.New("aztec", theme.AztecPalette()),
		MCPAuthorization: controller,
		OpenURL: func(context.Context, string) error {
			opened++
			return nil
		},
	})
	m.sessionID = "session-1"
	// Replayed state is presentation-only: it must not open a browser.
	m = applyAll(m, client.MCPAuthorizationMsg{AuthorizationID: "auth-1", CallID: "call-1", Status: "pending"})
	if opened != 0 {
		t.Fatalf("replayed authorization opened browser %d times", opened)
	}

	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
		want func() int
	}{
		{
			"open", tea.KeyPressMsg{Code: tea.KeyEnter}, func() int { return controller.presentation },
		},
		{"cancel", tea.KeyPressMsg{Code: 'x', Text: "x"}, func() int { return controller.cancel }},
	} {
		_, cmd := m.onMCPAuthorizationKey(tc.key)
		if cmd == nil {
			t.Fatalf("%s command was not installed", tc.name)
		}
		_ = cmd()
		if tc.want() != 1 {
			t.Fatalf("%s calls = %d, want 1", tc.name, tc.want())
		}
	}
	if opened != 1 {
		t.Fatalf("browser opens = %d, want explicit Open Browser only", opened)
	}
	if _, refreshCmd := m.onMCPAuthorizationKey(tea.KeyPressMsg{Code: 'r', Text: "r"}); refreshCmd != nil {
		t.Fatal("manual authorization refresh remained available")
	}
	_, copyCmd := m.onMCPAuthorizationKey(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	if copyCmd == nil || copyCmd() == nil {
		t.Fatal("copy-link command was not installed")
	}
	if controller.presentation != 2 || opened != 1 {
		t.Fatalf("copy calls presentation/open = %d/%d, want 2/1", controller.presentation, opened)
	}
}

func pendingMCPAuthorizationStatusError(t *testing.T) error {
	t.Helper()
	st, err := status.New(codes.FailedPrecondition, "server wording is not a classifier").WithDetails(&errdetails.ErrorInfo{
		Reason: client.MCPAuthorizationPendingCode,
		Domain: "mecatl.stacklok.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	return st.Err()
}

func TestMCPAuthorizationPendingStreamErrorKeepsCorrelatedCard(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m.sessionID = "session-1"
	m = applyAll(m, client.MCPAuthorizationMsg{AuthorizationID: "auth-1", Status: mcpAuthorizationStatusPending})
	m.queued = []string{"later"}
	m.prompt.Rewrite("draft")
	m.streamGen = 7
	m.streamCh = make(chan tea.Msg)
	blocks := len(m.conv.testBlocks())

	// Converse ReadLoop delivers the error through the generation-tagged stream
	// fan-in, rather than directly to updateLifecycle.
	mm, _ := m.Update(streamMsg{gen: 7, msg: client.StreamErrMsg{Err: pendingMCPAuthorizationStatusError(t)}})
	m = mm.(Model)
	if m.phase != phaseAuthorizing || m.authorization.authorizationID != "auth-1" {
		t.Fatalf("pending authorization was replaced: phase=%v authorization=%q", m.phase, m.authorization.authorizationID)
	}
	if m.streamCh != nil || m.streamGen != 8 {
		t.Fatalf("parked Converse source was not invalidated: channel=%v generation=%d", m.streamCh, m.streamGen)
	}
	if len(m.conv.testBlocks()) != blocks || len(m.queued) != 1 || m.prompt.Value() != "draft" {
		t.Fatalf("pending status polluted generic error or prompt state: blocks=%d queued=%v prompt=%q", len(m.conv.testBlocks()), m.queued, m.prompt.Value())
	}
}

func TestMCPAuthorizationPendingStreamErrorWithoutCardUsesGenericFailure(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m.sessionID = "session-1"
	m.phase = phaseRunning
	blocks := len(m.conv.testBlocks())

	mm, _ := m.Update(client.StreamErrMsg{Err: pendingMCPAuthorizationStatusError(t)})
	m = mm.(Model)
	if m.phase == phaseAuthorizing {
		t.Fatal("uncorrelated pending status created an authorization card")
	}
	if len(m.conv.testBlocks()) <= blocks {
		t.Fatal("uncorrelated pending status did not use generic stream failure")
	}
}

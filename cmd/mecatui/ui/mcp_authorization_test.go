package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
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
	for _, required := range []string{"Open Browser", "Copy Link", "Recheck", "Cancel"} {
		if !strings.Contains(view, required) {
			t.Fatalf("authorization view missing %q: %s", required, view)
		}
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

type mcpAuthorizationControllerFake struct {
	presentation, recheck, cancel int
	presentationErr, recheckErr   error
	controlStream                 *client.EventStream
}

func (f *mcpAuthorizationControllerFake) MCPAuthorizationPresentation(context.Context, string, string) (string, error) {
	f.presentation++
	return "https://authorization.example/", f.presentationErr
}

func (f *mcpAuthorizationControllerFake) RecheckMCPAuthorization(context.Context, string, string) (*client.EventStream, error) {
	f.recheck++
	return f.controlStream, f.recheckErr
}

func (f *mcpAuthorizationControllerFake) CancelMCPAuthorization(context.Context, string, string) (*client.EventStream, error) {
	f.cancel++
	return nil, nil
}

type authorizationControlRecorder struct {
	askID     string
	verdict   client.Verdict
	cancelled bool
}

func (r *authorizationControlRecorder) SendApproval(id string, verdict client.Verdict) error {
	r.askID, r.verdict = id, verdict
	return nil
}
func (r *authorizationControlRecorder) SendCancel() error {
	r.cancelled = true
	return nil
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
	m = applyAll(m, mcpAuthorizationEventMsg{sessionID: "session-1", authorizationID: "auth-1", gen: 7, msg: client.PermissionAskMsg{AskID: "session-1:1:followup", Tool: "protected"}})
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	runBatchLeaves(cmd)
	if recorder.askID != "session-1:1:followup" || recorder.verdict != client.VerdictAllowOnce {
		t.Fatalf("authorization control approval = (%q, %v)", recorder.askID, recorder.verdict)
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
	if _, ok := next.(mcpAuthorizationEventMsg); !ok {
		t.Fatalf("next reader source = %T, want authorization control", next)
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
	_, copyCmd := m.onMCPAuthorizationKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl | tea.ModShift})
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
		{"open", tea.KeyPressMsg{Code: tea.KeyEnter}, func() int { return controller.presentation }},
		{"recheck", tea.KeyPressMsg{Code: 'r', Text: "r"}, func() int { return controller.recheck }},
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
	_, copyCmd := m.onMCPAuthorizationKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl | tea.ModShift})
	if copyCmd == nil || copyCmd() == nil {
		t.Fatal("copy-link command was not installed")
	}
	if controller.presentation != 2 || opened != 1 {
		t.Fatalf("copy calls presentation/open = %d/%d, want 2/1", controller.presentation, opened)
	}
}

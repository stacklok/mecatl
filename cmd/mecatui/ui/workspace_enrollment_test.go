package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestWorkspaceEnrollmentIsNonBlocking(t *testing.T) {
	control := &workspaceEnrollmentControlFake{connect: client.WorkspaceEnrollment{
		ID: "bundle-1", Status: client.WorkspaceEnrollmentPending, RequiredServices: 2,
		PresentationURL: "https://provider-private.example/callback?token=token-canary",
	}}
	m := New(Deps{Ctx: context.Background(), WorkspaceEnrollment: control, OpenURL: func(context.Context, string) error { return nil }})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30}, client.SessionReadyMsg{
		SessionID: "session-1", Capabilities: client.Capabilities{WorkspaceEnrollment: true},
	})

	if m.phase != phaseIdle || m.modal != nil || !m.prompt.Focused() {
		t.Fatalf("opening an enrollment session must remain promptable: phase=%v modal=%T focused=%t", m.phase, m.modal, m.prompt.Focused())
	}
	if !strings.Contains(m.workspaceEnrollmentNotice, "/tools-connect") {
		t.Fatalf("workspaceEnrollmentNotice = %q, want /tools-connect", m.workspaceEnrollmentNotice)
	}
	if got := stripANSIstr(m.idleFooterLeft()); !strings.Contains(got, "/tools-connect") {
		t.Fatalf("footer-left = %q, want enrollment notice", got)
	}

	mm, cmd := m.runToolsConnect()
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("/tools-connect returned no command")
	}
	mm, presentationCmd := m.Update(cmd())
	m = mm.(Model)
	if presentationCmd == nil {
		t.Fatal("pending enrollment did not request browser presentation")
	}
	m = applyAll(m, presentationCmd())
	if control.connectCalls != 1 || m.enrollment.ID != "bundle-1" {
		t.Fatalf("connect calls/state = %d/%+v", control.connectCalls, m.enrollment)
	}
	if got := stripANSIstr(m.idleFooterLeft()); !strings.Contains(got, "notified when connected") {
		t.Fatalf("footer-left after a pending connect = %q, want automatic completion notice", got)
	}
	if got := stripANSIstr(m.idleFooterLeft()); strings.Contains(got, "https://") || strings.Contains(got, "token-canary") {
		t.Fatalf("footer rendered private presentation data: %s", got)
	}
	if got := fmt.Sprintf("%+v", m.enrollment); strings.Contains(got, "https://") || strings.Contains(got, "token-canary") {
		t.Fatalf("enrollment state retained private presentation data: %s", got)
	}
}

// TestWorkspaceEnrollmentUserCopy covers the concise user-facing states around
// /tools-connect so each remedy stays tied to its actual condition.
func TestWorkspaceEnrollmentUserCopy(t *testing.T) {
	if got := builtinCommands(client.Capabilities{WorkspaceEnrollment: true}, wiredCollaborators{Workspace: true}); !hasBuiltinDescription(got, "tools-connect", "deprecated alias for /mcp-refresh") {
		t.Fatalf("/tools-connect built-in = %#v, want deprecated broker alias", got)
	}

	for _, tc := range []struct {
		name, want string
		setup      func() Model
	}{
		{"unavailable", "cannot be connected on this server", func() Model { return New(Deps{Ctx: context.Background()}) }},
		{"no session", "no active session", func() Model {
			return New(Deps{Ctx: context.Background(), WorkspaceEnrollment: &workspaceEnrollmentControlFake{}})
		}},
		{"busy", "already connecting", func() Model {
			m := New(Deps{Ctx: context.Background(), WorkspaceEnrollment: &workspaceEnrollmentControlFake{}})
			m.sessionID = "session-1"
			m.phase = phaseIdle
			m.enrollment.busy = true
			return m
		}},
		{"non-idle", "before sending a prompt", func() Model {
			m := New(Deps{Ctx: context.Background(), WorkspaceEnrollment: &workspaceEnrollmentControlFake{}})
			m.sessionID = "session-1"
			m.phase = phaseRunning
			return m
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.setup()
			mm, cmd := m.runToolsConnect()
			m = mm.(Model)
			if cmd != nil || !strings.Contains(m.workspaceEnrollmentNotice, tc.want) {
				t.Fatalf("notice = %q, command=%v; want %q", m.workspaceEnrollmentNotice, cmd != nil, tc.want)
			}
		})
	}

	if terminal, _, got := terminalWorkspaceEnrollmentNotice(client.WorkspaceEnrollmentDenied); !terminal || !strings.Contains(got, "declined") || !strings.Contains(got, "/tools-connect") {
		t.Fatalf("denied notice = terminal:%t copy:%q", terminal, got)
	}
	if got := friendlyWorkspaceEnrollmentRejection("workspace services must be connected before prompting"); !strings.Contains(got, "enable protected tools") || !strings.Contains(got, "/tools-connect") {
		t.Fatalf("rejection = %q", got)
	}
	for _, phrase := range []string{"timed out", "uncertain", "/clear", "/tools-connect"} {
		if !strings.Contains(workspaceEnrollmentTimeoutNotice, phrase) {
			t.Fatalf("timeout notice missing %q: %q", phrase, workspaceEnrollmentTimeoutNotice)
		}
	}
}

func hasBuiltinDescription(builtins []builtin, name, want string) bool {
	for _, b := range builtins {
		if b.name == name {
			return strings.Contains(b.desc, want)
		}
	}
	return false
}

func TestWorkspaceEnrollmentConnectedRechecksAndResubmitsOnce(t *testing.T) {
	control := &workspaceEnrollmentControlFake{connect: client.WorkspaceEnrollment{
		ID: "bundle-1", Status: client.WorkspaceEnrollmentPending, PresentationURL: "https://authorization.example/",
	}}
	m, send := builtinDispatchModel(t, client.Capabilities{WorkspaceEnrollment: true}, false)
	m.deps.WorkspaceEnrollment = control
	m.deps.OpenURL = func(context.Context, string) error { return nil }
	m.pendingInitialPrompt = "list my open pull requests"

	mm, cmd := m.runToolsConnect()
	m = mm.(Model)
	mm, presentationCmd := m.Update(cmd())
	m = mm.(Model)
	mm, _ = m.Update(presentationCmd())
	m = mm.(Model)
	control.connect = client.WorkspaceEnrollment{ID: "bundle-1", Status: client.WorkspaceEnrollmentConnected}

	mm, finalizeCmd := m.applyWorkspaceEnrollmentPollTick(workspaceEnrollmentPollTickMsg{sessionID: m.sessionID, enrollmentID: m.enrollment.ID, gen: m.enrollment.controlGen})
	m = mm.(Model)
	mm, finalizeCmd = m.Update(finalizeCmd())
	m = mm.(Model)
	if control.connectCalls != 2 || m.enrollment.ID != "" || m.workspaceEnrollmentNotice != "" {
		t.Fatalf("connected recheck did not finalize: calls=%d enrollment=%+v notice=%q", control.connectCalls, m.enrollment, m.workspaceEnrollmentNotice)
	}
	if m.pendingInitialPrompt != "" {
		t.Fatalf("pendingInitialPrompt not consumed: %q", m.pendingInitialPrompt)
	}
	runBatchLeaves(finalizeCmd)
	if got := promptTexts(send); len(got) != 1 || got[0] != "list my open pull requests" {
		t.Fatalf("resubmitted prompts = %v", got)
	}

	m.phase = phaseIdle
	mm, cmd = m.runToolsConnect()
	m = mm.(Model)
	m = applyAll(m, cmd())
	if got := promptTexts(send); len(got) != 1 {
		t.Fatalf("connected recheck resubmitted more than once: %v", got)
	}
}

func TestWorkspaceEnrollmentRejectionAutoResubmits(t *testing.T) {
	m, send := builtinDispatchModel(t, client.Capabilities{WorkspaceEnrollment: true}, false)
	m = typeText(t, m, "list PRs")
	mm, _ := m.submitPrompt()
	m = mm.(Model)
	if m.promptRecovery == nil || m.promptRecovery.text != "list PRs" || m.promptRecovery.autoReplay {
		t.Fatalf("initial recovery = %#v", m.promptRecovery)
	}

	mm, _ = m.Update(client.StreamErrMsg{Err: errors.New("rpc error: code = FailedPrecondition desc = workspace services must be connected before prompting")})
	m = mm.(Model)
	if m.promptRecovery == nil || !m.promptRecovery.autoReplay || m.prompt.Value() != "list PRs" {
		t.Fatalf("rejection recovery = %#v, draft=%q", m.promptRecovery, m.prompt.Value())
	}

	m.enrollment.ID = "bundle-1"
	mm, cmd := m.applyWorkspaceEnrollment(workspaceEnrollmentMsg{
		action: "check", sessionID: m.sessionID, targetEnrollmentID: "bundle-1",
		result: client.WorkspaceEnrollment{ID: "bundle-1", Status: client.WorkspaceEnrollmentConnected},
	})
	m = mm.(Model)
	if cmd == nil || m.pendingInitialPrompt != "" {
		t.Fatalf("connected completion did not consume rejected prompt: cmd=%v pending=%q", cmd != nil, m.pendingInitialPrompt)
	}
	runBatchLeaves(cmd)
	if got := promptTexts(send); len(got) != 1 || got[0] != "list PRs" {
		t.Fatalf("sent prompts = %v, want one resubmission", got)
	}
}

func TestPromptTransportFailureRestoresDraftWithoutReplay(t *testing.T) {
	m, send := builtinDispatchModel(t, client.Capabilities{}, false)
	m = typeText(t, m, "review this change")
	mm, _ := m.submitPrompt()
	m = mm.(Model)

	mm, _ = m.Update(client.StreamErrMsg{Err: errors.New("connection lost")})
	m = mm.(Model)
	if got := m.prompt.Value(); got != "review this change" {
		t.Fatalf("restored draft = %q", got)
	}
	if m.promptRecovery != nil {
		t.Fatalf("ambiguous failure retained replay state: %#v", m.promptRecovery)
	}
	if got := promptTexts(send); len(got) != 0 {
		t.Fatalf("ambiguous failure replayed prompt: %v", got)
	}
}

func TestWorkspaceEnrollmentRecoveryDoesNotCrossReplacementDraft(t *testing.T) {
	m, send := builtinDispatchModel(t, client.Capabilities{WorkspaceEnrollment: true}, false)
	m = typeText(t, m, "original")
	mm, _ := m.submitPrompt()
	m = mm.(Model)
	mm, _ = m.Update(client.StreamErrMsg{Err: errors.New("workspace services must be connected before prompting")})
	m = mm.(Model)
	m.prompt.Rewrite("replacement")
	m.enrollment.ID = "bundle-1"

	mm, cmd := m.applyWorkspaceEnrollment(workspaceEnrollmentMsg{
		action: "check", sessionID: m.sessionID, targetEnrollmentID: "bundle-1",
		result: client.WorkspaceEnrollment{ID: "bundle-1", Status: client.WorkspaceEnrollmentConnected},
	})
	m = mm.(Model)
	runBatchLeaves(cmd)
	if got := m.prompt.Value(); got != "replacement" {
		t.Fatalf("replacement draft = %q", got)
	}
	if got := promptTexts(send); len(got) != 0 {
		t.Fatalf("replacement draft was replayed: %v", got)
	}
}

func TestPromptRecoveryIsClearedOnSessionReset(t *testing.T) {
	m, _ := builtinDispatchModel(t, client.Capabilities{WorkspaceEnrollment: true}, false)
	m = typeText(t, m, "original")
	mm, _ := m.submitPrompt()
	m = mm.(Model)
	mm, _ = m.Update(client.StreamErrMsg{Err: errors.New("workspace services must be connected before prompting")})
	m = mm.(Model)
	if m.promptRecovery == nil {
		t.Fatal("expected enrollment recovery before reset")
	}

	m = m.resetSession()
	if m.promptRecovery != nil {
		t.Fatalf("reset retained recovery: %#v", m.promptRecovery)
	}
}

func TestWorkspaceEnrollmentRejectionRewriteAppliesAtAllRunEntryPaths(t *testing.T) {
	raw := "rpc error: code = FailedPrecondition desc = workspace services must be connected before prompting"
	if got := friendlyWorkspaceEnrollmentRejection(raw); !strings.Contains(got, "/tools-connect") {
		t.Fatalf("friendlyWorkspaceEnrollmentRejection = %q", got)
	}
	m, _ := newQueueModel(t)
	mm, _ := m.Update(client.StreamErrMsg{Err: errors.New(raw)})
	m = mm.(Model)
	if got := m.conv.blocks[len(m.conv.blocks)-1].raw; !strings.Contains(got, "/tools-connect") {
		t.Fatalf("stream error block = %q", got)
	}
	mm, _ = m.applyResult(client.ResultMsg{Stop: stopError, Error: raw})
	m = mm.(Model)
	if got := m.conv.blocks[len(m.conv.blocks)-1].raw; !strings.Contains(got, "/tools-connect") {
		t.Fatalf("result error block = %q", got)
	}
}

func TestWorkspaceEnrollmentPollCompletesWithoutManualRecheck(t *testing.T) {
	control := &workspaceEnrollmentControlFake{connect: client.WorkspaceEnrollment{ID: "bundle-1", Status: client.WorkspaceEnrollmentPending}}
	m := New(Deps{Ctx: context.Background(), WorkspaceEnrollment: control})
	m.sessionID = "session-1"
	m.enrollment = workspaceEnrollmentState{ID: "bundle-1", Status: client.WorkspaceEnrollmentPending, presentationDelivered: true}

	mm, cmd := m.applyWorkspaceEnrollmentPollTick(workspaceEnrollmentPollTickMsg{sessionID: "session-1", enrollmentID: "bundle-1"})
	m = mm.(Model)
	if cmd == nil || !m.enrollment.busy {
		t.Fatal("pending enrollment poll did not start an observation")
	}
	if _, duplicate := m.applyWorkspaceEnrollmentPollTick(workspaceEnrollmentPollTickMsg{sessionID: "session-1", enrollmentID: "bundle-1"}); duplicate != nil {
		t.Fatal("duplicate enrollment poll overlapped the in-flight observation")
	}
	control.connect = client.WorkspaceEnrollment{ID: "bundle-1", Status: client.WorkspaceEnrollmentConnected}
	m = applyAll(m, cmd())
	if control.connectCalls != 1 || m.enrollment.ID != "" || m.statusMsg != "workspace tools connected" {
		t.Fatalf("automatic completion = calls %d, enrollment %+v, status %q", control.connectCalls, m.enrollment, m.statusMsg)
	}
}

func TestWorkspaceEnrollmentPollIgnoresStaleSessionOrEnrollment(t *testing.T) {
	control := &workspaceEnrollmentControlFake{}
	m := New(Deps{Ctx: context.Background(), WorkspaceEnrollment: control})
	m.sessionID = "session-current"
	m.enrollment = workspaceEnrollmentState{ID: "bundle-current", Status: client.WorkspaceEnrollmentPending, controlGen: 9}
	for _, tick := range []workspaceEnrollmentPollTickMsg{
		{sessionID: "session-old", enrollmentID: "bundle-current", gen: 9},
		{sessionID: "session-current", enrollmentID: "bundle-old", gen: 9},
		{sessionID: "session-current", enrollmentID: "bundle-current", gen: 8},
	} {
		if _, cmd := m.applyWorkspaceEnrollmentPollTick(tick); cmd != nil {
			t.Fatalf("stale tick %#v started a poll", tick)
		}
	}
	m.enrollment.Status = client.WorkspaceEnrollmentCancelled
	if _, cmd := m.applyWorkspaceEnrollmentPollTick(workspaceEnrollmentPollTickMsg{sessionID: "session-current", enrollmentID: "bundle-current", gen: 9}); cmd != nil {
		t.Fatal("cancelled enrollment continued polling")
	}
}

func TestWorkspaceEnrollmentStaleResultAndBrowserFailureAreCorrelated(t *testing.T) {
	m := New(Deps{Ctx: context.Background(), WorkspaceEnrollment: &workspaceEnrollmentControlFake{}})
	m.sessionID = "session-current"
	m.enrollment = workspaceEnrollmentState{ID: "bundle-current", Status: client.WorkspaceEnrollmentPending, controlGen: 4}
	m.statusMsg = "unchanged"

	for _, msg := range []workspaceEnrollmentMsg{
		{action: "check", sessionID: "session-old", targetEnrollmentID: "bundle-current", gen: 4, result: client.WorkspaceEnrollment{ID: "bundle-current", Status: client.WorkspaceEnrollmentConnected}},
		{action: "check", sessionID: "session-current", targetEnrollmentID: "bundle-old", gen: 4, result: client.WorkspaceEnrollment{ID: "bundle-old", Status: client.WorkspaceEnrollmentConnected}},
		{action: "check", sessionID: "session-current", targetEnrollmentID: "bundle-current", gen: 3, result: client.WorkspaceEnrollment{ID: "bundle-current", Status: client.WorkspaceEnrollmentConnected}},
		{action: "check", sessionID: "session-current", targetEnrollmentID: "bundle-current", gen: 4, result: client.WorkspaceEnrollment{ID: "bundle-other", Status: client.WorkspaceEnrollmentConnected}},
	} {
		m = applyAll(m, msg)
	}
	if m.enrollment.ID != "bundle-current" || m.statusMsg != "unchanged" {
		t.Fatalf("stale result changed enrollment: %+v status=%q", m.enrollment, m.statusMsg)
	}

	m = applyAll(m, workspaceEnrollmentPresentationMsg{sessionID: "session-current", enrollmentID: "bundle-current", gen: 4, err: errors.New("browser failed\nsecret")})
	if !strings.Contains(m.statusMsg, "browser failed") || strings.Contains(m.statusMsg, "\n") {
		t.Fatalf("browser failure was not visible and sanitized: %q", m.statusMsg)
	}
}

func TestWorkspaceEnrollmentFailedDoesNotPollOrRetry(t *testing.T) {
	control := &workspaceEnrollmentControlFake{}
	m := New(Deps{Ctx: context.Background(), WorkspaceEnrollment: control})
	m.sessionID = "session-1"
	m.enrollment = workspaceEnrollmentState{ID: "bundle-1", Status: client.WorkspaceEnrollmentFailed, controlGen: 3}

	if _, cmd := m.applyWorkspaceEnrollmentPollTick(workspaceEnrollmentPollTickMsg{sessionID: "session-1", enrollmentID: "bundle-1", gen: 3}); cmd != nil {
		t.Fatal("failed enrollment restarted background polling")
	}
	if control.connectCalls != 0 {
		t.Fatalf("failed enrollment rechecked %d times", control.connectCalls)
	}
}

// TestWorkspaceEnrollmentExpiredResetsInsteadOfPollingForever pins the bug
// found while implementing I-7's server-side fix: Expired had no branch at
// all in applyWorkspaceEnrollment, so it fell through to the
// presentationDelivered case and polled an already-dead transaction forever.
func TestWorkspaceEnrollmentExpiredResetsInsteadOfPollingForever(t *testing.T) {
	m, _ := builtinDispatchModel(t, client.Capabilities{WorkspaceEnrollment: true}, false)
	m.enrollment = workspaceEnrollmentState{ID: "bundle-1", Status: client.WorkspaceEnrollmentPending, controlGen: 4, presentationDelivered: true}

	mm, cmd := m.applyWorkspaceEnrollment(workspaceEnrollmentMsg{
		action: "check", sessionID: m.sessionID, targetEnrollmentID: "bundle-1", gen: 4,
		result: client.WorkspaceEnrollment{ID: "bundle-1", Status: client.WorkspaceEnrollmentExpired},
	})
	m = mm.(Model)
	if cmd != nil {
		t.Fatal("expired enrollment kept polling instead of resetting")
	}
	if m.enrollment.ID != "" {
		t.Fatalf("expired enrollment retained stale ID: %+v", m.enrollment)
	}
	if !strings.Contains(m.statusMsg, "expired") {
		t.Fatalf("statusMsg = %q, want an expiry notice", m.statusMsg)
	}
}

func TestWorkspaceEnrollmentDeniedResetsAndAllowsFreshConnect(t *testing.T) {
	m, _ := builtinDispatchModel(t, client.Capabilities{WorkspaceEnrollment: true}, false)
	m.enrollment = workspaceEnrollmentState{ID: "bundle-1", Status: client.WorkspaceEnrollmentPending, controlGen: 4, presentationDelivered: true}

	mm, cmd := m.applyWorkspaceEnrollment(workspaceEnrollmentMsg{
		action: "check", sessionID: m.sessionID, targetEnrollmentID: "bundle-1", gen: 4,
		result: client.WorkspaceEnrollment{ID: "bundle-1", Status: client.WorkspaceEnrollmentDenied},
	})
	m = mm.(Model)
	if cmd != nil {
		t.Fatal("denied enrollment kept polling instead of resetting")
	}
	if m.enrollment.ID != "" {
		t.Fatalf("denied enrollment retained stale ID: %+v", m.enrollment)
	}
	if !strings.Contains(m.statusMsg, "declined") {
		t.Fatalf("statusMsg = %q, want a declined notice", m.statusMsg)
	}

	// A fresh /tools-connect after a denial must start a brand-new enrollment
	// (connectAction), never "check"/"retry" against the now-cleared ID.
	control := &workspaceEnrollmentControlFake{connect: client.WorkspaceEnrollment{ID: "bundle-2", Status: client.WorkspaceEnrollmentPending}}
	m.deps.WorkspaceEnrollment = control
	mm, connectCmd := m.runToolsConnect()
	m = mm.(Model)
	if connectCmd == nil {
		t.Fatal("runToolsConnect returned no command")
	}
	m = applyAll(m, connectCmd())
	if control.connectCalls != 1 {
		t.Fatalf("connect calls = %d, want 1 (a fresh connectAction)", control.connectCalls)
	}
}

func TestWorkspaceEnrollmentPresentationGatesAndSurvivesTransientObservationError(t *testing.T) {
	control := &workspaceEnrollmentControlFake{}
	m := New(Deps{Ctx: t.Context(), WorkspaceEnrollment: control})
	m.sessionID = "session-1"
	m.enrollment = workspaceEnrollmentState{ID: "bundle-1", Status: client.WorkspaceEnrollmentPending, controlGen: 4}
	if _, cmd := m.applyWorkspaceEnrollmentPollTick(workspaceEnrollmentPollTickMsg{sessionID: "session-1", enrollmentID: "bundle-1", gen: 4}); cmd != nil {
		t.Fatal("poll began before browser presentation")
	}

	m.enrollment.presentationDelivered = true
	mm, cmd := m.applyWorkspaceEnrollment(workspaceEnrollmentMsg{action: "check", sessionID: "session-1", targetEnrollmentID: "bundle-1", gen: 4, err: errors.New("temporary outage\nsecret")})
	m = mm.(Model)
	if cmd == nil || !strings.Contains(m.workspaceEnrollmentNotice, "temporary outage") || strings.Contains(m.workspaceEnrollmentNotice, "\n") {
		t.Fatalf("transient observation did not retain sanitized error and re-arm: notice=%q cmd=%v", m.workspaceEnrollmentNotice, cmd != nil)
	}
}

func TestWorkspaceEnrollmentQueuedPresentationIsCancelledBeforeOpen(t *testing.T) {
	for _, supersede := range []bool{false, true} {
		t.Run(fmt.Sprintf("supersede=%t", supersede), func(t *testing.T) {
			opened := 0
			m := New(Deps{Ctx: t.Context(), OpenURL: func(context.Context, string) error { opened++; return nil }})
			m.sessionID = "session-1"
			m.enrollment = workspaceEnrollmentState{ID: "bundle-1", Status: client.WorkspaceEnrollmentPending, controlGen: 4}
			mm, presentationCmd := m.startWorkspaceEnrollmentPresentation("https://private.example/token")
			m = mm.(Model)
			if supersede {
				mm, _ = m.startWorkspaceEnrollmentControl("check")
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

type workspaceEnrollmentControlFake struct {
	connect      client.WorkspaceEnrollment
	connectCalls int
	cancelCalls  int
}

func (f *workspaceEnrollmentControlFake) ConnectWorkspaceServices(context.Context, string) (client.WorkspaceEnrollment, error) {
	f.connectCalls++
	return f.connect, nil
}

func (*workspaceEnrollmentControlFake) RetryWorkspaceEnrollment(context.Context, string, string) (client.WorkspaceEnrollment, error) {
	return client.WorkspaceEnrollment{}, nil
}

func (f *workspaceEnrollmentControlFake) CancelWorkspaceEnrollment(_ context.Context, _ string, id string) (client.WorkspaceEnrollment, error) {
	f.cancelCalls++
	return client.WorkspaceEnrollment{ID: id, Status: client.WorkspaceEnrollmentCancelled}, nil
}

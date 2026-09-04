package ui

import (
	"context"
	"errors"
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
	m := New(Deps{Ctx: context.Background(), WorkspaceEnrollment: control})
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
	m = applyAll(m, cmd())
	if control.connectCalls != 1 || m.enrollment.ID != "bundle-1" {
		t.Fatalf("connect calls/state = %d/%+v", control.connectCalls, m.enrollment)
	}
	if got := stripANSIstr(m.idleFooterLeft()); !strings.Contains(got, "run /tools-connect to recheck") {
		t.Fatalf("footer-left after a pending connect = %q, want manual recheck notice", got)
	}
	if got := stripANSIstr(m.idleFooterLeft()); strings.Contains(got, "https://") || strings.Contains(got, "token-canary") {
		t.Fatalf("footer rendered private presentation data: %s", got)
	}
}

func TestWorkspaceEnrollmentConnectedRechecksAndResubmitsOnce(t *testing.T) {
	control := &workspaceEnrollmentControlFake{connect: client.WorkspaceEnrollment{
		ID: "bundle-1", Status: client.WorkspaceEnrollmentPending,
	}}
	m, send := builtinDispatchModel(t, client.Capabilities{WorkspaceEnrollment: true}, false)
	m.deps.WorkspaceEnrollment = control
	m.pendingInitialPrompt = "list my open pull requests"

	mm, cmd := m.runToolsConnect()
	m = mm.(Model)
	m = applyAll(m, cmd())
	control.connect = client.WorkspaceEnrollment{ID: "bundle-1", Status: client.WorkspaceEnrollmentConnected}

	mm, cmd = m.runToolsConnect()
	m = mm.(Model)
	mm, finalizeCmd := m.Update(cmd())
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
	if m.lastSubmittedPromptText != "list PRs" {
		t.Fatalf("lastSubmittedPromptText = %q", m.lastSubmittedPromptText)
	}

	mm, _ = m.Update(client.StreamErrMsg{Err: errors.New("rpc error: code = FailedPrecondition desc = workspace services must be connected before prompting")})
	m = mm.(Model)
	if m.pendingInitialPrompt != "list PRs" || m.lastSubmittedPromptText != "" {
		t.Fatalf("rejection recovery pending/staged = %q/%q", m.pendingInitialPrompt, m.lastSubmittedPromptText)
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

type workspaceEnrollmentControlFake struct {
	connect      client.WorkspaceEnrollment
	connectCalls int
}

func (f *workspaceEnrollmentControlFake) ConnectWorkspaceServices(context.Context, string) (client.WorkspaceEnrollment, error) {
	f.connectCalls++
	return f.connect, nil
}

func (*workspaceEnrollmentControlFake) RetryWorkspaceEnrollment(context.Context, string, string) (client.WorkspaceEnrollment, error) {
	return client.WorkspaceEnrollment{}, nil
}

func (*workspaceEnrollmentControlFake) CancelWorkspaceEnrollment(context.Context, string, string) (client.WorkspaceEnrollment, error) {
	return client.WorkspaceEnrollment{}, nil
}

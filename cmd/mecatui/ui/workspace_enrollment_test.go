package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestInvariant_mecatui_workspace_enrollment_is_not_permission_approval(t *testing.T) {
	control := &workspaceEnrollmentControlFake{connect: client.WorkspaceEnrollment{
		ID: "bundle-1", Status: client.WorkspaceEnrollmentPending, RequiredServices: 2,
		PresentationURL: "https://provider-private.example/callback?token=token-canary",
	}}
	m := New(Deps{Ctx: context.Background(), WorkspaceEnrollment: control})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30}, client.SessionReadyMsg{
		SessionID: "session-1", Capabilities: client.Capabilities{WorkspaceEnrollment: true},
	})

	if m.phase != phaseWorkspaceEnrollment || m.prompt.Focused() || m.modal != nil {
		t.Fatalf("enrollment gate phase/focus/modal = %v/%t/%T, want distinct gate with disabled prompt and no approval modal", m.phase, m.prompt.Focused(), m.modal)
	}
	view := stripANSIstr(m.View().Content)
	for _, want := range []string{"Connect workspace services", "Prompt input is unavailable"} {
		if !strings.Contains(view, want) {
			t.Fatalf("workspace enrollment view missing %q:\n%s", want, view)
		}
	}
	for _, forbidden := range []string{"Allow", "Always", "Deny", "permission"} {
		if strings.Contains(view, forbidden) {
			t.Fatalf("workspace enrollment rendered permission control %q:\n%s", forbidden, view)
		}
	}

	mm, cmd := m.onWorkspaceEnrollmentKey(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("Connect workspace services action returned no command")
	}
	msg := cmd()
	m = applyAll(m, msg)
	if control.connectCalls != 1 || m.enrollment.ID != "bundle-1" || m.phase != phaseWorkspaceEnrollment {
		t.Fatalf("connect calls/state/phase = %d/%+v/%v", control.connectCalls, m.enrollment, m.phase)
	}
	if got := stripANSIstr(m.View().Content); !strings.Contains(got, "2 services") {
		t.Fatalf("bundle progress missing service count:\n%s", got)
	}
	if got := stripANSIstr(m.View().Content); strings.Contains(got, "https://") || strings.Contains(got, "provider-private") || strings.Contains(got, "token-canary") {
		t.Fatalf("enrollment rendered private presentation data:\n%s", got)
	}

	blocked, blockedCmd := m.submitPrompt()
	if blockedCmd != nil || blocked.(Model).phase != phaseWorkspaceEnrollment {
		t.Fatal("prompt submission escaped the frozen-catalogue enrollment gate")
	}
	stale, _ := m.applyWorkspaceEnrollment(workspaceEnrollmentMsg{
		action: "check", sessionID: "foreign-session", targetEnrollmentID: "bundle-1",
		result: client.WorkspaceEnrollment{Status: client.WorkspaceEnrollmentConnected},
	})
	staleModel := stale.(Model)
	if staleModel.phase != phaseWorkspaceEnrollment || staleModel.enrollment.ID != "bundle-1" {
		t.Fatal("foreign/stale enrollment completion escaped correlation gate")
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

package ui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// workspaceEnrollmentState is distinct from permission approval and per-tool MCP
// authorization. It retains only safe whole-bundle correlation and counts.
type workspaceEnrollmentState struct {
	ID               string
	Status           client.WorkspaceEnrollmentStatus
	RequiredServices uint32
	busy             bool
	err              string
}

type workspaceEnrollmentMsg struct {
	result             client.WorkspaceEnrollment
	action             string
	sessionID          string
	targetEnrollmentID string
	err                error
}

func workspaceEnrollmentCmd(ctx context.Context, control client.WorkspaceEnrollmentController, sessionID, enrollmentID, action string) tea.Cmd {
	return func() tea.Msg {
		var result client.WorkspaceEnrollment
		var err error
		switch action {
		case "connect", "check":
			result, err = control.ConnectWorkspaceServices(ctx, sessionID)
		case "retry":
			result, err = control.RetryWorkspaceEnrollment(ctx, sessionID, enrollmentID)
		case "cancel":
			result, err = control.CancelWorkspaceEnrollment(ctx, sessionID, enrollmentID)
		default:
			err = fmt.Errorf("unknown workspace enrollment action")
		}
		return workspaceEnrollmentMsg{result: result, action: action, sessionID: sessionID, targetEnrollmentID: enrollmentID, err: err}
	}
}

func (m Model) onWorkspaceEnrollmentKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.deps.WorkspaceEnrollment == nil || m.enrollment.busy {
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keys.Close):
		return m, nil
	case msg.String() == "c" && m.enrollment.ID == "":
		m.enrollment.busy = true
		m.enrollment.err = ""
		return m, workspaceEnrollmentCmd(m.deps.Ctx, m.deps.WorkspaceEnrollment, m.sessionID, "", "connect")
	case msg.String() == "r" && m.enrollment.ID != "":
		m.enrollment.busy = true
		action := "check"
		if m.enrollment.err != "" {
			action = "retry"
		}
		m.enrollment.err = ""
		return m, workspaceEnrollmentCmd(m.deps.Ctx, m.deps.WorkspaceEnrollment, m.sessionID, m.enrollment.ID, action)
	case msg.String() == "x" && m.enrollment.ID != "":
		m.enrollment.busy = true
		m.enrollment.err = ""
		return m, workspaceEnrollmentCmd(m.deps.Ctx, m.deps.WorkspaceEnrollment, m.sessionID, m.enrollment.ID, "cancel")
	default:
		return m, nil
	}
}

func (m Model) applyWorkspaceEnrollment(msg workspaceEnrollmentMsg) (tea.Model, tea.Cmd) {
	if msg.sessionID != m.sessionID || msg.action == "connect" && m.enrollment.ID != "" || msg.action != "connect" && msg.targetEnrollmentID != m.enrollment.ID {
		return m, nil
	}
	m.enrollment.busy = false
	if msg.err != nil {
		m.enrollment.err = "workspace enrollment failed"
		return m, nil
	}
	// Strip presentation data at the reducer boundary. It is never retained in the
	// model, rendered, logged, or included in an error.
	presentationURL := msg.result.PresentationURL
	m.enrollment.ID = msg.result.ID
	m.enrollment.Status = msg.result.Status
	m.enrollment.RequiredServices = msg.result.RequiredServices
	m.enrollment.err = ""
	switch msg.result.Status {
	case client.WorkspaceEnrollmentConnected:
		m.enrollment = workspaceEnrollmentState{}
		m.phase = phaseIdle
		focusCmd := m.prompt.Focus()
		m.statusMsg = "workspace services connected"
		cmd := tea.Batch(focusCmd, (&m).armLiveFeed())
		if p := strings.TrimSpace(m.pendingInitialPrompt); p != "" {
			m.pendingInitialPrompt = ""
			m.prompt.Rewrite(p)
			mm, submitCmd := m.submitPrompt()
			return mm, tea.Batch(cmd, submitCmd)
		}
		return m, cmd
	case client.WorkspaceEnrollmentFailed:
		m.enrollment.err = "workspace enrollment failed"
	case client.WorkspaceEnrollmentCancelled:
		m.enrollment.ID = ""
	}
	if presentationURL != "" && m.deps.OpenURL != nil {
		return m, func() tea.Msg {
			if err := m.deps.OpenURL(m.deps.Ctx, presentationURL); err != nil {
				return workspaceEnrollmentMsg{action: "open", err: err}
			}
			return nil
		}
	}
	return m, nil
}

func (m Model) renderWorkspaceEnrollment() string {
	progress := "All configured services must connect as one bundle."
	if m.enrollment.RequiredServices > 0 {
		progress = fmt.Sprintf("%d services must connect as one bundle.", m.enrollment.RequiredServices)
	}
	body := "Connect workspace services\n\n" + progress + "\nNo service is available until the complete catalogue is admitted.\n\nPrompt input is unavailable until enrollment completes."
	switch {
	case m.enrollment.busy:
		body += "\n\nConnecting workspace services…"
	case m.enrollment.err != "" && m.enrollment.ID == "":
		body += "\n\n" + m.enrollment.err + "   [c] Retry connection"
	case m.enrollment.err != "":
		body += "\n\n" + m.enrollment.err + "   [r] Retry bundle   [x] Cancel bundle"
	case m.enrollment.ID == "":
		body += "\n\n[c] Connect workspace services"
	default:
		body += "\n\nWorkspace service connection pending.   [r] Recheck bundle   [x] Cancel bundle"
	}
	return centerCard(m.deps.Theme, body, m.width, m.vp.Height())
}

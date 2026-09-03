package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// connectAction is the workspace-enrollment action used by /tools-connect.
const connectAction = "connect"

const workspaceEnrollmentPollInterval = 3 * time.Second

// workspaceEnrollmentPollTickMsg is bound to the session and pending enrollment
// it observes, so stale timer deliveries cannot affect a replacement session.
type workspaceEnrollmentPollTickMsg struct {
	sessionID, enrollmentID string
}

func workspaceEnrollmentPollTickCmd(sessionID, enrollmentID string) tea.Cmd {
	return tea.Tick(workspaceEnrollmentPollInterval, func(time.Time) tea.Msg {
		return workspaceEnrollmentPollTickMsg{sessionID: sessionID, enrollmentID: enrollmentID}
	})
}

func (m Model) applyWorkspaceEnrollmentPollTick(msg workspaceEnrollmentPollTickMsg) (tea.Model, tea.Cmd) {
	if msg.sessionID != m.sessionID || msg.enrollmentID == "" || msg.enrollmentID != m.enrollment.ID ||
		m.enrollment.Status != client.WorkspaceEnrollmentPending || m.enrollment.busy || m.deps.WorkspaceEnrollment == nil {
		return m, nil
	}
	m.enrollment.busy = true
	return m, workspaceEnrollmentCmd(m.deps.Ctx, m.deps.WorkspaceEnrollment, m.sessionID, m.enrollment.ID, "check")
}

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
		case connectCommand, "check":
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

// runToolsConnect drives bundled workspace-services enrollment on demand. A
// pending bundle is rechecked (or retried after a failed response) rather than
// starting a second bundle.
func (m Model) runToolsConnect() (tea.Model, tea.Cmd) {
	if m.deps.WorkspaceEnrollment == nil {
		m.statusMsg = m.deps.Theme.Style("warning").Render("/tools-connect is not available on this server")
		return m, nil
	}
	if m.sessionID == "" {
		m.statusMsg = m.deps.Theme.Style("warning").Render("cannot connect workspace services: no active session")
		return m, nil
	}
	if m.enrollment.busy {
		m.statusMsg = m.deps.Theme.Style("warning").Render("workspace services connection is already in progress")
		return m, nil
	}
	action := connectAction
	switch {
	case m.enrollment.ID != "" && m.enrollment.err != "":
		action = "retry"
	case m.enrollment.ID != "":
		action = "check"
	}
	m.enrollment.busy = true
	m.enrollment.err = ""
	m.statusMsg = m.deps.Theme.Style("muted").Render("connecting workspace services…")
	return m, workspaceEnrollmentCmd(m.deps.Ctx, m.deps.WorkspaceEnrollment, m.sessionID, m.enrollment.ID, action)
}

// runToolsCancel cancels the caller-owned pending bundle.
func (m Model) runToolsCancel() (tea.Model, tea.Cmd) {
	if m.deps.WorkspaceEnrollment == nil || m.enrollment.ID == "" {
		m.statusMsg = m.deps.Theme.Style("warning").Render("no pending workspace-services connection to cancel")
		return m, nil
	}
	if m.enrollment.busy {
		m.statusMsg = m.deps.Theme.Style("warning").Render("workspace services connection is already in progress")
		return m, nil
	}
	m.enrollment.busy = true
	m.enrollment.err = ""
	m.statusMsg = m.deps.Theme.Style("muted").Render("cancelling workspace services connection…")
	return m, workspaceEnrollmentCmd(m.deps.Ctx, m.deps.WorkspaceEnrollment, m.sessionID, m.enrollment.ID, "cancel")
}

// applyWorkspaceEnrollment reduces the direct RPC response from /tools-connect
// or /tools-cancel. Pending enrollments are observed periodically; failures remain
// explicit /tools-connect retries.
func (m Model) applyWorkspaceEnrollment(msg workspaceEnrollmentMsg) (tea.Model, tea.Cmd) {
	if msg.sessionID != m.sessionID || msg.action == connectAction && m.enrollment.ID != "" || msg.action != connectAction && msg.targetEnrollmentID != m.enrollment.ID {
		return m, nil
	}
	m.enrollment.busy = false
	if msg.err != nil {
		m.enrollment.err = "workspace enrollment failed"
		m.statusMsg = m.deps.Theme.Style("warning").Render(m.enrollment.err)
		return m, nil
	}
	// Presentation data is ephemeral: open it but never retain, render, or log it.
	presentationURL := msg.result.PresentationURL
	m.enrollment.ID = msg.result.ID
	m.enrollment.Status = msg.result.Status
	m.enrollment.RequiredServices = msg.result.RequiredServices
	m.enrollment.err = ""
	if msg.result.Status == client.WorkspaceEnrollmentConnected {
		return m.finalizeWorkspaceEnrollmentConnected()
	}
	if msg.result.Status == client.WorkspaceEnrollmentCancelled {
		m.enrollment = workspaceEnrollmentState{}
		m.workspaceEnrollmentNotice = ""
		m.statusMsg = "workspace services connection cancelled"
		return m, nil
	}
	if msg.result.Status == client.WorkspaceEnrollmentFailed {
		m.enrollment.err = "workspace enrollment failed"
		m.workspaceEnrollmentNotice = "workspace services connection failed — run /tools-connect to retry"
		return m, nil
	}
	m.workspaceEnrollmentNotice = "waiting for browser consent — you'll be notified when connected"
	pollCmd := workspaceEnrollmentPollTickCmd(m.sessionID, m.enrollment.ID)
	// Presentation data is available only for an interactive connect/retry, never
	// a background observation; it is opened and immediately discarded.
	if presentationURL != "" && msg.action != "check" && m.deps.OpenURL != nil {
		return m, tea.Batch(pollCmd, func() tea.Msg {
			if err := m.deps.OpenURL(m.deps.Ctx, presentationURL); err != nil {
				return workspaceEnrollmentMsg{action: "open", err: err}
			}
			return nil
		})
	}
	return m, pollCmd
}

// finalizeWorkspaceEnrollmentConnected is the shared completion path. Clearing
// pendingInitialPrompt before submit gives exactly-once initial/rejected prompt
// resubmission even if a later manual recheck repeats the connected response.
func (m Model) finalizeWorkspaceEnrollmentConnected() (tea.Model, tea.Cmd) {
	m.enrollment = workspaceEnrollmentState{}
	m.workspaceEnrollmentNotice = ""
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
}

// friendlyWorkspaceEnrollmentRejection replaces the one pre-prompt server gate
// rejection with the built-in command that resolves it.
func friendlyWorkspaceEnrollmentRejection(raw string) string {
	if isWorkspaceEnrollmentRejection(raw) {
		return "workspace services aren't connected — run /tools-connect to enable protected tools before prompting"
	}
	return raw
}

func isWorkspaceEnrollmentRejection(raw string) bool {
	return strings.Contains(raw, "workspace services must be connected before prompting")
}

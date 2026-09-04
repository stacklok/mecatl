package ui

import (
	"context"
	"errors"
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
	gen                     uint64
}

func workspaceEnrollmentPollTickCmd(sessionID, enrollmentID string, gen uint64) tea.Cmd {
	return tea.Tick(workspaceEnrollmentPollInterval, func(time.Time) tea.Msg {
		return workspaceEnrollmentPollTickMsg{sessionID: sessionID, enrollmentID: enrollmentID, gen: gen}
	})
}

func (m Model) applyWorkspaceEnrollmentPollTick(msg workspaceEnrollmentPollTickMsg) (tea.Model, tea.Cmd) {
	if !m.currentWorkspaceEnrollmentMessage(msg.sessionID, msg.enrollmentID, msg.gen) || !m.enrollment.presentationDelivered ||
		m.enrollment.Status != client.WorkspaceEnrollmentPending || m.enrollment.busy || m.deps.WorkspaceEnrollment == nil {
		return m, nil
	}
	m.enrollment.busy = true
	return m.startWorkspaceEnrollmentControl("check")
}

// workspaceEnrollmentState is distinct from permission approval and per-tool MCP
// authorization. It retains only safe whole-bundle correlation and counts.
type workspaceEnrollmentState struct {
	ID                    string
	Status                client.WorkspaceEnrollmentStatus
	RequiredServices      uint32
	busy                  bool
	controlGen            uint64
	controlCancel         context.CancelFunc
	presentationCancel    context.CancelFunc
	presentationDelivered bool
	err                   string
}

type workspaceEnrollmentMsg struct {
	result             client.WorkspaceEnrollment
	action             string
	sessionID          string
	targetEnrollmentID string
	gen                uint64
	err                error
}

type workspaceEnrollmentPresentationMsg struct {
	sessionID, enrollmentID string
	gen                     uint64
	err                     error
}

func workspaceEnrollmentPresentationCmd(ctx context.Context, openURL func(context.Context, string) error, sessionID, enrollmentID string, gen uint64, presentationURL string) tea.Cmd {
	return func() tea.Msg {
		if err := ctx.Err(); err != nil {
			return workspaceEnrollmentPresentationMsg{sessionID: sessionID, enrollmentID: enrollmentID, gen: gen, err: err}
		}
		if err := openURL(ctx, presentationURL); err != nil {
			return workspaceEnrollmentPresentationMsg{sessionID: sessionID, enrollmentID: enrollmentID, gen: gen, err: err}
		}
		return workspaceEnrollmentPresentationMsg{sessionID: sessionID, enrollmentID: enrollmentID, gen: gen}
	}
}

func (m Model) startWorkspaceEnrollmentPresentation(presentationURL string) (tea.Model, tea.Cmd) {
	if m.enrollment.presentationCancel != nil {
		m.enrollment.presentationCancel()
	}
	ctx, cancel := context.WithCancel(m.deps.Ctx)
	m.enrollment.presentationCancel = cancel
	return m, workspaceEnrollmentPresentationCmd(ctx, m.deps.OpenURL, m.sessionID, m.enrollment.ID, m.enrollment.controlGen, presentationURL)
}

func (m Model) applyWorkspaceEnrollmentPresentation(msg workspaceEnrollmentPresentationMsg) (tea.Model, tea.Cmd) {
	if !m.currentWorkspaceEnrollmentMessage(msg.sessionID, msg.enrollmentID, msg.gen) || m.enrollment.Status != client.WorkspaceEnrollmentPending {
		return m, nil
	}
	if m.enrollment.presentationCancel != nil {
		m.enrollment.presentationCancel()
		m.enrollment.presentationCancel = nil
	}
	if msg.err != nil {
		if !errors.Is(msg.err, context.Canceled) {
			m.enrollment.err = oneLine(sanitizeTerminal(msg.err.Error()))
			m.statusMsg = m.deps.Theme.Style("warning").Render("could not open browser: " + m.enrollment.err)
		}
		return m, nil
	}
	m.enrollment.presentationDelivered = true
	m.enrollment.err = ""
	m.workspaceEnrollmentNotice = "waiting for browser consent — you'll be notified when connected"
	return m, workspaceEnrollmentPollTickCmd(msg.sessionID, msg.enrollmentID, msg.gen)
}

func workspaceEnrollmentCmd(ctx context.Context, control client.WorkspaceEnrollmentController, sessionID, enrollmentID, action string, gen uint64) tea.Cmd {
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
		return workspaceEnrollmentMsg{result: result, action: action, sessionID: sessionID, targetEnrollmentID: enrollmentID, gen: gen, err: err}
	}
}

func (m Model) currentWorkspaceEnrollmentMessage(sessionID, enrollmentID string, gen uint64) bool {
	return sessionID == m.sessionID && enrollmentID != "" && enrollmentID == m.enrollment.ID && gen == m.enrollment.controlGen
}

func (m Model) startWorkspaceEnrollmentControl(action string) (tea.Model, tea.Cmd) {
	if m.enrollment.controlCancel != nil {
		m.enrollment.controlCancel()
	}
	if m.enrollment.presentationCancel != nil {
		m.enrollment.presentationCancel()
		m.enrollment.presentationCancel = nil
	}
	m.enrollment.controlGen++
	gen := m.enrollment.controlGen
	ctx, cancel := context.WithCancel(m.deps.Ctx)
	m.enrollment.controlCancel = cancel
	return m, workspaceEnrollmentCmd(ctx, m.deps.WorkspaceEnrollment, m.sessionID, m.enrollment.ID, action, gen)
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
	return m.startWorkspaceEnrollmentControl(action)
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
	return m.startWorkspaceEnrollmentControl("cancel")
}

// applyWorkspaceEnrollment reduces the direct RPC response from /tools-connect
// or /tools-cancel. Pending enrollments are observed periodically; failures remain
// explicit /tools-connect retries.
//
//nolint:gocyclo // status/action dispatch over the enrollment projection states; inherent.
func (m Model) applyWorkspaceEnrollment(msg workspaceEnrollmentMsg) (tea.Model, tea.Cmd) {
	current := msg.sessionID == m.sessionID && msg.gen == m.enrollment.controlGen
	if msg.action == connectAction {
		current = current && m.enrollment.ID == ""
	} else {
		current = current && msg.targetEnrollmentID != "" && msg.targetEnrollmentID == m.enrollment.ID
	}
	if !current || msg.err == nil && msg.action != connectAction && msg.result.ID != msg.targetEnrollmentID {
		return m, nil
	}
	m.enrollment.busy = false
	if m.enrollment.controlCancel != nil {
		m.enrollment.controlCancel()
		m.enrollment.controlCancel = nil
	}
	if msg.err != nil {
		m.enrollment.err = oneLine(sanitizeTerminal(msg.err.Error()))
		m.statusMsg = m.deps.Theme.Style("warning").Render(m.enrollment.err)
		if msg.action == "check" && m.enrollment.Status == client.WorkspaceEnrollmentPending && m.enrollment.presentationDelivered {
			return m, workspaceEnrollmentPollTickCmd(m.sessionID, m.enrollment.ID, m.enrollment.controlGen)
		}
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
		if m.enrollment.presentationCancel != nil {
			m.enrollment.presentationCancel()
		}
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
	if m.enrollment.presentationDelivered {
		m.workspaceEnrollmentNotice = "waiting for browser consent — you'll be notified when connected"
		return m, workspaceEnrollmentPollTickCmd(m.sessionID, m.enrollment.ID, m.enrollment.controlGen)
	}
	// Presentation data is available only for an interactive connect/retry, never
	// a background observation; it is opened and immediately discarded.
	if presentationURL != "" && msg.action != "check" && m.deps.OpenURL != nil {
		return m.startWorkspaceEnrollmentPresentation(presentationURL)
	}
	if m.deps.OpenURL == nil {
		m.enrollment.err = "browser opening is unavailable"
		m.statusMsg = m.deps.Theme.Style("warning").Render(m.enrollment.err)
	}
	return m, nil
}

// finalizeWorkspaceEnrollmentConnected is the shared completion path. Clearing
// pendingInitialPrompt before submit gives exactly-once initial/rejected prompt
// resubmission even if a later manual recheck repeats the connected response.
func (m Model) finalizeWorkspaceEnrollmentConnected() (tea.Model, tea.Cmd) {
	if m.enrollment.controlCancel != nil {
		m.enrollment.controlCancel()
	}
	if m.enrollment.presentationCancel != nil {
		m.enrollment.presentationCancel()
	}
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

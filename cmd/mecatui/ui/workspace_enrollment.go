package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
)

// connectAction is the workspace-enrollment action used by /tools-connect.
const connectAction = "connect"

const workspaceEnrollmentPollInterval = 3 * time.Second

// workspaceEnrollmentTimeout bounds each cooperative unary enrollment action.
// It is a variable only so tests can exercise the fixed production duration.
var workspaceEnrollmentTimeout = 30 * time.Second

const workspaceEnrollmentTimeoutNotice = "connecting workspace tools timed out; the result may be uncertain — start a new session with /clear, then run /tools-connect"

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
			m.enrollment.err = oneLine(terminaltext.Sanitize(msg.err.Error()))
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
		if ctxErr := ctx.Err(); ctxErr != nil {
			// A controller may finish concurrently with cancellation after the
			// server has committed. The client context remains authoritative: never
			// publish a result observed only after its operation boundary expired.
			result = client.WorkspaceEnrollment{}
			err = ctxErr
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
	ctx, cancel := context.WithTimeout(m.deps.Ctx, workspaceEnrollmentTimeout)
	m.enrollment.controlCancel = cancel
	return m, workspaceEnrollmentCmd(ctx, m.deps.WorkspaceEnrollment, m.sessionID, m.enrollment.ID, action, gen)
}

// runToolsConnect drives bundled workspace-services enrollment on demand. A
// pending bundle is rechecked (or retried after a failed response) rather than
// starting a second bundle.
func (m Model) runToolsConnect() (tea.Model, tea.Cmd) {
	if m.deps.WorkspaceEnrollment == nil {
		m.workspaceEnrollmentNotice = "workspace tools cannot be connected on this server"
		return m, nil
	}
	if m.sessionID == "" {
		m.workspaceEnrollmentNotice = "cannot connect workspace tools: no active session"
		return m, nil
	}
	if m.phase != phaseIdle {
		m.workspaceEnrollmentNotice = "connect workspace tools before sending a prompt or after the current response finishes"
		return m, nil
	}
	if m.enrollment.busy {
		m.workspaceEnrollmentNotice = "workspace tools are already connecting"
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
	// workspaceEnrollmentNotice, not statusMsg: idleFooterLeft gives the notice
	// priority over statusMsg (issue: a /tools-connect outcome written to
	// statusMsg renders invisibly whenever the ambient "not connected" notice
	// is also set — which it always is, right up until this call clears it).
	m.workspaceEnrollmentNotice = "connecting workspace tools…"
	return m.startWorkspaceEnrollmentControl(action)
}

// runToolsCancel cancels the caller-owned pending bundle.
func (m Model) runToolsCancel() (tea.Model, tea.Cmd) {
	if m.deps.WorkspaceEnrollment == nil || m.enrollment.ID == "" {
		m.workspaceEnrollmentNotice = "there is no workspace tool connection to cancel"
		return m, nil
	}
	if m.enrollment.busy {
		m.workspaceEnrollmentNotice = "workspace tools are already connecting"
		return m, nil
	}
	m.enrollment.busy = true
	m.enrollment.err = ""
	// workspaceEnrollmentNotice, not statusMsg — see runToolsConnect.
	m.workspaceEnrollmentNotice = "cancelling workspace tool connection…"
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
		switch {
		case errors.Is(msg.err, context.DeadlineExceeded):
			// The unary RPC may have committed before its response was lost. Keep
			// the opaque correlation, but stop this same-session control loop: an
			// automatic retry could duplicate an overloaded connect operation.
			m.enrollment.presentationDelivered = false
			m.enrollment.err = workspaceEnrollmentTimeoutNotice
			m.workspaceEnrollmentNotice = workspaceEnrollmentTimeoutNotice
			return m, nil
		case errors.Is(msg.err, context.Canceled):
			// Parent/session replacement cancellation is expected teardown, not a
			// deadline outcome. Leave it silent and prevent a queued poll re-arm.
			m.enrollment.presentationDelivered = false
			return m, nil
		case isWorkspaceEnrollmentCollision(msg.err.Error()):
			_, notice, statusMsg := terminalWorkspaceEnrollmentNotice(client.WorkspaceEnrollmentFailed)
			m.enrollment = workspaceEnrollmentState{Status: client.WorkspaceEnrollmentFailed, controlGen: m.enrollment.controlGen}
			m.workspaceEnrollmentNotice = notice
			m.statusMsg = statusMsg
			return m, nil
		default:
			m.enrollment.err = oneLine(terminaltext.Sanitize(msg.err.Error()))
			m.workspaceEnrollmentNotice = m.enrollment.err
			if msg.action == "check" && m.enrollment.Status == client.WorkspaceEnrollmentPending && m.enrollment.presentationDelivered {
				return m, workspaceEnrollmentPollTickCmd(m.sessionID, m.enrollment.ID, m.enrollment.controlGen)
			}
			return m, nil
		}
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
	if terminal, notice, statusMsg := terminalWorkspaceEnrollmentNotice(msg.result.Status); terminal {
		// Clear the transaction, retaining only its terminal status and generation.
		// A new /tools-connect starts fresh; old replies remain stale.
		if m.enrollment.presentationCancel != nil {
			m.enrollment.presentationCancel()
		}
		m.enrollment = workspaceEnrollmentState{Status: msg.result.Status, controlGen: m.enrollment.controlGen}
		m.workspaceEnrollmentNotice = notice
		m.statusMsg = statusMsg
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
		m.workspaceEnrollmentNotice = m.enrollment.err
	}
	return m, nil
}

// terminalWorkspaceEnrollmentNotice reports whether status is a terminal,
// non-connected outcome and — if so — the ambient notice and status-line copy
// to show for it. Every terminal status gets its own accurate wording rather
// than lumping them under one generic message, so a user who explicitly
// cancelled sees different copy than one whose GitHub consent was denied or
// whose enrollment simply timed out.
//
//nolint:unparam // notice is always "" today (statusMsg carries every current terminal message); kept as a symmetric slot for a future terminal status that needs both.
func terminalWorkspaceEnrollmentNotice(status client.WorkspaceEnrollmentStatus) (terminal bool, notice, statusMsg string) {
	switch status {
	case client.WorkspaceEnrollmentCancelled:
		return true, "", "workspace tool connection cancelled"
	case client.WorkspaceEnrollmentDenied:
		return true, "", "workspace tool connection was declined — run /tools-connect to try again"
	case client.WorkspaceEnrollmentExpired:
		return true, "", "workspace tool connection expired — run /tools-connect to try again"
	case client.WorkspaceEnrollmentFailed:
		return true, "", "workspace tool connection failed — run /tools-connect to retry"
	default:
		return false, "", ""
	}
}

// finalizeWorkspaceEnrollmentConnected is the shared completion path. It consumes
// a matching recovery before resubmitting, so either a later connected response or
// a replacement draft cannot replay the rejected prompt twice.
func (m Model) finalizeWorkspaceEnrollmentConnected() (tea.Model, tea.Cmd) {
	if m.enrollment.controlCancel != nil {
		m.enrollment.controlCancel()
	}
	if m.enrollment.presentationCancel != nil {
		m.enrollment.presentationCancel()
	}
	m.enrollment = workspaceEnrollmentState{Status: client.WorkspaceEnrollmentConnected, controlGen: m.enrollment.controlGen}
	m.workspaceEnrollmentNotice = ""
	focusCmd := m.prompt.Focus()
	m.statusMsg = "workspace tools connected"
	cmd := tea.Batch(focusCmd, (&m).armLiveFeed())
	if r := m.promptRecovery; r != nil {
		// The recovered draft must still be the source-session draft. A changed
		// textarea is a replacement composition and is deliberately never replayed.
		m.promptRecovery = nil
		if r.autoReplay && r.sessionID == m.sessionID && strings.TrimSpace(m.prompt.Value()) == r.text {
			mm, submitCmd := m.submitPrompt()
			return mm, tea.Batch(cmd, submitCmd)
		}
	}
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
		return "workspace tools aren't connected — run /tools-connect before sending a prompt to enable protected tools"
	}
	return raw
}

func isWorkspaceEnrollmentCollision(raw string) bool {
	return (strings.Contains(raw, "workspace tool ") &&
		strings.Contains(raw, "conflicts with an existing registration") ||
		strings.Contains(raw, "workspace enrollment registration collision")) &&
		strings.Contains(raw, "correct the broker configuration and retry")
}

func isWorkspaceEnrollmentRejection(raw string) bool {
	return strings.Contains(raw, "workspace services must be connected before prompting")
}

package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

const mcpAuthorizationStatusPending = "pending"

// Production polling stays fixed; a variable lets lifecycle tests execute the
// actual Bubble Tea handoff without waiting three seconds per assertion.
var mcpAuthorizationPollInterval = 3 * time.Second

// mcpAuthorizationFirstEventTimeout bounds how long a control (recheck/cancel)
// waits for its FIRST stream event. A recheck/cancel call normally completes in
// well under a second (the server processes it synchronously and closes the
// stream right after, unless it started a granted continuation). If the
// underlying transport silently drops the stream's data or close frame — a
// stalled port-forward or ngrok hop, observed live — this call would otherwise
// hang forever: startMCPAuthorizationControl's context has no deadline, and
// applyMCPAuthorizationPollTick's tick handler does not reschedule itself while
// authorization.pollBusy is true, so a single lost signal permanently kills
// polling until the user does something unrelated that happens to reset state.
// This timer cancels the call's context if no event arrives in time, which
// surfaces as an ordinary stream error (mcpAuthorizationErrorMsg) — a path that
// already resets pollBusy and reschedules the next tick. It is stopped as soon
// as the first event/error/close is observed, so it never bounds the drain of
// a genuinely long-running granted continuation. A var, not a const, so tests
// can shrink it rather than waiting out the real duration.
var mcpAuthorizationFirstEventTimeout = 10 * time.Second

// mcpAuthorizationPollTickMsg is bound to the authorization control generation.
// It cannot recheck a replacement authorization or resurrect a cancelled control.
type mcpAuthorizationPollTickMsg struct {
	sessionID, authorizationID string
	gen                        uint64
}

func mcpAuthorizationPollTickCmd(sessionID, authorizationID string, gen uint64) tea.Cmd {
	return tea.Tick(mcpAuthorizationPollInterval, func(time.Time) tea.Msg {
		return mcpAuthorizationPollTickMsg{sessionID: sessionID, authorizationID: authorizationID, gen: gen}
	})
}

func (m Model) applyMCPAuthorizationPollTick(msg mcpAuthorizationPollTickMsg) (tea.Model, tea.Cmd) {
	if !m.currentAuthorizationMessage(msg.sessionID, msg.authorizationID, msg.gen) || !m.authorization.polling ||
		m.authorization.pollBusy || m.phase != phaseAuthorizing {
		return m, nil
	}
	m.authorization.pollBusy = true
	mm, cmd := m.startMCPAuthorizationControl(false)
	updated := mm.(Model)
	return updated, tea.Batch(cmd, mcpAuthorizationPollTickCmd(updated.sessionID, updated.authorization.authorizationID, updated.authorization.controlGen))
}

// mcpAuthorizationState is intentionally distinct from permission approval.
// It holds safe correlation only; presentation URLs are fetched on demand and
// never retained in model state or reconstructed from replayed events.
type mcpAuthorizationState struct {
	authorizationID    string
	displayName        string
	callID             string
	errorText          string
	controlGen         uint64
	controlCancel      context.CancelFunc
	presentationCancel context.CancelFunc
	controlStream      *client.EventStream
	firstEventTimer    *time.Timer // bounds the wait for a control's first event; see mcpAuthorizationFirstEventTimeout.
	runningControlGen  uint64
	polling            bool // browser presentation succeeded; background observations are active
	pollBusy           bool // a poll control request is in flight
}

// stopFirstEventTimer cancels any outstanding first-event watchdog. Idempotent.
func (a *mcpAuthorizationState) stopFirstEventTimer() {
	if a.firstEventTimer != nil {
		a.firstEventTimer.Stop()
		a.firstEventTimer = nil
	}
}

func (m Model) applyMCPAuthorization(msg client.MCPAuthorizationMsg) (tea.Model, tea.Cmd) {
	if msg.Status != mcpAuthorizationStatusPending {
		reenteredRunning := false
		if m.authorization.authorizationID == msg.AuthorizationID {
			m.authorization.stopFirstEventTimer()
			if m.authorization.presentationCancel != nil {
				m.authorization.presentationCancel()
				m.authorization.presentationCancel = nil
			}
			m.authorization.errorText = ""
			m.authorization.polling = false
			m.authorization.pollBusy = false
			if m.phase == phaseAuthorizing {
				m.phase = phaseRunning
				reenteredRunning = true
			}
		}
		m.conv.addNotice(mcpAuthorizationNotice(msg))
		mm, cmd := m.afterEvent()
		if reenteredRunning {
			cmd = tea.Batch(cmd, m.sp.Tick)
		}
		return mm, cmd
	}
	if m.authorization.controlCancel != nil {
		m.authorization.controlCancel()
	}
	m.authorization.stopFirstEventTimer()
	if m.authorization.presentationCancel != nil {
		m.authorization.presentationCancel()
	}
	polling := m.authorization.authorizationID == msg.AuthorizationID && m.authorization.polling
	m.authorization = mcpAuthorizationState{
		authorizationID: msg.AuthorizationID,
		displayName:     oneLine(sanitizeTerminal(msg.DisplayName)),
		callID:          msg.CallID,
		controlGen:      m.authorization.controlGen + 1,
		polling:         polling,
	}
	m.authorizationEvents = nil
	m.phase = phaseAuthorizing
	m.activeTool = ""
	m.toolProgress = ""
	mm, cmd := m.afterEvent()
	if polling {
		cmd = tea.Batch(cmd, mcpAuthorizationPollTickCmd(m.sessionID, msg.AuthorizationID, m.authorization.controlGen))
	}
	return mm, cmd
}

func (m Model) onMCPAuthorizationKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.authorization.authorizationID == "" || m.deps.MCPAuthorization == nil {
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keys.Close):
		return m, nil // cancellation is explicit; escape cannot silently resolve it.
	case key.Matches(msg, m.keys.Choose):
		if m.deps.OpenURL == nil {
			m.authorization.errorText = "browser opening is unavailable"
			return m, nil
		}
		return m.startMCPAuthorizationPresentation(false)
	case key.Matches(msg, m.keys.CopySelection):
		return m.startMCPAuthorizationPresentation(true)
	case key.Matches(msg, m.keys.CancelChild):
		m.authorization.polling = false
		m.authorization.pollBusy = false
		return m.startMCPAuthorizationControl(true)
	default:
		return m, nil
	}
}

func (m Model) startMCPAuthorizationPresentation(copyLink bool) (tea.Model, tea.Cmd) {
	if m.authorization.presentationCancel != nil {
		m.authorization.presentationCancel()
	}
	ctx, cancel := context.WithCancel(m.deps.Ctx)
	m.authorization.presentationCancel = cancel
	sessionID, authorizationID, gen := m.sessionID, m.authorization.authorizationID, m.authorization.controlGen
	return m, func() tea.Msg {
		if err := ctx.Err(); err != nil {
			return mcpAuthorizationErrorMsg{sessionID: sessionID, authorizationID: authorizationID, gen: gen, err: err}
		}
		url, err := m.deps.MCPAuthorization.MCPAuthorizationPresentation(ctx, sessionID, authorizationID)
		if err == nil {
			if err = ctx.Err(); err == nil {
				if copyLink && m.deps.Clipboard != nil {
					err = m.deps.Clipboard.Write(ctx, "text/plain", []byte(url))
				} else if !copyLink {
					err = m.deps.OpenURL(ctx, url)
				}
			}
		}
		if err != nil {
			verb := "open"
			if copyLink {
				verb = "copy"
			}
			return mcpAuthorizationErrorMsg{sessionID: sessionID, authorizationID: authorizationID, gen: gen, err: fmt.Errorf("%s authorization: %w", verb, err)}
		}
		if copyLink {
			return mcpAuthorizationCopyMsg{sessionID: sessionID, authorizationID: authorizationID, gen: gen, url: url}
		}
		return mcpAuthorizationActionMsg{sessionID: sessionID, authorizationID: authorizationID, gen: gen, text: "authorization page opened"}
	}
}

func (m Model) startMCPAuthorizationControl(cancelAuthorization bool) (tea.Model, tea.Cmd) {
	if m.authorization.controlCancel != nil {
		m.authorization.controlCancel()
	}
	m.authorization.stopFirstEventTimer()
	if m.authorization.presentationCancel != nil {
		m.authorization.presentationCancel()
		m.authorization.presentationCancel = nil
	}
	m.authorization.controlGen++
	gen := m.authorization.controlGen
	ctx, cancel := context.WithCancel(m.deps.Ctx)
	m.authorization.controlCancel = cancel
	m.authorization.firstEventTimer = time.AfterFunc(mcpAuthorizationFirstEventTimeout, cancel)
	m.authorization.errorText = ""
	m.authorizationEvents = nil
	return m, controlMCPAuthorizationCmd(ctx, m.deps.MCPAuthorization, m.sessionID, m.authorization.authorizationID, gen, cancelAuthorization)
}

func controlMCPAuthorizationCmd(ctx context.Context, control client.MCPAuthorizationController, sessionID, authorizationID string, gen uint64, cancelAuthorization bool) tea.Cmd {
	return func() tea.Msg {
		var stream *client.EventStream
		var err error
		if cancelAuthorization {
			stream, err = control.CancelMCPAuthorization(ctx, sessionID, authorizationID)
		} else {
			stream, err = control.RecheckMCPAuthorization(ctx, sessionID, authorizationID)
		}
		if err != nil {
			return mcpAuthorizationErrorMsg{sessionID: sessionID, authorizationID: authorizationID, gen: gen, err: err}
		}
		return mcpAuthorizationStreamMsg{sessionID: sessionID, authorizationID: authorizationID, gen: gen, ctx: ctx, stream: stream}
	}
}

type mcpAuthorizationStreamMsg struct {
	sessionID, authorizationID string
	gen                        uint64
	ctx                        context.Context
	stream                     *client.EventStream
}
type mcpAuthorizationEventMsg struct {
	sessionID, authorizationID string
	gen                        uint64
	msg                        tea.Msg
}
type mcpAuthorizationStreamClosedMsg struct {
	sessionID, authorizationID string
	gen                        uint64
}
type mcpAuthorizationErrorMsg struct {
	sessionID, authorizationID string
	gen                        uint64
	err                        error
}
type mcpAuthorizationActionMsg struct {
	sessionID, authorizationID string
	gen                        uint64
	text                       string
}
type mcpAuthorizationCopyMsg struct {
	sessionID, authorizationID string
	gen                        uint64
	url                        string
}

func (m Model) currentAuthorizationMessage(sessionID, authorizationID string, gen uint64) bool {
	return sessionID == m.sessionID && authorizationID == m.authorization.authorizationID && gen == m.authorization.controlGen
}

//nolint:gocyclo // message-dispatch switch over the authorization control message variants; inherent.
func (m Model) updateMCPAuthorizationMsg(message tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := message.(type) {
	case mcpAuthorizationEventMsg:
		return m.updateMCPAuthorizationEvent(msg)
	case mcpAuthorizationStreamClosedMsg:
		if !m.currentAuthorizationMessage(msg.sessionID, msg.authorizationID, msg.gen) {
			return m, nil, true
		}
		m.authorizationEvents = nil
		m.authorization.stopFirstEventTimer()
		if m.authorization.controlCancel != nil {
			m.authorization.controlCancel()
		}
		m.authorization.controlCancel = nil
		if m.authorization.presentationCancel != nil {
			m.authorization.presentationCancel()
			m.authorization.presentationCancel = nil
		}
		m.authorization.controlStream = nil
		m.authorization.pollBusy = false
		if m.phase == phaseRunning && m.authorization.runningControlGen == msg.gen {
			m.phase = phaseIdle
			m.authorization.runningControlGen = 0
		}
		if m.phase == phaseAuthorizing && m.authorization.polling {
			return m, mcpAuthorizationPollTickCmd(msg.sessionID, msg.authorizationID, msg.gen), true
		}
		return m, nil, true
	case mcpAuthorizationErrorMsg:
		if !m.currentAuthorizationMessage(msg.sessionID, msg.authorizationID, msg.gen) {
			return m, nil, true
		}
		m.authorizationEvents = nil
		m.authorization.stopFirstEventTimer()
		if m.authorization.controlCancel != nil {
			m.authorization.controlCancel()
		}
		m.authorization.controlCancel = nil
		if m.authorization.presentationCancel != nil {
			m.authorization.presentationCancel()
			m.authorization.presentationCancel = nil
		}
		m.authorization.controlStream = nil
		m.authorization.pollBusy = false
		m.authorization.errorText = oneLine(sanitizeTerminal(msg.err.Error()))
		if m.phase == phaseAuthorizing && m.authorization.polling {
			return m, mcpAuthorizationPollTickCmd(msg.sessionID, msg.authorizationID, msg.gen), true
		}
		return m, nil, true
	case mcpAuthorizationActionMsg:
		if m.currentAuthorizationMessage(msg.sessionID, msg.authorizationID, msg.gen) {
			m.authorization.errorText = ""
			if m.authorization.presentationCancel != nil {
				m.authorization.presentationCancel()
				m.authorization.presentationCancel = nil
			}
			m.authorization.polling = true
			m.statusMsg = sanitizeTerminal(msg.text)
			return m, mcpAuthorizationPollTickCmd(msg.sessionID, msg.authorizationID, msg.gen), true
		}
		return m, nil, true
	case mcpAuthorizationCopyMsg:
		if !m.currentAuthorizationMessage(msg.sessionID, msg.authorizationID, msg.gen) {
			return m, nil, true
		}
		m.authorization.errorText = ""
		if m.authorization.presentationCancel != nil {
			m.authorization.presentationCancel()
			m.authorization.presentationCancel = nil
		}
		m.authorization.polling = true
		m.statusMsg = "authorization URL copied"
		return m, tea.Batch(tea.SetClipboard(msg.url), mcpAuthorizationPollTickCmd(msg.sessionID, msg.authorizationID, msg.gen)), true
	case mcpAuthorizationStreamMsg:
		mm, cmd := m.updateMCPAuthorizationStream(msg)
		return mm, cmd, true
	default:
		return m, nil, false
	}
}

func (m Model) updateMCPAuthorizationEvent(msg mcpAuthorizationEventMsg) (tea.Model, tea.Cmd, bool) {
	if !m.currentAuthorizationMessage(msg.sessionID, msg.authorizationID, msg.gen) {
		return m, nil, true
	}
	// Any message on this stream — error or genuine event — proves the call is
	// alive, so the first-event watchdog has done its job. Stopping it here
	// (rather than only in the mcpAuthorizationErrorMsg/StreamClosedMsg cases)
	// also covers a granted continuation's subsequent, potentially long-running
	// run events, which must never be bounded by this timeout.
	m.authorization.stopFirstEventTimer()
	if streamErr, ok := msg.msg.(client.StreamErrMsg); ok {
		errText := oneLine(sanitizeTerminal(streamErr.Err.Error()))
		m.authorizationEvents = nil
		if m.authorization.controlCancel != nil {
			m.authorization.controlCancel()
		}
		m.authorization.controlCancel = nil
		if m.authorization.presentationCancel != nil {
			m.authorization.presentationCancel()
			m.authorization.presentationCancel = nil
		}
		m.authorization.controlStream = nil
		m.authorization.pollBusy = false
		m.authorization.errorText = errText
		ownsRun := m.authorization.runningControlGen == msg.gen
		if ownsRun {
			m.authorization.runningControlGen = 0
		}
		if ownsRun && (m.phase == phaseAwaitingApproval || m.phase == phaseRunning) {
			m = m.endRun("")
			m.statusMsg = m.deps.Theme.Style("errorText").Render("authorization control stream error: " + errText)
			m.refreshView()
			return m, nil, true
		}
		if m.phase == phaseAuthorizing && m.authorization.polling {
			return m, mcpAuthorizationPollTickCmd(msg.sessionID, msg.authorizationID, msg.gen), true
		}
		return m, nil, true
	}
	mm, eventCmd := m.updateStreamEvent(msg.msg)
	m = mm.(Model)
	var sideEffect tea.Cmd
	if auth, ok := msg.msg.(client.MCPAuthorizationMsg); ok {
		switch {
		case auth.Status == mcpAuthorizationStatusPending && m.phase == phaseAuthorizing && m.authorization.polling:
			// applyMCPAuthorization replaced the completed observation with a new
			// generation. Hand off exactly one tick for that generation; eventCmd
			// also carries the parked Converse reader and must not be propagated.
			sideEffect = mcpAuthorizationPollTickCmd(m.sessionID, m.authorization.authorizationID, m.authorization.controlGen)
		case auth.Status != mcpAuthorizationStatusPending && m.phase == phaseRunning:
			m.authorization.runningControlGen = msg.gen
			// updateStreamEvent deliberately does not re-arm its ordinary Converse
			// reader here: that stream closed when the authorization parked. Preserve
			// the spinner tick that applyMCPAuthorization re-arms on the transition
			// back into its visible running phase.
			sideEffect = tea.Batch(sideEffect, m.sp.Tick)
		}
	}
	// Only the control stream is re-armed. updateStreamEvent's ordinary
	// afterEvent command owns the original Converse source, which is already
	// closed once authorization parked.
	if _, terminal := msg.msg.(client.ResultMsg); terminal {
		sideEffect = eventCmd
	}
	if m.authorizationEvents != nil {
		sideEffect = tea.Batch(sideEffect, m.waitMCPAuthorizationEvent(msg.sessionID, msg.authorizationID, msg.gen))
	}
	return m, sideEffect, true
}

func (m Model) updateMCPAuthorizationStream(msg mcpAuthorizationStreamMsg) (tea.Model, tea.Cmd) {
	if !m.currentAuthorizationMessage(msg.sessionID, msg.authorizationID, msg.gen) || msg.stream == nil {
		return m, nil
	}
	ch := make(chan tea.Msg, 16)
	m.authorizationEvents = ch
	m.authorization.controlStream = msg.stream
	ctx := msg.ctx
	if ctx == nil {
		ctx = m.deps.Ctx
	}
	go msg.stream.ReadLoop(ctx, ch)
	return m, m.waitMCPAuthorizationEvent(msg.sessionID, msg.authorizationID, msg.gen)
}

func (m Model) waitMCPAuthorizationEvent(sessionID, authorizationID string, gen uint64) tea.Cmd {
	ch := m.authorizationEvents
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return mcpAuthorizationStreamClosedMsg{sessionID: sessionID, authorizationID: authorizationID, gen: gen}
		}
		return mcpAuthorizationEventMsg{sessionID: sessionID, authorizationID: authorizationID, gen: gen, msg: msg}
	}
}

func (m Model) renderMCPAuthorization() string {
	open, copyLink := m.keys.Choose.Help().Key, m.keys.CopySelection.Help().Key
	cancel := m.keys.CancelChild.Help().Key
	body := "MCP authorization required"
	if m.authorization.displayName != "" {
		body += " for " + m.authorization.displayName
	}
	body += "\n\nOpen or copy the link, then browser consent is checked automatically.\n\n[" + open + "] Open Browser   [" + copyLink + "] Copy Link   [" + cancel + "] Cancel"
	if errText := strings.TrimSpace(m.authorization.errorText); errText != "" {
		body += "\n\nError: " + sanitizeTerminal(errText)
	}
	return centerCard(m.deps.Theme, body, m.width, m.vp.Height())
}

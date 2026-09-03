package ui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

const mcpAuthorizationStatusPending = "pending"

// mcpAuthorizationState is intentionally distinct from permission approval.
// It holds safe correlation only; presentation URLs are fetched on demand and
// never retained in model state or reconstructed from replayed events.
type mcpAuthorizationState struct {
	authorizationID   string
	displayName       string
	callID            string
	errorText         string
	controlGen        uint64
	controlCancel     context.CancelFunc
	controlStream     *client.EventStream
	runningControlGen uint64
}

func (m Model) applyMCPAuthorization(msg client.MCPAuthorizationMsg) (tea.Model, tea.Cmd) {
	if msg.Status != mcpAuthorizationStatusPending {
		reenteredRunning := false
		if m.authorization.authorizationID == msg.AuthorizationID {
			m.authorization.errorText = ""
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
	m.authorization = mcpAuthorizationState{
		authorizationID: msg.AuthorizationID,
		displayName:     oneLine(sanitizeTerminal(msg.DisplayName)),
		callID:          msg.CallID,
		controlGen:      m.authorization.controlGen + 1,
	}
	m.authorizationEvents = nil
	m.phase = phaseAuthorizing
	m.activeTool = ""
	m.toolProgress = ""
	return m.afterEvent()
}

func (m Model) onMCPAuthorizationKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.authorization.authorizationID == "" || m.deps.MCPAuthorization == nil {
		return m, nil
	}
	id, sessionID := m.authorization.authorizationID, m.sessionID
	gen := m.authorization.controlGen
	switch {
	case key.Matches(msg, m.keys.Close):
		return m, nil // cancellation is explicit; escape cannot silently resolve it.
	case key.Matches(msg, m.keys.Choose):
		if m.deps.OpenURL == nil {
			m.authorization.errorText = "browser opening is unavailable"
			return m, nil
		}
		return m, func() tea.Msg {
			url, err := m.deps.MCPAuthorization.MCPAuthorizationPresentation(m.deps.Ctx, sessionID, id)
			if err == nil {
				err = m.deps.OpenURL(m.deps.Ctx, url)
			}
			if err != nil {
				return mcpAuthorizationErrorMsg{sessionID: sessionID, authorizationID: id, gen: gen, err: fmt.Errorf("open authorization: %w", err)}
			}
			return mcpAuthorizationActionMsg{sessionID: sessionID, authorizationID: id, gen: gen, text: "authorization page opened"}
		}
	case key.Matches(msg, m.keys.CopySelection):
		return m, func() tea.Msg {
			url, err := m.deps.MCPAuthorization.MCPAuthorizationPresentation(m.deps.Ctx, sessionID, id)
			if err == nil && m.deps.Clipboard != nil {
				err = m.deps.Clipboard.Write(m.deps.Ctx, "text/plain", []byte(url))
			}
			if err != nil {
				return mcpAuthorizationErrorMsg{sessionID: sessionID, authorizationID: id, gen: gen, err: fmt.Errorf("copy authorization URL: %w", err)}
			}
			return mcpAuthorizationCopyMsg{sessionID: sessionID, authorizationID: id, gen: gen, url: url}
		}
	case key.Matches(msg, m.keys.Refresh):
		return m.startMCPAuthorizationControl(false)
	case key.Matches(msg, m.keys.CancelChild):
		return m.startMCPAuthorizationControl(true)
	default:
		return m, nil
	}
}

func (m Model) startMCPAuthorizationControl(cancelAuthorization bool) (tea.Model, tea.Cmd) {
	if m.authorization.controlCancel != nil {
		m.authorization.controlCancel()
	}
	m.authorization.controlGen++
	gen := m.authorization.controlGen
	ctx, cancel := context.WithCancel(m.deps.Ctx)
	m.authorization.controlCancel = cancel
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
		return mcpAuthorizationStreamMsg{sessionID: sessionID, authorizationID: authorizationID, gen: gen, stream: stream}
	}
}

type mcpAuthorizationStreamMsg struct {
	sessionID, authorizationID string
	gen                        uint64
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

func (m Model) updateMCPAuthorizationMsg(message tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := message.(type) {
	case mcpAuthorizationEventMsg:
		return m.updateMCPAuthorizationEvent(msg)
	case mcpAuthorizationStreamClosedMsg:
		if m.currentAuthorizationMessage(msg.sessionID, msg.authorizationID, msg.gen) {
			m.authorizationEvents = nil
			if m.authorization.controlCancel != nil {
				m.authorization.controlCancel()
			}
			m.authorization.controlCancel = nil
			m.authorization.controlStream = nil
			if m.phase == phaseRunning && m.authorization.runningControlGen == msg.gen {
				m.phase = phaseIdle
				m.authorization.runningControlGen = 0
			}
		}
		return m, nil, true
	case mcpAuthorizationErrorMsg:
		if m.currentAuthorizationMessage(msg.sessionID, msg.authorizationID, msg.gen) {
			m.authorizationEvents = nil
			if m.authorization.controlCancel != nil {
				m.authorization.controlCancel()
			}
			m.authorization.controlCancel = nil
			m.authorization.controlStream = nil
			m.authorization.errorText = oneLine(sanitizeTerminal(msg.err.Error()))
		}
		return m, nil, true
	case mcpAuthorizationActionMsg:
		if m.currentAuthorizationMessage(msg.sessionID, msg.authorizationID, msg.gen) {
			m.authorization.errorText = ""
			m.statusMsg = sanitizeTerminal(msg.text)
		}
		return m, nil, true
	case mcpAuthorizationCopyMsg:
		if !m.currentAuthorizationMessage(msg.sessionID, msg.authorizationID, msg.gen) {
			return m, nil, true
		}
		m.authorization.errorText = ""
		m.statusMsg = "authorization URL copied"
		return m, tea.SetClipboard(msg.url), true
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
	if streamErr, ok := msg.msg.(client.StreamErrMsg); ok {
		errText := oneLine(sanitizeTerminal(streamErr.Err.Error()))
		m.authorizationEvents = nil
		if m.authorization.controlCancel != nil {
			m.authorization.controlCancel()
		}
		m.authorization.controlCancel = nil
		m.authorization.controlStream = nil
		m.authorization.errorText = errText
		ownsRun := m.authorization.runningControlGen == msg.gen
		if ownsRun {
			m.authorization.runningControlGen = 0
		}
		if ownsRun && (m.phase == phaseAwaitingApproval || m.phase == phaseRunning) {
			m = m.endRun("")
			m.statusMsg = m.deps.Theme.Style("errorText").Render("authorization control stream error: " + errText)
			m.refreshView()
		}
		return m, nil, true
	}
	mm, eventCmd := m.updateStreamEvent(msg.msg)
	m = mm.(Model)
	var sideEffect tea.Cmd
	if auth, ok := msg.msg.(client.MCPAuthorizationMsg); ok && auth.Status != mcpAuthorizationStatusPending && m.phase == phaseRunning {
		m.authorization.runningControlGen = msg.gen
		// updateStreamEvent deliberately does not re-arm its ordinary Converse
		// reader here: that stream closed when the authorization parked. Preserve
		// the spinner tick that applyMCPAuthorization re-arms on the transition
		// back into its visible running phase.
		sideEffect = tea.Batch(sideEffect, m.sp.Tick)
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
	go msg.stream.ReadLoop(m.deps.Ctx, ch)
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
	recheck, cancel := m.keys.Refresh.Help().Key, m.keys.CancelChild.Help().Key
	body := "MCP authorization required"
	if m.authorization.displayName != "" {
		body += " for " + m.authorization.displayName
	}
	body += "\n\n[" + open + "] Open Browser   [" + copyLink + "] Copy Link   [" + recheck + "] Recheck   [" + cancel + "] Cancel"
	if errText := strings.TrimSpace(m.authorization.errorText); errText != "" {
		body += "\n\nError: " + sanitizeTerminal(errText)
	}
	return centerCard(m.deps.Theme, body, m.width, m.vp.Height())
}

package ui

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
)

// agentCommand recognizes /agent, mirroring titleCommand's bare-vs-argument
// split: whitespace (or end of input) after /agent is a bare picker request; a non-whitespace suffix is the agent-definition name to create a
// session for immediately. The next character must be whitespace (or absent)
// so "/agents" is never misparsed as "/agent" with argument "s".
func agentCommand(text string) (name string, bare, ok bool) {
	raw := strings.TrimLeftFunc(text, unicode.IsSpace)
	if !strings.HasPrefix(strings.ToLower(raw), "/agent") {
		return "", false, false
	}
	if len(raw) > len("/agent") && !unicode.IsSpace(rune(raw[len("/agent")])) {
		return "", false, false
	}
	name = strings.TrimSpace(raw[len("/agent"):])
	return name, name == "", true
}

// startAgentSession begins the /agent <name> handoff: like restartOnModel it
// tears down any in-flight run and the old per-session client state, then
// fires createSessionWithAgentCmd off the update goroutine. Unlike a model
// switch this is not a "restart" of the current session's selection — it is a
// fresh session bound to a DIFFERENT AgentDef — so it carries no model
// selection and no persisted-selection save.
func (m Model) startAgentSession(name string) (tea.Model, tea.Cmd, bool) {
	if m.phase != phaseIdle || m.deps.Session == nil {
		return m, nil, true
	}
	m = m.endRun("")
	oldID := m.sessionID
	m = m.resetSession()
	m = m.bindSessionID("")
	m.resolvedSessionModel = client.ResolvedModel{}
	m.caps = client.Capabilities{}
	m.restartFailed = false
	m.phase = phaseConnecting
	m.statusMsg = "starting agent " + terminaltext.SanitizeSingleLine(name) + " — connecting…"
	m.refreshView()
	return m, tea.Batch(m.createSessionWithAgentCmd(oldID, name), m.sp.Tick), true
}

// createSessionWithAgentCmd closes the OLD session (best-effort) then creates
// a NEW session bound to the named AgentDef, off the update goroutine. On
// success it returns the same SessionReadyMsg the ordinary connect/restart
// paths use. On failure it returns agentSessionFailedMsg (NOT
// client.ConnectErrMsg, which would drive the terminal fatal screen) —
// distinguishing an unknown definition (client.IsInvalidArgument) from a
// transient failure in the surfaced message only; both are equally
// recoverable (idle, enter-to-retry is NOT armed — the operator retypes the
// name, since unlike a model switch there is no single "the" pending
// selection to replay).
func (m Model) createSessionWithAgentCmd(oldID, name string) tea.Cmd {
	deps := m.deps
	mode := m.desiredMode()
	// Carry the operator's active selection like every other create path: a def
	// with no model of its own inherits it, whereas a zero selection would fall
	// to the server default (possibly a model the account cannot afford).
	sel := m.createModelSelection
	return func() tea.Msg {
		if oldID != "" {
			_ = deps.Session.CloseSession(deps.Ctx, oldID)
		}
		id, caps, resolved, err := deps.Session.CreateSessionWithAgent(deps.Ctx, sel, mode, name)
		if err != nil {
			return agentSessionFailedMsg{err: err, name: name, unknown: client.IsInvalidArgument(err)}
		}
		return client.SessionReadyMsg{SessionID: id, Capabilities: caps, ResolvedModel: resolved, Mode: mode}
	}
}

// agentSessionFailedMsg reports that a /agent <name> create FAILED. Distinct
// from client.ConnectErrMsg: its reducer leaves the app RECOVERABLE (idle, no
// session). unknown distinguishes the server's InvalidArgument (no such
// definition) from a transient failure, purely for the status copy.
type agentSessionFailedMsg struct {
	err     error
	name    string
	unknown bool
}

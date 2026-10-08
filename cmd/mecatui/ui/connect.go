package ui

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// ConnectTarget is public saved-target metadata. It deliberately has no token,
// credential reference, CA contents, or other secret-shaped fields.
type ConnectTarget struct {
	Target   string
	Issuer   string
	ClientID string
	Audience string
}

// ConnectController is the narrow composition-owned saved-target listing seam.
// The UI can only display its public metadata and emit an intent; it cannot
// authenticate or launch a browser.
type ConnectController interface {
	ListConnectTargets(context.Context) ([]ConnectTarget, error)
}

// ConnectAction is the closed set of operations main may perform after the TUI
// exits. The zero value is invalid so an incomplete intent fails loudly.
type ConnectAction uint8

// ConnectAction values are the closed set of operations main may perform after
// the TUI exits.
const (
	connectCommand = "connect"

	ConnectSaved ConnectAction = iota + 1
	Reauthenticate
	RetryAfterCleanup
	AddTarget
)

// ConnectRestartIntent is consumed by main after Bubble Tea exits. Target is
// public canonical registry metadata only.
type ConnectRestartIntent struct {
	Target string
	Action ConnectAction
	// ResumeSessionID is retained only for same-target recovery actions. Main
	// re-checks ownership through GetSession before adopting it.
	ResumeSessionID string
}

type connectState struct {
	open, loading, confirm bool
	err                    string
	reason                 client.AuthReason
	failedTarget           string
	resumeSessionID        string
	targets                []ConnectTarget
	targetsUnavailable     bool
	cursor                 int
}

type connectTargetsMsg struct {
	generation uint64
	targets    []ConnectTarget
	err        error
}

func (m Model) openConnect() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Connect == nil {
		return m, nil
	}
	m.prompt.Blur()
	m.connectLoadGeneration++
	generation := m.connectLoadGeneration
	m.connect = connectState{open: true, loading: true, err: m.connect.err, reason: m.connect.reason, failedTarget: m.connect.failedTarget, resumeSessionID: m.connect.resumeSessionID}
	deps := m.deps
	return m, func() tea.Msg {
		targets, err := deps.Connect.ListConnectTargets(deps.Ctx)
		return connectTargetsMsg{generation: generation, targets: targets, err: err}
	}
}

func (m Model) onConnectKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if !m.connect.open {
		return m, nil, false
	}
	if key.Matches(msg, m.keys.Close) {
		m.connect = connectState{}
		cmd := m.prompt.Focus()
		return m, cmd, true
	}
	if m.connect.loading || m.connect.targetsUnavailable {
		return m, nil, true
	}
	if msg.String() == keyMenuUp && m.connect.cursor > 0 {
		m.connect.cursor--
		return m, nil, true
	}
	maxCursor := len(m.connect.targets)
	if m.connect.reason == client.AuthRejected {
		maxCursor--
	}
	if msg.String() == keyMenuDown && m.connect.cursor < maxCursor {
		m.connect.cursor++
		return m, nil, true
	}
	if !key.Matches(msg, m.keys.Choose) {
		return m, nil, true
	}
	if _, ok := m.selectedConnectAction(); !ok {
		m.connect.err = "The server rejected this credential; check issuer, audience, and CA settings before trying again."
		return m, nil, true
	}
	if !m.connect.confirm {
		m.connect.confirm = true
		return m, nil, true
	}
	intent := m.connectRestartIntent()
	m.connectIntent = &intent
	return m, tea.Quit, true
}

// selectedConnectAction resolves the selected row's real next action. A rejected
// credential is deliberately not actionable: Enter only explains why it cannot
// be retried from this panel.
func (m Model) selectedConnectAction() (ConnectAction, bool) {
	if m.connect.cursor == len(m.connect.targets) {
		return AddTarget, true
	}
	if m.connect.cursor < 0 || m.connect.cursor >= len(m.connect.targets) {
		return 0, false
	}
	if m.connect.reason == client.AuthRejected && m.connect.targets[m.connect.cursor].Target == m.connect.failedTarget {
		return 0, false
	}
	if m.connect.targets[m.connect.cursor].Target != m.connect.failedTarget {
		return ConnectSaved, true
	}
	switch m.connect.reason {
	case client.AuthCredentialCleanup, client.AuthStorageUnavailable:
		return RetryAfterCleanup, true
	case client.AuthNotEnrolled, client.AuthSessionExpired, client.AuthCredentialUnusable, client.AuthTargetChanged:
		return Reauthenticate, true
	default:
		return ConnectSaved, true
	}
}

func connectActionLabel(action ConnectAction) string {
	switch action {
	case ConnectSaved:
		return "connect"
	case Reauthenticate:
		return "sign in again"
	case RetryAfterCleanup:
		return "retry connection"
	case AddTarget:
		return "sign in"
	default:
		return ""
	}
}

func connectConfirmationLabel(action ConnectAction) string {
	switch action {
	case ConnectSaved:
		return "Connect to this saved target?"
	case Reauthenticate:
		return "Sign in again and connect?"
	case RetryAfterCleanup:
		return "Retry connection without opening a browser?"
	case AddTarget:
		return "Sign in to a new target?"
	default:
		return ""
	}
}

func (m Model) connectRestartIntent() ConnectRestartIntent {
	action, ok := m.selectedConnectAction()
	if !ok {
		return ConnectRestartIntent{}
	}
	intent := ConnectRestartIntent{Action: action}
	if action != AddTarget {
		intent.Target = m.connect.targets[m.connect.cursor].Target
	}
	if intent.Action == Reauthenticate || intent.Action == RetryAfterCleanup {
		intent.ResumeSessionID = m.connect.resumeSessionID
	}
	return intent
}

func (m Model) updateConnectMsg(msg tea.Msg) (tea.Model, bool) {
	result, ok := msg.(connectTargetsMsg)
	if !ok {
		return m, false
	}
	if !m.connect.open || result.generation != m.connectLoadGeneration {
		return m, true
	}
	m.connect.loading = false
	if result.err != nil {
		m.connect.err = "saved targets are unavailable"
		m.connect.targetsUnavailable = true
		return m, true
	}
	m.connect.targets = result.targets
	for i, target := range m.connect.targets {
		if target.Target == m.connect.failedTarget {
			m.connect.cursor = i
			break
		}
	}
	if m.connect.cursor > len(m.connect.targets) {
		m.connect.cursor = 0
	}
	return m, true
}

func (m Model) renderConnectOverlay(th theme.Theme) string {
	var b strings.Builder
	b.WriteString(th.Style("askTitle").Render("Connect to remote") + "\n\n")
	budget := cardTextWidth(m.width)
	if m.connect.err != "" {
		b.WriteString(th.Style("warning").Render(wrapCardText(m.connect.err, budget)) + "\n\n")
	}
	if hint := connectAuthHint(m.connect.reason, m.connect.failedTarget); hint != "" {
		b.WriteString(th.Style("muted").Render(wrapCardText(hint, budget)) + "\n\n")
	}
	if m.connect.loading {
		b.WriteString("Loading saved targets…")
		return centerCard(th, b.String(), m.width, m.vp.Height())
	}
	for i, target := range m.connect.targets {
		prefix := "  "
		if i == m.connect.cursor {
			prefix = "> "
		}
		b.WriteString(wrapCardText(prefix+target.Target, budget) + "\n")
		b.WriteString(wrapCardText("   "+target.Issuer+" · "+target.ClientID+" · "+target.Audience, budget) + "\n")
	}
	if !m.connect.targetsUnavailable && m.connect.reason != client.AuthRejected {
		newRow := len(m.connect.targets)
		prefix := "  "
		if m.connect.cursor == newRow {
			prefix = "> "
		}
		b.WriteString(prefix + "OIDC sign-in for a new target…\n")
	}
	if m.connect.confirm {
		action, _ := m.selectedConnectAction()
		label := connectConfirmationLabel(action)
		b.WriteString("\n" + th.Style("warning").Render(label) + "\n")
		b.WriteString(th.Style("muted").Render("enter: "+connectActionLabel(action)+"  esc: back") + "\n")
	} else {
		action, ok := m.selectedConnectAction()
		hint := "↑/↓ select · esc: close"
		if ok {
			hint = "↑/↓ select · enter: " + connectActionLabel(action) + " · esc: close"
		}
		b.WriteString("\n" + th.Style("muted").Render(hint) + "\n")
	}
	return centerCard(th, b.String(), m.width, m.vp.Height())
}

func connectAuthHint(reason client.AuthReason, target string) string {
	if reason == client.AuthNotEnrolled && target != "" {
		return "The server requires caller authentication. Use --auth-token, or, if this server supports OIDC enrollment, run mecatui login " + terminaltext.Sanitize(target) + "."
	}
	if reason == client.AuthAnonymousRejected && target != "" {
		return "The server rejected --anonymous because it requires caller authentication. Retry without --anonymous to use saved OIDC, use --auth-token, or, if supported, run mecatui login " + terminaltext.Sanitize(target) + "."
	}
	switch reason {
	case client.AuthNotEnrolled:
		return "The server requires caller authentication. Use --auth-token or, if supported, enroll with OIDC."
	case client.AuthAnonymousRejected:
		return "The server rejected --anonymous because it requires caller authentication. Retry without --anonymous to use saved OIDC, use --auth-token, or, if supported, enroll with OIDC."
	case client.AuthSessionExpired:
		return "Your session expired. Sign in again to reconnect."
	case client.AuthCredentialUnusable:
		return "The saved credential is corrupt. Sign in again to replace it."
	case client.AuthTargetChanged:
		return "This target changed while signing in (a concurrent logout or newer login). Sign in again."
	case client.AuthStorageUnavailable:
		return "Local credential storage or the saved issuer/CA trust is unavailable. Retry the connection after restoring it -- signing in again will not fix this."
	case client.AuthCredentialCleanup:
		return "Stored credential cleanup is still in progress. Retry the connection before signing in again."
	case client.AuthRejected:
		return "The server rejected this credential. Re-login is disabled for this target; check issuer, audience, and CA settings."
	default:
		return ""
	}
}

// ConnectRestartIntent reports a deliberate internal restart request. It is
// intentionally a value with only public metadata.
func (m Model) ConnectRestartIntent() (ConnectRestartIntent, bool) {
	if m.connectIntent == nil {
		return ConnectRestartIntent{}, false
	}
	return *m.connectIntent, true
}

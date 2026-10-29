package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

// interruptedToolText is the result the server records for a tool call whose
// pending approval a mode change cancelled. It matches the session's own
// cancellation close-out, so the card reads the same before and after the
// durable tool.result arrives.
const interruptedToolText = "tool call interrupted by cancellation"

// recoveryModeSwitchAvailable reports whether ModeSwitch in a recovered
// pending approval asks the server's refused-mode recovery for a lower mode.
// It needs the server feature, a shown approval backed by a live watch, and no
// verdict already sent.
func (m Model) recoveryModeSwitchAvailable() bool {
	r := m.pendingRecovery
	return r != nil && !r.resolving && !r.resolved && r.watch != nil &&
		m.caps.RefusedModeRecovery && m.deps.DebugTarget == "" && m.deps.Session != nil &&
		m.sessionID != "" && approvalSurfaceFor(&m) != nil
}

// recoveryVerdictsBlocked reports that the recovered approval must not send a
// verdict: the user's recovery mode change is in flight, or the server already
// refused a verdict for it.
func (m Model) recoveryVerdictsBlocked() bool {
	r := m.pendingRecovery
	return r != nil && (m.recoveryModeSwitch != "" || r.verdictRefused)
}

// isApprovalVerdictKey matches every key that resolves the approval surface.
func isApprovalVerdictKey(msg tea.KeyPressMsg, keys keyMap) bool {
	return msg.String() == "enter" || key.Matches(msg, keys.Allow) || key.Matches(msg, keys.AllowAlways) || key.Matches(msg, keys.Deny)
}

// permissionModeRank orders session modes plan → yolo, the server's admission
// order. An unknown mode ranks -1.
func permissionModeRank(mode string) int {
	normalized := client.ModeString(client.ModeFromString(mode))
	for i, e := range PermissionModeVocabulary() {
		if e.SessionMode == normalized {
			return i
		}
	}
	return -1
}

// recoveryModeTarget picks the next mode in the ModeSwitch cycle, after the
// one last tried, that is strictly lower than the session's stored mode and
// within the server's posture. The server still decides admission.
func (m Model) recoveryModeTarget() (PermissionModeEntry, bool) {
	order := modeCycleOrder(m.legacyPermissionModes())
	active := permissionModeRank(m.activeMode)
	cur := m.pendingRecovery.modeTried
	if cur == "" {
		cur = m.acceptedModeToken()
	}
	start := -1
	for i, name := range order {
		if name == cur {
			start = i
			break
		}
	}
	for step := 1; step <= len(order); step++ {
		e := permissionModeEntry(order[(start+step+len(order))%len(order)])
		if permissionModeRank(e.SessionMode) < active && postureRank(e.Posture) <= postureRank(m.caps.Posture) {
			return e, true
		}
	}
	return PermissionModeEntry{}, false
}

// activeModeAboveServerPosture reports a stored session mode whose posture the
// server's ceiling no longer covers, so approving its pending ask is refused.
func (m Model) activeModeAboveServerPosture() bool {
	if m.activeMode == "" {
		return false
	}
	return postureRank(permissionModeEntry(m.acceptedModeToken()).Posture) > postureRank(m.caps.Posture)
}

// switchRecoveryMode sends the refused-mode recovery: SetMode to a lower mode,
// which the server applies by cancelling the pending turn without running it.
// It never sets pendingMode, so a refusal is final rather than deferred.
func (m Model) switchRecoveryMode() (tea.Model, tea.Cmd) {
	r := m.pendingRecovery
	if m.recoveryModeSwitch != "" {
		r.notice = "switching to " + m.recoveryModeSwitch + "…"
		m.refreshView()
		return m, nil
	}
	target, ok := m.recoveryModeTarget()
	if !ok {
		r.notice = "no lower mode is available to cancel this request"
		m.statusMsg = m.deps.Theme.Style("warning").Render(r.notice)
		m.refreshView()
		return m, nil
	}
	r.modeTried = target.Name
	mode := client.ModeString(client.ModeFromString(target.SessionMode))
	m.recoveryModeSwitch = mode
	r.notice = "switching to " + target.Name + "; the request will be cancelled without running"
	m.statusMsg = m.deps.Theme.Style("muted").Render(r.notice + "…")
	m.refreshView()
	return m, client.SetModeCmd(m.deps.Ctx, m.deps.Session, m.sessionID, mode)
}

// onRecoveryModeChanged settles the user's recovery mode change. Success means
// the pending approval no longer exists: the server cancelled it (or it was
// already settled) and applied the mode, so the recovery retires, its unanswered
// tool calls read as interrupted, and the session returns to idle with the draft
// intact. A refusal is final and keeps the approval and its modal open with the
// server's reason.
func (m Model) onRecoveryModeChanged(msg client.ModeChangedMsg, requested string) (tea.Model, tea.Cmd) {
	m.recoveryModeSwitch = ""
	modeKey := firstKey(m.keys.ModeSwitch, "shift+tab")
	if msg.Err != nil {
		reason := oneLine(terminaltext.Sanitize(client.ModeErrorText(msg.Err)))
		text := "mode " + requested + " refused: " + reason
		if r := m.pendingRecovery; r != nil {
			r.notice = text + " · " + modeKey + " tries a lower mode"
			text += "; the request is still pending"
		}
		m.statusMsg = m.deps.Theme.Style("warning").Render(text)
		m.refreshView()
		return m, nil
	}
	mode := client.ModeString(client.ModeFromString(msg.Mode))
	if msg.Mode == "" {
		mode = requested
	}
	m.activeMode = mode
	m.pendingMode = ""
	m.modeCursor = modeCursor{}
	m.modeRefused = ""
	if msg.State != "" {
		m.sessionState = msg.State
	}
	outcome := "the pending request is no longer pending"
	if msg.State == "cancelled" {
		outcome = "the pending request was cancelled without running"
	}
	// When the watch already delivered the run's terminal, the recovery is
	// retired and the session may have moved on; only the mode is news then.
	if m.pendingRecovery != nil {
		(&m).retirePendingApprovalRecovery()
		m.interruptUnresolvedToolCards()
		m = m.endRun("")
	}
	m.conv.addNotice("mode " + mode + " applied; " + outcome)
	m.statusMsg = m.deps.Theme.Style("success").Render("mode " + mode + " applied; " + outcome)
	m.refreshView()
	return m, m.armLiveFeed()
}

// interruptUnresolvedToolCards settles every still-pending tool card with the
// cancellation close-out the server records for the cancelled turn. Cards the
// durable tool.result already settled are immutable, so this is idempotent.
func (m *Model) interruptUnresolvedToolCards() {
	var pending []string
	for i := 0; i < m.conv.scrollback.Len(); i++ {
		card, ok := m.conv.scrollback.SnapshotAt(i).Payload.(scrollback.ToolCardSnapshot)
		if ok && !card.Resolved {
			pending = append(pending, card.Call.ID)
		}
	}
	for _, id := range pending {
		m.conv.resolveTool(id, interruptedToolText, true)
	}
}

// recoveryRefusedVerdictNotice names the refused-mode recovery after the
// server refused a verdict for the recovered approval.
func recoveryRefusedVerdictNotice(detail string) string {
	text := "approval refused, nothing ran"
	if detail = strings.TrimSpace(oneLine(terminaltext.Sanitize(detail))); detail != "" {
		text += ": " + detail
	}
	return text
}

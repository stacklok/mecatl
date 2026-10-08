package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// modeCycleOrder is the ModeSwitch order over every permission mode: the
// vocabulary with default and plan swapped, keeping the established
// default → plan → accept-edits start before the posture-raising modes.
func modeCycleOrder() []string {
	vocab := PermissionModeVocabulary()
	names := make([]string, len(vocab))
	for i, e := range vocab {
		names[i] = e.Name
	}
	names[0], names[1] = names[1], names[0]
	return names
}

// modeCursor is the permission mode the cycle last landed on, valid only while
// the session's desired mode is still the one it recorded. Any other mode change
// (server read-back, new session) invalidates it, so the cycle re-derives its
// position from the live session.
type modeCursor struct {
	token   string
	session string
}

// postureRank orders the posture tiers. An unknown posture (an older server
// omits it) ranks as strict.
func postureRank(p string) int {
	switch p {
	case postureTrusted:
		return 1
	case postureAuto:
		return 2
	case postureYolo:
		return 3
	default:
		return 0
	}
}

func permissionModeEntry(name string) PermissionModeEntry {
	for _, e := range PermissionModeVocabulary() {
		if e.Name == name {
			return e
		}
	}
	return PermissionModeEntry{}
}

// currentModeToken is the cycle position: the mode the cycle last landed on, or
// the mode matching the running posture and the session's desired mode.
func (m Model) currentModeToken() string {
	if m.modeCursor.token != "" && m.modeCursor.session == m.desiredMode() {
		return m.modeCursor.token
	}
	return m.acceptedModeToken()
}

// acceptedModeToken is the mode the session is actually running: the one
// matching the running posture and the session's desired mode, else the
// session mode itself.
func (m Model) acceptedModeToken() string {
	session := m.desiredMode()
	for _, e := range PermissionModeVocabulary() {
		if e.Posture == m.caps.Posture && e.SessionMode == session {
			return e.Name
		}
	}
	return session
}

func nextModeToken(cur string) string {
	order := modeCycleOrder()
	for i, name := range order {
		if name == cur {
			return order[(i+1)%len(order)]
		}
	}
	// No position yet (the server default is still unknown): start after default.
	return order[1]
}

// cycleMode lands on the next permission mode. A mode whose posture is at or
// below the running one applies its session half live; one that would raise the
// posture cannot, because the posture is fixed when the server starts, so the
// cycle stops on it and says what to configure and restart.
func (m Model) cycleMode() (tea.Model, tea.Cmd) {
	next := permissionModeEntry(nextModeToken(m.currentModeToken()))
	if postureRank(next.Posture) > postureRank(m.caps.Posture) {
		m.modeCursor = modeCursor{token: next.Name, session: m.desiredMode()}
		m.statusMsg = m.deps.Theme.Style("warning").Render(m.modeBlockedStatus())
		(&m).relayout()
		return m, nil
	}
	mm, cmd := m.switchMode(next)
	m = mm.(Model)
	m.modeCursor = modeCursor{token: next.Name, session: m.desiredMode()}
	(&m).relayout()
	return m, cmd
}

// headerMode is the session mode the header shows: the mode the cycle landed
// on when it did, else the session's mode, marked blocked or pending.
func (m Model) headerMode() string {
	mode := m.activeMode
	if mode == "" {
		mode = m.deps.Mode
	}
	if m.pendingMode != "" {
		mode = m.pendingMode
	}
	if m.modeCursor.token != "" && m.modeCursor.session == m.desiredMode() {
		mode = m.modeCursor.token
	}
	switch {
	case m.modeBlocked():
		mode += modeBlockedSuffix
	case m.pendingMode != "":
		mode += " pending"
	}
	return mode
}

// modeBlocked reports that the cycle sits on a mode that needs a restart. The
// session keeps running its accepted mode, but prompts and edits are held so
// nothing is sent under a mode the operator did not mean.
func (m Model) modeBlocked() bool {
	e := permissionModeEntry(m.currentModeToken())
	return e.Name != "" && postureRank(e.Posture) > postureRank(m.caps.Posture)
}

func (m Model) modeEscapeHint() string {
	return firstKey(m.keys.Cancel, "esc") + " back to " + m.acceptedModeToken() + " · " +
		firstKey(m.keys.ModeSwitch, "shift+tab") + " next mode"
}

func (m Model) modeBlockedStatus() string {
	return "mode " + m.currentModeToken() + " is blocked — " + m.modeEscapeHint()
}

// leaveBlockedMode is the escape hatch: drop the cursor so the session's
// accepted mode is the current one again.
func (m Model) leaveBlockedMode() Model {
	m.modeCursor = modeCursor{}
	m.statusMsg = m.deps.Theme.Style("success").Render("mode " + m.acceptedModeToken())
	(&m).relayout()
	return m
}

// onModeBlockedKey holds the prompt while the cycle sits on a blocked mode. Esc
// returns to the accepted mode; the mode switch, scrolling, help, and (at idle)
// the read-only panels still work; every key that would edit or send the draft
// is swallowed with a reminder. The draft itself is kept untouched.
func (m Model) onModeBlockedKey(msg tea.KeyPressMsg) (Model, bool) {
	if !m.modeBlocked() || (m.phase != phaseIdle && m.phase != phaseRunning) {
		return m, false
	}
	switch {
	case key.Matches(msg, m.keys.Cancel):
		return m.leaveBlockedMode(), true
	case key.Matches(msg, m.keys.Help):
		m.showHelp = true
		m.helpScroll = 0
		m.prompt.Blur()
		return m, true
	case key.Matches(msg, m.keys.ModeSwitch), key.Matches(msg, m.keys.CopySelection),
		key.Matches(msg, m.keys.ScrollU), key.Matches(msg, m.keys.ScrollD),
		key.Matches(msg, m.keys.ScrollTop), key.Matches(msg, m.keys.ScrollBottom):
		return m, false
	case m.phase == phaseIdle && (key.Matches(msg, m.keys.Agents) || key.Matches(msg, m.keys.MCPPanel) ||
		key.Matches(msg, m.keys.Resources) || key.Matches(msg, m.keys.Prompts) || key.Matches(msg, m.keys.Effort)):
		return m, false
	}
	m.statusMsg = m.deps.Theme.Style("warning").Render(m.modeBlockedStatus())
	return m, true
}

// renderModeBlocked replaces the prompt while the mode is blocked: why the mode
// cannot apply, what makes it apply, and how to get back.
func (m Model) renderModeBlocked() string {
	th := m.deps.Theme
	style := inputRailStyle(th, m.inputMode())
	contentW := max(1, m.width-style.GetHorizontalFrameSize())
	wrap := lipgloss.NewStyle().Width(contentW).Background(th.Color("bgPanel"))
	lines := []string{
		wrap.Inherit(th.Style("warning")).Bold(true).Render("⚠ mode " + m.currentModeToken() + " is blocked: prompts and edits are paused, your draft is kept"),
		wrap.Inherit(th.Style("text")).Render(m.modeRestartHint(permissionModeEntry(m.currentModeToken()))),
		wrap.Inherit(th.Style("muted")).Render(m.modeEscapeHint()),
	}
	return style.Render(strings.Join(lines, "\n"))
}

// modeLabel names a landed mode for the footer. A mode below the running
// posture applies only its session half, so the label says the posture stays.
func (m Model) modeLabel(e PermissionModeEntry) string {
	if postureRank(e.Posture) < postureRank(m.caps.Posture) {
		return e.Name + " · posture " + m.caps.Posture + " stays until restart"
	}
	return e.Name
}

// modeRestartHint says why a mode cannot apply now and what makes it apply.
func (m Model) modeRestartHint(e PermissionModeEntry) string {
	checker := postureRank(e.Posture) >= postureRank(postureAuto)
	if !m.deps.Embedded {
		hint := e.Name + " is fixed by the remote server: its operator must restart mecated with --permission-mode " + e.Name
		if checker {
			hint += " and a guardrails checker"
		}
		return hint
	}
	if checker {
		return e.Name + " needs a guardrails checker and a restart: set guardrails.model in settings.yaml, then relaunch with mecatui --permission-mode " + e.Name
	}
	return e.Name + " needs a restart: quit and relaunch with mecatui --permission-mode " + e.Name
}

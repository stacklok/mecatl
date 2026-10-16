package ui

import (
	tea "charm.land/bubbletea/v2"
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
// below the running one applies its session half live. One that would raise the
// posture cannot apply, because the posture (the server ceiling) is fixed when
// the server starts: the cycle only parks its cursor there so the next press
// moves on, while the session keeps its confirmed mode, the draft stays usable,
// and the footer says what would make the mode available.
func (m Model) cycleMode() (tea.Model, tea.Cmd) {
	cur := m.currentModeToken()
	if m.modeRefused != "" {
		cur, m.modeRefused = m.modeRefused, ""
	}
	next := permissionModeEntry(nextModeToken(cur))
	if postureRank(next.Posture) > postureRank(m.caps.Posture) {
		m.modeCursor = modeCursor{token: next.Name, session: m.desiredMode()}
		m.statusMsg = m.deps.Theme.Style("warning").Render(m.modeUnavailableStatus(next))
		(&m).relayout()
		return m, nil
	}
	mm, cmd := m.switchMode(next)
	m = mm.(Model)
	m.modeCursor = modeCursor{token: next.Name, session: m.desiredMode()}
	(&m).relayout()
	return m, cmd
}

// headerMode is the session mode the header shows: the confirmed mode, or the
// requested one marked pending. A mode the cycle parked on because it is
// unavailable never shows here: the session is not running it.
//
// "pending" means a SetMode round-trip is in flight. When the pending mode
// equals the server-confirmed active mode (m.pendingMode == m.activeMode) the
// session IS already in the target mode; the in-flight call is a no-op
// confirmation. Showing "pending" in that case is misleading — e.g. cycling
// through other modes and back to the starting auto mode would flash
// "auto pending" even though the session never left auto from the user's
// perspective. Skip the suffix in that case.
func (m Model) headerMode() string {
	mode := m.activeMode
	if mode == "" {
		mode = m.deps.Mode
	}
	if m.pendingMode != "" {
		mode = m.pendingMode
		if m.pendingMode != m.activeMode {
			// Only show "pending" when the in-flight mode differs from what the
			// server has already confirmed — a genuine transition, not a no-op.
			mode += " pending"
		}
	}
	return mode
}

// modeUnavailableStatus is the footer notice for a mode above the server
// ceiling: the session stays in its confirmed mode, what would make the mode
// available, and that the next press moves on.
func (m Model) modeUnavailableStatus(e PermissionModeEntry) string {
	return "mode " + e.Name + " unavailable, staying in " + m.acceptedModeToken() + " — " +
		m.modeRestartHint(e) + " · " + firstKey(m.keys.ModeSwitch, "shift+tab") + " next mode"
}

// modeLabel names a landed mode for the footer. All modes at or below the
// server ceiling are fully effective — allow-all and loose-substitution are
// session-mode-driven, not posture-driven, so switching to a lower mode
// actually changes behavior and nothing "stays until restart".
func (Model) modeLabel(e PermissionModeEntry) string {
	return e.Name
}

// modeRestartHint says what makes a mode above the server ceiling available: a
// restart with a higher --permission-mode and, for auto only, an enforcing
// guardrails checker on the server.
func (m Model) modeRestartHint(e PermissionModeEntry) string {
	checker := e.Posture == postureAuto
	if !m.deps.Embedded {
		hint := "the remote server's operator must restart mecated with --permission-mode " + e.Name
		if checker {
			hint += " and an enforcing guardrails checker"
		}
		return hint
	}
	hint := "quit and relaunch with mecatui --permission-mode " + e.Name
	if checker {
		hint += "; auto also needs an enforcing guardrails checker (set guardrails.model in settings.yaml)"
	}
	return hint
}

// modeRefusedHint adds what the server needs before it accepts a refused mode.
// Only auto has a prerequisite beyond the ceiling.
func modeRefusedHint(mode string) string {
	if mode == "auto" {
		return " — auto needs an enforcing guardrails checker on the server"
	}
	return ""
}

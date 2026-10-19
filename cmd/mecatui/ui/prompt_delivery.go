package ui

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// deliveryCommand strips exactly one editor delivery prefix, never its payload.
func deliveryCommand(text string) (kind, payload string, ok bool) {
	text = strings.TrimSpace(text)
	name, rest := text, ""
	if i := strings.IndexFunc(text, unicode.IsSpace); i >= 0 {
		name, rest = text[:i], text[i:]
	}
	switch strings.ToLower(name) {
	case "/queue", "/steer":
		return strings.ToLower(name[1:]), strings.TrimSpace(rest), true
	default:
		return "", "", false
	}
}

func (m Model) hasPendingSteer() bool {
	return m.steer != nil && (m.steer.Phase == steerPending || m.steer.Phase == steerSent)
}

func (m Model) deliveryError(message string) (tea.Model, tea.Cmd) {
	m.statusMsg = m.deps.Theme.Style("warning").Render(message)
	if m.phase == phaseRunning {
		m.conv.addError(message)
		m.refreshView()
	}
	return m, nil
}

func (m Model) emptyDelivery() (tea.Model, tea.Cmd) {
	if _, _, explicit := deliveryCommand(m.prompt.Value()); explicit {
		return m.deliveryError("prompt is empty; add text or attached content")
	}
	return m, nil
}

func (m Model) deliverPrompt(kind, text string) (tea.Model, tea.Cmd) {
	if m.phase != phaseRunning {
		return m.submitPromptText(text)
	}
	if kind == "queue" {
		if m.hasPendingSteer() {
			return m.deliveryError("pending steers must drain before queueing; to pull them back, clear the input and press " + m.helpKeyMarkings().editBack)
		}
		return m.queuePrompt(text)
	}
	if len(m.queued) > 0 {
		return m.deliveryError("pending queued prompts must drain before steering; to pull them back, clear the input and press " + m.helpKeyMarkings().editBack)
	}
	if !m.caps.Steer || m.stream == nil {
		return m.deliveryError("steering is unavailable for this active run")
	}
	return m.sendSteer(text)
}

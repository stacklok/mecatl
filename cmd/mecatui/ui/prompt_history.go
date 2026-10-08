package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

const promptHistoryLimit = 100

type promptOrigin uint8

const (
	promptOriginOperator promptOrigin = iota
	promptOriginSynthetic
)

func (m *Model) markSyntheticPrompt() {
	m.promptOrigin = promptOriginSynthetic
	m.promptOriginRevision = m.prompt.ContentRevision()
}

func (m *Model) takePromptOrigin() promptOrigin {
	if m.promptOrigin == promptOriginSynthetic && m.promptOriginRevision == m.prompt.ContentRevision() {
		return promptOriginSynthetic
	}
	m.clearPromptOrigin()
	return promptOriginOperator
}

func (m *Model) clearPromptOrigin() {
	m.promptOrigin = promptOriginOperator
	m.promptOriginRevision = 0
}

func (m *Model) takePromptHistoryText(text string, origin promptOrigin) (string, bool) {
	if m.pendingPromptHistoryRevision != 0 && m.pendingPromptHistoryRevision == m.prompt.ContentRevision() {
		text = m.pendingPromptHistory
		m.pendingPromptHistory = ""
		m.pendingPromptHistoryRevision = 0
		return text, strings.TrimSpace(text) != ""
	}
	m.pendingPromptHistory = ""
	m.pendingPromptHistoryRevision = 0
	return text, origin == promptOriginOperator
}

func historyTextFromSteerSends(sends []steerQueuedSend) string {
	parts := make([]string, 0, len(sends))
	for _, send := range sends {
		if !send.Synthetic && strings.TrimSpace(send.Text) != "" {
			parts = append(parts, send.Text)
		}
	}
	return strings.Join(parts, queueMergeSep)
}

type promptHistoryState struct {
	entries         []string
	index           int
	draft           string
	contentRevision uint64
}

func (h *promptHistoryState) record(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if len(h.entries) == promptHistoryLimit {
		copy(h.entries, h.entries[1:])
		h.entries[len(h.entries)-1] = text
	} else {
		h.entries = append(h.entries, text)
	}
	h.index = 0
	h.draft = ""
}

func (h *promptHistoryState) detachIfMutated(revision uint64, current string) bool {
	if h.index == 0 || h.contentRevision == revision {
		return false
	}
	h.index = 0
	h.draft = current
	return true
}

func (m Model) promptHistoryBlocked() bool {
	return m.prompt.HasSelection() || len(m.stagedMedia) > 0 || len(m.stagedPastes) > 0 ||
		len(m.pendingPromptMedia.Parts) > 0 || len(m.pendingPromptMedia.Descriptors) > 0 ||
		len(m.queued) > 0 || (m.steer != nil && (m.steer.Phase == steerPending || m.steer.Phase == steerSent))
}

func (m Model) onPromptHistoryKey(msg tea.KeyPressMsg) (Model, bool) {
	previous := key.Matches(msg, m.keys.EditBack)
	next := key.Matches(msg, m.keys.HistoryNext)
	if !previous && !next {
		return m, false
	}
	if m.promptHistory.detachIfMutated(m.prompt.ContentRevision(), m.prompt.Value()) {
		return m, false
	}
	if m.promptHistoryBlocked() || len(m.promptHistory.entries) == 0 {
		return m, false
	}
	if previous {
		if !m.prompt.AtFirstVisualRow() {
			return m, false
		}
		if m.promptHistory.index == 0 {
			m.promptHistory.draft = m.prompt.Value()
		}
		if m.promptHistory.index < len(m.promptHistory.entries) {
			m.promptHistory.index++
		}
		m.prompt.Rewrite(m.promptHistory.entries[len(m.promptHistory.entries)-m.promptHistory.index])
		m.promptHistory.contentRevision = m.prompt.ContentRevision()
		return m, true
	}
	if !m.prompt.AtLastVisualRow() || m.promptHistory.index == 0 {
		return m, false
	}
	m.promptHistory.index--
	if m.promptHistory.index == 0 {
		m.prompt.Rewrite(m.promptHistory.draft)
	} else {
		m.prompt.Rewrite(m.promptHistory.entries[len(m.promptHistory.entries)-m.promptHistory.index])
	}
	m.promptHistory.contentRevision = m.prompt.ContentRevision()
	return m, true
}

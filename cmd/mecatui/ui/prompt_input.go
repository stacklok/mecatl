package ui

import (
	"fmt"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

func (m *Model) updatePromptKey(msg tea.KeyPressMsg) tea.Cmd {
	cmd := m.prompt.UpdateKey(msg)
	if m.prompt.HasSelection() && m.sel.active {
		*m = m.clearSelection()
		m.refreshView()
	}
	return cmd
}

func (m *Model) promptSelectAll() {
	*m = m.clearSelection()
	m.prompt.SelectAll()
}

func (m *Model) promptMousePress(mo tea.Mouse) (tea.Cmd, bool) {
	if !m.pasteGateOpen() || !mouseCaptureEnabled(*m) {
		return nil, false
	}
	rect, ok := inputRegionRect(*m)
	if !ok || !rect.contains(mo.X, mo.Y) {
		return nil, false
	}
	*m = m.clearSelection()
	m.refreshView()
	focusCmd := m.prompt.Focus()
	m.prompt.BeginMouseSelection(mo.X-rect.x0, mo.Y-rect.y0)
	return focusCmd, true
}

func (m *Model) promptMouseMotion(mo tea.Mouse) bool {
	if !mouseCaptureEnabled(*m) {
		return false
	}
	rect, ok := inputRegionRect(*m)
	if !ok {
		return false
	}
	return m.prompt.ExtendMouseSelection(mo.X-rect.x0, mo.Y-rect.y0)
}

func (m *Model) promptMouseRelease(mo tea.Mouse) bool {
	rect, ok := inputRegionRect(*m)
	if ok {
		m.prompt.ExtendMouseSelection(mo.X-rect.x0, mo.Y-rect.y0)
	}
	return m.prompt.EndMouseSelection()
}

func (m Model) copyPayload(payload string) (Model, tea.Cmd) {
	if payload == "" {
		return m, nil
	}
	m.statusMsg = m.deps.Theme.Style("muted").Render(fmt.Sprintf("copied %s", plural(len([]rune(payload)), "char")))
	return m, tea.Batch(tea.SetClipboard(payload), m.shellWriteCmd(payload))
}

func (m Model) copyActiveSelection() (tea.Model, tea.Cmd) {
	if m.prompt.HasSelection() {
		return m.copyPayload(m.prompt.SelectedText())
	}
	if m.sel.active && !m.sel.empty() {
		return m.copyPayload(selectedText(m.vp.GetContent(), m.sel))
	}
	return m, nil
}

func (m Model) promptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if key.Matches(msg, m.keys.SelectAll) && m.pasteGateOpen() {
		m.promptSelectAll()
		return m, nil, true
	}
	if key.Matches(msg, m.keys.CopySelection) {
		mm, cmd := m.copyActiveSelection()
		return mm, cmd, true
	}
	return m, nil, false
}

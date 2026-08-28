package ui

import (
	"fmt"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// promptInput is the local seam between Model and bubbles' textarea. It owns
// textarea selection and all prompt-content edits; phase and overlay policy stay
// in update.go.
func (m *Model) promptInput(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return cmd
}

func (m *Model) promptNewline() {
	m.ta.DeleteSelection()
	m.ta.InsertRune('\n')
}

func (m *Model) promptInsert(s string) {
	m.ta.DeleteSelection()
	m.ta.InsertString(s)
}

// promptRewrite is for host/app-owned replacements, never user edits.
func (m *Model) promptRewrite(s string) {
	m.ta.ClearSelection()
	m.promptSelecting = false
	m.ta.SetValue(s)
}

func (m *Model) promptReset() {
	m.ta.ClearSelection()
	m.promptSelecting = false
	m.ta.Reset()
}

func (m *Model) promptSelectAll() {
	*m = m.clearSelection()
	m.ta.SelectAll()
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
	m.ta.BeginSelection(mo.X-rect.x0, mo.Y-rect.y0)
	m.promptSelecting = true
	return m.ta.Focus(), true
}

func (m *Model) promptMouseMotion(mo tea.Mouse) bool {
	if !m.promptSelecting || !mouseCaptureEnabled(*m) {
		return false
	}
	rect, ok := inputRegionRect(*m)
	if !ok {
		return false
	}
	m.ta.ExtendSelection(mo.X-rect.x0, mo.Y-rect.y0)
	return true
}

func (m *Model) promptMouseRelease(mo tea.Mouse) bool {
	if !m.promptSelecting {
		return false
	}
	rect, ok := inputRegionRect(*m)
	if ok {
		m.ta.ExtendSelection(mo.X-rect.x0, mo.Y-rect.y0)
	}
	m.ta.EndSelection()
	m.promptSelecting = false
	return true
}

func (m Model) copyPayload(payload string) (Model, tea.Cmd) {
	if payload == "" {
		return m, nil
	}
	m.statusMsg = m.deps.Theme.Style("muted").Render(fmt.Sprintf("copied %s", plural(len([]rune(payload)), "char")))
	m.refreshView()
	return m, tea.Batch(tea.SetClipboard(payload), m.shellWriteCmd(payload))
}

func (m Model) copyActiveSelection() (tea.Model, tea.Cmd) {
	if m.ta.HasSelection() {
		return m.copyPayload(m.ta.SelectedText())
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

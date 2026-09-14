package ui

import (
	"fmt"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
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
	if !selectable(*m) {
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
	// Mirror the copy into BOTH the clipboard (ctrl/cmd+v) and the X11/Wayland
	// PRIMARY selection (middle-click paste), each via OSC52 AND its shell fallback,
	// so a selection made inside mecatui behaves like a native terminal selection.
	return m, tea.Batch(
		tea.SetClipboard(payload), m.shellWriteCmd(payload),
		tea.SetPrimaryClipboard(payload), m.primaryWriteCmd(payload),
	)
}

func (m Model) copyActiveSelection() (tea.Model, tea.Cmd) {
	if m.prompt.HasSelection() {
		return m.copyPayload(m.prompt.SelectedText())
	}
	if m.sel.active && !m.sel.empty() {
		return m.copySelection()
	}
	return m, nil
}

// clearPrompt clears only the unsent draft. Queue, steer, and run state deliberately
// remain untouched; this action is for abandoning the current composition.
func (m Model) clearPrompt() (tea.Model, tea.Cmd) {
	m.prompt.Reset()
	m.promptRecovery = nil
	m.stagedMedia = nil
	m.stagedPastes = nil
	// Marker counters remain monotonic: an outstanding steer can restore its staged
	// maps through EditBack after this draft is cleared.
	m.pendingPromptMedia = client.MediaResult{}
	return m.afterInputEdit(nil)
}

func (m Model) promptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if key.Matches(msg, m.keys.ClearPrompt) && m.pasteGateOpen() {
		mm, cmd := m.clearPrompt()
		return mm, cmd, true
	}
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

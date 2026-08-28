package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestPromptSelectionMouseEditAndCopy(t *testing.T) {
	m, cb := selModel(t)
	m.ta.SetValue("hello world")
	rect, ok := inputRegionRect(m)
	if !ok {
		t.Fatal("input region unavailable")
	}

	m, _ = pressMouse(m, tea.MouseLeft, rect.x0, rect.y0)
	m, _ = motionMouse(m, rect.x0+5, rect.y0)
	m, cmd := releaseMouse(m, rect.x0+5, rect.y0)
	if cmd != nil || !m.ta.HasSelection() || m.ta.SelectedText() != "hello" {
		t.Fatalf("release must retain prompt selection without copying: selected=%q cmd=%v", m.ta.SelectedText(), cmd)
	}
	if len(cb.wrote) != 0 {
		t.Fatal("release copied prompt selection")
	}

	mm, _ := m.Update(tea.KeyPressMsg{Code: 'X', Text: "X"})
	m = mm.(Model)
	if got := m.ta.Value(); got != "X world" {
		t.Fatalf("typed replacement = %q, want %q", got, "X world")
	}

	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	m = mm.(Model)
	if got := m.ta.Value(); got != "X\n world" {
		t.Fatalf("newline replacement = %q", got)
	}

	mm, _ = m.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	m = mm.(Model)
	if !m.ta.HasSelection() || m.ta.SelectedText() != "X\n world" {
		t.Fatalf("ctrl+g did not select prompt: %q", m.ta.SelectedText())
	}
	mm, cmd = m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl | tea.ModShift})
	m = mm.(Model)
	leaves := collectLeaves(cmd)
	payload, ok := osc52Payload(leaves)
	if !ok || payload != "X\n world" {
		t.Fatalf("copy payload = %q, ok=%v", payload, ok)
	}
	for _, msg := range leaves {
		mm, _ = m.Update(msg)
		m = mm.(Model)
	}
	if len(cb.wrote) != 1 || string(cb.wrote[0]) != "X\n world" {
		t.Fatalf("shell clipboard writes = %q", cb.wrote)
	}
}

func TestPromptSelectionKeyboardWorksWithoutMouseAndStagingReplaces(t *testing.T) {
	m, _ := selModel(t)
	m.deps.NoMouse = true
	m.ta.SetValue("before after")
	m.ta.SelectAll()

	mm, _ := m.Update(pasteMsg(largePasteText()))
	m = mm.(Model)
	if !strings.Contains(m.ta.Value(), "[Pasted text #1]") || strings.Contains(m.ta.Value(), "before") {
		t.Fatalf("staged paste did not replace selection: %q", m.ta.Value())
	}
	if m.ta.HasSelection() {
		t.Fatal("paste left a stale selection")
	}
}

func TestPromptAndConversationSelectionsAreExclusive(t *testing.T) {
	m, _ := selModel(t)
	m.ta.SetValue("prompt")
	m.ta.SelectAll()
	top := convTopRow(m)
	m, _ = pressMouse(m, tea.MouseLeft, 0, top)
	if m.ta.HasSelection() {
		t.Fatal("conversation selection did not clear prompt selection")
	}

	rect, ok := inputRegionRect(m)
	if !ok {
		t.Fatal("input region unavailable")
	}
	m, _ = pressMouse(m, tea.MouseLeft, rect.x0, rect.y0)
	if m.sel.active {
		t.Fatal("prompt selection did not clear conversation selection")
	}
}

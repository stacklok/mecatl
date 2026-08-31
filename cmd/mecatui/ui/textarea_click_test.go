package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestTextareaClickPositionsCaret drives prompt clicks through the real mouse
// reducer. The textarea owns wrapping; these cases pin the public click contract
// rather than its private wrap implementation.
func TestTextareaClickPositionsCaret(t *testing.T) {
	m, _ := selModel(t)
	m.prompt.Rewrite("hello\nworld")

	rect, ok := inputRegionRect(m)
	if !ok {
		t.Fatal("inputRegionRect returned no input region")
	}
	for _, tc := range []struct {
		name       string
		x, y       int
		wantLine   int
		wantColumn int
	}{
		{"first line", rect.x0 + 3, rect.y0, 0, 3},
		{"second line", rect.x0 + 2, rect.y0 + 1, 1, 2},
		{"past end clamps", rect.x1 - 1, rect.y0 + 1, 1, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: tc.x, Y: tc.y})
			clicked := got.(Model)
			if line, col := clicked.prompt.Line(), clicked.prompt.Column(); line != tc.wantLine || col != tc.wantColumn {
				t.Fatalf("caret = (%d,%d), want (%d,%d)", line, col, tc.wantLine, tc.wantColumn)
			}
			if !clicked.prompt.Focused() {
				t.Fatal("click did not focus textarea")
			}
		})
	}
}

func TestTextareaClickMapsSoftWrapAndScroll(t *testing.T) {
	m, _ := selModel(t)
	m = applyAll(m, tea.WindowSizeMsg{Width: 20, Height: 30})
	m.prompt.Rewrite("abcdefghijklmnopqrst")
	rect, ok := inputRegionRect(m)
	if !ok {
		t.Fatal("inputRegionRect returned no input region")
	}

	got, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: rect.x0 + 2, Y: rect.y0 + 1})
	wrapped := got.(Model)
	if line, col := wrapped.prompt.Line(), wrapped.prompt.Column(); line != 0 || col != 19 {
		t.Fatalf("soft-wrapped caret = (%d,%d), want (0,19)", line, col)
	}

	m.prompt.Rewrite("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten")
	_ = m.prompt.UpdateKey(tea.KeyPressMsg{Code: tea.KeyHome})
	for range 9 {
		_ = m.prompt.UpdateKey(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if m.prompt.ScrollYOffset() == 0 {
		t.Fatal("test setup did not scroll textarea")
	}
	rect, ok = inputRegionRect(m)
	if !ok {
		t.Fatal("inputRegionRect returned no input region after scroll")
	}
	got, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: rect.x0 + 1, Y: rect.y0})
	scrolled := got.(Model)
	if line, col := scrolled.prompt.Line(), scrolled.prompt.Column(); line != 2 || col != 1 {
		t.Fatalf("scrolled caret = (%d,%d), want (2,1)", line, col)
	}
}

func TestTextareaDragSelectionEditsAcrossSoftWrapAndScroll(t *testing.T) {
	m, _ := selModel(t)
	m = applyAll(m, tea.WindowSizeMsg{Width: 20, Height: 30})

	for _, tc := range []struct {
		text                   string
		wantSelected           string
		wantFromRow, wantToRow int
		wantFromCol, wantToCol int
	}{
		{"abcdefghijklmnopqrst", "abcdefghijklmnopqrs", 0, 0, 0, 19},
		{"one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten", "three\nfo", 2, 3, 0, 2},
	} {
		text := tc.text
		m.prompt.Rewrite(text)
		if strings.Contains(text, "\n") {
			for range 9 {
				mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
				m = mm.(Model)
			}
			if m.prompt.ScrollYOffset() == 0 {
				t.Fatal("test setup did not scroll textarea")
			}
		}
		rect, ok := inputRegionRect(m)
		if !ok {
			t.Fatal("input region unavailable")
		}
		m, _ = pressMouse(m, tea.MouseLeft, rect.x0, rect.y0)
		m, _ = motionMouse(m, rect.x0+2, rect.y0+1)
		m, _ = releaseMouse(m, rect.x0+2, rect.y0+1)
		selected := m.prompt.SelectedText()
		from, to, ok := m.prompt.Selection()
		if !ok || selected != tc.wantSelected || from.Row != tc.wantFromRow || from.Col != tc.wantFromCol || to.Row != tc.wantToRow || to.Col != tc.wantToCol {
			t.Fatalf("drag selection = %q, (%d,%d)-(%d,%d), want %q, (%d,%d)-(%d,%d)",
				selected, from.Row, from.Col, to.Row, to.Col,
				tc.wantSelected, tc.wantFromRow, tc.wantFromCol, tc.wantToRow, tc.wantToCol)
		}
		mm, _ := m.Update(tea.KeyPressMsg{Code: 'X', Text: "X"})
		m = mm.(Model)
		if m.prompt.HasSelection() || strings.Contains(m.prompt.Value(), selected) {
			t.Fatalf("edit did not replace drag selection %q in %q", selected, m.prompt.Value())
		}
	}
}

func TestTextareaClickHonorsMouseCapture(t *testing.T) {
	m, _ := selModel(t)
	m.prompt.Rewrite("hello")
	_ = m.prompt.UpdateKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	rect, ok := inputRegionRect(m)
	if !ok {
		t.Fatal("inputRegionRect returned no input region")
	}
	m.deps.NoMouse = true

	got, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: rect.x0, Y: rect.y0})
	clicked := got.(Model)
	if clicked.prompt.Column() != len("hello") {
		t.Errorf("caret moved with mouse capture disabled: column = %d, want %d", clicked.prompt.Column(), len("hello"))
	}
}

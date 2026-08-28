package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestTextareaClickPositionsCaret drives prompt clicks through the real mouse
// reducer. The textarea owns wrapping; these cases pin the public click contract
// rather than its private wrap implementation.
func TestTextareaClickPositionsCaret(t *testing.T) {
	m, _ := selModel(t)
	m.ta.Rewrite("hello\nworld")

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
			if line, col := clicked.ta.Line(), clicked.ta.Column(); line != tc.wantLine || col != tc.wantColumn {
				t.Fatalf("caret = (%d,%d), want (%d,%d)", line, col, tc.wantLine, tc.wantColumn)
			}
			if !clicked.ta.Focused() {
				t.Fatal("click did not focus textarea")
			}
		})
	}
}

func TestTextareaClickMapsSoftWrapAndScroll(t *testing.T) {
	m, _ := selModel(t)
	m = applyAll(m, tea.WindowSizeMsg{Width: 20, Height: 30})
	m.ta.Rewrite("abcdefghijklmnopqrst")
	rect, ok := inputRegionRect(m)
	if !ok {
		t.Fatal("inputRegionRect returned no input region")
	}

	got, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: rect.x0 + 2, Y: rect.y0 + 1})
	wrapped := got.(Model)
	if line, col := wrapped.ta.Line(), wrapped.ta.Column(); line != 0 || col != 19 {
		t.Fatalf("soft-wrapped caret = (%d,%d), want (0,19)", line, col)
	}

	m.ta.Rewrite("one\ntwo\nthree\nfour\nfive")
	_ = m.ta.UpdateUserInput(tea.KeyPressMsg{Code: tea.KeyHome})
	for range 4 {
		_ = m.ta.UpdateUserInput(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if m.ta.ScrollYOffset() == 0 {
		t.Fatal("test setup did not scroll textarea")
	}
	rect, ok = inputRegionRect(m)
	if !ok {
		t.Fatal("inputRegionRect returned no input region after scroll")
	}
	got, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: rect.x0 + 1, Y: rect.y0})
	scrolled := got.(Model)
	if line, col := scrolled.ta.Line(), scrolled.ta.Column(); line != 2 || col != 1 {
		t.Fatalf("scrolled caret = (%d,%d), want (2,1)", line, col)
	}
}

func TestTextareaClickHonorsMouseCapture(t *testing.T) {
	m, _ := selModel(t)
	m.ta.Rewrite("hello")
	_ = m.ta.UpdateUserInput(tea.KeyPressMsg{Code: tea.KeyEnd})
	rect, ok := inputRegionRect(m)
	if !ok {
		t.Fatal("inputRegionRect returned no input region")
	}
	m.deps.NoMouse = true

	got, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: rect.x0, Y: rect.y0})
	clicked := got.(Model)
	if clicked.ta.Column() != len("hello") {
		t.Errorf("caret moved with mouse capture disabled: column = %d, want %d", clicked.ta.Column(), len("hello"))
	}
}

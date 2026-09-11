package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestReadlineCursorKeysEditPrompt(t *testing.T) {
	t.Run("ctrl+a moves to line start", func(t *testing.T) {
		m, _ := newQueueModel(t)
		m.prompt.Rewrite("alpha\nbravo")

		mm, _ := m.Update(ctrlKey('a'))
		m = mm.(Model)
		if m.prompt.Line() != 1 || m.prompt.Column() != 0 {
			t.Fatalf("cursor = (%d,%d), want second line start", m.prompt.Line(), m.prompt.Column())
		}
	})

	t.Run("ctrl+e moves to line end", func(t *testing.T) {
		m, _ := newQueueModel(t)
		m.prompt.Rewrite("alpha\nbravo")
		m = applyAll(m,
			tea.KeyPressMsg{Code: tea.KeyLeft},
			tea.KeyPressMsg{Code: tea.KeyLeft},
		)
		if m.prompt.Column() != 3 {
			t.Fatalf("test setup cursor column = %d, want 3", m.prompt.Column())
		}

		mm, _ := m.Update(ctrlKey('e'))
		m = mm.(Model)
		if m.prompt.Line() != 1 || m.prompt.Column() != 5 {
			t.Fatalf("cursor = (%d,%d), want second line end", m.prompt.Line(), m.prompt.Column())
		}
	})

	t.Run("ctrl+p moves to previous line", func(t *testing.T) {
		m, _ := newQueueModel(t)
		m.prompt.Rewrite("alpha\nbravo")

		mm, _ := m.Update(ctrlKey('p'))
		m = mm.(Model)
		if m.prompt.Line() != 0 || m.prompt.Column() != 5 {
			t.Fatalf("cursor = (%d,%d), want previous line at column 5", m.prompt.Line(), m.prompt.Column())
		}
	})
}

func TestReadlineCursorKeysEditPromptWhileRunning(t *testing.T) {
	for _, tc := range []struct {
		name       string
		text       string
		before     []tea.KeyPressMsg
		key        tea.KeyPressMsg
		wantLine   int
		wantColumn int
	}{
		{name: "ctrl+a moves to line start", text: "alpha\nbravo", key: ctrlKey('a'), wantLine: 1, wantColumn: 0},
		{name: "ctrl+e moves to line end", text: "alpha\nbravo", before: []tea.KeyPressMsg{{Code: tea.KeyLeft}, {Code: tea.KeyLeft}}, key: ctrlKey('e'), wantLine: 1, wantColumn: 5},
		{name: "ctrl+p moves to previous line", text: "alpha\nbravo", key: ctrlKey('p'), wantLine: 0, wantColumn: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newQueueModel(t)
			m = startRunning(t, m, "first")
			m.prompt.Rewrite(tc.text)
			for _, before := range tc.before {
				mm, _ := m.Update(before)
				m = mm.(Model)
			}

			mm, _ := m.Update(tc.key)
			m = mm.(Model)
			if m.phase != phaseRunning {
				t.Fatalf("phase = %v, want phaseRunning", m.phase)
			}
			if m.team.view != teamNone {
				t.Fatalf("key opened the agents overlay while running: view=%v", m.team.view)
			}
			if m.prompt.Line() != tc.wantLine || m.prompt.Column() != tc.wantColumn {
				t.Fatalf("cursor = (%d,%d), want (%d,%d)", m.prompt.Line(), m.prompt.Column(), tc.wantLine, tc.wantColumn)
			}
		})
	}
}

func TestPromptUndoKeysRemainUnbound(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{name: "ctrl+_", key: ctrlKey('_')},
		{name: "ctrl+-", key: ctrlKey('-')},
		{name: "ctrl+shift+-", key: tea.KeyPressMsg{Code: '-', Mod: tea.ModCtrl | tea.ModShift}},
		{name: "ctrl+shift+_", key: tea.KeyPressMsg{Code: '_', Mod: tea.ModCtrl | tea.ModShift}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newQueueModel(t)
			m.prompt.Rewrite("unchanged draft")

			mm, _ := m.Update(tc.key)
			m = mm.(Model)
			if got := m.prompt.Value(); got != "unchanged draft" {
				t.Fatalf("prompt value = %q, want unchanged draft", got)
			}
		})
	}
}

func TestNewlineActionsKeepCaretVisiblePastDynamicCap(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"shift+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}},
		{"ctrl+j", tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newQueueModel(t)
			m.prompt.Rewrite("replace me")
			m.prompt.SelectAll()
			for range 9 {
				mm, _ := m.Update(tc.key)
				m = mm.(Model)
			}
			if got := m.prompt.Value(); got != strings.Repeat("\n", 9) {
				t.Fatalf("newline action did not replace selection and append newlines: %q", got)
			}
			if m.prompt.Height() != 8 || m.prompt.ScrollYOffset() == 0 {
				t.Fatalf("newline action did not cap and scroll: height=%d offset=%d", m.prompt.Height(), m.prompt.ScrollYOffset())
			}
			info := m.prompt.LineInfo()
			visualRow := m.prompt.Line() + info.RowOffset - m.prompt.ScrollYOffset()
			if visualRow < 0 || visualRow >= m.prompt.Height() {
				t.Fatalf("caret visual row = %d, want within prompt viewport height %d", visualRow, m.prompt.Height())
			}
		})
	}
}

func TestClearPromptPreservesOutstandingSteerMarkerIdentities(t *testing.T) {
	m, _ := newSteerModel(t, true)
	m.caps.Image = true
	m = startRunning(t, m, "first")

	oldPaste := strings.Repeat("old", pasteCharThreshold)
	mm, _ := m.Update(clipboardResultMsg{mime: "image/png", data: []byte("old-image")})
	m = mm.(Model)
	mm, _ = m.Update(pasteMsg(oldPaste))
	m = mm.(Model)
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	runBatchLeaves(cmd)

	mm, _ = m.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	m = mm.(Model)
	if m.nextMediaN != 1 || m.nextPasteN != 1 {
		t.Fatalf("ClearPrompt reset marker counters: media=%d paste=%d", m.nextMediaN, m.nextPasteN)
	}

	newPaste := strings.Repeat("new", pasteCharThreshold)
	mm, _ = m.Update(clipboardResultMsg{mime: "image/png", data: []byte("new-image")})
	m = mm.(Model)
	mm, _ = m.Update(pasteMsg(newPaste))
	m = mm.(Model)
	mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	runBatchLeaves(cmd)

	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = mm.(Model)
	if got := string(m.stagedMedia["[Image #1]"].data); got != "old-image" {
		t.Fatalf("old image mapping = %q, want old-image", got)
	}
	if got := string(m.stagedMedia["[Image #2]"].data); got != "new-image" {
		t.Fatalf("new image mapping = %q, want new-image", got)
	}
	if got := m.stagedPastes["[Pasted text #1]"]; got != oldPaste {
		t.Fatalf("old paste mapping = %q, want old payload", got)
	}
	if got := m.stagedPastes["[Pasted text #2]"]; got != newPaste {
		t.Fatalf("new paste mapping = %q, want new payload", got)
	}
}

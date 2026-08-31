package prompttextarea

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestHostMutationsClearSelection(t *testing.T) {
	editor := New(Config{})
	editor.Rewrite("before")
	editor.SelectAll()
	editor.Rewrite("after")
	if editor.HasSelection() || editor.Value() != "after" {
		t.Fatalf("rewrite = (%q, selected=%t), want (after, false)", editor.Value(), editor.HasSelection())
	}

	editor.SelectAll()
	editor.Reset()
	if editor.HasSelection() || !editor.Empty() {
		t.Fatalf("reset = (%q, selected=%t), want empty without selection", editor.Value(), editor.HasSelection())
	}
}

func TestUserMutationsReplaceSelection(t *testing.T) {
	editor := New(Config{})
	editor.Rewrite("before")
	editor.SelectAll()
	editor.InsertText("inserted")
	if got := editor.Value(); got != "inserted" {
		t.Fatalf("insert = %q, want inserted", got)
	}

	editor.SelectAll()
	editor.PasteText("after")
	if got := editor.Value(); got != "after" {
		t.Fatalf("paste = %q, want after", got)
	}

	editor.SelectAll()
	editor.DeleteSelection()
	if got := editor.Value(); got != "" {
		t.Fatalf("delete selection = %q, want empty", got)
	}

	editor.Rewrite("before")
	editor.SelectAll()
	editor.InsertNewline()
	if got := editor.Value(); got != "\n" {
		t.Fatalf("newline = %q, want newline", got)
	}
}

func TestKeyUpdatePreservesUpstreamSelectionBehavior(t *testing.T) {
	editor := New(Config{})
	editor.Rewrite("before")
	editor.SelectAll()
	editor.UpdateKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if got := editor.Value(); got != "x" {
		t.Fatalf("key update = %q, want x", got)
	}
}

func TestDynamicHeightTracksSoftWrapAndCaps(t *testing.T) {
	editor := New(Config{})
	editor.SetWidth(10)
	if got := editor.Height(); got != 3 {
		t.Fatalf("empty height = %d, want minimum 3", got)
	}

	editor.Rewrite("12345678901234567890123456789012345678901234567890123456789012345678901234567890")
	if got := editor.Height(); got != 8 {
		t.Fatalf("soft-wrapped height = %d, want capped 8", got)
	}
	editor.UpdateKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	if editor.ScrollYOffset() == 0 {
		t.Fatal("capped soft-wrapped editor did not scroll to keep the caret visible")
	}
	if info := editor.LineInfo(); info.RowOffset-editor.ScrollYOffset() < 0 || info.RowOffset-editor.ScrollYOffset() >= editor.Height() {
		t.Fatalf("soft-wrapped cursor visual row = %d, want within viewport height %d", info.RowOffset-editor.ScrollYOffset(), editor.Height())
	}

	editor.Rewrite("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine")
	editor.UpdateKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	if got := editor.Height(); got != 8 {
		t.Fatalf("explicit-newline height = %d, want capped 8", got)
	}
	if editor.ScrollYOffset() == 0 {
		t.Fatal("capped explicit-newline editor did not scroll to keep the caret visible")
	}
	if info := editor.LineInfo(); editor.Line()+info.RowOffset-editor.ScrollYOffset() < 0 || editor.Line()+info.RowOffset-editor.ScrollYOffset() >= editor.Height() {
		t.Fatalf("explicit-newline cursor visual row = %d, want within viewport height %d", editor.Line()+info.RowOffset-editor.ScrollYOffset(), editor.Height())
	}

	editor.Rewrite("short")
	if got := editor.Height(); got != 3 {
		t.Fatalf("shrunk height = %d, want minimum 3", got)
	}
}

func TestDynamicHeightAcceptsMoreThanEightLogicalLines(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input func(*Editor)
	}{
		{"typing", func(editor *Editor) {
			for range 10 {
				editor.UpdateKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
				editor.UpdateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			}
		}},
		{"paste", func(editor *Editor) {
			editor.UpdatePaste(tea.PasteMsg{Content: "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			editor := New(Config{})
			editor.SetWidth(20)
			tc.input(&editor)
			if got := len(strings.Split(strings.TrimSuffix(editor.Value(), "\n"), "\n")); got != 10 {
				t.Fatalf("logical lines = %d, want 10; input was rejected at the visible-height cap", got)
			}
			editor.UpdateKey(tea.KeyPressMsg{Code: tea.KeyEnd})
			if editor.ScrollYOffset() == 0 {
				t.Fatal("editor did not scroll after content exceeded the eight-row viewport")
			}
		})
	}
}

func TestHostSelectionOperationsStopMouseGesture(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(*Editor)
	}{
		{"rewrite", func(e *Editor) { e.Rewrite("after") }},
		{"reset", func(e *Editor) { e.Reset() }},
		{"clear selection", func(e *Editor) { e.ClearSelection() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			editor := New(Config{})
			editor.Rewrite("before")
			editor.BeginMouseSelection(0, 0)
			tc.stop(&editor)
			if editor.ExtendMouseSelection(3, 0) {
				t.Fatal("host operation left mouse selection active")
			}
			if editor.EndMouseSelection() {
				t.Fatal("host operation left mouse selection active")
			}
		})
	}
}

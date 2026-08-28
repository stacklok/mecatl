package prompttextarea

import (
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
	_ = editor.UpdateUserInput(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if got := editor.Value(); got != "x" {
		t.Fatalf("key update = %q, want x", got)
	}
}

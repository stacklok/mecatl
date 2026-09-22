package ui

import (
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

func TestPresentListRow(t *testing.T) {
	selected := lipgloss.NewStyle().Bold(true)
	unselected := lipgloss.NewStyle().Italic(true)

	got := presentListRow(bounded.ListRow{Text: "first", Gutter: "  ", Selected: true, CursorMarker: true}, selected, unselected)
	if got.Text != "▶ first" {
		t.Errorf("selected row text = %q, want cursor marker", got.Text)
	}
	if got.Style.Render("x") != selected.Render("x") {
		t.Error("selected row did not retain selected style")
	}

	got = presentListRow(bounded.ListRow{Text: "continuation", Gutter: "  "}, selected, unselected)
	if got.Text != "  continuation" {
		t.Errorf("unselected row text = %q, want blank gutter", got.Text)
	}
	if got.Style.Render("x") != unselected.Render("x") {
		t.Error("unselected row did not retain unselected style")
	}
}

package ui

import (
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

func TestPresentListRow(t *testing.T) {
	selected := lipgloss.NewStyle().Bold(true)
	unselected := lipgloss.NewStyle().Italic(true)

	for _, tc := range []struct {
		name string
		row  bounded.ListRow
		want string
	}{
		{name: "selection only", row: bounded.ListRow{Text: "first", GutterCells: 1, Selected: true, CursorMarker: true}, want: "▶ first"},
		{name: "selection only unselected", row: bounded.ListRow{Text: "first", GutterCells: 1}, want: "  first"},
		{name: "one status", row: bounded.ListRow{Text: "first", GutterCells: 2, CursorMarker: true, StatusCells: [2]string{"★", ""}}, want: "▶★ first"},
		{name: "one empty status", row: bounded.ListRow{Text: "first", GutterCells: 2}, want: "   first"},
		{name: "two statuses", row: bounded.ListRow{Text: "first", GutterCells: 3, CursorMarker: true, StatusCells: [2]string{"●", "★"}}, want: "▶●★ first"},
		{name: "second status", row: bounded.ListRow{Text: "first", GutterCells: 3, StatusCells: [2]string{"", "★"}}, want: "  ★ first"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := presentListRow(tc.row, selected, unselected)
			if got.Text != tc.want {
				t.Errorf("row text = %q, want %q", got.Text, tc.want)
			}
			wantStyle := unselected
			if tc.row.Selected {
				wantStyle = selected
			}
			if got.Style.Render("x") != wantStyle.Render("x") {
				t.Error("row did not retain the expected style")
			}
		})
	}
}

func TestPresentListRowKeepsTextAlignmentAcrossGutters(t *testing.T) {
	for _, row := range []bounded.ListRow{
		{Text: "text", GutterCells: 1, CursorMarker: true},
		{Text: "text", GutterCells: 2, StatusCells: [2]string{"★", ""}},
		{Text: "text", GutterCells: 3, StatusCells: [2]string{"●", "★"}},
	} {
		got := presentListRow(row, lipgloss.NewStyle(), lipgloss.NewStyle()).Text
		if got[len(got)-4:] != "text" {
			t.Fatalf("text is not preserved after gutter: %q", got)
		}
	}
}

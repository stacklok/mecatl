package ui

import (
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

// listRowPresentation is the surface-ready portion of a bounded list row.
// Surfaces retain ownership of their surrounding chrome and interaction state.
type listRowPresentation struct {
	Text  string
	Style lipgloss.Style
}

// presentListRow applies the common selection, status-cell, and selected-row style
// policy. A list gutter has its configured cells followed by one padding cell.
func presentListRow(row bounded.ListRow, selected, unselected lipgloss.Style) listRowPresentation {
	cells := max(1, min(3, row.GutterCells))
	gutter := " "
	if row.CursorMarker {
		gutter = "▶"
	}
	for i := range cells - 1 {
		cell := row.StatusCells[i]
		if cell == "" {
			cell = " "
		}
		gutter += cell
	}
	gutter += " "
	style := unselected
	if row.Selected {
		style = selected
	}
	return listRowPresentation{Text: gutter + row.Text, Style: style}
}

package ui

import (
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

const defaultListCursorMarker = "▶ "

// listRowPresentation is the surface-ready portion of a bounded list row.
// Surfaces retain ownership of their surrounding chrome and interaction state.
type listRowPresentation struct {
	Text  string
	Style lipgloss.Style
}

// presentListRow applies the common cursor gutter and selected-row style policy.
func presentListRow(row bounded.ListRow, selected, unselected lipgloss.Style) listRowPresentation {
	gutter := row.Gutter
	if row.CursorMarker {
		gutter = defaultListCursorMarker
	}
	style := unselected
	if row.Selected {
		style = selected
	}
	return listRowPresentation{Text: gutter + row.Text, Style: style}
}

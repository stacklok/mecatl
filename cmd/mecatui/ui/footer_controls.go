package ui

import (
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

const footerCloseLabel = "Close"

type footerControlSpec struct {
	hint, label string
	enabled     bool
}

type renderedFooterControl struct {
	rect  cellRect
	index int
}

// renderFooterControls lays out enabled footer controls without assigning their actions.
func renderFooterControls(th theme.Theme, width int, specs []footerControlSpec) ([]string, []renderedFooterControl) {
	var lines []string
	var controls []renderedFooterControl
	for index, spec := range specs {
		if !spec.enabled {
			continue
		}
		label := "[" + terminaltext.SanitizeSingleLine(spec.hint) + " " + spec.label + "]"
		w := ansi.StringWidth(label)
		if w > width {
			return nil, nil
		}
		x := 0
		if len(lines) > 0 {
			x = ansi.StringWidth(lines[len(lines)-1]) + 1
		}
		if len(lines) == 0 || x+w > width {
			lines = append(lines, "")
			x = 0
		}
		y := len(lines) - 1
		if x > 0 {
			lines[y] += " "
		}
		lines[y] += th.Style("viewport").Bold(true).Render(label)
		controls = append(controls, renderedFooterControl{rect: cellRect{x0: x, x1: x + w, y0: y, y1: y + 1}, index: index})
	}
	return lines, controls
}

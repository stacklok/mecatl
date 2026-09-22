package cards

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func styledRows(sections []StyledSectionInput, card lipgloss.Style) []Row {
	top := card.GetBorderTopSize() + card.GetPaddingTop()
	bottom := card.GetBorderBottomSize() + card.GetPaddingBottom()
	rows := make([]Row, 0, top+bottom+len(sections))
	for range top {
		rows = append(rows, Row{Region: RegionChrome, FallbackRow: len(rows)})
	}
	bodyWidth := card.GetWidth() - card.GetHorizontalFrameSize()
	leading := card.GetBorderLeftSize() + card.GetPaddingLeft()
	offsets := map[Region]int{}
	for _, section := range sections {
		if section.Text == "" {
			continue
		}
		layoutRows := styledLayoutRows(section.Text, bodyWidth)
		semanticRows := styledSemanticRows(section, bodyWidth)
		for i, text := range layoutRows {
			semantic := strings.TrimRight(text, " ")
			if i < len(semanticRows) && strings.TrimRight(semanticRows[i], " ") == semantic {
				semantic = semanticRows[i]
			}
			row := Row{Region: section.Region, FallbackRow: len(rows)}
			if section.Region != RegionChrome {
				row.Text = true
				row.SourceOffset = offsets[section.Region]
				row.LeadingColumn = leading
				row.GraphemeSpan = graphemeCount(semantic)
				offsets[section.Region] += row.GraphemeSpan
			}
			rows = append(rows, row)
		}
	}
	for range bottom {
		rows = append(rows, Row{Region: RegionChrome, FallbackRow: len(rows)})
	}
	return rows
}

func styledLayoutRows(text string, bodyWidth int) []string {
	layout := lipgloss.NewStyle()
	if bodyWidth > 0 {
		layout = layout.Width(bodyWidth)
	}
	rows := strings.Split(layout.Render(text), "\n")
	for i := range rows {
		rows[i] = ansi.Strip(rows[i])
	}
	return rows
}

func styledSemanticRows(section StyledSectionInput, bodyWidth int) []string {
	text := ansi.Strip(section.Text)
	wrapped := text
	if bodyWidth > 0 {
		wrapped = ansi.Hardwrap(text, bodyWidth, true)
	}
	rows := strings.Split(wrapped, "\n")
	if sourceLines := strings.Split(text, "\n"); len(sourceLines) > 0 {
		last := sourceLines[len(sourceLines)-1]
		trailing := last[len(strings.TrimRight(last, " ")):]
		if section.Trailing > len(trailing) {
			trailing = strings.Repeat(" ", section.Trailing)
		}
		if trailing != "" && len(rows) > 0 && !strings.HasSuffix(rows[len(rows)-1], trailing) {
			rows[len(rows)-1] += trailing
		}
	}
	return rows
}

func fallbackStyledRows(sections []StyledSectionInput, lines []string, card lipgloss.Style) []Row {
	rows := make([]Row, len(lines))
	for i := range rows {
		rows[i] = Row{Region: RegionChrome, FallbackRow: i}
	}
	// A mismatch cannot safely invent semantic offsets. The ordinary bounded path
	// above is the contract; chrome fallback keeps line/provenance lockstep.
	_ = sections
	_ = card
	return rows
}

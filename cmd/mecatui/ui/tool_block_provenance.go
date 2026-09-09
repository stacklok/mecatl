package ui

import "strings"

// provenanceRows projects prepared tool-card sections through only their known
// layout frame. It derives durable structural spans while the prepared semantic
// text is live; callers subsequently recover text from the rendered frame lines,
// never by parsing decorated card chrome.
func (p preparedToolCard) provenanceRows(blockID uint64, indent, width int) []renderedRow {
	top := p.card.GetBorderTopSize() + p.card.GetPaddingTop()
	bottom := p.card.GetBorderBottomSize() + p.card.GetPaddingBottom()
	rows := make([]renderedRow, 0, top+bottom+len(p.sections))
	for i := 0; i < top; i++ {
		rows = append(rows, renderedRow{blockID: blockID, region: conversationRegionChrome, row: i})
	}
	// At tiny widths renderBlock deliberately omits the normal conversation
	// indent for the frameless fallback.
	if width <= indent {
		indent = 0
	}
	leading := indent + p.card.GetBorderLeftSize() + p.card.GetPaddingLeft()
	offsets := map[regionKind]int{}
	for section, prepared := range p.sections {
		if prepared.text == "" {
			continue
		}
		layoutRows := p.layoutRows(section)
		semanticRows := p.semanticRows(section)
		for i, text := range layoutRows {
			// A pathological styled row can still be reflowed by lipgloss at
			// decoration time. Its rendered span is the safe fallback; ordinary
			// rows retain the source's trailing semantic spaces.
			semantic := strings.TrimRight(text, " ")
			if i < len(semanticRows) && strings.TrimRight(semanticRows[i], " ") == semantic {
				semantic = semanticRows[i]
			}
			region := prepared.region
			row := renderedRow{blockID: blockID, region: region, row: len(rows)}
			if region != conversationRegionChrome {
				row.text = true
				row.sourceOffset = offsets[region]
				row.leading = leading
				row.span = graphemeCount(semantic)
				offsets[region] += row.span
			}
			rows = append(rows, row)
		}
	}
	for i := 0; i < bottom; i++ {
		rows = append(rows, renderedRow{blockID: blockID, region: conversationRegionChrome, row: len(rows)})
	}
	return rows
}

package ui

// provenanceRows projects prepared tool-card sections through only their known
// layout frame. It deliberately never examines the decorated card text: borders
// and padding are presentation, while section and wrapped-row references identify
// canonical content in the shared prepared card.
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
	for section, prepared := range p.sections {
		if prepared.text == "" {
			continue
		}
		for sectionRow := range p.sectionRows(section) {
			region := prepared.region
			row := renderedRow{blockID: blockID, region: region, row: len(rows)}
			if region != conversationRegionChrome {
				row.text = true
				row.section = section
				row.sectionRow = sectionRow
				row.leading = leading
			}
			rows = append(rows, row)
		}
	}
	for i := 0; i < bottom; i++ {
		rows = append(rows, renderedRow{blockID: blockID, region: conversationRegionChrome, row: len(rows)})
	}
	return rows
}

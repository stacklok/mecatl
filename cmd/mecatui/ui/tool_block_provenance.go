package ui

import "github.com/stacklok/mecatl/cmd/mecatui/ui/internal/cards"

func functionalToolProvenanceRows(prepared cards.Prepared, blockID uint64, indent, width int) []renderedRow {
	if width <= indent {
		indent = 0
	}
	rows := make([]renderedRow, len(prepared.Rows))
	for i, source := range prepared.Rows {
		region := conversationRegionChrome
		switch source.Region {
		case cards.RegionArguments:
			region = conversationRegionArguments
		case cards.RegionResult:
			region = conversationRegionResult
		}
		rows[i] = renderedRow{
			blockID:      blockID,
			region:       region,
			sourceOffset: source.SourceOffset,
			row:          source.FallbackRow,
			text:         source.Text,
			leading:      indent + source.LeadingColumn,
			span:         source.GraphemeSpan,
		}
	}
	return rows
}

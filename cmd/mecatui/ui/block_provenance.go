package ui

import "github.com/stacklok/mecatl/cmd/mecatui/ui/internal/blocks"

func blockProvenanceRows(prepared blocks.Prepared, blockID uint64, kind blockKind, indent, width int) []renderedRow {
	if width <= indent {
		indent = 0
	}
	rows := make([]renderedRow, len(prepared.Rows))
	for i, source := range prepared.Rows {
		region := conversationRegionChrome
		switch source.Region {
		case blocks.RegionBody:
			region = conversationRegionBody
		case blocks.RegionArguments:
			region = conversationRegionArguments
		case blocks.RegionResult:
			region = conversationRegionResult
		}
		rows[i] = renderedRow{
			blockID:      blockID,
			region:       region,
			sourceOffset: source.SourceOffset,
			row:          source.FallbackRow,
			text:         source.Text,
			kind:         kind,
			indent:       indent,
			leading:      indent + source.LeadingColumn,
			span:         source.GraphemeSpan,
		}
	}
	return rows
}

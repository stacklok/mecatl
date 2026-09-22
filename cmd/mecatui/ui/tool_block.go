package ui

import (
	"strings"

	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/blocks"
)

// preparedToolCard remains a package-local ephemeral adapter for regression
// checks that ensure prepared semantic content is never retained by the cache.
type preparedToolCard struct {
	blocks.Prepared
}

type preparedToolSection = blocks.StyledSectionInput

func (p preparedToolCard) render() string {
	return strings.Join(p.Lines, "\n")
}

// prepareToolCard snapshots the mutable conversation block into the real
// stateless blocks package. Dynamic rows are already bounded to bodyWidth before
// their styles are applied; the blocks package owns only final decoration and
// lockstep structural provenance.
func (r *renderer) prepareToolCard(b *block, expand bool) preparedToolCard {
	r.toolCardPrepares++
	_, _, bodyWidth := r.toolCardLayout()
	theme := r.blockTheme()

	var glyph, glyphText string
	switch {
	case !b.resolved:
		glyphText = "…"
		glyph = r.th.Style("toolName").Render(glyphText)
	case b.resultError:
		glyphText = "✗"
		glyph = r.th.Style("toolErr").Render(glyphText)
	default:
		glyphText = "✓"
		glyph = r.th.Style("toolOk").Render(glyphText)
	}

	mcpName, isMCP := mcpTitle(b.toolName)
	headLabel := terminaltext.Sanitize(b.toolName)
	if isMCP {
		headLabel = mcpName
	}
	head := renderToolHeader(glyph, glyphText, headLabel, r.th.Style("toolName"), bodyWidth)
	if isMCP && expand {
		head += "\n" + renderToolCardText(r.th.Style("muted"), terminaltext.Sanitize(b.toolName), bodyWidth)
	}

	sections := []preparedToolSection{{Region: blocks.RegionChrome, Text: head}}
	if args := r.renderToolArgs(b, expand, bodyWidth); args != "" {
		sections = append(sections, preparedToolSection{Region: blocks.RegionArguments, Text: args})
	}
	if b.resolved {
		if result := r.renderToolResult(b, expand, bodyWidth); result != "" {
			sections = append(sections, preparedToolSection{
				Region:   blocks.RegionResult,
				Text:     result,
				Trailing: len(b.resultBody) - len(strings.TrimRight(b.resultBody, " ")),
			})
		}
	}

	input := blocks.SnapshotStyledTool(blocks.StyledToolInput{Sections: sections})
	return preparedToolCard{Prepared: blocks.PrepareStyledTool(input, theme)}
}

func (r *renderer) renderTool(b *block, expand bool) string {
	return r.prepareToolCard(b, expand).render()
}

package ui

import (
	"strings"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/blocks"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

// toolCardPresentation is the renderer-owned immutable presentation input for an
// ordinary typed tool snapshot. It deliberately does not enter ui.block, whose
// remaining tool shape exists only for legacy delegation presentation helpers.
type toolCardPresentation struct {
	name      string
	arguments string
	resolved  bool
	result    string
	isError   bool
	artifacts []client.ContentBlock
}

func toolCardPresentationFromSnapshot(p scrollback.ToolCardSnapshot) toolCardPresentation {
	return toolCardPresentation{
		name: p.Call.Name, arguments: p.Call.Arguments, resolved: p.Resolved,
		result: p.Result.Body, isError: p.Result.IsError,
		artifacts: contentBlocks(p.Result.Artifacts),
	}
}

// renderToolSnapshot is the ordinary-tool renderer boundary. It adapts the sealed
// logical snapshot directly into immutable presentation input and keeps cache and
// frame provenance renderer-owned.
func (r *renderer) renderToolSnapshot(idx int, s scrollback.BlockSnapshot, p scrollback.ToolCardSnapshot, expand bool) string {
	return r.renderCachedSnapshot(idx, uint64(s.ID), rendererRevision(s.Revision), expand, func(blockID uint64) blockRenderOutput {
		presentation := toolCardPresentationFromSnapshot(p)
		prepared := r.prepareTypedToolCard(presentation, expand)
		out := prepared.Text()
		if r.width > r.indent {
			out = r.indentLines(out)
		}
		return blockRenderOutput{
			text: out,
			rows: blockProvenanceRows(prepared.Prepared, blockID, scrollback.KindTool, r.indent, r.width),
		}
	})
}

func (r *renderer) prepareTypedToolCard(p toolCardPresentation, expand bool) preparedToolCard {
	r.cardPrepares++
	r.toolCardPrepares++
	_, _, bodyWidth := r.toolCardLayout()
	theme := r.blockTheme()

	var glyph, glyphText string
	switch {
	case !p.resolved:
		glyphText, glyph = "…", r.th.Style("toolName").Render("…")
	case p.isError:
		glyphText, glyph = "✗", r.th.Style("toolErr").Render("✗")
	default:
		glyphText, glyph = "✓", r.th.Style("toolOk").Render("✓")
	}
	mcpName, isMCP := mcpTitle(p.name)
	headLabel := terminaltext.Sanitize(p.name)
	if isMCP {
		headLabel = mcpName
	}
	head := renderToolHeader(glyph, glyphText, headLabel, r.th.Style("toolName"), bodyWidth)
	if isMCP && expand {
		head += "\n" + renderToolCardText(r.th.Style("muted"), terminaltext.Sanitize(p.name), bodyWidth)
	}
	sections := []preparedToolSection{{Region: blocks.RegionChrome, Text: head}}
	if args := r.renderOrdinaryToolArgs(p.name, p.arguments, expand, bodyWidth); args != "" {
		sections = append(sections, preparedToolSection{Region: blocks.RegionArguments, Text: args})
	}
	if p.resolved {
		if result := r.renderTypedToolResult(p.result, p.isError, p.artifacts, expand, bodyWidth); result != "" {
			sections = append(sections, preparedToolSection{Region: blocks.RegionResult, Text: result, Trailing: len(p.result) - len(strings.TrimRight(p.result, " "))})
		}
	}
	return preparedToolCard{Prepared: blocks.PrepareStyledTool(blocks.SnapshotStyledTool(blocks.StyledToolInput{Sections: sections}), theme)}
}

func (r *renderer) renderTypedToolResult(body string, isError bool, artifacts []client.ContentBlock, expand bool, bodyWidth int) string {
	lines, hiddenSummaryFields := r.renderTypedToolResultLines(body, isError, expand)
	for _, artifact := range artifacts {
		if line, ok := renderResultBlockLine(artifact); ok {
			lines = append(lines, toolResultLine{text: line, style: resultLineArtifact})
		}
	}
	if expand {
		lines = wrapResultDisplayLines(lines, bodyWidth)
	} else {
		lines = r.truncateResultDisplayLines(lines, bodyWidth, hiddenSummaryFields)
	}
	var out strings.Builder
	for i, line := range lines {
		if i > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(r.renderToolResultLine(line))
	}
	return out.String()
}

func (*renderer) renderTypedToolResultLines(body string, isError, expand bool) ([]toolResultLine, int) {
	if !expand && !isError {
		if summary, hiddenFields, ok := summarizeResultDetail(body); ok {
			return resultLines(summary, resultLineSummary), hiddenFields
		}
	}
	body = terminaltext.Sanitize(strings.TrimRight(body, "\n"))
	if body == "" {
		return nil, 0
	}
	style := resultLineBody
	if isError {
		style = resultLineError
	}
	return resultLines(body, style), 0
}

func (r *renderer) renderOrdinaryToolArgs(name, arguments string, expand bool, bodyWidth int) string {
	var args string
	if diff, ok := r.renderToolDiffAtWidth(name, arguments, expand, bodyWidth); ok {
		return diff
	}
	if expand {
		if jsonArgs := prettyJSON(arguments); jsonArgs != "" {
			return renderToolCardText(r.th.Style("toolArgs"), jsonArgs, bodyWidth)
		}
	} else if summary, ok := r.summarizeArgs(arguments); ok {
		args = summary
	} else if jsonArgs := prettyJSON(arguments); jsonArgs != "" {
		args = r.th.Style("toolArgs").Render(jsonArgs)
	}
	return wrapToolCardRegion(args, bodyWidth)
}

// preparedToolCard remains a package-local ephemeral adapter for regression
// checks that ensure prepared semantic content is never retained by the cache.
type preparedToolCard struct {
	blocks.Prepared
}

type preparedToolSection = blocks.StyledSectionInput

func (p preparedToolCard) render() string {
	return p.Text()
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

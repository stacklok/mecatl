package ui

import (
	"fmt"
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

// renderSubagentSnapshot adapts only the sealed Subagent payload to its renderer
// presentation value. Cache storage and frame provenance remain renderer-owned.
func (r *renderer) renderSubagentSnapshot(idx int, s scrollback.BlockSnapshot, p scrollback.SubagentCardSnapshot, expand bool) string {
	return r.renderCachedSnapshot(idx, uint64(s.ID), rendererRevision(s.Revision), expand, func(blockID uint64) blockRenderOutput {
		r.cardPrepares++
		prepared := r.prepareSubagentCard(subagentCardPresentationFromSnapshot(p), expand).Prepared
		out := prepared.Text()
		if r.width > r.indent {
			out = r.indentLines(out)
		}
		return blockRenderOutput{text: out, rows: blockProvenanceRows(prepared, blockID, scrollback.KindSubagent, r.indent, r.width)}
	})
}

// renderTeamSnapshot adapts only the sealed Team payload to its renderer
// presentation value. The Agents overlay continues to use its existing ui.block path.
func (r *renderer) renderTeamSnapshot(idx int, s scrollback.BlockSnapshot, p scrollback.TeamCardSnapshot, expand bool) string {
	return r.renderCachedSnapshot(idx, uint64(s.ID), rendererRevision(s.Revision), expand, func(blockID uint64) blockRenderOutput {
		r.cardPrepares++
		prepared := r.prepareTeamCard(teamCardPresentationFromSnapshot(p), expand).Prepared
		out := prepared.Text()
		if r.width > r.indent {
			out = r.indentLines(out)
		}
		return blockRenderOutput{text: out, rows: blockProvenanceRows(prepared, blockID, scrollback.KindTeam, r.indent, r.width)}
	})
}

func (r *renderer) prepareSubagentCard(p subagentCardPresentation, expand bool) preparedToolCard {
	_, _, bodyWidth := r.toolCardLayout()
	return r.prepareDelegationCard(p.name, p.resolved, p.result, p.isError, p.artifacts, r.renderSubagentPresentation(p, expand, bodyWidth), expand)
}

func (r *renderer) prepareTeamCard(p teamCardPresentation, expand bool) preparedToolCard {
	_, _, bodyWidth := r.toolCardLayout()
	return r.prepareDelegationCard(p.name, p.resolved, p.result, p.isError, p.artifacts, r.renderTeamPresentation(p, expand, bodyWidth), expand)
}

func (r *renderer) prepareDelegationCard(name string, resolved bool, result string, isError bool, artifacts []client.ContentBlock, args string, expand bool) preparedToolCard {
	r.toolCardPrepares++
	_, _, bodyWidth := r.toolCardLayout()
	var glyph, glyphText string
	switch {
	case !resolved:
		glyphText, glyph = "…", r.th.Style("toolName").Render("…")
	case isError:
		glyphText, glyph = "✗", r.th.Style("toolErr").Render("✗")
	default:
		glyphText, glyph = "✓", r.th.Style("toolOk").Render("✓")
	}
	mcpName, isMCP := mcpTitle(name)
	headLabel := terminaltext.Sanitize(name)
	if isMCP {
		headLabel = mcpName
	}
	head := renderToolHeader(glyph, glyphText, headLabel, r.th.Style("toolName"), bodyWidth)
	if isMCP && expand {
		head += "\n" + renderToolCardText(r.th.Style("muted"), terminaltext.Sanitize(name), bodyWidth)
	}
	sections := []preparedToolSection{{Region: blocks.RegionChrome, Text: head}}
	if args != "" {
		sections = append(sections, preparedToolSection{Region: blocks.RegionArguments, Text: args})
	}
	if resolved {
		if rendered := r.renderDelegationResult(result, isError, artifacts, expand, bodyWidth); rendered != "" {
			sections = append(sections, preparedToolSection{Region: blocks.RegionResult, Text: rendered, Trailing: len(result) - len(strings.TrimRight(result, " "))})
		}
	}
	return preparedToolCard{Prepared: blocks.PrepareStyledTool(blocks.SnapshotStyledTool(blocks.StyledToolInput{Sections: sections}), r.blockTheme())}
}

func (r *renderer) renderDelegationResult(result string, isError bool, artifacts []client.ContentBlock, expand bool, bodyWidth int) string {
	lines, hiddenSummaryFields := r.renderTypedToolResultLines(result, isError, expand)
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

func (r *renderer) renderSubagentPresentation(p subagentCardPresentation, expand bool, bodyWidth int) string {
	muted := r.th.Style("muted")
	var out strings.Builder
	if p.goal != "" {
		out.WriteString(renderDelegationToolCardText(muted, "↳ "+terminaltext.Sanitize(p.goal), bodyWidth))
		out.WriteString("\n")
	}
	modelLabel := delegationModelLabel(p.routedCategory, p.routedModel, p.routingReason, p.model, p.routing)
	if expand && p.routing != nil {
		modelLabel = ""
	}
	if modelLabel != "" {
		out.WriteString(renderDelegationToolCardText(muted, modelLabel, bodyWidth))
		out.WriteString("\n")
	}
	if expand {
		if detail := routingDecisionDetail(p.routing, p.model, p.routingReason); detail != "" {
			out.WriteString(renderDelegationToolCardText(muted, detail, bodyWidth))
			out.WriteString("\n")
		}
	}
	if p.done {
		out.WriteString(renderDelegationToolCardText(muted, subagentPresentationResolvedLine(p), bodyWidth))
		return strings.TrimRight(out.String(), "\n")
	}
	if expand {
		out.WriteString(renderDelegationToolCardText(muted, "subagent · "+boundedPreviewsSubNote, bodyWidth))
		if trace := r.renderTraceAtWidth(p.trace, bodyWidth); trace != "" {
			out.WriteString("\n")
			out.WriteString(trace)
		}
		return strings.TrimRight(out.String(), "\n")
	}
	out.WriteString(renderDelegationToolCardText(muted, r.subagentPresentationLiveLine(p), bodyWidth))
	return strings.TrimRight(out.String(), "\n")
}

func (r *renderer) subagentPresentationLiveLine(p subagentCardPresentation) string {
	current := "…"
	if p.current != "" {
		current = terminaltext.Sanitize(p.current)
	}
	return fmt.Sprintf("subagent · %s · ↑%s ↓%s · %s · %s trace", current, humanizeTokens(p.usage.InputTokens), humanizeTokens(p.usage.OutputTokens), plural(p.toolCount, "tool"), r.marks.expandTools)
}

func subagentPresentationResolvedLine(p subagentCardPresentation) string {
	return fmt.Sprintf("subagent · %s · ↑%s ↓%s · %s · stop:%s", humanizeDuration(p.durationMS), humanizeTokens(p.usage.InputTokens), humanizeTokens(p.usage.OutputTokens), plural(p.toolCount, "tool"), subagentStopLabel(p.stop))
}

func (r *renderer) renderTeamPresentation(p teamCardPresentation, expand bool, bodyWidth int) string {
	muted := r.th.Style("muted")
	var out strings.Builder
	if p.done {
		out.WriteString(renderDelegationToolCardText(muted, teamPresentationResolvedLine(p), bodyWidth))
		if !expand {
			return out.String()
		}
	} else {
		out.WriteString(renderDelegationToolCardText(muted, r.teamPresentationHeader(p, expand), bodyWidth))
	}
	order := teamLaneOrder(p.lanes)
	shown := order
	if len(shown) > maxTeamLanes {
		shown = order[:maxTeamLanes]
	}
	nameW := teamNameWidth(p.lanes, shown)
	for n, idx := range shown {
		lane := &p.lanes[idx]
		if expand && n > 0 {
			out.WriteString("\n")
		}
		out.WriteString("\n")
		out.WriteString(renderDelegationToolCardText(muted, teamLaneLine(lane, nameW, p.done), bodyWidth))
		if expand {
			if detail := routingDecisionDetail(lane.routingDecision, lane.model, lane.routingReason); detail != "" {
				out.WriteString("\n")
				out.WriteString(renderDelegationToolCardText(muted, detail, bodyWidth))
			}
			if trace := r.renderTraceAtWidth(lane.trace, bodyWidth); trace != "" {
				out.WriteString("\n")
				out.WriteString(trace)
			}
		}
	}
	if extra := len(order) - len(shown); extra > 0 {
		out.WriteString("\n")
		out.WriteString(renderDelegationToolCardText(muted, fmt.Sprintf("  · +%d more · %s", extra, r.marks.agents), bodyWidth))
	}
	return out.String()
}

func (r *renderer) teamPresentationHeader(p teamCardPresentation, expand bool) string {
	verb := r.marks.expandTools + " trace"
	if expand {
		verb = r.marks.expandTools + " collapse"
	}
	return "team · " + plural(len(p.lanes), "member") + " · " + verb
}

func teamPresentationResolvedLine(p teamCardPresentation) string {
	line := fmt.Sprintf("team · %s · ↑%s ↓%s · stop:%s", plural(p.rounds, "round"), humanizeTokens(p.usage.InputTokens), humanizeTokens(p.usage.OutputTokens), subagentStopLabel(p.stop))
	stopped := 0
	for _, lane := range p.lanes {
		if lane.stopped {
			stopped++
		}
	}
	if stopped > 0 {
		line += fmt.Sprintf(" · %d stopped", stopped)
	}
	return line
}

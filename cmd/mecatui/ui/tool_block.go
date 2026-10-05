package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/renderfmt"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/blocks"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

const (
	toolEditName  = "Edit"
	toolWriteName = "Write"
	toolPathArg   = "path"
	toolURLArg    = "url"
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
// frame provenance renderer-owned. A settled call collapses to the same semantic
// one-line projection the /toolcalls inspector lists; complete arguments and
// results remain in the inspector.
func (r *renderer) renderToolSnapshot(idx int, s scrollback.BlockSnapshot, p scrollback.ToolCardSnapshot) string {
	return r.renderCachedSnapshot(idx, uint64(s.ID), rendererRevision(s.Revision), false, func(blockID uint64) blockRenderOutput {
		metadata, _ := scrollback.ToolCallMetadataOf(s)
		projection := projectToolCall(metadata)
		if projection.settled() {
			return r.renderSettledToolLine(blockID, projection)
		}
		presentation := toolCardPresentationFromSnapshot(p)
		prepared := r.prepareTypedToolCard(presentation, false, projection.state)
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

func (r *renderer) renderSettledToolLine(blockID uint64, projection toolcallProjection) blockRenderOutput {
	glyph, _, style := projection.state.status()
	line := r.th.Style(style).Render(glyph) + " " + projection.summary()
	indent := 0
	width := r.width
	if r.width > r.indent {
		indent = r.indent
		width = r.contentWidth()
	}
	line = ansi.Truncate(line, width, "…")
	if indent > 0 {
		line = strings.Repeat(" ", indent) + line
	}
	return blockRenderOutput{
		text: line,
		rows: []renderedRow{{
			blockID: blockID, region: conversationRegionBody, text: true,
			kind: scrollback.KindTool, indent: indent, leading: indent, span: graphemeCount(ansi.Strip(line)),
		}},
	}
}

func (r *renderer) prepareTypedToolCard(p toolCardPresentation, expand bool, state toolcallProjectionState) preparedToolCard {
	r.cardPrepares++
	r.toolCardPrepares++
	_, _, bodyWidth := r.toolCardLayout()
	theme := r.blockTheme()

	glyphText, status, style := state.status()
	glyph := r.th.Style(style).Render(glyphText)
	mcpName, isMCP := mcpTitle(p.name)
	headLabel := terminaltext.Sanitize(p.name)
	if isMCP {
		headLabel = mcpName
	}
	headLabel = status + " · " + headLabel
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
func (r *renderer) renderSubagentSnapshot(idx int, s scrollback.BlockSnapshot, p scrollback.SubagentCardSnapshot) string {
	return r.renderCachedSnapshot(idx, uint64(s.ID), rendererRevision(s.Revision), false, func(blockID uint64) blockRenderOutput {
		metadata, _ := scrollback.ToolCallMetadataOf(s)
		projection := projectToolCall(metadata)
		if projection.settled() {
			return r.renderSettledToolLine(blockID, projection)
		}
		r.cardPrepares++
		prepared := r.prepareSubagentCard(subagentCardPresentationFromSnapshot(p), false, projection.state).Prepared
		out := prepared.Text()
		if r.width > r.indent {
			out = r.indentLines(out)
		}
		return blockRenderOutput{text: out, rows: blockProvenanceRows(prepared, blockID, scrollback.KindSubagent, r.indent, r.width)}
	})
}

// renderTeamSnapshot adapts only the sealed Team payload to its renderer
// presentation value. The Agents overlay continues to use its existing ui.block path.
func (r *renderer) renderTeamSnapshot(idx int, s scrollback.BlockSnapshot, p scrollback.TeamCardSnapshot) string {
	return r.renderCachedSnapshot(idx, uint64(s.ID), rendererRevision(s.Revision), false, func(blockID uint64) blockRenderOutput {
		metadata, _ := scrollback.ToolCallMetadataOf(s)
		projection := projectToolCall(metadata)
		if projection.settled() {
			return r.renderSettledToolLine(blockID, projection)
		}
		r.cardPrepares++
		prepared := r.prepareTeamCard(teamCardPresentationFromSnapshot(p), false, projection.state).Prepared
		out := prepared.Text()
		if r.width > r.indent {
			out = r.indentLines(out)
		}
		return blockRenderOutput{text: out, rows: blockProvenanceRows(prepared, blockID, scrollback.KindTeam, r.indent, r.width)}
	})
}

func (r *renderer) prepareSubagentCard(p subagentCardPresentation, expand bool, state toolcallProjectionState) preparedToolCard {
	_, _, bodyWidth := r.toolCardLayout()
	return r.prepareDelegationCard(p.name, p.resolved, p.result, p.isError, p.artifacts, r.renderSubagentPresentation(p, expand, bodyWidth), expand, state)
}

func (r *renderer) prepareTeamCard(p teamCardPresentation, expand bool, state toolcallProjectionState) preparedToolCard {
	_, _, bodyWidth := r.toolCardLayout()
	return r.prepareDelegationCard(p.name, p.resolved, p.result, p.isError, p.artifacts, r.renderTeamPresentation(p, expand, bodyWidth), expand, state)
}

func (r *renderer) prepareDelegationCard(name string, resolved bool, result string, isError bool, artifacts []client.ContentBlock, args string, expand bool, state toolcallProjectionState) preparedToolCard {
	r.toolCardPrepares++
	_, _, bodyWidth := r.toolCardLayout()
	glyphText, status, style := state.status()
	glyph := r.th.Style(style).Render(glyphText)
	mcpName, isMCP := mcpTitle(name)
	headLabel := terminaltext.Sanitize(name)
	if isMCP {
		headLabel = mcpName
	}
	headLabel = status + " · " + headLabel
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
	return fmt.Sprintf("subagent · %s · ↑%s ↓%s · %s · %s trace", current, renderfmt.HumanizeTokens(p.usage.InputTokens), renderfmt.HumanizeTokens(p.usage.OutputTokens), plural(p.toolCount, "tool"), r.marks.toolcalls)
}

func subagentPresentationResolvedLine(p subagentCardPresentation) string {
	return fmt.Sprintf("subagent · %s · ↑%s ↓%s · %s · stop:%s", renderfmt.HumanizeDuration(p.durationMS), renderfmt.HumanizeTokens(p.usage.InputTokens), renderfmt.HumanizeTokens(p.usage.OutputTokens), plural(p.toolCount, "tool"), subagentStopLabel(p.stop))
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
	verb := r.marks.toolcalls + " trace"
	if expand {
		verb = r.marks.toolcalls + " collapse"
	}
	return "team · " + plural(len(p.lanes), "member") + " · " + verb
}

func teamPresentationResolvedLine(p teamCardPresentation) string {
	line := fmt.Sprintf("team · %s · ↑%s ↓%s · stop:%s", plural(p.rounds, "round"), renderfmt.HumanizeTokens(p.usage.InputTokens), renderfmt.HumanizeTokens(p.usage.OutputTokens), subagentStopLabel(p.stop))
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

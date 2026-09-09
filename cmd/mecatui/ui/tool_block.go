package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// preparedToolCard is the tool card's semantic body before lipgloss adds its
// border and padding. Sections retain semantic text so provenance and selection
// never need to recover content by parsing rendered card chrome.
type preparedToolCard struct {
	card     lipgloss.Style
	sections []preparedToolSection
}

type preparedToolSection struct {
	region regionKind
	text   string
}

func (p preparedToolCard) render() string {
	parts := make([]string, 0, len(p.sections))
	for _, section := range p.sections {
		if section.text != "" {
			parts = append(parts, section.text)
		}
	}
	return p.card.Render(strings.Join(parts, "\n"))
}

// sectionRows resolves one prepared section's wrapped canonical rows on demand.
// Callers retain only the section/row coordinates, never these strings.
func (p preparedToolCard) sectionRows(section int) []string {
	if section < 0 || section >= len(p.sections) || p.sections[section].text == "" {
		return nil
	}
	bodyWidth := p.card.GetWidth() - p.card.GetHorizontalFrameSize()
	layout := lipgloss.NewStyle()
	if bodyWidth > 0 {
		layout = layout.Width(bodyWidth)
	}
	lines := strings.Split(layout.Render(p.sections[section].text), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(ansi.Strip(lines[i]), " ")
	}
	return lines
}

// prepareToolCard builds every semantic section at the card body width before
// final decoration. It is shared by rendering and frame provenance.
func (r *renderer) prepareToolCard(b *block, expand bool) preparedToolCard {
	r.toolCardPrepares++
	card, _, bodyWidth := r.toolCardLayout()

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
	headLabel := sanitizeTerminal(b.toolName)
	if isMCP {
		headLabel = mcpName
	}
	head := renderToolHeader(glyph, glyphText, headLabel, r.th.Style("toolName"), bodyWidth)
	if isMCP && expand {
		head += "\n" + renderToolCardText(r.th.Style("muted"), sanitizeTerminal(b.toolName), bodyWidth)
	}

	sections := []preparedToolSection{{region: conversationRegionChrome, text: head}}
	if args := r.renderToolArgs(b, expand, bodyWidth); args != "" {
		sections = append(sections, preparedToolSection{region: conversationRegionArguments, text: args})
	}
	if b.resolved {
		if result := r.renderToolResult(b, expand, bodyWidth); result != "" {
			sections = append(sections, preparedToolSection{region: conversationRegionResult, text: result})
		}
	}
	return preparedToolCard{card: card, sections: sections}
}

// renderTool renders the prepared semantic card only after all sections have
// been independently wrapped to the card body width.
func (r *renderer) renderTool(b *block, expand bool) string {
	return r.prepareToolCard(b, expand).render()
}

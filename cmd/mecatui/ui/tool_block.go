package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// preparedToolCard is the tool card's semantic body before lipgloss adds its
// border and padding. It exists only during a fresh block render; its semantic
// sections are converted to structural provenance before the cache entry is kept.
type preparedToolCard struct {
	card     lipgloss.Style
	sections []preparedToolSection
}

type preparedToolSection struct {
	region   regionKind
	text     string
	trailing int
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

// layoutRows returns the decorated card body's padded rows solely to mirror
// lipgloss's final reflow; it is never retained after structural spans are built.
func (p preparedToolCard) layoutRows(section int) []string {
	if section < 0 || section >= len(p.sections) || p.sections[section].text == "" {
		return nil
	}
	bodyWidth := p.card.GetWidth() - p.card.GetHorizontalFrameSize()
	layout := lipgloss.NewStyle()
	if bodyWidth > 0 {
		layout = layout.Width(bodyWidth)
	}
	rows := strings.Split(layout.Render(p.sections[section].text), "\n")
	for i := range rows {
		rows[i] = ansi.Strip(rows[i])
	}
	return rows
}

// semanticRows wraps the ANSI-free section with the card's body rules without
// layout padding. It preserves trailing semantic spaces, unlike the padded layout
// rows used solely to account for decoration reflow.
func (p preparedToolCard) semanticRows(section int) []string {
	if section < 0 || section >= len(p.sections) || p.sections[section].text == "" {
		return nil
	}
	bodyWidth := p.card.GetWidth() - p.card.GetHorizontalFrameSize()
	text := ansi.Strip(p.sections[section].text)
	rows := strings.Split(wrapToolCardText(text, bodyWidth), "\n")
	// ansi.Hardwrap normalizes trailing spaces. They are semantic source text,
	// not card padding, so restore the final source-line suffix explicitly.
	if sourceLines := strings.Split(text, "\n"); len(sourceLines) > 0 {
		last := sourceLines[len(sourceLines)-1]
		trailing := last[len(strings.TrimRight(last, " ")):]
		if n := p.sections[section].trailing; n > len(trailing) {
			trailing = strings.Repeat(" ", n)
		}
		if trailing != "" && len(rows) > 0 && !strings.HasSuffix(rows[len(rows)-1], trailing) {
			rows[len(rows)-1] += trailing
		}
	}
	return rows
}

// prepareToolCard builds every semantic section at the card body width before
// final decoration. Its caller immediately renders the card and derives the
// structural provenance needed after these strings are discarded.
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
			sections = append(sections, preparedToolSection{
				region: conversationRegionResult, text: result,
				trailing: len(b.resultBody) - len(strings.TrimRight(b.resultBody, " ")),
			})
		}
	}
	return preparedToolCard{card: card, sections: sections}
}

// renderTool renders the prepared semantic card only after all sections have
// been independently wrapped to the card body width.
func (r *renderer) renderTool(b *block, expand bool) string {
	return r.prepareToolCard(b, expand).render()
}

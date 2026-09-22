package cards

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// StyledSectionInput is one already width-bounded semantic section of a real
// tool card. Text may contain ANSI styling, but all dynamic text has been
// wrapped before that styling was applied.
type StyledSectionInput struct {
	Region   Region
	Text     string
	Trailing int
}

// StyledToolInput is the caller-owned input for the main-conversation tool card.
type StyledToolInput struct {
	Sections []StyledSectionInput
}

// StyledToolSnapshot owns the strings used by a tool-card preparation.
type StyledToolSnapshot struct {
	sections []StyledSectionInput
}

// SnapshotStyledTool detaches preparation from the mutable conversation block.
func SnapshotStyledTool(input StyledToolInput) StyledToolSnapshot {
	sections := make([]StyledSectionInput, len(input.Sections))
	for i, section := range input.Sections {
		sections[i] = section
		sections[i].Text = strings.Clone(section.Text)
	}
	return StyledToolSnapshot{sections: sections}
}

// StyledLayoutInput contains the concrete card style and every non-content fact
// observed by the real tool-card compiler.
type StyledLayoutInput struct {
	Card lipgloss.Style
}

// StyledLayout is an immutable copy of StyledLayoutInput.
type StyledLayout struct {
	card lipgloss.Style
}

// SnapshotStyledLayout copies the concrete card appearance. lipgloss.Style is a
// value whose mutators return a new value.
func SnapshotStyledLayout(input StyledLayoutInput) StyledLayout {
	return StyledLayout{card: input.Card}
}

// PrepareStyledTool produces the actual main-conversation card lines and their
// structural provenance in one stateless operation.
func PrepareStyledTool(input StyledToolSnapshot, layout StyledLayout) Prepared {
	parts := make([]string, 0, len(input.sections))
	for _, section := range input.sections {
		if section.Text != "" {
			parts = append(parts, section.Text)
		}
	}
	lines := strings.Split(layout.card.Render(strings.Join(parts, "\n")), "\n")
	if layout.card.GetWidth() > 0 && layout.card.GetHorizontalFrameSize() == 0 {
		for i, line := range lines {
			if ansi.StringWidth(line) > layout.card.GetWidth() {
				lines[i] = ansi.Truncate(line, layout.card.GetWidth(), "")
			}
		}
	}
	rows := styledRows(input.sections, layout.card)
	if len(rows) != len(lines) {
		// The semantic sections are width-bounded before decoration, so this is a
		// fail-safe for an unexpected lipgloss layout rule rather than a reflow path.
		rows = fallbackStyledRows(input.sections, lines, layout.card)
	}
	return Prepared{Lines: lines, Rows: rows}
}

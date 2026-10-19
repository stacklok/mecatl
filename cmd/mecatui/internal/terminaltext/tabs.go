package terminaltext

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// DefaultTabWidth is the fixed spaces-per-tab policy for plain-text display
// paths without a style. It matches Lip Gloss's default tab width.
const DefaultTabWidth = 4

// ExpandTabs replaces each tab with width spaces before display measurement or
// wrapping. Zero removes tabs; a negative width preserves them. This matches
// Lip Gloss's fixed-width conversion, not column-dependent terminal tab stops.
// It does not sanitize text or interpret Markdown.
func ExpandTabs(text string, width int) string {
	if width < 0 || !strings.ContainsRune(text, '\t') {
		return text
	}
	return strings.ReplaceAll(text, "\t", strings.Repeat(" ", width))
}

// ExpandStyleTabs expands tabs using the style's effective conversion policy
// before display measurement or wrapping. It leaves all other text unchanged.
func ExpandStyleTabs(text string, style lipgloss.Style) string {
	if !strings.ContainsRune(text, '\t') {
		return text
	}
	// GetTabWidth returns zero for an unset property in Lip Gloss v2.0.6,
	// while Render defaults to four. Inherit fills only the unset property,
	// preserving explicit zero (remove) and negative (no conversion) settings.
	width := style.Inherit(lipgloss.NewStyle().TabWidth(DefaultTabWidth)).GetTabWidth()
	return ExpandTabs(text, width)
}

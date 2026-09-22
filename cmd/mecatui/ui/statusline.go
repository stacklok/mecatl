package ui

import (
	"strings"

	"charm.land/lipgloss/v2"

	statusline "github.com/stacklok/mecatl/cmd/mecatui/statusline"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func statusSpansText(spans []statusline.Span) string {
	var b strings.Builder
	for _, span := range spans {
		b.WriteString(sanitizeTerminal(span.Text))
	}
	return b.String()
}

func statusSurfaceFits(surface statusline.Surface, width int) bool {
	return lipgloss.Width(statusSpansText(surface.Spans)) <= width
}

func renderStatusSurface(th theme.Theme, surface statusline.Surface, width int, rightAlign bool) string {
	line := renderStatusSpans(th, surface.Spans)
	if !rightAlign {
		return line
	}
	if gap := width - lipgloss.Width(line); gap > 0 {
		return strings.Repeat(" ", gap) + line
	}
	return line
}
func renderStatusSpans(th theme.Theme, spans []statusline.Span) string {
	var b strings.Builder
	for _, span := range spans {
		text := sanitizeTerminal(span.Text)
		if text == "" {
			continue
		}
		style := lipgloss.NewStyle()
		if span.Href != "" {
			b.WriteString(style.Foreground(lipgloss.Color(th.Palette.MdLink)).Underline(true).Render(text))
			continue
		}
		if color := statusColor(th, span.Token); color != "" {
			b.WriteString(style.Foreground(lipgloss.Color(color)).Render(text))
		} else {
			b.WriteString(text)
		}
	}
	return b.String()
}
func statusColor(th theme.Theme, token statusline.Token) string {
	p := th.Palette
	switch token {
	case statusline.TokenMuted:
		return p.TextMuted
	case statusline.TokenPrimary:
		return p.Primary
	case statusline.TokenSecondary:
		return p.Secondary
	case statusline.TokenAccent:
		return p.Accent
	case statusline.TokenSuccess:
		return p.Success
	case statusline.TokenWarning:
		return p.Warning
	case statusline.TokenError:
		return p.Error
	case statusline.TokenInfo:
		return p.Info
	default:
		return p.Text
	}
}

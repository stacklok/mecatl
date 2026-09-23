package ui

import (
	"strings"

	"charm.land/lipgloss/v2"

	customization "github.com/stacklok/mecatl/cmd/mecatui/customization"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func statusSpansText(spans []customization.Span) string {
	var b strings.Builder
	for _, span := range spans {
		b.WriteString(terminaltext.SanitizeSingleLine(span.Text))
	}
	return b.String()
}

func statusSurfaceFits(surface customization.Surface, width int) bool {
	return lipgloss.Width(statusSpansText(surface.Spans)) <= width
}

func renderStatusSurface(th theme.Theme, surface customization.Surface, width int, rightAlign bool) string {
	line := renderStatusSpans(th, surface.Spans)
	if !rightAlign {
		return line
	}
	if gap := width - lipgloss.Width(line); gap > 0 {
		return strings.Repeat(" ", gap) + line
	}
	return line
}
func renderStatusSpans(th theme.Theme, spans []customization.Span) string {
	var b strings.Builder
	for _, span := range spans {
		text := terminaltext.SanitizeSingleLine(span.Text)
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
func statusColor(th theme.Theme, token customization.Token) string {
	p := th.Palette
	switch token {
	case customization.TokenMuted:
		return p.TextMuted
	case customization.TokenPrimary:
		return p.Primary
	case customization.TokenSecondary:
		return p.Secondary
	case customization.TokenAccent:
		return p.Accent
	case customization.TokenSuccess:
		return p.Success
	case customization.TokenWarning:
		return p.Warning
	case customization.TokenError:
		return p.Error
	case customization.TokenInfo:
		return p.Info
	default:
		return p.Text
	}
}

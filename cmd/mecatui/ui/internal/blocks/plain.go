package blocks

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
)

// PlainLayoutInput is the caller-owned geometry used by non-Markdown cards.
type PlainLayoutInput struct {
	Width      int
	Expanded   bool
	ExpandMark string
}

// PlainLayout is an immutable geometry snapshot.
type PlainLayout struct {
	width      int
	expanded   bool
	expandMark string
}

// SnapshotPlainLayout detaches plain-card preparation from renderer state.
func SnapshotPlainLayout(in PlainLayoutInput) PlainLayout {
	return PlainLayout{width: in.Width, expanded: in.Expanded, expandMark: strings.Clone(in.ExpandMark)}
}

// SanitizePlain removes terminal controls from plain card text while preserving
// layout newlines and tabs. Markdown must not pass through this function.
func SanitizePlain(s string) string {
	return terminaltext.Sanitize(s)
}

func wrapStyled(text string, style lipgloss.Style, width int) string {
	frame := style.GetHorizontalFrameSize()
	if width > frame+1 {
		text = ansi.Wrap(terminaltext.NormalizeWidth(text), width-frame, "")
	}
	return style.Render(text)
}

func wrapPrefixed(prefix, body string, style lipgloss.Style, width int) string {
	pw := lipgloss.Width(prefix)
	if width <= pw+1 {
		return style.Render(prefix + body)
	}
	rows := strings.Split(ansi.Wrap(terminaltext.NormalizeWidth(body), width-pw, ""), "\n")
	for i := range rows {
		if i == 0 {
			rows[i] = prefix + rows[i]
		} else {
			rows[i] = strings.Repeat(" ", pw) + rows[i]
		}
	}
	return style.Render(strings.Join(rows, "\n"))
}

func preparePlain(chunks []string, firstTextRow int) Prepared {
	lines := strings.Split(strings.Join(chunks, "\n"), "\n")
	rows := make([]Row, len(lines))
	offset := 0
	for i, line := range lines {
		rows[i] = Row{Region: RegionChrome, FallbackRow: i}
		if i < firstTextRow {
			continue
		}
		span := graphemeCount(ansi.Strip(line))
		rows[i] = Row{
			Region: RegionBody, Text: true, SourceOffset: offset,
			FallbackRow: i, GraphemeSpan: span,
		}
		offset += span
	}
	return Prepared{Lines: lines, Rows: rows}
}

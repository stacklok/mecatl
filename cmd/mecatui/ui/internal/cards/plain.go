package cards

import (
	"crypto/sha256"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// PlainLayoutInput is the caller-owned geometry used by non-Markdown cards.
type PlainLayoutInput struct {
	Width      int
	Expanded   bool
	ExpandMark string
	Dialect    uint32
}

// PlainLayout is an immutable geometry snapshot.
type PlainLayout struct {
	width      int
	expanded   bool
	expandMark string
	dialect    uint32
}

// SnapshotPlainLayout detaches plain-card preparation from renderer state.
func SnapshotPlainLayout(in PlainLayoutInput) PlainLayout {
	return PlainLayout{width: in.Width, expanded: in.Expanded, expandMark: strings.Clone(in.ExpandMark), dialect: in.Dialect}
}

// SanitizePlain removes terminal controls from plain card text while preserving
// layout newlines and tabs. Markdown must not pass through this function.
func SanitizePlain(s string) string {
	if !strings.ContainsFunc(s, plainControl) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if plainControl(r) {
			return -1
		}
		return r
	}, s)
}

func plainControl(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20 || r == 0x7f:
		return true
	case r >= 0x80 && r <= 0x9f:
		return true
	case unicode.Is(unicode.Cf, r):
		return true
	default:
		return false
	}
}

func wrapStyled(text string, style lipgloss.Style, width int) string {
	frame := style.GetHorizontalFrameSize()
	if width > frame+1 {
		text = ansi.Wrap(normalizePlainWidth(text), width-frame, "")
	}
	return style.Render(text)
}

func wrapPrefixed(prefix, body string, style lipgloss.Style, width int) string {
	pw := lipgloss.Width(prefix)
	if width <= pw+1 {
		return style.Render(prefix + body)
	}
	rows := strings.Split(ansi.Wrap(normalizePlainWidth(body), width-pw, ""), "\n")
	for i := range rows {
		if i == 0 {
			rows[i] = prefix + rows[i]
		} else {
			rows[i] = strings.Repeat(" ", pw) + rows[i]
		}
	}
	return style.Render(strings.Join(rows, "\n"))
}

func normalizePlainWidth(src string) string {
	if !strings.ContainsFunc(src, func(r rune) bool { return r > 0x7f }) {
		return src
	}
	var out strings.Builder
	out.Grow(len(src))
	for rest := src; rest != ""; {
		cluster, _ := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		rest = rest[len(cluster):]
		if ansi.StringWidthWc(cluster) == ansi.StringWidth(cluster) {
			out.WriteString(cluster)
			continue
		}
		stripped := strings.ReplaceAll(cluster, "\ufe0f", "")
		if ansi.StringWidthWc(stripped) == ansi.StringWidth(stripped) {
			out.WriteString(stripped)
			continue
		}
		first, _ := utf8.DecodeRuneInString(stripped)
		candidate := string(first)
		if ansi.StringWidthWc(candidate) == ansi.StringWidth(candidate) {
			out.WriteString(candidate)
		} else {
			out.WriteRune('�')
		}
	}
	return out.String()
}

func preparePlain(family string, layout PlainLayout, chunks, keyValues []string, firstTextRow int) Prepared {
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
	var encoded canonicalEncoder
	encoded.string("mecatui.cards.plain/" + family + "/v1")
	encoded.int(layout.width)
	encoded.bool(layout.expanded)
	encoded.string(layout.expandMark)
	encoded.uint32(layout.dialect)
	encoded.strings(keyValues)
	for _, chunk := range chunks {
		encoded.string(chunk)
	}
	return Prepared{Key: sha256.Sum256(encoded.bytes), Lines: lines, Rows: rows}
}

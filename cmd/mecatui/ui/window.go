package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

type boundedWidthPolicy uint8

const (
	boundedWrap boundedWidthPolicy = iota
	boundedClip
)

type boundedScrollMove uint8

const (
	boundedLineUp boundedScrollMove = iota
	boundedLineDown
	boundedPageUp
	boundedPageDown
	boundedTop
	boundedEnd
)

// boundedScrollItem is one logical selectable item. The control applies the
// appropriate prefix to every physical line, so a paged segment never loses
// its cursor marker.
type boundedScrollItem struct {
	text, prefix, cursorPrefix string
}

type boundedRenderedRow struct {
	text          string
	item, itemRow int
}

type boundedScrollView struct {
	rows         []boundedRenderedRow
	above, below int
}

// boundedScrollCursor owns one physical-line window. cursorMode selects logical
// item movement; without it offset is a physical browsing-line offset. All
// state is renderer-local and ephemeral.
type boundedScrollCursor struct {
	width, height int
	policy        boundedWidthPolicy
	items         []boundedScrollItem
	cursorMode    bool
	cursor        int
	itemOffset    int
	offset        int
}

func (s *boundedScrollCursor) setBounds(width, height int, policy boundedWidthPolicy) {
	s.width, s.height, s.policy = width, height, policy
	s.clampState()
}

func (s *boundedScrollCursor) setBrowsing(lines []string) {
	s.items = make([]boundedScrollItem, len(lines))
	for i, line := range lines {
		s.items[i].text = line
	}
	s.cursorMode = false
	s.itemOffset = 0
	s.clampState()
}

func (s *boundedScrollCursor) setCursorItems(items []boundedScrollItem, cursor int) {
	s.items = append(s.items[:0], items...)
	s.cursorMode = true
	s.cursor = clampBounded(cursor, len(items))
	s.itemOffset, s.offset = 0, 0
	s.clampState()
}

func (s *boundedScrollCursor) move(move boundedScrollMove) {
	if s.width <= 0 || s.height <= 0 {
		return
	}
	layout := s.layout()
	if s.cursorMode {
		s.moveCursor(move, layout)
	} else {
		s.moveBrowsing(move, len(layout.rows))
	}
	s.clampState()
}

func (s *boundedScrollCursor) moveBrowsing(move boundedScrollMove, total int) {
	switch move {
	case boundedLineUp:
		s.offset--
	case boundedLineDown:
		s.offset++
	case boundedPageUp:
		s.offset -= s.height
	case boundedPageDown:
		s.offset += s.height
	case boundedTop:
		s.offset = 0
	case boundedEnd:
		s.offset = maxScrollOffset(total, s.height)
	}
}

func (s *boundedScrollCursor) moveCursor(move boundedScrollMove, layout boundedRowLayout) {
	if len(s.items) == 0 {
		return
	}
	s.cursor = clampBounded(s.cursor, len(s.items))
	itemHeight := layout.ends[s.cursor] - layout.starts[s.cursor]
	switch move {
	case boundedLineUp:
		s.cursor--
		s.itemOffset = 0
	case boundedLineDown:
		s.cursor++
		s.itemOffset = 0
	case boundedTop:
		s.cursor, s.itemOffset = 0, 0
	case boundedEnd:
		s.cursor, s.itemOffset = len(s.items)-1, 0
	case boundedPageDown:
		if itemHeight > s.height && s.itemOffset+s.height < itemHeight {
			s.itemOffset += s.height
			return
		}
		window := s.cursorWindow(layout)
		for item, start := range layout.starts {
			if start >= window.end {
				s.cursor, s.itemOffset = item, 0
				return
			}
		}
		s.cursor, s.itemOffset = len(s.items)-1, 0
	case boundedPageUp:
		if itemHeight > s.height && s.itemOffset > 0 {
			s.itemOffset = max(0, s.itemOffset-s.height)
			return
		}
		window := s.cursorWindow(layout)
		target := max(0, window.start-s.height)
		candidate := 0
		for item := range layout.starts {
			if layout.starts[item] >= window.start {
				break
			}
			if layout.ends[item] > target {
				candidate = item
				break
			}
		}
		s.cursor = candidate
		candidateHeight := layout.ends[candidate] - layout.starts[candidate]
		s.itemOffset = lastBoundedPageOffset(candidateHeight, s.height)
	}
	s.cursor = clampBounded(s.cursor, len(s.items))
}

func (s *boundedScrollCursor) view() boundedScrollView {
	if s.width <= 0 || s.height <= 0 {
		return boundedScrollView{}
	}
	s.clampState()
	layout := s.layout()
	if len(layout.rows) == 0 {
		return boundedScrollView{}
	}
	start, end := 0, 0
	if s.cursorMode {
		window := s.cursorWindow(layout)
		start, end = window.start, window.end
	} else {
		window := renderedLineWindow(s.offset, len(layout.rows), s.height)
		start, end = window.start, window.end
	}
	return boundedScrollView{
		rows:  append([]boundedRenderedRow(nil), layout.rows[start:end]...),
		above: start,
		below: len(layout.rows) - end,
	}
}

type boundedRowLayout struct {
	rows         []boundedRenderedRow
	starts, ends []int
}

type boundedPhysicalWindow struct{ start, end int }

func (s *boundedScrollCursor) cursorWindow(layout boundedRowLayout) boundedPhysicalWindow {
	if len(layout.rows) == 0 || len(s.items) == 0 {
		return boundedPhysicalWindow{}
	}
	cursor := clampBounded(s.cursor, len(s.items))
	itemStart, itemEnd := layout.starts[cursor], layout.ends[cursor]
	if itemEnd-itemStart > s.height {
		start := itemStart + min(s.itemOffset, itemEnd-itemStart-1)
		return boundedPhysicalWindow{start: start, end: min(start+s.height, itemEnd)}
	}
	start := min(itemStart, max(0, len(layout.rows)-s.height))
	return boundedPhysicalWindow{start: start, end: min(start+s.height, len(layout.rows))}
}

func (s *boundedScrollCursor) layout() boundedRowLayout {
	layout := boundedRowLayout{
		starts: make([]int, len(s.items)),
		ends:   make([]int, len(s.items)),
	}
	for item, entry := range s.items {
		layout.starts[item] = len(layout.rows)
		prefix := entry.prefix
		if s.cursorMode && item == clampBounded(s.cursor, len(s.items)) {
			prefix = entry.cursorPrefix
		}
		prefixWidth := ansi.StringWidth(ansi.Strip(prefix))
		textWidth := max(0, s.width-prefixWidth)
		for _, sourceLine := range strings.Split(entry.text, "\n") {
			physical := boundedWidthLines(sourceLine, textWidth, s.policy)
			for _, line := range physical {
				text := prefix + line
				if ansi.StringWidth(ansi.Strip(text)) > s.width {
					text = ansi.Truncate(text, s.width, "")
				}
				layout.rows = append(layout.rows, boundedRenderedRow{
					text: text + "\x1b[0m", item: item, itemRow: len(layout.rows) - layout.starts[item],
				})
			}
		}
		layout.ends[item] = len(layout.rows)
	}
	return layout
}

func boundedWidthLines(line string, width int, policy boundedWidthPolicy) []string {
	if width <= 0 {
		return []string{""}
	}
	if policy == boundedClip {
		return []string{ansi.Cut(line, 0, width)}
	}
	wrapped := strings.Split(ansi.Hardwrap(line, width, true), "\n")
	lines := make([]string, 0, len(wrapped))
	left := 0
	for _, row := range wrapped {
		rowWidth := ansi.StringWidth(row)
		if rowWidth == 0 {
			lines = append(lines, row)
			continue
		}
		segment := ansi.Cut(line, left, left+rowWidth)
		left += rowWidth
		if ansi.StringWidth(segment) <= width {
			lines = append(lines, segment)
		}
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func lastBoundedPageOffset(itemHeight, pageHeight int) int {
	if itemHeight <= pageHeight || pageHeight <= 0 {
		return 0
	}
	return (itemHeight - 1) / pageHeight * pageHeight
}

func (s *boundedScrollCursor) clampState() {
	s.cursor = clampBounded(s.cursor, len(s.items))
	if s.width <= 0 || s.height <= 0 {
		s.offset, s.itemOffset = 0, 0
		return
	}
	layout := s.layout()
	if s.cursorMode && len(s.items) > 0 {
		itemHeight := layout.ends[s.cursor] - layout.starts[s.cursor]
		s.itemOffset = min(max(0, s.itemOffset), max(0, itemHeight-1))
	} else {
		s.offset = clampScroll(s.offset, len(layout.rows), s.height)
	}
}

func clampBounded(value, count int) int {
	if count <= 0 || value < 0 {
		return 0
	}
	return min(value, count-1)
}

// scrollWindow returns the [start,end) slice bounds of a scrolling window of size
// limit over n rows, kept around the selected cursor so it stays visible. It is a
// pure function of (cursor, n, limit) — the window FOLLOWS the cursor (no stored
// offset to drift), so the selected row stays in view when paging past the top or
// bottom edge. Shared by the slash palette (renderPalette), the @-mention menu
// (renderMention), and the /models picker (renderModelsPanel); lifted from
// palette.go (was paletteWindow) so the call sites can't diverge.
func scrollWindow(cursor, n, limit int) (start, end int) {
	if n <= limit {
		return 0, n
	}
	start = cursor - limit/2
	if start < 0 {
		start = 0
	}
	end = start + limit
	if end > n {
		end = n
		start = end - limit
	}
	return start, end
}

// maxScrollOffset is the largest valid scroll offset for total lines viewed
// through a fixed window: total minus the window, never negative. Shared by the
// offset-scrolled overlay panels (/soul, /skills, /agents inventory), which keep
// an explicit offset instead of a cursor (there is no selection to follow).
func maxScrollOffset(total, window int) int {
	if total <= window {
		return 0
	}
	return total - window
}

// clampScroll bounds want into [0, maxScrollOffset(total, window)] — the shared
// scroll clamp for the offset-scrolled overlay panels.
func clampScroll(want, total, window int) int {
	if want < 0 {
		return 0
	}
	if mx := maxScrollOffset(total, window); want > mx {
		return mx
	}
	return want
}

type renderedLineWindowBounds struct {
	start, end, total, window int
}

func renderedLineWindow(scroll, total, window int) renderedLineWindowBounds {
	if window < 0 {
		window = 0
	}
	start := clampScroll(scroll, total, window)
	return renderedLineWindowBounds{start: start, end: min(start+window, total), total: total, window: window}
}

// windowRenderedLines windows ALREADY-RENDERED (ANSI-carrying) lines to a fixed
// window starting at scroll, appending a muted "lines X–Y of N" indicator when
// the content overflows the window. Each input line must be a COMPLETE styled
// line (lipgloss renders multi-line strings with per-line SGR sequences — the
// same property capRenderedLines relies on), so slicing never severs an escape.
// Like capRenderedLines it must NOT sanitizeTerminal its input (that would strip
// the embedded styling); the line TEXT is sanitized by the callers at render
// time. Every emitted line carries a trailing newline so the callers' footer
// concatenation stays uniform across the scrolled and unscrolled cases.
func windowRenderedLines(th theme.Theme, lines []string, scroll, window int) string {
	return windowRenderedLinesWithIndicator(th, lines, scroll, window, func(start, end, total int) string {
		return fmt.Sprintf("lines %d–%d of %d", start+1, end, total)
	})
}

// windowRenderedLinesWithIndicator is windowRenderedLines with a caller-owned
// overflow indicator. It lets a clipped surface retain its live navigation
// affordances without consuming another row.
func windowRenderedLinesWithIndicator(th theme.Theme, lines []string, scroll, window int, indicator func(start, end, total int) string) string {
	w := renderedLineWindow(scroll, len(lines), window)

	var b strings.Builder
	for _, ln := range lines[w.start:w.end] {
		b.WriteString(ln + "\n")
	}
	if w.total > w.window {
		b.WriteString(th.Style("muted").Render(indicator(w.start, w.end, w.total)) + "\n")
	}
	return b.String()
}

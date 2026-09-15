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

type boundedMove uint8

const (
	boundedLineUp boundedMove = iota
	boundedLineDown
	boundedPageUp
	boundedPageDown
	boundedTop
	boundedEnd
)

type boundedViewport struct {
	width, height int
	gutter        int
	policy        boundedWidthPolicy
	offset        int
}

type boundedViewportView struct {
	rows         []string
	above, below int
}

func (v *boundedViewport) setGeometry(width, height, gutter int, policy boundedWidthPolicy) {
	v.width, v.height, v.gutter, v.policy = width, height, max(0, gutter), policy
	if !v.valid() {
		v.offset = 0
	}
}

func (v *boundedViewport) valid() bool {
	return v.width >= v.gutter+1 && v.height > 0
}

func (v *boundedViewport) contentWidth() int {
	if !v.valid() {
		return 0
	}
	return v.width - v.gutter
}

func (v *boundedViewport) layout(lines []string) []string {
	if !v.valid() {
		return nil
	}
	var rows []string
	for _, line := range lines {
		for _, sourceLine := range strings.Split(line, "\n") {
			rows = append(rows, boundedWidthLines(sourceLine, v.contentWidth(), v.policy)...)
		}
	}
	return rows
}

func (v *boundedViewport) window(total int) renderedLineWindowBounds {
	if !v.valid() {
		return renderedLineWindowBounds{}
	}
	v.offset = clampScroll(v.offset, total, v.height)
	return renderedLineWindow(v.offset, total, v.height)
}

func (v *boundedViewport) view(lines []string) boundedViewportView {
	rows := v.layout(lines)
	if len(rows) == 0 {
		v.offset = 0
		return boundedViewportView{}
	}
	window := v.window(len(rows))
	return boundedViewportView{
		rows:  append([]string(nil), rows[window.start:window.end]...),
		above: window.start,
		below: len(rows) - window.end,
	}
}

func (v *boundedViewport) move(move boundedMove, total int) {
	if !v.valid() {
		return
	}
	switch move {
	case boundedLineUp:
		v.offset--
	case boundedLineDown:
		v.offset++
	case boundedPageUp:
		v.offset -= v.height
	case boundedPageDown:
		v.offset += v.height
	case boundedTop:
		v.offset = 0
	case boundedEnd:
		v.offset = maxScrollOffset(total, v.height)
	}
	v.offset = clampScroll(v.offset, total, v.height)
}

type boundedListItem struct {
	id, text string
}

type boundedListRow struct {
	text                   string
	id                     string
	itemIndex, itemLine    int
	selected, cursorMarker bool
	gutter                 string
}

type boundedListView struct {
	rows         []boundedListRow
	above, below int
}

type boundedList struct {
	viewport boundedViewport
	items    []boundedListItem

	cursor     int
	cursorID   string
	cursorLine int
}

type boundedListLayout struct {
	rows         []boundedListRow
	starts, ends []int
}

func (l *boundedList) setGeometry(width, height, gutter int, policy boundedWidthPolicy) {
	l.viewport.setGeometry(width, height, gutter, policy)
	layout := l.layout()
	l.clamp(layout)
}

func (l *boundedList) setItems(items []boundedListItem) {
	oldLayout := l.layout()
	oldOffset, oldCursor := l.viewport.offset, l.cursor
	oldCursorID, oldCursorLine := l.cursorID, l.cursorLine
	topID, topLine, haveTop := "", 0, false
	if oldOffset >= 0 && oldOffset < len(oldLayout.rows) {
		topID, topLine, haveTop = oldLayout.rows[oldOffset].id, oldLayout.rows[oldOffset].itemLine, true
	}

	l.items = append(l.items[:0], items...)
	newLayout := l.layout()
	if len(l.items) == 0 {
		l.cursor, l.cursorID, l.cursorLine, l.viewport.offset = 0, "", 0, 0
		return
	}
	if index := l.itemIndex(oldCursorID); index >= 0 {
		l.cursor, l.cursorID = index, oldCursorID
		itemHeight := newLayout.ends[index] - newLayout.starts[index]
		l.cursorLine = min(max(0, oldCursorLine), max(0, itemHeight-1))
	} else {
		l.cursor = clampBounded(oldCursor, len(l.items))
		l.cursorID, l.cursorLine = l.items[l.cursor].id, 0
	}

	if haveTop {
		if index := l.itemIndex(topID); index >= 0 {
			itemHeight := newLayout.ends[index] - newLayout.starts[index]
			l.viewport.offset = newLayout.starts[index] + min(topLine, max(0, itemHeight-1))
		} else {
			l.viewport.offset = oldOffset
		}
	} else {
		l.viewport.offset = oldOffset
	}
	l.clamp(newLayout)
}

func (l *boundedList) setCursor(index int) {
	if len(l.items) == 0 {
		l.cursor, l.cursorID, l.cursorLine = 0, "", 0
		return
	}
	l.cursor = clampBounded(index, len(l.items))
	l.cursorID, l.cursorLine = l.items[l.cursor].id, 0
	layout := l.layout()
	l.revealCursor(layout)
}

func (l *boundedList) scroll(move boundedMove) {
	layout := l.layout()
	l.viewport.move(move, len(layout.rows))
}

func (l *boundedList) move(move boundedMove) {
	layout := l.layout()
	if len(l.items) == 0 || len(layout.rows) == 0 || !l.viewport.valid() {
		return
	}
	l.clamp(layout)
	itemHeight := layout.ends[l.cursor] - layout.starts[l.cursor]
	switch move {
	case boundedLineUp:
		l.setCursor(l.cursor - 1)
	case boundedLineDown:
		l.setCursor(l.cursor + 1)
	case boundedTop:
		l.setCursor(0)
	case boundedEnd:
		l.setCursor(len(l.items) - 1)
	case boundedPageDown:
		l.pageDown(layout, itemHeight)
	case boundedPageUp:
		l.pageUp(layout, itemHeight)
	}
	l.clamp(l.layout())
}

func (l *boundedList) pageDown(layout boundedListLayout, itemHeight int) {
	if itemHeight > l.viewport.height && l.cursorLine+l.viewport.height < itemHeight {
		l.cursorLine += l.viewport.height
		l.viewport.offset = layout.starts[l.cursor] + l.cursorLine
		return
	}
	window := l.viewport.window(len(layout.rows))
	for index, start := range layout.starts {
		if start >= window.end {
			l.cursor, l.cursorID, l.cursorLine = index, l.items[index].id, 0
			l.viewport.offset = start
			return
		}
	}
	l.setCursor(len(l.items) - 1)
}

func (l *boundedList) pageUp(layout boundedListLayout, itemHeight int) {
	if itemHeight > l.viewport.height && l.cursorLine > 0 {
		l.cursorLine = max(0, l.cursorLine-l.viewport.height)
		l.viewport.offset = layout.starts[l.cursor] + l.cursorLine
		return
	}
	window := l.viewport.window(len(layout.rows))
	target := max(0, window.start-l.viewport.height)
	candidate := 0
	for index := range layout.starts {
		if layout.starts[index] > target {
			break
		}
		candidate = index
		if target < layout.ends[index] {
			break
		}
	}
	l.cursor, l.cursorID = candidate, l.items[candidate].id
	candidateHeight := layout.ends[candidate] - layout.starts[candidate]
	if candidateHeight > l.viewport.height {
		if layout.ends[candidate] == window.start {
			l.cursorLine = lastBoundedPageOffset(candidateHeight, l.viewport.height)
		} else {
			l.cursorLine = (target - layout.starts[candidate]) / l.viewport.height * l.viewport.height
		}
		l.viewport.offset = layout.starts[candidate] + l.cursorLine
	} else {
		l.cursorLine = 0
		l.viewport.offset = target
	}
}

func (l *boundedList) view() boundedListView {
	layout := l.layout()
	if len(layout.rows) == 0 {
		l.viewport.offset = 0
		return boundedListView{}
	}
	l.clamp(layout)
	window := l.viewport.window(len(layout.rows))
	rows := append([]boundedListRow(nil), layout.rows[window.start:window.end]...)
	marked := false
	for index := range rows {
		rows[index].selected = rows[index].id == l.cursorID
		if rows[index].selected && !marked {
			rows[index].cursorMarker = true
			marked = true
		}
	}
	return boundedListView{rows: rows, above: window.start, below: len(layout.rows) - window.end}
}

func (l *boundedList) layout() boundedListLayout {
	layout := boundedListLayout{
		starts: make([]int, len(l.items)),
		ends:   make([]int, len(l.items)),
	}
	if !l.viewport.valid() {
		return layout
	}
	gutter := strings.Repeat(" ", l.viewport.gutter)
	for itemIndex, item := range l.items {
		layout.starts[itemIndex] = len(layout.rows)
		for _, sourceLine := range strings.Split(item.text, "\n") {
			for _, text := range boundedWidthLines(sourceLine, l.viewport.contentWidth(), l.viewport.policy) {
				layout.rows = append(layout.rows, boundedListRow{
					text: text, id: item.id, itemIndex: itemIndex,
					itemLine: len(layout.rows) - layout.starts[itemIndex], gutter: gutter,
				})
			}
		}
		layout.ends[itemIndex] = len(layout.rows)
	}
	return layout
}

func (l *boundedList) revealCursor(layout boundedListLayout) {
	if len(l.items) == 0 || !l.viewport.valid() {
		return
	}
	line := layout.starts[l.cursor] + l.cursorLine
	if line < l.viewport.offset {
		l.viewport.offset = line
	} else if line >= l.viewport.offset+l.viewport.height {
		l.viewport.offset = line - l.viewport.height + 1
	}
	l.viewport.offset = clampScroll(l.viewport.offset, len(layout.rows), l.viewport.height)
}

func (l *boundedList) clamp(layout boundedListLayout) {
	if len(l.items) == 0 || len(layout.rows) == 0 || !l.viewport.valid() {
		l.viewport.offset = 0
		return
	}
	l.cursor = clampBounded(l.cursor, len(l.items))
	if l.cursorID == "" || l.itemIndex(l.cursorID) < 0 {
		l.cursorID = l.items[l.cursor].id
	} else {
		l.cursor = l.itemIndex(l.cursorID)
	}
	itemHeight := layout.ends[l.cursor] - layout.starts[l.cursor]
	l.cursorLine = min(max(0, l.cursorLine), max(0, itemHeight-1))
	l.viewport.offset = clampScroll(l.viewport.offset, len(layout.rows), l.viewport.height)
}

func (l *boundedList) itemIndex(id string) int {
	for index := range l.items {
		if l.items[index].id == id {
			return index
		}
	}
	return -1
}

func boundedWidthLines(line string, width int, policy boundedWidthPolicy) []string {
	if width <= 0 {
		return nil
	}
	if policy == boundedClip {
		return []string{ansi.Cut(line, 0, width) + "\x1b[0m"}
	}
	wrapped := strings.Split(ansi.Hardwrap(line, width, true), "\n")
	lines := make([]string, 0, len(wrapped))
	left := 0
	for _, row := range wrapped {
		rowWidth := ansi.StringWidth(row)
		if rowWidth == 0 {
			lines = append(lines, row+"\x1b[0m")
			continue
		}
		segment := ansi.Cut(line, left, left+rowWidth)
		left += rowWidth
		if ansi.StringWidth(segment) <= width {
			lines = append(lines, segment+"\x1b[0m")
		}
	}
	if len(lines) == 0 {
		return []string{"\x1b[0m"}
	}
	return lines
}

func lastBoundedPageOffset(itemHeight, pageHeight int) int {
	if itemHeight <= pageHeight || pageHeight <= 0 {
		return 0
	}
	return (itemHeight - 1) / pageHeight * pageHeight
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
// Like capRenderedLines it must NOT terminaltext.Sanitize its input (that would strip
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

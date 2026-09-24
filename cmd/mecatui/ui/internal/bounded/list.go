package bounded

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// ListItem is an entry rendered by a List. StatusCells are optional one-display-cell
// markers for the two cells after the selection cell; invalid markers are omitted.
type ListItem struct {
	ID, Text    string
	StatusCells [2]string
}

// ListRow is one rendered line in a ListView. StatusCells and GutterCells are
// presentation metadata: the caller renders their cells. GutterCells is always
// in [1, 3], and StatusCells contain only valid one-display-cell markers.
type ListRow struct {
	Text, ID               string
	ItemIndex, ItemLine    int
	Selected, CursorMarker bool
	StatusCells            [2]string
	GutterCells            int
}

// ListView is the bounded portion of a list and its scroll indicators.
type ListView struct {
	Rows         []ListRow
	Above, Below int
}

// List manages selectable items in a bounded viewport.
type List struct {
	viewport   Viewport
	items      []ListItem
	cursor     int
	cursorID   string
	cursorLine int
	reveal     bool
}

type listLayout struct {
	rows         []ListRow
	starts, ends []int
}

type indicatorCandidate struct {
	start, height int
	above, below  int
	singletons    int
}

// SetGeometry configures the list viewport dimensions, gutter-cell count, and fitting
// policy. Every list has one selection cell and zero to two status cells; a trailing
// padding cell separates that gutter from content.
func (l *List) SetGeometry(width, height, gutterCells int, policy Policy) {
	l.viewport.SetGeometry(width, height, listGutterCells(gutterCells)+1, policy)
}

// Valid reports whether the list viewport has usable dimensions.
func (l *List) Valid() bool { return l.viewport.Valid() }

// Height returns the configured list viewport height.
func (l *List) Height() int { return l.viewport.Height() }

// Offset returns the list's current scroll offset.
func (l *List) Offset() int { return l.viewport.Offset() }

// Cursor returns the selected item index.
func (l *List) Cursor() int { return l.cursor }

// CursorID returns the selected item ID.
func (l *List) CursorID() string { return l.cursorID }

// RevealPending reports whether the cursor should be brought into view.
func (l *List) RevealPending() bool { return l.reveal }

// SetItems replaces the list contents while preserving compatible viewport anchors.
func (l *List) SetItems(items []ListItem) {
	if sameListItems(l.items, items) {
		return
	}
	old := l.layout()
	oldOffset, oldCursor, oldID, oldLine := l.viewport.offset, l.cursor, l.cursorID, l.cursorLine
	topID, topLine, haveTop := "", 0, false
	if oldOffset >= 0 && oldOffset < len(old.rows) {
		topID, topLine, haveTop = old.rows[oldOffset].ID, old.rows[oldOffset].ItemLine, true
	}
	l.items = append([]ListItem(nil), items...)
	for i := range l.items {
		for cell := range l.items[i].StatusCells {
			l.items[i].StatusCells[cell] = statusCell(l.items[i].StatusCells[cell])
		}
	}
	layout := l.layout()
	if len(l.items) == 0 {
		l.cursor, l.cursorID, l.cursorLine, l.viewport.offset = 0, "", 0, 0
		return
	}
	if i := l.itemIndex(oldID); i >= 0 {
		l.cursor, l.cursorID = i, oldID
		h := layout.ends[i] - layout.starts[i]
		l.cursorLine = min(max(0, oldLine), max(0, h-1))
	} else {
		l.cursor = clampBounded(oldCursor, len(l.items))
		l.cursorID, l.cursorLine = l.items[l.cursor].ID, 0
	}
	if haveTop {
		if i := l.itemIndex(topID); i >= 0 {
			h := layout.ends[i] - layout.starts[i]
			l.viewport.offset = layout.starts[i] + min(topLine, max(0, h-1))
		} else {
			l.viewport.offset = oldOffset
		}
	} else {
		l.viewport.offset = oldOffset
	}
	l.clamp(layout)
}

// SetCursor selects the item at index and reveals it.
func (l *List) SetCursor(index int) {
	if len(l.items) == 0 {
		l.cursor, l.cursorID, l.cursorLine = 0, "", 0
		return
	}
	l.cursor = clampBounded(index, len(l.items))
	l.cursorID, l.cursorLine = l.items[l.cursor].ID, 0
	l.reveal = true
	l.revealCursor(l.layout())
}

// Scroll moves the list viewport without changing the selection.
func (l *List) Scroll(move Move) {
	layout := l.layout()
	l.viewport.Move(move, len(layout.rows))
	l.reveal = false
}

// Move changes the selection or pages through the list.
func (l *List) Move(move Move) {
	layout := l.layout()
	if len(l.items) == 0 || len(layout.rows) == 0 || !l.viewport.Valid() {
		return
	}
	l.clamp(layout)
	l.reveal = true
	h := layout.ends[l.cursor] - layout.starts[l.cursor]
	switch move {
	case LineUp:
		l.SetCursor(l.cursor - 1)
	case LineDown:
		l.SetCursor(l.cursor + 1)
	case Top:
		l.SetCursor(0)
	case End:
		l.SetCursor(len(l.items) - 1)
	case PageDown:
		l.pageDown(layout, h)
	case PageUp:
		l.pageUp(layout, h)
	}
	l.clamp(l.layout())
}

func (l *List) pageDown(layout listLayout, h int) {
	if h > l.viewport.height && l.cursorLine+l.viewport.height < h {
		l.cursorLine += l.viewport.height
		l.viewport.offset = layout.starts[l.cursor] + l.cursorLine
		return
	}
	w := l.viewport.window(len(layout.rows))
	for i, start := range layout.starts {
		if start >= w.end {
			l.cursor, l.cursorID, l.cursorLine = i, l.items[i].ID, 0
			l.viewport.offset = start
			return
		}
	}
	l.SetCursor(len(l.items) - 1)
}

func (l *List) pageUp(layout listLayout, h int) {
	if h > l.viewport.height && l.cursorLine > 0 {
		l.cursorLine = max(0, l.cursorLine-l.viewport.height)
		l.viewport.offset = layout.starts[l.cursor] + l.cursorLine
		return
	}
	w := l.viewport.window(len(layout.rows))
	target := max(0, w.start-l.viewport.height)
	candidate := 0
	for i := range layout.starts {
		if layout.starts[i] > target {
			break
		}
		candidate = i
		if target < layout.ends[i] {
			break
		}
	}
	l.cursor, l.cursorID = candidate, l.items[candidate].ID
	ch := layout.ends[candidate] - layout.starts[candidate]
	if ch > l.viewport.height {
		if layout.ends[candidate] == w.start {
			l.cursorLine = lastPageOffset(ch, l.viewport.height)
		} else {
			l.cursorLine = (target - layout.starts[candidate]) / l.viewport.height * l.viewport.height
		}
		l.viewport.offset = layout.starts[candidate] + l.cursorLine
	} else {
		l.cursorLine = 0
		l.viewport.offset = target
	}
}

// View returns the visible list rows and the counts outside the viewport.
func (l *List) View() ListView {
	layout := l.layout()
	if len(layout.rows) == 0 {
		l.viewport.offset = 0
		return ListView{}
	}
	l.clamp(layout)
	w := l.viewport.window(len(layout.rows))
	rows := append([]ListRow(nil), layout.rows[w.start:w.end]...)
	marked := false
	for i := range rows {
		rows[i].Selected = rows[i].ID == l.cursorID
		if rows[i].Selected && !marked {
			rows[i].CursorMarker = true
			marked = true
		}
	}
	return ListView{Rows: rows, Above: w.start, Below: len(layout.rows) - w.end}
}

// ViewWithIndicators applies the shared logical-item indicator policy. Indicators
// report only complete items outside the physical window; partial oversized-item
// segments remain reachable through normal paging rather than becoming overflow.
func (l *List) ViewWithIndicators(capacity int, reveal bool) ListView {
	if capacity <= 0 {
		l.viewport.height = 0
		l.reveal = false
		return ListView{}
	}

	layout := l.layout()
	if len(layout.rows) == 0 {
		l.viewport.height, l.viewport.offset, l.reveal = capacity, 0, false
		return ListView{}
	}
	// Geometry and wrapping can change the physical row count between renders.
	// Preserve independent physical scrolling while constraining a stale offset to a
	// real row before choosing a non-revealing logical projection.
	l.viewport.offset = clampBounded(l.viewport.offset, len(layout.rows))
	previousOffset := l.viewport.offset
	selectedHeight := l.normalizeIndicatorCursor(layout)
	best, found := l.bestIndicatorCandidate(layout, capacity, previousOffset, reveal, selectedHeight)
	if !found {
		best = l.indicatorFallback(layout, capacity, previousOffset, reveal)
	}

	l.viewport.height, l.viewport.offset = best.height, best.start
	l.reveal = false
	return l.indicatorView(layout, best)
}

func (l *List) normalizeIndicatorCursor(layout listLayout) int {
	l.cursor = clampBounded(l.cursor, len(l.items))
	if l.cursorID == "" || l.itemIndex(l.cursorID) < 0 {
		l.cursorID = l.items[l.cursor].ID
	} else {
		l.cursor = l.itemIndex(l.cursorID)
	}
	selectedHeight := layout.ends[l.cursor] - layout.starts[l.cursor]
	l.cursorLine = min(max(0, l.cursorLine), max(0, selectedHeight-1))
	return selectedHeight
}

func (l *List) bestIndicatorCandidate(layout listLayout, capacity, previousOffset int, reveal bool, selectedHeight int) (indicatorCandidate, bool) {
	var best indicatorCandidate
	found := false
	for mask := 0; mask < 4; mask++ {
		chrome := indicatorChrome(mask)
		if chrome >= capacity {
			continue
		}
		height := capacity - chrome
		first, last := indicatorCandidateRange(len(layout.rows), height, previousOffset, reveal)
		for start := first; start < last; start++ {
			if !indicatorCandidateFits(len(layout.rows), start, height) {
				continue
			}
			end := min(len(layout.rows), start+height)
			above, below := hiddenCompleteItems(layout, start, end)
			if !indicatorMaskMatches(mask, above, below) || !l.indicatorReveals(layout, start, end, capacity, selectedHeight, reveal) {
				continue
			}
			candidate := indicatorCandidate{start: start, height: height, above: above, below: below}
			candidate.singletons = singletonIndicators(above, below)
			if !found || betterIndicatorCandidate(candidate, best, previousOffset, reveal) {
				best, found = candidate, true
			}
		}
	}
	return best, found
}

func indicatorChrome(mask int) int {
	return mask&1 + (mask&2)/2
}

func indicatorCandidateRange(rows, height, offset int, reveal bool) (int, int) {
	if reveal {
		return 0, max(1, rows-height+1)
	}
	return offset, offset + 1
}

func indicatorCandidateFits(rows, start, height int) bool {
	return start >= 0 && start < rows && (rows < height || start+height <= rows)
}

func indicatorMaskMatches(mask, above, below int) bool {
	return (mask&1 != 0) == (above > 1) && (mask&2 != 0) == (below > 1)
}

func (l *List) indicatorReveals(layout listLayout, start, end, capacity, selectedHeight int, reveal bool) bool {
	if !reveal {
		return true
	}
	selectedStart, selectedEnd := layout.starts[l.cursor], layout.ends[l.cursor]
	if selectedHeight <= capacity {
		return selectedStart >= start && selectedEnd <= end
	}
	cursorLine := selectedStart + l.cursorLine
	return cursorLine >= start && cursorLine < end
}

func singletonIndicators(above, below int) int {
	count := 0
	if above == 1 {
		count++
	}
	if below == 1 {
		count++
	}
	return count
}

func betterIndicatorCandidate(candidate, best indicatorCandidate, previousOffset int, reveal bool) bool {
	if !reveal || candidate.singletons != best.singletons {
		return reveal && candidate.singletons < best.singletons
	}
	candidateDistance, bestDistance := abs(candidate.start-previousOffset), abs(best.start-previousOffset)
	return candidateDistance < bestDistance || candidateDistance == bestDistance && candidate.start < best.start
}

func (l *List) indicatorFallback(layout listLayout, capacity, previousOffset int, reveal bool) indicatorCandidate {
	l.viewport.height, l.viewport.offset = capacity, previousOffset
	if reveal {
		l.revealCursor(layout)
	} else {
		l.viewport.offset = clampScroll(l.viewport.offset, len(layout.rows), l.viewport.height)
	}
	w := l.viewport.window(len(layout.rows))
	above, below := hiddenCompleteItems(layout, w.start, w.end)
	// A fitting selected item can leave no room for the chrome implied by logical
	// overflow. Keep that item visible rather than returning a body the renderer
	// will expand past its capacity. One-row renderers put overflow in their header.
	if capacity > 1 && w.end-w.start+indicatorChromeForCounts(above, below) > capacity {
		above, below = 0, 0
	}
	return indicatorCandidate{start: w.start, height: l.viewport.height, above: above, below: below}
}

func indicatorChromeForCounts(above, below int) int {
	chrome := 0
	if above > 1 {
		chrome++
	}
	if below > 1 {
		chrome++
	}
	return chrome
}

func (l *List) indicatorView(layout listLayout, candidate indicatorCandidate) ListView {
	w := l.viewport.window(len(layout.rows))
	rows := append([]ListRow(nil), layout.rows[w.start:w.end]...)
	marked := false
	for i := range rows {
		rows[i].Selected = rows[i].ID == l.cursorID
		if rows[i].Selected && !marked {
			rows[i].CursorMarker = true
			marked = true
		}
	}
	view := ListView{Rows: rows}
	if candidate.above > 1 {
		view.Above = candidate.above
	}
	if candidate.below > 1 {
		view.Below = candidate.below
	}
	return view
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func sameListItems(left, right []ListItem) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func hiddenCompleteItems(layout listLayout, start, end int) (above, below int) {
	for i := range layout.starts {
		if layout.ends[i] <= start {
			above++
		}
		if layout.starts[i] >= end {
			below++
		}
	}
	return above, below
}

func (l *List) layout() listLayout {
	layout := listLayout{starts: make([]int, len(l.items)), ends: make([]int, len(l.items))}
	if !l.viewport.Valid() {
		return layout
	}
	for i, item := range l.items {
		layout.starts[i] = len(layout.rows)
		for _, source := range strings.Split(item.Text, "\n") {
			for _, text := range widthLines(source, l.viewport.contentWidth(), l.viewport.policy) {
				itemLine := len(layout.rows) - layout.starts[i]
				status := [2]string{}
				if itemLine == 0 {
					status = item.StatusCells
				}
				layout.rows = append(layout.rows, ListRow{Text: text, ID: item.ID, ItemIndex: i, ItemLine: itemLine, StatusCells: status, GutterCells: l.viewport.gutter - 1})
			}
		}
		layout.ends[i] = len(layout.rows)
	}
	return layout
}

func (l *List) revealCursor(layout listLayout) {
	if len(l.items) == 0 || !l.viewport.Valid() {
		return
	}
	start, end := layout.starts[l.cursor], layout.ends[l.cursor]
	line := start + l.cursorLine
	if end-start <= l.viewport.height {
		if start < l.viewport.offset {
			l.viewport.offset = start
		} else if end > l.viewport.offset+l.viewport.height {
			l.viewport.offset = end - l.viewport.height
		}
	} else if line < l.viewport.offset {
		l.viewport.offset = line
	} else if line >= l.viewport.offset+l.viewport.height {
		l.viewport.offset = line - l.viewport.height + 1
	}
	l.viewport.offset = clampScroll(l.viewport.offset, len(layout.rows), l.viewport.height)
}

func (l *List) clamp(layout listLayout) {
	if len(l.items) == 0 || len(layout.rows) == 0 || !l.viewport.Valid() {
		l.viewport.offset = 0
		return
	}
	l.cursor = clampBounded(l.cursor, len(l.items))
	if l.cursorID == "" || l.itemIndex(l.cursorID) < 0 {
		l.cursorID = l.items[l.cursor].ID
	} else {
		l.cursor = l.itemIndex(l.cursorID)
	}
	h := layout.ends[l.cursor] - layout.starts[l.cursor]
	l.cursorLine = min(max(0, l.cursorLine), max(0, h-1))
	l.viewport.offset = clampScroll(l.viewport.offset, len(layout.rows), l.viewport.height)
}

func (l *List) itemIndex(id string) int {
	for i := range l.items {
		if l.items[i].ID == id {
			return i
		}
	}
	return -1
}

func lastPageOffset(h, page int) int {
	if h <= page || page <= 0 {
		return 0
	}
	return (h - 1) / page * page
}

func clampBounded(v, n int) int {
	if n <= 0 || v < 0 {
		return 0
	}
	return min(v, n-1)
}

func listGutterCells(cells int) int { return min(3, max(1, cells)) }

func statusCell(cell string) string {
	if !utf8.ValidString(cell) || ansi.StringWidth(cell) != 1 {
		return ""
	}
	for _, r := range cell {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ""
		}
	}
	return cell
}

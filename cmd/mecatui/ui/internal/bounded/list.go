package bounded

import "strings"

// ListItem is an entry rendered by a List.
type ListItem struct{ ID, Text string }

// ListRow is one rendered line in a ListView.
type ListRow struct {
	Text, ID               string
	ItemIndex, ItemLine    int
	Selected, CursorMarker bool
	Gutter                 string
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

// SetGeometry configures the list viewport dimensions and fitting policy.
func (l *List) SetGeometry(width, height, gutter int, policy Policy) {
	l.viewport.SetGeometry(width, height, gutter, policy)
	l.clamp(l.layout())
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
	old := l.layout()
	oldOffset, oldCursor, oldID, oldLine := l.viewport.offset, l.cursor, l.cursorID, l.cursorLine
	topID, topLine, haveTop := "", 0, false
	if oldOffset >= 0 && oldOffset < len(old.rows) {
		topID, topLine, haveTop = old.rows[oldOffset].ID, old.rows[oldOffset].ItemLine, true
	}
	l.items = append([]ListItem(nil), items...)
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

// ViewWithIndicators applies the one canonical indicator-adjusted geometry.
func (l *List) ViewWithIndicators(capacity int, reveal bool) ListView {
	if capacity <= 0 {
		l.viewport.height = 0
		l.reveal = false
		return ListView{}
	}
	reserved := 0
	for range 3 {
		l.viewport.height = max(1, capacity-reserved)
		if reveal {
			l.revealCursor(l.layout())
		}
		v := l.View()
		needed := 0
		if v.Above > 0 {
			needed++
		}
		if v.Below > 0 {
			needed++
		}
		next := min(needed, capacity-1)
		if next == reserved {
			break
		}
		reserved = next
	}
	l.viewport.height = max(1, capacity-reserved)
	if reveal {
		l.revealCursor(l.layout())
	}
	v := l.View()
	available := max(0, capacity-len(v.Rows))
	if v.Above > 0 && available > 0 {
		available--
	} else {
		v.Above = 0
	}
	if v.Below > 0 && available == 0 {
		v.Below = 0
	}
	l.reveal = false
	return v
}

func (l *List) layout() listLayout {
	layout := listLayout{starts: make([]int, len(l.items)), ends: make([]int, len(l.items))}
	if !l.viewport.Valid() {
		return layout
	}
	gutter := strings.Repeat(" ", l.viewport.gutter)
	for i, item := range l.items {
		layout.starts[i] = len(layout.rows)
		for _, source := range strings.Split(item.Text, "\n") {
			for _, text := range widthLines(source, l.viewport.contentWidth(), l.viewport.policy) {
				layout.rows = append(layout.rows, ListRow{Text: text, ID: item.ID, ItemIndex: i, ItemLine: len(layout.rows) - layout.starts[i], Gutter: gutter})
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

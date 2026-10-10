package ui

// scrollWindow returns the [start,end) slice bounds of a scrolling window of size
// limit over n rows, kept around the selected cursor so it stays visible. It is a
// pure function of (cursor, n, limit) — the window FOLLOWS the cursor (no stored
// offset to drift), so the selected row stays in view when paging past the top or
// bottom edge. Shared by the slash palette (renderPalette), the @-mention menu
// (renderMention); retained for those fixed-row consumers while modelsState.Render
// uses bounded.List for item-aware geometry.
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

// clampBounded constrains an index to a possibly empty collection. It remains a
// generic UI helper for fixed-row surface state.
func clampBounded(value, count int) int {
	if count <= 0 || value < 0 {
		return 0
	}
	return min(value, count-1)
}

type renderedLineWindowBounds struct {
	start, end, total, window int
}

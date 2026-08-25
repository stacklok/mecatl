package ui

// cellRect is a half-open screen-cell rectangle [x0,x1)×[y0,y1).
type cellRect struct {
	x0, x1, y0, y1 int
}

func (r cellRect) contains(x, y int) bool { return x >= r.x0 && x < r.x1 && y >= r.y0 && y < r.y1 }

type cellPoint struct{ x, y int }

// renderedSurfaceMetrics is the parent-owned placement result for the current
// rendered modal. Both bounds are always concrete: fill surfaces use the same
// rectangle for outer and content bounds.
type renderedSurfaceMetrics struct {
	outerBounds   cellRect
	contentBounds cellRect
	contentOrigin cellPoint
}

func (m *renderedSurfaceMetrics) clear() { *m = renderedSurfaceMetrics{} }

func (m renderedSurfaceMetrics) globalToLocal(x, y int) (int, int) {
	return x - m.contentOrigin.x, y - m.contentOrigin.y
}

func (m renderedSurfaceMetrics) localToGlobal(x, y int) (int, int) {
	return x + m.contentOrigin.x, y + m.contentOrigin.y
}

// centeredCardOrigin mirrors lipgloss.Place's centred placement arithmetic.
func centeredCardOrigin(cardW, cardH, regionW, regionH int) (x, y int) {
	x = max(0, (regionW-cardW)/2)
	y = max(0, (regionH-cardH)/2)
	return x, y
}

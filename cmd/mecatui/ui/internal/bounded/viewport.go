package bounded

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Policy controls how content is fitted to the viewport.
type Policy uint8

// Content fitting policies.
const (
	Wrap Policy = iota
	Clip
)

// Move identifies a viewport navigation operation.
type Move uint8

// Viewport navigation operations.
const (
	LineUp Move = iota
	LineDown
	PageUp
	PageDown
	Top
	End
)

// ViewportView is the bounded projection of caller-provided lines.
type ViewportView struct {
	Rows         []string
	Above, Below int
}

// Viewport tracks the visible window over caller-provided laid-out lines.
type Viewport struct {
	width, height, gutter int
	policy                Policy
	offset                int
}

// SetGeometry configures the viewport dimensions and fitting policy.
func (v *Viewport) SetGeometry(width, height, gutter int, policy Policy) {
	v.width, v.height, v.gutter, v.policy = width, height, max(0, gutter), policy
	if !v.Valid() {
		v.offset = 0
	}
}

// Valid reports whether the viewport has usable dimensions.
func (v *Viewport) Valid() bool { return v.width >= v.gutter+1 && v.height > 0 }

// Height returns the configured viewport height.
func (v *Viewport) Height() int { return v.height }

// Offset returns the current scroll offset.
func (v *Viewport) Offset() int { return v.offset }

// Reset scrolls the viewport to its beginning.
func (v *Viewport) Reset() { v.offset = 0 }

// Clamp constrains the current offset to the configured geometry and content size.
func (v *Viewport) Clamp(total int) { v.offset = clampScroll(v.offset, total, v.height) }

func (v *Viewport) contentWidth() int {
	if !v.Valid() {
		return 0
	}
	return v.width - v.gutter
}

func (v *Viewport) layout(lines []string) []string {
	if !v.Valid() {
		return nil
	}
	var rows []string
	for _, line := range lines {
		for _, source := range strings.Split(line, "\n") {
			rows = append(rows, widthLines(source, v.contentWidth(), v.policy)...)
		}
	}
	return rows
}

func (v *Viewport) window(total int) window {
	if !v.Valid() {
		return window{}
	}
	v.offset = clampScroll(v.offset, total, v.height)
	return lineWindow(v.offset, total, v.height)
}

// View returns the bounded projection of lines. Callers provide lines for each render.
func (v *Viewport) View(lines []string) ViewportView {
	layout := v.layout(lines)
	if len(layout) == 0 {
		v.offset = 0
		return ViewportView{}
	}
	w := v.window(len(layout))
	return ViewportView{Rows: append([]string(nil), layout[w.start:w.end]...), Above: w.start, Below: len(layout) - w.end}
}

// Move scrolls the viewport according to move.
func (v *Viewport) Move(move Move, total int) {
	if !v.Valid() {
		return
	}
	switch move {
	case LineUp:
		v.offset--
	case LineDown:
		v.offset++
	case PageUp:
		v.offset -= v.height
	case PageDown:
		v.offset += v.height
	case Top:
		v.offset = 0
	case End:
		v.offset = maxScrollOffset(total, v.height)
	}
	v.offset = clampScroll(v.offset, total, v.height)
}

type window struct{ start, end int }

func lineWindow(scroll, total, height int) window {
	start := clampScroll(scroll, total, height)
	return window{start, min(total, start+height)}
}

func widthLines(line string, width int, policy Policy) []string {
	if width <= 0 {
		return nil
	}
	if policy == Clip {
		return []string{ansi.Cut(line, 0, width) + "\x1b[0m"}
	}
	wrapped := strings.Split(ansi.Hardwrap(line, width, true), "\n")
	lines := make([]string, 0, len(wrapped))
	left := 0
	for _, row := range wrapped {
		rw := ansi.StringWidth(row)
		if rw == 0 {
			lines = append(lines, row+"\x1b[0m")
			continue
		}
		if rw > width {
			// A grapheme can be wider than a narrow viewport (for example, 2-cell
			// CJK text in a 1-cell list). Skip it entirely rather than emitting an
			// over-wide row or carrying it into the next row.
			left += rw
			continue
		}
		segment := ansi.Cut(line, left, left+rw)
		left += rw
		if ansi.StringWidth(segment) <= width {
			lines = append(lines, segment+"\x1b[0m")
		}
	}
	if len(lines) == 0 {
		return []string{"\x1b[0m"}
	}
	return lines
}

func maxScrollOffset(total, window int) int {
	if total <= window {
		return 0
	}
	return total - window
}

func clampScroll(want, total, window int) int {
	if want < 0 {
		return 0
	}
	if mx := maxScrollOffset(total, window); want > mx {
		return mx
	}
	return want
}

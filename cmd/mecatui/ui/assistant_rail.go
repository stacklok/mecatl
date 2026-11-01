package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// assistantRail draws the left rail of an assistant body one line at a time,
// reusing the previous render's lines. While an answer streams, almost all of
// its body is unchanged from the last frame, and running lipgloss over the whole
// body made every delta cost more than the last; now only lines that changed are
// rendered.
//
// The rail style has no width, background, or right border, so lipgloss renders
// a multi-line body (after turning CRLF into LF, as here) as each line rendered
// alone, padded with spaces to the widest line. render reproduces that exactly
// (assistant_rail_test.go pins it). A railed line is a pure function of its text
// and the rail's own style, which is set once, so a reused line is never stale,
// whichever block it came from. It is used only on the update goroutine.
type assistantRail struct {
	style  lipgloss.Style
	styled bool     // style has been set
	lines  []string // body lines of the previous render
	railed []string // each line rendered alone through style
	widths []int    // cell width of each railed line
}

// render returns a.style.Render(body).
func (a *assistantRail) render(body string) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	keep := 0
	for keep < len(lines) && keep < len(a.lines) && lines[keep] == a.lines[keep] {
		keep++
	}
	a.lines = lines
	a.railed, a.widths = a.railed[:keep], a.widths[:keep]
	for _, line := range lines[keep:] {
		out := a.style.Render(line)
		a.railed = append(a.railed, out)
		a.widths = append(a.widths, ansi.StringWidth(out))
	}
	if len(a.railed) == 1 {
		return a.railed[0]
	}
	widest, size := 0, 0
	for i, out := range a.railed {
		widest = max(widest, a.widths[i])
		size += len(out) + 1
	}
	var b strings.Builder
	b.Grow(size + len(a.railed)*widest)
	for i, out := range a.railed {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(out)
		for range widest - a.widths[i] {
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// forget drops the reused lines, keeping the style. It runs with
// resetBlockCaches so a rebuilt conversation doesn't keep the old answer's
// lines alive.
func (a *assistantRail) forget() {
	a.lines, a.railed, a.widths = nil, nil, nil
}

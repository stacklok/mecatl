package ui

import (
	"testing"

	"charm.land/lipgloss/v2"
)

// TestToolCardWidthCap pins decision 7: a tool card never grows past
// toolCardMaxWidth columns on a wide terminal, but on a narrow terminal the
// contentWidth-2 inset wins (the card never exceeds the viewport content). The card is
// laid out against contentWidth() = r.width - the left-margin indent, so the cap binds at
// width ≥ toolCardMaxWidth + 2 + indent and the narrow card is (width - indent - 2). The
// card's rendered width is measured per-line via lipgloss.Width on the widest line
// (renderToolBlock calls renderTool directly, so the per-block indent prefix is NOT
// applied here — this measures the raw card).
func TestToolCardWidthCap(t *testing.T) {
	const capBindsAt = toolCardMaxWidth + 2 + defaultBlockIndent
	cases := []struct {
		name     string
		width    int
		wantMax  int // the card's rendered width must be ≤ this
		wantWide bool
	}{
		{"wide terminal caps at the max", 200, toolCardMaxWidth, true},
		{"exactly the cap-binding width caps at the max", capBindsAt, toolCardMaxWidth, true},
		{"narrow terminal uses contentWidth-2", 60, 60 - defaultBlockIndent - 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRenderer()
			r.setWidth(tc.width)
			out := r.renderToolBlock("Read", `{"path":"greeting.txt"}`, false)
			got := maxLineWidth(out)
			if got > tc.wantMax {
				t.Errorf("width %d: card rendered %d cols, want ≤ %d", tc.width, got, tc.wantMax)
			}
			// A wide card should actually REACH the cap (border fills the card width), so
			// the cap is load-bearing, not vacuously satisfied by a short body.
			if tc.wantWide && got != toolCardMaxWidth {
				t.Errorf("width %d: capped card should render exactly %d cols, got %d", tc.width, toolCardMaxWidth, got)
			}
		})
	}
}

// TestToolCardWidthUnchangedAtCap proves the cap is a no-op exactly at the cap
// boundary: at the cap-binding width the card is identical to a card at a far wider
// width — i.e. the cap only ever clamps, it never changes a card that already fits.
func TestToolCardWidthUnchangedAtCap(t *testing.T) {
	const capBindsAt = toolCardMaxWidth + 2 + defaultBlockIndent

	r1 := newTestRenderer()
	r1.setWidth(capBindsAt) // contentWidth-2 == cap, min(cap, cap) == cap
	a := r1.renderToolBlock("Read", `{"path":"x"}`, false)

	r2 := newTestRenderer()
	r2.setWidth(400) // far past the cap → min clamps to the cap
	b := r2.renderToolBlock("Read", `{"path":"x"}`, false)

	if a != b {
		t.Errorf("card at the cap-binding width and at 400 must be byte-identical (both clamp to the cap):\nlen(a)=%d len(b)=%d", lipgloss.Width(a), lipgloss.Width(b))
	}
}

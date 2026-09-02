package ui

import (
	"strings"
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

// TestResolvedBashToolCardFitsViewport renders the normal transcript path, including
// the conversation indent, for a resolved Bash call whose command and result have no
// natural break points. Both views must remain within a narrow terminal.
func TestResolvedBashToolCardFitsViewport(t *testing.T) {
	const viewportWidth = 6
	command := strings.Repeat("x", 200)
	result := strings.Repeat("y", 200)

	for _, expand := range []bool{false, true} {
		t.Run(map[bool]string{false: "collapsed", true: "expanded"}[expand], func(t *testing.T) {
			r := newTestRenderer()
			r.setWidth(viewportWidth)
			c := &conversation{}
			c.addTool("bash-1", "Bash", mustJSON(t, map[string]string{"command": command}))
			if !c.resolveTool("bash-1", result, false) {
				t.Fatal("resolve Bash tool")
			}

			out := r.renderConversation(c, expand)
			for i, line := range strings.Split(out, "\n") {
				if got := maxLineWidth(line); got > viewportWidth {
					t.Errorf("line %d exceeds viewport width %d (got %d): %q", i, viewportWidth, got, stripANSIstr(line))
				}
			}
		})
	}
}

// TestResolvedBashToolCardFitsOneColumnViewport ensures the transcript indent is
// suppressed when it would otherwise make a frameless, one-column card overflow.
func TestResolvedBashToolCardFitsOneColumnViewport(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(1)
	c := &conversation{}
	c.addTool("bash-1", "Bash", mustJSON(t, map[string]string{"command": strings.Repeat("x", 200)}))
	if !c.resolveTool("bash-1", strings.Repeat("y", 200), false) {
		t.Fatal("resolve Bash tool")
	}

	for i, line := range strings.Split(r.renderConversation(c, false), "\n") {
		if got := maxLineWidth(line); got > r.width {
			t.Errorf("line %d exceeds viewport width %d (got %d): %q", i, r.width, got, stripANSIstr(line))
		}
	}
}

// TestResolvedBashToolCardFitsFrameTransition verifies the first width that can
// render the normal card frame while retaining the existing 2-cell right inset.
func TestResolvedBashToolCardFitsFrameTransition(t *testing.T) {
	command := strings.Repeat("x", 200)
	result := strings.Repeat("y", 200)

	for _, expand := range []bool{false, true} {
		t.Run(map[bool]string{false: "collapsed", true: "expanded"}[expand], func(t *testing.T) {
			r := newTestRenderer()
			r.setWidth(defaultBlockIndent + r.th.Style("toolCard").GetHorizontalFrameSize() + 2)
			c := &conversation{}
			c.addTool("bash-1", "Bash", mustJSON(t, map[string]string{"command": command}))
			if !c.resolveTool("bash-1", result, false) {
				t.Fatal("resolve Bash tool")
			}

			for i, line := range strings.Split(r.renderConversation(c, expand), "\n") {
				if got := maxLineWidth(line); got > r.width {
					t.Errorf("line %d exceeds viewport width %d (got %d): %q", i, r.width, got, stripANSIstr(line))
				}
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

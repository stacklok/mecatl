package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// TestSpeakerLabels pins decisions 1+2: the user block leads with "▌ you" and the
// assistant block with "● mecatl" — the speaker-turn glyphs. The labels are the
// first line of each block's render (stripped of ANSI).
func TestSpeakerLabels(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(80)

	user := block{kind: blockUser, raw: "hello there"}
	got := stripANSIstr(r.renderBlockFresh(0, &user, false))
	if first := firstLine(got); !strings.HasPrefix(first, "▌ you") {
		t.Errorf("user block must lead with %q, got first line %q", "▌ you", first)
	}

	asst := block{kind: blockAssistant, raw: "the answer"}
	got = stripANSIstr(r.renderBlockFresh(1, &asst, false))
	if first := firstLine(got); !strings.HasPrefix(first, "● mecatl") {
		t.Errorf("assistant block must lead with %q, got first line %q", "● mecatl", first)
	}
}

// TestUserBlockTintFillsColumn pins decision 1's critical detail: the user block's
// panel-tint background fills the FULL wrapped column (Width(r.width-frame)), not
// just the text cells — otherwise the tint is ragged-right. It renders a short
// prompt at a wide width and asserts the body line reaches the wrapped width
// (label line excluded — the label is the unfilled "▌ you").
func TestUserBlockTintFillsColumn(t *testing.T) {
	r := newTestRenderer()
	const w = 80
	r.setWidth(w)

	user := block{kind: blockUser, raw: "hi"} // far shorter than the column
	out := r.renderBlockFresh(0, &user, false)
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least a label + body line, got %q", out)
	}
	// The body line (line 1, after the "▌ you" label) must span the full wrapped
	// width even though "hi" is tiny — the tint Width fills it.
	frame := theme.New("aztec", theme.AztecPalette()).Style("userBlock").GetHorizontalFrameSize()
	bodyW := lipgloss.Width(stripANSIstr(lines[1]))
	if bodyW != w-frame {
		t.Errorf("user body line width = %d, want %d (full wrapped column = width-frame); tint is ragged-right", bodyW, w-frame)
	}
}

// TestUserBlockTintNarrowDegrades guards the width guard: at a width at/below the
// style frame the body renders unwrapped (no negative Width panic), mirroring
// wrapStyled's guard.
func TestUserBlockTintNarrowDegrades(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(2) // ≤ frame+1
	user := block{kind: blockUser, raw: "hello"}
	// Must not panic and must still carry the body text.
	out := stripANSIstr(r.renderBlockFresh(0, &user, false))
	if !strings.Contains(out, "hello") {
		t.Errorf("narrow user block should still render the body, got %q", out)
	}
}

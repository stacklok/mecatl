package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

// TestSpeakerLabels pins decisions 1+2: the user block leads with "▌ you" and the
// assistant block with "● mecatl" — the speaker-turn glyphs. The labels are the
// first line of each block's render (stripped of ANSI).
func TestSpeakerLabels(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(80)

	user := testSnapshot(0, scrollback.UserCardSnapshot{Text: "hello there"})
	got := stripANSIstr(r.renderSnapshot(0, user, false))
	if first := strings.TrimLeft(firstLine(got), " "); !strings.HasPrefix(first, "▌ you") {
		t.Errorf("user block must lead with %q, got first line %q", "▌ you", first)
	}

	asst := testSnapshot(1, scrollback.AssistantCardSnapshot{Text: "the answer"})
	got = stripANSIstr(r.renderSnapshot(1, asst, false))
	if first := strings.TrimLeft(firstLine(got), " "); !strings.HasPrefix(first, "● mecatl") {
		t.Errorf("assistant block must lead with %q, got first line %q", "● mecatl", first)
	}
}

// TestUserBlockNoBackgroundTint pins decision 1 (partial-reverted): the conversation
// user block carries NO background tint — the faint panel tint belongs ONLY to the input
// box, never the conversation history. A short prompt at a wide width must NOT pad its
// body line out to the full column (no Width fill), and must carry no background SGR.
func TestUserBlockNoBackgroundTint(t *testing.T) {
	r := newTestRenderer()
	const w = 80
	r.setWidth(w)

	user := testSnapshot(0, scrollback.UserCardSnapshot{Text: "hi"}) // far shorter than the column
	out := r.renderSnapshot(0, user, false)
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least a label + body line, got %q", out)
	}
	// The body line is NOT padded to the full column — a short prompt stays short
	// (no background to fill out). Its visible width is far below the wrapped column.
	bodyW := lipgloss.Width(stripANSIstr(lines[1]))
	if bodyW >= w-2 {
		t.Errorf("user body line width = %d, want it NOT padded to the full column (no tint to fill)", bodyW)
	}
	// No background-setting SGR anywhere in the block (the aztec bgPanel is #15201C →
	// "48;2;21;32;28"; no background of any kind should appear).
	if strings.Contains(out, "\x1b[48;") {
		t.Errorf("user block must carry NO background tint, found a background SGR in %q", out)
	}
}

// TestUserBlockNarrowDegrades guards that the body still renders (no panic) at a width
// at/below the style frame, through wrapStyled's own width-guard.
func TestUserBlockNarrowDegrades(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(2) // ≤ frame+1
	user := testSnapshot(0, scrollback.UserCardSnapshot{Text: "hello"})
	// Must not panic and must still carry the body text.
	out := stripANSIstr(r.renderSnapshot(0, user, false))
	if !strings.Contains(out, "hello") {
		t.Errorf("narrow user block should still render the body, got %q", out)
	}
}

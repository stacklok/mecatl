package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// TestConversationIndentUniform pins the left-margin indent: EVERY non-blank line of a
// rendered conversation — across all block kinds (user, assistant, tool card, notice,
// turn-stat, error) — begins with exactly defaultBlockIndent leading spaces, so the
// history aligns with the 1-col-padded header/footer instead of sitting flush at column
// 0. Blank inter-turn separator lines are exempt. It also asserts no line overflows the
// viewport (the wrap budget correctly subtracted the indent).
func TestConversationIndentUniform(t *testing.T) {
	r := newTestRenderer()
	const w = 80
	r.setWidth(w)
	c := &conversation{}
	c.addUser("a user prompt long enough to wrap across the available content column for sure, yes indeed it keeps going")
	c.appendAssistant("an assistant reply, also reasonably long so it wraps near the right edge of the indented content column")
	c.addTool("call-1", "Read", `{"path":"greeting.txt"}`)
	c.addNotice("a notice line")
	c.addTurnStat("↑1.2K ↓340 · 4.1s")
	c.addError("an error happened")

	out := r.renderConversation(c, false)
	for i, ln := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		stripped := ansi.Strip(ln)
		if strings.TrimSpace(stripped) == "" {
			continue // blank inter-turn separator
		}
		lead := len(stripped) - len(strings.TrimLeft(stripped, " "))
		if lead < defaultBlockIndent {
			t.Errorf("line %d has %d leading spaces, want ≥ %d (uniform left indent): %q", i, lead, defaultBlockIndent, stripped)
		}
		if width := ansi.StringWidth(stripped); width > w {
			t.Errorf("line %d width %d overflows viewport %d (wrap budget must subtract the indent): %q", i, width, w, stripped)
		}
	}
}

// TestConversationIndentWidth0 guards the bare/width-0 renderer (team/fleet focus panes,
// constructed as &renderer{} without newRenderer): indent is 0, so indentLines is a no-op
// and renderBlock returns the content unindented — no leading spaces, no panic.
func TestConversationIndentWidth0(t *testing.T) {
	r := &renderer{th: theme.New("aztec", theme.AztecPalette())} // bare: indent 0, width 0
	if r.indent != 0 {
		t.Fatalf("bare renderer indent = %d, want 0", r.indent)
	}
	b := block{kind: blockNotice, raw: "x"}
	out := r.renderBlock(0, &b, false)
	if strings.HasPrefix(ansi.Strip(out), " ") {
		t.Errorf("bare renderer (indent 0) must not indent: %q", out)
	}
}

// TestInterTurnSpacing pins the vertical gap between turns: consecutive blocks are
// separated by interBlockBlankLines blank lines (the CC-style breathing room) — verified
// by counting the blank run between two adjacent blocks' content.
func TestInterTurnSpacing(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(80)
	c := &conversation{}
	c.addUser("first")
	c.appendAssistant("second")

	lines := strings.Split(strings.TrimRight(r.renderConversation(c, false), "\n"), "\n")
	// Find the blank run between the user block and the assistant label.
	blankRun, sawText := 0, false
	maxBlankRun := 0
	for _, ln := range lines {
		if strings.TrimSpace(ansi.Strip(ln)) == "" {
			if sawText {
				blankRun++
			}
		} else {
			if blankRun > maxBlankRun {
				maxBlankRun = blankRun
			}
			blankRun = 0
			sawText = true
		}
	}
	if maxBlankRun != interBlockBlankLines {
		t.Errorf("inter-turn blank run = %d, want %d (interBlockBlankLines)", maxBlankRun, interBlockBlankLines)
	}
}

// TestSelectionXMapWithIndent confirms the selection screen→content x-mapping stays
// identity WITH the indent: the leading indent spaces are REAL content cells, so screen
// X k maps to content grapheme column k (the first real glyph sits at column == indent).
// This is the guard that the indent is content-not-viewport-offset, so selection never
// drifts.
func TestSelectionXMapWithIndent(t *testing.T) {
	cb := &fakeClipboard{}
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.deps.NoAltScreen = false
	m.deps.Clipboard = cb
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-xmap-0001"},
	)
	m.conv.addUser("req")
	m.conv.appendAssistant(strings.Repeat("streamed line of output\n", 60))
	m.phase = phaseIdle
	m.stuck = true
	m.refreshView()

	top := convTopRow(m)
	// screen X=0 → content col 0 (a leading indent space).
	if _, col, ok := screenToContent(m, 0, top); !ok || col != 0 {
		t.Errorf("screenToContent(x=0) = (%d, ok=%v), want (0, true) — the indent space", col, ok)
	}
	// screen X=indent → content col == indent (the first real glyph after the margin).
	if _, col, ok := screenToContent(m, defaultBlockIndent, top); !ok || col != defaultBlockIndent {
		t.Errorf("screenToContent(x=%d) = (%d, ok=%v), want (%d, true) — first real glyph; selection x-mapping drifted",
			defaultBlockIndent, col, ok, defaultBlockIndent)
	}
}

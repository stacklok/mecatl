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
	m.refreshView()

	// Find a screen row over a CONTENT line wide enough to span the probed columns (a
	// blank inter-turn/label gap would clamp every x to 0 — not an x-mapping bug, just an
	// empty line, so probe a real content row).
	top := convTopRow(m)
	contentLines := strings.Split(m.vp.GetContent(), "\n")
	var y int
	found := false
	for sy := top; sy < top+m.vp.Height(); sy++ {
		li := m.vp.YOffset() + (sy - top)
		if li >= 0 && li < len(contentLines) && len(ansi.Strip(contentLines[li])) >= 16 {
			y, found = sy, true
			break
		}
	}
	if !found {
		t.Fatal("no content line ≥16 cols visible to probe x-mapping")
	}

	// The indent + hang are REAL content cells, so screen X k maps to content grapheme
	// column k (identity) — independent of what glyph sits at column k. Assert that
	// identity across a span of columns on a content row, the property that keeps
	// selection from drifting under any left margin.
	for _, x := range []int{0, 1, 2, 3, 7, 15} {
		if _, col, ok := screenToContent(m, x, y); !ok || col != x {
			t.Errorf("screenToContent(x=%d) = (col=%d, ok=%v), want (col=%d, true) — x-mapping must be identity (indent/hang are real content cells)", x, col, ok, x)
		}
	}
}

// bodyTextColumn returns the VISUAL cell column at which the body text begins — the
// count of leading cells before the first body letter `firstLetter` on the FIRST
// non-blank line AFTER the line containing `label` (ANSI-stripped). Skipping blank lines
// matters because the assistant block now has a blank line between its label and body.
// This measures true visual alignment: the user body's leading cells are " │ " (space,
// rail glyph, space) while the assistant body's are "   " (three spaces); both land the
// text at the same column even though their leading-SPACE counts differ, so a raw
// leading-space compare would be wrong.
func bodyTextColumn(t *testing.T, out, label string, firstLetter byte) int {
	t.Helper()
	lines := strings.Split(out, "\n")
	for i, ln := range lines {
		if !strings.Contains(ansi.Strip(ln), label) {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			body := ansi.Strip(lines[j])
			if strings.TrimSpace(body) == "" {
				continue // skip the label→body blank line
			}
			if idx := strings.IndexByte(body, firstLetter); idx >= 0 {
				return ansi.StringWidth(body[:idx])
			}
			t.Fatalf("body letter %q not found in first body line %q", string(firstLetter), body)
		}
	}
	t.Fatalf("label %q not found (or no body line after it) in %q", label, out)
	return -1
}

// TestAssistantBodyHangsUnderLabel pins CHANGE A: the assistant MESSAGE body hangs by
// assistantBodyHang so its TEXT sits under "mecatl" (base indent + the "● " marker
// width), matching the user body, which the gold rail + PaddingLeft(1) already lands
// under "you". Both bodies begin at the SAME visual column == base + 2 (the marker width);
// the labels stay at the base indent. (The two bodies' leading-cell makeup differs — the
// user's is " │ ", the assistant's is "   " — so this asserts the VISUAL text column, not
// a raw leading-space count.)
func TestAssistantBodyHangsUnderLabel(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(70)
	c := &conversation{}
	c.addUser("user body text")
	c.appendAssistant("assistant body text")
	out := r.renderConversation(c, false)

	wantCol := r.indent + assistantBodyHang // base + marker width
	asstCol := bodyTextColumn(t, out, "● mecatl", 'a')
	if asstCol != wantCol {
		t.Errorf("assistant body text column = %d, want %d (base %d + hang %d, under \"mecatl\")", asstCol, wantCol, r.indent, assistantBodyHang)
	}
	userCol := bodyTextColumn(t, out, "▌ you", 'u')
	if userCol != asstCol {
		t.Errorf("user body text column = %d, assistant = %d — the two message bodies must align under their labels", userCol, asstCol)
	}
	if userCol != wantCol {
		t.Errorf("user body text column = %d, want %d (base + marker width, under \"you\")", userCol, wantCol)
	}
}

// TestAssistantBodyNoOverflow guards the wrap budget: the assistant body, wrapped at
// contentWidth()-hang and then hang-indented, must never exceed the viewport width.
func TestAssistantBodyNoOverflow(t *testing.T) {
	r := newTestRenderer()
	const w = 60
	r.setWidth(w)
	c := &conversation{}
	c.appendAssistant(strings.Repeat("a long assistant answer that keeps going and going to force wrapping ", 6))
	out := r.renderConversation(c, false)
	for i, ln := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if width := ansi.StringWidth(ansi.Strip(ln)); width > w {
			t.Errorf("assistant body line %d width %d overflows viewport %d (wrap budget must subtract base+hang): %q", i, width, w, ansi.Strip(ln))
		}
	}
}

// TestToolCardNotHangIndented confirms only MESSAGE blocks (user/assistant) get the body
// hang — a tool card stays at the base indent (its border box is not a label+body
// message), so its first line leads with exactly the base indent, not base+hang.
func TestToolCardNotHangIndented(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(70)
	c := &conversation{}
	c.addTool("call-1", "Read", `{"path":"x"}`)
	out := r.renderConversation(c, false)
	first := ansi.Strip(strings.Split(out, "\n")[0])
	lead := len(first) - len(strings.TrimLeft(first, " "))
	if lead != r.indent {
		t.Errorf("tool card lead = %d, want %d (base indent only — NOT hang-indented)", lead, r.indent)
	}
}

// TestInputTopSpacer pins CHANGE B: the layout carries exactly one blank spacer row
// directly above the input region (top padding), and the body height shrinks by that one
// row (so the spacer is accounted for, the input height-invariance holds, and convTopRow —
// summing only the regions ABOVE the body — is unchanged).
func TestInputTopSpacer(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-spacer-0001"})

	above, below := m.chrome()

	// Exactly one spacer region, height 1, sitting immediately before the input.
	spacerIdx, inputIdx := -1, -1
	for i, reg := range below {
		switch reg.role {
		case regionInputSpacer:
			spacerIdx = i
			if reg.height() != 1 {
				t.Errorf("input spacer height = %d, want 1 (a single blank row)", reg.height())
			}
		case regionInput:
			inputIdx = i
		}
	}
	if spacerIdx < 0 {
		t.Fatal("no regionInputSpacer in the layout below-body regions")
	}
	if inputIdx != spacerIdx+1 {
		t.Errorf("input spacer at %d, input at %d — the spacer must sit DIRECTLY above the input", spacerIdx, inputIdx)
	}

	// Body height = total - above - below; the spacer is in `below`, so the body shrank
	// by its one row. Confirm the sums are self-consistent and the body is positive.
	bodyH := m.height - sumHeight(above) - sumHeight(below)
	if bodyH != m.vp.Height() {
		t.Errorf("vp height %d != computed body height %d (relayout must subtract the spacer)", m.vp.Height(), bodyH)
	}
	// convTopRow (regions above the body) is unaffected by a below-body spacer.
	if got, want := convTopRow(m), sumHeight(above); got != want {
		t.Errorf("convTopRow = %d, want %d (above-body height; the spacer is below and must not shift it)", got, want)
	}
}

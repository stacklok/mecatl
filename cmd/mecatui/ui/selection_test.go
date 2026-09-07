package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// selModel builds an idle, ALT-SCREEN (selectable) model whose conversation
// overflows the viewport, with a known multi-line content set into the viewport so
// screen→content mapping, highlighting, and copy are all exercisable. Unlike
// scrollModel it sets NoAltScreen=false (selection requires the alt screen) and
// threads a fakeClipboard so the shell-write fallback is assertable.
func selModel(t *testing.T) (Model, *fakeClipboard) {
	t.Helper()
	cb := &fakeClipboard{}
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.deps.NoAltScreen = false
	m.deps.Clipboard = cb
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-sel-0001"},
	)
	m.conv.addUser("a request")
	m.conv.appendAssistant(strings.Repeat("line of streamed output\n", 120))
	m.phase = phaseIdle
	m.view.mode = followTail
	m.refreshView()
	return m, cb
}

// pressMouse / motionMouse / releaseMouse drive the three mouse messages through
// Update at a given button + cell.
func pressMouse(m Model, btn tea.MouseButton, x, y int) (Model, tea.Cmd) {
	return pressKey(m, tea.MouseClickMsg{Button: btn, X: x, Y: y})
}
func motionMouse(m Model, x, y int) (Model, tea.Cmd) {
	return pressKey(m, tea.MouseMotionMsg{Button: tea.MouseLeft, X: x, Y: y})
}
func releaseMouse(m Model, x, y int) (Model, tea.Cmd) {
	return pressKey(m, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x, Y: y})
}

// collectLeaves runs a (possibly batched) command and returns its leaf messages.
//
// It executes every leaf cmd SYNCHRONOUSLY, so it must only be handed batches whose
// leaves all return promptly — the release / right-click copy paths
// (tea.SetClipboard + the shell-write fallback), which is what the pre-existing copy
// tests feed it. It must NOT be handed the double/triple-click copy-on-select batch:
// that one also carries the multi-click disarm tick (tea.Tick(clickWindow=400ms)),
// and running it would block the full window. The click-copy tests assert on model
// state instead (selectedText + statusMsg), so no tea.Tick is ever executed and
// there is no timing dependence anywhere — see TestDoubleClickSelectsWord et al.
func collectLeaves(cmd tea.Cmd) []tea.Msg {
	var out []tea.Msg
	var walk func(c tea.Cmd)
	walk = func(c tea.Cmd) {
		if c == nil {
			return
		}
		msg := c()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, sub := range batch {
				walk(sub)
			}
			return
		}
		if msg != nil {
			out = append(out, msg)
		}
	}
	walk(cmd)
	return out
}

// osc52Payload returns the OSC52 (tea.SetClipboard) payload found among the leaves,
// or "" if none. tea.SetClipboard yields an unexported setClipboardMsg whose
// underlying type is a string, so its %v rendering IS the payload.
func osc52Payload(leaves []tea.Msg) (string, bool) {
	for _, msg := range leaves {
		// shellWriteResultMsg is the other copy-path leaf; skip it. (The multi-click
		// disarm tick never reaches here: collectLeaves is only fed the tick-free
		// release / right-click copy batches — the click-copy tests assert on model
		// state instead, so this helper never walks a batch containing a tea.Tick.)
		if _, ok := msg.(shellWriteResultMsg); ok {
			continue
		}
		return fmt.Sprint(msg), true
	}
	return "", false
}

// TestScreenToContentMapsRowToLine: a click in the conversation region maps to the
// expected logical line (YOffset + row offset); clicks above the top row and at/
// below the viewport bottom miss (Req 9).
func TestScreenToContentMapsRowToLine(t *testing.T) {
	m, _ := selModel(t)
	top := convTopRow(m)
	// The body-top is the header's RENDERED height (not a magic number); assert the
	// relationship. At width 100 with default deps the header is 2 rows, so this is
	// still 2 — behaviour unchanged — but the test no longer hardcodes it.
	if want := lipgloss.Height(m.renderHeader()); top != want {
		t.Fatalf("convTopRow = %d, want %d (rendered header height)", top, want)
	}
	// A click at the first viewport row maps to YOffset()+0.
	line, _, ok := screenToContent(m, 0, top)
	if !ok {
		t.Fatal("click at the first viewport row should map")
	}
	if line != m.vp.YOffset() {
		t.Errorf("first row mapped to line %d, want YOffset %d", line, m.vp.YOffset())
	}
	// A mid-viewport row maps to YOffset()+k (k=5 is well within the viewport
	// height) — pins that offset propagation holds beyond the origin in the common
	// (non-wrapped) case, not just the top row.
	if mid, _, ok := screenToContent(m, 0, top+5); !ok || mid != m.vp.YOffset()+5 {
		t.Errorf("mid row (top+5) mapped to line %d (ok=%v), want YOffset+5 = %d", mid, ok, m.vp.YOffset()+5)
	}
	// Above the top row: a miss (header region).
	if _, _, ok := screenToContent(m, 0, top-1); ok {
		t.Error("a click above the viewport top must not map (header region)")
	}
	// At/below the viewport bottom: a miss (input/footer region).
	if _, _, ok := screenToContent(m, 0, top+m.vp.Height()); ok {
		t.Error("a click at the viewport bottom edge must not map (input region)")
	}
}

// TestConvTopRowMatchesRenderedBodyTop validates the HAPPY PATH: at width 100 the
// header is 2 rows, and it renders the full View() frame, finds the real screen row
// of the first body content line, and asserts convTopRow points there AND
// screenToContent maps that row to the first visible content line (YOffset). Because
// the header here is 2 rows, this case would also pass against the OLD buggy
// headerH=2 constant — TestConvTopRowTracksWrappedHeader is the actual regression
// guard for the wrapped (>2 row) case the fix targets.
func TestConvTopRowMatchesRenderedBodyTop(t *testing.T) {
	m, _ := selModel(t)

	headerRows := lipgloss.Height(m.renderHeader())
	top := convTopRow(m)
	if top != headerRows {
		t.Fatalf("convTopRow = %d, want %d (rendered header height = body-top)", top, headerRows)
	}

	// screenToContent at the body-top row returns the first visible content line.
	line, _, ok := screenToContent(m, 0, top)
	if !ok {
		t.Fatal("body-top row should map")
	}
	if line != m.vp.YOffset() {
		t.Errorf("body-top mapped to line %d, want YOffset %d", line, m.vp.YOffset())
	}

	// Cross-check against the actual rendered frame: the frame line at screen index
	// `top`, ANSI-stripped, must equal the first visible body line — i.e. the body
	// really does render at convTopRow on screen.
	frame := strings.Split(m.View().Content, "\n")
	if top >= len(frame) {
		t.Fatalf("convTopRow %d past frame end (%d lines)", top, len(frame))
	}
	// Compare on content identity (right-trimmed): the frame pads the body line to
	// the full terminal width while vp.GetContent() carries the renderer's own
	// padding, so the trailing whitespace differs by a cell — the load-bearing
	// assertion is that the SAME text renders at screen row `top`.
	gotFrameLine := strings.TrimRight(ansi.Strip(frame[top]), " ")
	wantBodyLine := strings.TrimRight(ansi.Strip(strings.Split(m.vp.GetContent(), "\n")[m.vp.YOffset()]), " ")
	if gotFrameLine != wantBodyLine {
		t.Errorf("frame[%d] = %q, want first visible body line %q", top, gotFrameLine, wantBodyLine)
	}
}

// TestConvTopRowTracksWrappedHeader is the ACTUAL regression guard for the wrapped
// case: a long model id + acceptEdits mode + long host:port at a NARROW width forces
// the identity line to wrap, so the header renders >2 rows. convTopRow must track
// that taller height; the body must RENDER at that row in the real frame (the
// renderHeader/headerHeight-disagreement class the fix eliminates); the mapping must
// propagate the offset across the whole viewport (not just the origin); and the row
// just above (the header's last row) must NOT map. selModel sets stuck=true over 120
// content lines, so YOffset>0 and the top visible line is real text — the frame
// cross-check is not vacuous.
func TestConvTopRowTracksWrappedHeader(t *testing.T) {
	m, _ := selModel(t)
	m.deps.Model = "openrouter/anthropic/claude-3.5-sonnet-20241022-extended"
	m.deps.Mode = "acceptEdits"
	m.deps.Server = "some-long-host.example.internal:50051"
	// Resize NARROW so the joined identity line exceeds the width and wraps.
	mm, _ := m.onResize(tea.WindowSizeMsg{Width: 40, Height: 30})
	m = mm.(Model)

	headerRows := lipgloss.Height(m.renderHeader())
	if headerRows <= 2 {
		t.Fatalf("PRECONDITION: header did not wrap (%d rows); test would no-op", headerRows)
	}

	top := convTopRow(m)
	if top != headerRows {
		t.Fatalf("convTopRow = %d, want %d (wrapped header height)", top, headerRows)
	}

	line, _, ok := screenToContent(m, 0, top)
	if !ok {
		t.Fatal("body-top row should map under a wrapped header")
	}
	if line != m.vp.YOffset() {
		t.Errorf("body-top mapped to line %d, want YOffset %d", line, m.vp.YOffset())
	}

	// FRAME CROSS-CHECK under the wrapped header: the body must actually RENDER at
	// screen row `top` in the full frame. This is what catches a renderHeader /
	// headerHeight disagreement — convTopRow==headerHeight() alone would pass even if
	// the body rendered somewhere else. Compared right-trimmed (the frame pads to the
	// terminal width; identity is the load-bearing part). The body-top line is real
	// non-blank text here (stuck=true, YOffset>0), so the compare is not vacuous.
	frame := strings.Split(m.View().Content, "\n")
	if top >= len(frame) {
		t.Fatalf("convTopRow %d past frame end (%d lines)", top, len(frame))
	}
	bodyLines := strings.Split(m.vp.GetContent(), "\n")
	gotFrameLine := strings.TrimRight(ansi.Strip(frame[top]), " ")
	wantBodyLine := strings.TrimRight(ansi.Strip(bodyLines[m.vp.YOffset()]), " ")
	if gotFrameLine == "" {
		t.Fatal("PRECONDITION: body-top frame line is blank; frame cross-check would be vacuous")
	}
	if gotFrameLine != wantBodyLine {
		t.Errorf("frame[%d] = %q, want first visible body line %q", top, gotFrameLine, wantBodyLine)
	}

	// MID-VIEWPORT mapping: the bug shifted EVERY row, not just the top. A row k below
	// the body-top must map to YOffset()+k. k=3 is within the (shrunken) viewport
	// height under the wrapped header.
	const k = 3
	if k >= m.vp.Height() {
		t.Fatalf("PRECONDITION: viewport height %d too small for mid-row k=%d", m.vp.Height(), k)
	}
	if mid, _, ok := screenToContent(m, 0, top+k); !ok || mid != m.vp.YOffset()+k {
		t.Errorf("mid row (top+%d) mapped to line %d (ok=%v), want YOffset+%d = %d", k, mid, ok, k, m.vp.YOffset()+k)
	}

	// The row just above the body-top is the header's last row — no selection there.
	if _, _, ok := screenToContent(m, 0, top-1); ok {
		t.Error("the header's last row (convTopRow-1) must not map")
	}
}

// TestOnResizeUsesMeasuredHeaderHeight guards the viewport sizing: vpH is derived
// from the MEASURED header height, so a wrapping resize shrinks the viewport by the
// real header rows, and a non-wrapping (width 100) resize keeps the steady-state
// height (header == 2) byte-for-byte unchanged. It uses the SAME total height for
// both cases and asserts the wrapped vpH is strictly LESS — proving the measured
// height actually flows into sizing (not a no-op).
func TestOnResizeUsesMeasuredHeaderHeight(t *testing.T) {
	// taH=5 (input region: the 3-row textarea + its rail top-pad row, measured at 4 +
	// historical 1), footerH=2, spacerH=1 (the inter-region spacer above the input). The
	// body is total minus header + spacer + input + footer. (taH grew by 1 vs the
	// pre-top-pad layout — the inputRailPadTop row.)
	const taH, footerH, spacerH, totalH = 5, 2, 1, 30

	// Wrapping case: long deps at a narrow width.
	m, _ := selModel(t)
	m.deps.Model = "openrouter/anthropic/claude-3.5-sonnet-20241022-extended"
	m.deps.Mode = "acceptEdits"
	m.deps.Server = "some-long-host.example.internal:50051"
	mm, _ := m.onResize(tea.WindowSizeMsg{Width: 40, Height: totalH})
	m = mm.(Model)
	wrappedHeader := lipgloss.Height(m.renderHeader())
	if wrappedHeader <= 2 {
		t.Fatalf("PRECONDITION: header did not wrap (%d rows); sizing test would be vacuous", wrappedHeader)
	}
	if got, want := m.vp.Height(), m.height-taH-footerH-spacerH-wrappedHeader; got != want {
		t.Errorf("wrapped vpH = %d, want %d (height - %d - %d - %d - measured header)", got, want, taH, footerH, spacerH)
	}

	// Non-wrapping steady state: same total height at width 100, header is 2 rows.
	m2, _ := selModel(t)
	mm2, _ := m2.onResize(tea.WindowSizeMsg{Width: 100, Height: totalH})
	m2 = mm2.(Model)
	if got, want := m2.vp.Height(), m2.height-taH-footerH-spacerH-2; got != want {
		t.Errorf("steady-state vpH = %d, want %d (header == 2 rows)", got, want)
	}

	// The wrapped (taller header) viewport must be strictly SHORTER than the
	// non-wrapped one for the same total height — the measured header flows into
	// sizing rather than being ignored.
	if m.vp.Height() >= m2.vp.Height() {
		t.Errorf("wrapped vpH %d should be < non-wrapped vpH %d (taller header eats more rows)", m.vp.Height(), m2.vp.Height())
	}
}

// TestScreenToContentRespectsScrollOffset: after scrolling up, the SAME screen row
// maps to a HIGHER logical line (the YOffset shifted) — Req 2's logical anchoring.
func TestScreenToContentRespectsScrollOffset(t *testing.T) {
	m, _ := selModel(t)
	top := convTopRow(m)
	beforeLine, _, ok := screenToContent(m, 0, top+1)
	if !ok {
		t.Fatal("precondition: row should map")
	}
	beforeOff := m.vp.YOffset()

	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.vp.YOffset() >= beforeOff {
		t.Fatalf("precondition: pgup should reduce YOffset (%d → %d)", beforeOff, m.vp.YOffset())
	}

	afterLine, _, ok := screenToContent(m, 0, top+1)
	if !ok {
		t.Fatal("row should still map after scroll")
	}
	if afterLine >= beforeLine {
		t.Errorf("after scrolling up the same screen row should map to a higher (smaller) logical line: %d → %d", beforeLine, afterLine)
	}
	if afterLine != m.vp.YOffset()+1 {
		t.Errorf("mapped line %d, want YOffset+1 = %d", afterLine, m.vp.YOffset()+1)
	}
}

// TestSelectedTextStripsANSI: a styled (SGR-coloured) line yields an ANSI-FREE
// payload with trailing padding trimmed. FAILS if any ANSI escape leaks.
func TestSelectedTextStripsANSI(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	styled := th.Style("toolName").Render("HELLO") + " world   " // trailing pad
	content := styled + "\nplain second line"
	sel := selection{active: true, anchorL: 0, anchorC: 0, headL: 0, headC: graphemeCount("HELLO world   ")}
	got := selectedText(content, sel)
	if strings.Contains(got, "\x1b") {
		t.Errorf("payload leaked ANSI: %q", got)
	}
	if got != "HELLO world" {
		t.Errorf("payload = %q, want %q (ansi stripped, trailing pad trimmed)", got, "HELLO world")
	}
}

// TestCellWidthForCol pins the grapheme-column → CELL-width conversion that lets
// styleSelection place the highlight correctly over WIDE runes (lipgloss.StyleRanges
// is cell-indexed; the selection's columns are grapheme-indexed). "ab中文cd" is 6
// grapheme columns but the two CJK runes are 2 cells each, so the first 4 grapheme
// columns ("ab中文") span 1+1+2+2 = 6 cells.
func TestCellWidthForCol(t *testing.T) {
	cases := []struct {
		stripped  string
		col, want int
	}{
		{"ab中文cd", 0, 0},
		{"ab中文cd", 2, 2},  // "ab" → 2 cells
		{"ab中文cd", 3, 4},  // "ab中" → 1+1+2
		{"ab中文cd", 4, 6},  // "ab中文" → 1+1+2+2 (the plan's load-bearing case)
		{"ab中文cd", 6, 8},  // whole line → 1+1+2+2+1+1
		{"ab中文cd", 99, 8}, // past end clamps to full width
		{"ascii", 3, 3},   // narrow runes: cell == grapheme
		{"", 0, 0},
	}
	for _, tc := range cases {
		if got := cellWidthForCol(tc.stripped, tc.col); got != tc.want {
			t.Errorf("cellWidthForCol(%q, %d) = %d, want %d", tc.stripped, tc.col, got, tc.want)
		}
	}
}

// TestStyleSelectionWideRuneSpan: selecting a span that starts AFTER wide runes
// places the open SGR immediately before the selected substring — proving the
// grapheme→cell conversion (cellWidthForCol) feeds StyleRanges the right cell
// offsets. Selecting "cd" (grapheme cols [4,6)) over "ab中文cd" must wrap exactly
// "cd": the open SGR sits immediately before "cd", and the wide runes BEFORE it are
// NOT inside the styled block.
func TestStyleSelectionWideRuneSpan(t *testing.T) {
	m, _ := selModel(t)
	content := "ab中文cd"
	sel := selection{active: true, anchorL: 0, anchorC: 4, headL: 0, headC: 6} // "cd"
	open := openSelectionSGR(t, m)
	styled := styleSelection(content, sel, m.deps.Theme.Style("selection"))

	if !strings.Contains(styled, open+"cd") {
		t.Errorf("wide-rune span: want open SGR %q immediately before %q in %q", open, "cd", styled)
	}
	// The wide runes before the span must NOT be inside the styled block: nothing
	// styled should precede "中文" up to the open SGR.
	if strings.Contains(styled, open+"中") || strings.Contains(styled, open+"ab") {
		t.Errorf("the highlight must start at the selected 'cd', not over the preceding glyphs: %q", styled)
	}
}

// TestWideRuneSelectionThroughStyledRenderPath combines the wide-rune cell-conversion
// (cellWidthForCol) with the styled-content fidelity guard — the offset bug is most
// dangerous when wide runes coexist with ANSI-styled lines above. It builds a styled
// transcript (collapsed reasoning summary etc. above) whose ANSWER line contains CJK
// wide runes, selects that whole line, renders the FULL frame, and asserts the
// selection bg SGR lands on the answer's screen row (and no earlier row) AND wraps the
// wide glyphs (the open SGR sits immediately before the "中文" run on that row).
func TestWideRuneSelectionThroughStyledRenderPath(t *testing.T) {
	const wide = "中文"
	m, answerIdx := styledTranscriptModel(t, "answer with "+wide+" wide runes here")

	// Select the whole answer line.
	m = m.lineSelect(answerIdx)
	if !m.sel.active || m.sel.empty() {
		t.Fatal("lineSelect should produce a non-empty selection on the wide-rune answer line")
	}

	wantRow := convTopRow(m) + answerIdx - m.vp.YOffset()
	// The highlight is on the answer's row and nowhere earlier (styled-content guard).
	assertHighlightOnlyOnRow(t, m, wantRow)

	// The wide-rune run is wrapped by the highlight: because the whole line is selected
	// (anchorC 0), the open SGR opens at column 0 and the line — including the wide
	// runes — is inside the styled block, so the "中文" run appears on the highlighted
	// row. The cell-width conversion placed the span over the full glyph width without
	// truncating mid-wide-rune (a grapheme-vs-cell bug would clip "中文").
	frame := strings.Split(m.View().Content, "\n")
	if !strings.Contains(ansi.Strip(frame[wantRow]), wide) {
		t.Errorf("the wide-rune run %q is missing from the highlighted row %d: %q", wide, wantRow, ansi.Strip(frame[wantRow]))
	}
	open := openSelectionSGR(t, m)
	// The selected line is styled from its start: the open SGR appears on the row and
	// the wide-rune run follows it within the same styled block (no reset between the
	// open SGR and "中文").
	row := frame[wantRow]
	oi := strings.Index(row, open)
	if oi < 0 {
		t.Fatalf("open SGR not found on the highlighted row: %q", row)
	}
	if wi := strings.Index(row, wide); wi < oi {
		t.Errorf("the wide-rune run appears before the selection open SGR on row %d — the highlight does not cover it: %q", wantRow, row)
	}
}

// TestSelectionHighlightOnStyledLaterLine is the CORE regression guard for THIS bug
// (the highlight rendered ~2 lines above the selected line on ANSI-styled content).
// It builds the conversation through the REAL render path — a collapsed reasoning
// summary line (which carries the multibyte "·" AND dim SGR, so ansi.Strip(line) !=
// line for a line ABOVE the answer) plus a glamour-styled assistant answer — so the
// content is ANSI-styled exactly like a real session. It selects on the ANSWER line
// (several content lines down), renders the full View(), and asserts the selection bg
// SGR appears on the EXPECTED screen row and on NO earlier row.
//
// This MUST FAIL against the old viewport.SetHighlights path: parseMatches walked
// ansi.Strip(content) for positions but indexed the original ANSI bytes for newline
// detection, so the stripped/original offset divergence over the styled lines above
// pushed the highlight onto the wrong (earlier) row. styleSelection splices per-line,
// so the highlight lands on the selected line. NOTE for maintainers: plain-content
// tests never triggered this — STYLED content (ansi.Strip(line) != line above the
// selection) is MANDATORY for this guard.
func TestSelectionHighlightOnStyledLaterLine(t *testing.T) {
	cb := &fakeClipboard{}
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.deps.NoAltScreen = false
	m.deps.Clipboard = cb
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 40},
		client.SessionReadyMsg{SessionID: "sess-styled-1"},
	)
	// Real render path: a user prompt, then an assistant turn whose collapsed reasoning
	// summary line carries the multibyte "·" + dim SGR (so ansi.Strip != raw on lines
	// ABOVE the answer — the bug's trigger), plus a glamour-styled answer line with a
	// UNIQUE marker word to locate it.
	const marker = "ANSWERMARKERZZZ"
	m.conv.addUser("a styled request")
	m.conv.startAssistant()
	m.conv.appendReasoning("step one\nstep two\nstep three")
	m.conv.endReasoningStream() // freeze the collapsed "reasoning summary · N lines" line
	m.conv.appendAssistant(marker + " is the selected answer line")
	m.phase = phaseIdle
	m.view.mode = followTail
	m.refreshView()

	// PRECONDITION: at least one content line ABOVE the answer is ANSI-styled (the
	// stripped form differs from the raw), so the original-vs-stripped offset
	// divergence that broke the native highlighter is actually present.
	lines := strings.Split(m.vp.GetContent(), "\n")
	answerIdx := lineIndexContaining(m.vp.GetContent(), marker)
	if answerIdx <= 0 {
		t.Fatalf("answer marker not found below another line (idx=%d)", answerIdx)
	}
	styledAbove := false
	for i := 0; i < answerIdx; i++ {
		if ansi.Strip(lines[i]) != lines[i] {
			styledAbove = true
			break
		}
	}
	if !styledAbove {
		t.Fatal("PRECONDITION: no ANSI-styled line above the answer; the bug's trigger is absent and this guard would be vacuous")
	}

	// Select the answer line (whole line).
	m = m.lineSelect(answerIdx)
	if !m.sel.active || m.sel.empty() {
		t.Fatal("precondition: lineSelect should produce a non-empty selection on the answer line")
	}

	// The expected screen row of the selected line.
	wantRow := convTopRow(m) + answerIdx - m.vp.YOffset()
	frame := strings.Split(m.View().Content, "\n")
	if wantRow < 0 || wantRow >= len(frame) {
		t.Fatalf("expected row %d out of frame range (%d lines)", wantRow, len(frame))
	}
	selSGR := selectionBgSGR(t, m)

	// The selection bg SGR must appear on the EXPECTED row.
	if !strings.Contains(frame[wantRow], selSGR) {
		t.Errorf("selection highlight missing on the answer's screen row %d: %q", wantRow, frame[wantRow])
	}
	// And on NO EARLIER row (the exact wrong-line bug: the old path painted it ~2 rows
	// above, over the styled reasoning/user lines).
	for r := 0; r < wantRow; r++ {
		if strings.Contains(frame[r], selSGR) {
			t.Errorf("selection highlight leaked onto earlier screen row %d (should be only on row %d): %q", r, wantRow, frame[r])
		}
	}
}

// styledTranscriptModel builds a selectable, idle model whose conversation is built
// through the REAL render path (user prompt → assistant turn with a collapsed
// reasoning summary — multibyte "·" + dim SGR — then a glamour-styled answer line
// carrying `answer`), so content lines ABOVE the answer are ANSI-styled (ansi.Strip
// != raw). It returns the model, the logical line index of the answer, and the
// renderer's stripped form of that answer line (the glyphs as they appear after
// glamour). The answer text must be a single visual line at width 100.
func styledTranscriptModel(t *testing.T, answer string) (Model, int) {
	t.Helper()
	cb := &fakeClipboard{}
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.deps.NoAltScreen = false
	m.deps.Clipboard = cb
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 40},
		client.SessionReadyMsg{SessionID: "sess-styled-tx"},
	)
	m.conv.addUser("a styled request")
	m.conv.startAssistant()
	m.conv.appendReasoning("step one\nstep two\nstep three")
	m.conv.endReasoningStream()
	m.conv.appendAssistant(answer)
	m.phase = phaseIdle
	m.view.mode = followTail
	m.refreshView()

	idx := lineIndexContaining(m.vp.GetContent(), strings.Fields(answer)[0])
	if idx <= 0 {
		t.Fatalf("answer line %q not found below another line (idx=%d)", answer, idx)
	}
	// PRECONDITION: at least one styled line above the answer (the bug's trigger).
	lines := strings.Split(m.vp.GetContent(), "\n")
	styledAbove := false
	for i := 0; i < idx; i++ {
		if ansi.Strip(lines[i]) != lines[i] {
			styledAbove = true
			break
		}
	}
	if !styledAbove {
		t.Fatal("PRECONDITION: no ANSI-styled line above the answer; the bug's trigger is absent")
	}
	return m, idx
}

// assertHighlightOnlyOnRow renders the full frame and asserts the selection bg SGR
// is present on screen row `wantRow` and on NO earlier row — the wrong-line guard.
func assertHighlightOnlyOnRow(t *testing.T, m Model, wantRow int) {
	t.Helper()
	frame := strings.Split(m.View().Content, "\n")
	if wantRow < 0 || wantRow >= len(frame) {
		t.Fatalf("expected row %d out of frame range (%d lines)", wantRow, len(frame))
	}
	selSGR := selectionBgSGR(t, m)
	if !strings.Contains(frame[wantRow], selSGR) {
		t.Errorf("selection highlight missing on the expected screen row %d: %q", wantRow, frame[wantRow])
	}
	for r := 0; r < wantRow; r++ {
		if strings.Contains(frame[r], selSGR) {
			t.Errorf("selection highlight leaked onto earlier screen row %d (should be only on row %d): %q", r, wantRow, frame[r])
		}
	}
}

// TestDragReSplicesAfterDeltaUsesFreshBase is the selBase-staleness guard. The whole
// risk of the m.selBase state is that a content change (a streaming delta) re-captures
// the base in refreshView, and a SUBSEQUENT gesture (snapshotSelection) must re-splice
// the FRESH base — not a stale one. This exercises exactly that path: select on a
// styled answer line, apply a delta + frame flush (refreshView re-renders the GROWN
// content and re-captures selBase), THEN drag to extend the selection (snapshotSelection
// re-splices selBase in place). The rendered frame must carry the highlight correctly
// against the fresh content — wrapping the answer line's text on its row, nowhere
// earlier — proving the re-splice used the grown base.
func TestDragReSplicesAfterDeltaUsesFreshBase(t *testing.T) {
	const marker = "FRESHBASEMARKER"
	m, answerIdx := styledTranscriptModel(t, marker+" is the selected answer line")

	// Scroll up so the answer line is on a stable, non-tail line and a delta won't
	// re-pin the view past it (the append lands below the selection).
	for m.vp.YOffset() > 0 && answerIdx < m.vp.YOffset() {
		m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	}
	top := convTopRow(m)
	y := top + (answerIdx - m.vp.YOffset())
	if y < top || y >= top+m.vp.Height() {
		t.Fatalf("answer line %d not on screen (YOffset=%d top=%d h=%d)", answerIdx, m.vp.YOffset(), top, m.vp.Height())
	}

	// Anchor on the answer line, at the first real glyph past the assistant body margin
	// (base indent + body hang) so the selection open SGR sits immediately before the
	// marker text, not before the leading margin spaces.
	m, _ = pressMouse(m, tea.MouseLeft, convBodyXOffset, y)
	if !m.sel.active {
		t.Fatal("press should activate a selection on the answer line")
	}

	// Streaming delta BELOW the selection + frame flush: refreshView re-renders the
	// grown conversation and re-captures m.selBase against it.
	m.phase = phaseRunning
	m = applyAll(m,
		client.AssistantDeltaMsg{Turn: 1, Text: strings.Repeat("appended tail line\n", 5)},
		renderTickMsg{},
	)
	if !m.sel.active {
		t.Fatal("selection should survive a streaming delta below it")
	}
	m.phase = phaseIdle

	// The grown content (post-delta) has MORE lines than at press time — record the
	// fresh unstyled render line count so a stale (shorter) base re-splice is caught.
	freshLineCount := strings.Count(m.rend.renderConversation(&m.conv, m.expandTools), "\n")

	// Now DRAG to extend the head along the answer line: snapshotSelection re-splices
	// the (freshly re-captured) selBase in place — NOT a stale base.
	m, _ = motionMouse(m, 30, y)
	if !m.sel.active || m.sel.empty() {
		t.Fatal("drag should extend to a non-empty selection on the answer line")
	}

	// TEETH: the post-drag viewport content must have the FRESH (grown) line count. A
	// stale base re-splice would SetContent the shorter pre-delta content, shrinking the
	// viewport's line count below the current conversation render — caught here.
	if got := strings.Count(m.vp.GetContent(), "\n"); got != freshLineCount {
		t.Errorf("post-drag viewport has %d newlines, want %d (the fresh grown content) — snapshotSelection re-spliced a STALE base", got, freshLineCount)
	}

	// The re-splice must be correct against the FRESH content: the answer line's text
	// sits immediately after the selection open SGR on its row, and the bg SGR is on no
	// earlier row. If the re-splice had used a stale base, the spliced line would not
	// match the current viewport content and the highlight would land wrong.
	open := openSelectionSGR(t, m)
	wantRow := convTopRow(m) + answerIdx - m.vp.YOffset()
	frame := strings.Split(m.View().Content, "\n")
	if wantRow < 0 || wantRow >= len(frame) {
		t.Fatalf("expected row %d out of frame range (%d lines)", wantRow, len(frame))
	}
	if !strings.Contains(frame[wantRow], open+marker) {
		t.Errorf("re-splice did not wrap the fresh answer text: want open SGR %q immediately before %q on row %d, got %q", open, marker, wantRow, frame[wantRow])
	}
	assertHighlightOnlyOnRow(t, m, wantRow)
}

// TestInitialSelectionInDirtyWindowKeepsTailFollowed covers the distinct first-click
// path: a streaming delta has grown the conversation but its coalesced render tick
// has not yet run. snapshotSelection must install the fresh content AND preserve
// tail-following immediately; the tick must keep it there.
func TestInitialSelectionInDirtyWindowKeepsTailFollowed(t *testing.T) {
	const tail = "DIRTY SELECTION TAIL"

	m, _ := selModel(t)
	m.phase = phaseRunning
	if m.view.mode != followTail || !m.vp.AtBottom() {
		t.Fatalf("precondition: selection model should follow the tail: stuck=%v atBottom=%v", m.view.mode == followTail, m.vp.AtBottom())
	}

	// The real delta reducer marks the grown transcript dirty without flushing it.
	m = applyAll(m, client.AssistantDeltaMsg{Turn: 1, Text: "\n" + tail})
	if !m.viewDirty {
		t.Fatal("precondition: streaming delta should leave the view dirty before its render tick")
	}

	// The initial press starts a selection and takes the dirty snapshot before the
	// pending render tick. It must not leave the former YOffset visible.
	m, _ = pressMouse(m, tea.MouseLeft, 0, convTopRow(m))
	if !m.sel.active {
		t.Fatal("initial press should activate a selection")
	}
	if !m.vp.AtBottom() {
		t.Fatal("dirty selection snapshot should remain at the fresh tail")
	}
	if m.view.mode != followTail {
		t.Fatal("dirty selection snapshot should keep tail-following enabled")
	}
	if !strings.Contains(ansi.Strip(m.vp.View()), tail) {
		t.Fatalf("dirty selection snapshot should show the fresh tail %q", tail)
	}

	m = applyAll(m, renderTickMsg{})
	if !m.vp.AtBottom() {
		t.Fatal("render tick should keep the dirty selection snapshot at the tail")
	}
	if m.view.mode != followTail {
		t.Fatal("render tick should keep tail-following enabled")
	}
	if !strings.Contains(ansi.Strip(m.vp.View()), tail) {
		t.Fatalf("render tick should keep the fresh tail %q visible", tail)
	}
}

// TestInitialSelectionInDirtyWindowDoesNotRepinManualScroll covers the complementary
// first-click path: a dirty snapshot must not turn a manually-scrolled viewport back
// into a tail-following one.
func TestInitialSelectionInDirtyWindowDoesNotRepinManualScroll(t *testing.T) {
	m, _ := selModel(t)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.view.mode == followTail || m.vp.AtBottom() {
		t.Fatalf("precondition: pgup should leave the selection model manually scrolled: stuck=%v atBottom=%v", m.view.mode == followTail, m.vp.AtBottom())
	}
	before := m.vp.YOffset()
	m.phase = phaseRunning

	// The real delta reducer creates the dirty window without the coalesced tick.
	m = applyAll(m, client.AssistantDeltaMsg{Turn: 1, Text: "\nDIRTY MANUAL SCROLL TAIL"})
	if !m.viewDirty {
		t.Fatal("precondition: streaming delta should leave the view dirty before its render tick")
	}

	// This is safely inside the viewport, rather than either edge that arms drag
	// autoscroll. A press begins the selection without moving it.
	top := convTopRow(m)
	y := top + 5
	if y >= top+m.vp.Height()-1 {
		t.Fatalf("precondition: selection coordinate y=%d is not inside the viewport", y)
	}
	m, _ = pressMouse(m, tea.MouseLeft, 10, y)
	if !m.sel.active {
		t.Fatal("initial press should activate a selection")
	}
	if m.view.mode == followTail || m.vp.AtBottom() {
		t.Fatalf("dirty selection snapshot repinned manual scroll: stuck=%v atBottom=%v", m.view.mode == followTail, m.vp.AtBottom())
	}
	if got := m.vp.YOffset(); got != before {
		t.Fatalf("dirty selection snapshot changed manual YOffset: got %d, want %d", got, before)
	}

	m = applyAll(m, renderTickMsg{})
	if m.view.mode == followTail || m.vp.AtBottom() {
		t.Fatalf("render tick repinned manual scroll: stuck=%v atBottom=%v", m.view.mode == followTail, m.vp.AtBottom())
	}
	if got := m.vp.YOffset(); got != before {
		t.Fatalf("render tick changed manual YOffset: got %d, want %d", got, before)
	}
}

// TestGestureInDirtyWindowDoesNotFlashBack covers the gap
// TestDragReSplicesAfterDeltaUsesFreshBase deliberately leaves: that test always
// pairs the streaming delta with its renderTickMsg flush, so selBase is already
// refreshed before the next gesture. But a streamed delta only marks the view
// DIRTY (the flush is deferred to the frame-cadence tick) — a mouse gesture that
// lands in the window BETWEEN the delta and the tick would re-splice the STALE
// selBase and SetContent it, reverting the viewport to the pre-delta conversation
// (a visible "flash back" to an earlier state). snapshotSelection must re-capture
// the base from the live conversation whenever the view is dirty, so the splice
// always starts from current content. Here a delta lands in the dirty window
// (viewDirty set, tick not yet fired) before the extending drag, and the post-drag
// viewport must still carry the new token (not the pre-delta content).
func TestGestureInDirtyWindowDoesNotFlashBack(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.deps.NoAltScreen = false
	m.deps.Clipboard = &fakeClipboard{}
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-dirty-window"},
	)
	// A settled multi-turn conversation that overflows the viewport.
	for i := 0; i < 8; i++ {
		m.conv.addUser("question " + strings.Repeat("x", 30))
		m.conv.appendAssistant(strings.Repeat("answer line\n", 12))
	}
	m.phase = phaseRunning
	m.view.mode = followTail
	m.refreshView()

	top := convTopRow(m)
	y := top + 5

	// Anchor + extend a selection (non-empty, so it stays active).
	m, _ = pressMouse(m, tea.MouseLeft, 10, y)
	m, _ = motionMouse(m, 20, y)
	if !m.sel.active || m.sel.empty() {
		t.Fatal("press+drag should open a non-empty selection")
	}

	// A streaming delta lands in the dirty window: the conversation advances and the
	// view is marked dirty, but the frame-cadence render tick has NOT fired yet (so
	// selBase has not been refreshed by refreshView). Appending directly + setting
	// viewDirty is exactly what the AssistantDeltaMsg reducer does (appendAssistant +
	// markDirty) minus the tick arm.
	m.conv.appendAssistant("IN-FLIGHT DELTA TOKEN\n")
	m.viewDirty = true

	// DRAG again INSIDE the dirty window: snapshotSelection must re-capture the fresh
	// base (grown by the delta) rather than re-splice the stale pre-delta one.
	m, _ = motionMouse(m, 30, y)

	// TEETH: the post-drag viewport must contain the in-flight token. A stale base
	// re-splice would SetContent the pre-delta content, dropping the token (flash back).
	// Assert on the ANSI-STRIPPED content: the markdown render interleaves style codes
	// with the text, so a literal substring match on the raw output is unreliable.
	if !strings.Contains(ansi.Strip(m.vp.GetContent()), "IN-FLIGHT DELTA TOKEN") {
		t.Error("post-drag viewport lost the in-flight delta — snapshotSelection re-spliced a STALE base (flash back)")
	}
}

// openSelectionSGR returns the full open SGR (fg+bg, up to and including the 'm')
// the resolved "selection" theme style emits — the prefix that must immediately
// precede a styled span in a styleSelection/StyleRanges render.
func openSelectionSGR(t *testing.T, m Model) string {
	t.Helper()
	probe := m.deps.Theme.Style("selection").Render("X")
	mIdx := strings.IndexByte(probe, 'm')
	if mIdx < 0 {
		t.Fatalf("selection style has no SGR: %q", probe)
	}
	return probe[:mIdx+1]
}

// styledLines is the per-line result of styleSelection over content, split on "\n",
// using the model's "selection" theme style — the render-level basis for the
// migrated byteRanges tests (they used to assert on the native SetHighlights byte
// ranges; the highlight is now an app-owned per-line splice).
func styledLines(m Model, content string, sel selection) []string {
	styled := styleSelection(content, sel, m.deps.Theme.Style("selection"))
	return strings.Split(styled, "\n")
}

// TestSelectionMultiLineStylesEachSpannedLine (migrated from TestByteRangesMultiLine):
// each spanned line carries the selection open SGR immediately wrapping its expected
// substring — the per-line splice covers the start-line tail and the end-line head.
func TestSelectionMultiLineStylesEachSpannedLine(t *testing.T) {
	m, _ := selModel(t)
	content := "hello world\nsecond line\nthird row"
	// Select from col 6 line0 ("world") through col 6 line1 ("second").
	sel := selection{active: true, anchorL: 0, anchorC: 6, headL: 1, headC: 6}
	open := openSelectionSGR(t, m)
	lines := styledLines(m, content, sel)

	if !strings.Contains(lines[0], open+"world") {
		t.Errorf("line0 styled span: want open SGR %q immediately before %q in %q", open, "world", lines[0])
	}
	if !strings.Contains(lines[1], open+"second") {
		t.Errorf("line1 styled span: want open SGR %q immediately before %q in %q", open, "second", lines[1])
	}
	// The unspanned line2 must carry no selection styling.
	if strings.Contains(lines[2], selectionBgSGR(t, m)) {
		t.Errorf("line2 is outside the selection but carries the bg SGR: %q", lines[2])
	}
}

// TestSelectionEmptyInteriorLinePaintsOneCell (migrated from
// TestByteRangesEmptyInteriorLineGetsNoCell — CONTRACT CHANGE): an EMPTY interior
// line of a multi-line selection now paints exactly ONE styled cell (style.Render(" "))
// so the run stays solid through the blank line, removing the old byteRanges gap. The
// surrounding non-empty lines carry their styled substrings; the copy path
// (selectedText) still preserves the empty line as "\n" (asserted separately).
func TestSelectionEmptyInteriorLinePaintsOneCell(t *testing.T) {
	m, _ := selModel(t)
	content := "alpha\n\nbeta"
	// Select from line0col0 through line2col4 — spanning the empty middle line.
	sel := selection{active: true, anchorL: 0, anchorC: 0, headL: 2, headC: 4}
	lines := styledLines(m, content, sel)
	open := openSelectionSGR(t, m)

	if !strings.Contains(lines[0], open+"alpha") {
		t.Errorf("line0 want %q before %q, got %q", open, "alpha", lines[0])
	}
	if !strings.Contains(lines[2], open+"beta") {
		t.Errorf("line2 want %q before %q, got %q", open, "beta", lines[2])
	}
	// The empty interior line now carries exactly the style.Render(" ") output: the
	// selection bg SGR with a single space glyph (NOT a gap).
	if lines[1] != m.deps.Theme.Style("selection").Render(" ") {
		t.Errorf("empty interior line = %q, want the one-cell painted style %q", lines[1], m.deps.Theme.Style("selection").Render(" "))
	}
	if !strings.Contains(lines[1], selectionBgSGR(t, m)) {
		t.Errorf("empty interior line should carry the selection bg SGR (one painted cell), got %q", lines[1])
	}
}

// TestSelectionInteriorLineGlyphBounded (migrated from TestByteRangesGlyphWidthNotPadded):
// an interior line's highlight stops at its last glyph, NOT the terminal width — the
// styled span ends right after the line's content, not padded to 100 cells (Req 6).
func TestSelectionInteriorLineGlyphBounded(t *testing.T) {
	m, _ := selModel(t)
	content := "first line\nshort\nlast line here"
	// Select all three lines; the middle line "short" is the interior line.
	sel := selection{active: true, anchorL: 0, anchorC: 0, headL: 2, headC: 14}
	lines := styledLines(m, content, sel)
	open := openSelectionSGR(t, m)

	// The interior line must be the styled "short" with NO trailing padded cells: it
	// equals StyleRanges over exactly the 5-cell content, so stripping ANSI yields
	// "short" with no width pad.
	mid := lines[1]
	if stripped := ansi.Strip(mid); stripped != "short" {
		t.Errorf("interior line stripped = %q, want %q (glyph-bounded, not full-width)", stripped, "short")
	}
	if !strings.Contains(mid, open+"short") {
		t.Errorf("interior line want %q before %q, got %q", open, "short", mid)
	}
}

// TestSelectedTextUnchangedByEmptyLineFix asserts the COPY path still preserves
// the empty middle line (the highlight-render change is additive and must not touch
// selectedText). The empty line survives as a "\n" in the joined payload. It ALSO
// asserts copy over ANSI-styled + wide-rune content yields the correct ansi-stripped
// payload (the copy path is grapheme-column indexed and strips ANSI, independent of
// the styleSelection render path).
func TestSelectedTextUnchangedByEmptyLineFix(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent("alpha\n\nbeta")
	sel := selection{active: true, anchorL: 0, anchorC: 0, headL: 2, headC: 4}
	got := selectedText(m.vp.GetContent(), sel)
	if got != "alpha\n\nbeta" {
		t.Errorf("selectedText = %q, want %q (empty middle line preserved)", got, "alpha\n\nbeta")
	}

	// ANSI + wide-rune copy: a styled line containing CJK wide runes. The selection
	// columns are GRAPHEME columns; selectedText must strip ANSI and slice by grapheme,
	// so selecting "ab中文cd" (6 graphemes) recovers exactly that text.
	styled := m.deps.Theme.Style("toolName").Render("ab中文cd") + "   "
	m.vp.SetContent(styled)
	wide := selection{active: true, anchorL: 0, anchorC: 0, headL: 0, headC: graphemeCount("ab中文cd")}
	if got := selectedText(m.vp.GetContent(), wide); got != "ab中文cd" {
		t.Errorf("wide-rune copy = %q, want %q (ansi stripped, grapheme-sliced)", got, "ab中文cd")
	}
}

// TestPressDragReleaseCopiesSelection confirms a non-empty conversation drag
// copies on release while retaining its selection.
func TestPressDragReleaseCopiesSelection(t *testing.T) {
	m, cb := selModel(t)
	m.vp.SetContent("hello world\nsecond line\nthird row")
	m.vp.SetYOffset(0)
	top := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, top)
	m, _ = motionMouse(m, 11, top)
	m, cmd := releaseMouse(m, 11, top)

	leaves := collectLeaves(cmd)
	osc52, shellWrites := 0, 0
	for _, msg := range leaves {
		if _, ok := msg.(shellWriteResultMsg); ok {
			shellWrites++
		} else {
			osc52++
		}
	}
	if osc52 != 1 || shellWrites != 1 {
		t.Fatalf("copy transports = OSC52:%d shell:%d, want one each", osc52, shellWrites)
	}
	payload, ok := osc52Payload(leaves)
	if !ok || payload != "hello world" {
		t.Fatalf("release OSC52 payload = %q, ok=%v", payload, ok)
	}
	for _, msg := range leaves {
		mm, _ := m.Update(msg)
		m = mm.(Model)
	}
	if !m.sel.active || selectedText(m.vp.GetContent(), m.sel) != "hello world" {
		t.Fatalf("selection = %q, want retained hello world", selectedText(m.vp.GetContent(), m.sel))
	}
	if len(cb.wrote) != 1 || string(cb.wrote[0]) != "hello world" {
		t.Fatalf("release shell clipboard writes = %q", cb.wrote)
	}
}

// TestRightClickCopiesExistingSelection: with an active selection, a RIGHT click
// copies it through the same path (Req 4).
func TestRightClickCopiesExistingSelection(t *testing.T) {
	m, cb := selModel(t)
	m.vp.SetContent("hello world\nsecond line")
	m.vp.SetYOffset(0)
	top := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, top)
	m, _ = motionMouse(m, 5, top) // select "hello"
	// Right click (no release yet) copies the current selection.
	m, cmd := pressMouse(m, tea.MouseRight, 40, top)

	payload, ok := osc52Payload(collectLeaves(cmd))
	if !ok || payload != "hello" {
		t.Errorf("right-click OSC52 payload = %q ok=%v, want %q", payload, ok, "hello")
	}
	if len(cb.wrote) != 1 || string(cb.wrote[0]) != "hello" {
		t.Errorf("right-click shell-write not invoked: %v", cb.wrote)
	}
}

// TestRightClickDoesNotAdvanceClickCount: a right-click is outside the multi-click
// sequence (Req 10) — it must NOT advance clickCount/clickGen, yet it still copies
// the existing selection.
func TestRightClickDoesNotAdvanceClickCount(t *testing.T) {
	m, _, y := convModel(t, "hello world here")

	// Build a REAL (non-empty) selection via press+drag so the right-click has
	// something to copy. The drag invalidates the multi-click sequence (clickCount→0)
	// while leaving an active span. The press/motion X are offset by the assistant body
	// margin so the span covers "hello" (the real text), not the leading margin.
	m, _ = pressMouse(m, tea.MouseLeft, convBodyXOffset, y)
	m, _ = motionMouse(m, convBodyXOffset+5, y) // select "hello"
	if !m.sel.active || m.sel.empty() {
		t.Fatal("precondition: press+drag should leave a non-empty selection")
	}
	// Re-arm the count to a known value WITHOUT disturbing the span (a left press
	// would collapse it to a zero-width anchor). The right-click must leave both the
	// count and the generation exactly as it found them.
	m.clickCount = 1
	m.clickGen = 7
	beforeCount, beforeGen := m.clickCount, m.clickGen

	m, cmd := pressMouse(m, tea.MouseRight, 40, y)

	if m.clickCount != beforeCount {
		t.Errorf("right-click advanced clickCount %d → %d, want it unchanged", beforeCount, m.clickCount)
	}
	if m.clickGen != beforeGen {
		t.Errorf("right-click bumped clickGen %d → %d, want it unchanged", beforeGen, m.clickGen)
	}
	if payload, ok := osc52Payload(collectLeaves(cmd)); !ok || payload != "hello" {
		t.Errorf("right-click should copy the existing selection, got payload %q ok=%v", payload, ok)
	}
}

// TestEscClearsSelectionThenRestoresSemantics: esc with an active selection clears
// it and does NOT cancel a run; a SECOND esc (no selection) falls through to today's
// esc meaning (clear input / cancel) — Req 5.
func TestEscClearsSelectionThenRestoresSemantics(t *testing.T) {
	m, _ := selModel(t)
	m.phase = phaseRunning // so "cancel run" is the would-be esc meaning
	top := convTopRow(m)
	m.vp.SetContent("hello world\nsecond line")
	m.vp.SetYOffset(0)

	m, _ = pressMouse(m, tea.MouseLeft, 0, top)
	m, _ = motionMouse(m, 5, top)
	if !m.sel.active {
		t.Fatal("precondition: a selection should be active")
	}
	// PRECONDITION: the selection is actually RENDERED (the bg SGR splice is in the
	// frame) — so the post-esc absence assertion is meaningful, not vacuous.
	if !strings.Contains(m.vp.View(), selectionBgSGR(t, m)) {
		t.Fatal("precondition: the active selection should be rendered (bg SGR present) before esc")
	}

	// First esc: clears the selection, consumes the key, does NOT cancel the run.
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.sel.active {
		t.Error("first esc should clear the active selection")
	}
	if cmd != nil {
		t.Error("esc that only clears a selection should issue no cancel command")
	}
	if m.phase != phaseRunning {
		t.Error("esc clearing a selection must not cancel the run")
	}
	// FRAME-LEVEL: the esc clear path's follow-up refreshView actually WIPED the
	// spliced highlight — no stuck selection block lingers in the rendered frame.
	if strings.Contains(m.vp.View(), selectionBgSGR(t, m)) {
		t.Error("esc-clear must leave no selection highlight (bg SGR) in the rendered frame")
	}

	// Second esc (no selection, empty input, no queue): today's esc cancels the run.
	m, cmd = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Error("second esc (no selection) should fall through to today's cancel semantics")
	}
}

// TestSelectionBlockedUnderOverlay: a left press while an overlay owns the body
// starts NO selection (Req 8).
func TestSelectionBlockedUnderOverlay(t *testing.T) {
	m, _ := selModel(t)
	m.showHelp = true // help owns the body
	top := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, top)
	if m.sel.active {
		t.Error("a press while an overlay owns the body must not start a selection")
	}
}

// TestNoAltScreenDisablesSelection: in --inline/--no-alt-screen mode no selection is
// ever created (Req 7).
func TestNoAltScreenDisablesSelection(t *testing.T) {
	m, _ := selModel(t)
	m.deps.NoAltScreen = true
	top := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, top)
	if m.sel.active {
		t.Error("NoAltScreen must disable selection")
	}
	if selectable(m) {
		t.Error("selectable should be false under NoAltScreen")
	}
}

// TestNoMouseDisablesSelection: the --no-mouse escape hatch leaves the alt screen
// up but disables in-app selection so the terminal's native selection works — a
// left-press starts nothing and selectable() is false.
func TestNoMouseDisablesSelection(t *testing.T) {
	m, _ := selModel(t)
	m.deps.NoMouse = true
	top := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, top)
	if m.sel.active {
		t.Error("NoMouse must disable in-app selection")
	}
	if selectable(m) {
		t.Error("selectable should be false under NoMouse")
	}
}

// TestNoMouseLeavesMouseUncaptured: View must NOT request mouse capture under
// --no-mouse (so the terminal keeps the mouse for native selection), while the
// default alt-screen path DOES capture it (MouseModeCellMotion) for wheel + in-app
// drag-select. This is the load-bearing toggle behind the escape hatch.
func TestNoMouseLeavesMouseUncaptured(t *testing.T) {
	m, _ := selModel(t)

	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Errorf("default alt screen: MouseMode = %v, want MouseModeCellMotion (mouse captured)", got)
	}

	m.deps.NoMouse = true
	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Errorf("--no-mouse: MouseMode = %v, want MouseModeNone (uncaptured for native selection)", got)
	}

	// --inline already leaves the mouse uncaptured regardless of NoMouse.
	m.deps.NoMouse = false
	m.deps.NoAltScreen = true
	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Errorf("--inline: MouseMode = %v, want MouseModeNone", got)
	}
}

// TestSelectionAssumesSoftWrapDisabled guards the load-bearing invariant of the
// screen→content mapping: screenToContent maps a screen row to logical line
// YOffset()+(y-convTop) and a cell X straight to a grapheme column ONLY because the
// viewport does not soft-wrap (glamour hard-wraps the content to the width instead).
// If anyone enables viewport SoftWrap, that one-line offset silently mis-maps every
// click — this test fails loudly to point them at selection.go's mapping.
func TestSelectionAssumesSoftWrapDisabled(t *testing.T) {
	m, _ := selModel(t)
	if m.vp.SoftWrap {
		t.Fatal("viewport SoftWrap is enabled — the selection screen→content mapping in " +
			"selection.go assumes it is OFF (no wrap-aware walk); re-derive screenToContent/" +
			"graphemeColForCellX for wrapped lines before enabling it")
	}
}

// lineIndexContaining returns the index of the first content line containing sub,
// or -1. Lines are the viewport content split on "\n" (the same basis the
// selection's absolute line indices use).
func lineIndexContaining(content, sub string) int {
	for i, ln := range strings.Split(content, "\n") {
		if strings.Contains(ansi.Strip(ln), sub) {
			return i
		}
	}
	return -1
}

// TestSelectionClearedOnReflowAboveIt: selection identity must not retain a span
// whose copied visible text changes when a reflow introduces new rendered rows.
func TestSelectionClearedOnReflowAboveIt(t *testing.T) {
	cb := &fakeClipboard{}
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.deps.NoAltScreen = false
	m.deps.Clipboard = cb
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 40},
		client.SessionReadyMsg{SessionID: "sess-reflow-1"},
	)
	// A tool block whose body is long enough that ctrl+t (full vs line-capped) changes
	// its rendered height, followed by a UNIQUE assistant marker line BELOW it.
	m.conv.addUser("req")
	m.conv.addTool("t1", "Bash", `{"cmd":"seq 40"}`)
	m.conv.resolveTool("t1", strings.TrimRight(strings.Repeat("toolbodyline\n", 40), "\n"), false)
	const marker = "UNIQUEMARKERZZZ"
	m.conv.appendAssistant(marker + " trailing words here")
	m.phase = phaseIdle
	m.view.mode = followTail
	m.refreshView()

	// Select within the marker line (in the capped render).
	markerLine := lineIndexContaining(m.vp.GetContent(), marker)
	if markerLine < 0 {
		t.Fatal("marker not found in capped content")
	}
	top := convTopRow(m)
	y := top + (markerLine - m.vp.YOffset())
	if y < top || y >= top+m.vp.Height() {
		t.Fatalf("marker line %d not on screen (YOffset=%d top=%d h=%d)", markerLine, m.vp.YOffset(), top, m.vp.Height())
	}
	m, _ = pressMouse(m, tea.MouseLeft, 0, y)
	m, _ = motionMouse(m, 40, y) // wide enough to cover the whole marker line
	if !m.sel.active {
		t.Fatal("expected an active selection on the marker line")
	}
	if !strings.Contains(m.sel.snapshot, marker) {
		t.Fatalf("selection snapshot %q should cover the marker", m.sel.snapshot)
	}

	// Toggle ctrl+t → the tool body expands, shifting the marker DOWN, so line index
	// markerLine now holds a tool-body line instead of the marker.
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	afterIdx := lineIndexContaining(m.vp.GetContent(), marker)
	if afterIdx == markerLine {
		t.Fatalf("test setup did not shift the layout (marker stayed at line %d); ctrl+t must change the tool body height", markerLine)
	}
	if m.sel.active {
		t.Errorf("selection should be CLEARED after a reflow changed the selected text (marker %d → %d)", markerLine, afterIdx)
	}
	if strings.Contains(m.vp.View(), selectionBgSGR(t, m)) {
		t.Error("no selection background highlight should remain after the reflow-clear")
	}
}

// selectionBgSGR returns the truecolor background SGR substring the resolved
// "selection" theme style emits (e.g. "48;2;42;77;69" for aztec's #2A4D45), so a
// test can assert the highlight block's presence/absence without hard-coding the
// hex. It probes the style by rendering a single glyph and extracting the
// background portion of the SGR — the part that survives even when no glyph is
// present.
func selectionBgSGR(t *testing.T, m Model) string {
	t.Helper()
	probe := m.deps.Theme.Style("selection").Render("X")
	idx := strings.Index(probe, "48;")
	if idx < 0 {
		t.Fatalf("selection style has no background SGR: %q", probe)
	}
	// Slice from "48;" up to (and excluding) the SGR terminator 'm'.
	end := strings.IndexByte(probe[idx:], 'm')
	if end < 0 {
		t.Fatalf("malformed selection SGR: %q", probe)
	}
	return probe[idx : idx+end]
}

// TestWheelKeepsSelection: a wheel scroll while a selection exists still scrolls
// (re-derives auto-follow) and does NOT clear the selection (Req 10).
func TestWheelKeepsSelection(t *testing.T) {
	m, _ := selModel(t)
	top := convTopRow(m)
	m, _ = pressMouse(m, tea.MouseLeft, 0, top)
	m, _ = motionMouse(m, 5, top)
	if !m.sel.active {
		t.Fatal("precondition: a selection should be active")
	}
	beforeMode := m.view.mode

	m, _ = pressKey(m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})

	if !m.sel.active {
		t.Error("a wheel scroll must NOT clear an active selection")
	}
	if m.view.mode == beforeMode && beforeMode == followTail {
		t.Error("a wheel-up should have unstuck the view (auto-follow re-derived)")
	}
}

// TestSelectionHighlightWrapsSelectedText is the STRONG visibility guard: it
// asserts the selection background SGR immediately WRAPS the selected glyphs after
// the app-owned styleSelection splice — not an empty open+reset pair around unstyled
// text. A presence-only check ("does the SGR byte appear?") would pass on a broken
// splice that emitted the SGR with the text outside the block; this requires the
// selected text to sit immediately after the open SGR. It runs styleSelection
// directly (a pure per-line splice) so a refreshView conversation re-render can't
// clobber the controlled content. B1 (TestSelectionHighlightOnStyledLaterLine) is
// the companion guard that exercises the full styled render path through View().
func TestSelectionHighlightWrapsSelectedText(t *testing.T) {
	m, _ := selModel(t)
	// Controlled single-line content: the selected span has no newline, so a correct
	// highlight must wrap it contiguously.
	content := "alpha bravo charlie delta echo"
	// Select "bravo" — cols [6,11), no trailing space, so the wrapped span equals the
	// (trailing-trimmed) selectedText.
	sel := selection{active: true, anchorL: 0, anchorC: 6, headL: 0, headC: 11}

	want := selectedText(content, sel)
	if want != "bravo" {
		t.Fatalf("setup: selected text = %q, want %q", want, "bravo")
	}
	openSGR := openSelectionSGR(t, m)
	styled := styleSelection(content, sel, m.deps.Theme.Style("selection"))

	if !strings.Contains(styled, openSGR+want) {
		t.Errorf("selection highlight does not wrap the selected text:\n want open SGR %q immediately followed by %q in the styled content %q", openSGR, want, styled)
	}
}

// TestSelectionSurvivesStreamingDelta: a selection's logical anchors are unchanged
// by a streamed delta + frame flush, and the viewport highlights stay applied; the
// stuck/AtBottom state is unaffected (Req 2).
func TestSelectionSurvivesStreamingDelta(t *testing.T) {
	m, _ := selModel(t)
	top := convTopRow(m)
	// Scroll up so the selection is on a stable, non-tail line and a delta won't
	// re-pin the view.
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	m, _ = pressMouse(m, tea.MouseLeft, 0, top)
	m, _ = motionMouse(m, 10, top)
	anchorL, anchorC := m.sel.anchorL, m.sel.anchorC
	headL, headC := m.sel.headL, m.sel.headC
	beforeMode := m.view.mode

	m.phase = phaseRunning
	m = applyAll(m,
		client.AssistantDeltaMsg{Turn: 1, Text: strings.Repeat("appended\n", 5)},
		renderTickMsg{},
	)

	if m.sel.anchorL != anchorL || m.sel.anchorC != anchorC || m.sel.headL != headL || m.sel.headC != headC {
		t.Errorf("streaming delta moved the selection anchors: (%d,%d)-(%d,%d) → (%d,%d)-(%d,%d)",
			anchorL, anchorC, headL, headC, m.sel.anchorL, m.sel.anchorC, m.sel.headL, m.sel.headC)
	}
	if !m.sel.active {
		t.Error("selection should still be active after a streaming delta")
	}
	if m.view.mode != beforeMode {
		t.Errorf("streaming delta changed stuck: %v → %v", beforeMode, m.view.mode)
	}
	// The selection is still derivable after the delta: its visible text is non-empty
	// and still equals the identity snapshot (the snapshot reflow-clear in refreshView
	// did NOT fire, so the selection survived the append below it).
	payload := selectedText(m.vp.GetContent(), m.sel)
	if payload == "" {
		t.Error("selection should still carry visible text after a delta")
	}
	if payload != m.sel.snapshot {
		t.Errorf("selectedText %q diverged from snapshot %q after a delta (should be untouched by an append below)", payload, m.sel.snapshot)
	}
	// Stronger: the VIEWPORT itself must still carry the highlight after the
	// SetContent → styleSelection splice ran in refreshView. Assert the rendered view
	// carries the selection BACKGROUND SGR (the "selection" theme style is a solid
	// block, NOT reverse video).
	selSGR := selectionBgSGR(t, m)
	if !strings.Contains(m.vp.View(), selSGR) {
		t.Errorf("after a streaming delta the rendered viewport should still carry the selection background SGR %q", selSGR)
	}
	if strings.Contains(m.vp.View(), "\x1b[7m") {
		t.Error("the selection highlight must be a solid block, not reverse video")
	}
	// The styleSelection splice over the current content wraps the selected span: the
	// first selected line's text sits immediately after the open SGR in the rendered
	// view (closing the splice-against-current-content path for real).
	open := openSelectionSGR(t, m)
	wantFirstLine := strings.SplitN(payload, "\n", 2)[0]
	if wantFirstLine != "" && !strings.Contains(m.vp.View(), open+wantFirstLine) {
		t.Errorf("styled view should wrap the first selected line %q immediately after the open SGR", wantFirstLine)
	}
}

// TestEmptyClickNoCopy: a plain click (press+release at the same cell, no drag)
// selects nothing and copies nothing.
func TestEmptyClickNoCopy(t *testing.T) {
	m, cb := selModel(t)
	top := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 3, top)
	m, cmd := releaseMouse(m, 3, top)

	if m.sel.active {
		t.Error("an empty click should leave no active selection")
	}
	if cmd != nil {
		t.Error("an empty click should copy nothing (no command)")
	}
	if len(cb.wrote) != 0 {
		t.Errorf("an empty click should not invoke the shell write: %v", cb.wrote)
	}
}

// TestOpeningOverlayClearsSelection: opening an overlay (help) mid-selection clears
// it (Req 8 — the mid-selection clear).
func TestOpeningOverlayClearsSelection(t *testing.T) {
	m, _ := selModel(t)
	top := convTopRow(m)
	m, _ = pressMouse(m, tea.MouseLeft, 0, top)
	m, _ = motionMouse(m, 5, top)
	if !m.sel.active {
		t.Fatal("precondition: a selection should be active")
	}
	// PRECONDITION: the selection is actually RENDERED (the bg SGR splice is in the
	// frame) so the post-clear absence assertion is meaningful.
	if !strings.Contains(m.vp.View(), selectionBgSGR(t, m)) {
		t.Fatal("precondition: the active selection should be rendered (bg SGR present) before the overlay opens")
	}

	// "?" on an empty prompt opens help.
	m, _ = pressKey(m, tea.KeyPressMsg{Code: '?'})
	if !m.showHelp {
		t.Fatal("precondition: ? should open help")
	}
	if m.sel.active {
		t.Error("opening an overlay mid-selection should clear the selection")
	}
	// FRAME-LEVEL: opening the overlay routes through the Update non-selectable
	// chokepoint, whose explicit refreshView must repaint the UNSTYLED content — no
	// stuck selection block lingers in the conversation viewport frame.
	if strings.Contains(m.vp.View(), selectionBgSGR(t, m)) {
		t.Error("opening an overlay must leave no selection highlight (bg SGR) in the rendered frame")
	}
}

// hlContent is a known multi-line viewport content with enough lines to overflow
// the viewport so edge-autoscroll has room to move. Each line is "row NN" so a
// scrolled line is identifiable by its number.
func hlContent(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "row %02d", i)
	}
	return b.String()
}

// TestDragNearTopEdgeScrollsUp: a drag whose motion lands at/above the top edge
// scrolls the viewport UP by a line and extends the selection head to the
// newly-revealed top content line. Returns a re-arming tick.
func TestDragNearTopEdgeScrollsUp(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent(hlContent(200))
	m.vp.SetYOffset(50) // mid-content: room to scroll both ways
	top := convTopRow(m)

	// Anchor somewhere in the middle of the visible region.
	m, _ = pressMouse(m, tea.MouseLeft, 0, top+3)
	beforeOff := m.vp.YOffset()

	// Motion AT the top edge → scroll up.
	m, cmd := motionMouse(m, 0, top)

	if m.vp.YOffset() != beforeOff-1 {
		t.Errorf("YOffset = %d, want one line up (%d)", m.vp.YOffset(), beforeOff-1)
	}
	if m.sel.autoScroll != scrollUp {
		t.Errorf("autoScroll = %v, want scrollUp", m.sel.autoScroll)
	}
	if m.sel.headL != m.vp.YOffset() {
		t.Errorf("head line = %d, want the new top content line %d", m.sel.headL, m.vp.YOffset())
	}
	if cmd == nil {
		t.Error("an armed edge-autoscroll should return a re-arming tick command")
	}
	// An upward scroll is an explicit user scroll → unstick auto-follow.
	if m.view.mode == followTail {
		t.Error("edge-autoscroll up should unstick auto-follow")
	}
}

// TestDragNearBottomEdgeScrollsDown: a drag at/below the bottom edge scrolls DOWN a
// line and extends the head to the new bottom visible content line.
func TestDragNearBottomEdgeScrollsDown(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent(hlContent(200))
	m.vp.SetYOffset(50)
	top := convTopRow(m)
	bottom := top + m.vp.Height() - 1

	m, _ = pressMouse(m, tea.MouseLeft, 0, top+3)
	beforeOff := m.vp.YOffset()

	m, cmd := motionMouse(m, 0, bottom)

	if m.vp.YOffset() != beforeOff+1 {
		t.Errorf("YOffset = %d, want one line down (%d)", m.vp.YOffset(), beforeOff+1)
	}
	if m.sel.autoScroll != scrollDown {
		t.Errorf("autoScroll = %v, want scrollDown", m.sel.autoScroll)
	}
	wantHead := m.vp.YOffset() + m.vp.Height() - 1
	if m.sel.headL != wantHead {
		t.Errorf("head line = %d, want the new bottom visible content line %d", m.sel.headL, wantHead)
	}
	if cmd == nil {
		t.Error("an armed edge-autoscroll should return a re-arming tick command")
	}
}

// TestAutoScrollTickContinues: an armed autoScroll tick scrolls one more line and
// re-arms (the self-re-arming continuous scroll while held at the edge).
func TestAutoScrollTickContinues(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent(hlContent(200))
	m.vp.SetYOffset(50)
	top := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, top+3)
	m, _ = motionMouse(m, 0, top) // arm scrollUp
	armedOff := m.vp.YOffset()

	mm, cmd := m.Update(autoScrollMsg{})
	m = mm.(Model)

	if m.vp.YOffset() != armedOff-1 {
		t.Errorf("tick YOffset = %d, want one more line up (%d)", m.vp.YOffset(), armedOff-1)
	}
	if m.sel.autoScroll != scrollUp {
		t.Error("tick should keep autoScroll armed while still at the edge")
	}
	if cmd == nil {
		t.Error("the tick should re-arm itself while still scrolling")
	}
}

// TestAutoScrollStopsOnRelease: a release disarms autoScroll (a pending tick
// no-ops) and finalises the selection.
func TestAutoScrollStopsOnRelease(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent(hlContent(200))
	m.vp.SetYOffset(50)
	top := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, top+3)
	m, _ = motionMouse(m, 0, top) // arm scrollUp
	if m.sel.autoScroll != scrollUp {
		t.Fatal("precondition: autoScroll should be armed")
	}

	m, _ = releaseMouse(m, 0, top)

	// After release the selection finalised (active=false on a real selection it
	// copied) OR cleared; either way a pending tick must no-op. Drive the stale tick.
	mm, cmd := m.Update(autoScrollMsg{})
	m = mm.(Model)
	if m.sel.autoScroll != scrollNone {
		t.Errorf("release should disarm autoScroll, got %v", m.sel.autoScroll)
	}
	if cmd != nil {
		t.Error("a stale autoScroll tick after release must not re-arm")
	}
}

// TestAutoScrollStopsOnMotionBackInside: moving the pointer back inside the region
// disarms autoScroll so the tick no-ops.
func TestAutoScrollStopsOnMotionBackInside(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent(hlContent(200))
	m.vp.SetYOffset(50)
	top := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, top+3)
	m, _ = motionMouse(m, 0, top) // arm scrollUp
	if m.sel.autoScroll != scrollUp {
		t.Fatal("precondition: autoScroll should be armed")
	}

	// Motion back inside the region (not at an edge) disarms.
	m, _ = motionMouse(m, 2, top+5)
	if m.sel.autoScroll != scrollNone {
		t.Errorf("motion back inside should disarm autoScroll, got %v", m.sel.autoScroll)
	}

	mm, cmd := m.Update(autoScrollMsg{})
	m = mm.(Model)
	if cmd != nil {
		t.Error("a stale tick after motion-back-inside must not re-arm")
	}
}

// TestAutoScrollStopsAtContentTop: at the actual content top, an up-edge drag does
// NOT spin a tick (YOffset can't move further) — autoScroll stays disarmed.
func TestAutoScrollStopsAtContentTop(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent(hlContent(200))
	m.vp.GotoTop() // YOffset == 0, can't scroll up
	top := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, top+3)
	m, cmd := motionMouse(m, 0, top) // up-edge, but already at content top

	if m.vp.YOffset() != 0 {
		t.Errorf("YOffset = %d, want 0 (already at content top)", m.vp.YOffset())
	}
	if m.sel.autoScroll != scrollNone {
		t.Errorf("at content top autoScroll should not arm, got %v", m.sel.autoScroll)
	}
	if cmd != nil {
		t.Error("at content top no re-arming tick should be returned")
	}
}

// TestAutoScrollStopsAtContentBottom: at the actual content bottom, a down-edge
// drag does NOT spin a tick.
func TestAutoScrollStopsAtContentBottom(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent(hlContent(200))
	m.vp.GotoBottom() // can't scroll down further
	top := convTopRow(m)
	bottom := top + m.vp.Height() - 1

	m, _ = pressMouse(m, tea.MouseLeft, 0, top+1)
	m, cmd := motionMouse(m, 0, bottom)

	if m.sel.autoScroll != scrollNone {
		t.Errorf("at content bottom autoScroll should not arm, got %v", m.sel.autoScroll)
	}
	if cmd != nil {
		t.Error("at content bottom no re-arming tick should be returned")
	}
}

// TestAutoScrollClearedByEsc: esc during an armed edge-autoscroll clears the
// selection AND disarms the tick.
func TestAutoScrollClearedByEsc(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent(hlContent(200))
	m.vp.SetYOffset(50)
	top := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, top+3)
	m, _ = motionMouse(m, 0, top)
	if m.sel.autoScroll != scrollUp {
		t.Fatal("precondition: autoScroll armed")
	}

	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.sel.active || m.sel.autoScroll != scrollNone {
		t.Errorf("esc should clear the selection and disarm autoScroll: active=%v dir=%v", m.sel.active, m.sel.autoScroll)
	}
	mm, cmd := m.Update(autoScrollMsg{})
	m = mm.(Model)
	if cmd != nil {
		t.Error("a stale tick after esc-clear must not re-arm")
	}
}

// TestAutoScrollArmIsSingleFlight: the FIRST edge motion arms a tick loop (returns
// a tick cmd); a SECOND edge motion in the SAME direction must NOT spawn a second
// concurrent loop (returns nil) — terminal drag reporting emits a motion per cell,
// so re-arming each one would multiply the autoscroll speed.
func TestAutoScrollArmIsSingleFlight(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent(hlContent(200))
	m.vp.SetYOffset(50)
	top := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, top+3)

	// First edge motion: arms the loop and returns a tick.
	m, cmd1 := motionMouse(m, 0, top)
	if m.sel.autoScroll != scrollUp {
		t.Fatalf("first edge motion should arm scrollUp, got %v", m.sel.autoScroll)
	}
	if cmd1 == nil {
		t.Fatal("first edge motion should return a tick command (arms the loop)")
	}

	// Second edge motion, same direction: must NOT spawn a second loop.
	m, cmd2 := motionMouse(m, 0, top)
	if m.sel.autoScroll != scrollUp {
		t.Errorf("second edge motion should keep scrollUp, got %v", m.sel.autoScroll)
	}
	if cmd2 != nil {
		t.Error("a second same-direction edge motion must NOT spawn a second tick loop (single-flight)")
	}
}

// TestAutoScrollDirectionFlipNoSecondLoop: arming scrollUp then dragging to the
// opposite (bottom) edge flips the direction to scrollDown WITHOUT spawning a
// second tick loop — the single running loop reads the new direction itself.
func TestAutoScrollDirectionFlipNoSecondLoop(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent(hlContent(200))
	m.vp.SetYOffset(50)
	top := convTopRow(m)
	bottom := top + m.vp.Height() - 1

	m, _ = pressMouse(m, tea.MouseLeft, 0, top+3)
	m, cmd1 := motionMouse(m, 0, top) // arm scrollUp (spawns the loop)
	if m.sel.autoScroll != scrollUp || cmd1 == nil {
		t.Fatalf("precondition: scrollUp armed with a tick (dir=%v cmd=%v)", m.sel.autoScroll, cmd1)
	}

	// Flip to the bottom edge: direction changes, but no second loop is spawned.
	m, cmd2 := motionMouse(m, 0, bottom)
	if m.sel.autoScroll != scrollDown {
		t.Errorf("opposite-edge motion should flip autoScroll to scrollDown, got %v", m.sel.autoScroll)
	}
	if cmd2 != nil {
		t.Error("a direction flip must NOT spawn a second tick loop (the existing loop handles it)")
	}
}

// driveAutoScroll feeds one autoScrollMsg tick through Update and returns the new
// model plus the re-arm command (nil once the loop disarms). Pure-reducer, no clock.
func driveAutoScroll(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	mm, cmd := m.Update(autoScrollMsg{})
	return mm.(Model), cmd
}

// TestRampToLines pins the pure acceleration curve directly: gentle first contact
// (ramp 0 → 1 line), a Fibonacci-ish ramp, then saturation at the cap. This is the
// deterministic kernel the per-tick deltas are derived from.
func TestRampToLines(t *testing.T) {
	// onAutoScroll increments the ramp THEN scrolls, so ramp=1 is the first tick.
	want := []int{1, 1, 2, 3, 5, 8, 10, 10, 10, 10}
	for r, w := range want {
		if got := rampToLines(r); got != w {
			t.Errorf("rampToLines(%d) = %d, want %d", r, got, w)
		}
	}
	if got := rampToLines(100); got != maxAutoScrollLines {
		t.Errorf("rampToLines(100) = %d, want cap %d", got, maxAutoScrollLines)
	}
}

// TestAutoScrollFirstTickIsOneLine: the FIRST held tick after arming scrolls exactly
// one line (gentle first contact). rampToLines(1)==1.
func TestAutoScrollFirstTickIsOneLine(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent(hlContent(200))
	m.vp.SetYOffset(50)
	top := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, top+3)
	m, _ = motionMouse(m, 0, top) // arm scrollUp (already scrolled 1 via arm)
	armedOff := m.vp.YOffset()

	m, cmd := driveAutoScroll(t, m)
	if m.vp.YOffset() != armedOff-1 {
		t.Errorf("first tick YOffset = %d, want one line up (%d)", m.vp.YOffset(), armedOff-1)
	}
	if cmd == nil {
		t.Error("first tick should re-arm while still scrolling")
	}
}

// TestAutoScrollAccelerates: a sustained hold ramps the lines-per-tick along the
// curve. Observed deltas are 1,2,3,5,8,10 (ramp 1..6). Fails if the curve is wrong
// or absent (a flat 1-line-per-tick would diverge at tick 2).
func TestAutoScrollAccelerates(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent(hlContent(2000)) // tall enough not to hit the top before saturating
	m.vp.SetYOffset(1000)
	top := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, top+3)
	m, _ = motionMouse(m, 0, top) // arm scrollUp
	armedOff := m.vp.YOffset()

	// Cumulative offsets for deltas 1,2,3,5,8,10 scrolling UP.
	deltas := []int{1, 2, 3, 5, 8, 10}
	want := armedOff
	for i, d := range deltas {
		var cmd tea.Cmd
		m, cmd = driveAutoScroll(t, m)
		want -= d
		if m.vp.YOffset() != want {
			t.Fatalf("tick %d: YOffset = %d, want %d (cumulative delta %d)", i+1, m.vp.YOffset(), want, d)
		}
		if cmd == nil {
			t.Fatalf("tick %d should re-arm while still scrolling", i+1)
		}
	}
}

// TestAutoScrollSpeedCaps: past saturation every tick moves exactly
// maxAutoScrollLines — never more — so one tick can't leap a full screen.
func TestAutoScrollSpeedCaps(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent(hlContent(5000))
	m.vp.SetYOffset(2500)
	top := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, top+3)
	m, _ = motionMouse(m, 0, top) // arm scrollDown? no — top edge → scrollUp

	for i := 0; i < 12; i++ {
		before := m.vp.YOffset()
		var cmd tea.Cmd
		m, cmd = driveAutoScroll(t, m)
		moved := before - m.vp.YOffset() // scrolling up: YOffset decreases
		if moved > maxAutoScrollLines {
			t.Fatalf("tick %d moved %d lines, exceeds cap %d", i+1, moved, maxAutoScrollLines)
		}
		if cmd == nil {
			t.Fatalf("tick %d should still be scrolling against tall content", i+1)
		}
	}
	// After ~6 ticks the curve has saturated; the final step must be exactly the cap.
	before := m.vp.YOffset()
	m, _ = driveAutoScroll(t, m)
	if got := before - m.vp.YOffset(); got != maxAutoScrollLines {
		t.Errorf("saturated tick moved %d lines, want exactly cap %d", got, maxAutoScrollLines)
	}
}

// TestAutoScrollNeverOvershootsContentTop: with fewer than maxAutoScrollLines lines
// of room above, a ramped up-tick lands exactly on 0 and the next tick disarms — it
// never scrolls past the content top.
func TestAutoScrollNeverOvershootsContentTop(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent(hlContent(200))
	m.vp.SetYOffset(4) // < maxAutoScrollLines lines above the top
	top := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, top+3)
	m, _ = motionMouse(m, 0, top) // arm scrollUp (scrolls 1 → YOffset 3)

	// Accelerate until the loop disarms; assert YOffset never goes below 0.
	cmd := m.autoScrollCmd()
	for i := 0; cmd != nil && i < 20; i++ {
		m, cmd = driveAutoScroll(t, m)
		if m.vp.YOffset() < 0 {
			t.Fatalf("tick %d overshot content top: YOffset = %d", i+1, m.vp.YOffset())
		}
	}
	if m.vp.YOffset() != 0 {
		t.Errorf("after disarm YOffset = %d, want exactly 0 (content top)", m.vp.YOffset())
	}
	if cmd != nil {
		t.Error("the loop must disarm once it reaches the content top")
	}
	if m.sel.autoScroll != scrollNone {
		t.Errorf("autoScroll = %v, want scrollNone after content top", m.sel.autoScroll)
	}
	if m.sel.autoScrollRamp != 0 {
		t.Errorf("autoScrollRamp = %d, want reset to 0 at content top", m.sel.autoScrollRamp)
	}
}

// TestAutoScrollNeverOvershootsContentBottom: the down-edge mirror — lands exactly on
// the max offset and disarms, never past the content bottom.
func TestAutoScrollNeverOvershootsContentBottom(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent(hlContent(200))
	// Discover the max YOffset, then back off a few lines so a ramped tick would
	// overshoot if it weren't clamped.
	m.vp.GotoBottom()
	maxOff := m.vp.YOffset()
	m.vp.SetYOffset(maxOff - 4)
	top := convTopRow(m)
	bottom := top + m.vp.Height() - 1

	m, _ = pressMouse(m, tea.MouseLeft, 0, top+1)
	m, _ = motionMouse(m, 0, bottom) // arm scrollDown (scrolls 1)

	cmd := m.autoScrollCmd()
	for i := 0; cmd != nil && i < 20; i++ {
		m, cmd = driveAutoScroll(t, m)
		if m.vp.YOffset() > maxOff {
			t.Fatalf("tick %d overshot content bottom: YOffset = %d (max %d)", i+1, m.vp.YOffset(), maxOff)
		}
	}
	if m.vp.YOffset() != maxOff {
		t.Errorf("after disarm YOffset = %d, want exactly the max offset %d", m.vp.YOffset(), maxOff)
	}
	if cmd != nil {
		t.Error("the loop must disarm once it reaches the content bottom")
	}
	if m.sel.autoScroll != scrollNone {
		t.Errorf("autoScroll = %v, want scrollNone after content bottom", m.sel.autoScroll)
	}
}

// TestAutoScrollRampResetsOnRelease: accelerate, release, re-press + re-arm; the next
// tick scrolls one line again (a fresh hold restarts gentle).
func TestAutoScrollRampResetsOnRelease(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent(hlContent(2000))
	m.vp.SetYOffset(1000)
	top := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, top+3)
	m, _ = motionMouse(m, 0, top) // arm scrollUp
	// Accelerate several ticks.
	for i := 0; i < 4; i++ {
		m, _ = driveAutoScroll(t, m)
	}
	if m.sel.autoScrollRamp == 0 {
		t.Fatal("precondition: ramp should be advanced after accelerating")
	}

	// Release ends the drag; re-press starts a fresh selection (ramp zeroed).
	m, _ = releaseMouse(m, 0, top)
	m, _ = pressMouse(m, tea.MouseLeft, 0, top+3)
	m, _ = motionMouse(m, 0, top) // re-arm scrollUp
	armedOff := m.vp.YOffset()

	m, _ = driveAutoScroll(t, m)
	if got := armedOff - m.vp.YOffset(); got != 1 {
		t.Errorf("first tick after re-arm moved %d lines, want 1 (ramp reset on release)", got)
	}
}

// TestAutoScrollRampResetsOnMotionBackInside: accelerate, move the pointer back
// inside the region, then re-arm at the edge; the next tick scrolls one line again.
func TestAutoScrollRampResetsOnMotionBackInside(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent(hlContent(2000))
	m.vp.SetYOffset(1000)
	top := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, top+3)
	m, _ = motionMouse(m, 0, top) // arm scrollUp
	for i := 0; i < 4; i++ {
		m, _ = driveAutoScroll(t, m)
	}

	// Motion back inside disarms AND resets the ramp.
	m, _ = motionMouse(m, 2, top+5)
	if m.sel.autoScroll != scrollNone {
		t.Fatal("precondition: motion back inside should disarm")
	}
	if m.sel.autoScrollRamp != 0 {
		t.Fatalf("motion back inside should reset the ramp, got %d", m.sel.autoScrollRamp)
	}

	// Re-arm at the edge; the first held tick is gentle again.
	m, _ = motionMouse(m, 0, top)
	armedOff := m.vp.YOffset()
	m, _ = driveAutoScroll(t, m)
	if got := armedOff - m.vp.YOffset(); got != 1 {
		t.Errorf("first tick after re-hold moved %d lines, want 1 (ramp reset inside)", got)
	}
}

// TestAutoScrollRampResetsOnEsc: accelerate, esc-clear, re-select + re-arm; the next
// tick scrolls one line again.
func TestAutoScrollRampResetsOnEsc(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent(hlContent(2000))
	m.vp.SetYOffset(1000)
	top := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, top+3)
	m, _ = motionMouse(m, 0, top) // arm scrollUp
	for i := 0; i < 4; i++ {
		m, _ = driveAutoScroll(t, m)
	}

	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.sel.active || m.sel.autoScrollRamp != 0 {
		t.Fatalf("esc should clear selection and reset ramp: active=%v ramp=%d", m.sel.active, m.sel.autoScrollRamp)
	}

	// Re-select and re-arm; first held tick is gentle.
	m, _ = pressMouse(m, tea.MouseLeft, 0, top+3)
	m, _ = motionMouse(m, 0, top)
	armedOff := m.vp.YOffset()
	m, _ = driveAutoScroll(t, m)
	if got := armedOff - m.vp.YOffset(); got != 1 {
		t.Errorf("first tick after esc+re-arm moved %d lines, want 1 (ramp reset on esc)", got)
	}
}

// TestAutoScrollHeadTracksEdgeAfterMultiLineStep: after a multi-line accelerated
// step the selection head still tracks the scrolled edge at the drag column
// (extendHeadToEdge is step-size-agnostic).
func TestAutoScrollHeadTracksEdgeAfterMultiLineStep(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent(hlContent(2000))
	m.vp.SetYOffset(1000)
	top := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, top+3)
	m, _ = motionMouse(m, 0, top) // arm scrollUp
	// Accelerate to a multi-line step (tick 3 moves 3 lines, well past 1).
	for i := 0; i < 3; i++ {
		m, _ = driveAutoScroll(t, m)
	}
	if m.sel.headL != m.vp.YOffset() {
		t.Errorf("scrollUp head line = %d, want the top content line %d after a multi-line step", m.sel.headL, m.vp.YOffset())
	}

	// Now the down-edge mirror: head tracks the bottom visible line.
	m2, _ := selModel(t)
	m2.vp.SetContent(hlContent(2000))
	m2.vp.SetYOffset(1000)
	top2 := convTopRow(m2)
	bottom2 := top2 + m2.vp.Height() - 1
	m2, _ = pressMouse(m2, tea.MouseLeft, 0, top2+1)
	m2, _ = motionMouse(m2, 0, bottom2) // arm scrollDown
	for i := 0; i < 3; i++ {
		m2, _ = driveAutoScroll(t, m2)
	}
	wantHead := m2.vp.YOffset() + m2.vp.Height() - 1
	if m2.sel.headL != wantHead {
		t.Errorf("scrollDown head line = %d, want the bottom visible line %d after a multi-line step", m2.sel.headL, wantHead)
	}
}

// TestArmAutoScrollDoesNotAdvanceRamp is the load-bearing invariant: armAutoScroll
// must NOT advance the ramp. The ramp lives in onAutoScroll (the held tick), NOT in
// the per-cell motion handler — terminal drag reporting fires a motion event per
// cell at the edge, so if arming advanced the ramp every mouse jiggle would silently
// accelerate. Fire several edge motions WITHOUT any ticks between them, assert the
// ramp stays 0, then drive ONE tick and assert it moved exactly 1 line (ramp was
// still 0 → onAutoScroll advances to 1 → rampToLines(1)==1).
func TestArmAutoScrollDoesNotAdvanceRamp(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent(hlContent(2000))
	m.vp.SetYOffset(1000)
	top := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, top+3)
	// Re-fire the SAME-edge motion several times with NO autoScrollMsg ticks between
	// (the runaway-acceleration scenario): each re-arm scrolls 1 line but must not
	// touch the ramp.
	for i := 0; i < 5; i++ {
		m, _ = motionMouse(m, 0, top)
		if m.sel.autoScroll != scrollUp {
			t.Fatalf("motion %d should keep scrollUp armed, got %v", i+1, m.sel.autoScroll)
		}
		if m.sel.autoScrollRamp != 0 {
			t.Fatalf("motion %d advanced the ramp to %d — armAutoScroll must NOT touch it", i+1, m.sel.autoScrollRamp)
		}
	}

	before := m.vp.YOffset()
	m, _ = driveAutoScroll(t, m)
	if got := before - m.vp.YOffset(); got != 1 {
		t.Errorf("first held tick moved %d lines, want 1 (ramp was still 0)", got)
	}
}

// TestAutoScrollRampPersistsAcrossDirectionFlip pins the DELIBERATE persist-across-
// flip behavior: a direction flip mid-hold (top edge → bottom edge) is a CONTINUATION
// of an active edge-scroll, not a fresh hold, so the ramp is intentionally NOT reset
// — armAutoScroll never touches it and onAutoScroll keeps incrementing across the
// flip. The head re-anchors to the new edge each tick, so there is no desync. (The
// reset seams are release / motion-back-inside / esc / inactive / content-edge — a
// flip is none of those.)
func TestAutoScrollRampPersistsAcrossDirectionFlip(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent(hlContent(2000))
	m.vp.SetYOffset(1000)
	top := convTopRow(m)
	bottom := top + m.vp.Height() - 1

	m, _ = pressMouse(m, tea.MouseLeft, 0, top+3)
	m, _ = motionMouse(m, 0, top) // arm scrollUp
	// Accelerate a few ticks at the top edge: ramp ends at 3 after three ticks.
	for i := 0; i < 3; i++ {
		m, _ = driveAutoScroll(t, m)
	}
	if m.sel.autoScrollRamp != 3 {
		t.Fatalf("precondition: ramp = %d, want 3 after three ticks", m.sel.autoScrollRamp)
	}

	// Flip to the bottom edge: continuation, NOT a reset. The ramp must persist.
	m, _ = motionMouse(m, 0, bottom)
	if m.sel.autoScroll != scrollDown {
		t.Fatalf("flip should arm scrollDown, got %v", m.sel.autoScroll)
	}
	if m.sel.autoScrollRamp != 3 {
		t.Errorf("flip reset the ramp to %d — a mid-hold direction flip must NOT reset (it's a continuation)", m.sel.autoScrollRamp)
	}

	// The next tick continues the ramp (ramp 3→4 → rampToLines(4)==5), proving it did
	// NOT restart at 1.
	before := m.vp.YOffset()
	m, _ = driveAutoScroll(t, m)
	if got := m.vp.YOffset() - before; got != rampToLines(4) {
		t.Errorf("post-flip tick moved %d lines down, want the continued-ramp value %d (not a reset-to-1)", got, rampToLines(4))
	}
	if rampToLines(4) == 1 {
		t.Fatal("test premise broken: rampToLines(4) should be > 1")
	}
}

// nonSelectable is one row of the table-driven overlay/mode gate test: a mutator
// that drives the model into a state where selectable() is false.
type nonSelectableCase struct {
	name  string
	enter func(m *Model)
}

// TestSelectableGateBlocksAndClears is the TABLE-driven cover of the selectable()
// gate (Req 8): for each non-selectable state, (a) a left-press starts NO selection,
// and (b) an already-active selection is cleared via the Update chokepoint. The
// permission-ask case (the most likely real mid-drag interruption) is included.
func TestSelectableGateBlocksAndClears(t *testing.T) {
	cases := []nonSelectableCase{
		{"mcpOverlay", func(m *Model) { m.modal = &mcpState{view: mcpPanel} }},
		{"modelsOverlay", func(m *Model) { m.modal = &modelsState{view: modelsPanel} }},
		{"worktreesOverlay", func(m *Model) { m.worktrees.view = worktreesPanel }},
		{"soulOverlay", func(m *Model) { m.modal = &soulState{view: soulPanel} }},
		{"help", func(m *Model) { m.showHelp = true }},
		{"awaitingApproval", func(m *Model) { m.phase = phaseAwaitingApproval }},
		{"fatal", func(m *Model) { m.phase = phaseFatal }},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/press-blocked", func(t *testing.T) {
			m, _ := selModel(t)
			tc.enter(&m)
			if selectable(m) {
				t.Fatalf("precondition: %s should be non-selectable", tc.name)
			}
			top := convTopRow(m)
			m, _ = pressMouse(m, tea.MouseLeft, 0, top)
			if m.sel.active {
				t.Errorf("%s: a left-press must not start a selection while non-selectable", tc.name)
			}
		})
		t.Run(tc.name+"/active-cleared", func(t *testing.T) {
			m, _ := selModel(t)
			top := convTopRow(m)
			// Start a real selection while still selectable.
			m, _ = pressMouse(m, tea.MouseLeft, 0, top)
			m, _ = motionMouse(m, 5, top)
			if !m.sel.active {
				t.Fatal("precondition: selection should be active")
			}
			// Transition to the non-selectable state via a real message through Update
			// so the chokepoint runs. We drive the state mutator, then send a benign
			// message (a renderTick) so Update's post-reduce chokepoint sees the new
			// state. (In production the SAME message that opens the overlay carries the
			// transition; here we split it to keep the table mutator simple.)
			tc.enter(&m)
			mm, _ := m.Update(renderTickMsg{})
			m = mm.(Model)
			if m.sel.active {
				t.Errorf("%s: an active selection must be cleared once the body is non-selectable", tc.name)
			}
		})
	}
}

// TestKeyboardScrollKeepsSelection: pgup/pgdn/home/end must NOT clear an active
// selection (a separate code path from the wheel), and the highlight stays
// derivable after each (Req 2, keyboard path).
func TestKeyboardScrollKeepsSelection(t *testing.T) {
	keys := []struct {
		name string
		code tea.KeyPressMsg
	}{
		{"pgup", tea.KeyPressMsg{Code: tea.KeyPgUp}},
		{"pgdn", tea.KeyPressMsg{Code: tea.KeyPgDown}},
		{"home", tea.KeyPressMsg{Code: tea.KeyHome}},
		{"end", tea.KeyPressMsg{Code: tea.KeyEnd}},
	}
	for _, k := range keys {
		t.Run(k.name, func(t *testing.T) {
			m, _ := selModel(t)
			top := convTopRow(m)
			m, _ = pressMouse(m, tea.MouseLeft, 0, top)
			m, _ = motionMouse(m, 10, top)
			if !m.sel.active {
				t.Fatal("precondition: selection active")
			}

			m, _ = pressKey(m, k.code)

			if !m.sel.active {
				t.Errorf("%s scroll must not clear the selection", k.name)
			}
			// The highlight is content-level (styleSelection), so it stays rendered even
			// if the scroll moved the selected line off-screen — assert against the
			// styled content, not the visible viewport view (visibility-independent, like
			// the old byteRanges check).
			styled := styleSelection(m.vp.GetContent(), m.sel, m.deps.Theme.Style("selection"))
			if !strings.Contains(styled, selectionBgSGR(t, m)) {
				t.Errorf("%s scroll left the selection highlight unrendered (bg SGR absent in styled content)", k.name)
			}
		})
	}
}

// TestCtrlVPasteWithActiveSelection is a no-regression guard: a ctrl+v paste while a
// selection is active behaves exactly as without one — the text is inserted and the
// esc-guard (Cancel-only) does not misfire on the paste path.
func TestCtrlVPasteWithActiveSelection(t *testing.T) {
	cb := &fakeClipboard{mime: "text/plain", data: []byte("pasted text")}
	m, _ := newClipboardModel(t, client.Capabilities{Image: true}, cb)
	m.deps.NoAltScreen = false
	m.conv.addUser("x")
	m.conv.appendAssistant(strings.Repeat("line of text\n", 60))
	m.refreshView()
	top := convTopRow(m)

	// Establish an active selection.
	m, _ = pressMouse(m, tea.MouseLeft, 0, top)
	m, _ = motionMouse(m, 5, top)
	if !m.sel.active {
		t.Fatal("precondition: selection active")
	}

	m = pressCtrlV(t, m)

	if !strings.Contains(m.prompt.Value(), "pasted text") {
		t.Errorf("ctrl+v should insert the pasted text regardless of an active selection, got %q", m.prompt.Value())
	}
}

// convModel builds a selectable model whose conversation renders the given plain
// assistant line VERBATIM into the viewport (a plain run of words round-trips
// through glamour unchanged), so a press maps to real, refreshView-stable content —
// unlike a bare vp.SetContent, which refreshView (called by the copy path) would
// clobber with the conversation render and then drop the now-mismatched selection.
// It returns the model, the logical line index of the assistant line, and the screen
// y to click it at; for an ASCII line the screen x equals the grapheme column.
func convModel(t *testing.T, line string) (Model, int, int) {
	t.Helper()
	cb := &fakeClipboard{}
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.deps.NoAltScreen = false
	m.deps.Clipboard = cb
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 40},
		client.SessionReadyMsg{SessionID: "sess-conv-0001"},
	)
	m.conv.addUser("req")
	m.conv.appendAssistant(line)
	m.phase = phaseIdle
	m.view.mode = followTail
	m.refreshView()
	m.deps.Clipboard = cb // ensure threaded after refresh
	idx := lineIndexContaining(m.vp.GetContent(), strings.Fields(line)[0])
	if idx < 0 {
		t.Fatalf("assistant line %q not found in rendered content", line)
	}
	top := convTopRow(m)
	y := top + (idx - m.vp.YOffset())
	if y < top || y >= top+m.vp.Height() {
		t.Fatalf("assistant line %d not on screen (YOffset=%d top=%d h=%d)", idx, m.vp.YOffset(), top, m.vp.Height())
	}
	return m, idx, y
}

// convBodyXOffset is the screen-X offset of the ASSISTANT body text in a convModel
// render: the base block indent plus the assistant body hang (the body hangs under
// "mecatl"). For an ASCII assistant line, screen x == convBodyXOffset + grapheme column
// in the raw line — so a click on raw-column k is at screen x convBodyXOffset+k.
const convBodyXOffset = defaultBlockIndent + assistantBodyHang

// setColContent sets raw viewport content for the cases that do NOT trigger a
// copy-driven refreshView (empty/no-copy selections): a successful copy calls
// refreshView, which rebuilds the viewport from the CONVERSATION and would clobber
// this raw content, so use convModel for any test that copies. Pins YOffset to 0 so
// logical line index equals screen offset from convTopRow.
func setColContent(t *testing.T, m Model, content string) Model {
	t.Helper()
	m.vp.SetContent(content)
	m.vp.SetYOffset(0)
	return m
}

// TestWordAtWordChars exercises the pure word classifier on word/space/punct runs,
// boundaries, and multibyte/wide clusters. FAILS if any run is mis-bounded.
func TestWordAtWordChars(t *testing.T) {
	cases := []struct {
		name             string
		line             string
		col              int
		wantStart, wantE int
	}{
		{"mid word bar", "foo bar baz", 5, 4, 7},     // "bar" at cols 4..6
		{"on space", "foo bar baz", 3, 3, 4},         // the single space run
		{"snake token", "snake_case_id", 6, 0, 13},   // whole identifier
		{"a-b on a", "a-b", 0, 0, 1},                 // "a"
		{"a-b on dash", "a-b", 1, 1, 2},              // "-" punct run
		{"col 0", "foo bar", 0, 0, 3},                // "foo"
		{"col n-1", "foo bar", 6, 4, 7},              // last char of "bar"
		{"multibyte héllo", "héllo wörld", 0, 0, 5},  // "héllo" (5 graphemes)
		{"multibyte wörld", "héllo wörld", 6, 6, 11}, // "wörld"
		{"wide CJK run", "日本 語", 0, 0, 2},            // "日本" (space at col 2)
		{"wide CJK after space", "日本 語", 3, 3, 4},    // "語"
		{"multi-char punct run", "a::b", 1, 1, 3},    // "::" bounded as one punct run [1,3)
		// A DECOMPOSED combining cluster: 'e'+U+0301 (combining acute) is ONE grapheme
		// cluster at col 0. Written with explicit \u escapes so an editor NFC pass
		// cannot silently precompose it: the first word is 4 grapheme columns, so
		// wordAt(col 0) spans [0,4) — proving grapheme-cluster (not rune) indexing.
		// A regression to rune-indexing would see 5 runes in the first word -> [0,5).
		{"decomposed combining", "e\u0301llo wo\u0308rld", 0, 0, 4},
		{"past EOL", "abc", 3, 3, 3}, // col == n → empty
		{"empty line", "", 0, 0, 0},  // empty → (0,0)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, e := wordAt(tc.line, tc.col)
			if s != tc.wantStart || e != tc.wantE {
				t.Errorf("wordAt(%q, %d) = (%d, %d), want (%d, %d)", tc.line, tc.col, s, e, tc.wantStart, tc.wantE)
			}
		})
	}
}

// TestDoubleClickSelectsWord: two left presses at the same spot over a word select
// the WHOLE word and copy it immediately. Asserted DETERMINISTICALLY on model state
// — selectedText is the exact string clickCopy hands to tea.SetClipboard (payload :=
// selectedText(...)), and statusMsg is set synchronously by clickCopy on a real copy
// — so no cmd is executed and the 400ms disarm tick never runs (zero timing
// dependence; this is why the test is ~0.00s).
func TestDoubleClickSelectsWord(t *testing.T) {
	m, _, y := convModel(t, "hello world after")
	// "world" starts at raw grapheme col 6; click mid-word at raw col 7, i.e. screen x
	// convBodyXOffset+7 (the assistant body hangs under "mecatl").
	m, _ = pressMouse(m, tea.MouseLeft, convBodyXOffset+7, y)
	m, _ = pressMouse(m, tea.MouseLeft, convBodyXOffset+7, y)

	if !m.sel.active {
		t.Fatal("double-click should leave an active selection")
	}
	if got := selectedText(m.vp.GetContent(), m.sel); got != "world" {
		t.Errorf("double-click selectedText = %q, want %q (the exact OSC52 payload)", got, "world")
	}
	if !strings.Contains(stripANSIstr(m.statusMsg), "copied") {
		t.Errorf("double-click status = %q, want a 'copied N chars' confirmation (copy fired)", stripANSIstr(m.statusMsg))
	}
}

// TestTripleClickSelectsLine: a third press at the same spot selects the WHOLE
// logical line (anchorC==0, headC==graphemeCount) and copies it. Asserted on model
// state (selectedText + statusMsg), so no cmd / disarm tick runs — see
// TestDoubleClickSelectsWord for why.
func TestTripleClickSelectsLine(t *testing.T) {
	m, idx, y := convModel(t, "hello world here")
	stripped := ansi.Strip(strings.Split(m.vp.GetContent(), "\n")[idx])

	m, _ = pressMouse(m, tea.MouseLeft, convBodyXOffset+7, y)
	m, _ = pressMouse(m, tea.MouseLeft, convBodyXOffset+7, y)
	m, _ = pressMouse(m, tea.MouseLeft, convBodyXOffset+7, y)

	// Triple-click selects the line's CONTENT past the leading whitespace margin (base
	// indent + the assistant body hang) — so the anchor is at the first real glyph, not
	// 0, and the selected text excludes the margin.
	if m.sel.anchorC != convBodyXOffset {
		t.Errorf("triple-click anchorC = %d, want %d (past the left margin + body hang)", m.sel.anchorC, convBodyXOffset)
	}
	if want := graphemeCount(stripped); m.sel.headC != want {
		t.Errorf("triple-click headC = %d, want graphemeCount %d", m.sel.headC, want)
	}
	if got := selectedText(m.vp.GetContent(), m.sel); got != "hello world here" {
		t.Errorf("triple-click selectedText = %q, want %q (whole line, margin + trailing trimmed)", got, "hello world here")
	}
	if !strings.Contains(stripANSIstr(m.statusMsg), "copied") {
		t.Errorf("triple-click status = %q, want a 'copied N chars' confirmation (copy fired)", stripANSIstr(m.statusMsg))
	}
}

// TestClickCountSamePositionAdvances: presses at the same logical position advance
// 1→2→3 and a 4th WRAPS to 1 (a fresh zero-width anchor, not a line).
func TestClickCountSamePositionAdvances(t *testing.T) {
	m, _, y := convModel(t, "hello world here")

	m, _ = pressMouse(m, tea.MouseLeft, 7, y)
	if m.clickCount != 1 {
		t.Fatalf("press1 clickCount = %d, want 1", m.clickCount)
	}
	m, _ = pressMouse(m, tea.MouseLeft, 7, y)
	if m.clickCount != 2 {
		t.Fatalf("press2 clickCount = %d, want 2", m.clickCount)
	}
	m, _ = pressMouse(m, tea.MouseLeft, 7, y)
	if m.clickCount != 3 {
		t.Fatalf("press3 clickCount = %d, want 3", m.clickCount)
	}
	m, _ = pressMouse(m, tea.MouseLeft, 7, y)
	if m.clickCount != 1 {
		t.Fatalf("press4 clickCount = %d, want 1 (wrap)", m.clickCount)
	}
	// The wrapped 4th press must be a fresh zero-width anchor, NOT a line select —
	// and a zero-width anchor copies NOTHING. An empty selection means clickCopy is
	// never reached (the count==1 branch returns before it) and selectedText is "",
	// so there is provably no clipboard payload. This is the deterministic no-copy
	// assertion (criterion 6): we never run the returned disarm tick.
	if !m.sel.empty() {
		t.Errorf("wrapped 4th press should be a zero-width anchor, got anchorC=%d headC=%d", m.sel.anchorC, m.sel.headC)
	}
	if got := selectedText(m.vp.GetContent(), m.sel); got != "" {
		t.Errorf("wrapped 4th press selectedText = %q, want \"\" (no copy)", got)
	}
}

// TestClickCountDifferentPositionResets: a press at a clearly different logical
// column resets the count to 1, updates clickL/clickC, and yields a single anchor
// (not a word).
func TestClickCountDifferentPositionResets(t *testing.T) {
	m, _, y := convModel(t, "hello world here")

	m, _ = pressMouse(m, tea.MouseLeft, 1, y) // col A
	if m.clickCount != 1 {
		t.Fatalf("first press clickCount = %d, want 1", m.clickCount)
	}
	m, _ = pressMouse(m, tea.MouseLeft, 9, y) // clearly different col B
	if m.clickCount != 1 {
		t.Errorf("press at a different position clickCount = %d, want 1 (reset)", m.clickCount)
	}
	if m.clickC != 9 {
		t.Errorf("clickC = %d, want 9 (recorded the new position)", m.clickC)
	}
	if !m.sel.empty() {
		t.Errorf("a reset press should be a zero-width anchor, not a word: anchorC=%d headC=%d", m.sel.anchorC, m.sel.headC)
	}
}

// TestClickDisarmResetsCount: a matching clickDisarmMsg resets the count to 0; a
// STALE gen (after a re-arm) does NOT.
func TestClickDisarmResetsCount(t *testing.T) {
	m, _, y := convModel(t, "hello world here")

	m, _ = pressMouse(m, tea.MouseLeft, 7, y)
	if m.clickCount != 1 {
		t.Fatalf("press clickCount = %d, want 1", m.clickCount)
	}
	gen := m.clickGen
	mm, _ := m.Update(clickDisarmMsg{gen: gen})
	m = mm.(Model)
	if m.clickCount != 0 {
		t.Errorf("matching clickDisarmMsg should reset count to 0, got %d", m.clickCount)
	}

	// Re-arm with a new press (bumps clickGen), then feed the STALE prior-gen tick.
	m, _ = pressMouse(m, tea.MouseLeft, 7, y)
	if m.clickCount != 1 {
		t.Fatalf("re-arm press clickCount = %d, want 1", m.clickCount)
	}
	staleGen := m.clickGen - 1
	// Guard: the stale gen must be genuinely PRIOR — if it accidentally equalled the
	// current gen the test would silently pass by feeding the live disarm.
	if staleGen == m.clickGen {
		t.Fatal("stale gen equals current — test can't distinguish")
	}
	mm, _ = m.Update(clickDisarmMsg{gen: staleGen})
	m = mm.(Model)
	if m.clickCount != 1 {
		t.Errorf("a stale clickDisarmMsg must NOT reset the count, got %d", m.clickCount)
	}
}

// TestDoubleClickOnWhitespaceSelectsSpaceRun: a double-click on whitespace selects
// the whitespace run (assert on the column span width, since selectedText trims
// trailing spaces). The assistant body margin (convBodyXOffset = base indent + body hang)
// shifts the content right, so the four-space run's ABSOLUTE columns are offset by it —
// the run WIDTH is what matters and is margin-independent.
func TestDoubleClickOnWhitespaceSelectsSpaceRun(t *testing.T) {
	// "ab    cd": four spaces at raw cols 2..5; with the assistant body margin the run
	// sits at content cols (2+convBodyXOffset)..(5+convBodyXOffset). A whitespace payload
	// trims to "" so the click copies nothing and does NOT refreshView — the geometry
	// persists.
	m, _, y := convModel(t, "ab    cd")

	m, _ = pressMouse(m, tea.MouseLeft, convBodyXOffset+3, y) // inside the space run
	m, _ = pressMouse(m, tea.MouseLeft, convBodyXOffset+3, y)

	if !m.sel.active {
		t.Fatal("double-click on whitespace should leave an active selection")
	}
	if w := m.sel.headC - m.sel.anchorC; w != 4 {
		t.Errorf("whitespace run width = %d, want 4", w)
	}
	if want := 2 + convBodyXOffset; m.sel.anchorC != want || m.sel.headC != want+4 {
		t.Errorf("whitespace span = [%d,%d), want [%d,%d)", m.sel.anchorC, m.sel.headC, want, want+4)
	}
}

// assertNoCopy is the DETERMINISTIC no-copy check shared by the empty-gesture tests:
// the selection is empty (so clickCopy's payload := selectedText(...) is "" and the
// copy branch is never taken) AND the status carries no "copied" confirmation. It
// touches no command, so the 400ms disarm tick is never run.
func assertNoCopy(t *testing.T, m Model, what string) {
	t.Helper()
	if !m.sel.empty() {
		t.Errorf("%s should be an empty selection, got [%d,%d)-[%d,%d)", what, m.sel.anchorL, m.sel.anchorC, m.sel.headL, m.sel.headC)
	}
	if got := selectedText(m.vp.GetContent(), m.sel); got != "" {
		t.Errorf("%s selectedText = %q, want \"\" (no payload)", what, got)
	}
	if strings.Contains(stripANSIstr(m.statusMsg), "copied") {
		t.Errorf("%s set a 'copied' status %q, want none (no copy fired)", what, stripANSIstr(m.statusMsg))
	}
}

// TestDoubleClickPastEOLNoCopy: a double-click past the content width selects
// nothing and copies nothing.
func TestDoubleClickPastEOLNoCopy(t *testing.T) {
	m, _ := selModel(t)
	m = setColContent(t, m, "abc")
	tp := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 50, tp) // well past EOL (clamps to col 3 == n)
	m, _ = pressMouse(m, tea.MouseLeft, 50, tp)

	assertNoCopy(t, m, "double-click past EOL")
}

// TestDoubleClickEmptyLineNoCopy: a double-click on a blank line copies nothing.
func TestDoubleClickEmptyLineNoCopy(t *testing.T) {
	m, _ := selModel(t)
	m = setColContent(t, m, "first\n\nthird") // line 1 is blank
	tp := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, tp+1) // the blank line
	m, _ = pressMouse(m, tea.MouseLeft, 0, tp+1)

	assertNoCopy(t, m, "double-click on a blank line")
}

// TestTripleClickEmptyLineNoCopy: a triple-click on a blank line copies nothing
// (the whole-line span is still empty).
func TestTripleClickEmptyLineNoCopy(t *testing.T) {
	m, _ := selModel(t)
	m = setColContent(t, m, "first\n\nthird")
	tp := convTopRow(m)

	m, _ = pressMouse(m, tea.MouseLeft, 0, tp+1)
	m, _ = pressMouse(m, tea.MouseLeft, 0, tp+1)
	m, _ = pressMouse(m, tea.MouseLeft, 0, tp+1)

	assertNoCopy(t, m, "triple-click on a blank line")
}

// TestMultiClickInertUnderOverlay: under each non-selectable state, two presses
// start NO selection AND leave clickCount==0 (Req 9, count is AFTER the gate).
func TestMultiClickInertUnderOverlay(t *testing.T) {
	cases := []nonSelectableCase{
		{"help", func(m *Model) { m.showHelp = true }},
		{"noMouse", func(m *Model) { m.deps.NoMouse = true }},
		{"noAltScreen", func(m *Model) { m.deps.NoAltScreen = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := selModel(t)
			m = setColContent(t, m, "hello world")
			tc.enter(&m)
			if selectable(m) {
				t.Fatalf("precondition: %s should be non-selectable", tc.name)
			}
			tp := convTopRow(m)
			m, _ = pressMouse(m, tea.MouseLeft, 7, tp)
			m, _ = pressMouse(m, tea.MouseLeft, 7, tp)
			if m.sel.active {
				t.Errorf("%s: presses must not start a selection", tc.name)
			}
			if m.clickCount != 0 {
				t.Errorf("%s: presses must not advance clickCount (got %d)", tc.name, m.clickCount)
			}
		})
	}
}

// TestDragAfterDoubleClickResetsCount: a drag-extend after a double-click resets
// clickCount to 0 and moves the head (the word/line leaves a real anchor/head so a
// subsequent drag extends normally).
func TestDragAfterDoubleClickResetsCount(t *testing.T) {
	m, _, y := convModel(t, "hello world second line")

	m, _ = pressMouse(m, tea.MouseLeft, 7, y)
	m, _ = pressMouse(m, tea.MouseLeft, 7, y)
	if m.clickCount != 2 {
		t.Fatalf("precondition: double-click clickCount = %d, want 2", m.clickCount)
	}
	beforeHead := m.sel.headC

	m, _ = motionMouse(m, 20, y) // drag-extend to the right

	if m.clickCount != 0 {
		t.Errorf("a drag after double-click should reset clickCount to 0, got %d", m.clickCount)
	}
	if m.sel.headC == beforeHead {
		t.Errorf("the drag should have moved the head (%d → %d)", beforeHead, m.sel.headC)
	}
}

// TestDoubleClickIdentitySnapshotSurvivesRefresh: a double-click word selection
// survives a refreshView with appended content below it (snapshot still matches, so
// the highlight is reapplied rather than the selection dropped).
func TestDoubleClickIdentitySnapshotSurvivesRefresh(t *testing.T) {
	m, _, y := convModel(t, "hello world after")

	// "world" at cols 6..10; double-click selects it.
	m, _ = pressMouse(m, tea.MouseLeft, 7, y)
	m, _ = pressMouse(m, tea.MouseLeft, 7, y)
	if !m.sel.active || m.sel.empty() {
		t.Fatal("precondition: double-click should leave a non-empty selection")
	}
	anchorC, headC := m.sel.anchorC, m.sel.headC

	m.phase = phaseRunning
	m = applyAll(m,
		client.AssistantDeltaMsg{Turn: 1, Text: strings.Repeat("appended\n", 5)},
		renderTickMsg{},
	)

	if !m.sel.active {
		t.Error("double-click selection should survive a streaming delta below it")
	}
	if m.sel.anchorC != anchorC || m.sel.headC != headC {
		t.Errorf("delta moved the word selection columns: [%d,%d) → [%d,%d)", anchorC, headC, m.sel.anchorC, m.sel.headC)
	}
	styled := styleSelection(m.vp.GetContent(), m.sel, m.deps.Theme.Style("selection"))
	if !strings.Contains(styled, selectionBgSGR(t, m)) {
		t.Error("the word-selection highlight should still be rendered after the refresh (bg SGR absent)")
	}
}

// TestMouseDebugOverlay covers the gated MECATUI_DEBUG_MOUSE diagnostic: with
// DebugMouse on, a mouse press sets m.mouseDebug to the formatted line (raw coords +
// content and input mapping) and the footer surfaces it (highest priority — over the phase
// arms). With DebugMouse off, no press sets it and the footer shows the normal
// status. Default OFF, zero cost when unset.
func TestMouseDebugOverlay(t *testing.T) {
	// Off by default: a press records nothing and the footer shows the normal status.
	m, _ := selModel(t)
	top := convTopRow(m)
	m, _ = pressMouse(m, tea.MouseLeft, 3, top)
	if m.mouseDebug != "" {
		t.Errorf("DebugMouse off: a press must not set mouseDebug, got %q", m.mouseDebug)
	}
	if got := stripANSIstr(m.renderFooter()); strings.Contains(got, "MOUSE raw") {
		t.Errorf("DebugMouse off: footer must not show the mouse diagnostic, got %q", got)
	}

	// mouseDebugLine formats the expected shape directly.
	m2, _ := selModel(t)
	m2.deps.DebugMouse = true
	mo := tea.Mouse{X: 7, Y: convTopRow(m2) + 1}
	line := m2.mouseDebugLine(mo)
	for _, want := range []string{"MOUSE raw x=7", "y=", "top=", "yoff=", "vph=", "map ok=", "input ok="} {
		if !strings.Contains(line, want) {
			t.Errorf("mouseDebugLine = %q, missing %q", line, want)
		}
	}

	// With DebugMouse on, a press sets m.mouseDebug and the footer surfaces it (over
	// the idle "ready" status).
	m2, _ = pressMouse(m2, tea.MouseLeft, 7, convTopRow(m2)+1)
	if !strings.Contains(m2.mouseDebug, "MOUSE raw") {
		t.Errorf("DebugMouse on: a press should set mouseDebug, got %q", m2.mouseDebug)
	}
	if got := stripANSIstr(m2.renderFooter()); !strings.Contains(got, "MOUSE raw") {
		t.Errorf("DebugMouse on: footer should surface the mouse diagnostic, got %q", got)
	}
}

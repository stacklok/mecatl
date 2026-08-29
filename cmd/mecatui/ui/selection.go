package ui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The on-screen height of any region (header, body, transients, input, footer) is
// MEASURED via lipgloss.Height of its rendered content, never a constant — see
// region.height() in layout.go. The header in particular is NOT fixed-height: its
// identity line word-WRAPS when it exceeds the width (lipgloss .Width() wraps;
// fitHeader only sheds the right-aligned indicator, never the identity parts), so a
// real session (long model id + "mode acceptEdits" + host:port) renders a 3–4 row
// header at a narrow width. Both the viewport sizing (relayout) and the click→content
// mapping (convTopRow) derive from the SAME chrome() regions, so they can never
// disagree, and because chrome is re-derived on demand the heights track changes that
// happen WITHOUT a resize (the active model id wrapping, a transient toggling).

// autoScrollDir is the edge-autoscroll direction stashed while a drag is held at a
// viewport border (so a self-re-arming tick keeps scrolling without further mouse
// movement). scrollNone disarms any pending tick.
type autoScrollDir int

const (
	scrollNone autoScrollDir = iota
	scrollUp
	scrollDown
)

// selection is the in-app text-selection state: a left-click-drag over the
// conversation viewport that highlights runes and copies them on release (OSC52 +
// shell fallback). Its coordinates are LOGICAL CONTENT positions, NOT screen
// positions: anchorL/headL index a real line of the viewport content
// (m.vp.GetContent() split on \n), and anchorC/headC are GRAPHEME COLUMNS into the
// ANSI-STRIPPED form of that line. Logical anchors are why the highlight survives
// scrolling and a streaming re-render: the selection style is spliced into the
// CURRENT content each frame (styleSelection in refreshView), so a scroll only
// moves which logical lines are on screen, and a streamed delta that grows the
// content leaves the selected logical span untouched. The zero value is an
// inactive selection.
//
// autoScroll is the edge-drag direction: non-scrollNone while the held pointer sits
// at the top/bottom viewport border, driving a self-re-arming tick that scrolls the
// view (ACCELERATING the longer the edge is held — see autoScrollRamp) and extends
// the head to the newly-revealed lines. It is cleared (back to scrollNone) on
// release, on motion back inside the region, on esc-clear, and whenever the
// selection goes inactive — so any in-flight tick no-ops.
type selection struct {
	active           bool
	anchorL, anchorC int
	headL, headC     int
	autoScroll       autoScrollDir
	// dragX is the last drag pointer's CELL X, stashed while edge-autoscroll is armed
	// so the self-re-arming tick (which carries no fresh mouse coordinate) can keep
	// the head at the same horizontal column as the view scrolls.
	dragX int
	// autoScrollRamp counts consecutive armed TICKS at the current edge; it drives the
	// lines-per-tick acceleration (rampToLines). Reset to 0 on every disarm (release /
	// motion-back-inside / esc / content-edge / inactive) so a fresh edge-hold restarts
	// at 1 line. It is advanced ONLY in onAutoScroll — never in armAutoScroll, which
	// re-fires per cell at the edge and would otherwise reset acceleration each motion.
	autoScrollRamp int
	// snapshot is the VISIBLE selected text (selectedText) as of the last time the
	// selection geometry changed via a gesture — the identity anchor for the
	// selection. The anchor/head are absolute line indices into the viewport content,
	// so a reflow that changes the line count ABOVE or WITHIN the selection (ctrl+t
	// expand/collapse, compaction) silently re-points them at different text. On every
	// re-render refreshView recomputes selectedText against the CURRENT content and,
	// if it no longer equals snapshot, DROPS the selection rather than highlight/copy
	// the wrong runes. A pure streaming append BELOW the selection leaves the selected
	// lines untouched, so snapshot still matches and the selection survives (Req 2).
	snapshot string
}

// convTopRow is the 0-based screen row where the conversation viewport's first row
// sits. View() joins its regions with "\n" and the body sits directly below the
// regions chrome() places ABOVE it, so the body-top screen row is exactly the summed
// height of those above-regions — DERIVED from the layout model, not assumed. Today
// the only above-region is the header, so this equals the header's rendered height
// (value-identical to the old m.headerHeight()), but it tracks the layout rather than
// hardcoding "body is region[1]": were a region ever added above the body, this offset
// follows automatically (the convTopRow-drift test in selection_test.go guards it).
// (Note: it is the SCREEN row a click maps from, distinct from styleSelection, which
// splices the highlight into the content; both are content-relative and agree.)
// The header word-WRAPS at narrow widths, so a fixed "2" would under-count the real
// body-top and paint the highlight ABOVE the cursor — measuring via chrome() is why it
// can't. Returns -1 (sentinel "unknown") before the first resize, when width/height
// are unset — callers then refuse to start a selection.
func convTopRow(m Model) int {
	if m.width <= 0 || m.height <= 0 {
		return -1
	}
	above, _ := m.chrome()
	return sumHeight(above)
}

// selectable reports whether a left-click may START a selection right now. It is
// the SAME overlay/mode gate the body switch in view.go uses to decide what owns
// the conversation region: a selection may only begin when the plain viewport is
// showing it. Mouse capture exists only on the alt screen with mouse enabled, so
// --inline/--no-alt-screen (NoAltScreen) and --no-mouse (NoMouse) are never
// selectable — those leave the mouse uncaptured for the terminal's native
// selection. An open overlay/modal/help, session-details view, or the fatal screen
// owns the body and blocks a new selection (and opening one mid-drag clears the
// active selection — see the overlay-open paths in update.go).
func selectable(m Model) bool {
	return !m.deps.NoAltScreen &&
		!m.deps.NoMouse &&
		m.phase != phaseFatal &&
		m.phase != phaseAwaitingApproval &&
		m.phase != phaseReplay &&
		!m.sessionDetailsOpen &&
		!m.showHelp &&
		m.team.view == teamNone &&
		m.agentsInv.view == agentsInvNone &&
		m.modal == nil && // no surface-migrated overlay owns the body
		m.userModel.view == userModelNone &&
		m.reflections.view == reflectionsNone &&
		m.dream.view == dreamClosed &&
		m.effort.view == effortNone &&
		m.worktrees.view == worktreesNone &&
		m.schedule.view == scheduleNone
}

// screenToContent maps a screen cell (x, y) to a LOGICAL content position (line
// index into the viewport content, grapheme column into the ansi-stripped line).
// ok is false when (x, y) is outside the conversation region — above the top row,
// at/below the bottom of the viewport, or before width/height are known (Req 9:
// header/input/footer clicks start no selection). SoftWrap is OFF in mecatui (it
// is never enabled), so the screen→line mapping is a simple offset: a screen row
// is YOffset()+(screenY-convTop), and the screen X is a grapheme column directly
// (XOffset is 0 in normal use, added defensively).
func screenToContent(m Model, x, y int) (line, col int, ok bool) {
	top := convTopRow(m)
	if top < 0 {
		return 0, 0, false
	}
	vpH := m.vp.Height()
	if y < top || y >= top+vpH {
		return 0, 0, false
	}
	line = m.vp.YOffset() + (y - top)
	lines := strings.Split(m.vp.GetContent(), "\n")
	if len(lines) == 0 {
		return 0, 0, false
	}
	if line < 0 {
		line = 0
	}
	if line >= len(lines) {
		// A click in the blank region below the last content line clamps to the end
		// of the last line (a natural "select to the end" gesture), not a miss.
		line = len(lines) - 1
	}
	stripped := ansi.Strip(lines[line])
	col = graphemeColForCellX(stripped, x+m.vp.XOffset())
	return line, col, true
}

// graphemeColForCellX converts a display cell X into a GRAPHEME-CLUSTER column on
// the ansi-stripped line by walking clusters and summing their display width until
// the accumulated width exceeds cellX. The result is clamped to the line's
// grapheme count, so a click past end-of-line lands at the line end (Req 6's
// "click past EOL = line end"). It uses ansi.FirstGraphemeCluster + ansi.StringWidth
// — the SAME segmentation/width engine the renderer uses — so the column can never
// disagree with the on-screen layout.
func graphemeColForCellX(stripped string, cellX int) int {
	if cellX <= 0 {
		return 0
	}
	col, width := 0, 0
	rest := stripped
	for len(rest) > 0 {
		cl, _ := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		if cl == "" {
			break
		}
		w := ansi.StringWidth(cl)
		if w <= 0 {
			w = 1
		}
		if width+w > cellX {
			return col
		}
		width += w
		col++
		rest = rest[len(cl):]
	}
	return col
}

// normalize returns the selection's (start, end) in document order — start <= end
// by (line, then column) — so styleSelection and selectedText are order-independent
// (a top-down drag and a bottom-up drag select the same span).
func (s selection) normalize() (startL, startC, endL, endC int) {
	startL, startC, endL, endC = s.anchorL, s.anchorC, s.headL, s.headC
	if endL < startL || (endL == startL && endC < startC) {
		startL, startC, endL, endC = endL, endC, startL, startC
	}
	return startL, startC, endL, endC
}

// empty reports a degenerate (zero-width) selection: anchor == head. An empty
// selection copies nothing and is cleared on release rather than copied.
func (s selection) empty() bool {
	return s.anchorL == s.headL && s.anchorC == s.headC
}

// cellWidthForCol converts a GRAPHEME-CLUSTER column into a CELL/display-width
// column on an ansi-stripped line: it sums the display width (ansi.StringWidth) of
// the first `col` grapheme clusters, using the SAME FirstGraphemeCluster engine as
// graphemeColForCellX/colToByte. It is needed because lipgloss.StyleRanges (and the
// ansi.Cut beneath it) is CELL-width indexed, while the selection's anchorC/headC
// are GRAPHEME columns — a line with wide runes (CJK, "中") would otherwise mis-place
// the styled span. A col at/past the grapheme count returns the full display width.
func cellWidthForCol(stripped string, col int) int {
	if col <= 0 {
		return 0
	}
	width, n := 0, 0
	rest := stripped
	for len(rest) > 0 && n < col {
		cl, _ := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		if cl == "" {
			break
		}
		w := ansi.StringWidth(cl)
		if w <= 0 {
			w = 1
		}
		width += w
		rest = rest[len(cl):]
		n++
	}
	return width
}

// styleSelection splices the selection style into the rendered content lines BEFORE
// SetContent — owning the highlight render ourselves rather than relying on the
// bubbles viewport's native SetHighlights. The viewport's parseMatches walks
// ansi.Strip(content) for positions but indexes the ORIGINAL ANSI content to detect
// newlines, so with ANSI-styled content (glamour markdown — the real conversation)
// the stripped and original byte offsets diverge and the highlight lands on the
// WRONG line (~2 lines off). We sidestep that defect entirely by styling our own
// lines: a pure per-line splice of the selection style over the spanned columns,
// translated from GRAPHEME columns (the selection's anchorC/headC) to CELL columns
// (lipgloss.StyleRanges is cell-indexed) via cellWidthForCol.
//
// An EMPTY interior line of a multi-line selection now paints ONE styled cell
// (style.Render(" ")) — the run is solid through blank lines, removing the old
// byteRanges gap limitation. The first/last lines run from the selection's start/end
// column; interior lines span the whole line width. selectedText (the copy path) is
// untouched — it still preserves the empty line via "\n". Returns content unchanged
// for an inactive/empty selection.
func styleSelection(content string, sel selection, style lipgloss.Style) string {
	if !sel.active || sel.empty() {
		return content
	}
	startL, startC, endL, endC := sel.normalize()
	lines := strings.Split(content, "\n")
	if startL < 0 || startL >= len(lines) {
		return content
	}
	if endL >= len(lines) {
		endL = len(lines) - 1
	}
	for li := startL; li <= endL; li++ {
		stripped := ansi.Strip(lines[li])
		fromCol := 0
		if li == startL {
			fromCol = startC
		}
		toCol := graphemeCount(stripped)
		if li == endL {
			toCol = endC
		}
		from := cellWidthForCol(stripped, fromCol)
		to := cellWidthForCol(stripped, toCol)
		switch {
		case to > from:
			lines[li] = lipgloss.StyleRanges(lines[li], lipgloss.NewRange(from, to, style))
		case startL < li && li < endL && stripped == "":
			// An empty INTERIOR line: paint ONE cell so the multi-line run stays solid
			// through blank lines (the run above/below would otherwise show a gap).
			lines[li] = style.Render(" ")
		}
	}
	return strings.Join(lines, "\n")
}

// selectedText is the ANSI-STRIPPED, copy-ready payload for the selection: for
// each spanned line, the ansi-stripped text sliced by the grapheme-column range,
// joined with "\n", with per-line trailing padding trimmed (mirroring render.go's
// trimTrailingSpaces so the glamour right-padding never bloats a copy). Returns ""
// for an inactive/empty selection.
func selectedText(content string, sel selection) string {
	if !sel.active || sel.empty() {
		return ""
	}
	startL, startC, endL, endC := sel.normalize()
	lines := strings.Split(content, "\n")
	if startL < 0 || startL >= len(lines) {
		return ""
	}
	if endL >= len(lines) {
		endL = len(lines) - 1
	}
	out := make([]string, 0, endL-startL+1)
	for li := startL; li <= endL; li++ {
		stripped := ansi.Strip(lines[li])
		fromCol := 0
		if li == startL {
			fromCol = startC
		}
		toCol := graphemeCount(stripped)
		if li == endL {
			toCol = endC
		}
		seg := stripped[colToByte(stripped, fromCol):colToByte(stripped, toCol)]
		out = append(out, strings.TrimRight(seg, " "))
	}
	return strings.Join(out, "\n")
}

// graphemeCount returns the number of grapheme clusters in an ansi-stripped line —
// the maximum valid grapheme column (a column equal to it is the line end).
func graphemeCount(stripped string) int {
	n := 0
	rest := stripped
	for len(rest) > 0 {
		cl, _ := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		if cl == "" {
			break
		}
		n++
		rest = rest[len(cl):]
	}
	return n
}

// colToByte converts a grapheme-cluster column into a BYTE offset within an
// ansi-stripped line, clamped to the line length when col is at/past the end.
func colToByte(stripped string, col int) int {
	if col <= 0 {
		return 0
	}
	pos, n := 0, 0
	rest := stripped
	for len(rest) > 0 && n < col {
		cl, _ := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		if cl == "" {
			break
		}
		pos += len(cl)
		rest = rest[len(cl):]
		n++
	}
	return pos
}

// Grapheme-cluster classes for the double-click word selector. This is a
// hand-rolled, dependency-free classification (stdlib unicode only) — NOT UAX#29
// word segmentation. Terminal word-select uses word-character CLASSES (the classic
// "double-click selects the identifier/path/run" behaviour), not the linguistic
// word boundaries UAX#29 yields, which would surprise users by splitting
// identifiers, dotted paths, and contractions. A cluster is classified by its FIRST
// rune.
const (
	classWord  = iota // letters, digits, '_' — an identifier-ish run
	classSpace        // whitespace
	classPunct        // everything else
)

// graphemeClass classifies a grapheme cluster by its FIRST rune into one of
// classWord / classSpace / classPunct. An empty cluster is treated as punct (it
// only arises defensively; wordAt never feeds it one).
func graphemeClass(cluster string) int {
	if cluster == "" {
		return classPunct
	}
	r, _ := utf8.DecodeRuneInString(cluster)
	switch {
	case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
		return classWord
	case unicode.IsSpace(r):
		return classSpace
	default:
		return classPunct
	}
}

// wordAt returns the [startCol, endCol) grapheme-column span (exclusive end,
// matching headC semantics) of the maximal run of the SAME grapheme class as the
// cluster at col, on the already-ansi-stripped line. Columns are GRAPHEME columns,
// never bytes, so wide/multibyte runes are handled correctly. A click on whitespace
// selects the whitespace run; on punctuation, the punctuation run. col at/past the
// grapheme count (a click past end-of-line) returns an empty (col, col) span — the
// caller treats that as "nothing to select" and copies nothing.
func wordAt(stripped string, col int) (startCol, endCol int) {
	// Build one class per grapheme column by walking clusters once.
	var classes []int
	rest := stripped
	for len(rest) > 0 {
		cl, _ := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		if cl == "" {
			break
		}
		classes = append(classes, graphemeClass(cl))
		rest = rest[len(cl):]
	}
	n := len(classes)
	if n == 0 || col < 0 || col >= n {
		return col, col
	}
	target := classes[col]
	start := col
	for start > 0 && classes[start-1] == target {
		start--
	}
	end := col + 1
	for end < n && classes[end] == target {
		end++
	}
	return start, end
}

// wordSelect sets the selection to the word (same-class run) under the click at
// (line, col), then snapshots its identity + re-renders the highlight. An out-of-bounds
// line index is a no-op (returns the model unchanged). A click that yields an empty
// span (past end-of-line, or a blank line) leaves an inactive-equivalent empty
// selection (anchor==head) so the caller copies nothing.
func (m Model) wordSelect(line, col int) Model {
	lines := strings.Split(m.vp.GetContent(), "\n")
	if line < 0 || line >= len(lines) {
		return m
	}
	stripped := ansi.Strip(lines[line])
	s, e := wordAt(stripped, col)
	m.sel = selection{active: true, anchorL: line, anchorC: s, headL: line, headC: e}
	snapshotSelection(&m)
	return m
}

// lineSelect sets the selection to the WHOLE logical line's CONTENT — from the first
// non-whitespace grapheme to the line's grapheme count — then snapshots its identity +
// re-renders the highlight. Skipping the LEADING WHITESPACE means triple-click grabs the
// line's text regardless of how much left margin the renderer prepended: the base block
// indent, plus the assistant body hang, plus the user block's rail+padding all leave
// leading spaces (the rail glyph is the only non-space, and it precedes the body text, so
// for an assistant/notice/tool line this lands on the first real character). A blank line
// (all whitespace) yields an empty selection (anchor==head) so the caller copies nothing.
// An out-of-bounds line index is a no-op.
func (m Model) lineSelect(line int) Model {
	lines := strings.Split(m.vp.GetContent(), "\n")
	if line < 0 || line >= len(lines) {
		return m
	}
	stripped := ansi.Strip(lines[line])
	n := graphemeCount(stripped)
	// Skip leading whitespace (the conversation left margin / hang) so the selection
	// starts at the first real glyph. Counted in GRAPHEME columns to match anchorC/headC.
	start := 0
	for _, rn := range stripped {
		if rn != ' ' {
			break
		}
		start++
	}
	if start > n {
		start = n
	}
	m.sel = selection{active: true, anchorL: line, anchorC: start, headL: line, headC: n}
	snapshotSelection(&m)
	return m
}

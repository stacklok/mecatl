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

// withQueue stages n follow-ups so the queue card (a transient region between body
// and input) renders, then re-runs the relayout chokepoint so the viewport is sized
// for the now-present transient — exactly as a real message would. It returns the
// model with the transient live. The queue is the easiest transient to drive
// deterministically (a plain []string field, rendered in any phase by renderQueue).
func withQueue(t *testing.T, m Model, n int) Model {
	t.Helper()
	m.queued = nil
	for i := 0; i < n; i++ {
		m.queued = append(m.queued, "staged follow-up")
	}
	if m.renderQueue() == "" {
		t.Fatalf("precondition: queue of %d should render a non-empty region", n)
	}
	m.relayout()
	return m
}

// withPalette opens the slash-command palette with n filtered rows so it renders a
// MULTI-ROW transient region (rows + the "commands" header + the key hint), then
// relayouts. State is set directly (not via keystrokes) so the menu height is
// deterministic regardless of any workspace/command-discovery fixture.
func withPalette(t *testing.T, m Model, n int) Model {
	t.Helper()
	m.palette.open = true
	m.palette.dismissed = false
	m.palette.filtered = nil
	for i := 0; i < n; i++ {
		m.palette.filtered = append(m.palette.filtered, client.Command{Name: fmt.Sprintf("cmd%d", i), Description: "a command"})
	}
	m.palette.cursor = 0
	if renderPalette(m.deps.Theme, m.palette, m.caps, m.prompt.Value(), m.width) == "" {
		t.Fatalf("precondition: palette of %d rows should render a non-empty region", n)
	}
	m.relayout()
	return m
}

// withMention opens the @-mention file menu with n match rows so it renders a
// MULTI-ROW transient region, then relayouts. Like withPalette the state is set
// directly so the menu height is deterministic.
func withMention(t *testing.T, m Model, n int) Model {
	t.Helper()
	m.mention.open = true
	m.mention.dismissed = false
	m.mention.matches = nil
	for i := 0; i < n; i++ {
		m.mention.matches = append(m.mention.matches, fmt.Sprintf("dir/file%d.go", i))
	}
	m.mention.cursor = 0
	if renderMention(m.deps.Theme, m.mention, m.width) == "" {
		t.Fatalf("precondition: mention of %d rows should render a non-empty region", n)
	}
	m.relayout()
	return m
}

// TestLayoutOffsetsMatchRenderedFrame proves the WHOLE region stack — not just the
// body-top — renders at the screen offset the layout derives for it. With a transient
// (the queue card) present, it walks the assembled regions, computes each region's
// expected top screen row as the sum of the heights before it, and asserts the
// rendered frame's first row of that region matches the region's own first rendered
// row. This pins that View() and the layout model agree about every region's position.
func TestLayoutOffsetsMatchRenderedFrame(t *testing.T) {
	m, _ := selModel(t)
	m = withQueue(t, m, 2)

	body := m.vp.View()
	l := m.assembleLayout(body)
	frame := strings.Split(m.View().Content, "\n")

	// Sanity: the assembled join IS the frame.
	if l.join() != m.View().Content {
		t.Fatal("assembleLayout(body).join() must equal View().Content")
	}

	// PRECONDITION: the body region must carry real (non-blank) text, or the
	// per-region cross-check below silently degrades into a chrome-only check (a blank
	// body trivially matches blank frame rows). selModel sets 120 content lines with
	// stuck=true, so the body is real text.
	bodyHasText := false
	for _, r := range l.regions {
		if r.role == regionBody && strings.TrimSpace(ansi.Strip(r.content)) != "" {
			bodyHasText = true
		}
	}
	if !bodyHasText {
		t.Fatal("precondition: body region is all-blank; the offset cross-check would be vacuous")
	}

	// At least one transient must be in the stack, or the test is vacuous.
	sawQueue := false
	for _, r := range l.regions {
		if r.role == regionQueue {
			sawQueue = true
		}
	}
	if !sawQueue {
		t.Fatal("precondition: queue region missing from the layout")
	}

	off := 0
	for i, r := range l.regions {
		regionLines := strings.Split(r.content, "\n")
		for j, want := range regionLines {
			screenRow := off + j
			if screenRow >= len(frame) {
				t.Fatalf("region %d (role %d) line %d at screen row %d past frame end (%d)", i, r.role, j, screenRow, len(frame))
			}
			gotLine := strings.TrimRight(ansi.Strip(frame[screenRow]), " ")
			wantLine := strings.TrimRight(ansi.Strip(want), " ")
			if gotLine != wantLine {
				t.Errorf("role %d line %d: frame[%d] = %q, want %q", r.role, j, screenRow, gotLine, wantLine)
			}
		}
		off += r.height()
	}
}

// TestTransientRegionShrinksViewportKeepsFooter is the OVERFLOW FIX: a transient
// region present shrinks the viewport so the footer is NOT clipped, and the body
// height equals exactly m.height − above − below. It compares against the
// no-transient case (viewport strictly taller) so the shrink is real, asserts the
// rendered frame's last line is the footer (not the input or a clipped region), and
// that the total frame height never exceeds m.height.
func TestTransientRegionShrinksViewportKeepsFooter(t *testing.T) {
	// No-transient baseline.
	base, _ := selModel(t)
	baseH := base.vp.Height()

	// With a deep queue transient.
	m, _ := selModel(t)
	m = withQueue(t, m, 3)

	above, below := m.chrome()
	wantBody := m.height - sumHeight(above) - sumHeight(below)
	if got := m.vp.Height(); got != wantBody {
		t.Errorf("transient bodyHeight = %d, want %d (height - above - below)", got, wantBody)
	}

	// The transient ate rows: the viewport must be strictly SHORTER than the
	// no-transient viewport at the same total height.
	if m.vp.Height() >= baseH {
		t.Errorf("transient vpH %d should be < no-transient vpH %d (the card eats rows)", m.vp.Height(), baseH)
	}

	// The frame's LAST line must be the footer's last line (the footer is not pushed
	// off-screen). renderFooter is "<status>\n<help muted>", so its last line is the
	// muted help line.
	frame := strings.Split(m.View().Content, "\n")
	footer := m.renderFooter()
	footerLines := strings.Split(footer, "\n")
	wantLast := strings.TrimRight(ansi.Strip(footerLines[len(footerLines)-1]), " ")
	gotLast := strings.TrimRight(ansi.Strip(frame[len(frame)-1]), " ")
	if gotLast != wantLast {
		t.Errorf("frame last line = %q, want footer last line %q (footer clipped?)", gotLast, wantLast)
	}

	// Total frame height must fit the terminal.
	if len(frame) > m.height {
		t.Errorf("frame is %d rows, exceeds terminal height %d", len(frame), m.height)
	}
}

// TestNoTransientBodyHeightMatchesMeasuredChrome is the magic-number-gone guard: in
// the NO-transient case the body height equals m.height minus the MEASURED header +
// input-top-spacer + input + footer heights (lipgloss.Height of the rendered regions).
// A reintroduced taH=4/footerH=2 constant — or a header-height assumption, or a forgotten
// input spacer — would diverge here.
func TestNoTransientBodyHeightMatchesMeasuredChrome(t *testing.T) {
	m, _ := selModel(t)
	// Precondition: no transient is present.
	if m.renderQueue() != "" || renderPalette(m.deps.Theme, m.palette, m.caps, m.prompt.Value(), m.width) != "" ||
		renderMention(m.deps.Theme, m.mention, m.width) != "" {
		t.Fatal("precondition: no transient should be present in selModel")
	}
	want := m.height - lipgloss.Height(m.renderHeader()) - lipgloss.Height(inputSpacerRow) -
		lipgloss.Height(m.renderInput()) - lipgloss.Height(m.renderFooter())
	if got := m.vp.Height(); got != want {
		t.Errorf("no-transient bodyHeight = %d, want %d (height - measured header - input spacer - input - footer)", got, want)
	}
}

// TestClickRegionRolesOnlyBodySelectable proves screenToContent's region gate across
// EVERY transient (palette / mention / queue): clicks in the header rows and at/below
// the body (the transient/input/footer rows) return ok=false, while clicks inside the
// body return ok=true. Each transient is multi-row, so it also probes a row INTERIOR
// to the transient (not just the first below-body row) → still ok=false: the whole
// transient band, not merely its top edge, is non-selectable. The body bound is the
// SHRUNKEN viewport (the transient ate rows), so this also pins that the gate tracks
// the relayout'd height.
func TestClickRegionRolesOnlyBodySelectable(t *testing.T) {
	const rows = 4 // each transient renders > 2 rows, so an interior probe exists
	cases := []struct {
		name string
		with func(*testing.T, Model) Model
	}{
		{"queue", func(t *testing.T, m Model) Model { return withQueue(t, m, rows) }},
		{"palette", func(t *testing.T, m Model) Model { return withPalette(t, m, rows) }},
		{"mention", func(t *testing.T, m Model) Model { return withMention(t, m, rows) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := selModel(t)
			m = tc.with(t, m)
			top := convTopRow(m)
			vpH := m.vp.Height()

			// Header rows (above the body) miss.
			for y := 0; y < top; y++ {
				if _, _, ok := screenToContent(m, 0, y); ok {
					t.Errorf("header row %d should not be selectable", y)
				}
			}
			// Body rows hit (top and bottom edge).
			if _, _, ok := screenToContent(m, 0, top); !ok {
				t.Error("body-top row should be selectable")
			}
			if _, _, ok := screenToContent(m, 0, top+vpH-1); !ok {
				t.Error("body-bottom row should be selectable")
			}
			// The row JUST below the body — the transient's first row — misses.
			if _, _, ok := screenToContent(m, 0, top+vpH); ok {
				t.Error("the row just below the body (transient region) must not be selectable")
			}
			// A row INTERIOR to the transient band (not its top edge) also misses. The
			// transient renders below the body and is > 2 rows, so top+vpH+1 lands inside
			// it (before the input/footer below).
			frame := strings.Split(m.View().Content, "\n")
			interior := top + vpH + 1
			if interior >= len(frame) {
				t.Fatalf("precondition: no interior transient row (vpH=%d, frame=%d rows)", vpH, len(frame))
			}
			if _, _, ok := screenToContent(m, 0, interior); ok {
				t.Errorf("an interior transient row (%d) must not be selectable", interior)
			}
		})
	}
}

// TestRelayoutGrowsViewportWhenTransientClears proves relayout is bidirectional: a
// transient appearing shrinks the viewport, and clearing it grows it back to the
// no-transient height — driven through the SAME relayout the per-message chokepoint
// uses.
func TestRelayoutGrowsViewportWhenTransientClears(t *testing.T) {
	m, _ := selModel(t)
	baseH := m.vp.Height()

	m = withQueue(t, m, 2)
	if m.vp.Height() >= baseH {
		t.Fatalf("precondition: transient should shrink the viewport (%d → %d)", baseH, m.vp.Height())
	}

	// Clear the queue and relayout: the viewport grows back.
	m.queued = nil
	m.relayout()
	if got := m.vp.Height(); got != baseH {
		t.Errorf("after clearing the transient vpH = %d, want %d (restored)", got, baseH)
	}
}

// TestRelayoutChokepointSizesOnTransientToggle proves the END-TO-END seam: feeding a
// message through Update that toggles a transient resizes the viewport via the
// per-message relayout chokepoint, not just an explicit onResize. The footer stays
// on-screen the whole time.
func TestRelayoutChokepointSizesOnTransientToggle(t *testing.T) {
	m, _ := selModel(t)
	baseH := m.vp.Height()

	// Stage a queue, then push ANY message through Update (a spinner tick is inert but
	// runs the wrapper). The chokepoint must size the viewport down for the now-present
	// transient.
	m.queued = []string{"a", "b"}
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = model.(Model)
	if m.vp.Height() >= baseH {
		t.Errorf("after a message with a transient present vpH = %d, want < %d", m.vp.Height(), baseH)
	}
	frame := strings.Split(m.View().Content, "\n")
	if len(frame) > m.height {
		t.Errorf("frame %d rows exceeds height %d (footer clipped)", len(frame), m.height)
	}
}

// TestLayoutJoinIdenticalToManualForNoTransient guards the byte-identical refactor:
// the layout join for a no-transient frame equals the manual header+body+input+footer
// join the old View() produced. A divergence means the refactor changed render output.
func TestLayoutJoinIdenticalToManualForNoTransient(t *testing.T) {
	m, _ := selModel(t)
	body := m.vp.View()

	manual := strings.Join([]string{
		m.renderHeader(),
		body,
		inputSpacerRow, // one blank row of top padding above the input
		m.renderInput(),
		m.renderFooter(),
	}, "\n")

	got := m.assembleLayout(body).join()
	if got != manual {
		t.Errorf("layout join diverged from the manual no-transient join:\n got: %q\nwant: %q", got, manual)
	}
}

// TestLayoutJoinEqualsViewContent is a fresh-model sanity check: assembleLayout's
// join is exactly what View() renders, even straight after construction + the first
// resize (before any conversation content), so the layout model is the render path —
// not a parallel approximation of it.
func TestLayoutJoinEqualsViewContent(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	body := m.vp.View()
	if m.assembleLayout(body).join() != m.View().Content {
		t.Error("assembleLayout join must equal View().Content")
	}
}

// TestRelayoutKeepsSelectionAcrossHeightChange pins that a pure HEIGHT change (a
// transient shrinking the viewport) does NOT drop an active selection. The chokepoint
// clears a selection only via !selectable (an overlay/help/fatal taking the body) —
// never on a resize — so the anchors/head and the copy-ready byte ranges must survive
// the relayout's SetHeight + refreshView. A real press+drag builds the selection; a
// queue transient then forces the height change.
func TestRelayoutKeepsSelectionAcrossHeightChange(t *testing.T) {
	m, _ := selModel(t)
	m.relayout() // settle to the no-transient height first
	top := convTopRow(m)

	// Scroll up so the selection sits on a stable, non-tail line of the REAL
	// conversation content (selModel rendered 120 lines) — so the snapshot identity
	// check in refreshView matches the re-rendered content and a height change can't
	// reflow the selected span. (Selecting over manually-SetContent'd text would be
	// dropped: relayout's refreshView re-renders the conversation, replacing it.)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	m, _ = pressMouse(m, tea.MouseLeft, 0, top)
	m, _ = motionMouse(m, 10, top)
	if !m.sel.active {
		t.Fatal("precondition: press+drag should activate a selection")
	}
	wantAnchorL, wantAnchorC := m.sel.anchorL, m.sel.anchorC
	wantHeadL, wantHeadC := m.sel.headL, m.sel.headC
	if !strings.Contains(m.vp.View(), selectionBgSGR(t, m)) {
		t.Fatal("precondition: selection should render the highlight (selection bg SGR present)")
	}
	beforeH := m.vp.Height()

	// Stage a transient and relayout → the viewport SHRINKS (a real height change that
	// drives SetHeight + refreshView + syncStuck).
	m = withQueue(t, m, 3)
	if m.vp.Height() >= beforeH {
		t.Fatalf("precondition: transient should shrink the viewport (%d → %d)", beforeH, m.vp.Height())
	}

	// The selection survives a pure height change.
	if !m.sel.active {
		t.Error("selection must stay active across a height-only relayout")
	}
	if m.sel.anchorL != wantAnchorL || m.sel.anchorC != wantAnchorC ||
		m.sel.headL != wantHeadL || m.sel.headC != wantHeadC {
		t.Errorf("selection anchors moved: anchor (%d,%d)→(%d,%d) head (%d,%d)→(%d,%d)",
			wantAnchorL, wantAnchorC, m.sel.anchorL, m.sel.anchorC,
			wantHeadL, wantHeadC, m.sel.headL, m.sel.headC)
	}
	if !strings.Contains(m.vp.View(), selectionBgSGR(t, m)) {
		t.Error("selection highlight (bg SGR) must stay rendered after the height change")
	}
}

// TestRelayoutSyncStuckAfterTransientShrink pins the auto-follow contract across a
// transient shrink: a stuck (at-bottom) view stays pinned to the bottom after the
// viewport shrinks. relayout calls syncStuck after SetHeight (which can clamp YOffset),
// so stuck is correctly re-derived and the view does not silently unstick.
func TestRelayoutSyncStuckAfterTransientShrink(t *testing.T) {
	m, _ := selModel(t) // selModel sets stuck=true over 120 content lines
	m.relayout()
	if m.conversationView.mode != followTail || !m.vp.AtBottom() {
		t.Fatalf("precondition: view should start stuck at the bottom (stuck=%v atBottom=%v)", m.conversationView.mode == followTail, m.vp.AtBottom())
	}

	m = withQueue(t, m, 4) // shrink the viewport

	if m.conversationView.mode != followTail {
		t.Error("stuck must be preserved (still auto-following) after the shrink")
	}
	if !m.vp.AtBottom() {
		t.Error("the view must stay pinned to the bottom after the shrink")
	}
}

// TestRelayoutNoOpWhenHeightUnchanged guards the early-return / no-double-render
// contract: when the computed body height equals the current viewport height, relayout
// must NOT re-render or re-clamp. It scrolls UP (unsticking) so a stray refreshView (it
// re-pins only when stuck) or a stray syncStuck would be observable, then calls
// relayout with no layout change and asserts YOffset and stuck are untouched.
func TestRelayoutNoOpWhenHeightUnchanged(t *testing.T) {
	m, _ := selModel(t)
	m.relayout() // settle

	// Scroll up so we're NOT stuck and YOffset > 0 — a no-op relayout must leave both.
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.conversationView.mode == followTail {
		t.Fatalf("precondition: pgup should unstick the view")
	}
	beforeOff := m.vp.YOffset()
	beforeMode := m.conversationView.mode
	beforeH := m.vp.Height()

	// No transient toggled, no resize: bodyHeight == vp.Height(), so relayout early-returns.
	m.relayout()

	if m.vp.Height() != beforeH {
		t.Errorf("no-op relayout changed height %d → %d", beforeH, m.vp.Height())
	}
	if m.vp.YOffset() != beforeOff {
		t.Errorf("no-op relayout moved YOffset %d → %d (stray re-pin/clamp)", beforeOff, m.vp.YOffset())
	}
	if m.conversationView.mode != beforeMode {
		t.Errorf("no-op relayout changed stuck %v → %v (stray syncStuck)", beforeMode, m.conversationView.mode)
	}
}

// TestSelectableBodyIsViewport pins the architecture invariant from finding #1:
// whenever a selection MAY start (selectable(m) true), the rendered body region is the
// live conversation viewport (m.vp.View()) — never an overlay/help/fatal takeover.
// relayout's SetHeight and convTopRow's offset are only meaningful for the viewport
// body, and selectable() is the gate that guarantees it; if a future inline non-overlay
// body case made selectable() true while a non-viewport body rendered, this fails.
func TestSelectableBodyIsViewport(t *testing.T) {
	// A selectable model with each transient toggled (transients are NOT body
	// takeovers — they render below the body, so the body stays the viewport).
	cases := []struct {
		name string
		with func(*testing.T, Model) Model
	}{
		{"plain", func(_ *testing.T, m Model) Model { return m }},
		{"queue", func(t *testing.T, m Model) Model { return withQueue(t, m, 2) }},
		{"palette", func(t *testing.T, m Model) Model { return withPalette(t, m, 2) }},
		{"mention", func(t *testing.T, m Model) Model { return withMention(t, m, 2) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := selModel(t)
			m = tc.with(t, m)
			if !selectable(m) {
				t.Fatalf("precondition: %s model should be selectable", tc.name)
			}
			// What View() ACTUALLY rendered must equal the frame assembled with the
			// VIEWPORT as the body — i.e. View()'s body switch chose m.vp.View(), not an
			// overlay/help/fatal takeover. (assembleLayout(m.vp.View()).join() is the
			// viewport-body frame; if View() had taken over the body, its Content would
			// differ.) This is byte-identity, so a non-viewport body is caught exactly.
			if m.View().Content != m.assembleLayout(m.vp.View()).join() {
				t.Error("selectable body must be the conversation viewport (m.vp.View()), not a takeover")
			}
		})
	}
}

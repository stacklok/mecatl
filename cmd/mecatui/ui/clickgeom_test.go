package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// clickgeom_test.go covers the mouse hit-test geometry (issue #486): the
// permission modal's and plan-review action bar's buttons resolve a left-click to
// the same verdict their key chord drives. The tests drive the REAL Model to the
// awaiting-approval state (driveTo / planAskModel) and feed tea.MouseClickMsg
// through Update, asserting the resolution lands (phase + notice) and that misses
// resolve nothing.

// leftClickCmd feeds a left mouse-button PRESS through Update and returns the
// resulting Model and command, so callers can execute and inspect the approval
// frame sent by resolveAsk.
func leftClickCmd(t *testing.T, m Model, x, y int) (Model, tea.Cmd) {
	t.Helper()
	mm, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	return mm.(Model), cmd
}

// leftClick feeds a left mouse-button PRESS at (x, y) through Update and returns
// the resulting Model. The approval-button path resolves on press (no release
// needed), matching key-chord parity.
func leftClick(t *testing.T, m Model, x, y int) Model {
	t.Helper()
	m, _ = leftClickCmd(t, m, x, y)
	return m
}

// hitScan sweeps the whole frame and returns every (focus, x, y) cell
// askButtonAt reports as a hit. It is the test's handle on the hit-test's shape:
// the button boxes must appear as contiguous per-button column spans on the
// expected rows, with gaps between buttons and nothing outside.
func hitScan(m Model) map[[2]int][]int {
	hits := map[[2]int][]int{}
	for y := 0; y < m.height; y++ {
		for x := 0; x < m.width; x++ {
			if focus, ok := m.askButtonAt(x, y); ok {
				hits[[2]int{focus, y}] = append(hits[[2]int{focus, y}], x)
			}
		}
	}
	return hits
}

// xsByFocusRow collapses hitScan into focus → row → sorted column list.
func xsByFocusRow(hits map[[2]int][]int) map[int]map[int][]int {
	out := map[int]map[int][]int{}
	for k, xs := range hits {
		if out[k[0]] == nil {
			out[k[0]] = map[int][]int{}
		}
		out[k[0]][k[1]] = xs
	}
	return out
}

func TestAskButtonRectsTileTheButtonsLine(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	hk := defaultHelpKeys()
	for _, offerAlways := range []bool{true, false} {
		ask := pendingAsk{offerAlways: offerAlways}
		rects := askButtonRects(th, hk, ask, false)
		wantFoci := []int{0, 2}
		if offerAlways {
			wantFoci = []int{0, 1, 2}
		}
		if len(rects) != len(wantFoci) {
			t.Fatalf("offerAlways=%v: got %d rects, want %d", offerAlways, len(rects), len(wantFoci))
		}
		// Rects must tile left-to-right with the buttonGap separator and no overlap.
		x := 0
		for i, r := range rects {
			if r.focus != wantFoci[i] {
				t.Errorf("offerAlways=%v rect %d focus=%d, want %d", offerAlways, i, r.focus, wantFoci[i])
			}
			if r.x0 != x {
				t.Errorf("offerAlways=%v rect %d x0=%d, want %d (contiguous tiling)", offerAlways, i, r.x0, x)
			}
			if r.x1 <= r.x0 {
				t.Errorf("offerAlways=%v rect %d has non-positive width [%d,%d)", offerAlways, i, r.x0, r.x1)
			}
			x = r.x1 + buttonGap
		}
	}
}

func TestAskButtonAtGenericModalHitsEachButton(t *testing.T) {
	m := driveTo(t, theme.New("aztec", theme.AztecPalette()))
	if m.phase != phaseAwaitingApproval {
		t.Fatalf("driveTo must land in phaseAwaitingApproval, got %v", m.phase)
	}
	byFocus := xsByFocusRow(hitScan(m))
	if len(byFocus) != 3 {
		t.Fatalf("generic modal (offerAlways) must expose 3 buttons, got foci %v", keysOf(byFocus))
	}
	// Each button's columns must be a contiguous non-empty span on every row it
	// occupies, and the three buttons must not share a column on the same row.
	for focus, rows := range byFocus {
		for row, xs := range rows {
			if len(xs) == 0 {
				t.Fatalf("focus %d row %d: empty span", focus, row)
			}
			for i := 1; i < len(xs); i++ {
				if xs[i] != xs[i-1]+1 {
					t.Fatalf("focus %d row %d: non-contiguous span %v", focus, row, xs)
				}
			}
		}
	}
}

// buttonCenter finds the middle column of a button's span on its first hit row —
// the cell a real click is most likely to land on.
func buttonCenter(m Model, focus int) (int, int, bool) {
	for y := 0; y < m.height; y++ {
		var xs []int
		for x := 0; x < m.width; x++ {
			if f, ok := m.askButtonAt(x, y); ok && f == focus {
				xs = append(xs, x)
			}
		}
		if len(xs) > 0 {
			return xs[len(xs)/2], y, true
		}
	}
	return 0, 0, false
}

func TestAskButtonAtGenericModalResolvesClick(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	cases := []struct {
		focus      int
		wantNotice string
	}{
		{0, "permission allowed"},
		{1, "permission allowed (always, this session)"},
		{2, "permission denied"},
	}
	for _, tc := range cases {
		m := driveTo(t, th)
		m.deps.NoAltScreen = false
		x, y, ok := buttonCenter(m, tc.focus)
		if !ok {
			t.Fatalf("focus %d: no hit found", tc.focus)
		}
		m = leftClick(t, m, x, y)
		if m.phase != phaseRunning {
			t.Errorf("focus %d: click must resolve the modal → phaseRunning, got %v", tc.focus, m.phase)
		}
		if got := lastNotice(m); got != tc.wantNotice {
			t.Errorf("focus %d: notice = %q, want %q", tc.focus, got, tc.wantNotice)
		}
	}
}

// TestAskButtonAtGenericModalBandMatchesRenderedBox pins the hit band to the
// EXACT rows the rendered button box occupies — the off-by-one class the Spec
// review caught: the band must cover the box's top-border row and exclude the
// footnote row below it. It locates the rendered box in the frame by its border
// glyphs, then asserts the hit rows equal that span.
func TestAskButtonAtGenericModalBandMatchesRenderedBox(t *testing.T) {
	m := driveTo(t, theme.New("aztec", theme.AztecPalette()))
	frame := stripANSIstr(m.View().Content)
	lines := strings.Split(frame, "\n")
	// Find the button box's three rows in the rendered frame by their rounded
	// border/middle glyphs (the Allow button's own border-left chars).
	var boxRows []int
	for i, ln := range lines {
		if strings.Contains(ln, "╭──") || strings.Contains(ln, "│  [A]") || strings.Contains(ln, "╰──") {
			boxRows = append(boxRows, i)
		}
	}
	if len(boxRows) != 3 {
		t.Fatalf("expected to locate 3 button-box rows in the frame, found %v", boxRows)
	}
	top, bottom := boxRows[0], boxRows[2]
	// Every box row must hit; the rows immediately above and below must not.
	for y := 0; y < m.height; y++ {
		hit := false
		for x := 0; x < m.width; x++ {
			if _, ok := m.askButtonAt(x, y); ok {
				hit = true
				break
			}
		}
		want := y >= top && y <= bottom
		if hit != want {
			t.Errorf("row %d: hit=%v, want %v (button box spans rows %d–%d; the footnote row %d must NOT hit)",
				y, hit, want, top, bottom, bottom+1)
		}
	}
}

func TestAskButtonAtGenericModalMissesResolveNothing(t *testing.T) {
	m := driveTo(t, theme.New("aztec", theme.AztecPalette()))
	// A click on the modal's title row (not a button) and a click far outside the
	// card must both be swallowed: phase stays awaitingApproval, no notice appended.
	titleY := convTopRow(m) + 2 // card top border+padding lands the title here
	before := lastNotice(m)
	for _, p := range [][2]int{{m.width / 2, titleY}, {0, 0}, {m.width - 1, m.height - 1}} {
		mm := leftClick(t, m, p[0], p[1])
		if mm.phase != phaseAwaitingApproval {
			t.Errorf("click at %v: a non-button click must not resolve the modal (phase %v)", p, mm.phase)
		}
		if got := lastNotice(mm); got != before {
			t.Errorf("click at %v: a non-button click must not append a notice (got %q)", p, got)
		}
	}
}

// TestClickAtReturnsAskVerdictActions pins the region-registry path (issue
// #555): the hit-test returns ClickAction payloads (not bare focus ints), each
// approval button emits exactly one clickAskVerdict region, and the regions
// tile the button rows with no overlap.
func TestClickAtReturnsAskVerdictActions(t *testing.T) {
	m := driveTo(t, theme.New("aztec", theme.AztecPalette()))
	regions := m.approvalClickRegions()
	if len(regions) != 3 {
		t.Fatalf("a three-button modal must emit 3 regions, got %d", len(regions))
	}
	wantFoci := []int{0, 1, 2}
	for i, r := range regions {
		if r.action.kind != clickAskVerdict {
			t.Errorf("region %d: action kind = %v, want clickAskVerdict", i, r.action.kind)
		}
		if r.action.focus != wantFoci[i] {
			t.Errorf("region %d: action focus = %d, want %d", i, r.action.focus, wantFoci[i])
		}
		if r.rect.x1 <= r.rect.x0 || r.rect.y1 <= r.rect.y0 {
			t.Errorf("region %d: empty rect %v", i, r.rect)
		}
	}
	// clickAt returns the region's action for a cell inside it, and misses outside.
	for _, r := range regions {
		act, ok := m.clickAt(r.rect.x0, r.rect.y0)
		if !ok || act != r.action {
			t.Errorf("clickAt(%d,%d) = %+v, %v; want %+v", r.rect.x0, r.rect.y0, act, ok, r.action)
		}
	}
	if _, ok := m.clickAt(0, 0); ok {
		t.Error("a click at the frame corner must miss every approval region")
	}
}

// TestClickAtOutsideApprovalPhaseIsEmpty pins that the registry reports no
// regions when no ask owns the body (a click resolves nothing at idle/running).
func TestClickAtOutsideApprovalPhaseIsEmpty(t *testing.T) {
	m := driveTo(t, theme.New("aztec", theme.AztecPalette()))
	m.phase = phaseRunning
	if got := m.approvalClickRegions(); len(got) != 0 {
		t.Errorf("phaseRunning must emit no approval regions, got %d", len(got))
	}
	if _, ok := m.clickAt(m.width/2, m.height/2); ok {
		t.Error("no click may resolve outside phaseAwaitingApproval")
	}
}

func TestAskButtonAtTwoButtonModalHasNoMiddle(t *testing.T) {
	// A surfaced child ask (offerAlways=false) renders Allow · Deny only — the
	// hit-test must expose exactly foci {0, 2}, never a middle "always" rect.
	m := driveTo(t, theme.New("aztec", theme.AztecPalette()))
	m.approval.ask.offerAlways = false
	byFocus := xsByFocusRow(hitScan(m))
	if _, ok := byFocus[1]; ok {
		t.Error("two-button modal must not expose a focus=1 (always) hit")
	}
	for _, f := range []int{0, 2} {
		if len(byFocus[f]) == 0 {
			t.Errorf("two-button modal must expose focus=%d", f)
		}
	}
}

func TestAskButtonAtPlanBarResolvesClick(t *testing.T) {
	cases := []struct {
		focus      int
		wantNotice string
	}{
		{0, "permission allowed"},
		{1, "permission allowed (always, this session)"},
		{2, "permission denied"},
	}
	for _, tc := range cases {
		m := planAskModel(t, true)
		m.deps.NoAltScreen = false
		x, y, found := buttonCenter(m, tc.focus)
		if !found {
			t.Fatalf("plan bar focus %d: no hit found", tc.focus)
		}
		m = leftClick(t, m, x, y)
		if m.phase != phaseRunning {
			t.Errorf("plan bar focus %d: click must resolve → phaseRunning, got %v", tc.focus, m.phase)
		}
		if got := lastNotice(m); got != tc.wantNotice {
			t.Errorf("plan bar focus %d: notice = %q, want %q", tc.focus, got, tc.wantNotice)
		}
	}
}

// TestPlanRenderedButtonLabelClickSendsApproval starts from the displayed label's
// actual frame coordinates, not hit-test geometry, then executes the returned
// command and verifies the outgoing approval correlation and verdict.
func TestPlanRenderedButtonLabelClickSendsApproval(t *testing.T) {
	m := planAskModel(t, true)
	m.deps.NoAltScreen = false
	send := &fakeSender{}
	m.stream = client.NewStream(&fakeRecver{}, send)

	lines := strings.Split(stripANSIstr(m.View().Content), "\n")
	const label = "[A]pprove & run"
	for y, line := range lines {
		if x := strings.Index(line, label); x >= 0 {
			var cmd tea.Cmd
			m, cmd = leftClickCmd(t, m, x, y)
			runBatchLeaves(cmd)
			apps := approvalFrames(send)
			if len(apps) != 1 {
				t.Fatalf("click on rendered label sent %d approval frames, want 1", len(apps))
			}
			if apps[0].GetAskId() != "sess-test-0001:1:presentplan-1" || !apps[0].GetAllow() {
				t.Fatalf("approval = ask_id=%q allow=%v, want plan ask allow-once", apps[0].GetAskId(), apps[0].GetAllow())
			}
			return
		}
	}
	t.Fatalf("rendered frame did not contain %q", label)
}

func TestPlanRenderedFootnoteAndBlankRowsDoNotHit(t *testing.T) {
	m := planAskModel(t, true)
	lines := strings.Split(stripANSIstr(m.View().Content), "\n")
	foundFootnote, foundBlank := false, false
	for y, line := range lines {
		isFootnote := strings.Contains(line, "auto-accept allows every edit")
		isBlank := strings.TrimSpace(line) == ""
		if !isFootnote && !isBlank {
			continue
		}
		if isFootnote {
			foundFootnote = true
		}
		if isBlank {
			foundBlank = true
		}
		for x := 0; x < m.width; x++ {
			if _, ok := m.askButtonAt(x, y); ok {
				t.Fatalf("rendered non-button row %d (%q) hit a button at x=%d", y, line, x)
			}
		}
	}
	if !foundFootnote || !foundBlank {
		t.Fatalf("frame must contain a footnote and blank row; footnote=%v blank=%v", foundFootnote, foundBlank)
	}
}

func TestApprovalMouseClickRequiresCapture(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(*Model)
	}{
		{"inline", func(m *Model) { m.deps.NoAltScreen = true }},
		{"no mouse", func(m *Model) { m.deps.NoMouse = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := planAskModel(t, true)
			send := &fakeSender{}
			m.stream = client.NewStream(&fakeRecver{}, send)
			x, y, ok := buttonCenter(m, 0)
			if !ok {
				t.Fatal("precondition: allow button must have hit geometry")
			}
			tc.apply(&m)
			m, cmd := leftClickCmd(t, m, x, y)
			if cmd != nil || m.phase != phaseAwaitingApproval || len(approvalFrames(send)) != 0 {
				t.Fatalf("uncaptured click resolved approval: cmd=%v phase=%v frames=%d", cmd != nil, m.phase, len(approvalFrames(send)))
			}
		})
	}
}

// TestAskButtonAtArgsViewHitsEachButton pins the full-screen ask-args view's
// hit-test (issue #488): with the view open, the GENERIC permission buttons sit
// on the pinned bar at the bottom of the body region and each resolves a click
// to its verdict — the SAME buttons as the modal (not the plan wording).
func TestAskButtonAtArgsViewHitsEachButton(t *testing.T) {
	m := openArgsView(t, bashAskModel(t, longBashArgs))
	byFocus := xsByFocusRow(hitScan(m))
	if len(byFocus) != 3 {
		t.Fatalf("the args view (offerAlways) must expose 3 buttons, got foci %v", keysOf(byFocus))
	}
	// The buttons must sit on the ask-view layout's buttons row, not wherever the
	// centered modal would put them.
	layout := m.argsReviewLayout(m.approval.ask)
	wantRow := convTopRow(m) + layout.buttonsRow
	for focus, rows := range byFocus {
		for row := range rows {
			if row < wantRow || row >= wantRow+layout.buttonsHeight {
				t.Errorf("focus %d hit on row %d, want within [%d,%d)", focus, row, wantRow, wantRow+layout.buttonsHeight)
			}
		}
	}
	// A click on the allow button resolves the ask (key-chord parity).
	m.deps.NoAltScreen = false
	x, y, ok := buttonCenter(m, 0)
	if !ok {
		t.Fatal("allow button must have hit geometry in the args view")
	}
	m = leftClick(t, m, x, y)
	if m.phase != phaseRunning {
		t.Errorf("clicking allow inside the args view must resolve → phaseRunning, got %v", m.phase)
	}
	if got := lastNotice(m); got != "permission allowed" {
		t.Errorf("notice = %q, want 'permission allowed'", got)
	}
}

// TestAskButtonAtLongArgsModalStable pins that a long-args modal (wrapped,
// capped mini-viewport + hint line) still hits its buttons exactly — the added
// region rows and the hint line never swallow the button band.
func TestAskButtonAtLongArgsModalStable(t *testing.T) {
	m := bashAskModel(t, longBashArgs)
	byFocus := xsByFocusRow(hitScan(m))
	if len(byFocus) != 3 {
		t.Fatalf("the long-args modal must expose 3 buttons, got foci %v", keysOf(byFocus))
	}
	body, buttonsRow := m.permissionModalBody()
	card := m.deps.Theme.Style("askCard").Render(body)
	_, originY := centeredCardOrigin(lipgloss.Width(card), lipgloss.Height(card), m.width, m.vp.Height())
	style := m.deps.Theme.Style("askCard")
	wantRow := convTopRow(m) + originY + style.GetBorderTopSize() + style.GetPaddingTop() + buttonsRow
	for focus, rows := range byFocus {
		for row := range rows {
			if row != wantRow && row != wantRow+1 && row != wantRow+2 {
				t.Errorf("focus %d hit on row %d, want the button box rows from %d", focus, row, wantRow)
			}
		}
	}
}

func TestAskButtonAtRequiresApprovalPhase(t *testing.T) {
	// Outside phaseAwaitingApproval the hit-test is inert: a click where a button
	// WOULD be resolves nothing (the conversation path owns the click then).
	m := driveTo(t, theme.New("aztec", theme.AztecPalette()))
	m.phase = phaseRunning
	for y := 0; y < m.height; y++ {
		for x := 0; x < m.width; x++ {
			if _, ok := m.askButtonAt(x, y); ok {
				t.Fatalf("askButtonAt must be gated on phaseAwaitingApproval (hit at %d,%d in phaseRunning)", x, y)
			}
		}
	}
}

func keysOf(m map[int]map[int][]int) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

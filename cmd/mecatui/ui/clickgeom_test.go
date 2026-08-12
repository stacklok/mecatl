package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// clickgeom_test.go covers the mouse hit-test geometry (issue #486): the
// permission modal's and plan-review action bar's buttons resolve a left-click to
// the same verdict their key chord drives. The tests drive the REAL Model to the
// awaiting-approval state (driveTo / planAskModel) and feed tea.MouseClickMsg
// through Update, asserting the resolution lands (phase + notice) and that misses
// resolve nothing.

// leftClick feeds a left mouse-button PRESS at (x, y) through Update and returns
// the resulting Model. The approval-button path resolves on press (no release
// needed), matching key-chord parity.
func leftClick(t *testing.T, m Model, x, y int) Model {
	t.Helper()
	mm, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	return mm.(Model)
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

func TestAskButtonAtTwoButtonModalHasNoMiddle(t *testing.T) {
	// A surfaced child ask (offerAlways=false) renders Allow · Deny only — the
	// hit-test must expose exactly foci {0, 2}, never a middle "always" rect.
	m := driveTo(t, theme.New("aztec", theme.AztecPalette()))
	m.ask.offerAlways = false
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

func TestAskButtonAtPlanBarClampedOffFootnote(t *testing.T) {
	// The 3-row button box overflows the 3-row plan bar; the hit band must be
	// clamped so a click on the footnote/scroll-hint rows resolves nothing.
	m := planAskModel(t, true)
	top := convTopRow(m)
	barRow := top + m.vp.Height() - planReviewFooterHeight
	// Footnote row = barRow+2 (buttons box rows 0,1 then the footnote). A click
	// anywhere on it must miss.
	for x := 0; x < m.width; x++ {
		if _, ok := m.askButtonAt(x, barRow+2); ok {
			t.Fatalf("footnote row (barRow+2=%d) must not hit a button (x=%d)", barRow+2, x)
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

package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// geom_test.go covers the mouse hit-test geometry (issue #486): the
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
	_ = m.View() // pointer input may resolve only against the current rendered frame
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

type frameApprovalRegion struct {
	rect    cellRect
	verdict client.Verdict
}

// frameApprovalRegions reads only the active View frame; it does not recreate
// approval geometry in the test.
func frameApprovalRegions(m Model) []frameApprovalRegion {
	if m.phase != phaseAwaitingApproval {
		return nil
	}
	_ = m.View()
	s, ok := m.modal.(*approvalSurface)
	if !ok {
		return nil
	}
	regions := make([]frameApprovalRegion, 0, len(m.hits.frame))
	for _, hit := range m.hits.frame {
		if verdict, ok := s.hits[hit.id]; ok {
			x0, y0 := m.metrics.localToGlobal(hit.rect.x0, hit.rect.y0)
			x1, y1 := m.metrics.localToGlobal(hit.rect.x1, hit.rect.y1)
			regions = append(regions, frameApprovalRegion{rect: cellRect{x0: x0, x1: x1, y0: y0, y1: y1}, verdict: verdict})
		}
	}
	return regions
}

func frameVerdictAt(regions []frameApprovalRegion, x, y int) (client.Verdict, bool) {
	for _, region := range regions {
		if region.rect.contains(x, y) {
			return region.verdict, true
		}
	}
	return client.VerdictAllowOnce, false
}

// hitScan sweeps one rendered frame and returns every (verdict, x, y) cell the
// hit map reports. The button boxes must appear as contiguous per-verdict
// column spans on the expected rows, with gaps between buttons and nothing outside.
func hitScan(m Model) map[client.Verdict]map[int][]int {
	regions := frameApprovalRegions(m)
	hits := map[client.Verdict]map[int][]int{}
	for y := 0; y < m.height; y++ {
		for x := 0; x < m.width; x++ {
			if verdict, ok := frameVerdictAt(regions, x, y); ok {
				if hits[verdict] == nil {
					hits[verdict] = map[int][]int{}
				}
				hits[verdict][y] = append(hits[verdict][y], x)
			}
		}
	}
	return hits
}

func TestAskButtonRectsTileTheButtonsLine(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	hk := defaultHelpKeys()
	for _, offerAlways := range []bool{true, false} {
		ask := pendingAsk{offerAlways: offerAlways}
		rects := askButtonRects(th, hk, ask, false)
		wantVerdicts := []client.Verdict{client.VerdictAllowOnce, client.VerdictDeny}
		if offerAlways {
			wantVerdicts = []client.Verdict{client.VerdictAllowOnce, client.VerdictAllowAlways, client.VerdictDeny}
		}
		if len(rects) != len(wantVerdicts) {
			t.Fatalf("offerAlways=%v: got %d rects, want %d", offerAlways, len(rects), len(wantVerdicts))
		}
		// Rects must tile left-to-right with the buttonGap separator and no overlap.
		x := 0
		for i, r := range rects {
			if r.verdict != wantVerdicts[i] {
				t.Errorf("offerAlways=%v rect %d verdict=%v, want %v", offerAlways, i, r.verdict, wantVerdicts[i])
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
	byVerdict := hitScan(m)
	if len(byVerdict) != 3 {
		t.Fatalf("generic modal (offerAlways) must expose 3 buttons, got %d", len(byVerdict))
	}
	// Each button's columns must be a contiguous non-empty span on every row it
	// occupies, and the three buttons must not share a column on the same row.
	for verdict, rows := range byVerdict {
		for row, xs := range rows {
			if len(xs) == 0 {
				t.Fatalf("verdict %v row %d: empty span", verdict, row)
			}
			for i := 1; i < len(xs); i++ {
				if xs[i] != xs[i-1]+1 {
					t.Fatalf("verdict %v row %d: non-contiguous span %v", verdict, row, xs)
				}
			}
		}
	}
}

// buttonCenter finds the middle cell of a verdict button's first hit region.
func buttonCenter(m Model, verdict client.Verdict) (int, int, bool) {
	for _, region := range frameApprovalRegions(m) {
		if region.verdict == verdict {
			return (region.rect.x0 + region.rect.x1 - 1) / 2, region.rect.y0, true
		}
	}
	return 0, 0, false
}

func TestAskButtonAtGenericModalResolvesClick(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	cases := []struct {
		verdict    client.Verdict
		wantNotice string
	}{
		{client.VerdictAllowOnce, "permission allowed"},
		{client.VerdictAllowAlways, "permission allowed (always, this session)"},
		{client.VerdictDeny, "permission denied"},
	}
	for _, tc := range cases {
		m := driveTo(t, th)
		m.deps.NoAltScreen = false
		x, y, ok := buttonCenter(m, tc.verdict)
		if !ok {
			t.Fatalf("verdict %v: no hit found", tc.verdict)
		}
		m = leftClick(t, m, x, y)
		if m.phase != phaseRunning {
			t.Errorf("verdict %v: click must resolve the modal → phaseRunning, got %v", tc.verdict, m.phase)
		}
		if got := lastNotice(m); got != tc.wantNotice {
			t.Errorf("verdict %v: notice = %q, want %q", tc.verdict, got, tc.wantNotice)
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
	regions := frameApprovalRegions(m)
	for y := 0; y < m.height; y++ {
		hit := false
		for x := 0; x < m.width; x++ {
			if _, ok := frameVerdictAt(regions, x, y); ok {
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

// TestClickAtReturnsAskVerdicts pins the region-registry path: each approval
// button emits exactly one verdict region, and the regions tile the button rows
// with no overlap.
func TestClickAtReturnsAskVerdicts(t *testing.T) {
	m := driveTo(t, theme.New("aztec", theme.AztecPalette()))
	regions := frameApprovalRegions(m)
	if len(regions) != 3 {
		t.Fatalf("a three-button modal must emit 3 regions, got %d", len(regions))
	}
	wantVerdicts := []client.Verdict{client.VerdictAllowOnce, client.VerdictAllowAlways, client.VerdictDeny}
	for i, r := range regions {
		if r.verdict != wantVerdicts[i] {
			t.Errorf("region %d: verdict = %v, want %v", i, r.verdict, wantVerdicts[i])
		}
		if r.rect.x1 <= r.rect.x0 || r.rect.y1 <= r.rect.y0 {
			t.Errorf("region %d: empty rect %v", i, r.rect)
		}
	}
	// frameVerdictAt returns the region's verdict for a cell inside it, and misses outside.
	for _, r := range regions {
		verdict, ok := frameVerdictAt(regions, r.rect.x0, r.rect.y0)
		if !ok || verdict != r.verdict {
			t.Errorf("frameVerdictAt(%d,%d) = %v, %v; want %v", r.rect.x0, r.rect.y0, verdict, ok, r.verdict)
		}
	}
	if _, ok := frameVerdictAt(regions, 0, 0); ok {
		t.Error("a click at the frame corner must miss every approval region")
	}
}

// TestClickAtOutsideApprovalPhaseIsEmpty pins that the registry reports no
// regions when no ask owns the body (a click resolves nothing at idle/running).
func TestClickAtOutsideApprovalPhaseIsEmpty(t *testing.T) {
	m := driveTo(t, theme.New("aztec", theme.AztecPalette()))
	m.phase = phaseRunning
	if got := frameApprovalRegions(m); len(got) != 0 {
		t.Errorf("phaseRunning must emit no approval regions, got %d", len(got))
	}
	if _, ok := frameVerdictAt(frameApprovalRegions(m), m.width/2, m.height/2); ok {
		t.Error("no click may resolve outside phaseAwaitingApproval")
	}
}

func TestAskButtonAtTwoButtonModalHasNoAlways(t *testing.T) {
	// A surfaced child ask (offerAlways=false) renders Allow · Deny only — the
	// hit-test must expose exactly Allow Once and Deny, never Allow Always.
	m := driveTo(t, theme.New("aztec", theme.AztecPalette()))
	approvalSurfaceOf(t, m).ask.offerAlways = false
	byVerdict := hitScan(m)
	if _, ok := byVerdict[client.VerdictAllowAlways]; ok {
		t.Error("two-button modal must not expose an Allow Always hit")
	}
	for _, verdict := range []client.Verdict{client.VerdictAllowOnce, client.VerdictDeny} {
		if len(byVerdict[verdict]) == 0 {
			t.Errorf("two-button modal must expose verdict=%v", verdict)
		}
	}
}

func TestAskButtonAtPlanBarResolvesClick(t *testing.T) {
	cases := []struct {
		verdict    client.Verdict
		wantNotice string
	}{
		{client.VerdictAllowOnce, "permission allowed"},
		{client.VerdictAllowAlways, "permission allowed (always, this session)"},
		{client.VerdictDeny, "permission denied"},
	}
	for _, tc := range cases {
		m := planAskModel(t, true)
		m.deps.NoAltScreen = false
		x, y, found := buttonCenter(m, tc.verdict)
		if !found {
			t.Fatalf("plan bar verdict %v: no hit found", tc.verdict)
		}
		m = leftClick(t, m, x, y)
		if m.phase != phaseRunning {
			t.Errorf("plan bar verdict %v: click must resolve → phaseRunning, got %v", tc.verdict, m.phase)
		}
		if got := lastNotice(m); got != tc.wantNotice {
			t.Errorf("plan bar verdict %v: notice = %q, want %q", tc.verdict, got, tc.wantNotice)
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
	regions := frameApprovalRegions(m)
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
			if _, ok := frameVerdictAt(regions, x, y); ok {
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
			x, y, ok := buttonCenter(m, client.VerdictAllowOnce)
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
	m := openArgsView(t, bashAskModel(t, longShellArgs))
	byVerdict := hitScan(m)
	if len(byVerdict) != 3 {
		t.Fatalf("the args view (offerAlways) must expose 3 buttons, got %d", len(byVerdict))
	}
	// The buttons must sit on the ask-view layout's buttons row, not wherever the
	// centered modal would put them.
	s := approvalSurfaceOf(t, m)
	layout := s.argsLayout()
	wantRow := convTopRow(m) + layout.buttonsRow
	for verdict, rows := range byVerdict {
		for row := range rows {
			if row < wantRow || row >= wantRow+layout.buttonsHeight {
				t.Errorf("verdict %v hit on row %d, want within [%d,%d)", verdict, row, wantRow, wantRow+layout.buttonsHeight)
			}
		}
	}
	// A click on the allow button resolves the ask (key-chord parity).
	m.deps.NoAltScreen = false
	x, y, ok := buttonCenter(m, client.VerdictAllowOnce)
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
	m := bashAskModel(t, longShellArgs)
	byVerdict := hitScan(m)
	if len(byVerdict) != 3 {
		t.Fatalf("the long-args modal must expose 3 buttons, got %d", len(byVerdict))
	}
	for verdict, rows := range byVerdict {
		for row := range rows {
			if row < 0 || row >= m.height {
				t.Errorf("verdict %v hit row %d is outside the rendered frame", verdict, row)
			}
		}
	}
}

func TestAskButtonAtRequiresApprovalPhase(t *testing.T) {
	// Outside phaseAwaitingApproval the hit-test is inert: a click where a button
	// WOULD be resolves nothing (the conversation path owns the click then).
	m := driveTo(t, theme.New("aztec", theme.AztecPalette()))
	m.phase = phaseRunning
	if regions := frameApprovalRegions(m); len(regions) != 0 {
		t.Fatalf("askButtonAt must be gated on phaseAwaitingApproval, got %d hit regions in phaseRunning", len(regions))
	}
}

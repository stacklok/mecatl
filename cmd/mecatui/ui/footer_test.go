package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func TestHumanizeTokens(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{999, "999"},
		{1000, "1K"},
		{2000, "2K"},
		{7903, "7.9K"},
		{1500, "1.5K"},
		{999999, "1000K"},
		{1_000_000, "1M"},
		{1_200_000, "1.2M"},
		{-5, "0"},
	}
	for _, c := range cases {
		if got := humanizeTokens(c.in); got != c.want {
			t.Errorf("humanizeTokens(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCacheHitRate(t *testing.T) {
	cases := []struct {
		name string
		u    client.Usage
		want float64
	}{
		{"zero input", client.Usage{}, 0},
		{"half", client.Usage{InputTokens: 100, CacheReadTokens: 50}, 0.5},
		{"full", client.Usage{InputTokens: 100, CacheReadTokens: 100}, 1.0},
		{"88pct", client.Usage{InputTokens: 1500, CacheReadTokens: 1320}, 0.88},
	}
	for _, c := range cases {
		got := cacheHitRate(c.u)
		if diff := got - c.want; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("%s: cacheHitRate = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPctString(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0%"},
		{0.5, "50%"},
		{0.881, "88%"},
		{1.0, "100%"},
		{1.5, "100%"},
		{-0.2, "0%"},
	}
	for _, c := range cases {
		if got := pctString(c.in); got != c.want {
			t.Errorf("pctString(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRenderContextMeterUnknownWindow(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	got := renderContextMeter(th, 7903, 0)
	if got != "ctx 7.9K" {
		t.Errorf("unknown window: got %q, want %q", got, "ctx 7.9K")
	}
	// No bar glyphs when the window is unknown.
	if strings.ContainsAny(got, ctxGlyphOk+ctxGlyphWarn+ctxGlyphDanger+ctxGlyphEmpty) {
		t.Errorf("unknown window should have no bar, got %q", got)
	}
	// Negative used clamps to 0.
	if got := renderContextMeter(th, -10, 0); got != "ctx 0" {
		t.Errorf("negative used: got %q, want %q", got, "ctx 0")
	}
}

func TestRenderContextMeterKnownWindow(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	// 40K / 200K = 20% → ok band.
	got := stripANSIstr(renderContextMeter(th, 40000, 200000))
	if !strings.Contains(got, "20%") {
		t.Errorf("expected 20%% in %q", got)
	}
	if !strings.Contains(got, "40K/200K") {
		t.Errorf("expected used/total 40K/200K in %q", got)
	}
	if !strings.ContainsAny(got, ctxGlyphOk+ctxGlyphEmpty) {
		t.Errorf("expected a bar in %q", got)
	}
}

// TestContextMeterPressureColours asserts each pressure band selects its own
// theme slot (colours differ) by comparing the raw ANSI output across bands.
func TestContextMeterPressureColours(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	const window = 200000
	ok := renderContextMeter(th, 40000, window)      // 20% → ctxOk
	warn := renderContextMeter(th, 140000, window)   // 70% → ctxWarn
	danger := renderContextMeter(th, 190000, window) // 95% → ctxDanger

	if ok == warn || warn == danger || ok == danger {
		t.Errorf("expected distinct colouring across pressure bands:\nok=%q\nwarn=%q\ndanger=%q", ok, warn, danger)
	}
	// Sanity: slot selection by fraction.
	if s := ctxPressureSlot(0.2); s != "ctxOk" {
		t.Errorf("ctxPressureSlot(0.2) = %q, want ctxOk", s)
	}
	if s := ctxPressureSlot(0.70); s != "ctxWarn" {
		t.Errorf("ctxPressureSlot(0.70) = %q, want ctxWarn", s)
	}
	if s := ctxPressureSlot(0.90); s != "ctxDanger" {
		t.Errorf("ctxPressureSlot(0.90) = %q, want ctxDanger", s)
	}
}

// TestContextMeterPressureNonColour is the ACCESSIBILITY guard: pressure must be
// legible WITHOUT colour. It strips ANSI and asserts the three bands still
// differ — via the per-band fill glyph and the danger ⚠ marker — so a
// red/green-colourblind user (or anyone reading the stripped golden) can tell a
// 5% context from a 95% one.
func TestContextMeterPressureNonColour(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	const window = 200000
	ok := stripANSIstr(renderContextMeter(th, 80000, window))      // 40% → ok (fills cells)
	warn := stripANSIstr(renderContextMeter(th, 140000, window))   // 70% → warn
	danger := stripANSIstr(renderContextMeter(th, 190000, window)) // 95% → danger

	if ok == warn || warn == danger || ok == danger {
		t.Errorf("pressure must differ with ANSI stripped:\nok=%q\nwarn=%q\ndanger=%q", ok, warn, danger)
	}
	// The bands carry distinct fill glyphs.
	if !strings.Contains(ok, ctxGlyphOk) {
		t.Errorf("ok band should use %q glyph: %q", ctxGlyphOk, ok)
	}
	if !strings.Contains(warn, ctxGlyphWarn) {
		t.Errorf("warn band should use %q glyph: %q", ctxGlyphWarn, warn)
	}
	if !strings.Contains(danger, ctxGlyphDanger) {
		t.Errorf("danger band should use %q glyph: %q", ctxGlyphDanger, danger)
	}
	// Danger also appends a non-colour textual cue; the others must not.
	if !strings.Contains(danger, ctxDangerMark) {
		t.Errorf("danger band should append the ⚠ marker: %q", danger)
	}
	if strings.Contains(ok, ctxDangerMark) || strings.Contains(warn, ctxDangerMark) {
		t.Errorf("only the danger band may show the ⚠ marker")
	}
}

func TestRenderUsageFacets(t *testing.T) {
	got := renderUsageFacets(client.Usage{
		InputTokens:      7903,
		OutputTokens:     345,
		CacheReadTokens:  6955,
		CacheWriteTokens: 1200,
	})
	for _, want := range []string{"↑7.9K", "↓345", "⊕1.2K", "cache 88%"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in facets %q", want, got)
		}
	}
}

// TestRenderUsageFacetsOmitsZeroCacheWrite keeps the segment scannable: the
// cache-write facet is hidden when zero.
func TestRenderUsageFacetsOmitsZeroCacheWrite(t *testing.T) {
	got := renderUsageFacets(client.Usage{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 50})
	if strings.Contains(got, "⊕") {
		t.Errorf("zero cache-write should be omitted, got %q", got)
	}
}

// stripANSIstr is a string convenience over stripANSI for assertions.
func stripANSIstr(s string) string { return string(stripANSI([]byte(s))) }

// TestTurnStatLine pins the per-turn stat line: it always leads with the
// input/output token arrows, appends the duration when the server reported one,
// and appends "N% cached" ONLY when the turn's cache-hit rate is at or above
// turnStatCacheFloor (decision 4). A negligible cache rate is omitted to keep the
// line scannable.
func TestTurnStatLine(t *testing.T) {
	cases := []struct {
		name       string
		usage      client.Usage
		durationMs int64
		wantSubs   []string
		absentSubs []string
	}{
		{
			name:       "no cache facet below floor",
			usage:      client.Usage{InputTokens: 1200, OutputTokens: 340}, // 0% cache
			durationMs: 4100,
			wantSubs:   []string{"↑1.2K", "↓340", "4.1s"},
			absentSubs: []string{"cached"},
		},
		{
			name:       "cache facet when material",
			usage:      client.Usage{InputTokens: 1500, OutputTokens: 30, CacheReadTokens: 1320}, // 88%
			durationMs: 0,
			wantSubs:   []string{"↑1.5K", "↓30", "88% cached"},
			absentSubs: []string{" · 4"}, // no duration segment when 0ms
		},
		{
			name:       "just under the floor is omitted",
			usage:      client.Usage{InputTokens: 1000, OutputTokens: 10, CacheReadTokens: 90}, // 9% < 10%
			durationMs: 0,
			wantSubs:   []string{"↑1K", "↓10"},
			absentSubs: []string{"cached"},
		},
		{
			name:       "exactly at the floor is shown",
			usage:      client.Usage{InputTokens: 1000, OutputTokens: 10, CacheReadTokens: 100}, // 10%
			durationMs: 0,
			wantSubs:   []string{"10% cached"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := turnStatLine(client.TurnEndMsg{Usage: c.usage, DurationMs: c.durationMs})
			for _, sub := range c.wantSubs {
				if !strings.Contains(got, sub) {
					t.Errorf("turnStatLine = %q, want it to contain %q", got, sub)
				}
			}
			for _, sub := range c.absentSubs {
				if strings.Contains(got, sub) {
					t.Errorf("turnStatLine = %q, must NOT contain %q", got, sub)
				}
			}
		})
	}
}

// TestTurnStatCacheReachesScrollback is the integration guard for the per-turn cache
// facet: a TurnEndMsg carrying a material cache rate, driven through the REAL update
// path (TurnEndMsg → addTurnStat(turnStatLine) → blockTurnStat → renderBlockFresh),
// must surface "% cached" in the rendered conversation scrollback. TestTurnStatLine
// tests the formatter in isolation; this proves the string actually reaches a rendered
// block (the trivialTurn gate, the conversation append, and the block render all wired).
func TestTurnStatCacheReachesScrollback(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001"},
		// Non-trivial tokens (so the stat line is not suppressed) with an 88% cache rate.
		client.TurnEndMsg{Turn: 1, Usage: client.Usage{InputTokens: 1500, OutputTokens: 300, CacheReadTokens: 1320}, DurationMs: 4100},
	)
	got := stripANSIstr(m.rend.renderConversation(&m.conv, m.expandTools))
	if !strings.Contains(got, "88% cached") {
		t.Errorf("rendered scrollback missing the per-turn cache facet %q; got %q", "88% cached", got)
	}
}

// TestFooterSelectionCount: an idle model with a known multi-line, non-empty
// selection shows the live "N chars · M lines" count in the footer-left.
func TestFooterSelectionCount(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent("hello world\nsecond line\nthird row")
	// Select "world\nsecond line\nthird" — line0col6 .. line2col5.
	m.sel = selection{active: true, anchorL: 0, anchorC: 6, headL: 2, headC: 5}
	m.phase = phaseIdle
	got := stripANSIstr(m.renderFooter())
	// "world" (5) + "\n" + "second line" (11) + "\n" + "third" (5) = 23 chars.
	if !strings.Contains(got, "23 chars · 3 lines") {
		t.Errorf("footer = %q, want it to contain %q", got, "23 chars · 3 lines")
	}
}

// TestFooterSelectionCountSingular: a one-char, one-line selection uses the
// singular nouns "1 char · 1 line".
func TestFooterSelectionCountSingular(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent("hello world\nsecond line")
	// Select a single character on line0: col0..col1 ("h").
	m.sel = selection{active: true, anchorL: 0, anchorC: 0, headL: 0, headC: 1}
	m.phase = phaseIdle
	got := stripANSIstr(m.renderFooter())
	if !strings.Contains(got, "1 char · 1 line") {
		t.Errorf("footer = %q, want it to contain %q", got, "1 char · 1 line")
	}
}

// TestFooterSelectionCountAfterCopy: after a copy (statusMsg carries "copied …")
// while the selection is still active, the footer prefixes the count with
// "copied · " — the selection persists past the copy (Req 7). The selection is
// made via the REAL drag gesture so the identity snapshot matches and the
// copy→refreshView path KEEPS it (a manual SetContent would be overwritten by the
// conversation re-render inside refreshView).
func TestFooterSelectionCountAfterCopy(t *testing.T) {
	m, _ := selModel(t)
	top := convTopRow(m)
	// Drag-select a span on the first viewport line.
	m, _ = pressMouse(m, tea.MouseLeft, 0, top)
	m, _ = motionMouse(m, 10, top)
	if !m.sel.active || m.sel.empty() {
		t.Fatal("precondition: an active non-empty selection")
	}
	// Compute the count the footer should report from the model's own state.
	chars := len([]rune(selectedText(m.vp.GetContent(), m.sel)))
	startL, _, endL, _ := m.sel.normalize()
	wantLines := endL - startL + 1
	want := fmt.Sprintf("copied · %s · %s", plural(chars, "char"), plural(wantLines, "line"))

	updated, _ := m.copySelection()
	m = updated.(Model)
	if !m.sel.active {
		t.Fatal("selection must persist past a copy (Req 7)")
	}
	got := stripANSIstr(m.renderFooter())
	if !strings.Contains(got, want) {
		t.Errorf("footer = %q, want it to contain %q", got, want)
	}
}

// TestFooterNoSelectionShowsStatus: with no active selection the footer shows the
// existing statusMsg, or "ready" when it's empty.
func TestFooterNoSelectionShowsStatus(t *testing.T) {
	m, _ := selModel(t)
	m.sel = selection{} // inactive
	m.phase = phaseIdle
	m.statusMsg = ""
	if got := stripANSIstr(m.renderFooter()); !strings.Contains(got, "ready") {
		t.Errorf("footer = %q, want it to contain %q", got, "ready")
	}
	m.statusMsg = "connected"
	if got := stripANSIstr(m.renderFooter()); !strings.Contains(got, "connected") {
		t.Errorf("footer = %q, want it to contain %q", got, "connected")
	}
}

// TestFooterSelectionCountSuppressedWhileRunning: a running phase owns the
// footer-left (spinner path), so even with an active selection the count is NOT
// shown — the count is idle/default-only by construction (Req 5).
func TestFooterSelectionCountSuppressedWhileRunning(t *testing.T) {
	m, _ := selModel(t)
	m.vp.SetContent("hello world\nsecond line\nthird row")
	m.sel = selection{active: true, anchorL: 0, anchorC: 6, headL: 2, headC: 5}
	m.phase = phaseRunning
	got := stripANSIstr(m.renderFooter())
	if strings.Contains(got, "chars · ") {
		t.Errorf("footer while running must NOT show the selection count, got %q", got)
	}
}

// TestStopReasonLabel locks the human phrasing + style slot for every stop
// reason in the session.StopReason / proto Result.stop vocabulary, plus the
// empty and unknown fallbacks. The limit stops carry the warning slot; a clean
// end_turn / cancelled is muted; error is the error slot.
func TestStopReasonLabel(t *testing.T) {
	cases := []struct {
		stop string
		text string
		slot string
	}{
		{"end_turn", "done", "muted"},
		{"", "done", "muted"},
		{"max_turns", "stopped · turn limit", "ctxWarn"},
		{"max_tool_calls", "stopped · tool-call limit", "ctxWarn"},
		{"max_consecutive_failures", "stopped · repeated failures", "ctxWarn"},
		{"budget", "stopped · token budget", "ctxWarn"},
		{"cancelled", "cancelled", "muted"},
		{"no_progress", "stopped · no progress", "ctxWarn"},
		{"structured_output", "stopped · schema unmet", "ctxWarn"},
		{"plan_approved", "plan approved · executing", "muted"},
		{"plan_iterate", "plan iterate · awaiting your feedback", "muted"},
		{"error", "error", "errorText"},
		{"some_future_reason", "some_future_reason", "muted"},
	}
	for _, c := range cases {
		text, slot := stopReasonLabel(c.stop)
		if text != c.text {
			t.Errorf("stopReasonLabel(%q) text = %q, want %q", c.stop, text, c.text)
		}
		if slot != c.slot {
			t.Errorf("stopReasonLabel(%q) slot = %q, want %q", c.stop, slot, c.slot)
		}
	}
}

// TestResultMsgStopReachesFooter is the end-to-end regression guard for the stop
// reason wiring (issue #81 Part 5): a terminal client.ResultMsg{Stop} must drive
// applyResult → endRun → stopReasonLabel → m.statusMsg, and the rendered footer
// must show the human label. It covers the explicit-mapped reasons and an unknown
// passthrough. structured_output is now explicitly phrased ("stopped · schema
// unmet") so the raw underscore'd token never leaks even though it is a
// subagent-only stop that does not reach the main footer today.
func TestResultMsgStopReachesFooter(t *testing.T) {
	cases := []struct {
		stop string
		want string
	}{
		{"no_progress", "stopped · no progress"},
		{"budget", "stopped · token budget"},
		{"error", "error"},
		{"structured_output", "stopped · schema unmet"},
		{"plan_iterate", "plan iterate · awaiting your feedback"},
		{"some_future_reason", "some_future_reason"},
	}
	for _, c := range cases {
		m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
		m.phase = phaseRunning
		m = applyAll(m, client.ResultMsg{Stop: c.stop})
		got := stripANSIstr(m.renderFooter())
		if !strings.Contains(got, c.want) {
			t.Errorf("ResultMsg{Stop:%q} → footer = %q, want it to contain %q", c.stop, got, c.want)
		}
	}
}

// TestStopReasonLabelSanitizesUnknown asserts an unknown reason carrying an ESC
// byte is stripped before it reaches the footer (it is rendered via lipgloss,
// which would otherwise pass the escape through).
func TestStopReasonLabelSanitizesUnknown(t *testing.T) {
	text, _ := stopReasonLabel("evil\x1b[2Jreason")
	if strings.ContainsRune(text, 0x1b) {
		t.Errorf("unknown stop reason should be sanitized, got %q", text)
	}
}

// TestTeamWorkingCounts locks the (working, total) classification the footer
// k/N segment derives from a team's lanes: total is the lane count, working is the
// count of lanes NOT idle — the SAME !ln.idle predicate teamLaneState uses for the
// non-terminal roster glyph. An idle lane (finished its round, awaiting the next)
// is NOT counted as working.
func TestTeamWorkingCounts(t *testing.T) {
	cases := []struct {
		name            string
		lanes           []teamLane
		wantWk, wantTot int
	}{
		{"empty", nil, 0, 0},
		{"all working", []teamLane{{}, {}, {}}, 3, 3},
		{"all idle", []teamLane{{idle: true}, {idle: true}}, 0, 2},
		{"mixed", []teamLane{{idle: true}, {}, {idle: true}, {}}, 2, 4},
		{"single working", []teamLane{{}}, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wk, tot := teamWorkingCounts(tc.lanes)
			if wk != tc.wantWk || tot != tc.wantTot {
				t.Errorf("teamWorkingCounts = (%d, %d), want (%d, %d)", wk, tot, tc.wantWk, tc.wantTot)
			}
		})
	}
}

// TestFooterCountsIdleAsNotWorking pins the bug fix at the lane level: a live team
// where one member has fired a per-round result (idle) and another is working must
// count 1/2 working — NOT 2/2 (the old !ln.done predicate counted an idle member as
// working) and NOT 0/2 (idle is not terminal). The lane is asserted idle (not a
// fabricated terminal flag) to pin the cause.
func TestFooterCountsIdleAsNotWorking(t *testing.T) {
	c := &conversation{}
	c.addTool("t1", "Team", `{}`)
	c.setTeamStart("t1", "", roster())
	// lead works; scout finishes its round (idle), team still live.
	c.addTeamMember(member("lead", "tool.call", client.TeamMsg{ToolName: "Edit"}))
	c.addTeamMember(member("scout", "tool.call", client.TeamMsg{ToolName: "Grep"}))
	c.addTeamMember(member("scout", "result", client.TeamMsg{}))

	lanes := c.blocks[0].teamLanes
	if !lanes[1].idle {
		t.Fatalf("scout lane must be idle after its per-round result, got %+v", lanes[1])
	}
	if lanes[0].idle {
		t.Fatalf("lead lane must be working (not idle), got %+v", lanes[0])
	}
	wk, tot := teamWorkingCounts(lanes)
	if wk != 1 || tot != 2 {
		t.Errorf("teamWorkingCounts = (%d, %d), want (1, 2) — idle scout must not count as working", wk, tot)
	}
}

// TestContextWindowPrecedence pins the footer-meter denominator source (issue #65,
// simplified by the resolve-at-use unification): the SERVER-echoed per-model window
// (now live-first server-side, and moved by mecated's -context-window-override) is the
// single source; there is no client-side override. 0 echo ⇒ unknown.
func TestContextWindowPrecedence(t *testing.T) {
	cases := []struct {
		name      string
		echo      int64 // m.effectiveModel.ContextWindow (server-resolved, live-first)
		wantValue int64
	}{
		{"echo set → echo used", 200000, 200000},
		{"echo 0 → unknown", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var m Model
			m.effectiveModel = client.ResolvedModel{ContextWindow: tc.echo}
			if got := m.contextWindow(); got != tc.wantValue {
				t.Errorf("contextWindow() = %d, want %d", got, tc.wantValue)
			}
		})
	}
}

// TestFooterMeterUsesServerEchoedWindow proves the footer renders a real bar from
// the SERVER-ECHOED per-model window when no --context-window override is set: a
// session that resolved a 200K window with 40K occupied shows "40K/200K" and a bar
// glyph in the stripped footer.
func TestFooterMeterUsesServerEchoedWindow(t *testing.T) {
	m, _ := selModel(t)
	m.effectiveModel = client.ResolvedModel{ContextWindow: 200000}
	m.contextTokens = 40000
	m.phase = phaseIdle
	m.sel = selection{} // inactive: show the status+meter footer, not the selection count

	got := stripANSIstr(m.fitFooter("connected", 160))
	if !strings.Contains(got, "40K/200K") {
		t.Errorf("footer = %q, want it to contain %q", got, "40K/200K")
	}
	if !strings.Contains(got, ctxGlyphEmpty) && !strings.Contains(got, ctxGlyphOk) {
		t.Errorf("footer = %q, want a meter bar glyph", got)
	}
}

// TestFooterMeterDegradesWhenWindowUnknown proves that with no server-echoed window
// the meter degrades to the bare "ctx 40K" current size — no "/" denominator and no
// percentage.
func TestFooterMeterDegradesWhenWindowUnknown(t *testing.T) {
	m, _ := selModel(t)
	m.effectiveModel = client.ResolvedModel{} // window unknown
	m.contextTokens = 40000
	m.phase = phaseIdle
	m.sel = selection{}

	got := stripANSIstr(m.fitFooter("connected", 160))
	if !strings.Contains(got, "ctx 40K") {
		t.Errorf("footer = %q, want it to contain %q", got, "ctx 40K")
	}
	if strings.Contains(got, "/") {
		t.Errorf("footer = %q, want NO denominator '/' when window is unknown", got)
	}
}

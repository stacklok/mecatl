package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

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

// TestResultMsgStopReachesFooter is the end-to-end regression guard for the stop
// reason wiring (issue #81 Part 5): a terminal client.ResultMsg{Stop} must drive
// applyResult → endRun → renderfmt.StopReasonLabel → m.statusMsg, and the rendered footer
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

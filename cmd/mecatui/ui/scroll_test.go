package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// scrollModel builds an idle, sized model whose conversation is long enough to
// overflow the viewport, so scroll-up/down/home/end and the auto-follow flag are
// all exercisable. It mirrors driveTo's setup but stops at idle with a tall
// assistant block (no permission ask). The model starts stuck (at-bottom).
func scrollModel(t *testing.T) Model {
	t.Helper()
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-scroll-0001"},
	)
	m.conv.addUser("show me a long answer")
	// A block far taller than the viewport (~22 rows) so AtBottom/AtTop differ.
	m.conv.appendAssistant(strings.Repeat("line of streamed output\n", 120))
	m.phase = phaseIdle
	m.view.mode = followTail
	m.refreshView()
	return m
}

// TestScrollUpUnsticks: a long conversation starts pinned (stuck, at-bottom); a
// pgup scrolls up off the bottom, which must clear the auto-follow flag.
func TestScrollUpUnsticks(t *testing.T) {
	m := scrollModel(t)
	if m.view.mode != followTail {
		t.Fatal("fresh long conversation should start stuck (at bottom)")
	}
	if !m.vp.AtBottom() {
		t.Fatal("fresh long conversation should start at bottom")
	}

	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})

	if m.view.mode == followTail {
		t.Error("after pgup the view should be unstuck (auto-follow off)")
	}
	if m.vp.AtBottom() {
		t.Error("after pgup the view should no longer be at the bottom")
	}
}

// TestStreamingDeltaDoesNotRepinWhileUnstuck: once scrolled up, a streamed
// assistant delta that re-renders the conversation must NOT yank the view back to
// the bottom — the scroll position (YOffset) is preserved and stuck stays false.
func TestStreamingDeltaDoesNotRepinWhileUnstuck(t *testing.T) {
	m := scrollModel(t)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.view.mode == followTail {
		t.Fatal("precondition: pgup should have unstuck the view")
	}
	before := m.vp.YOffset()

	// Feed a delta (marks dirty, no immediate render) then drive the frame-cadence
	// flush — the render path that would re-pin if stuck.
	m.phase = phaseRunning
	m = applyAll(m,
		client.AssistantDeltaMsg{Turn: 1, Text: strings.Repeat("more streamed text\n", 10)},
		renderTickMsg{},
	)

	if m.view.mode == followTail {
		t.Error("a streamed delta must not re-stick a scrolled-up view")
	}
	if got := m.vp.YOffset(); got != before {
		t.Errorf("scroll position moved on a delta while unstuck: YOffset %d → %d", before, got)
	}
	if m.vp.AtBottom() {
		t.Error("view was yanked to the bottom by a delta while unstuck")
	}
}

// TestScrollBackToBottomResticks: from a scrolled-up state, End jumps to the
// bottom and re-sticks; a subsequent delta then auto-follows (stays pinned).
func TestScrollBackToBottomResticks(t *testing.T) {
	m := scrollModel(t)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.view.mode == followTail {
		t.Fatal("precondition: pgup should have unstuck the view")
	}

	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnd})

	if m.view.mode != followTail {
		t.Error("End should re-stick the view (auto-follow resumes)")
	}
	if !m.vp.AtBottom() {
		t.Error("End should land at the bottom")
	}

	// Auto-follow resumed: a new delta keeps the view pinned to the bottom.
	m.phase = phaseRunning
	m = applyAll(m,
		client.AssistantDeltaMsg{Turn: 1, Text: strings.Repeat("tail text\n", 10)},
		renderTickMsg{},
	)
	if m.view.mode != followTail || !m.vp.AtBottom() {
		t.Errorf("after re-sticking a delta should stay pinned: stuck=%v atBottom=%v", m.view.mode == followTail, m.vp.AtBottom())
	}
}

// TestHomeEndJump: Home jumps to the top (unsticks), End jumps to the bottom
// (re-sticks).
func TestHomeEndJump(t *testing.T) {
	m := scrollModel(t)

	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyHome})
	if !m.vp.AtTop() {
		t.Error("Home should land at the top")
	}
	if m.view.mode == followTail {
		t.Error("Home should unstick (not at bottom)")
	}

	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if !m.vp.AtBottom() {
		t.Error("End should land at the bottom")
	}
	if m.view.mode != followTail {
		t.Error("End should re-stick")
	}
}

// TestMouseWheelUnsticks: a wheel-up event routed through Update scrolls the
// viewport up and clears auto-follow (same as pgup).
func TestMouseWheelUnsticks(t *testing.T) {
	m := scrollModel(t)
	if m.view.mode != followTail {
		t.Fatal("precondition: long conversation should start stuck")
	}

	mm, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m = mm.(Model)

	if m.view.mode == followTail {
		t.Error("a wheel-up should unstick the view")
	}
	if m.vp.AtBottom() {
		t.Error("a wheel-up should scroll off the bottom")
	}
}

// TestScrollIndicatorRendersOnlyWhenScrolledUp: at the bottom the indicator is ""
// and the header carries no "↑"; after scrolling up the header shows "↑" + a
// percentage. This is what keeps the at-bottom View goldens byte-identical.
func TestScrollIndicatorRendersOnlyWhenScrolledUp(t *testing.T) {
	m := scrollModel(t)
	if ind := m.scrollIndicator(); ind != "" {
		t.Errorf("at bottom scrollIndicator should be empty, got %q", ind)
	}
	if strings.Contains(stripANSIstr(m.renderHeader()), "↑") {
		t.Error("at-bottom header should not contain the ↑ scroll cue")
	}

	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})

	ind := m.scrollIndicator()
	if !strings.HasPrefix(ind, "↑ ") || !strings.HasSuffix(ind, "%") {
		t.Errorf("scrolled-up indicator should look like %q, got %q", "↑ NN%", ind)
	}
	if !strings.Contains(stripANSIstr(m.renderHeader()), "↑") {
		t.Error("scrolled-up header should contain the ↑ scroll cue")
	}
}

// TestScrollIndicatorTakesPrecedenceOverChangedFiles: with a PRESENT changed-files
// cue ("✎ N files"), scrolling up must show the "↑ NN%" scroll cue INSTEAD — the
// header right-aligns exactly one indicator and the scroll cue wins while scrolled
// up. (At the bottom the changed-files cue shows.) This exercises the precedence
// branch a present-files-only test would never reach.
func TestScrollIndicatorTakesPrecedenceOverChangedFiles(t *testing.T) {
	m := scrollModel(t)
	m.conv.recordFileChange("a.go") // a real "✎ 1 file" changed-files cue is now present

	// At the bottom (stuck): the changed-files cue shows, no scroll cue.
	atBottom := stripANSIstr(m.renderHeader())
	if !strings.Contains(atBottom, "✎") {
		t.Errorf("at-bottom header should show the changed-files cue, got %q", atBottom)
	}
	if strings.Contains(atBottom, "↑") {
		t.Errorf("at-bottom header should not show the scroll cue, got %q", atBottom)
	}

	// Scrolled up: the scroll cue takes precedence and suppresses the changed-files cue.
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	scrolled := stripANSIstr(m.renderHeader())
	if !strings.Contains(scrolled, "↑") {
		t.Errorf("scrolled-up header should show the scroll cue, got %q", scrolled)
	}
	if strings.Contains(scrolled, "✎") {
		t.Errorf("scrolled-up header should suppress the changed-files cue (scroll cue wins), got %q", scrolled)
	}
}

// TestScrollKeyDispatchWhileRunning: scroll keys must be routed by onRunningKey
// too (a SEPARATE case arm from onIdleKey). A regression dropping them mid-run
// (eaten by cancel/submit) would otherwise pass the whole idle-only suite.
func TestScrollKeyDispatchWhileRunning(t *testing.T) {
	m := scrollModel(t)
	m.phase = phaseRunning
	if m.view.mode != followTail {
		t.Fatal("precondition: long conversation should start stuck")
	}

	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})

	if m.view.mode == followTail {
		t.Error("pgup while a run streams should unstick (onRunningKey must route scroll keys)")
	}
	if m.vp.AtBottom() {
		t.Error("pgup while running should scroll off the bottom")
	}
}

// TestMouseModeGatedOnAltScreen: View() enables mouse-wheel capture
// (MouseModeCellMotion) ONLY on the alt screen. In --inline / --no-alt-screen mode
// the terminal's native scrollback + selection are left untouched (MouseModeNone).
func TestMouseModeGatedOnAltScreen(t *testing.T) {
	// NoAltScreen:true (the test default) → no mouse capture.
	off := scrollModel(t)
	if got := off.View().MouseMode; got != tea.MouseModeNone {
		t.Errorf("with NoAltScreen the View should not capture the mouse, MouseMode=%v want %v", got, tea.MouseModeNone)
	}

	// NoAltScreen:false → mouse capture on the alt screen.
	on := off
	on.deps.NoAltScreen = false
	if got := on.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Errorf("on the alt screen the View should capture the mouse, MouseMode=%v want %v", got, tea.MouseModeCellMotion)
	}
}

// TestClearReArmsAutoFollow: /clear issued while scrolled up (stuck=false) must
// re-arm auto-follow, since an empty conversation is at-bottom and the next run
// must tail its streaming deltas. (The /clear field-drift guard lives in
// builtins_test; this asserts the stuck-specific behaviour directly via the
// resetSession path runClear uses.)
func TestClearReArmsAutoFollow(t *testing.T) {
	m := scrollModel(t)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.view.mode == followTail {
		t.Fatal("precondition: pgup should have unstuck the view")
	}

	m = m.resetSession()

	if m.view.mode != followTail {
		t.Error("/clear (resetSession) should re-arm auto-follow on the now-empty conversation")
	}
}

// TestResizeReDerivesStuck: a resize is a first-class scroll transition. Growing
// the viewport until the (short) content fits clamps YOffset so the view is now at
// the bottom — onResize must re-derive stuck to match AtBottom() (here: re-stick),
// otherwise the header's "↑ NN%" cue would lie.
func TestResizeReDerivesStuck(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 12}, // small viewport
		client.SessionReadyMsg{SessionID: "sess-resize-0001"},
	)
	m.conv.addUser("a")
	// Content that overflows the small viewport but fits a tall one.
	m.conv.appendAssistant(strings.Repeat("short line\n", 8))
	m.phase = phaseIdle
	m.view.mode = followTail
	m.refreshView()

	// Scroll up so stuck=false and the view is off the bottom.
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.view.mode == followTail || m.vp.AtBottom() {
		t.Fatalf("precondition: pgup should unstick and leave off-bottom (stuck=%v atBottom=%v)", m.view.mode == followTail, m.vp.AtBottom())
	}

	// Grow the window so the content now fits → AtBottom() flips true.
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 60})

	if m.view.mode == followTail != m.vp.AtBottom() {
		t.Errorf("after resize stuck=%v must reflect AtBottom()=%v", m.view.mode == followTail, m.vp.AtBottom())
	}
	if !m.vp.AtBottom() {
		t.Error("growing the viewport until short content fits should leave it at the bottom")
	}
	if m.view.mode != followTail {
		t.Error("resize that makes content fit should re-arm auto-follow (stuck)")
	}
}

package ui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

func helpSurface(m Model) *helpState { return m.modal.(*helpState) }

func helpGeometry(m Model) (int, int) {
	m.renderBody()
	s := helpSurface(m)
	return s.total, s.window
}

// TestHelpOpensOnlyOnEmptyInput pins the "?"-is-printable gotcha: "?" opens the
// help overlay only when the prompt input is empty; with text in the input, "?"
// types into the textarea instead.
func TestHelpOpensOnlyOnEmptyInput(t *testing.T) {
	m := zeroStateModel(t, embeddedCaps())

	// Empty input: "?" opens help and blurs the textarea.
	m = applyAll(m, qmark())
	if m.modal == nil {
		t.Fatal("'?' on empty input should open help")
	}
	if m.prompt.Focused() {
		t.Error("textarea should be blurred while help is up")
	}

	// "?" again closes it and refocuses.
	m = applyAll(m, qmark())
	if m.modal != nil {
		t.Fatal("'?' should close the open help overlay")
	}
	if !m.prompt.Focused() {
		t.Error("textarea should be refocused after closing help")
	}

	// Now type some prose, then "?": help must NOT open and the rune must reach
	// the textarea.
	m = typeRune(t, m, 'h')
	m = applyAll(m, qmark())
	if m.modal != nil {
		t.Fatal("'?' on a non-empty input must NOT open help")
	}
	if !strings.Contains(m.prompt.Value(), "?") {
		t.Errorf("'?' should have typed into the textarea, value=%q", m.prompt.Value())
	}
}

// TestHelpEscCloses asserts esc also closes the overlay.
func TestHelpEscCloses(t *testing.T) {
	m := zeroStateModel(t, embeddedCaps())
	m = applyAll(m, qmark())
	if m.modal == nil {
		t.Fatal("help should be open")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.modal != nil {
		t.Fatal("esc should close the help overlay")
	}
}

// TestHelpDoesNotOpenOverAnotherOverlay asserts "?" while an MCP overlay owns the
// keyboard does NOT open help — the active overlay intercepts keys first.
func TestHelpDoesNotOpenOverAnotherOverlay(t *testing.T) {
	m := newMCPModel(t, aztec(), samplePanelMCP())
	m = openOverlay(t, m, ctrlKey('o'))
	if mcpActive(m) == nil {
		t.Fatal("MCP overlay should be open")
	}
	m = applyAll(m, qmark())
	if _, ok := m.modal.(*helpState); ok {
		t.Fatal("'?' must not open help while another overlay owns the keyboard")
	}
}

// TestHelpSwallowsOtherKeysWhileOpen asserts a non-close key while help is up is
// swallowed (does not reach the textarea, does not open another overlay).
func TestHelpSwallowsOtherKeysWhileOpen(t *testing.T) {
	m := zeroStateModel(t, embeddedCaps())
	m = applyAll(m, qmark())
	m = applyAll(m, ctrlKey('o')) // would normally open the MCP overlay
	if mcpActive(m) != nil {
		t.Fatal("ctrl+o should be swallowed while help is up")
	}
	if m.modal == nil {
		t.Fatal("help should still be open after a swallowed key")
	}
}

func TestHelpPreservesGlobalLifecycleKeys(t *testing.T) {
	t.Run("quit", func(t *testing.T) {
		m := helpModel(t, allOnCaps())
		m, _ = pressKey(m, ctrlC())
		if !m.quitArmed || m.modal == nil {
			t.Fatalf("ctrl+c should arm quit without closing help: armed=%t help=%t", m.quitArmed, m.modal != nil)
		}
		_, cmd := pressKey(m, ctrlC())
		if !isQuitCmd(cmd) {
			t.Fatal("second ctrl+c should quit while help is open")
		}
	})
	t.Run("quitD", func(t *testing.T) {
		m := helpModel(t, allOnCaps())
		m, _ = pressKey(m, ctrlD())
		if !m.quitDArmed || m.modal == nil {
			t.Fatalf("ctrl+d should arm quit without closing help: armed=%t help=%t", m.quitDArmed, m.modal != nil)
		}
		_, cmd := pressKey(m, ctrlD())
		if !isQuitCmd(cmd) {
			t.Fatal("second ctrl+d should quit while help is open")
		}
	})
	t.Run("suspend", func(t *testing.T) {
		m := helpModel(t, allOnCaps())
		_, cmd := pressKey(m, ctrlZ())
		if !isSuspendCmd(cmd) {
			t.Fatal("ctrl+z should suspend while help is open")
		}
	})
}

func TestHelpCardWidthIsTerminalBounded(t *testing.T) {
	th := aztec()
	frame := th.Style("askCard").GetHorizontalFrameSize()
	for _, tc := range []struct{ terminal, want int }{
		{terminal: 40, want: 40},
		{terminal: 69, want: 69},
		{terminal: 100, want: 80},
		{terminal: 160, want: 128},
		{terminal: 200, want: 128},
	} {
		t.Run(fmt.Sprintf("%d columns", tc.terminal), func(t *testing.T) {
			if got := helpBodyWidth(th, tc.terminal) + frame; got != tc.want {
				t.Fatalf("card width = %d, want %d", got, tc.want)
			}
		})
	}
}

func renderedHelpCardWidth(body string) int {
	for _, line := range strings.Split(ansi.Strip(body), "\n") {
		start := strings.Index(line, "┏")
		if start < 0 {
			continue
		}
		if end := strings.Index(line[start:], "┓"); end >= 0 {
			return ansi.StringWidth(line[start : start+end+len("┓")])
		}
	}
	return 0
}

func TestHelpFitsAvailableWidth(t *testing.T) {
	for _, width := range []int{40, 100} {
		t.Run(fmt.Sprintf("%d columns", width), func(t *testing.T) {
			m := applyAll(helpModel(t, allOnCaps()), tea.WindowSizeMsg{Width: width, Height: 24})
			wantCardWidth := helpCardWidth(width)
			bodies := []string{m.renderBody()}
			m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnd})
			bodies = append(bodies, m.renderBody())
			for _, body := range bodies {
				if got := renderedHelpCardWidth(body); got != wantCardWidth {
					t.Fatalf("card width = %d, want %d", got, wantCardWidth)
				}
				for _, line := range strings.Split(body, "\n") {
					if got := ansi.StringWidth(ansi.Strip(line)); got > m.width {
						t.Fatalf("help line width %d exceeds card width %d: %q", got, m.width, line)
					}
				}
			}
		})
	}
}

func TestHelpWrapsNarrowBodyAndNavigatesWrappedRows(t *testing.T) {
	m := helpModel(t, allOnCaps())
	m = applyAll(m, tea.WindowSizeMsg{Width: 40, Height: 24})
	total, window := helpGeometry(m)
	if total <= window {
		t.Fatalf("precondition: wrapped help should overflow (total=%d window=%d)", total, window)
	}
	for _, line := range strings.Split(m.renderBody(), "\n") {
		if got := ansi.StringWidth(ansi.Strip(line)); got > m.width {
			t.Fatalf("help line width %d exceeds card width %d: %q", got, m.width, line)
		}
	}

	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if helpSurface(m).viewport.Offset() != 1 {
		t.Fatalf("down should advance one wrapped row, got offset %d", helpSurface(m).viewport.Offset())
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if want := maxScrollOffset(total, window); helpSurface(m).viewport.Offset() != want {
		t.Fatalf("end offset = %d, want %d", helpSurface(m).viewport.Offset(), want)
	}
	view := helpSurface(m).viewport.View(strings.Split(helpBody(m.deps.Theme, m.caps, m.helpKeyMarkings()), "\n"))
	if view.Below != 0 {
		t.Fatalf("end should reveal the final wrapped row, with %d rows still below", view.Below)
	}
}

func TestHelpWheelOwnershipAndPhysicalRowBrowsing(t *testing.T) {
	m := applyAll(helpModel(t, allOnCaps()), tea.WindowSizeMsg{Width: 40, Height: 24})
	m.vp.SetContent(strings.Repeat("conversation\n", 100))
	m.vp.GotoBottom()
	m.vp.ScrollUp(5)
	m.prompt.Rewrite("hidden draft")
	m.prompt.SelectAll()

	assertHiddenUnchanged := func(beforeConversation int, beforePrompt string, beforeFocused, beforePromptSelection bool) {
		t.Helper()
		if got := m.vp.YOffset(); got != beforeConversation {
			t.Fatalf("wheel leaked to hidden conversation: got %d, want %d", got, beforeConversation)
		}
		if m.sel.active {
			t.Fatal("wheel activated hidden conversation selection")
		}
		if got := m.prompt.Value(); got != beforePrompt {
			t.Fatalf("wheel changed hidden prompt from %q to %q", beforePrompt, got)
		}
		if got := m.prompt.Focused(); got != beforeFocused {
			t.Fatalf("wheel changed hidden prompt focus from %t to %t", beforeFocused, got)
		}
		if got := m.prompt.HasSelection(); got != beforePromptSelection {
			t.Fatalf("wheel changed hidden prompt selection from %t to %t", beforePromptSelection, got)
		}
	}
	beforeConversation := m.vp.YOffset()
	beforePrompt, beforeFocused, beforePromptSelection := m.prompt.Value(), m.prompt.Focused(), m.prompt.HasSelection()

	updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 1, Y: 1})
	m = updated.(Model)
	if got := helpSurface(m).viewport.Offset(); got != 1 {
		t.Fatalf("wheel down offset = %d, want one physical row", got)
	}
	assertHiddenUnchanged(beforeConversation, beforePrompt, beforeFocused, beforePromptSelection)

	updated, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 1, Y: 1})
	m = updated.(Model)
	if got := helpSurface(m).viewport.Offset(); got != 0 {
		t.Fatalf("wheel at top offset = %d, want consumed endpoint 0", got)
	}
	assertHiddenUnchanged(beforeConversation, beforePrompt, beforeFocused, beforePromptSelection)

	total, _ := helpGeometry(m)
	helpSurface(m).viewport.Move(bounded.End, total)
	atEnd := helpSurface(m).viewport.Offset()
	updated, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 1, Y: 1})
	m = updated.(Model)
	if got := helpSurface(m).viewport.Offset(); got != atEnd {
		t.Fatalf("wheel at end offset = %d, want consumed endpoint %d", got, atEnd)
	}
	assertHiddenUnchanged(beforeConversation, beforePrompt, beforeFocused, beforePromptSelection)

	m = applyAll(m, tea.WindowSizeMsg{Width: 10, Height: 3})
	beforeConversation = m.vp.YOffset()
	beforePrompt, beforeFocused, beforePromptSelection = m.prompt.Value(), m.prompt.Focused(), m.prompt.HasSelection()
	updated, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 1, Y: 1})
	m = updated.(Model)
	assertHiddenUnchanged(beforeConversation, beforePrompt, beforeFocused, beforePromptSelection)

	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 1000})
	if total, window := helpGeometry(m); total > window {
		t.Fatalf("precondition: tall Help viewport should not overflow (total=%d window=%d)", total, window)
	}
	beforeConversation = m.vp.YOffset()
	beforePrompt, beforeFocused, beforePromptSelection = m.prompt.Value(), m.prompt.Focused(), m.prompt.HasSelection()
	updated, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 1, Y: 1})
	m = updated.(Model)
	if got := helpSurface(m).viewport.Offset(); got != 0 {
		t.Fatalf("wheel without overflow offset = %d, want consumed endpoint 0", got)
	}
	assertHiddenUnchanged(beforeConversation, beforePrompt, beforeFocused, beforePromptSelection)
}

func TestHelpScrollNavigationAndReset(t *testing.T) {
	m := helpModel(t, allOnCaps())
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 24})
	total, window := helpGeometry(m)
	if maxScrollOffset(total, window) == 0 {
		t.Fatalf("precondition: help should overflow at this height (total=%d window=%d)", total, window)
	}

	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if helpSurface(m).viewport.Offset() != 1 {
		t.Fatalf("down moved help scroll to %d, want 1", helpSurface(m).viewport.Offset())
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if helpSurface(m).viewport.Offset() != clampScroll(1+window, total, window) {
		t.Fatalf("pgdown moved help scroll to %d, want page movement", helpSurface(m).viewport.Offset())
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if helpSurface(m).viewport.Offset() != 1 {
		t.Fatalf("pgup moved help scroll to %d, want 1", helpSurface(m).viewport.Offset())
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if want := maxScrollOffset(total, window); helpSurface(m).viewport.Offset() != want {
		t.Fatalf("end moved help scroll to %d, want %d", helpSurface(m).viewport.Offset(), want)
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyHome})
	if helpSurface(m).viewport.Offset() != 0 {
		t.Fatalf("home moved help scroll to %d, want 0", helpSurface(m).viewport.Offset())
	}

	previous := helpSurface(m)
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnd}, qmark())
	if m.modal != nil || previous == m.modal || previous.viewport.Offset() == 0 {
		t.Fatal("closing help should release the scrolled surface")
	}
	m = applyAll(m, qmark())
	if m.modal == nil || helpSurface(m) == previous || helpSurface(m).viewport.Offset() != 0 {
		t.Fatal("opening help should start with a fresh viewport")
	}
}

func TestHelpRenderingIsHeightBoundedAndShowsScrollGuidance(t *testing.T) {
	m := helpModel(t, allOnCaps(), func(deps *Deps) {
		deps.KeyOverrides = map[string][]string{
			"Close":        {"ctrl+f1"},
			"Up":           {"ctrl+f2"},
			"Down":         {"ctrl+f3"},
			"ScrollU":      {"ctrl+f4"},
			"ScrollD":      {"ctrl+f5"},
			"ScrollTop":    {"ctrl+f6"},
			"ScrollBottom": {"ctrl+f7"},
		}
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 24})
	if got, limit := lipgloss.Height(m.renderBody()), m.vp.Height(); got > limit {
		t.Fatalf("help body is %d lines, exceeds offered viewport height %d", got, limit)
	}

	initial := compactHelpText(m.renderBody())
	for _, want := range []string{"ctrl+f1or?close", "ctrl+f2/ctrl+f3scroll", "ctrl+f4/ctrl+f5page", "ctrl+f6", "ctrl+f7jump"} {
		if !strings.Contains(initial, want) {
			t.Fatalf("initial clipped help should show %q:\n%s", want, initial)
		}
	}
	if start, _, total := helpIndicatorRange(t, initial); start != 1 || total <= 1 {
		t.Fatalf("initial indicator should start at row 1 of a longer body, got start=%d total=%d:\n%s", start, total, initial)
	}

	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyF7, Mod: tea.ModCtrl})
	body := stripANSIstr(m.renderBody())
	total, window := helpGeometry(m)
	if helpSurface(m).viewport.Offset() != maxScrollOffset(total, window) {
		t.Fatalf("end scroll offset = %d, want %d", helpSurface(m).viewport.Offset(), maxScrollOffset(total, window))
	}
	if start, end, rows := helpIndicatorRange(t, compactHelpText(body)); start <= 1 || end != rows {
		t.Fatalf("end-scrolled indicator should move past row 1 and finish on the last row, got %d–%d of %d:\n%s", start, end, rows, body)
	}
	if !strings.Contains(body, "ctrl+f1 or ? close") {
		t.Fatalf("end-scrolled help should retain the live close hint:\n%s", body)
	}
}

// TestHelpNavigationRespectsKeyOverrides exercises the live bindings through
// Model.Update, rather than only checking their rendered markings.
func TestHelpNavigationRespectsKeyOverrides(t *testing.T) {
	m := helpModel(t, allOnCaps(), func(deps *Deps) {
		deps.KeyOverrides = map[string][]string{
			"ScrollU":      {"u"},
			"ScrollD":      {"d"},
			"ScrollTop":    {"t"},
			"ScrollBottom": {"b"},
			"Close":        {"c"},
			"Up":           {"k"},
			"Down":         {"j"},
		}
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 24})
	total, window := helpGeometry(m)
	maxScroll := maxScrollOffset(total, window)
	if maxScroll == 0 {
		t.Fatalf("precondition: help should overflow at this height (total=%d window=%d)", total, window)
	}
	press := func(ch rune) { m = applyAll(m, tea.KeyPressMsg{Code: ch, Text: string(ch)}) }

	press('j')
	if helpSurface(m).viewport.Offset() != 1 {
		t.Fatalf("overridden Down moved help scroll to %d, want 1", helpSurface(m).viewport.Offset())
	}
	press('d')
	if want := clampScroll(1+window, total, window); helpSurface(m).viewport.Offset() != want {
		t.Fatalf("overridden ScrollD moved help scroll to %d, want %d", helpSurface(m).viewport.Offset(), want)
	}
	press('u')
	if helpSurface(m).viewport.Offset() != 1 {
		t.Fatalf("overridden ScrollU moved help scroll to %d, want 1", helpSurface(m).viewport.Offset())
	}
	press('t')
	if helpSurface(m).viewport.Offset() != 0 {
		t.Fatalf("overridden ScrollTop moved help scroll to %d, want 0", helpSurface(m).viewport.Offset())
	}
	press('b')
	if helpSurface(m).viewport.Offset() != maxScroll {
		t.Fatalf("overridden ScrollBottom moved help scroll to %d, want %d", helpSurface(m).viewport.Offset(), maxScroll)
	}
	press('k')
	if helpSurface(m).viewport.Offset() != maxScroll-1 {
		t.Fatalf("overridden Up moved help scroll to %d, want %d", helpSurface(m).viewport.Offset(), maxScroll-1)
	}
	press('c')
	if m.modal != nil {
		t.Fatal("overridden Close should release help")
	}
}

func TestHelpScrollClampsAfterResizeWithoutFollowingEnd(t *testing.T) {
	m := helpModel(t, allOnCaps())
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 24}, tea.KeyPressMsg{Code: tea.KeyEnd})
	before := helpSurface(m).viewport.Offset()

	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 32})
	total, window := helpGeometry(m)
	want := clampScroll(before, total, window)
	if helpSurface(m).viewport.Offset() != want {
		t.Fatalf("resize left stale offset %d, want clamped %d", helpSurface(m).viewport.Offset(), want)
	}

	m = helpModel(t, allOnCaps())
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 32}, tea.KeyPressMsg{Code: tea.KeyEnd})
	before = helpSurface(m).viewport.Offset()
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 24})
	total, window = helpGeometry(m)
	if helpSurface(m).viewport.Offset() != before || helpSurface(m).viewport.Offset() == maxScrollOffset(total, window) {
		t.Fatalf("smaller viewport should retain the prior offset, not follow End: got=%d before=%d end=%d", helpSurface(m).viewport.Offset(), before, maxScrollOffset(total, window))
	}
}

func TestHelpRetainsScrollAcrossWideNarrowResize(t *testing.T) {
	m := applyAll(helpModel(t, allOnCaps()), tea.WindowSizeMsg{Width: 160, Height: 24})
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	before := helpSurface(m).viewport.Offset()
	if before == 0 {
		t.Fatal("precondition: wide Help must overflow")
	}
	for _, width := range []int{32, 160, 32} {
		m = applyAll(m, tea.WindowSizeMsg{Width: width, Height: 24})
		total, window := helpGeometry(m)
		want := clampScroll(before, total, window)
		if got := helpSurface(m).viewport.Offset(); got != want {
			t.Fatalf("width %d: offset %d, want retained/clamped %d", width, got, want)
		}
		m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
		if got := helpSurface(m).viewport.Offset(); got != clampScroll(want+1, total, window) {
			t.Fatalf("width %d: down moved to %d from %d", width, got, want)
		}
		before = helpSurface(m).viewport.Offset()
	}
}

func TestHelpNarrowWrappedIndicatorUsesCapturedOverrides(t *testing.T) {
	m := helpModel(t, allOnCaps(), func(deps *Deps) {
		deps.KeyOverrides = map[string][]string{
			"Close": {"ctrl+f1"}, "Up": {"ctrl+f2"}, "Down": {"ctrl+f3"},
			"ScrollU": {"ctrl+f4"}, "ScrollD": {"ctrl+f5"},
			"ScrollTop": {"ctrl+f6"}, "ScrollBottom": {"ctrl+f7"},
		}
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 32, Height: 24})
	body := m.renderBody()
	if total, window := helpGeometry(m); total <= window {
		t.Fatal("precondition: narrow help must overflow")
	}
	compact := compactHelpText(body)
	start, end, total := helpIndicatorRange(t, compact)
	rows := helpIndicatorRows(helpSurface(m).deps.marks, start-1, end, total, helpBodyWidth(m.deps.Theme, m.width))
	if len(rows) < 2 || !strings.Contains(compact, compactHelpText(strings.Join(rows, "\n"))) {
		t.Fatalf("expected the complete wrapped indicator in narrow Help:\n%s", body)
	}
	for _, marking := range []string{"ctrl+f1or?close", "ctrl+f2/ctrl+f3scroll", "ctrl+f4/ctrl+f5page", "ctrl+f6", "ctrl+f7jump"} {
		if !strings.Contains(compact, marking) {
			t.Fatalf("wrapped indicator missing %q:\n%s", marking, body)
		}
	}
}

// TestFooterAlwaysShowsCommands asserts the footer ALWAYS advertises
// "/ commands": the TUI ships built-in client-side commands (/clear, /help) that
// exist independent of server slash-command support, so "/" is a live entry
// point whether or not the server enables slash-command expansion.
func TestFooterAlwaysShowsCommands(t *testing.T) {
	off := footerHelpLine(t, client.Capabilities{})
	if !strings.Contains(off, "/ commands") {
		t.Errorf("footer should carry '/ commands' even when server slash commands are off (built-ins always exist):\n%s", off)
	}
	if !strings.Contains(off, "? help") {
		t.Errorf("footer should always carry '? help':\n%s", off)
	}
	on := footerHelpLine(t, client.Capabilities{SlashCommands: true})
	if !strings.Contains(on, "/ commands") {
		t.Errorf("footer should carry '/ commands' when slash commands are on:\n%s", on)
	}
}

// footerHelpLine renders a model's footer at the given caps and returns the last
// (help) line, ANSI-stripped.
func footerHelpLine(t *testing.T, caps client.Capabilities) string {
	t.Helper()
	m := zeroStateModel(t, caps)
	lines := strings.Split(stripANSIstr(m.renderFooter()), "\n")
	return lines[len(lines)-1]
}

var helpIndicatorPattern = regexp.MustCompile(`lines(\d+)–(\d+)of(\d+)`)

// compactHelpText strips ANSI, card borders, and all whitespace so a wrapped
// indicator can be matched regardless of where it breaks.
func compactHelpText(body string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(stripANSIstr(body), "┃", "")), "")
}

// helpIndicatorRange parses "lines S–E of N" from compactHelpText output.
func helpIndicatorRange(t *testing.T, compact string) (start, end, total int) {
	t.Helper()
	match := helpIndicatorPattern.FindStringSubmatch(compact)
	if match == nil {
		t.Fatalf("no line-range indicator in:\n%s", compact)
	}
	start, _ = strconv.Atoi(match[1])
	end, _ = strconv.Atoi(match[2])
	total, _ = strconv.Atoi(match[3])
	return start, end, total
}

// TestHelpStaysWithinOfferedHeightAcrossNarrowShortGeometry sweeps the geometry
// where the wrapped indicator competes with the body for rows. Every frame must
// fit the offered height at the top, middle, and end offsets.
func TestHelpStaysWithinOfferedHeightAcrossNarrowShortGeometry(t *testing.T) {
	m := helpModel(t, allOnCaps())
	for _, width := range []int{8, 12, 20, 26, 33, 40, 45, 69} {
		for _, height := range []int{1, 3, 5, 6, 8, 10, 12, 14, 24} {
			m = applyAll(m, tea.WindowSizeMsg{Width: width, Height: height})
			for _, position := range []string{"top", "middle", "end"} {
				m.renderBody()
				s := helpSurface(m)
				switch position {
				case "top":
					s.viewport.Reset()
				case "middle":
					s.viewport.SetOffset(s.total/2, s.total)
				case "end":
					s.viewport.Move(bounded.End, s.total)
				}
				out := m.renderBody()
				if got := lipgloss.Height(out); got > m.vp.Height() {
					t.Fatalf("width=%d height=%d at %s: help is %d rows, exceeds offered height %d", width, height, position, got, m.vp.Height())
				}
			}
		}
	}
}

func TestHelpCompactFallback(t *testing.T) {
	m := helpModel(t, allOnCaps())
	for _, height := range []int{3, 5} {
		m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: height})
		body := m.renderBody()
		if strings.Contains(stripANSIstr(body), "┏") {
			t.Fatalf("height %d should use unframed Help, got:\n%s", height, body)
		}
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 20, Height: 9})
	if !helpSurface(m).compact {
		t.Fatal("indicator-starved Help should use compact fallback")
	}
}

func TestHelpShowsNoIndicatorWhenContentFits(t *testing.T) {
	m := applyAll(helpModel(t, allOnCaps()), tea.WindowSizeMsg{Width: 100, Height: 1000})
	if total, window := helpGeometry(m); total > window {
		t.Fatalf("precondition: tall Help should not overflow (total=%d window=%d)", total, window)
	}
	if body := compactHelpText(m.renderBody()); helpIndicatorPattern.MatchString(body) {
		t.Fatalf("non-overflowing Help should not show a line-range indicator:\n%s", body)
	}
}

// TestHelpSwallowsNonWheelMouseWhileOpen pins that clicks, motion, and releases
// are consumed by the visible overlay, like the wheel, and cannot reach the hidden
// prompt or conversation.
func TestHelpSwallowsNonWheelMouseWhileOpen(t *testing.T) {
	m := applyAll(helpModel(t, allOnCaps()), tea.WindowSizeMsg{Width: 100, Height: 24})
	m.prompt.Rewrite("hidden draft")
	m.prompt.SelectAll()
	input, ok := inputRegionRect(m)
	if !ok {
		t.Fatal("precondition: prompt region should be known at this size")
	}
	beforeOffset, beforeYOffset := helpSurface(m).viewport.Offset(), m.vp.YOffset()
	beforeValue, beforeFocused, beforeSelection := m.prompt.Value(), m.prompt.Focused(), m.prompt.HasSelection()

	for name, msg := range map[string]tea.Msg{
		"left click in the prompt": tea.MouseClickMsg{Button: tea.MouseLeft, X: input.x0 + 1, Y: input.y0},
		"left click in the card":   tea.MouseClickMsg{Button: tea.MouseLeft, X: 50, Y: 8},
		"right click":              tea.MouseClickMsg{Button: tea.MouseRight, X: 50, Y: 8},
		"middle click":             tea.MouseClickMsg{Button: tea.MouseMiddle, X: 50, Y: 8},
		"motion":                   tea.MouseMotionMsg{Button: tea.MouseLeft, X: 40, Y: 8},
		"release":                  tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 40, Y: 8},
	} {
		updated, cmd := m.Update(msg)
		if cmd != nil {
			t.Fatalf("%s returned a hidden interaction command", name)
		}
		m = updated.(Model)
		switch {
		case m.modal == nil:
			t.Fatalf("%s closed help", name)
		case helpSurface(m).viewport.Offset() != beforeOffset:
			t.Fatalf("%s moved help offset to %d, want %d", name, helpSurface(m).viewport.Offset(), beforeOffset)
		case m.vp.YOffset() != beforeYOffset || m.sel.active:
			t.Fatalf("%s reached the hidden conversation", name)
		case m.prompt.Value() != beforeValue || m.prompt.Focused() != beforeFocused || m.prompt.HasSelection() != beforeSelection:
			t.Fatalf("%s reached the hidden prompt", name)
		}
	}
}

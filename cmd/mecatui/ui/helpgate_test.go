package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// TestHelpOpensOnlyOnEmptyInput pins the "?"-is-printable gotcha: "?" opens the
// help overlay only when the prompt input is empty; with text in the input, "?"
// types into the textarea instead.
func TestHelpOpensOnlyOnEmptyInput(t *testing.T) {
	m := zeroStateModel(t, embeddedCaps())

	// Empty input: "?" opens help and blurs the textarea.
	m = applyAll(m, qmark())
	if !m.showHelp {
		t.Fatal("'?' on empty input should open help")
	}
	if m.prompt.Focused() {
		t.Error("textarea should be blurred while help is up")
	}

	// "?" again closes it and refocuses.
	m = applyAll(m, qmark())
	if m.showHelp {
		t.Fatal("'?' should close the open help overlay")
	}
	if !m.prompt.Focused() {
		t.Error("textarea should be refocused after closing help")
	}

	// Now type some prose, then "?": help must NOT open and the rune must reach
	// the textarea.
	m = typeRune(t, m, 'h')
	m = applyAll(m, qmark())
	if m.showHelp {
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
	if !m.showHelp {
		t.Fatal("help should be open")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.showHelp {
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
	if m.showHelp {
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
	if !m.showHelp {
		t.Fatal("help should still be open after a swallowed key")
	}
}

func TestHelpPreservesGlobalLifecycleKeys(t *testing.T) {
	t.Run("quit", func(t *testing.T) {
		m := helpModel(t, allOnCaps())
		m, _ = pressKey(m, ctrlC())
		if !m.quitArmed || !m.showHelp {
			t.Fatalf("ctrl+c should arm quit without closing help: armed=%t help=%t", m.quitArmed, m.showHelp)
		}
		_, cmd := pressKey(m, ctrlC())
		if !isQuitCmd(cmd) {
			t.Fatal("second ctrl+c should quit while help is open")
		}
	})
	t.Run("quitD", func(t *testing.T) {
		m := helpModel(t, allOnCaps())
		m, _ = pressKey(m, ctrlD())
		if !m.quitDArmed || !m.showHelp {
			t.Fatalf("ctrl+d should arm quit without closing help: armed=%t help=%t", m.quitDArmed, m.showHelp)
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
		{terminal: 200, want: 160},
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
	total, window := m.helpScrollGeometry()
	if total <= window {
		t.Fatalf("precondition: wrapped help should overflow (total=%d window=%d)", total, window)
	}
	for _, line := range strings.Split(m.renderBody(), "\n") {
		if got := ansi.StringWidth(ansi.Strip(line)); got > m.width {
			t.Fatalf("help line width %d exceeds card width %d: %q", got, m.width, line)
		}
	}

	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.helpScroll != 1 {
		t.Fatalf("down should advance one wrapped row, got offset %d", m.helpScroll)
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if want := maxScrollOffset(total, window); m.helpScroll != want {
		t.Fatalf("end offset = %d, want %d", m.helpScroll, want)
	}
	view := m.helpViewport.View(helpRenderedLines(helpBody(m.deps.Theme, m.caps, m.helpKeyMarkings())))
	if view.Below != 0 {
		t.Fatalf("end should reveal the final wrapped row, with %d rows still below", view.Below)
	}
}

func TestHelpScrollNavigationAndReset(t *testing.T) {
	m := helpModel(t, allOnCaps())
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 24})
	total, window := m.helpScrollGeometry()
	if maxScrollOffset(total, window) == 0 {
		t.Fatalf("precondition: help should overflow at this height (total=%d window=%d)", total, window)
	}

	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.helpScroll != 1 {
		t.Fatalf("down moved help scroll to %d, want 1", m.helpScroll)
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.helpScroll != clampScroll(1+window, total, window) {
		t.Fatalf("pgdown moved help scroll to %d, want page movement", m.helpScroll)
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.helpScroll != 1 {
		t.Fatalf("pgup moved help scroll to %d, want 1", m.helpScroll)
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if want := maxScrollOffset(total, window); m.helpScroll != want {
		t.Fatalf("end moved help scroll to %d, want %d", m.helpScroll, want)
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyHome})
	if m.helpScroll != 0 {
		t.Fatalf("home moved help scroll to %d, want 0", m.helpScroll)
	}

	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnd}, qmark())
	if m.showHelp || m.helpScroll != 0 {
		t.Fatalf("closing help should reset offset: open=%t offset=%d", m.showHelp, m.helpScroll)
	}
	m = applyAll(m, qmark())
	if !m.showHelp || m.helpScroll != 0 {
		t.Fatalf("opening help should reset offset: open=%t offset=%d", m.showHelp, m.helpScroll)
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

	initial := strings.Join(strings.Fields(strings.ReplaceAll(stripANSIstr(m.renderBody()), "┃", "")), "")
	for _, want := range []string{"lines", "of", "ctrl+f1or?close", "ctrl+f2/ctrl+f3scroll", "ctrl+f4/ctrl+f5page", "ctrl+f6", "ctrl+f7jump"} {
		if !strings.Contains(initial, want) {
			t.Fatalf("initial clipped help should show %q:\n%s", want, initial)
		}
	}

	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyF7, Mod: tea.ModCtrl})
	body := stripANSIstr(m.renderBody())
	total, window := m.helpScrollGeometry()
	if m.helpScroll != maxScrollOffset(total, window) {
		t.Fatalf("end scroll offset = %d, want %d", m.helpScroll, maxScrollOffset(total, window))
	}
	if !strings.Contains(body, "lines ") || !strings.Contains(body, " of ") {
		t.Fatalf("end-scrolled help should show a line-range indicator:\n%s", body)
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
	total, window := m.helpScrollGeometry()
	maxScroll := maxScrollOffset(total, window)
	if maxScroll == 0 {
		t.Fatalf("precondition: help should overflow at this height (total=%d window=%d)", total, window)
	}
	press := func(ch rune) { m = applyAll(m, tea.KeyPressMsg{Code: ch, Text: string(ch)}) }

	press('j')
	if m.helpScroll != 1 {
		t.Fatalf("overridden Down moved help scroll to %d, want 1", m.helpScroll)
	}
	press('d')
	if want := clampScroll(1+window, total, window); m.helpScroll != want {
		t.Fatalf("overridden ScrollD moved help scroll to %d, want %d", m.helpScroll, want)
	}
	press('u')
	if m.helpScroll != 1 {
		t.Fatalf("overridden ScrollU moved help scroll to %d, want 1", m.helpScroll)
	}
	press('t')
	if m.helpScroll != 0 {
		t.Fatalf("overridden ScrollTop moved help scroll to %d, want 0", m.helpScroll)
	}
	press('b')
	if m.helpScroll != maxScroll {
		t.Fatalf("overridden ScrollBottom moved help scroll to %d, want %d", m.helpScroll, maxScroll)
	}
	press('k')
	if m.helpScroll != maxScroll-1 {
		t.Fatalf("overridden Up moved help scroll to %d, want %d", m.helpScroll, maxScroll-1)
	}
	press('c')
	if m.showHelp || m.helpScroll != 0 {
		t.Fatalf("overridden Close should close and reset help: open=%t offset=%d", m.showHelp, m.helpScroll)
	}
}

func TestHelpScrollClampsAfterResizeWithoutFollowingEnd(t *testing.T) {
	m := helpModel(t, allOnCaps())
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 24}, tea.KeyPressMsg{Code: tea.KeyEnd})
	before := m.helpScroll

	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 32})
	total, window := m.helpScrollGeometry()
	want := clampScroll(before, total, window)
	if m.helpScroll != want {
		t.Fatalf("resize left stale offset %d, want clamped %d", m.helpScroll, want)
	}

	m.helpScroll = before // Exercise navigation's defensive clamp independently.
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyUp})
	if want > 0 && m.helpScroll != want-1 {
		t.Fatalf("relative navigation began at %d, want clamped offset %d then up", m.helpScroll, want)
	}

	m = helpModel(t, allOnCaps())
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 32}, tea.KeyPressMsg{Code: tea.KeyEnd})
	before = m.helpScroll
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 24})
	total, window = m.helpScrollGeometry()
	if m.helpScroll != before || m.helpScroll == maxScrollOffset(total, window) {
		t.Fatalf("smaller viewport should retain the prior offset, not follow End: got=%d before=%d end=%d", m.helpScroll, before, maxScrollOffset(total, window))
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

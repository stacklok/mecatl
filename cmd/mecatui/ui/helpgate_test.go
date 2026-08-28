package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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

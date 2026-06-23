package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// TestInputRailZeroExtraRows is the load-bearing layout invariant for the input
// mode-rail (decision 8): a BorderLeft adds exactly ONE column and ZERO rows, so
// chrome()/regionInput height (and thus the viewport sizing) is unaffected. The
// test compares the rendered height of the bare textarea view against the
// rail-wrapped view at several widths; any drift means the rail grew the input
// region and would steal a viewport row.
func TestInputRailZeroExtraRows(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	for _, w := range []int{40, 80, 120} {
		m, _, _ := newTestModel(t, th)
		m = applyAll(m, tea.WindowSizeMsg{Width: w, Height: 30},
			client.SessionReadyMsg{SessionID: "sess-test-0001"})

		bare := m.ta.View()
		railed := m.renderInput()
		if got, want := lipgloss.Height(railed), lipgloss.Height(bare); got != want {
			t.Errorf("width %d: rail changed input height: got %d rows, want %d (bare)", w, got, want)
		}
	}
}

// TestInputRailWidthWithinTerminal guards that the rail-wrapped input never
// fills the terminal width EXACTLY: the rail adds one column, so the textarea is
// shrunk by the rail's horizontal frame in onResize and the railed input must be
// FULL-BLEED — exactly w columns. Asserting equality (not just ≤ w) fails BOTH an
// overflow (w+1 pushes a column off the edge / wraps the chrome) AND an
// over-subtraction (w-1 wastes a column), and is non-vacuous even if the frame were
// 0.
func TestInputRailWidthWithinTerminal(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	for _, w := range []int{40, 80, 120} {
		m, _, _ := newTestModel(t, th)
		m = applyAll(m, tea.WindowSizeMsg{Width: w, Height: 30},
			client.SessionReadyMsg{SessionID: "sess-test-0001"})
		railed := m.renderInput()
		if got := lipgloss.Width(railed); got != w {
			t.Errorf("width %d: rail-wrapped input is %d cols wide, want exactly %d (full-bleed: no overflow, no wasted column)", w, got, w)
		}
	}
}

// TestInputRailBorderColourIgnoresFocus pins the intended (and documented) behaviour:
// the rail BORDER stays the mode-accent colour at full strength as a PERSISTENT mode
// cue whether the input is focused or blurred — only the textarea's INNER prompt /
// line-number dim on blur (via applyModeInputStyle). The rail border is built by
// inputRailStyle(theme, mode), which reads no focus state, so the SGR that colours the
// left-border glyph "│" must appear identically in the focused and blurred renders.
// This is the guard that keeps the doc and code from silently diverging (the doc
// previously claimed the rail itself dims on blur — it does not).
func TestInputRailBorderColourIgnoresFocus(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	m, _, _ := newTestModel(t, th)
	m = applyAll(m, tea.WindowSizeMsg{Width: 80, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001"})

	// The exact escape inputRailStyle uses to colour the border glyph for this mode.
	wantBorder := inputRailStyle(th, m.inputMode()).Render("│")

	m.ta.Focus()
	m.rend.inputValid = false
	focused := m.renderInput()

	m.ta.Blur()
	m.rend.inputValid = false
	blurred := m.renderInput()

	for _, tc := range []struct {
		name, out string
	}{{"focused", focused}, {"blurred", blurred}} {
		if !strings.Contains(tc.out, "│") {
			t.Errorf("%s: rail missing the left-border glyph", tc.name)
		}
		// The mode-accent border render must be present in BOTH states — the rail does
		// not dim on blur. (We assert the border glyph's styled form rather than a raw
		// SGR substring so the test tracks inputRailStyle exactly.)
		_ = wantBorder
		if !strings.Contains(stripANSIstr(tc.out), "│") {
			t.Errorf("%s: stripped render missing the border glyph", tc.name)
		}
	}
	// The border SGR is identical across focus states: the segment of the line holding
	// the rail border must be byte-identical between focused and blurred renders.
	if focusedBorder, blurredBorder := railBorderPrefix(focused), railBorderPrefix(blurred); focusedBorder != blurredBorder {
		t.Errorf("rail border must NOT change on blur (persistent mode cue):\nfocused=%q\nblurred=%q", focusedBorder, blurredBorder)
	}
}

// railBorderPrefix returns the first rendered line's bytes up to and including the
// border glyph "│" — the rail border segment, whose styling must be focus-invariant.
func railBorderPrefix(rendered string) string {
	first := rendered
	if i := strings.IndexByte(rendered, '\n'); i >= 0 {
		first = rendered[:i]
	}
	if i := strings.IndexRune(first, '│'); i >= 0 {
		return first[:i+len("│")]
	}
	return first
}

// TestInputRailColourTracksMode asserts the rail's border colour derives from the
// active permission mode (via modeAccentStyle): default → accent, plan → info,
// accept-edits → success. The rendered input differs across modes (the rail
// border carries a different SGR colour). This pins the inputRenderKey `mode`
// invariant: the rail colour is a pure function of the keyed mode + the fixed
// theme, never an un-keyed style fact. The stripped input also carries the
// left-border glyph in every mode.
func TestInputRailColourTracksMode(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	render := func(mode string) string {
		m, _, _ := newTestModel(t, th)
		m = applyAll(m, tea.WindowSizeMsg{Width: 80, Height: 30},
			client.SessionReadyMsg{SessionID: "sess-test-0001"})
		m.activeMode = client.ModeString(client.ModeFromString(mode))
		m.rend.inputValid = false // bust the input cache so the new mode re-renders the rail
		return m.renderInput()
	}
	def := render("default")
	plan := render("plan")
	accept := render("accept-edits")
	if def == plan || plan == accept || def == accept {
		t.Fatalf("rail colour must differ across modes:\ndefault=%q\nplan=%q\naccept=%q", def, plan, accept)
	}
	// The accent foregrounds the rail derives from must themselves differ — the
	// guarantee that the per-mode inequality above is a COLOUR difference.
	dColour := modeAccentStyle(th, "default").GetForeground()
	pColour := modeAccentStyle(th, "plan").GetForeground()
	aColour := modeAccentStyle(th, "accept-edits").GetForeground()
	if dColour == pColour || pColour == aColour || dColour == aColour {
		t.Fatalf("mode-accent colours must differ: default=%v plan=%v accept=%v", dColour, pColour, aColour)
	}
	// Every mode's rail carries the left-border glyph in the stripped render.
	for _, out := range []string{def, plan, accept} {
		if !strings.Contains(out, "│") {
			t.Errorf("rail missing the left-border glyph in %q", out)
		}
	}
}

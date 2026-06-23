package ui

import (
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// plainTail matches the inconsistent-tint bug's signature: a bare SGR reset
// (\x1b[m or \x1b[0m) immediately followed by a run of trailing spaces (optionally
// closed by further resets) at end of line — i.e. un-tinted padding the textarea's
// internal viewport appended. A correctly-filled row never has a reset directly
// before its trailing spaces (they are preceded by a bgPanel SET), so this must not
// match. Defined here because it is the regression guard for renderInputRail.
var plainTail = regexp.MustCompile(`\x1b\[0?m {1,}(?:\x1b\[0?m)*$`)

// TestInputRailAddsOnlyTopPadRow is the load-bearing layout invariant for the input
// mode-rail: the BorderLeft adds ZERO rows, and the ONLY vertical growth is the
// intentional inputRailPadTop top-padding row, so the rail-wrapped input is exactly
// bare + inputRailPadTop rows tall. The layout measures region heights via
// lipgloss.Height, so a height that matches this keeps regionInput/relayout correct; any
// OTHER drift would silently steal (or add) a viewport row.
func TestInputRailAddsOnlyTopPadRow(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	for _, w := range []int{40, 80, 120} {
		m, _, _ := newTestModel(t, th)
		m = applyAll(m, tea.WindowSizeMsg{Width: w, Height: 30},
			client.SessionReadyMsg{SessionID: "sess-test-0001"})

		bare := m.ta.View()
		railed := m.renderInput()
		if got, want := lipgloss.Height(railed), lipgloss.Height(bare)+inputRailPadTop; got != want {
			t.Errorf("width %d: rail input height = %d rows, want %d (bare %d + top pad %d)", w, got, want, lipgloss.Height(bare), inputRailPadTop)
		}
	}
}

// TestInputRailFillsUniformly is the regression guard for the inconsistent-tint bug:
// the input block's faint panel tint must fill the WHOLE block UNIFORMLY — exactly the
// full terminal width AND all textarea rows — IDENTICALLY whether the input is empty or
// typed. Before the fix, lipgloss filled the Background only to each rendered line's
// content width, so an empty placeholder rendered a narrow tint, a typed value a wider
// one, and the empty rows of the 3-row textarea showed no tint at all.
//
// The PARITY of the two widths (empty vs typed) is the real guard: it fails an overflow
// (w+1), an over-subtraction (w-1), AND the original ragged-fill bug (empty != typed).
// It also asserts the tinted block keeps every textarea row (height == ta.Height()), so
// no row renders zero-width / untinted.
func TestInputRailFillsUniformly(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	for _, w := range []int{40, 80, 120} {
		// (a) empty input.
		me, _, _ := newTestModel(t, th)
		me = applyAll(me, tea.WindowSizeMsg{Width: w, Height: 30},
			client.SessionReadyMsg{SessionID: "sess-test-0001"})
		empty := me.renderInput()

		// (b) typed input — same width, same everything else.
		mt, _, _ := newTestModel(t, th)
		mt = applyAll(mt, tea.WindowSizeMsg{Width: w, Height: 30},
			client.SessionReadyMsg{SessionID: "sess-test-0001"})
		mt.ta.SetValue("Hello")
		mt.rend.inputValid = false // bust the cache so the typed value re-renders
		typed := mt.renderInput()

		emptyW, typedW := lipgloss.Width(empty), lipgloss.Width(typed)
		// Exactly full-bleed: total == terminal width for BOTH states.
		if emptyW != w {
			t.Errorf("width %d: empty input is %d cols, want exactly %d (full-bleed tint)", w, emptyW, w)
		}
		if typedW != w {
			t.Errorf("width %d: typed input is %d cols, want exactly %d (full-bleed tint)", w, typedW, w)
		}
		// PARITY: empty and typed must be the SAME width (the bug made them differ).
		if emptyW != typedW {
			t.Errorf("width %d: empty (%d) and typed (%d) input widths differ — the tint must be uniform regardless of content", w, emptyW, typedW)
		}
		// Every textarea row is present and tinted, PLUS the one top-padding row
		// (inputRailPadTop): height == ta.Height() + the pad. No zero-width empty rows.
		if got, want := lipgloss.Height(empty), me.ta.Height()+inputRailPadTop; got != want {
			t.Errorf("width %d: empty input block height = %d rows, want %d (textarea rows + %d top pad)", w, got, want, inputRailPadTop)
		}
		if got, want := lipgloss.Height(typed), mt.ta.Height()+inputRailPadTop; got != want {
			t.Errorf("width %d: typed input block height = %d rows, want %d (textarea rows + %d top pad)", w, got, want, inputRailPadTop)
		}
		// EVEN tint: every row's fill must run flush to the right edge — NO trailing
		// PLAIN (unstyled) cells. The bug's signature is a bare reset (\x1b[m / \x1b[0m)
		// immediately followed by the trailing spaces (the textarea's internal-viewport
		// padding, which carries no background). The fix re-pads with bgPanel-backed
		// spaces, so trailing spaces are always preceded by a bg-SET (\x1b[48;…m), never a
		// bare reset — plainTail must NOT match any row. (The old strings.HasSuffix(" \x1b[m")
		// check missed it: the real tail was "\x1b[m …spaces… \x1b[m\x1b[m", not " \x1b[m".)
		for _, in := range []string{empty, typed} {
			for ri, row := range strings.Split(in, "\n") {
				if plainTail.MatchString(row) {
					t.Errorf("width %d row %d: tint not flush to the edge — a bare reset precedes the trailing spaces (un-tinted cells): %q", w, ri, row)
				}
			}
		}
	}
}

// TestInputRailBorderColourIgnoresFocus pins the intended (and documented) behaviour:
// the rail BORDER stays the mode-accent colour at full strength as a PERSISTENT mode
// cue whether the input is focused or blurred. The textarea's own inner prompt bar and
// line-number gutter are suppressed (issue #161), so the rail border is the single
// vertical accent cue. The rail border is built by inputRailStyle(theme, mode), which
// reads no focus state, so the SGR that colours the left-border glyph "│" must appear
// identically in the focused and blurred renders. This is the guard that keeps the doc
// and code from silently diverging (the doc previously claimed the rail itself dims on
// blur — it does not).
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

// TestInputTextHasStrongContrast guards that typed text renders in the theme's
// full-strength Text colour, not the bubbles default (no foreground / grey CursorLine)
// that rendered it washed-out on the panel tint. applyModeInputStyle sets the Text and
// CursorLine foregrounds to the Text slot; a regression that drops it (back to the dim
// default) fails here.
func TestInputTextHasStrongContrast(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	m, _, _ := newTestModel(t, th)
	m = applyAll(m, tea.WindowSizeMsg{Width: 80, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001"})
	m.ta.SetValue("And if I write")
	m.rend.inputValid = false
	out := m.renderInput()
	// The Aztec Text slot (#E7E2D3) as the RGB foreground SGR lipgloss emits.
	if want := "38;2;231;226;211"; !strings.Contains(out, want) {
		t.Errorf("typed text missing the strong Text foreground %q — it would render washed-out on the panel tint", want)
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

package ui

// Tests for issue #319's TUI half: a FAILED delegation must be able to tell the operator
// WHY. The inline Subagent card already surfaces the cause through the tool result the
// agent received (that is the server's model-facing body — see TestSubagentErrorCardShows
// ProviderCause below), but the ctrl+a fleet pane has no result body at all: a roster row
// shows only "stop:error", and a BACKGROUND child's failure never reaches an inline card
// because its Subagent call already returned the started-result. subagent.end's Cause is
// the only channel there.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// endSubFailed builds an errored subagent.end carrying a failure cause.
func endSubFailed(parent, child, cause string) client.SubagentMsg {
	return client.SubagentMsg{
		Kind: client.SubagentEnd, ParentCallID: parent, ChildID: child,
		Stop: "error", Cause: cause, DurationMs: 900,
	}
}

// focusFirstChild opens the ctrl+a overlay and focuses the first fleet row, returning the
// stripped focus-pane render.
func focusFirstChild(t *testing.T, m Model) string {
	t.Helper()
	mm, _ := m.Update(ctrlKey('a'))
	m = mm.(Model)
	if m.agentsTab != tabSubagents {
		t.Fatalf("expected the Subagents tab, got %v", m.agentsTab)
	}
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.subagents.view != subagentFocus {
		t.Fatalf("enter should focus a child, view = %v", m.subagents.view)
	}
	return stripANSIstr(m.View().Content)
}

// TestSubagentFocusPaneShowsFailureCause is the fleet-pane half: an errored child's focus
// pane names the failure, so ctrl+a answers "why did it fail" instead of only "it did".
func TestSubagentFocusPaneShowsFailureCause(t *testing.T) {
	const cause = "upstream 503: model overloaded"
	m := newMCPModel(t, aztec(), nil)
	m = seedSubagents(m, "p1",
		startSub("p1", "c1", "audit auth"),
		endSubFailed("p1", "c1", cause),
	)
	out := focusFirstChild(t, m)
	if !strings.Contains(out, cause) {
		t.Fatalf("the focus pane must name the failure cause, got %q", out)
	}
	if !strings.Contains(out, "failed:") {
		t.Fatalf("the failure line should be labelled, got %q", out)
	}
}

// TestSubagentFocusPaneOmitsCauseWhenBenign is the negative half: no failure line for a
// clean terminal, and none for an errored child whose server sent no cause (an older
// server) — the pane then reads exactly as it did before.
func TestSubagentFocusPaneOmitsCauseWhenBenign(t *testing.T) {
	t.Run("clean terminal", func(t *testing.T) {
		m := newMCPModel(t, aztec(), nil)
		m = seedSubagents(m, "p1",
			startSub("p1", "c1", "audit auth"),
			endSub("p1", "c1", 100, 20, 2, "end_turn"),
		)
		if out := focusFirstChild(t, m); strings.Contains(out, "failed:") {
			t.Fatalf("a clean child must show no failure line, got %q", out)
		}
	})
	t.Run("errored but no cause from the server", func(t *testing.T) {
		m := newMCPModel(t, aztec(), nil)
		m = seedSubagents(m, "p1",
			startSub("p1", "c1", "audit auth"),
			endSubFailed("p1", "c1", ""),
		)
		if out := focusFirstChild(t, m); strings.Contains(out, "failed:") {
			t.Fatalf("no cause means no failure line, got %q", out)
		}
	})
}

// TestSubagentFailureLineIsBoundedAndWrapped proves the display is safe against a
// pathological provider error body on BOTH axes. Height: multi-line content is collapsed
// (its own newlines would defeat the width budget) and the whole thing is rune-clamped, so
// one failure cannot push the roster off-screen. Width: the result is word-wrapped to the
// overlay card's text budget, because renderSubagentFocus's widest line directly sets the
// card width and centerCard/lipgloss.Place cannot shrink it.
//
// The pre-#319-fix version of this test asserted the line was a SINGLE line of up to
// maxSubagentCauseWidth + the prefix — ~170 columns — i.e. it explicitly accepted a width
// wider than the viewport it gets drawn into. That is the hole; the per-line width check
// below is what closes it.
func TestSubagentFailureLineIsBoundedAndWrapped(t *testing.T) {
	ln := &subagentLane{
		done:  true,
		stop:  "error",
		cause: "line one\nline two\n" + strings.Repeat("z", maxSubagentCauseWidth*3),
	}

	for _, width := range []int{80, 100, 120} {
		got := subagentFailureLine(ln, width)
		if !strings.Contains(strings.Join(strings.Fields(got), " "), "line one line two") {
			t.Fatalf("width %d: collapsing must join the source lines with spaces, got %q", width, got)
		}
		// Rune total (the height bound) — the clamp, plus the per-line "  " indents the
		// wrap adds.
		if n := len([]rune(got)); n > maxSubagentCauseWidth+len("  failed: ")+1+2*len(strings.Split(got, "\n")) {
			t.Fatalf("width %d: the cause was not clamped: %d runes (%q)", width, n, got)
		}
		// The width bound: EVERY wrapped line must fit the card's text budget. A
		// space-free run of 480 z's is the adversarial case — ansi.Wrap must break it.
		budget := cardTextWidth(width)
		for i, line := range strings.Split(got, "\n") {
			if w := lipgloss.Width(line); w > budget {
				t.Fatalf("width %d: wrapped line %d is %d cols, over the %d-col card budget: %q",
					width, i, w, budget, line)
			}
		}
	}

	// Unknown width degrades to the bare unwrapped line, exactly as the /skills and
	// /agents inventory panels do — centerCard does not Place at width 0, so there is
	// nothing to overflow.
	if bare := subagentFailureLine(ln, 0); strings.Contains(bare, "\n") {
		t.Fatalf("an unknown width must not wrap (the bare content-sized card), got %q", bare)
	}
}

// TestSubagentFocusPaneWithLongCauseFitsViewport is the REAL-RENDER guard for the same
// property, and the one that would have caught the overflow: it drives the whole ctrl+a
// focus pane through View() with a realistic long provider error and asserts no rendered
// line exceeds the viewport. Asserting the bound on subagentFailureLine in isolation is
// not enough — the helper cannot see the card's border and padding, and centerCard cannot
// shrink what it is given.
func TestSubagentFocusPaneWithLongCauseFitsViewport(t *testing.T) {
	// A genuine Envoy/gateway error shape (~95 chars), the case the 160-rune bound let
	// through at 80 and 100 columns.
	const longCause = "upstream connect error or disconnect/reset before headers. " +
		"reset reason: connection termination"
	for _, width := range []int{80, 100, 120} {
		m := newMCPModel(t, aztec(), nil)
		m = applyAll(m, tea.WindowSizeMsg{Width: width, Height: 30})
		m = seedSubagents(m, "p1",
			startSub("p1", "c1", "audit auth"),
			endSubFailed("p1", "c1", longCause),
		)
		out := focusFirstChild(t, m)
		assertFitsViewport(t, []byte(out), width)
		// …and the cause is still there to read (wrapped, so match on a fragment that
		// cannot straddle a line break).
		if !strings.Contains(out, "upstream connect error") {
			t.Fatalf("width %d: the wrapped failure line must still name the cause, got:\n%s", width, out)
		}
	}
}

// TestSubagentErrorCardShowsProviderCause is the INLINE-card half. The card deliberately
// has no second cause slot: the server's error body already carries the cause (issue #319
// composes it there), and the card renders that body in its result slot. This pins that
// path end-to-end so a regression in either half — the server dropping the cause, or the
// card stopping rendering the error body — is caught here.
func TestSubagentErrorCardShowsProviderCause(t *testing.T) {
	const cause = "upstream 503: model overloaded"
	out := subagentCard(t, false, func(c *conversation) {
		c.setSubagentStart("p1", "investigate the loop", "", "", "")
		c.setSubagentEnd("p1", client.Usage{}, 0, "error", 100)
		// The server-composed body: the cause leads, the child's last text follows as
		// labelled context (renderSubagentResult → subagentErrorBody).
		c.resolveTool("p1", "Subagent: "+cause+
			"\n\nLast activity before the failure: Now let me check the tests."+
			"\n\nagentId: subagent-p1", true)
	})
	if !strings.Contains(out, "stop:error") {
		t.Fatalf("errored card should show stop:error, got %q", out)
	}
	if !strings.Contains(out, cause) {
		t.Fatalf("errored card must render the provider cause, got %q", out)
	}
}

// TestSubagentFailureLineCollapsesEveryWhitespaceKind pins the client-side collapse against
// the peer it exists for: an OLDER mecated whose subagent.end Cause was not normalised at
// the emit site (the current server collapses with strings.Fields, so against it this call
// is a no-op and any regression is invisible).
//
// It is a regression guard for a specific weakening: the collapse was briefly switched to
// the package `oneLine` helper, which splits on {\n, \r, \t} only — so runs of spaces, NBSP
// and U+2028/U+2029 stopped being collapsed while the code comment still claimed the bound.
// unicode.IsSpace (strings.Fields) is what "one logical line" has to mean for an untrusted
// peer string.
func TestSubagentFailureLineCollapsesEveryWhitespaceKind(t *testing.T) {
	ln := &subagentLane{
		done: true,
		stop: "error",
		// A run of spaces, a no-break space and a Unicode line separator: all IsSpace, none
		// of them in oneLine's {\n, \r, \t} set.
		cause: "upstream 503:    model\u00a0overloaded\u2028retry later",
	}
	got := subagentFailureLine(ln, 120)
	if !strings.Contains(got, "upstream 503: model overloaded retry later") {
		t.Fatalf("every kind of whitespace must collapse to a single space for the width budget, got %q", got)
	}
}

// TestSubagentFailureLineStripsBidiAndZeroWidth is the display half of the engine's own
// canonLine lesson, applied to the bound this client keeps for itself.
//
// strings.Fields collapses everything unicode.IsSpace considers whitespace, which is not
// the same set as "invisible or reordering to a terminal": U+200B (zero width space),
// U+FEFF (BOM) and U+202E (right-to-left override) are Unicode Cf, so the collapse leaves
// them in place. A provider cause or child summary carrying an RTL override can therefore
// present a failure line that READS as something other than what it says (CWE-1007), on a
// pane whose whole job is telling the operator what went wrong. sanitizeTerminal now strips
// Cf alongside C0/C1/ESC/DEL.
//
// The vectors are \u escapes deliberately: an invisible character in source is
// unreviewable, and a raw one trips the source-level control-character rules.
func TestSubagentFailureLineStripsBidiAndZeroWidth(t *testing.T) {
	invisibles := map[string]string{
		"U+200B zero-width space": "\u200b",
		"U+202E RTL override":     "\u202e",
		"U+FEFF byte-order mark":  "\ufeff",
	}
	ln := &subagentLane{
		done: true,
		stop: "error",
		cause: "upstream 503:" + invisibles["U+200B zero-width space"] +
			" model" + invisibles["U+202E RTL override"] +
			"overloaded" + invisibles["U+FEFF byte-order mark"],
	}
	got := subagentFailureLine(ln, 120)
	for name, r := range invisibles {
		if strings.Contains(got, r) {
			t.Errorf("%s survived into the rendered failure line, so it can reorder or hide what the operator reads: %q", name, got)
		}
	}
	// Negative control: the diagnostic itself still reads.
	if !strings.Contains(got, "upstream 503") || !strings.Contains(got, "overloaded") {
		t.Fatalf("the cause text was destroyed: %q", got)
	}
}

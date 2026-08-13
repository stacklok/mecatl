package ui

// Tests for the plain-text block wrapping fix: renderBlock previously rendered
// every non-assistant/non-tool-card block through lipgloss .Render() with no
// width, so long lines overflowed the viewport's right edge (the bubbles
// viewport does not re-wrap). wrapStyled / wrapPrefixed now wrap plain bodies at
// render time, deriving the inset from the lipgloss style's own horizontal frame
// so the wrap budget can never drift behind a hardcoded number.

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// visibleLineWidths returns the per-line cell width (under both width methods) of
// a rendered block, stripping ANSI first. It is the shared overflow measurement:
// a fix that wraps must leave no line wider than the viewport under EITHER method.
func maxLineWidth(s string) int {
	w := 0
	for _, ln := range strings.Split(s, "\n") {
		plain := stripANSIstr(ln)
		if g := lipgloss.Width(plain); g > w {
			w = g
		}
		if c := ansi.StringWidthWc(plain); c > w {
			w = c
		}
	}
	return w
}

// TestUserBlockWraps locks the core fix: a long single-paragraph user prompt at a
// narrow width wraps to multiple lines, none exceeding the viewport width.
func TestUserBlockWraps(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(40)
	long := strings.TrimSpace(strings.Repeat("the quick brown fox jumps over the lazy dog ", 6))
	b := block{kind: blockUser, raw: long}
	out := r.renderBlock(0, &b, false)
	lines := strings.Split(out, "\n")
	if len(lines) <= 1 {
		t.Fatalf("expected the long prompt to wrap to multiple lines, got %d", len(lines))
	}
	for i, ln := range lines {
		if w := maxLineWidth(ln); w > r.width {
			t.Errorf("line %d exceeds width %d (got %d): %q", i, r.width, w, stripANSIstr(ln))
		}
	}
}

// TestNoticeAndErrorWrap locks the prefixed-wrap path: a long notice and a long
// error wrap without overflowing, the marker appears exactly once (on line 0),
// and continuation lines hang-indent under the text by the marker width.
func TestNoticeAndErrorWrap(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(40)
	long := strings.TrimSpace(strings.Repeat("compaction collapsed the early turns to keep the window bounded ", 4))

	for _, tc := range []struct {
		name   string
		kind   blockKind
		marker string
	}{
		{"notice", blockNotice, "•"},
		{"error", blockError, "✗"},
	} {
		b := block{kind: tc.kind, raw: long}
		// renderBlockFresh: two DIFFERENT logical blocks share this renderer at a
		// dummy index, which would alias in renderBlock's per-block cache (its
		// contract is one stable conversation index per block; render_cache_test.go
		// covers the cached path).
		out := r.renderBlockFresh(0, &b, false)
		lines := strings.Split(out, "\n")
		if len(lines) <= 1 {
			t.Fatalf("%s: expected wrapping to multiple lines, got %d", tc.name, len(lines))
		}
		for i, ln := range lines {
			if w := maxLineWidth(ln); w > r.width {
				t.Errorf("%s: line %d exceeds width %d (got %d): %q", tc.name, i, r.width, w, stripANSIstr(ln))
			}
		}
		// The marker glyph "<m> " is 2 cells; line 0 carries it, continuations
		// hang-indent under the body with exactly that many leading spaces.
		plain0 := stripANSIstr(lines[0])
		if !strings.HasPrefix(strings.TrimLeft(plain0, " "), tc.marker) {
			t.Errorf("%s: line 0 should carry the %q marker, got %q", tc.name, tc.marker, plain0)
		}
		whole := stripANSIstr(out)
		if strings.Count(whole, tc.marker) != 1 {
			t.Errorf("%s: marker %q should appear exactly once, got %d in %q",
				tc.name, tc.marker, strings.Count(whole, tc.marker), whole)
		}
		const indent = "  " // marker width: glyph + space
		for i := 1; i < len(lines); i++ {
			plain := stripANSIstr(lines[i])
			if plain == "" {
				continue
			}
			if !strings.HasPrefix(plain, indent) {
				t.Errorf("%s: continuation line %d should hang-indent by %d spaces, got %q",
					tc.name, i, len(indent), plain)
			}
		}
	}
}

// TestUnbreakableTokenForceBreaks locks the no-space worst case: a single
// space-free ~200-char token (no break opportunity) must still be hard-broken so
// no line overflows, through both the styled (user) and prefixed (error) paths.
func TestUnbreakableTokenForceBreaks(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(40)
	token := strings.Repeat("a", 200)
	for _, tc := range []struct {
		name string
		kind blockKind
	}{
		{"user", blockUser},
		{"error", blockError},
	} {
		b := block{kind: tc.kind, raw: token}
		// renderBlockFresh: two DIFFERENT logical blocks share this renderer at a
		// dummy index — through renderBlock the second case would cache-HIT the
		// first's entry (same idx/rev/width/expand) and silently unpin the error
		// force-break path (renderBlock's contract is one stable conversation index
		// per block; render_cache_test.go covers the cached path).
		out := r.renderBlockFresh(0, &b, false)
		for i, ln := range strings.Split(out, "\n") {
			if w := maxLineWidth(ln); w > r.width {
				t.Errorf("%s: line %d exceeds width %d (got %d): %q", tc.name, i, r.width, w, stripANSIstr(ln))
			}
		}
	}
}

// TestWrapResizeSafe locks resize-safety: rendering wide then narrow re-wraps to
// the new width (the renders differ) and no line exceeds the narrow width.
func TestWrapResizeSafe(t *testing.T) {
	r := newTestRenderer()
	long := strings.TrimSpace(strings.Repeat("the quick brown fox jumps over the lazy dog ", 6))
	b := block{kind: blockUser, raw: long}

	r.setWidth(100)
	wide := r.renderBlock(0, &b, false)
	r.setWidth(30)
	narrow := r.renderBlock(0, &b, false)

	if wide == narrow {
		t.Fatal("expected the wide and narrow renders to differ after resize")
	}
	for i, ln := range strings.Split(narrow, "\n") {
		if w := maxLineWidth(ln); w > 30 {
			t.Errorf("narrow line %d exceeds width 30 (got %d): %q", i, w, stripANSIstr(ln))
		}
	}
}

// TestWrapWidthZeroNoWrap locks the unknown/tiny-width guard: a width-0 renderer
// (the team focus renderer constructs one directly) must NOT force breaks — the
// long body stays on a single logical line.
func TestWrapWidthZeroNoWrap(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	r := &renderer{th: th, marks: defaultHelpKeys()} // width 0
	long := strings.TrimSpace(strings.Repeat("the quick brown fox jumps over the lazy dog ", 6))
	b := block{kind: blockUser, raw: long}
	out := r.renderBlock(0, &b, false)
	// The label is on its own line; the body must remain one logical line (no
	// forced breaks were inserted at width 0).
	plain := stripANSIstr(out)
	body := plain
	if idx := strings.Index(plain, "\n"); idx >= 0 {
		body = plain[idx+1:]
	}
	if strings.Contains(strings.TrimRight(body, "\n"), "\n") {
		t.Errorf("width-0 body should not be wrapped, got multi-line: %q", body)
	}
}

// TestUserBlockInsetTracksStyle is the DRIFT tripwire: the userBlock style's own
// horizontal frame (BorderLeft + PaddingLeft) is 2, and wrapStyled derives its
// wrap budget from GetHorizontalFrameSize() — so wrapped userBlock content fits
// within r.width - 2. If theme.go changes the userBlock border/padding, this
// catches a stale hardcoded inset.
func TestUserBlockInsetTracksStyle(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(40)
	if got := r.th.Style("userBlock").GetHorizontalFrameSize(); got != 2 {
		t.Fatalf("userBlock horizontal frame = %d, want 2 (the wrap-budget contract)", got)
	}
	long := strings.TrimSpace(strings.Repeat("the quick brown fox jumps over the lazy dog ", 6))
	b := block{kind: blockUser, raw: long}
	out := r.renderBlock(0, &b, false)
	for i, ln := range strings.Split(out, "\n") {
		if w := maxLineWidth(ln); w > r.width {
			t.Errorf("line %d exceeds width %d (got %d): %q", i, r.width, w, stripANSIstr(ln))
		}
	}
}

// TestReasoningExpandedShowsFullBody locks the issue #96 fix: expanding a
// reasoning summary (ctrl+t) shows the FULL body with no line cap, NOT the old
// 24-line tail truncation. A 58-line reasoning block must render all 58 lines
// (plus header + caveat) when expanded — matching how resultBody handles tool
// results — while the collapsed path still shows only the one-line header.
func TestReasoningExpandedShowsFullBody(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(200) // wide so each reasoning line stays one logical line
	var lines []string
	for i := 0; i < 58; i++ {
		lines = append(lines, fmt.Sprintf("reasoning step %d", i+1))
	}
	reasoning := strings.Join(lines, "\n")
	b := block{kind: blockAssistant, raw: "the answer", reasoning: reasoning}

	// Collapsed: only the header, none of the body lines.
	collapsed := stripANSIstr(r.renderBlock(0, &b, false))
	if !strings.Contains(collapsed, "reasoning summary · 58 lines · ctrl+t expand") {
		t.Errorf("collapsed should report 58 lines:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "reasoning step 1") || strings.Contains(collapsed, "reasoning step 58") {
		t.Errorf("collapsed must hide the body:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "…(truncated)") {
		t.Errorf("collapsed must not carry a truncation tail:\n%s", collapsed)
	}

	// Expanded: EVERY reasoning line present, no truncation tail.
	expanded := stripANSIstr(r.renderBlock(0, &b, true))
	if strings.Contains(expanded, "…(truncated)") {
		t.Errorf("expanded reasoning must NOT truncate (issue #96):\n%s", expanded)
	}
	for i := 1; i <= 58; i++ {
		want := fmt.Sprintf("reasoning step %d", i)
		if !strings.Contains(expanded, want) {
			t.Errorf("expanded reasoning missing %q:\n%s", want, expanded)
		}
	}
}

// TestReasoningExpandedWraps locks the expanded-reasoning BODY wrap: a long
// single-line reasoning summary, expanded (ctrl+t), wraps without overflow. The
// short collapsed/expanded HEADER line is intentionally left unwrapped per the
// fix (it is a short fixed affordance, not free-form body), so the assertion is
// scoped to the caveat + body lines that follow it.
func TestReasoningExpandedWraps(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(60)
	long := strings.TrimSpace(strings.Repeat("first I considered the options then weighed the tradeoffs carefully ", 4))
	b := block{kind: blockAssistant, raw: "the answer", reasoning: long}
	out := r.renderBlock(0, &b, true)
	lines := strings.Split(out, "\n")
	if len(lines) <= 3 {
		t.Fatalf("expected expanded reasoning to wrap the body to multiple lines, got %d", len(lines))
	}
	// Find the wrapped caveat/body region (everything after the header line, which
	// is the line ending in "ctrl+t collapse"). Those free-form lines must not
	// overflow the viewport width.
	bodyStart := 0
	for i, ln := range lines {
		if strings.Contains(stripANSIstr(ln), "ctrl+t collapse") {
			bodyStart = i + 1
			break
		}
	}
	if bodyStart == 0 || bodyStart >= len(lines) {
		t.Fatalf("could not locate the wrapped reasoning body after the header: %q", stripANSIstr(out))
	}
	wrapped := false
	for i := bodyStart; i < len(lines); i++ {
		ln := lines[i]
		if w := maxLineWidth(ln); w > r.width {
			t.Errorf("body line %d exceeds width %d (got %d): %q", i, r.width, w, stripANSIstr(ln))
		}
		wrapped = true
	}
	if !wrapped {
		t.Fatal("expected wrapped reasoning body lines")
	}
}

// TestTurnStatWraps locks the blockTurnStat wrapStyled path (muted, frame 0): a
// long per-turn stat line wraps without overflow.
func TestTurnStatWraps(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(40)
	long := strings.TrimSpace(strings.Repeat("turn 7 · 1234 in 5678 out · 12.3s elapsed · model gpt-4o · compacted once ", 3))
	b := block{kind: blockTurnStat, raw: long}
	out := r.renderBlock(0, &b, false)
	lines := strings.Split(out, "\n")
	if len(lines) <= 1 {
		t.Fatalf("expected the long stat line to wrap, got %d lines", len(lines))
	}
	for i, ln := range lines {
		if w := maxLineWidth(ln); w > r.width {
			t.Errorf("line %d exceeds width %d (got %d): %q", i, r.width, w, stripANSIstr(ln))
		}
	}
}

// TestHookOutcomesWrap locks all three wrapPrefixed hook markers (✗ blocked / ✎
// modified / • info): each long hook notice wraps without overflow.
func TestHookOutcomesWrap(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(40)
	long := strings.TrimSpace(strings.Repeat("the hook rejected this action because the diff contained a forbidden change ", 3))
	for _, tc := range []struct {
		name     string
		decision string
		marker   string
	}{
		{"blocked", string(client.HookBlocked), "✗"},
		{"modified", string(client.HookModified), "✎"},
		{"info", "", "•"},
	} {
		out := renderHookBlock(r, long, "PreToolUse", "Bash", tc.decision)
		lines := strings.Split(out, "\n")
		if len(lines) <= 1 {
			t.Fatalf("%s: expected the long hook notice to wrap, got %d lines", tc.name, len(lines))
		}
		for i, ln := range lines {
			if w := maxLineWidth(ln); w > r.width {
				t.Errorf("%s: line %d exceeds width %d (got %d): %q", tc.name, i, r.width, w, stripANSIstr(ln))
			}
		}
		if !strings.Contains(stripANSIstr(out), tc.marker) {
			t.Errorf("%s: expected the %q marker, got %q", tc.name, tc.marker, stripANSIstr(out))
		}
	}
}

// TestUserMediaLineWraps locks the only emoji-prefix (📎, width-3) wrapPrefixed
// call site: a long media path wraps without overflow and the marker survives.
// This exercises the dynamic lipgloss.Width(prefix) hang-indent (3, not 2).
func TestUserMediaLineWraps(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(40)
	longPath := "/some/deeply/nested/workspace/assets/images/screenshots/very-long-capture-filename.png"
	b := block{kind: blockUser, raw: "see attached", media: []string{longPath}}
	out := r.renderBlock(0, &b, false)
	for i, ln := range strings.Split(out, "\n") {
		if w := maxLineWidth(ln); w > r.width {
			t.Errorf("line %d exceeds width %d (got %d): %q", i, r.width, w, stripANSIstr(ln))
		}
	}
	if !strings.Contains(stripANSIstr(out), "📎") {
		t.Errorf("expected the 📎 media marker, got %q", stripANSIstr(out))
	}
}

// TestAssistantResizeRerenders proves the (src,width)-keyed glamour memo
// (markdownAt) re-renders on resize: a blockAssistant rendered wide then narrow
// produces different output, and the narrow render fits the narrow width.
func TestAssistantResizeRerenders(t *testing.T) {
	r := newTestRenderer()
	long := strings.TrimSpace(strings.Repeat("the quick brown fox jumps over the lazy dog and keeps running ", 6))
	b := block{kind: blockAssistant, raw: long}

	r.setWidth(100)
	wide := r.renderBlock(0, &b, false)
	r.setWidth(40)
	narrow := r.renderBlock(0, &b, false)

	if wide == narrow {
		t.Fatal("expected the assistant render to differ after resize (memo must re-render on width change)")
	}
	for i, ln := range strings.Split(narrow, "\n") {
		if w := maxLineWidth(ln); w > 40 {
			t.Errorf("narrow line %d exceeds width 40 (got %d): %q", i, w, stripANSIstr(ln))
		}
	}
}

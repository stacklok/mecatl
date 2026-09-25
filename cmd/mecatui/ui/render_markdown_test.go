package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestMarkdownNoTrailingPadding locks the trimTrailingSpaces hygiene: glamour
// right-pads every wrapped line out to the full wrap width. That padding is wasted
// bytes and pushes each row to the terminal's final column; trimTrailingSpaces
// strips it (retained as hygiene, not as the scramble fix — see the width-method
// note on TestMarkdownWidthMethodAgreement). markdown() must leave no rendered line
// carrying trailing spaces.
func TestMarkdownNoTrailingPadding(t *testing.T) {
	r := newTestRenderer() // width 100
	// A short paragraph (would otherwise pad to ~100 cols) plus inline code and an
	// em-dash, mirroring the assistant prose in the bug report.
	src := "The edits I made to `AGENTS.md` are committed in `04cca3` — already up to date."
	out := r.markdown(src)
	if strings.TrimSpace(out) == "" {
		t.Fatal("markdown returned empty for non-empty source")
	}
	for i, ln := range strings.Split(out, "\n") {
		if ln != strings.TrimRight(ln, " ") {
			t.Errorf("line %d has trailing padding spaces: %q", i, stripANSIstr(ln))
		}
	}
}

// TestMarkdownOrderedListMarkerSpacing pins the ordered-list marker separator:
// glamour renders the item number from Styles.Enumeration, and the marker→text
// separator is that style's BlockPrefix (stock glamour styles set ". " — see
// glamour styles.go). The from-scratch GlamourStyle must set it too, or an
// ordered list renders "1First item" (number and text run together).
func TestMarkdownOrderedListMarkerSpacing(t *testing.T) {
	r := newTestRenderer()
	out := stripANSIstr(r.markdown("1. First item\n2. Second item\n"))
	if !strings.Contains(out, "1. First item") {
		t.Errorf("ordered-list marker lost its separator:\n%s", out)
	}
	if !strings.Contains(out, "2. Second item") {
		t.Errorf("ordered-list marker lost its separator:\n%s", out)
	}
}

// TestMarkdownReservesFinalColumn locks the reserve-final-column hygiene: no
// rendered markdown line may occupy the terminal's FINAL column, measured under
// BOTH width methods. This is retained hygiene, not the scramble fix — that root
// cause is the width-method disagreement guarded by TestMarkdownWidthMethodAgreement.
// markdown() wraps one column short, so each line stays below the width under
// lipgloss/GraphemeWidth AND
// under WcWidth (the method the renderer actually paints with on terminals that do
// not confirm DEC mode 2027).
func TestMarkdownReservesFinalColumn(t *testing.T) {
	r := newTestRenderer() // width 100
	// Long single paragraph with no hard breaks, so glamour greedily fills lines
	// right up to the wrap boundary — the case where a wrapped line would otherwise
	// reach the full width.
	src := strings.TrimSpace(strings.Repeat(
		"Perfect now I have a comprehensive understanding of the mecatl repository and "+
			"will save this knowledge before continuing with the next implementation step. ",
		6))
	out := r.markdown(src)
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected the prose to wrap to multiple lines, got %d", len(lines))
	}
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w >= r.width {
			t.Errorf("line %d reaches the final column (GraphemeWidth %d >= %d): %q",
				i, w, r.width, stripANSIstr(ln))
		}
		if w := ansi.StringWidthWc(ln); w >= r.width {
			t.Errorf("line %d reaches the final column (WcWidth %d >= %d): %q",
				i, w, r.width, stripANSIstr(ln))
		}
	}
}

// emojiWidthFixtures is the realistic set used by the width-agreement tests: the
// bare and VS16 forms of common emoji, a ZWJ family, a regional-indicator flag,
// and the two markdown shapes from the scramble screenshot (a heading and a
// numbered list) — both now carrying a VS16-bearing width-1 emoji so the
// screenshot shapes genuinely diverge on pre-fix code, not just the isolated heart.
// Fixtures tagged "load-bearing: diverges pre-fix" actually fail before
// normalizeEmojiWidth is applied; the rest already agree and guard against a
// regression that would START mangling them.
var emojiWidthFixtures = []struct {
	name string
	src  string
}{
	{"bare-check", "✅"},  // already width-2 both ways (agrees)
	{"check-vs16", "✅️"}, // ✅ + VS16 already agrees (width 2 both ways); guards no-regression
	{"heart-vs16", "❤️"}, // load-bearing: diverges pre-fix (❤ + VS16, WcWidth 1 / GraphemeWidth 2)
	{"flag", "🇺🇸 ja"},    // load-bearing: diverges pre-fix (regional-indicator flag; first-scalar can not rescue -> placeholder)
	{"zwj-family", "\U0001F468\u200d\U0001F469\u200d\U0001F467"},       // man-ZWJ-woman-ZWJ-girl (agrees)
	{"heading", "## mecatl ⚠️"},                                        // load-bearing: diverges pre-fix (heading shape + VS16 warning)
	{"numbered-list", "1. ✅ first item ❤️\n2. ✅️ second item ⚠️ here"}, // load-bearing: diverges pre-fix (list shape + VS16 emoji)
}

// TestMarkdownWidthMethodAgreement is the AUTHORITATIVE guard for the streaming
// scramble. The bug is a width-method disagreement: glamour's word-wrap measures
// cells with GraphemeWidth (ansi.StringWidth) while Bubble Tea v2's differential
// renderer paints with WcWidth (ansi.StringWidthWc) on terminals that do not
// confirm DEC mode 2027. When the two disagree on an emoji cluster, every cell to
// its right is offset and the line scrambles ("mecatl" → "mec##atl", "1. ✅" losing
// its ". "), and it persists because the renderer's width method is fixed for the
// session. markdown() must normalise emoji presentation so that, for EVERY rendered
// line, the two methods agree. This test fails on the un-normalised code (the
// VS16 fixtures diverge) and passes once normalizeEmojiWidth is applied.
func TestMarkdownWidthMethodAgreement(t *testing.T) {
	r := newTestRenderer()
	for _, f := range emojiWidthFixtures {
		out := r.markdown(f.src)
		for i, ln := range strings.Split(out, "\n") {
			gw := ansi.StringWidth(ln)   // GraphemeWidth — glamour's wrap method
			wc := ansi.StringWidthWc(ln) // WcWidth — the renderer's paint method
			if gw != wc {
				t.Errorf("%s: line %d width methods disagree (GraphemeWidth %d != WcWidth %d): %q",
					f.name, i, gw, wc, stripANSIstr(ln))
			}
		}
	}
}

// TestNormalizeEmojiWidthAgreement exercises normalizeEmojiWidth directly (below
// glamour) so a regression is pinned to the helper, not the renderer: every
// fixture's output must have WcWidth == GraphemeWidth per line.
func TestNormalizeEmojiWidthAgreement(t *testing.T) {
	for _, f := range emojiWidthFixtures {
		out := normalizeEmojiWidth(f.src)
		for i, ln := range strings.Split(out, "\n") {
			if gw, wc := ansi.StringWidth(ln), ansi.StringWidthWc(ln); gw != wc {
				t.Errorf("%s: line %d width methods disagree after normalize (gw %d != wc %d): %q",
					f.name, i, gw, wc, ln)
			}
		}
	}
}

// TestNormalizeEmojiWidthPreservesContent guards that the normalizer touches ONLY
// width-divergent presentation artifacts (VS16 selectors / residual divergent
// clusters) and leaves every other rune, its order, and all surrounding text and
// whitespace intact. Plain prose must pass through byte-identical; a string with a
// VS16 must lose only the VS16; an already-agreeing emoji such as bare ✅ must
// survive whole. The ZWJ family differs under the current width methods, so
// the normalizer retains its first scalar to keep display widths aligned.
func TestNormalizeEmojiWidthPreservesContent(t *testing.T) {
	const vs16 = "️"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain-ascii", "First line.\n\nSecond paragraph — em dash, `code`.", "First line.\n\nSecond paragraph — em dash, `code`."},
		{"bare-check-untouched", "1. ✅ done", "1. ✅ done"},
		{"zwj-family-first-scalar", "team \U0001F468\u200d\U0001F469\u200d\U0001F467 here", "team \U0001F468 here"},
		// A letter + combining accent (a + U+0301) is one width-1 cluster under both
		// methods; the normalizer must never strip or mangle the combining mark.
		{"combining-accent-untouched", "a\u0301 cafe\u0301", "a\u0301 cafe\u0301"},
		// ❤+VS16 diverges (WcWidth 1 / GraphemeWidth 2), so its VS16 is stripped.
		{"strip-only-vs16", "I " + "❤" + vs16 + " it", "I ❤ it"},
		// The normalizer is least-lossy: it touches a cluster ONLY when it diverges.
		// ❤+VS16 diverges → VS16 stripped; ✅+VS16 already AGREES (width 2 both ways) →
		// left byte-for-byte intact, VS16 and all. So only the heart loses its VS16.
		{"mixed", "a " + "❤" + vs16 + " b ✅ c ✅" + vs16 + " d", "a ❤ b ✅ c ✅" + vs16 + " d"},
		// Regional-indicator width now agrees in the upgraded rendering stack, so it
		// remains intact rather than taking the legacy replacement fallback.
		{"flag-untouched", "\U0001F1FA\U0001F1F8 ja", "\U0001F1FA\U0001F1F8 ja"},
	}
	for _, c := range cases {
		if got := normalizeEmojiWidth(c.in); got != c.want {
			t.Errorf("%s: normalizeEmojiWidth(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestMarkdownAtNeverStale locks the per-block memoization (markdownAt): the
// cached render must always equal a fresh markdown() of the CURRENT src, never a
// stale earlier one. It walks a growing src (a streaming turn) through one index,
// asserting every step matches the uncached render — so the live block re-renders
// on change — and that a repeated identical src is byte-identical (a cache hit,
// not a re-render that could diverge). A second index with different content must
// not be contaminated by the first.
func TestMarkdownAtNeverStale(t *testing.T) {
	r := newTestRenderer()
	steps := []string{"Perfect", "Perfect! Now", "Perfect! Now I have a full picture."}
	for _, src := range steps {
		got := r.markdownAt(0, src)
		want := r.markdown(src)
		if got != want {
			t.Errorf("markdownAt(0, %q) returned stale render:\n got %q\nwant %q",
				src, stripANSIstr(got), stripANSIstr(want))
		}
	}
	// Repeated identical src is a cache hit — must be byte-identical.
	if a, b := r.markdownAt(0, steps[2]), r.markdownAt(0, steps[2]); a != b {
		t.Errorf("repeated markdownAt diverged: %q vs %q", stripANSIstr(a), stripANSIstr(b))
	}
	// A different index with different content is independent.
	other := "A separate block with its own text."
	if got, want := r.markdownAt(1, other), r.markdown(other); got != want {
		t.Errorf("index 1 contaminated by index 0:\n got %q\nwant %q",
			stripANSIstr(got), stripANSIstr(want))
	}
}

// TestMarkdownPreservesContent guards that stripping trailing padding never eats
// the visible text or its order (the regression we were chasing was scrambled,
// not merely padded, text — this keeps the content path honest).
func TestMarkdownPreservesContent(t *testing.T) {
	r := newTestRenderer()
	src := "First line stays first.\n\nSecond paragraph stays second."
	plain := stripANSIstr(r.markdown(src))
	first := strings.Index(plain, "First line stays first.")
	second := strings.Index(plain, "Second paragraph stays second.")
	if first < 0 || second < 0 {
		t.Fatalf("content lost: %q", plain)
	}
	if first > second {
		t.Errorf("content reordered: first=%d second=%d in %q", first, second, plain)
	}
}

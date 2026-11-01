package ui

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

// streamMarkdownCorpus covers the markdown shapes whose rendering can change as
// later lines arrive, or whose glamour output ends mid-line, plus width-sensitive
// text. Each entry is streamed prefix by prefix.
var streamMarkdownCorpus = map[string]string{
	"paragraphs": "First paragraph with **bold**, _emphasis_, `code`, and a [link](https://example.com).\n" +
		"It continues on a second line.\n\nSecond paragraph.  \nWith a hard break.\n\nThird.\n",
	"atx and setext headings": "# Title\n\nIntro text.\n\n## Section\nBody right under the heading.\n\n" +
		"Setext heading\n==============\n\nAnother\n---\n\n### Third level\n\nTail.\n",
	"tight and loose lists": "- one\n- two\n- three\n\nAfter the tight list.\n\n- loose one\n\n- loose two\n\n" +
		"  continued paragraph in item two\n\n- loose three\n\nDone.\n",
	"nested and ordered lists": "1. first\n   - nested a\n   - nested b\n     1. deep\n2. second\n3. third\n\n" +
		"7. starts at seven\n8. eight\n\n* star list\n+ plus list\n\nEnd.\n",
	"fences": "Before.\n\n```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```\n\nBetween.\n\n~~~\nplain tilde\n\n" +
		"with a blank line\n~~~\n\n````md\n```inner```\n````\n\nAfter.\n",
	"unclosed fence": "Text.\n\n```python\ndef f():\n    return 1\n",
	"tables": "| a | b | c |\n|---|:-:|--:|\n| 1 | 2 | 3 |\n| four | five | six |\n\nAfter table.\n\n" +
		"| solo |\n|------|\n| x |\n",
	"blockquotes":     "> quoted line one\n> quoted line two\nlazy continuation\n\n> second quote\n>\n> - list in quote\n\nOut.\n",
	"thematic breaks": "Above.\n\n---\n\nBelow.\n\n***\n___\n\nEnd.\n",
	"html and tabs":   "<div>\nraw html block\n</div>\n\nText\twith\ttabs.\n\n\tindented code\n\tsecond line\n\nInline <b>html</b>.\n",
	"definition list": "Term\n: definition one\n: definition two\n\nOther paragraph\n\n: late definition\n",
	"link references": "See [the docs][docs] and [more].\n\nMiddle paragraph.\n\n[docs]: https://example.com/docs\n[more]: https://example.com/more\n",
	"emoji and cjk": "Status ✅ done ❤️ love ⚠️ warn 👩‍💻 dev 🇺🇸 flag 1️⃣ keycap.\n\n" +
		"中文段落，用于测试宽字符换行是否正确处理，这里有足够多的字以便换行。\n\n- 項目一 ✅\n- 項目二 ⚠️\n",
	"heading runs":                "# One\n## Two\n### Three\nText after headings.\n# Four\n",
	"task list and strikethrough": "- [x] done item\n- [ ] open item\n\n~~struck~~ text and www.example.com autolink.\n",
	"crlf":                        "Line one\r\nline two\r\n\r\n- item\r\n- item two\r\n\r\nEnd.\r\n",
	"code in lists and quotes": "1. Run the build:\n\n   ```sh\n   task build\n   ```\n\n2. Then check:\n\n   | a | b |\n   |---|---|\n   | 1 | 2 |\n\n" +
		"> A quote with code:\n>\n> ```go\n> x := 1\n> ```\n\nAfter.\n",
	"heading and fence runs": headingRunsAnswer(4) + "Closing paragraph.\n\nOne more line.\n",
}

// newStreamTestRenderer returns a renderer whose assistant body wraps at width.
func newStreamTestRenderer(width int) *renderer {
	r := newTestRenderer()
	r.setWidth(width)
	return r
}

// assertStreamFrames streams src through the incremental path for one block,
// one frame per entry of ends (each a prefix length), and compares every
// compareEvery-th frame and the last with a fresh full render. It returns the most
// blocks the incremental path held settled, so callers can prove it split.
func assertStreamFrames(t *testing.T, name, src string, width int, ends []int, compareEvery int) int {
	t.Helper()
	inc := newStreamTestRenderer(width)
	full := newStreamTestRenderer(width)
	maxSettled := 0
	for i, end := range ends {
		prefix := src[:end]
		got := maskChromaColours(inc.markdownStreamed(1, prefix))
		maxSettled = max(maxSettled, len(inc.stream.settled))
		if i%compareEvery != 0 && i != len(ends)-1 {
			continue
		}
		want := maskChromaColours(full.markdown(prefix))
		if got != want {
			at := commonPrefixLen(got, want)
			t.Fatalf("%s at width %d, prefix %d/%d bytes %q: outputs differ at byte %d\nincremental: …%q\nfull:        …%q",
				name, width, end, len(src), prefix, at, clip(got, at), clip(want, at))
		}
	}
	return maxSettled
}

// runeEnds returns every prefix length of s that ends on a rune boundary.
func runeEnds(s string) []int {
	var ends []int
	for end := 1; end <= len(s); end++ {
		if end == len(s) || utf8.RuneStart(s[end]) {
			ends = append(ends, end)
		}
	}
	return ends
}

// lineEnds returns the prefix lengths after each line of s, so one frame can add
// a whole line, as a coalesced frame does.
func lineEnds(s string) []int {
	var ends []int
	for i := range len(s) {
		if s[i] == '\n' {
			ends = append(ends, i+1)
		}
	}
	if len(ends) == 0 || ends[len(ends)-1] != len(s) {
		ends = append(ends, len(s))
	}
	return ends
}

// unevenEnds returns prefix lengths for deltas of cycling uneven sizes, so a
// frame can add several lines at once and rewrite a block that had settled.
func unevenEnds(s string) []int {
	sizes := []int{1, 7, 19, 43, 3, 61, 11, 97}
	var ends []int
	for end, i := 0, 0; end < len(s); i++ {
		end = min(end+sizes[i%len(sizes)], len(s))
		for end < len(s) && !utf8.RuneStart(s[end]) {
			end++
		}
		ends = append(ends, end)
	}
	return ends
}

// chunkEnds returns the prefix lengths after each streamed chunk.
func chunkEnds(chunks []string) []int {
	ends := make([]int, len(chunks))
	n := 0
	for i, c := range chunks {
		n += len(c)
		ends[i] = n
	}
	return ends
}

// chromaColour matches the 256-colour foreground SGR chroma's terminal256
// formatter emits for fenced code. glamour's own styles use 24-bit colour.
var chromaColour = regexp.MustCompile(`\x1b\[38;5;\d+m`)

// maskChromaColours hides chroma's palette index. Its nearest-colour search
// breaks distance ties by map iteration order, so two full renders of the same
// code block can pick different indexes (the aztec string colour is one such
// tie). Text, layout, padding, and glamour's styles are still compared exactly.
func maskChromaColours(s string) string {
	return chromaColour.ReplaceAllString(s, "\x1b[38;5;?m")
}

func commonPrefixLen(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// clip returns up to 120 bytes of s around offset at, for failure messages.
func clip(s string, at int) string {
	return s[max(0, at-60):min(len(s), at+120)]
}

// TestStreamMarkdownMatchesFullRender is the byte-identity property: streaming
// any corpus answer one rune at a time renders exactly what a full render of the
// same prefix produces, at several widths.
func TestStreamMarkdownMatchesFullRender(t *testing.T) {
	for name, src := range streamMarkdownCorpus {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, width := range []int{24, 100} {
				settled := assertStreamFrames(t, name, src, width, runeEnds(src), 1)
				if settled == 0 && name != "link references" && name != "unclosed fence" {
					t.Errorf("width %d never settled a block; the incremental path was not exercised", width)
				}
				assertStreamFrames(t, name+" by line", src, width, lineEnds(src), 1)
				assertStreamFrames(t, name+" in uneven deltas", src, width, unevenEnds(src), 1)
			}
		})
	}
}

// TestStreamMarkdownBenchAnswerMatchesFullRender streams two sections of the
// benchmark answer with the benchmark's chunking. Every frame advances the
// incremental state; a sample of frames is compared, because full reference
// renders of the growing answer dominate the test's cost.
func TestStreamMarkdownBenchAnswerMatchesFullRender(t *testing.T) {
	answer := strings.Repeat(streamAnswerSection, 2)
	assertStreamFrames(t, "bench answer", answer, 120, chunkEnds(streamChunks(answer, 32)), 3)
}

// TestStreamMarkdownResetsOnRevision covers the non-append paths: a revised
// (shorter or rewritten) answer, a different block, and a width change all render
// the full document again.
func TestStreamMarkdownResetsOnRevision(t *testing.T) {
	r := newStreamTestRenderer(80)
	full := newStreamTestRenderer(80)
	steps := []struct {
		idx   int
		width int
		src   string
	}{
		{1, 80, "# Title\n\nFirst paragraph.\n\nSecond"},
		{1, 80, "# Title\n\nFirst paragraph.\n\nSecond paragraph.\n\n- a"},
		// Same length, so every settled block keeps its span; only the source
		// check can tell the settled heading changed.
		{1, 80, "# Tilte\n\nFirst paragraph.\n\nSecond paragraph.\n\n- a\n- b"},
		{1, 80, "# Retitled\n\nFirst paragraph.\n\nSecond paragraph.\n\n- a\n- b"},
		{1, 80, "# Retitled\n\nShort."},
		{2, 80, "Another block.\n\nWith two paragraphs."},
		{2, 50, "Another block.\n\nWith two paragraphs and a narrower width that wraps."},
		{1, 50, "# Retitled\n\nShort.\n\nBack to the first block."},
	}
	for i, step := range steps {
		r.setWidth(step.width)
		full.setWidth(step.width)
		got := maskChromaColours(r.markdownStreamed(step.idx, step.src))
		want := maskChromaColours(full.markdown(step.src))
		if got != want {
			t.Fatalf("step %d: incremental %q, full %q", i, got, want)
		}
		if i == 1 && len(r.stream.settled) == 0 {
			t.Fatal("precondition: the heading must have settled before it is rewritten")
		}
	}
}

// TestStreamMarkdownCoalescedRewriteOfSettledBlock: one frame can add enough
// lines to turn a settled paragraph into a definition-list term while the block
// count also grows. The source before the split is unchanged, so only the block
// spans show the prefix no longer holds.
func TestStreamMarkdownCoalescedRewriteOfSettledBlock(t *testing.T) {
	r := newStreamTestRenderer(80)
	full := newStreamTestRenderer(80)
	frames := []string{
		"Intro.\n\nTerm\n\nx",
		"Intro.\n\nTerm\n\nx more",
		"Intro.\n\nTerm\n\n: a definition\n\nNext para.\n",
	}
	for i, src := range frames {
		if got, want := r.markdownStreamed(1, src), full.markdown(src); got != want {
			t.Fatalf("frame %d: incremental %q, full %q", i, got, want)
		}
		if i == 1 && len(r.stream.settled) != 2 {
			t.Fatalf("precondition: want 2 settled blocks before the rewrite, got %d", len(r.stream.settled))
		}
	}
}

// TestStreamMarkdownSettlesFinishedBlocks proves the prefix is actually reused:
// after the first blocks finish, rendering a longer tail settles them, and their
// output is not rebuilt on the next delta.
func TestStreamMarkdownSettlesFinishedBlocks(t *testing.T) {
	r := newStreamTestRenderer(80)
	answer := strings.Repeat("A finished paragraph of streamed text.\n\n", 5) + "The open tail"
	r.markdownStreamed(1, answer)
	r.markdownStreamed(1, answer+" grows")
	if got := len(r.stream.settled); got != 5 {
		t.Fatalf("settled blocks = %d, want 5", got)
	}
	base := r.stream.rendered
	r.markdownStreamed(1, answer+" grows again")
	if n := r.stream.rendered - base; n != 1 || len(r.stream.settled) != 5 {
		t.Fatalf("a tail-only delta must render only the tail block, rendered %d blocks", n)
	}
}

// TestStreamMarkdownHeadingRunsStayLinear: headings and fenced blocks end
// mid-line, so a run of them never settles. A frame must still render each block
// a bounded number of times, not once per rejected split point.
func TestStreamMarkdownHeadingRunsStayLinear(t *testing.T) {
	r := newStreamTestRenderer(100)
	answer := headingRunsAnswer(50)
	for _, end := range lineEnds(answer) {
		base := r.stream.rendered
		r.markdownStreamed(1, answer[:end])
		blocks := 2 * strings.Count(answer[:end], "## Step")
		if n := r.stream.rendered - base; n > blocks+2 {
			t.Fatalf("prefix %d/%d: rendered %d blocks for a %d-block answer", end, len(answer), n, blocks)
		}
	}
}

// streamIntoModel appends src to the open assistant block in deltas of n bytes,
// flushing a frame after each one.
func streamIntoModel(m *Model, src string, n int) {
	for _, chunk := range streamChunks(src, n) {
		m.conv.appendAssistant(chunk)
		m.refreshView()
	}
}

// assertConversationMatchesFresh compares m's cached conversation render with a
// fresh renderer's at the same width.
func assertConversationMatchesFresh(t *testing.T, m Model) {
	t.Helper()
	got := maskChromaColours(m.rend.renderConversation(&m.conv, false))
	want := maskChromaColours(freshConvString(m.rend.th, m.rend.width, &m.conv, false))
	if got != want {
		at := commonPrefixLen(got, want)
		t.Fatalf("cached render differs from fresh at byte %d\ncached: …%q\nfresh:  …%q", at, clip(got, at), clip(want, at))
	}
}

// TestStreamMarkdownWidthChangeMidStream: a resize while an answer streams
// renders the rest at the new width; nothing settled at the old width survives.
func TestStreamMarkdownWidthChangeMidStream(t *testing.T) {
	m := newStreamBenchModel(t)
	answer := streamAnswer()
	streamIntoModel(&m, answer[:len(answer)/2], 64)
	if len(m.rend.stream.settled) == 0 {
		t.Fatal("precondition: blocks must settle before the resize")
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 70, Height: 40})
	streamIntoModel(&m, answer[len(answer)/2:], 64)
	assertConversationMatchesFresh(t, m)
}

// TestStreamMarkdownForgottenOnRebuild: a rebuilt conversation can reuse the
// streaming block's index, so resetSession forgets the stream's progress and an
// answer of the same shape at that index renders from its own source.
func TestStreamMarkdownForgottenOnRebuild(t *testing.T) {
	m := newStreamBenchModel(t)
	streamIntoModel(&m, "# Title\n\nFirst paragraph.\n\nSecond paragraph.\n\nTail", 8)
	if len(m.rend.stream.settled) == 0 {
		t.Fatal("precondition: blocks must settle before the rebuild")
	}
	m = m.resetSession()
	if m.rend.stream.started || len(m.rend.stream.settled) != 0 {
		t.Fatal("resetSession must forget the stream's progress")
	}
	m.conv.addUser("Explain the change.")
	m.conv.startAssistant()
	streamIntoModel(&m, "# Tilte\n\nFirst paragraph.\n\nSecond paragraph.\n\nTail", 8)
	assertConversationMatchesFresh(t, m)
}

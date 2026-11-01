package ui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
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
}

// newStreamTestRenderer returns a renderer whose assistant body wraps at width.
func newStreamTestRenderer(width int) *renderer {
	r := newRenderer(theme.New("aztec", theme.AztecPalette()), defaultHelpKeys())
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
		if end == len(s) || utf8RuneStart(s[end]) {
			ends = append(ends, end)
		}
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
	settledOut := r.stream.out
	r.markdownStreamed(1, answer+" grows again")
	if r.stream.out != settledOut || len(r.stream.settled) != 5 {
		t.Fatal("settled prefix was rebuilt for a tail-only delta")
	}
}

package ui

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

// assertRail renders body through rail and compares it with lipgloss rendering
// the whole body at once.
func assertRail(t *testing.T, r *renderer, rail *assistantRail, name, body string) {
	t.Helper()
	style := r.assistantRailStyle()
	if got, want := rail.render(style, body), style.Render(body); got != want {
		at := commonPrefixLen(got, want)
		t.Fatalf("%s: body %q: rail differs from lipgloss at byte %d\nrail:     …%q\nlipgloss: …%q",
			name, body, at, clip(got, at), clip(want, at))
	}
}

// TestAssistantRailMatchesLipgloss is the byte-identity property: the per-line
// rail equals lipgloss over the whole body, for edge-case bodies and for every
// frame of the streamed corpus answers, with and without a reasoning summary
// above them. One rail serves every body in turn, so line reuse across frames
// and across unrelated bodies is exercised.
func TestAssistantRailMatchesLipgloss(t *testing.T) {
	r := newStreamTestRenderer(60)
	rail := &assistantRail{}
	for _, body := range []string{
		"", "x", "\n", "\n\n", "a\n\nbbbb", "  trailing  \nshort", "\ttab\tstops\nwide 中文 ✅ ❤️",
		"same first line\nlonger second line here", "same first line\nshort",
	} {
		assertRail(t, r, rail, "edge", body)
	}
	thinking := scrollback.AssistantCardSnapshot{Reasoning: "**Weighing the options**\nRead the handler first, then the tests."}
	collapsed := r.renderReasoningSnapshot(thinking, false)
	expanded := r.renderReasoningSnapshot(thinking, true)
	for name, src := range streamMarkdownCorpus {
		for _, end := range runeEnds(src) {
			md := r.markdownStreamed(1, src[:end])
			assertRail(t, r, rail, name, md)
			assertRail(t, r, rail, name+" with reasoning", collapsed+"\n\n"+md)
			assertRail(t, r, rail, name+" with expanded reasoning", expanded+"\n\n"+md)
		}
	}
}

// TestAssistantRailReusesUnchangedLines: a frame that only grows the last line
// renders that line alone, and keeps every earlier line's output.
func TestAssistantRailReusesUnchangedLines(t *testing.T) {
	r := newStreamTestRenderer(60)
	rail := &assistantRail{}
	style := r.assistantRailStyle()
	rail.render(style, "first line\nsecond line\nthird")
	first := rail.railed[0]
	rail.render(style, "first line\nsecond line\nthird line grows")
	if len(rail.railed) != 3 || rail.railed[0] != first {
		t.Fatalf("unchanged lines must be reused, got %q", rail.railed)
	}
	if want := style.Render("third line grows"); rail.railed[2] != want {
		t.Errorf("changed line must be re-rendered: got %q, want %q", rail.railed[2], want)
	}
	if got := strings.Count(rail.render(style, "only one"), "\n"); got != 0 {
		t.Errorf("a shorter body must drop the old lines, got %d newlines", got)
	}
}

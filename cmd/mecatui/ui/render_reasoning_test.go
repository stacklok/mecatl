package ui

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func TestReasoningStandaloneHeadings(t *testing.T) {
	for _, th := range []theme.Theme{theme.NewRegistry().Default(), theme.Solar()} {
		t.Run(th.Name, func(t *testing.T) {
			r := newRenderer(th, defaultHelpKeys())
			r.setWidth(100)
			style := th.Style("transcriptReasoningText")
			for _, tc := range []struct {
				source, text string
				heading      bool
			}{
				{"**Weighing the options**", "Weighing the options", true},
				{"  **Choosing an approach**  ", "Choosing an approach", true},
				{"Use **small changes** here.", "Use **small changes** here.", false},
				{"**First** and **second**", "**First** and **second**", false},
				{"**Still streaming", "**Still streaming", false},
				{"**", "**", false},
				{"****", "****", false},
				{"## Not a heading", "## Not a heading", false},
				{"Use `tests` and [links](url).", "Use `tests` and [links](url).", false},
			} {
				t.Run(tc.source, func(t *testing.T) {
					want := style.Bold(tc.heading).Render(tc.text)
					if got := r.reasoningBody(tc.source); got != want {
						t.Fatalf("body = %q, want muted italic text %q", got, want)
					}
				})
			}
			p := scrollback.AssistantCardSnapshot{Reasoning: "**Heading**\nfirst line\n\nsecond line"}
			out := stripANSIstr(r.renderReasoningSnapshot(p, true))
			if !strings.HasSuffix(out, "Heading\nfirst line\n\nsecond line") {
				t.Fatalf("reasoning must preserve plain-text line breaks: %q", out)
			}
			if strings.Count(out, "reasoning summary") != 1 || strings.Contains(out, "may not reflect") {
				t.Fatalf("expected one summary header, without a disclaimer: %q", out)
			}
			if collapsed := stripANSIstr(r.renderReasoningSnapshot(p, false)); strings.Contains(collapsed, "Heading") || !strings.Contains(collapsed, "f9 expand") {
				t.Fatalf("collapsed reasoning exposed its body: %q", collapsed)
			}
			if r.mdRenders != 0 {
				t.Fatal("reasoning must not invoke the Markdown renderer")
			}
		})
	}
}

func TestReasoningHeadingCompletesWhileStreaming(t *testing.T) {
	r := newTestRenderer()
	c := &conversation{}
	c.appendReasoning("**Choosing")
	if out := stripANSIstr(strings.Join(r.renderConversationFrame(&c.scrollback, true).lines, "\n")); !strings.Contains(out, "**Choosing") {
		t.Fatalf("incomplete heading should remain literal: %q", out)
	}
	c.appendReasoning(" an approach**\nKeep the change small.")
	out := stripANSIstr(strings.Join(r.renderConversationFrame(&c.scrollback, true).lines, "\n"))
	if strings.Contains(out, "**") || !strings.Contains(out, "Choosing an approach") || !strings.Contains(out, "Keep the change small.") {
		t.Fatalf("completed heading did not refresh: %q", out)
	}
}

func TestReasoningSanitizesTerminalControls(t *testing.T) {
	r := newTestRenderer()
	p := scrollback.AssistantCardSnapshot{Reasoning: "**Safe heading**\n\nhello\x1b[2Jworld\x1b]0;malicious title\a"}
	out := r.renderReasoningSnapshot(p, true)
	if strings.Contains(out, "\x1b[2J") || strings.Contains(out, "\x1b]0;") || strings.Contains(out, "\a") {
		t.Fatalf("reasoning emitted terminal controls: %q", out)
	}
	if !strings.Contains(stripANSIstr(out), "Safe heading") || !strings.Contains(stripANSIstr(out), "hello") {
		t.Fatalf("sanitization lost visible content: %q", out)
	}
}

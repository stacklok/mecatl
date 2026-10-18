package ui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func TestPlainTextLayoutExpandsTabsBeforeWrapping(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	style := th.Style("toolArgs")
	text := strings.Repeat("alpha\tbeta\tgamma\tdelta ", 4)
	expanded := strings.ReplaceAll(text, "\t", "    ")
	for _, width := range []int{20, 40, 60} {
		for _, tc := range []struct {
			name   string
			render func(string) string
		}{
			{"reasoning", func(s string) string {
				r := newTestRenderer()
				r.setWidth(width)
				return r.renderSnapshot(0, testSnapshot(0, scrollback.AssistantCardSnapshot{Reasoning: s}), true)
			}},
			{"card", func(s string) string { return style.Render(wrapCardText(s, width)) }},
			{"focus", func(s string) string { return style.Render(wrapFocusMetadataAtWidth(s, width)) }},
			{"hanging", func(s string) string { return style.Render(hangingIndentWrap(s, "  ", width)) }},
			{"description", func(s string) string { return style.Render(indentWrap(s, width)) }},
			{"skills", func(s string) string { return strings.Join(skillsTextLines(style, s, width), "\n") }},
			{"agents", func(s string) string { return strings.Join(renderAgentsInvTextLines(style, s, width), "\n") }},
			{"title", func(s string) string { return style.Render(truncateDisplayWidth(s, width)) }},
			{"approval", func(s string) string { return style.Render(wrapAskArgs(s, width)) }},
			{"approval continuation", func(s string) string { return style.Render(wrapAskArgsContinuations(th, s, width)) }},
		} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, width), func(t *testing.T) {
				got := tc.render(text)
				if want := tc.render(expanded); got != want {
					t.Errorf("tabbed layout differs from expanded source:\ngot %q\nwant %q", got, want)
				}
				if w := maxLineWidth(got); w > width {
					t.Errorf("rendered width %d exceeds %d: %q", w, width, got)
				}
			})
		}
	}
}

func TestStyledTextLayoutUsesStyleTabWidth(t *testing.T) {
	text := strings.Repeat("alpha\tbeta\tgamma ", 4)
	for _, tabWidth := range []int{0, 2, 8} {
		style := lipgloss.NewStyle().TabWidth(tabWidth)
		expanded := strings.ReplaceAll(text, "\t", strings.Repeat(" ", tabWidth))
		for _, render := range []func(string) string{
			func(s string) string { return strings.Join(skillsTextLines(style, s, 30), "\n") },
			func(s string) string { return strings.Join(renderAgentsInvTextLines(style, s, 30), "\n") },
		} {
			if got, want := render(text), render(expanded); got != want {
				t.Errorf("tab width %d: got %q, want %q", tabWidth, got, want)
			}
		}
	}
}

func TestSoulLayoutExpandsTabsBeforeViewport(t *testing.T) {
	text := strings.Repeat("alpha\tbeta\tgamma\tdelta ", 4)
	th := theme.New("aztec", theme.AztecPalette())
	render := func(content string, width int) string {
		s := &soulState{
			view: soulPanel,
			soul: client.Soul{Content: content, Present: true, Provenance: client.SoulProvenanceUser},
			deps: surfaceDeps{theme: th, marks: defaultHelpKeys()},
		}
		out, _ := s.Render(width, 40)
		return out
	}
	for _, width := range []int{20, 60, 20} {
		if got, want := render(text, width), render(strings.ReplaceAll(text, "\t", "    "), width); got != want {
			t.Errorf("width %d: viewport lost or reflowed tabbed content: got %q, want %q", width, got, want)
		}
	}
}

func TestMemoryDescriptionExpandsTabsBeforeListLayout(t *testing.T) {
	text := strings.Repeat("alpha\tbeta\tgamma\tdelta ", 4)
	_, tabbed := savedMemoryOpened(t, []client.UserModelEntry{{Key: "fact/test", Description: text}})
	_, expanded := savedMemoryOpened(t, []client.UserModelEntry{{Key: "fact/test", Description: strings.ReplaceAll(text, "\t", "    ")}})
	for _, width := range []int{20, 60, 20} {
		got, _ := tabbed.Render(width, 24)
		want, _ := expanded.Render(width, 24)
		if got != want {
			t.Errorf("width %d: tabbed memory description differs: got %q, want %q", width, got, want)
		}
	}
}

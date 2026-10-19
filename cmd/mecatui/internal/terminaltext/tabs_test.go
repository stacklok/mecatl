package terminaltext

import (
	"testing"

	"charm.land/lipgloss/v2"
)

func TestExpandTabs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		width int
		want  string
	}{
		{"empty", "", 4, ""},
		{"no tabs", "界 plain\ntext", 4, "界 plain\ntext"},
		{"fixed spaces not tab stops", "a\tb\t\n\t界", 4, "a    b    \n    界"},
		{"one space", "\ta\t", 1, " a "},
		{"two spaces", "a\t\tb", 2, "a    b"},
		{"remove", "a\tb\n\tc", 0, "ab\nc"},
		{"preserve", "a\tb", -1, "a\tb"},
		{"negative", "a\tb", -2, "a\tb"},
		{"not sanitization", "\x1b[31ma\tb\x1b[0m", 2, "\x1b[31ma  b\x1b[0m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExpandTabs(tc.text, tc.width); got != tc.want {
				t.Fatalf("ExpandTabs(%q, %d) = %q, want %q", tc.text, tc.width, got, tc.want)
			}
			if tc.width >= -1 {
				style := lipgloss.NewStyle().TabWidth(tc.width)
				if got, want := style.Render(ExpandTabs(tc.text, tc.width)), style.Render(tc.text); got != want {
					t.Fatalf("expansion disagrees with style: got %q, want %q", got, want)
				}
			}
		})
	}
}

func TestExpandStyleTabs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		style lipgloss.Style
		want  string
	}{
		{"default", lipgloss.NewStyle(), "a    b"},
		{"styled default", lipgloss.NewStyle().Bold(true).PaddingLeft(1), "a    b"},
		{"explicit zero", lipgloss.NewStyle().TabWidth(0), "ab"},
		{"explicit two", lipgloss.NewStyle().TabWidth(2), "a  b"},
		{"explicit eight", lipgloss.NewStyle().TabWidth(8), "a        b"},
		{"no conversion", lipgloss.NewStyle().TabWidth(lipgloss.NoTabConversion), "a\tb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const text = "a\tb"
			before := tc.style.Render(text)
			got := ExpandStyleTabs(text, tc.style)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if rendered := tc.style.Render(got); rendered != before {
				t.Fatalf("expansion changed style output: got %q, want %q", rendered, before)
			}
			if after := tc.style.Render(text); after != before {
				t.Fatalf("expansion mutated caller's style: got %q, want %q", after, before)
			}
		})
	}
}

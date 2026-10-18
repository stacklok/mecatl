package blocks

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestPlainCardLayoutExpandsTabsBeforeWrapping(t *testing.T) {
	text := strings.Repeat("alpha\tbeta\tgamma\tdelta ", 4)
	for _, tabWidth := range []int{0, 2, 4, 8} {
		style := lipgloss.NewStyle().TabWidth(tabWidth)
		expanded := strings.ReplaceAll(text, "\t", strings.Repeat(" ", tabWidth))
		for _, width := range []int{20, 40} {
			for _, render := range []func(string) string{
				func(s string) string { return wrapStyled(s, style.PaddingLeft(1), width) },
				func(s string) string { return wrapPrefixed("◆ ", s, style, width) },
			} {
				got := render(text)
				if want := render(expanded); got != want {
					t.Errorf("tab width %d, width %d: got %q, want %q", tabWidth, width, got, want)
				}
				for _, row := range strings.Split(got, "\n") {
					if w := ansi.StringWidth(row); w > width {
						t.Errorf("tab width %d: rendered row width %d exceeds %d", tabWidth, w, width)
					}
				}
			}
		}
	}
}

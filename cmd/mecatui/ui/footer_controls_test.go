package ui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func TestRenderFooterControls(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())

	t.Run("wraps and preserves full-label geometry", func(t *testing.T) {
		lines, controls := renderFooterControls(th, 11, []footerControlSpec{
			{hint: "a", label: "Apply", enabled: true},
			{hint: "esc", label: "Close", enabled: true},
		})
		if got, want := stripANSIstr(strings.Join(lines, "\n")), "[a Apply]\n[esc Close]"; got != want {
			t.Fatalf("lines = %q, want %q", got, want)
		}
		want := []renderedFooterControl{
			{rect: cellRect{x0: 0, x1: 9, y0: 0, y1: 1}, index: 0},
			{rect: cellRect{x0: 0, x1: 11, y0: 1, y1: 2}, index: 1},
		}
		if !reflect.DeepEqual(controls, want) {
			t.Fatalf("controls = %#v, want %#v", controls, want)
		}
		if got, want := lines[0], th.Style("viewport").Bold(true).Render("[a Apply]"); got != want {
			t.Fatal("control lost bold viewport style")
		}
	})

	t.Run("omits disabled controls without renumbering inputs", func(t *testing.T) {
		lines, controls := renderFooterControls(th, 80, []footerControlSpec{
			{hint: "a", label: "Apply", enabled: false},
			{hint: "esc", label: "Close", enabled: true},
		})
		if got := stripANSIstr(strings.Join(lines, "\n")); got != "[esc Close]" {
			t.Fatalf("lines = %q", got)
		}
		want := []renderedFooterControl{{rect: cellRect{x0: 0, x1: 11, y0: 0, y1: 1}, index: 1}}
		if !reflect.DeepEqual(controls, want) {
			t.Fatalf("controls = %#v, want %#v", controls, want)
		}
	})

	t.Run("fails atomically when an enabled label is too wide", func(t *testing.T) {
		lines, controls := renderFooterControls(th, 9, []footerControlSpec{
			{hint: "a", label: "Apply", enabled: true},
			{hint: "esc", label: "Close", enabled: true},
		})
		if lines != nil || controls != nil {
			t.Fatalf("too-wide footer = %#v, %#v; want nil, nil", lines, controls)
		}
	})

	t.Run("sanitizes hints before measuring", func(t *testing.T) {
		lines, controls := renderFooterControls(th, 80, []footerControlSpec{{hint: "a\n\t\x1b[31m", label: "Go", enabled: true}})
		if got, want := stripANSIstr(lines[0]), "[a[31m Go]"; got != want {
			t.Fatalf("line = %q, want %q", got, want)
		}
		if strings.ContainsAny(stripANSIstr(lines[0]), "\n\t\x1b") || controls[0].rect.x1 != ansi.StringWidth("[a[31m Go]") {
			t.Fatalf("unsafe hint or wrong geometry: %q, %#v", lines[0], controls[0].rect)
		}
	})
}

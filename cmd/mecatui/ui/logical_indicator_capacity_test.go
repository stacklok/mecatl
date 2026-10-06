package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

func TestLogicalIndicatorRenderersRespectBodyCapacity(t *testing.T) {
	for _, capacity := range []int{2, 3} {
		t.Run("mention-capacity", func(t *testing.T) {
			st := scenarioMentionState(scenarioMentionMatches(5))
			st.list.SetCursor(2)
			got := ansi.Strip(renderMentionSized(testTheme(), st, 32, capacity))
			assertRenderedListCapacity(t, st.list.ViewWithIndicators(capacity, true), got, capacity, " @")
		})
		t.Run("palette-capacity", func(t *testing.T) {
			st := scenarioPaletteState([]client.Command{
				{Name: "alpha", Description: "first"},
				{Name: "beta", Description: "second"},
				{Name: "gamma", Description: "third"},
				{Name: "delta", Description: "fourth"},
				{Name: "epsilon", Description: "fifth"},
			})
			st.list.SetCursor(2)
			got := ansi.Strip(renderPaletteSized(testTheme(), st, client.Capabilities{}, "/", 48, capacity))
			assertRenderedListCapacity(t, st.list.ViewWithIndicators(capacity, true), got, capacity, " /")
		})
	}
}

func assertRenderedListCapacity(t *testing.T, view bounded.ListView, rendered string, capacity int, rowPrefix string) {
	t.Helper()
	chrome := 0
	if view.Above > 0 {
		chrome++
	}
	if view.Below > 0 {
		chrome++
	}
	if len(view.Rows)+chrome > capacity {
		t.Fatalf("logical body uses %d rows with capacity %d: %#v", len(view.Rows)+chrome, capacity, view)
	}

	bodyRows := 0
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, rowPrefix) || strings.Contains(line, "↑ +") || strings.Contains(line, "↓ +") {
			bodyRows++
		}
	}
	if bodyRows > capacity {
		t.Fatalf("rendered body uses %d rows with capacity %d:\n%s", bodyRows, capacity, rendered)
	}
}

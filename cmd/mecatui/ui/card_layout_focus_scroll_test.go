package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// TestMecatuiCardLayout_Scenario3_FocusAndScrollableViewsRemainUsable verifies
// AC3.3: focus panes and cursor-windowed pickers retain their existing selection
// and height behaviour while every visible dynamic row fits the offered body width.
func TestMecatuiCardLayout_Scenario3_FocusAndScrollableViewsRemainUsable(t *testing.T) {
	const width = 32
	th, hk := aztec(), defaultHelpKeys()
	long := strings.Repeat("unbreakable-dynamic-value-", 6)
	assertFits := func(t *testing.T, name, out string) {
		t.Helper()
		for row, line := range strings.Split(stripANSIstr(out), "\n") {
			if !strings.Contains(line, "unbreakable-dynamic-value-") {
				continue
			}
			if got := maxLineWidth(line); got > width {
				t.Errorf("%s row %d width = %d, want <= %d: %q", name, row, got, width, line)
			}
		}
	}

	t.Run("focused team trace remains selected and height-bounded", func(t *testing.T) {
		lanes := []teamLane{{name: "selected", trace: []teamTrace{{kind: teamTraceTool, name: long, detail: long}, {kind: teamTraceTool, name: long, detail: long}, {kind: teamTraceTool, name: long, detail: long}, {kind: teamTraceTool, name: long, detail: long}}}}
		block := &block{teamLanes: lanes}
		out := renderTeamFocus(th, block, "selected", hk, width, 15)
		assertFits(t, "team focus", out)
		if !strings.Contains(stripANSIstr(out), "selected") {
			t.Fatalf("focused member was lost: %q", stripANSIstr(out))
		}
		if !strings.Contains(stripANSIstr(out), "lines 1–") {
			t.Fatalf("height-bounded trace lost its accurate range cue: %q", stripANSIstr(out))
		}
	})

	t.Run("models cursor keeps its window and selected row", func(t *testing.T) {
		models := make([]client.ModelInfo, 8)
		for i := range models {
			models[i] = client.ModelInfo{ProviderID: "provider", ID: long + string(rune('a'+i))}
		}
		picker := &modelsState{catalog: modelCatalog{models: models}, filtered: models, deps: surfaceDeps{keys: defaultKeys(), theme: th, marks: hk}}
		prefix, suffix := modelsFixedLines(*picker, "")
		height := len(prefix) + len(suffix) + 3
		out, _ := picker.Render(width, height)
		assertFits(t, "models initial", out)
		picker.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
		out, _ = picker.Render(width, height)
		assertFits(t, "models paged", out)
		view := picker.list.View()
		if picker.list.Cursor() != 0 || len(view.Rows) == 0 || view.Rows[0].ItemLine == 0 || !strings.Contains(stripANSIstr(out), "▶") {
			t.Fatalf("paged oversized model = cursor %d rows %v, selected marker missing from %q", picker.list.Cursor(), view.Rows, stripANSIstr(out))
		}
	})

	t.Run("resource preview retains its source-line truncation within width", func(t *testing.T) {
		preview := strings.Join([]string{long, long, long, long, long, long, long, long, long, long, long, long, long}, "\n")
		state := &mcpState{view: mcpResourcePrev, preview: preview, deps: surfaceDeps{theme: th, marks: hk}}
		out, _ := state.Render(width, 20)
		assertFits(t, "resource preview", out)
		plain := stripANSIstr(out)
		if !strings.Contains(plain, hk.expandTools) {
			t.Fatalf("resource preview lost its existing truncation cue: %q", plain)
		}
	})
}

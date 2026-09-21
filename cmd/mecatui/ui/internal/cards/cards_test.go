package cards

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func scenarioToolSnapshot() ToolSnapshot {
	return SnapshotTool(ToolInput{
		Title:     "Read",
		Arguments: []string{"path: README.md"},
		Results:   []string{"first result", "second result"},
		Labels:    map[string]string{"source": "workspace"},
	})
}

func scenarioLayout() Layout {
	return SnapshotLayout(LayoutInput{
		Width:        48,
		Expanded:     true,
		VisibleHints: []string{"ctrl+t collapse"},
		Dialect:      1,
		Appearance: Appearance{
			Frame:         Frame{Top: "+", Bottom: "+", Left: "| ", Right: " |"},
			LinePrefix:    "* ",
			LineSuffix:    " *",
			LeadingColumn: 3,
		},
	})
}

func TestMecatuiFunctionalConversationCards_Scenario1_PreparedRowsCarryStructuralProvenance(t *testing.T) {
	prepared := PrepareTool(scenarioToolSnapshot(), scenarioLayout())
	if len(prepared.Lines) != len(prepared.Rows) {
		t.Fatalf("line/row count = %d/%d, want lockstep", len(prepared.Lines), len(prepared.Rows))
	}
	if len(prepared.Lines) < 4 {
		t.Fatalf("prepared lines = %q, want framed header and semantic rows", prepared.Lines)
	}
	if got := prepared.Rows[0]; got.Region != RegionChrome || got.Text || got.FallbackRow != 0 {
		t.Errorf("top frame row = %#v, want chrome structural fallback", got)
	}

	var argument, result *Row
	for i := range prepared.Rows {
		row := &prepared.Rows[i]
		switch row.Region {
		case RegionArguments:
			if argument == nil {
				argument = row
			}
		case RegionResult:
			if result == nil {
				result = row
			}
		}
	}
	if argument == nil || !argument.Text || argument.SourceOffset != 0 || argument.LeadingColumn != 3 || argument.GraphemeSpan == 0 {
		t.Errorf("argument provenance = %#v, want text-bearing structural row", argument)
	}
	if result == nil || !result.Text || result.SourceOffset != 0 || result.GraphemeSpan == 0 {
		t.Errorf("result provenance = %#v, want independent source offset and span", result)
	}
}

func TestMecatuiFunctionalConversationCards_Scenario1_PrepareAndKeyOwnImmutableSnapshots(t *testing.T) {
	input := ToolInput{
		Title:     "Read",
		Arguments: []string{"path: before"},
		Results:   []string{"before"},
		Labels:    map[string]string{"phase": "before"},
	}
	layoutInput := LayoutInput{
		Width:        40,
		Expanded:     true,
		VisibleHints: []string{"ctrl+t"},
		Dialect:      1,
		Appearance:   Appearance{Frame: Frame{Left: "[", Right: "]"}, LeadingColumn: 2},
	}
	snapshot := SnapshotTool(input)
	layout := SnapshotLayout(layoutInput)
	before := PrepareTool(snapshot, layout)

	input.Arguments[0] = "path: after"
	input.Results[0] = "after"
	input.Labels["phase"] = "after"
	layoutInput.VisibleHints[0] = "changed"
	layoutInput.Appearance.Frame.Left = "{"

	after := PrepareTool(snapshot, layout)
	if before.Key != after.Key || !equalPrepared(before, after) {
		t.Fatalf("preparation changed after caller mutation:\n before=%#v\n after=%#v", before, after)
	}
}

func TestMecatuiFunctionalConversationCards_Scenario1_CacheKeyTracksRenderInputs(t *testing.T) {
	input := scenarioToolSnapshot()
	layout := scenarioLayout()
	baseline := PrepareTool(input, layout)
	if baseline.Key != PrepareTool(input, layout).Key || !equalPrepared(baseline, PrepareTool(input, layout)) {
		t.Fatal("unchanged snapshots must prepare byte-identically")
	}

	variants := []struct {
		name   string
		input  ToolSnapshot
		layout Layout
	}{
		{name: "input", input: SnapshotTool(ToolInput{Title: "Write", Arguments: []string{"path: README.md"}, Results: []string{"first result", "second result"}, Labels: map[string]string{"source": "workspace"}}), layout: layout},
		{name: "width", input: input, layout: SnapshotLayout(LayoutInput{Width: 12, Expanded: true, VisibleHints: []string{"ctrl+t collapse"}, Dialect: 1, Appearance: scenarioAppearance()})},
		{name: "expansion", input: input, layout: SnapshotLayout(LayoutInput{Width: 48, Expanded: false, VisibleHints: []string{"ctrl+t collapse"}, Dialect: 1, Appearance: scenarioAppearance()})},
		{name: "hint", input: input, layout: SnapshotLayout(LayoutInput{Width: 48, Expanded: true, VisibleHints: []string{"x expand"}, Dialect: 1, Appearance: scenarioAppearance()})},
		{name: "frame", input: input, layout: SnapshotLayout(LayoutInput{Width: 48, Expanded: true, VisibleHints: []string{"ctrl+t collapse"}, Dialect: 1, Appearance: Appearance{Frame: Frame{Top: "=", Bottom: "+", Left: "| ", Right: " |"}, LinePrefix: "* ", LineSuffix: " *", LeadingColumn: 3}})},
		{name: "dialect", input: input, layout: SnapshotLayout(LayoutInput{Width: 48, Expanded: true, VisibleHints: []string{"ctrl+t collapse"}, Dialect: 2, Appearance: scenarioAppearance()})},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			if got := PrepareTool(variant.input, variant.layout).Key; got == baseline.Key {
				t.Fatal("changed render input retained the cache identity")
			}
		})
	}
}

func TestMecatuiFunctionalConversationCards_Scenario1_CardsPackageDependencyBoundary(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list dependency closure: %v", err)
	}
	for _, dependency := range strings.Fields(string(output)) {
		if strings.HasPrefix(dependency, "github.com/stacklok/mecatl/engine/") ||
			strings.HasPrefix(dependency, "github.com/stacklok/mecatl/internal/") ||
			strings.HasPrefix(dependency, "github.com/stacklok/mecatl/contracts/") {
			t.Errorf("cards package must not depend on %q", dependency)
		}
	}
}

func scenarioAppearance() Appearance {
	return Appearance{
		Frame:         Frame{Top: "+", Bottom: "+", Left: "| ", Right: " |"},
		LinePrefix:    "* ",
		LineSuffix:    " *",
		LeadingColumn: 3,
	}
}

func equalPrepared(a, b Prepared) bool {
	return a.Key == b.Key && bytes.Equal([]byte(strings.Join(a.Lines, "\x00")), []byte(strings.Join(b.Lines, "\x00"))) &&
		len(a.Rows) == len(b.Rows) && func() bool {
		for i := range a.Rows {
			if a.Rows[i] != b.Rows[i] {
				return false
			}
		}
		return true
	}()
}

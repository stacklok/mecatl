package ui

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/cards"
)

func TestMecatuiFunctionalConversationCards_Scenario1_PrepareOwnsImmutableSnapshots(t *testing.T) {
	input := cards.UserInput{Text: "immutable prompt", Media: []string{"image/png (inline)"}}
	layoutInput := cards.PlainLayoutInput{Width: 42, ExpandMark: "ctrl+t"}
	inputSnapshot := cards.SnapshotUser(input)
	layoutSnapshot := cards.SnapshotPlainLayout(layoutInput)
	appearance := cards.UserAppearance{
		Label: testTheme().Style("userLabel"),
		Body:  testTheme().Style("userBlock"),
		Muted: testTheme().Style("muted"),
	}
	before := cards.PrepareUser(inputSnapshot, layoutSnapshot, appearance)

	input.Text = "mutated prompt"
	input.Media[0] = "mutated media"
	layoutInput.Width = 8
	layoutInput.ExpandMark = "changed"
	after := cards.PrepareUser(inputSnapshot, layoutSnapshot, appearance)

	if !reflect.DeepEqual(after, before) {
		t.Fatalf("preparation changed after caller-owned inputs mutated:\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestMecatuiFunctionalConversationCards_Scenario1_BlockRenderKeyTracksContentAndContext(t *testing.T) {
	c := &conversation{}
	c.addTool("call-1", "Read", `{"path":"README.md"}`)
	r := newCacheRenderer()
	base := r.blockRenderKey(&c.blocks[0], false)
	if base.context != (renderContextKey{
		width:             r.width,
		paletteGeneration: r.paletteGeneration,
		hintGeneration:    r.hintGeneration,
		dialect:           r.renderDialect,
	}) {
		t.Fatalf("render context key = %#v", base.context)
	}

	assertChanged := func(name string, changed blockRenderKey) {
		t.Helper()
		if changed == base {
			t.Fatalf("%s did not change the composite block render key", name)
		}
	}

	if !c.resolveTool("call-1", "done", false) {
		t.Fatal("resolve tool")
	}
	assertChanged("content revision", r.blockRenderKey(&c.blocks[0], false))

	block := block{rev: base.revision}
	r.width++
	assertChanged("width", r.blockRenderKey(&block, false))
	r.width--
	assertChanged("expanded state", r.blockRenderKey(&block, true))
	r.paletteGeneration++
	assertChanged("palette generation", r.blockRenderKey(&block, false))
	r.paletteGeneration--
	r.hintGeneration++
	assertChanged("visible-hint generation", r.blockRenderKey(&block, false))
	r.hintGeneration--
	r.renderDialect++
	assertChanged("render dialect", r.blockRenderKey(&block, false))
}

func TestMecatuiFunctionalConversationCards_Scenario1_PreparedRowsCarryStructuralProvenance(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(defaultBlockIndent + 34)
	b := &block{
		kind:       blockTool,
		toolID:     "provenance",
		toolName:   "Read",
		toolArgs:   `{"path":"ARGUMENT-PROVENANCE-MARKER-with-a-long-value.txt"}`,
		resolved:   true,
		resultBody: "RESULT-PROVENANCE-MARKER",
	}

	prepared := r.prepareToolCard(b, true)
	if len(prepared.Lines) != len(prepared.Rows) {
		t.Fatalf("StyledTool lines/rows = %d/%d, want lockstep", len(prepared.Lines), len(prepared.Rows))
	}

	_, _, bodyWidth := r.toolCardLayout()
	seen := map[cards.Region]bool{}
	nextOffset := map[cards.Region]int{}
	for i, row := range prepared.Rows {
		if row.FallbackRow != i {
			t.Errorf("row %d fallback = %d, want final-line index", i, row.FallbackRow)
		}
		plain := stripANSIstr(prepared.Lines[i])
		if row.Region == cards.RegionChrome {
			if row.Text || row.SourceOffset != 0 || row.LeadingColumn != 0 || row.GraphemeSpan != 0 {
				t.Errorf("chrome row %d carries text provenance: %#v", i, row)
			}
			continue
		}
		seen[row.Region] = true
		if !row.Text {
			t.Errorf("semantic row %d (%v) is not text-bearing", i, row.Region)
		}
		if row.SourceOffset != nextOffset[row.Region] {
			t.Errorf("semantic row %d (%v) source offset = %d, want %d", i, row.Region, row.SourceOffset, nextOffset[row.Region])
		}
		if row.GraphemeSpan <= 0 {
			t.Errorf("semantic row %d (%v) span = %d, want positive: %q", i, row.Region, row.GraphemeSpan, plain)
		}
		if row.LeadingColumn <= 0 || row.LeadingColumn >= bodyWidth {
			t.Errorf("semantic row %d (%v) leading column = %d, want within card body width %d", i, row.Region, row.LeadingColumn, bodyWidth)
		}
		nextOffset[row.Region] += row.GraphemeSpan
	}
	for _, region := range []cards.Region{cards.RegionArguments, cards.RegionResult} {
		if !seen[region] {
			t.Errorf("StyledTool prepared no %v provenance rows: %q", region, prepared.Lines)
		}
	}
}

func TestMecatuiFunctionalConversationCards_Scenario1_CardsPackageDependencyBoundary(t *testing.T) {
	const (
		cardsPackage        = "github.com/stacklok/mecatl/cmd/mecatui/ui/internal/cards"
		terminaltextPackage = "github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	)
	cmd := exec.Command("go", "list", "-deps", "-f={{.ImportPath}}", cardsPackage)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list %s dependencies: %v\n%s", cardsPackage, err, output)
	}
	for _, dependency := range strings.Fields(string(output)) {
		if strings.HasPrefix(dependency, "github.com/stacklok/mecatl/") && dependency != cardsPackage && dependency != terminaltextPackage {
			t.Errorf("cards package imports project dependency %q; preparation must remain outside ui state, engine, host adapters, and contracts", dependency)
		}
		if strings.HasPrefix(dependency, "google.golang.org/protobuf") || strings.HasPrefix(dependency, "github.com/stacklok/mecatl/contracts/") {
			t.Errorf("cards package imports protobuf dependency %q", dependency)
		}
	}
}

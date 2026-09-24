package ui

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/blocks"
)

func TestMecatuiFunctionalConversationCards_Scenario1_PrepareOwnsImmutableSnapshots(t *testing.T) {
	input := blocks.UserInput{Text: "immutable prompt", Media: []string{"image/png (inline)"}}
	layoutInput := blocks.PlainLayoutInput{Width: 42, ExpandMark: "ctrl+t"}
	inputSnapshot := blocks.SnapshotUser(input)
	layoutSnapshot := blocks.SnapshotPlainLayout(layoutInput)
	theme := blocks.Theme{
		UserLabel: testTheme().Style("userLabel"),
		UserBody:  testTheme().Style("userBlock"),
		Muted:     testTheme().Style("muted"),
	}
	before := blocks.PrepareUser(inputSnapshot, layoutSnapshot, theme)

	input.Text = "mutated prompt"
	input.Media[0] = "mutated media"
	layoutInput.Width = 8
	layoutInput.ExpandMark = "changed"
	after := blocks.PrepareUser(inputSnapshot, layoutSnapshot, theme)

	if !reflect.DeepEqual(after, before) {
		t.Fatalf("preparation changed after caller-owned inputs mutated:\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestMecatuiFunctionalConversationCards_Scenario1_BlockRenderKeyTracksContentAndContext(t *testing.T) {
	c := &conversation{}
	c.addTool("call-1", "Read", `{"path":"README.md"}`)
	r := newCacheRenderer()
	baseSnapshot := c.testBlocks()[0]
	base := blockRenderKey{revision: rendererRevision(baseSnapshot.Revision), context: r.renderContext(false)}
	if base.context != (renderContextKey{
		width: r.width,
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
	changedSnapshot := c.testBlocks()[0]
	assertChanged("content revision", blockRenderKey{revision: rendererRevision(changedSnapshot.Revision), context: r.renderContext(false)})

	r.width++
	assertChanged("width", blockRenderKey{revision: base.revision, context: r.renderContext(false)})
	r.width--
	assertChanged("expanded state", blockRenderKey{revision: base.revision, context: r.renderContext(true)})
}

func TestMecatuiFunctionalConversationCards_Scenario1_PreparedRowsCarryStructuralProvenance(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(defaultBlockIndent + 34)
	presentation := toolCardPresentation{name: "Read", arguments: `{"path":"ARGUMENT-PROVENANCE-MARKER-with-a-long-value.txt"}`, resolved: true, result: "RESULT-PROVENANCE-MARKER"}

	prepared := r.prepareTypedToolCard(presentation, true)
	if len(prepared.Lines) != len(prepared.Rows) {
		t.Fatalf("StyledTool lines/rows = %d/%d, want lockstep", len(prepared.Lines), len(prepared.Rows))
	}

	_, _, bodyWidth := r.toolCardLayout()
	seen := map[blocks.Region]bool{}
	nextOffset := map[blocks.Region]int{}
	for i, row := range prepared.Rows {
		if row.FallbackRow != i {
			t.Errorf("row %d fallback = %d, want final-line index", i, row.FallbackRow)
		}
		plain := stripANSIstr(prepared.Lines[i])
		if row.Region == blocks.RegionChrome {
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
	for _, region := range []blocks.Region{blocks.RegionArguments, blocks.RegionResult} {
		if !seen[region] {
			t.Errorf("StyledTool prepared no %v provenance rows: %q", region, prepared.Lines)
		}
	}
}

func TestMecatuiFunctionalConversationCards_Scenario1_BlocksPackageDependencyBoundary(t *testing.T) {
	const (
		blocksPackage       = "github.com/stacklok/mecatl/cmd/mecatui/ui/internal/blocks"
		terminaltextPackage = "github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	)
	cmd := exec.Command("go", "list", "-deps", "-f={{.ImportPath}}", blocksPackage)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list %s dependencies: %v\n%s", blocksPackage, err, output)
	}
	for _, dependency := range strings.Fields(string(output)) {
		if strings.HasPrefix(dependency, "github.com/stacklok/mecatl/") && dependency != blocksPackage && dependency != terminaltextPackage {
			t.Errorf("blocks package imports project dependency %q; preparation must remain outside ui state, engine, host adapters, and contracts", dependency)
		}
		if strings.HasPrefix(dependency, "google.golang.org/protobuf") || strings.HasPrefix(dependency, "github.com/stacklok/mecatl/contracts/") {
			t.Errorf("blocks package imports protobuf dependency %q", dependency)
		}
	}
}

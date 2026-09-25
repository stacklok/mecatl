package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/blocks"
)

func preparedRegionText(card preparedToolCard, region blocks.Region) []string {
	var rows []string
	for i, provenance := range card.Rows {
		if provenance.Region != region {
			continue
		}
		if provenance.GraphemeSpan == 0 {
			rows = append(rows, "")
			continue
		}
		plain := ansi.Strip(card.Lines[i])
		rows = append(rows, graphemeSlice(plain, provenance.LeadingColumn, provenance.LeadingColumn+provenance.GraphemeSpan))
	}
	return rows
}

func preparedChromeText(card preparedToolCard) string {
	var rows []string
	for i, provenance := range card.Rows {
		if provenance.Region == blocks.RegionChrome {
			rows = append(rows, ansi.Strip(card.Lines[i]))
		}
	}
	return strings.Join(rows, "\n")
}

func preparedChromeContentRows(card preparedToolCard) []string {
	var rows []string
	for i, provenance := range card.Rows {
		if provenance.Region != blocks.RegionChrome {
			continue
		}
		plain := ansi.Strip(card.Lines[i])
		if strings.Trim(plain, " ╭╮╰╯─│") != "" {
			rows = append(rows, plain)
		}
	}
	return rows
}

func TestMecatuiCompactToolCards_Scenario1_CollapsedHeaderSummarizesArguments(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(100)
	b := &block{
		kind:     blockTool,
		toolName: "Search",
		toolArgs: mustJSON(t, map[string]any{"query": "needle", "path": "cmd/mecatui", "limit": 5}),
	}

	collapsed := r.prepareToolCard(b, false)
	header := preparedChromeText(collapsed)
	for _, want := range []string{"… Search", `path: "cmd/mecatui"`, `query: "needle"`, "limit: 5"} {
		if !strings.Contains(header, want) {
			t.Errorf("collapsed header missing %q: %q", want, header)
		}
	}
	searchAt, pathAt := strings.Index(header, "Search"), strings.Index(header, "path:")
	queryAt, limitAt := strings.Index(header, "query:"), strings.Index(header, "limit:")
	if searchAt >= pathAt || pathAt >= queryAt || queryAt >= limitAt {
		t.Errorf("collapsed header order is not status/name/priority arguments: %q", header)
	}
	if rows := preparedRegionText(collapsed, blocks.RegionArguments); len(rows) != 0 {
		t.Errorf("collapsed ordinary card repeated arguments below header: %q", rows)
	}

	r.setWidth(defaultBlockIndent + 28)
	narrow := preparedChromeText(r.prepareToolCard(b, false))
	if !strings.Contains(narrow, "…") || strings.Contains(narrow, "limit: 5") {
		t.Errorf("narrow header must retain a deterministic complete prefix and omission marker: %q", narrow)
	}
}

func TestMecatuiCompactToolCards_Scenario1_SkillHeaderNameAndAsset(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(100)
	args := mustJSON(t, map[string]any{"name": "panel-review", "asset": "checklist"})
	b := &block{kind: blockTool, toolName: "Skill", toolArgs: args}

	header := preparedChromeText(r.prepareToolCard(b, false))
	if !strings.Contains(header, "… Skill · panel-review · asset: checklist") {
		t.Fatalf("collapsed Skill header = %q", header)
	}

	b.toolArgs = mustJSON(t, map[string]any{"name": "panel-review", "asset": ""})
	emptyAssetCard := r.prepareToolCard(b, false)
	if strings.Contains(ansi.Strip(emptyAssetCard.Text()), "asset") {
		t.Errorf("empty Skill asset rendered in collapsed card: %q", ansi.Strip(emptyAssetCard.Text()))
	}

	b.toolArgs = args
	expanded := r.prepareToolCard(b, true)
	fullArgs := strings.Join(preparedRegionText(expanded, blocks.RegionArguments), "\n")
	for _, want := range []string{`"name": "panel-review"`, `"asset": "checklist"`} {
		if !strings.Contains(fullArgs, want) {
			t.Errorf("expanded Skill arguments missing %q: %q", want, fullArgs)
		}
	}
}

func TestMecatuiCompactToolCards_Scenario1_DefaultThreeResultRows(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(70)
	b := &block{
		kind:       blockTool,
		toolName:   "Fetch",
		resolved:   true,
		resultBody: "first\nsecond\n\nfourth",
		resultBlocks: []client.ContentBlock{
			{Kind: client.ContentBlockResourceLink, Name: "artifact-one", URL: "https://example.com/one"},
			{Kind: client.ContentBlockImage, MimeType: "image/png"},
		},
	}

	collapsedRows := preparedRegionText(r.prepareToolCard(b, false), blocks.RegionResult)
	if len(collapsedRows) != 4 {
		t.Fatalf("collapsed result rows = %d, want three rows plus marker: %q", len(collapsedRows), collapsedRows)
	}
	if !strings.Contains(collapsedRows[3], "expand") {
		t.Errorf("collapsed result has no omission/expansion marker: %q", collapsedRows)
	}
	if strings.Contains(strings.Join(collapsedRows[:3], "\n"), "image/png") {
		t.Errorf("typed artifact escaped the shared three-row budget: %q", collapsedRows)
	}

	expandedRows := preparedRegionText(r.prepareToolCard(b, true), blocks.RegionResult)
	expanded := strings.Join(expandedRows, "\n")
	for _, want := range []string{"first\nsecond\n\nfourth", "artifact-one", "image/png"} {
		if !strings.Contains(expanded, want) {
			t.Errorf("expanded result missing %q: %q", want, expanded)
		}
	}
}

func TestMecatuiCompactToolCards_Scenario1_HeaderWidthSafetyAndSpecializedCards(t *testing.T) {
	const longName = "ExtremelyLongToolNameThatCannotFit"
	args := `{"path":"file.txt","query":"needle"}`
	for width := 1; width <= 42; width++ {
		r := newTestRenderer()
		r.indent = 0
		r.setWidth(width)
		card := r.prepareToolCard(&block{kind: blockTool, toolName: longName, toolArgs: args}, false)
		for i, line := range card.Lines {
			if got := lipgloss.Width(line); got > width {
				t.Errorf("width %d row %d occupies %d cells: %q", width, i, got, ansi.Strip(line))
			}
		}
		header := preparedChromeText(card)
		if width >= 1 && !strings.Contains(header, "…") {
			t.Errorf("width %d lost status glyph: %q", width, header)
		}
		if width < lipgloss.Width("… "+longName) && strings.Contains(header, "path:") {
			t.Errorf("width %d rendered arguments before complete tool name fit: %q", width, header)
		}
		if rows := preparedChromeContentRows(card); len(rows) != 1 {
			t.Errorf("width %d collapsed header has %d content rows, want one: %q", width, len(rows), rows)
		}
	}

	tinyResolved := newTestRenderer()
	tinyResolved.indent = 0
	tinyResolved.setWidth(2)
	tinyHeader := preparedChromeText(tinyResolved.prepareToolCard(&block{kind: blockTool, toolName: longName, resolved: true}, false))
	if !strings.Contains(tinyHeader, "✓…") {
		t.Errorf("two-cell header must preserve status and a tool-name omission marker: %q", tinyHeader)
	}

	r := newTestRenderer()
	r.setWidth(80)
	mcp := &block{kind: blockTool, toolName: "mcp__github__issue_write", toolArgs: args, resolved: true, resultBody: "\x1b[2Jfailure", resultError: true}
	expanded := r.renderTool(mcp, true)
	plainExpanded := ansi.Strip(expanded)
	for _, want := range []string{"GitHub · Issue write", "mcp__github__issue_write", `"path": "file.txt"`, "failure"} {
		if !strings.Contains(plainExpanded, want) {
			t.Errorf("expanded card missing %q: %q", want, plainExpanded)
		}
	}
	if strings.Contains(expanded, "\x1b[2J") {
		t.Error("expanded result leaked an injected terminal control")
	}
	if !strings.Contains(expanded, r.th.Style("errorText").Render("[2Jfailure")) {
		t.Errorf("expanded error result lost error styling: %q", expanded)
	}

	edit := &block{kind: blockTool, toolName: "Edit", toolArgs: mustJSON(t, map[string]any{
		"path": "file.txt", "old_string": "a\nb\nc\nd", "new_string": "w\nx\ny\nz", "replace_all": false,
	})}
	editBody := strings.Join(preparedRegionText(r.prepareToolCard(edit, false), blocks.RegionArguments), "\n")
	if !strings.Contains(editBody, "- d") || !strings.Contains(editBody, "+ z") {
		t.Errorf("Edit diff was subjected to the generic three-row/body removal: %q", editBody)
	}

	subagent := &block{kind: blockTool, toolName: "Subagent", subagent: true, subGoal: "preserve specialized body", subDone: true, subStop: "end_turn"}
	subBody := strings.Join(preparedRegionText(r.prepareToolCard(subagent, false), blocks.RegionArguments), "\n")
	if !strings.Contains(subBody, "preserve specialized body") || !strings.Contains(subBody, "subagent") {
		t.Errorf("Subagent specialized body changed: %q", subBody)
	}
}

func TestADR_0301_CompactToolHeaderProvenanceAndSelection(t *testing.T) {
	c := &conversation{}
	c.addTool("call-1", "Read", `{"path":"selection-marker.txt"}`)
	c.resolveTool("call-1", "stable-result-marker\nsecond\nthird\nfourth", false)
	r := newCacheRenderer()
	r.setWidth(70)

	collapsed := r.renderConversationFrame(c, false)
	collapsed.provenance = append([]renderedRow(nil), collapsed.provenance...)
	expanded := r.renderConversationFrame(c, true)
	if len(collapsed.lines) != len(collapsed.provenance) || len(expanded.lines) != len(expanded.provenance) {
		t.Fatalf("frame lines/provenance are not lockstep: collapsed=%d/%d expanded=%d/%d", len(collapsed.lines), len(collapsed.provenance), len(expanded.lines), len(expanded.provenance))
	}
	if collapsed.hasRegion(c.blocks[0].id, conversationRegionArguments) {
		t.Fatal("collapsed header chrome retained a duplicate arguments region")
	}
	argRow := -1
	for i, line := range expanded.lines {
		if expanded.provenance[i].region == conversationRegionArguments && strings.Contains(ansi.Strip(line), "selection-marker.txt") {
			argRow = i
			break
		}
	}
	if argRow < 0 {
		t.Fatal("expanded card has no visible arguments provenance")
	}
	argAnchor, ok := expanded.anchorForRow(argRow)
	if !ok {
		t.Fatal("expanded argument row has no reading anchor")
	}
	fallback := (conversationView{}).restore(collapsed, argAnchor)
	if collapsed.provenance[fallback].blockID != c.blocks[0].id {
		t.Fatalf("hidden argument anchor left its card: %#v", collapsed.provenance[fallback])
	}

	resultRow := -1
	for i, line := range collapsed.lines {
		if strings.Contains(ansi.Strip(line), "stable-result-marker") {
			resultRow = i
			break
		}
	}
	if resultRow < 0 {
		t.Fatal("collapsed result marker missing")
	}
	plain := ansi.Strip(collapsed.lines[resultRow])
	markerByte := strings.Index(plain, "stable-result-marker")
	start := graphemeColForCellX(plain, ansi.StringWidth(plain[:markerByte]))
	sel := selection{active: true, anchorL: resultRow, anchorC: start, headL: resultRow, headC: start + graphemeCount("stable-result-marker")}
	if !sel.snapshotLogical(collapsed, strings.Join(collapsed.lines, "\n")) {
		t.Fatal("could not snapshot stable collapsed result selection")
	}
	if !sel.resolveLogical(expanded) || selectedText(strings.Join(expanded.lines, "\n"), sel) != "stable-result-marker" {
		t.Fatalf("identical result selection did not survive expansion: active text %q", selectedText(strings.Join(expanded.lines, "\n"), sel))
	}

	argPlain := ansi.Strip(expanded.lines[argRow])
	argMarkerByte := strings.Index(argPlain, "selection-marker.txt")
	argStart := graphemeColForCellX(argPlain, ansi.StringWidth(argPlain[:argMarkerByte]))
	argSel := selection{active: true, anchorL: argRow, anchorC: argStart, headL: argRow, headC: argStart + graphemeCount("selection-marker.txt")}
	if !argSel.snapshotLogical(expanded, strings.Join(expanded.lines, "\n")) {
		t.Fatal("could not snapshot expanded argument selection")
	}
	if argSel.resolveLogical(collapsed) {
		t.Fatal("selection survived after its exact visible argument text and endpoint context disappeared into header chrome")
	}
}

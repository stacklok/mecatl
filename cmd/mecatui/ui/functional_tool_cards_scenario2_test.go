package ui

import (
	"strings"
	"testing"
	"unicode"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestMecatuiFunctionalConversationCards_Scenario2_ReadCardWrapsExactlyOnce(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(defaultBlockIndent + 30)
	_, outerWidth, bodyWidth := r.toolCardLayout()
	result := "read-result-" + strings.Repeat("x", bodyWidth+7) + "\nshort-read-row"
	b := &block{
		kind:       blockTool,
		toolID:     "read",
		toolName:   "Read",
		toolArgs:   `{"path":"README.md"}`,
		resolved:   true,
		resultBody: result,
	}

	prepared := r.prepareToolCard(b, true)
	if len(prepared.Lines) != len(prepared.Rows) {
		t.Fatalf("prepared lines/rows = %d/%d, want lockstep", len(prepared.Lines), len(prepared.Rows))
	}
	out := strings.Join(prepared.Lines, "\n")
	if got := stripANSIstr(r.renderTool(b, true)); got != stripANSIstr(out) {
		t.Fatalf("main tool renderer did not use functional preparation\n got: %q\nwant: %q", got, stripANSIstr(out))
	}
	plainRows := strings.Split(stripANSIstr(out), "\n")
	shortAt := -1
	for i, row := range plainRows {
		if maxLineWidth(row) > outerWidth {
			t.Errorf("row %d width exceeds outer card width %d: %q", i, outerWidth, row)
		}
		if strings.Contains(row, "short-read-row") {
			shortAt = i
		}
	}
	if shortAt < 0 {
		t.Fatalf("short source row missing after wrapped Read result:\n%s", stripANSIstr(out))
	}
	for _, row := range plainRows[:shortAt] {
		if strings.TrimSpace(strings.Trim(row, "│╭╮╰╯─ ")) == "" && !strings.ContainsAny(row, "╭╰") {
			t.Errorf("Read result gained a padding-only row from outer-card reflow: %q", row)
		}
	}
}

func TestMecatuiFunctionalConversationCards_Scenario2_ToolVariantsPreserveWidthAndExpansion(t *testing.T) {
	long := strings.Repeat("unbreakable", 24)
	variants := []struct {
		name          string
		block         *block
		collapsedWant string
		expandedWant  string
	}{
		{
			name: "ordinary artifacts",
			block: &block{kind: blockTool, toolID: "artifact", toolName: "WebFetch", toolArgs: `{"url":"https://example.test/` + long + `"}`, resolved: true,
				resultBody: "result-" + long, resultBlocks: []client.ContentBlock{{Kind: client.ContentBlockResourceLink, Name: "artifact-" + long, URL: "https://example.test/" + long}}},
			collapsedWant: "result-", expandedWant: "artifact-",
		},
		{
			name:          "edit diff",
			block:         &block{kind: blockTool, toolID: "edit", toolName: "Edit", toolArgs: `{"path":"` + long + `","old_string":"old-` + long + `","new_string":"new-` + long + `"}`},
			collapsedWant: "Edit", expandedWant: "+ new-",
		},
		{
			name:          "write diff",
			block:         &block{kind: blockTool, toolID: "write", toolName: "Write", toolArgs: `{"path":"` + long + `","content":"written-` + long + `"}`},
			collapsedWant: "Write", expandedWant: "+ written-",
		},
		{
			name:          "subagent projection",
			block:         &block{kind: blockTool, toolID: "sub", toolName: "Subagent", subagent: true, subGoal: "goal-" + long, subModel: "model-" + long, subCurrent: "current-" + long},
			collapsedWant: "goal-", expandedWant: "model-",
		},
		{
			name:          "team projection",
			block:         &block{kind: blockTool, toolID: "team", toolName: "Team", team: true, teamLanes: []teamLane{{name: "member-" + long, current: "current-" + long, lead: true}}},
			collapsedWant: "member-", expandedWant: "current-",
		},
		{
			name:          "parallel projection",
			block:         &block{kind: blockTool, toolID: "parallel", toolName: "Parallel", toolArgs: `{"tasks":["task-` + long + `"]}`},
			collapsedWant: "Parallel", expandedWant: "task-",
		},
	}
	widths := []struct {
		name  string
		width int
	}{
		{name: "zero", width: 0},
		{name: "tiny frameless", width: 1},
		{name: "narrow", width: defaultBlockIndent + 20},
		{name: "normal", width: 100},
		{name: "capped", width: 240},
	}

	for _, width := range widths {
		for _, variant := range variants {
			t.Run(width.name+"/"+variant.name, func(t *testing.T) {
				r := newTestRenderer()
				r.setWidth(width.width)
				for _, expanded := range []bool{false, true} {
					prepared := r.prepareToolCard(variant.block, expanded)
					if len(prepared.Lines) != len(prepared.Rows) {
						t.Fatalf("expanded=%v lines/rows = %d/%d", expanded, len(prepared.Lines), len(prepared.Rows))
					}
					out := strings.Join(prepared.Lines, "\n")
					if width.width > 0 {
						actual := r.renderBlock(0, variant.block, expanded)
						for row, line := range strings.Split(actual, "\n") {
							if got := maxLineWidth(line); got > width.width {
								t.Errorf("expanded=%v row %d width=%d exceeds viewport width %d: %q", expanded, row, got, width.width, stripANSIstr(line))
							}
						}
					}
					want := variant.collapsedWant
					if expanded {
						want = variant.expandedWant
					}
					compact := strings.Map(func(r rune) rune {
						if unicode.IsSpace(r) || strings.ContainsRune("╭╮╰╯│", r) {
							return -1
						}
						return r
					}, stripANSIstr(out))
					compactWant := strings.Map(func(r rune) rune {
						if unicode.IsSpace(r) {
							return -1
						}
						return r
					}, want)
					if !strings.Contains(compact, compactWant) {
						t.Errorf("expanded=%v output lost %q:\n%s", expanded, want, stripANSIstr(out))
					}
				}
			})
		}
	}
}

package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// TestMecatuiCardLayout_Scenario1_ToolCardRegionsFitBodyWidth verifies AC1.3:
// every non-result tool-card region is prepared for the card body before the
// card style is applied. Diff continuation rows retain the meaningful source
// prefix and indentation rather than being reconstructed from a styled card.
func TestMecatuiCardLayout_Scenario1_ToolCardRegionsFitBodyWidth(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(defaultBlockIndent + 38)
	_, cardWidth, bodyWidth := r.toolCardLayout()
	if bodyWidth < 1 {
		t.Fatalf("card body width = %d, want positive", bodyWidth)
	}

	cases := []struct {
		name  string
		block *block
		want  []string
	}{
		{
			name:  "header and expanded arguments",
			block: &block{kind: blockTool, toolID: "header", toolName: "mcp__very_long_server_name__very_long_tool_name", toolArgs: `{"very_long_argument_name":"` + strings.Repeat("argument-value-", 8) + `"}`},
			want:  []string{"very_long_argument_name", "argument-value-"},
		},
		{
			name:  "subagent metadata",
			block: &block{kind: blockTool, toolID: "sub", toolName: "Subagent", subagent: true, subGoal: strings.Repeat("investigate the independently styled child metadata ", 3), subModel: strings.Repeat("model-identifier-", 5), subCurrent: strings.Repeat("tool-name-", 8)},
			want:  []string{"investigate", "model-identifier-", "subagent"},
		},
		{
			name:  "team metadata",
			block: &block{kind: blockTool, toolID: "team", toolName: "Team", team: true, teamLanes: []teamLane{{name: strings.Repeat("member-name-", 4), current: strings.Repeat("current-tool-", 5), lead: true}}},
			want:  []string{"team", "member-name-"},
		},
		{
			name:  "parallel arguments",
			block: &block{kind: blockTool, toolID: "parallel", toolName: "Parallel", toolArgs: `{"tasks":["` + strings.Repeat("parallel-task-", 8) + `"]}`},
			want:  []string{"tasks", "parallel-task-"},
		},
		{
			name:  "edit diff preserves prefix and indentation",
			block: &block{kind: blockTool, toolID: "edit", toolName: "Edit", toolArgs: `{"path":"very-long-path/` + strings.Repeat("nested/", 8) + `file.go","old_string":"    old source ` + strings.Repeat("x", 60) + `","new_string":"    new source ` + strings.Repeat("y", 60) + `"}`},
			want:  []string{"-     old source", "+     new source"},
		},
		{
			name:  "write diff preserves prefix and indentation",
			block: &block{kind: blockTool, toolID: "write", toolName: "Write", toolArgs: `{"path":"very-long-path/` + strings.Repeat("nested/", 8) + `file.go","content":"    source indentation ` + strings.Repeat("z", 60) + `"}`},
			want:  []string{"+     source indentation"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := stripANSIstr(r.renderTool(tc.block, true))
			for i, line := range strings.Split(out, "\n") {
				if got := maxLineWidth(line); got > cardWidth {
					t.Errorf("card row %d width = %d, want ≤ %d: %q", i, got, cardWidth, line)
				}
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("rendered card lost %q:\n%s", want, out)
				}
			}
			if tc.block.toolName == "Edit" || tc.block.toolName == "Write" {
				diff, ok := r.renderToolDiff(tc.block.toolName, tc.block.toolArgs, true)
				if !ok {
					t.Fatal("diff renderer unexpectedly declined valid arguments")
				}
				for i, line := range strings.Split(stripANSIstr(diff), "\n") {
					if got := maxLineWidth(line); got > bodyWidth {
						t.Errorf("prepared diff row %d width = %d, want ≤ %d: %q", i, got, bodyWidth, line)
					}
				}
			}
		})
	}
}

// TestMecatuiCardLayout_Scenario1_ExpandedToolCardWidthInvariant verifies AC1.4:
// expanded main-conversation cards preserve every source region while every rendered
// row fits the card at the tiny, narrow, normal, and capped geometries.
func TestMecatuiCardLayout_Scenario1_ExpandedToolCardWidthInvariant(t *testing.T) {
	const sourceRun = 160
	cases := []struct {
		name  string
		width int
	}{
		{name: "tiny", width: defaultBlockIndent + 6},
		{name: "narrow", width: defaultBlockIndent + 20},
		{name: "normal", width: 100},
		{name: "capped", width: 200},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRenderer()
			r.setWidth(tc.width)
			_, cardWidth, _ := r.toolCardLayout()
			if cardWidth < 1 {
				t.Fatalf("card width = %d, want positive", cardWidth)
			}

			blocks := []*block{
				{
					kind:       blockTool,
					toolID:     "ordinary",
					toolName:   "tool-" + strings.Repeat("H", sourceRun),
					toolArgs:   `{"argument":"` + strings.Repeat("A", sourceRun) + `"}`,
					resolved:   true,
					resultBody: strings.Repeat("D", sourceRun),
					resultBlocks: []client.ContentBlock{
						{Kind: client.ContentBlockResourceLink, Name: strings.Repeat("E", sourceRun), URL: strings.Repeat("F", sourceRun)},
					},
				},
				{
					kind:     blockTool,
					toolID:   "edit",
					toolName: "Edit",
					toolArgs: `{"path":"` + strings.Repeat("P", sourceRun) + `","old_string":"` + strings.Repeat("B", sourceRun) + `","new_string":"` + strings.Repeat("C", sourceRun) + `"}`,
				},
			}
			for _, b := range blocks {
				out := stripANSIstr(r.renderTool(b, true))
				for i, row := range strings.Split(out, "\n") {
					if got := maxLineWidth(row); got > cardWidth {
						t.Errorf("%s row %d width = %d, want ≤ %d: %q", b.toolID, i, got, cardWidth, row)
					}
				}
				wantSource := map[string][]string{
					"ordinary": {"H", "A", "D", "E", "F"},
					"edit":     {"P", "B", "C"},
				}[b.toolID]
				for _, token := range wantSource {
					if got := strings.Count(out, token); got != sourceRun {
						t.Errorf("%s lost expanded %q source content: got %d occurrences, want %d", b.toolID, token, got, sourceRun)
					}
				}
			}
		})
	}
}

// TestMecatuiCardLayout_Scenario1_NoStyledBodyWrap verifies AC1.5: renderTool
// frames the already-width-bounded regions directly, rather than wrapping a
// styled assembled card body (which can turn style alignment padding into rows).
func TestMecatuiCardLayout_Scenario1_NoStyledBodyWrap(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	source, err := os.ReadFile(filepath.Join(filepath.Dir(testFile), "render.go"))
	if err != nil {
		t.Fatalf("read render.go: %v", err)
	}
	start := strings.Index(string(source), "func (r *renderer) renderTool(")
	end := strings.Index(string(source), "\nfunc (r *renderer) toolCardLayout")
	if start < 0 || end < 0 || end <= start {
		t.Fatal("locate renderTool")
	}
	body := string(source)[start:end]
	for _, forbidden := range []string{
		"ansi.Wrap(head",
		"ansi.Hardwrap(head",
		"card.Render(ansi.Wrap",
		"card.Render(ansi.Hardwrap",
		"card.Render(wrapToolCardRegion",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("renderTool applies a width-affecting wrap to its assembled styled body: %s", forbidden)
		}
	}
	for _, required := range []string{
		"head := renderToolHeader(glyph, glyphText, headLabel, r.th.Style(\"toolName\"), bodyWidth)",
		"renderToolCardText(r.th.Style(\"muted\"), sanitizeTerminal(b.toolName), bodyWidth)",
		"r.renderToolArgs(b, expand, bodyWidth)",
		"r.renderToolResult(b, expand, bodyWidth)",
		"return card.Render(head)",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("renderTool no longer prepares a card region before final framing: missing %s", required)
		}
	}
}

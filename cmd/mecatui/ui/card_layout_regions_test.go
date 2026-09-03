package ui

import (
	"strings"
	"testing"
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

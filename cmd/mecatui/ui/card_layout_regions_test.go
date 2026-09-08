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

// TestMecatuiCardLayout_Scenario3_InventoryAndMCPFitWidth verifies AC3.1:
// inventory and MCP list rows are prepared within the body width their container
// offers before styling and final card framing.
func TestMecatuiCardLayout_Scenario3_InventoryAndMCPFitWidth(t *testing.T) {
	const width = 32
	long := strings.Repeat("unbreakable-inventory-value-", 4)
	th, hk := aztec(), defaultHelpKeys()
	assertFits := func(t *testing.T, name, out string) {
		t.Helper()
		for row, line := range strings.Split(stripANSIstr(out), "\n") {
			if !strings.Contains(line, "unbreakable-inventory-value-") {
				continue
			}
			if got := maxLineWidth(line); got > width {
				t.Errorf("%s row %d width = %d, want ≤ %d: %q", name, row, got, width, line)
			}
		}
	}

	t.Run("mcp", func(t *testing.T) {
		state := mcpState{sources: []client.MCPSource{{
			Name: long, Kind: long, Group: long,
			Servers:     []client.MCPServerInfo{{Name: long, Transport: long, URL: long}},
			Diagnostics: []string{long},
		}}, groups: []string{long}, groupsDone: true}
		assertFits(t, "panel", renderMCPPanel(th, state, client.Capabilities{MCP: true}, hk, width))
		assertFits(t, "resources", renderResourceList(th, mcpState{resources: []client.MCPResource{{Name: long, Server: long}}}, client.Capabilities{MCP: true}, hk, width))
		assertFits(t, "prompts", renderPromptList(th, mcpState{prompts: []client.MCPPrompt{{Name: long, Server: long, Arguments: []client.MCPPromptArgument{{Required: true}}}}}, client.Capabilities{MCP: true}, hk, width))
	})

	t.Run("models", func(t *testing.T) {
		catalog := modelCatalog{models: []client.ModelInfo{{ProviderID: long, ID: long, DisplayName: long}}, statuses: []client.ProviderStatus{{ProviderID: long, State: "unreachable", Hint: long}}}
		picker := modelsState{catalog: catalog, filtered: catalog.models}
		assertFits(t, "models", renderModelsPanel(th, catalog, picker, client.Capabilities{ModelSelection: true}, long, hk, 3, width))
	})

	t.Run("sessions and worktrees", func(t *testing.T) {
		st := sessionsState{filtered: []client.SessionListItem{{ID: "s", Title: long, State: long, ModelID: long, Relationship: client.SessionRelationship{MemberName: long}}}, handles: map[string]string{"s": long}}
		assertFits(t, "sessions", renderSessionsPanel(th, st, client.Capabilities{}, hk, width, 20))
		wt := worktreesState{view: worktreesPanel, filtered: []client.Worktree{{Label: long, Branch: long}}}
		assertFits(t, "worktrees", renderWorktreesPanel(th, wt, client.Capabilities{}, hk, width, 20))
	})

	t.Run("agents team and parallel", func(t *testing.T) {
		assertFits(t, "agent definitions", renderAgentsInvPanel(th, agentsInvState{agents: []client.Agent{{Name: long, Description: long, Model: long, PermissionMode: long, Tools: []string{long}}}}, client.Capabilities{Agents: true}, hk, width))
		lane := teamLane{name: long, current: long, role: long}
		teamBlock := &block{teamLanes: []teamLane{lane}}
		assertFits(t, "team", renderAgentsOverlay(th, tabTeams, subagentState{}, parallelState{}, teamState{view: teamRoster}, teamBlock, nil, nil, hk, width, 20))
		fleet := []subagentLane{{childID: long, goal: long, current: long}}
		assertFits(t, "subagents", renderAgentsOverlay(th, tabSubagents, subagentState{}, parallelState{}, teamState{}, nil, fleet, nil, hk, width, 20))
		groups := []parallelGroup{{parentCallID: long, join: long, branches: []parallelBranch{{index: 0, label: long, goal: long, current: long}}}}
		assertFits(t, "parallel", renderAgentsOverlay(th, tabParallel, subagentState{}, parallelState{}, teamState{}, nil, nil, groups, hk, width, 20))
	})
}

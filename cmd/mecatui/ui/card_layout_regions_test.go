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

// TestMecatuiCardLayout_Scenario1_ExpandedToolCardWidthInvariant verifies AC1.4:
// expanded main-conversation cards preserve every source region while every rendered
// row fits the card at the tiny, narrow, normal, and capped geometries.

// TestMecatuiCardLayout_Scenario1_NoStyledBodyWrap verifies AC1.5: renderTool
// frames the already-width-bounded regions directly, rather than wrapping a
// styled assembled card body (which can turn style alignment padding into rows).

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
		picker := modelsState{catalog: catalog, filtered: catalog.models, provenance: long, deps: surfaceDeps{theme: th, keys: defaultKeys(), marks: hk, caps: client.Capabilities{ModelSelection: true}}}
		out, _ := picker.Render(width, 30)
		assertFits(t, "models", out)
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
		teamBlock := &teamOverlaySnapshot{teamLanes: []teamLane{lane}}
		assertFits(t, "team", renderAgentsOverlay(th, tabTeams, subagentState{}, parallelState{}, teamState{view: teamRoster}, teamBlock, nil, nil, hk, width, 20))
		fleet := []subagentLane{{childID: long, goal: long, current: long}}
		assertFits(t, "subagents", renderAgentsOverlay(th, tabSubagents, subagentState{}, parallelState{}, teamState{}, nil, fleet, nil, hk, width, 20))
		groups := []parallelGroup{{parentCallID: long, join: long, branches: []parallelBranch{{index: 0, label: long, goal: long, current: long}}}}
		assertFits(t, "parallel", renderAgentsOverlay(th, tabParallel, subagentState{}, parallelState{}, teamState{}, nil, nil, groups, hk, width, 20))
	})
}

func TestMecatuiCardLayout_Scenario1_ToolCardRegionsFitBodyWidth(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(defaultBlockIndent + 38)
	_, cardWidth, bodyWidth := r.toolCardLayout()
	cases := []struct {
		name string
		card any
		want []string
	}{
		{"header and expanded arguments", toolCardPresentation{name: "mcp__very_long_server_name__very_long_tool_name", arguments: `{"very_long_argument_name":"` + strings.Repeat("argument-value-", 8) + `"}`}, []string{"very_long_argument_name", "argument-value-"}},
		{"subagent metadata", subagentCardPresentation{name: "Subagent", goal: strings.Repeat("investigate the independently styled child metadata ", 3), model: strings.Repeat("model-identifier-", 5), current: strings.Repeat("tool-name-", 8)}, []string{"investigate", "model-identifier-", "subagent"}},
		{"team metadata", teamCardPresentation{name: "Team", lanes: []teamLane{{name: strings.Repeat("member-name-", 4), current: strings.Repeat("current-tool-", 5), lead: true}}}, []string{"team", "member-name-"}},
		{"parallel arguments", toolCardPresentation{name: "Parallel", arguments: `{"tasks":["` + strings.Repeat("parallel-task-", 8) + `"]}`}, []string{"tasks", "parallel-task-"}},
		{"edit diff preserves prefix and indentation", toolCardPresentation{name: "Edit", arguments: `{"path":"very-long-path/` + strings.Repeat("nested/", 8) + `file.go","old_string":"    old source ` + strings.Repeat("x", 60) + `","new_string":"    new source ` + strings.Repeat("y", 60) + `"}`}, []string{"-     old source", "+     new source"}},
		{"write diff preserves prefix and indentation", toolCardPresentation{name: "Write", arguments: `{"path":"very-long-path/` + strings.Repeat("nested/", 8) + `file.go","content":"    source indentation ` + strings.Repeat("z", 60) + `"}`}, []string{"+     source indentation"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out string
			switch card := tc.card.(type) {
			case toolCardPresentation:
				out = r.prepareTypedToolCard(card, true).render()
			case subagentCardPresentation:
				out = r.prepareSubagentCard(card, true).render()
			case teamCardPresentation:
				out = r.prepareTeamCard(card, true).render()
			}
			out = stripANSIstr(out)
			for i, line := range strings.Split(out, "\n") {
				if got := maxLineWidth(line); got > cardWidth {
					t.Errorf("card row %d width = %d, want ≤ %d", i, got, cardWidth)
				}
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("rendered card lost %q:\n%s", want, out)
				}
			}
			if card, ok := tc.card.(toolCardPresentation); ok && (card.name == "Edit" || card.name == "Write") {
				diff, ok := r.renderToolDiff(card.name, card.arguments, true)
				if !ok {
					t.Fatal("diff renderer declined")
				}
				for _, line := range strings.Split(stripANSIstr(diff), "\n") {
					if maxLineWidth(line) > bodyWidth {
						t.Errorf("diff exceeds body width")
					}
				}
			}
		})
	}
}

func TestMecatuiCardLayout_Scenario1_ExpandedToolCardWidthInvariant(t *testing.T) {
	const sourceRun = 160
	for _, width := range []int{defaultBlockIndent + 6, defaultBlockIndent + 20, 100, 200} {
		r := newTestRenderer()
		r.setWidth(width)
		_, cardWidth, _ := r.toolCardLayout()
		cards := []toolCardPresentation{
			{name: "tool-" + strings.Repeat("H", sourceRun), arguments: `{"argument":"` + strings.Repeat("A", sourceRun) + `"}`, resolved: true, result: strings.Repeat("D", sourceRun), artifacts: []client.ContentBlock{{Kind: client.ContentBlockResourceLink, Name: strings.Repeat("E", sourceRun), URL: strings.Repeat("F", sourceRun)}}},
			{name: "Edit", arguments: `{"path":"` + strings.Repeat("P", sourceRun) + `","old_string":"` + strings.Repeat("B", sourceRun) + `","new_string":"` + strings.Repeat("C", sourceRun) + `"}`},
		}
		for _, card := range cards {
			out := stripANSIstr(r.prepareTypedToolCard(card, true).render())
			for _, row := range strings.Split(out, "\n") {
				if maxLineWidth(row) > cardWidth {
					t.Errorf("row exceeds card width")
				}
			}
		}
	}
}

func TestMecatuiCardLayout_Scenario1_NoStyledBodyWrap(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	source, err := os.ReadFile(filepath.Join(filepath.Dir(testFile), "tool_block.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	for _, required := range []string{"func (r *renderer) prepareTypedToolCard(", "renderOrdinaryToolArgs", "renderTypedToolResult"} {
		if !strings.Contains(body, required) {
			t.Errorf("typed tool renderer missing %s", required)
		}
	}
}

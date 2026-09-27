package ui

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestConcurrentDelegations_RenderTerminalGlyphBeforeSiblingFinishes(t *testing.T) {
	families := []struct {
		name, tool string
		finish     func(Model, string) Model
	}{
		{
			name: "subagent", tool: "Subagent",
			finish: func(m Model, stop string) Model {
				return applyAll(m,
					client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "fast", ChildID: "child", Goal: "fast"},
					client.SubagentMsg{Kind: client.SubagentEnd, ParentCallID: "fast", ChildID: "child", Stop: stop, DurationMs: 10},
				)
			},
		},
		{
			name: "team", tool: "Team",
			finish: func(m Model, stop string) Model {
				return applyAll(m,
					client.TeamMsg{Kind: client.TeamStart, ParentCallID: "fast", TeamID: "team-1", Roster: []client.TeamMemberSpec{{Name: "lead", Lead: true}}},
					client.TeamMsg{Kind: client.TeamEnd, ParentCallID: "fast", TeamID: "team-1", Rounds: 1, Stop: stop},
				)
			},
		},
		{
			name: "parallel", tool: "Parallel",
			finish: func(m Model, stop string) Model {
				return applyAll(m,
					client.ParallelMsg{Kind: client.ParallelStart, ParentCallID: "fast", Join: "all", BranchCount: 1},
					client.ParallelMsg{Kind: client.ParallelEnd, ParentCallID: "fast", Join: "all", BranchCount: 1, Winner: -1, Stop: stop},
				)
			},
		},
	}

	for _, family := range families {
		for _, failed := range []bool{false, true} {
			name := family.name + "/success"
			stop, glyph := "end_turn", "✓ "
			if failed {
				name, stop, glyph = family.name+"/error", "error", "✗ "
			}
			t.Run(name, func(t *testing.T) {
				m := newMCPModel(t, aztec(), nil)
				m = applyAll(m,
					client.ToolCallMsg{ID: "fast", Name: family.tool, Args: `{}`},
					client.ToolCallMsg{ID: "slow", Name: "Read", Args: `{}`},
				)
				m = family.finish(m, stop)
				assertConcurrentCards(t, m, glyph+family.tool, "Read")

				m = applyAll(m, client.ToolResultMsg{CallID: "fast", Content: "done", IsError: failed})
				assertConcurrentCards(t, m, glyph+family.tool, "Read")
			})
		}
	}
}

func TestLateDelegationResultOwnsFinalOutcome(t *testing.T) {
	tests := []struct {
		name, tool string
		finish     func(Model) Model
	}{
		{"subagent", "Subagent", func(m Model) Model {
			return applyAll(m,
				client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "fast", ChildID: "child"},
				client.SubagentMsg{Kind: client.SubagentEnd, ParentCallID: "fast", ChildID: "child", Stop: "error"},
			)
		}},
		{"team", "Team", func(m Model) Model {
			return applyAll(m,
				client.TeamMsg{Kind: client.TeamStart, ParentCallID: "fast", TeamID: "team-1"},
				client.TeamMsg{Kind: client.TeamEnd, ParentCallID: "fast", TeamID: "team-1", Stop: "error"},
			)
		}},
		{"parallel", "Parallel", func(m Model) Model {
			return applyAll(m, client.ParallelMsg{Kind: client.ParallelEnd, ParentCallID: "fast", Join: "all", BranchCount: 1, Winner: -1, Stop: "error"})
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMCPModel(t, aztec(), nil)
			m = applyAll(m,
				client.ToolCallMsg{ID: "fast", Name: tt.tool, Args: `{}`},
				client.ToolCallMsg{ID: "slow", Name: "Read", Args: `{}`},
			)
			m = tt.finish(m)
			assertConcurrentCards(t, m, "✗ "+tt.tool, "Read")

			m = applyAll(m, client.ToolResultMsg{CallID: "fast", Content: "usable result"})
			assertConcurrentCards(t, m, "✓ "+tt.tool, "Read")
		})
	}
}

func TestConcurrentTools_ResultSettlesOnlyMatchingCardAndKeepsSiblingActive(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name, glyph := "success", "✓ Read"
		if failed {
			name, glyph = "error", "✗ Read"
		}
		t.Run(name, func(t *testing.T) {
			m := newMCPModel(t, aztec(), nil)
			m = applyAll(m,
				client.ToolCallMsg{ID: "fast", Name: "Read", Args: `{}`},
				client.ToolCallMsg{ID: "slow", Name: "Grep", Args: `{}`},
				client.ToolResultMsg{CallID: "fast", Content: "done", IsError: failed},
			)
			assertConcurrentCards(t, m, glyph, "Grep")
		})
	}
}

func assertConcurrentCards(t *testing.T, m Model, fastGlyph, slowName string) {
	t.Helper()
	blocks := m.conv.testBlocks()
	r := newTestRenderer()
	fast := stripANSIstr(r.renderSnapshot(0, blocks[0], false))
	slow := stripANSIstr(r.renderSnapshot(1, blocks[1], false))
	if !strings.Contains(fast, fastGlyph) || strings.Contains(fast, "… ") {
		t.Fatalf("completed card did not settle independently: %q", fast)
	}
	if !strings.Contains(slow, "… "+slowName) {
		t.Fatalf("pending sibling did not remain running: %q", slow)
	}
	if m.activeTool != slowName {
		t.Fatalf("active tool = %q, want remaining sibling %q", m.activeTool, slowName)
	}
}

package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// TestMecatuiCardLayout_Scenario2_DelegationRowsFitBodyWidth verifies AC2.1:
// every delegation row is wrapped as raw text within the supplied card body
// before its style and the final frame are applied.
func TestMecatuiCardLayout_Scenario2_DelegationRowsFitBodyWidth(t *testing.T) {
	const width = 36
	long := strings.Repeat("unbreakable-delegation-value-", 5)

	viewAtWidth := func(t *testing.T, m Model) string {
		t.Helper()
		mm, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
		return stripANSIstr(mm.(Model).View().Content)
	}
	assertFits := func(t *testing.T, name, out string) {
		t.Helper()
		if !strings.Contains(out, "unbreakable-") {
			t.Errorf("%s lost the raw delegation content: %q", name, out)
		}
		for row, line := range strings.Split(out, "\n") {
			if got := maxLineWidth(line); got > width {
				t.Errorf("%s row %d width = %d, want ≤ %d: %q", name, row, got, width, line)
			}
		}
	}

	t.Run("trace", func(t *testing.T) {
		m := newMCPModel(t, aztec(), nil)
		m = seedSubagents(m, "p1",
			startSub("p1", "child-1", "inspect"),
			toolSubPreview("p1", "child-1", "tool.call", long, long, 1),
		)
		m.agentsTab = tabSubagents
		m.team.view = teamRoster
		m.subagents = subagentState{view: subagentFocus, child: "child-1"}
		assertFits(t, "trace", viewAtWidth(t, m))
	})

	t.Run("roster", func(t *testing.T) {
		m := newMCPModel(t, aztec(), nil)
		m = seedTeam(m, func(c *conversation) {
			c.setTeamStart("t1", "", []client.TeamMemberSpec{{Name: long, Role: long}})
			c.addTeamMember(member(long, "tool.call", client.TeamMsg{ToolName: long}))
		})
		m.agentsTab = tabTeams
		m.team = teamState{view: teamRoster}
		assertFits(t, "roster", viewAtWidth(t, m))
	})

	t.Run("task", func(t *testing.T) {
		m := newMCPModel(t, aztec(), nil)
		m = seedTeam(m, func(c *conversation) {
			c.setTeamStart("t1", "", []client.TeamMemberSpec{{Name: "lead"}})
			c.setTeamTasks("t1", []client.TeamTask{{ID: long, Description: long, State: taskStatePending, Assignee: long, Deps: []string{long}}})
		})
		m.agentsTab = tabTeams
		m.team = teamState{view: teamTasks}
		assertFits(t, "task", viewAtWidth(t, m))
	})

	t.Run("finding", func(t *testing.T) {
		m := newMCPModel(t, aztec(), nil)
		m = seedTeam(m, func(c *conversation) {
			c.setTeamStart("t1", "", []client.TeamMemberSpec{{Name: "lead"}})
			c.setTeamFindings("t1", []client.TeamFinding{{Member: long, Body: long + "   "}})
		})
		m.agentsTab = tabTeams
		m.team = teamState{view: teamFindings}
		assertFits(t, "finding", viewAtWidth(t, m))
	})

	t.Run("focus", func(t *testing.T) {
		m := newMCPModel(t, aztec(), nil)
		m = seedParallel(m, "p1",
			startPar("p1", "all", 1),
			branchStartPar("p1", 0, long, long),
			branchToolParPreview("p1", 0, "tool.call", long, long, 1),
		)
		m.agentsTab = tabParallel
		m.team.view = teamRoster
		m.parallel = parallelState{view: parallelGroupView, group: "p1"}
		assertFits(t, "focus", viewAtWidth(t, m))
	})
}

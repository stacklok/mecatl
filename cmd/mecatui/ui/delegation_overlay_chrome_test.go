package ui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestDelegationOverlayChromeLongKeyLabelsStayOneRow proves that live/rebound
// key labels cannot steal rows from the delegation overlays' fixed-height
// roster, ledger, and focus windows.
func TestDelegationOverlayChromeLongKeyLabelsStayOneRow(t *testing.T) {
	const width = 48
	const height = 20
	_, _, bodyWidth := agentsCardLayout(aztec(), width)
	long := strings.Repeat("rebound-key-label-", 8)
	hk := defaultHelpKeys()
	hk.navUp, hk.navDown = long+"up", long+"down"
	hk.scroll, hk.jumpTop, hk.jumpEnd = long+"page", long+"first", long+"last"
	hk.jumpTopFull, hk.jumpEndFull = long+"first/all", long+"last/all"
	hk.choose, hk.nextTab, hk.closeOnly = long+"focus", long+"switch", long+"back"
	hk.cancelChild, hk.tasks, hk.findings = long+"cancel", long+"tasks", long+"findings"

	assertRows := func(name, out string) {
		t.Helper()
		footer := stripANSIstr(out[strings.LastIndex(out, "\n")+1:])
		if strings.ContainsRune(footer, '\n') || !strings.Contains(footer, "...") {
			t.Errorf("%s footer must be one truncated row, got %q", name, footer)
		}
		if got := lipgloss.Width(footer); got > bodyWidth {
			t.Errorf("%s footer width = %d, want <= %d: %q", name, got, bodyWidth, footer)
		}
	}

	fleet := make([]subagentLane, 12)
	groups := make([]parallelGroup, 12)
	lanes := make([]teamLane, 12)
	tasks := make([]teamTask, 12)
	findings := make([]teamFinding, 12)
	for i := range fleet {
		fleet[i] = subagentLane{childID: fmt.Sprintf("child-%d", i), goal: fmt.Sprintf("goal-%d", i)}
		groups[i] = parallelGroup{parentCallID: fmt.Sprintf("parallel-%d", i), branchCount: 1}
		lanes[i] = teamLane{name: fmt.Sprintf("member-%d", i)}
		tasks[i] = teamTask{id: fmt.Sprintf("task-%d", i), state: taskStatePending}
		findings[i] = teamFinding{member: "lead", body: fmt.Sprintf("finding-%d", i)}
	}
	team := &teamOverlaySnapshot{teamLanes: lanes, teamTasks: tasks, teamFindings: findings}
	th := aztec()

	// Each window still gets precisely its calculated row budget: the long
	// chrome is truncated in place rather than becoming an unaccounted row.
	rosterOut := renderSubagentRoster(th, subagentState{roster: agentsTestListCursor(6)}, fleet, hk, height, bodyWidth)
	parallelOut := renderParallelRoster(th, parallelState{roster: agentsTestListCursor(6)}, groups, hk, height, bodyWidth)
	teamOut := renderTeamRoster(th, teamState{roster: agentsTestListCursor(6)}, team, hk, height, bodyWidth)
	tasksOut := renderTeamTasks(th, team, hk, height, bodyWidth)
	findingsOut := renderTeamFindings(th, team, hk, height, bodyWidth)
	for _, tc := range []struct {
		name   string
		out    string
		marker string
		rows   int
	}{
		{"subagent roster", rosterOut, "goal-", len(subagentSelectableList(th, subagentState{roster: agentsTestListCursor(6)}, fleet, hk, bodyWidth).boundedView(th, height).Rows)},
		{"parallel roster", parallelOut, "branches", len(parallelSelectableList(th, parallelState{roster: agentsTestListCursor(6)}, groups, hk, bodyWidth).boundedView(th, height).Rows)},
		{"team roster", teamOut, "member-", len(teamSelectableList(th, teamState{roster: agentsTestListCursor(6)}, team, hk, bodyWidth).boundedView(th, height).Rows)},
		{"tasks", tasksOut, "task-", teamTasksRows(height)},
		{"findings", findingsOut, "finding-", teamFindingsRows(height)},
	} {
		assertRows(tc.name, tc.out)
		if got := strings.Count(stripANSIstr(tc.out), tc.marker); got < 1 || got > tc.rows {
			t.Errorf("%s rendered %d identifiable rows, want 1..%d within the physical window", tc.name, got, tc.rows)
		}
	}
	for _, tc := range []struct {
		name string
		out  string
	}{
		{"subagent focus", renderSubagentFocus(th, fleet[:1], fleet[0].childID, hk, bodyWidth, height)},
		{"parallel focus", renderParallelGroupFocus(th, parallelState{group: groups[0].parentCallID}, groups[:1], hk, bodyWidth, height)},
		{"subagent fallback", renderSubagentFocus(th, nil, long, hk, bodyWidth, height)},
		{"parallel fallback", renderParallelGroupFocus(th, parallelState{group: long}, nil, hk, bodyWidth, height)},
		{"member fallback", renderTeamFocus(th, team, long, hk, bodyWidth, height)},
		{"empty subagents", renderSubagentRoster(th, subagentState{}, nil, hk, height, bodyWidth)},
		{"empty parallel", renderParallelRoster(th, parallelState{}, nil, hk, height, bodyWidth)},
		{"empty teams", renderTeamsTab(th, teamState{}, nil, hk, bodyWidth, height)},
		{"empty tasks", renderTeamTasks(th, &teamOverlaySnapshot{}, hk, height, bodyWidth)},
		{"empty findings", renderTeamFindings(th, &teamOverlaySnapshot{}, hk, height, bodyWidth)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertRows(tc.name, tc.out)
			if strings.Contains(tc.name, "fallback") {
				for row, line := range strings.Split(stripANSIstr(tc.out), "\n") {
					if got := lipgloss.Width(line); got > bodyWidth {
						t.Errorf("%s fallback row %d width = %d, want <= %d: %q", tc.name, row, got, bodyWidth, line)
					}
				}
			}
		})
	}
}

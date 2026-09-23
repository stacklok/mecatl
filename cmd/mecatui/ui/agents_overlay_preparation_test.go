package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// repeatedPreparationAgentsOverlay is the differential baseline: it re-prepares
// the current renderers for each call. The comparison measures reuse versus
// re-preparation, not independence from a historical full renderer.
func repeatedPreparationAgentsOverlay(th theme.Theme, tab agentsTab, sub subagentState, par parallelState, team teamState, b *block, fleet []subagentLane, groups []parallelGroup, hk helpKeys, width, height, terminal int) string {
	if terminal > 0 && terminal < 24 {
		return renderCompactAgentsOverlay(th, tab, sub, par, team, hk, width)
	}
	layout := newAgentsOverlayLayout(th, tab, width, height)
	body, ok := layout.renderBody(func(bodyHeight int) string {
		switch tab {
		case tabSubagents:
			return renderSubagentTab(th, sub, fleet, hk, layout.bodyWidth, bodyHeight)
		case tabParallel:
			return renderParallelTab(th, par, groups, hk, layout.bodyWidth, bodyHeight)
		default:
			return renderTeamsTab(th, team, b, hk, layout.bodyWidth, bodyHeight)
		}
	}, func() string {
		return renderEssentialAgentsBody(th, tab, sub, par, team, b, fleet, groups, hk, layout.bodyWidth)
	})
	if !ok {
		return renderViewportAgentsFallback(th, tab, sub, par, team, hk, width)
	}
	return centerAgentsCard(th, layout.tabStrip+"\n"+body, layout.outerWidth, width, height)
}

func overlayPreparationFixtures() (fleet []subagentLane, groups []parallelGroup, team *block) {
	trace := make([]teamTrace, 20)
	tasks := make([]teamTask, 20)
	findings := make([]teamFinding, 20)
	for i := range trace {
		trace[i] = teamTrace{kind: teamTraceMessage, text: fmt.Sprintf("trace-%02d %s", i, strings.Repeat("wrapped ", 8))}
		tasks[i] = teamTask{id: fmt.Sprintf("task-%02d", i), desc: strings.Repeat("task detail ", 5), state: taskStatePending}
		findings[i] = teamFinding{member: "worker", body: fmt.Sprintf("finding-%02d %s", i, strings.Repeat("detail ", 8))}
	}
	fleet = []subagentLane{
		{childID: "selected", goal: strings.Repeat("selected goal ", 5), trace: trace},
		{childID: "done", goal: "done", done: true},
	}
	groups = []parallelGroup{{
		parentCallID: "group", join: "judge", branchCount: 2, winner: 1,
		branches: []parallelBranch{
			{index: 0, childID: "branch-0", label: "first", trace: trace},
			{index: 1, childID: "branch-1", label: "winner", trace: trace, done: true},
		},
	}}
	team = &block{teamLanes: []teamLane{{name: "worker", sessionID: "member-1", trace: trace}}, teamTasks: tasks, teamFindings: findings}
	return fleet, groups, team
}

func TestAgentsOverlayPreparedRenderingMatchesRepeatedPreparation(t *testing.T) {
	fleet, groups, teamBlock := overlayPreparationFixtures()
	type view struct {
		name string
		tab  agentsTab
		sub  subagentState
		par  parallelState
		team teamState
		b    *block
		f    []subagentLane
		g    []parallelGroup
	}
	views := []view{
		{name: "subagent roster", tab: tabSubagents, sub: subagentState{roster: agentsTestListCursor(1)}, f: fleet},
		{name: "subagent focus", tab: tabSubagents, sub: subagentState{view: subagentFocus, child: "selected", detail: agentsTestViewport(3)}, f: fleet},
		{name: "subagent missing", tab: tabSubagents, sub: subagentState{view: subagentFocus, child: "missing"}},
		{name: "subagent empty", tab: tabSubagents},
		{name: "parallel roster", tab: tabParallel, par: parallelState{roster: agentsTestListCursor(1)}, g: groups},
		{name: "parallel focus", tab: tabParallel, par: parallelState{view: parallelGroupView, group: "group", branches: agentsTestListCursor(1)}, g: groups},
		{name: "parallel missing", tab: tabParallel, par: parallelState{view: parallelGroupView, group: "missing"}},
		{name: "parallel empty", tab: tabParallel},
		{name: "team roster", tab: tabTeams, team: teamState{roster: agentsTestListCursor(1)}, b: teamBlock},
		{name: "team focus", tab: tabTeams, team: teamState{view: teamFocus, member: "worker", detail: agentsTestViewport(4)}, b: teamBlock},
		{name: "team missing", tab: tabTeams, team: teamState{view: teamFocus, member: "missing"}, b: teamBlock},
		{name: "team tasks", tab: tabTeams, team: teamState{view: teamTasks, detail: agentsTestViewport(2)}, b: teamBlock},
		{name: "team tasks empty", tab: tabTeams, team: teamState{view: teamTasks}, b: &block{}},
		{name: "team findings", tab: tabTeams, team: teamState{view: teamFindings, detail: agentsTestViewport(2)}, b: teamBlock},
		{name: "team findings empty", tab: tabTeams, team: teamState{view: teamFindings}, b: &block{}},
		{name: "team absent", tab: tabTeams},
	}
	geometries := []struct{ width, height, terminal int }{
		{3, 8, 24},
		{80, 24, 24},
		{120, 40, 24},
		{80, 24, 23},
	}
	for _, v := range views {
		for _, g := range geometries {
			name := fmt.Sprintf("%s/%dx%d-terminal-%d", v.name, g.width, g.height, g.terminal)
			t.Run(name, func(t *testing.T) {
				want := repeatedPreparationAgentsOverlay(aztec(), v.tab, v.sub, v.par, v.team, v.b, v.f, v.g, defaultHelpKeys(), g.width, g.height, g.terminal)
				got := renderAgentsOverlay(aztec(), v.tab, v.sub, v.par, v.team, v.b, v.f, v.g, defaultHelpKeys(), g.width, g.height, g.terminal)
				if got != want {
					t.Fatalf("prepared rendering differs from repeated preparation\nwant:\n%q\ngot:\n%q", want, got)
				}
			})
		}
	}
}

func TestPreparedAgentsDetailRenderersKeepSourceViewportAcrossHeightProbes(t *testing.T) {
	fleet, _, teamBlock := overlayPreparationFixtures()
	th, hk := aztec(), defaultHelpKeys()
	const width = 80
	detail := agentsTestViewport(1 << 20)

	tests := []struct {
		name    string
		prepare func() agentsBodyRenderer
		last    string
	}{
		{
			name: "subagent focus",
			prepare: func() agentsBodyRenderer {
				return prepareSubagentFocusAt(th, fleet, "selected", detail, hk, width)
			},
			last: "trace-19",
		},
		{
			name: "team focus",
			prepare: func() agentsBodyRenderer {
				return prepareTeamFocusAt(th, teamBlock, "worker", detail, hk, width)
			},
			last: "trace-19",
		},
		{
			name: "team tasks",
			prepare: func() agentsBodyRenderer {
				return prepareTeamTasksAt(th, teamBlock, detail, hk, width)
			},
			last: "task-19",
		},
		{
			name: "team findings",
			prepare: func() agentsBodyRenderer {
				return prepareTeamFindingsAt(th, teamBlock, detail, hk, width)
			},
			last: "finding-19",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prepared := tc.prepare()
			_ = prepared(40)
			got := prepared(16)
			want := tc.prepare()(16)
			if got != want {
				t.Fatalf("prepared renderer retained state from an earlier height probe\nwant:\n%q\ngot:\n%q", want, got)
			}
			if !strings.Contains(stripANSIstr(got), tc.last) {
				t.Fatalf("end-anchored renderer did not retain %q after height probes:\n%s", tc.last, stripANSIstr(got))
			}
		})
	}
}

func BenchmarkAgentsOverlayPreparation(b *testing.B) {
	fleet, _, _ := overlayPreparationFixtures()
	th, hk := aztec(), defaultHelpKeys()
	sub := subagentState{view: subagentFocus, child: "selected", detail: agentsTestViewport(3)}
	b.Run("repeated-preparation", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = repeatedPreparationAgentsOverlay(th, tabSubagents, sub, parallelState{}, teamState{}, nil, fleet, nil, hk, 32, 24, 24)
		}
	})
	b.Run("prepared-once", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = renderAgentsOverlay(th, tabSubagents, sub, parallelState{}, teamState{}, nil, fleet, nil, hk, 32, 24, 24)
		}
	})
}

package ui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func TestMecatuiBoundedScrollCursor_Scenario2_AllAgentsSubviewsFitOfferedGeometry(t *testing.T) {
	th, hk := aztec(), defaultHelpKeys()
	fleet := boundedScenarioFleet(12)
	groups := boundedScenarioGroups(10)
	team := boundedScenarioTeam(10)
	cases := []struct {
		name string
		tab  agentsTab
		sub  subagentState
		par  parallelState
		team teamState
	}{
		{"subagent roster", tabSubagents, subagentState{view: subagentRoster, cursor: 8}, parallelState{}, teamState{}},
		{"subagent focus", tabSubagents, subagentState{view: subagentFocus, child: fleet[0].childID}, parallelState{}, teamState{}},
		{"parallel roster", tabParallel, subagentState{}, parallelState{view: parallelRoster, cursor: 8}, teamState{}},
		{"parallel group", tabParallel, subagentState{}, parallelState{view: parallelGroupView, group: groups[0].parentCallID, branchCursor: 2}, teamState{}},
		{"team roster", tabTeams, subagentState{}, parallelState{}, teamState{view: teamRoster, cursor: 8}},
		{"team focus", tabTeams, subagentState{}, parallelState{}, teamState{view: teamFocus, member: "member-00"}},
		{"team tasks", tabTeams, subagentState{}, parallelState{}, teamState{view: teamTasks}},
		{"team findings", tabTeams, subagentState{}, parallelState{}, teamState{view: teamFindings}},
	}
	for _, size := range []struct{ width, height int }{{8, 3}, {24, 12}, {80, 24}, {160, 48}} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%s/%dx%d", tc.name, size.width, size.height), func(t *testing.T) {
				out := renderAgentsOverlay(th, tc.tab, tc.sub, tc.par, tc.team, team, fleet, groups, hk, size.width, size.height, size.height)
				assertRenderedFits(t, out, size.width, size.height)
				if size.height < 24 && strings.Contains(stripANSIstr(out), "select") {
					t.Fatalf("compact agents view exposed navigation beyond Escape:\n%s", stripANSIstr(out))
				}
			})
		}
	}
}

func TestMecatuiBoundedScrollCursor_Scenario2_AgentsModesUseSharedAccounting(t *testing.T) {
	th, hk := aztec(), defaultHelpKeys()
	fleet := boundedScenarioFleet(12)
	groups := boundedScenarioGroups(10)
	team := boundedScenarioTeam(10)
	for _, tc := range []struct {
		name string
		out  string
		want string
	}{
		{"subagent", renderSubagentRoster(th, subagentState{cursor: 9}, fleet, hk, 12, 42), "goal-09"},
		{"parallel", renderParallelRoster(th, parallelState{cursor: 9}, groups, hk, 12, 42), "join-09"},
		{"parallel branch", renderParallelGroupFocus(th, parallelState{view: parallelGroupView, group: groups[0].parentCallID, branchCursor: 2}, groups, hk, 42, 24), "branch-3"},
		{"team", renderTeamRoster(th, teamState{cursor: 9}, team, hk, 14, 42), "member-09"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plain := stripANSIstr(tc.out)
			if !strings.Contains(plain, "▶ ") || !strings.Contains(plain, tc.want) {
				t.Fatalf("selected multiline item is not visible:\n%s", plain)
			}
		})
	}
	for _, view := range []teamView{teamTasks, teamFindings} {
		st := teamState{view: view, scroll: 999}
		out := renderTeamsTab(th, st, team, hk, 42, 14)
		if plain := stripANSIstr(out); !strings.Contains(plain, "of 10") || !strings.Contains(plain, "09") {
			t.Fatalf("bounded team detail lacks final content/overflow for view %d:\n%s", view, plain)
		}
	}
}

func TestMecatuiBoundedScrollCursor_Scenario2_ModelsFitsOfferedGeometry(t *testing.T) {
	for _, size := range []struct{ width, height int }{{1, 1}, {12, 5}, {32, 12}, {100, 30}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			s := boundedScenarioModelsState(t, 20)
			out, _ := s.Render(size.width, size.height)
			assertRenderedFits(t, out, size.width, size.height)
		})
	}
}

func TestMecatuiBoundedScrollCursor_Scenario2_CursorAndStatusStylesStayDistinct(t *testing.T) {
	th := aztec()
	s := boundedScenarioModelsState(t, 2)
	s.catalog.active = client.ModelSelection{ProviderID: "provider", ModelID: "model-00"}
	s.catalog.globalDefault = client.ModelSelection{ProviderID: "provider", ModelID: "model-00"}
	out, _ := s.Render(80, 24)
	plain := stripANSIstr(out)
	if !strings.Contains(plain, "▶ ●★ provider") {
		t.Fatalf("cursor obscured model status markers:\n%s", plain)
	}
	selectedStyle := th.Style("spinner")
	if selectedStyle.GetHorizontalFrameSize() != 0 || fmt.Sprint(selectedStyle.GetForeground()) != fmt.Sprint(th.Color("accent")) {
		t.Fatalf("selected-row style is not unbordered accent: frame=%d foreground=%v accent=%v", selectedStyle.GetHorizontalFrameSize(), selectedStyle.GetForeground(), th.Color("accent"))
	}
	agents := stripANSIstr(renderAgentsOverlay(th, tabParallel, subagentState{}, parallelState{cursor: 0}, teamState{view: teamRoster}, nil, nil, boundedScenarioGroups(1), defaultHelpKeys(), 80, 24, 24))
	if !strings.Contains(agents, "▸ Parallel") || !strings.Contains(agents, "▶ ") || !strings.Contains(agents, "winner") {
		t.Fatalf("agents cursor, active tab, and winner markers are not independent:\n%s", agents)
	}

	var list boundedList
	list.setGeometry(20, 2, 2, boundedClip)
	list.setItems([]boundedListItem{{id: "one", text: "custom"}})
	row := list.view().rows[0]
	custom := th.Style("warning").Render(map[bool]string{true: "!! "}[row.cursorMarker] + row.text)
	if !strings.Contains(custom, "!! custom") {
		t.Fatalf("caller-owned selected style/marker was not usable: %q", custom)
	}
}

func TestMecatuiBoundedScrollCursor_Scenario2_UnlistedSharedConsumersUnchanged(t *testing.T) {
	for _, tc := range []struct {
		cursor, count, limit int
		start, end           int
	}{{0, 10, 3, 0, 3}, {5, 10, 3, 4, 7}, {9, 10, 3, 7, 10}} {
		start, end := scrollWindow(tc.cursor, tc.count, tc.limit)
		if start != tc.start || end != tc.end {
			t.Fatalf("legacy shared window changed: got [%d,%d), want [%d,%d)", start, end, tc.start, tc.end)
		}
	}
	m := resize(newMCPModel(t, aztec(), nil), 80, 30)
	m.vp.SetContent(strings.Repeat("conversation\n", 40))
	m.vp.SetYOffset(4)
	mm, _ := m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = mm.(Model)
	if got := m.vp.YOffset(); got <= 4 {
		t.Fatalf("unowned conversation wheel no longer scrolls: offset=%d", got)
	}
}

func TestMecatuiBoundedScrollCursor_Scenario3_AgentsWheelSubviewMatrix(t *testing.T) {
	for _, mode := range []string{"subagent-roster", "subagent-focus", "parallel-roster", "parallel-group", "team-roster", "team-focus", "team-tasks", "team-findings"} {
		t.Run(mode, func(t *testing.T) {
			m := boundedScenarioAgentsModel(t, mode)
			beforeCursor := []int{m.subagents.cursor, m.parallel.cursor, m.parallel.branchCursor, m.team.cursor}
			before := stripANSIstr(m.View().Content)
			mm, _ := m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
			m = mm.(Model)
			after := stripANSIstr(m.View().Content)
			if before == after {
				t.Fatalf("wheel did not move visible %s viewport", mode)
			}
			afterCursor := []int{m.subagents.cursor, m.parallel.cursor, m.parallel.branchCursor, m.team.cursor}
			for i := range beforeCursor {
				if afterCursor[i] != beforeCursor[i] {
					t.Fatalf("wheel moved logical cursor: before=%v after=%v", beforeCursor, afterCursor)
				}
			}
		})
	}
}

func TestMecatuiBoundedScrollCursor_Scenario3_WheelNeverLeaksOrNavigatesFallback(t *testing.T) {
	m := boundedScenarioAgentsModel(t, "subagent-roster")
	m.height = 12
	m.vp.SetContent(strings.Repeat("conversation\n", 40))
	m.vp.SetYOffset(5)
	before := m.subagents
	mm, _ := m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = mm.(Model)
	if m.vp.YOffset() != 5 || m.subagents.cursor != before.cursor {
		t.Fatalf("compact agents wheel leaked or navigated: vp=%d cursor=%d", m.vp.YOffset(), m.subagents.cursor)
	}

	models := boundedScenarioModelsState(t, 2)
	root := resize(newMCPModel(t, aztec(), nil), 80, 30)
	root.modal = models
	root.vp.SetContent(strings.Repeat("conversation\n", 40))
	root.vp.SetYOffset(5)
	mm, _ = root.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	root = mm.(Model)
	if got := root.vp.YOffset(); got != 5 {
		t.Fatalf("models wheel leaked to hidden conversation: offset=%d", got)
	}
}

func TestMecatuiBoundedScrollCursor_Scenario3_ModelClickSelectsEnterActivates(t *testing.T) {
	s := boundedScenarioModelsState(t, 12)
	_, regions := s.Render(60, 18)
	if len(regions) < 2 {
		t.Fatalf("models rendered %d row hit regions, want at least 2", len(regions))
	}
	second := regions[1]
	_, handled, closed := s.HandleMsg(surfaceHitMsg{ID: second.hit, X: 1, Y: 0})
	if !handled || closed || s.cursor != 1 || s.intent != nil {
		t.Fatalf("click should move cursor only: handled=%v closed=%v cursor=%d intent=%T", handled, closed, s.cursor, s.intent)
	}
	_, handled, closed = s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !handled || !closed {
		t.Fatalf("Enter did not retain activation: handled=%v closed=%v", handled, closed)
	}
	if _, ok := s.intent.(modelsSelectIntent); !ok {
		t.Fatalf("Enter intent = %T, want modelsSelectIntent", s.intent)
	}
}

func TestMecatuiBoundedScrollCursor_Scenario3_StaleModelHitsIgnored(t *testing.T) {
	s := boundedScenarioModelsState(t, 8)
	_, first := s.Render(60, 18)
	if len(first) == 0 {
		t.Fatal("models rendered no row hits")
	}
	stale := first[0].hit
	_, _ = s.Render(40, 12)
	s.cursor = 3
	_, handled, closed := s.HandleMsg(surfaceHitMsg{ID: stale})
	if !handled || closed || s.cursor != 3 || s.intent != nil {
		t.Fatalf("stale hit changed models state: handled=%v closed=%v cursor=%d intent=%T", handled, closed, s.cursor, s.intent)
	}
	s.Close()
	_, handled, closed = s.HandleMsg(surfaceHitMsg{ID: stale})
	if handled || closed || s.cursor != 3 || s.intent != nil {
		t.Fatalf("closed-surface hit changed state: handled=%v closed=%v cursor=%d intent=%T", handled, closed, s.cursor, s.intent)
	}
}

func assertRenderedFits(t *testing.T, out string, width, height int) {
	t.Helper()
	if got := lipgloss.Height(out); got > max(0, height) {
		t.Fatalf("rendered height %d exceeds offered %d:\n%s", got, height, stripANSIstr(out))
	}
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > max(0, width) {
			t.Fatalf("line %d width %d exceeds offered %d: %q", i, got, width, stripANSIstr(line))
		}
	}
}

func boundedScenarioTrace(prefix string) []teamTrace {
	trace := make([]teamTrace, 20)
	for i := range trace {
		trace[i] = teamTrace{kind: teamTraceMessage, text: fmt.Sprintf("%s-%02d", prefix, i)}
	}
	return trace
}

func boundedScenarioFleet(n int) []subagentLane {
	fleet := make([]subagentLane, n)
	for i := range fleet {
		fleet[i] = subagentLane{childID: fmt.Sprintf("child-%02d", i), goal: fmt.Sprintf("goal-%02d with asymmetric details", i), trace: boundedScenarioTrace(fmt.Sprintf("trace-%02d", i))}
	}
	return fleet
}

func boundedScenarioGroups(n int) []parallelGroup {
	groups := make([]parallelGroup, n)
	for i := range groups {
		groups[i] = parallelGroup{parentCallID: fmt.Sprintf("join-%02d", i), join: fmt.Sprintf("join-%02d", i), winner: 2, branchCount: 3, branches: []parallelBranch{{index: 0, label: "branch-1", trace: []teamTrace{{kind: teamTraceMessage, text: "one\ntwo\nthree"}}}, {index: 1, label: "branch-2"}, {index: 2, label: "branch-3"}}}
	}
	return groups
}

func boundedScenarioTeam(n int) *block {
	b := &block{kind: blockTool, team: true}
	for i := range n {
		name := fmt.Sprintf("member-%02d", i)
		b.teamLanes = append(b.teamLanes, teamLane{name: name, role: "asymmetric multiline role", trace: boundedScenarioTrace("trace-" + name)})
		b.teamTasks = append(b.teamTasks, teamTask{id: fmt.Sprintf("task-%02d", i), state: taskStatePending})
		b.teamFindings = append(b.teamFindings, teamFinding{member: name, body: fmt.Sprintf("finding-%02d", i)})
	}
	return b
}

func boundedScenarioModelsState(t *testing.T, n int) *modelsState {
	t.Helper()
	models := make([]client.ModelInfo, n)
	for i := range models {
		models[i] = client.ModelInfo{ProviderID: "provider", ID: fmt.Sprintf("model-%02d", i), DisplayName: fmt.Sprintf("Model %02d with a long narrow label", i)}
	}
	th := theme.New("aztec", theme.AztecPalette())
	s := &modelsState{view: modelsPanel, catalog: modelCatalog{models: models}, filtered: models, filter: newFilterInput(), deps: surfaceDeps{theme: th, keys: defaultKeys(), marks: defaultHelpKeys(), hits: &hitRegions{}}}
	return s
}

func newFilterInput() textinput.Model {
	return textinput.New()
}

func boundedScenarioAgentsModel(t *testing.T, mode string) Model {
	t.Helper()
	m := resize(newMCPModel(t, aztec(), nil), 80, 30)
	m.conv.subagentFleet = boundedScenarioFleet(12)
	m.conv.parallelGroups = boundedScenarioGroups(10)
	m.conv.blocks = append(m.conv.blocks, *boundedScenarioTeam(10))
	m.team = teamState{view: teamRoster}
	switch mode {
	case "subagent-roster":
		m.agentsTab = tabSubagents
	case "subagent-focus":
		m.agentsTab, m.subagents = tabSubagents, subagentState{view: subagentFocus, child: "child-00"}
	case "parallel-roster":
		m.agentsTab = tabParallel
	case "parallel-group":
		m.agentsTab, m.parallel = tabParallel, parallelState{view: parallelGroupView, group: "join-00"}
	case "team-roster":
		m.agentsTab = tabTeams
	case "team-focus":
		m.agentsTab, m.team = tabTeams, teamState{view: teamFocus, member: "member-00"}
	case "team-tasks":
		m.agentsTab, m.team = tabTeams, teamState{view: teamTasks}
	case "team-findings":
		m.agentsTab, m.team = tabTeams, teamState{view: teamFindings}
	}
	return m
}

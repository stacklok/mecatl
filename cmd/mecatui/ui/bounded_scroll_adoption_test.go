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
				plain := stripANSIstr(out)
				if size.height < 24 {
					if !strings.Contains(plain, "▶") || strings.Contains(plain, "select") || strings.Contains(plain, "vp short") {
						t.Fatalf("compact agents view lost fallback identity or exposed navigation:\n%s", plain)
					}
				} else if strings.Contains(plain, "vp short") || !strings.Contains(plain, "▸") {
					t.Fatalf("normal agents view used fallback or lost active-tab identity:\n%s", plain)
				} else if size.width >= 160 {
					want := map[string]string{
						"subagent roster": "goal-08", "subagent focus": "trace-00-00",
						"parallel roster": "join-08", "parallel group": "branch-3",
						"team roster": "member-08", "team focus": "trace-member-00-00",
						"team tasks": "task-00", "team findings": "finding-00",
					}[tc.name]
					if !strings.Contains(plain, want) {
						t.Fatalf("normal agents view lost expected %q content:\n%s", want, plain)
					}
				}
			})
		}
	}
}

func TestMecatuiBoundedScrollCursor_Scenario2_AgentsModesUseSharedAccounting(t *testing.T) {
	t.Run("stream refresh persists semantic roster anchors", func(t *testing.T) {
		m := boundedScenarioAgentsModel(t, "subagent-roster")
		m.conv.fleetIndex = make(map[string]int, len(m.conv.subagentFleet))
		for i := range m.conv.subagentFleet {
			m.conv.fleetIndex[m.conv.subagentFleet[i].childID] = i
		}
		th, hk, width, height := m.agentsListGeometry()
		control, _, _ := subagentSelectableList(th, m.subagents, m.conv.subagentFleet, hk, width).configuredControl(th, height)
		control.setCursor(5)
		control.scroll(boundedLineDown)
		before := control.view().rows[0]
		m.subagents.cursor, m.subagents.roster = control.cursor, control

		mm, _ := m.Update(client.SubagentMsg{Kind: client.SubagentTool, ChildID: "child-05", ToolName: strings.Repeat("streamed-tool-", 8), ToolCount: 1})
		m = mm.(Model)
		if m.subagents.roster.cursorID != "child-05" {
			t.Fatalf("stream refresh selected ID = %q, want child-05", m.subagents.roster.cursorID)
		}
		if len(m.subagents.roster.items) == 0 || !strings.Contains(m.subagents.roster.items[5].text, "streamed-tool-") {
			t.Fatalf("stream refresh did not persist rebuilt roster items: %#v", m.subagents.roster.items)
		}
		after := m.subagents.roster.view().rows[0]
		if after.id != before.id || after.itemLine != before.itemLine {
			t.Fatalf("stream refresh top anchor = {%q,%d}, want {%q,%d}", after.id, after.itemLine, before.id, before.itemLine)
		}
	})

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
		st := teamState{view: view, detail: boundedViewport{offset: 999}}
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
			plain := stripANSIstr(out)
			if size.width >= 32 && size.height >= 12 && (!strings.Contains(plain, "Models") || !strings.Contains(plain, "provider")) {
				t.Fatalf("normal Models geometry lost surface or row identity:\n%s", plain)
			}
			if size.width <= 1 && plain == "" {
				t.Fatal("tiny Models geometry lost its bounded surface identity")
			}
		})
	}

	t.Run("Page Down uses wrapped item geometry", func(t *testing.T) {
		s := boundedScenarioModelsState(t, 3)
		s.filtered[0].DisplayName = strings.Repeat("oversized ", 20)
		s.catalog.models = s.filtered
		prefix, suffix := modelsFixedLines(*s, "")
		_, _ = s.Render(24, len(prefix)+len(suffix)+3)
		s.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
		if s.cursor != 0 || s.list.cursorLine == 0 {
			t.Fatalf("Page Down skipped wrapped item segment: cursor=%d line=%d", s.cursor, s.list.cursorLine)
		}
		s.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgUp})
		if s.cursor != 0 || s.list.cursorLine != 0 {
			t.Fatalf("Page Up did not return through wrapped item: cursor=%d line=%d", s.cursor, s.list.cursorLine)
		}
	})
}

func TestMecatuiBoundedScrollCursor_Scenario2_CursorAndStatusStylesStayDistinct(t *testing.T) {
	t.Run("real render applies selected style to Models and Agents", func(t *testing.T) {
		th := aztec()
		models := boundedScenarioModelsState(t, 2)
		models.catalog.active = client.ModelSelection{ProviderID: "provider", ModelID: "model-00"}
		models.catalog.globalDefault = client.ModelSelection{ProviderID: "provider", ModelID: "model-00"}
		modelOut, _ := models.Render(80, 24)
		if want := strings.TrimSuffix(th.Style("spinner").Render("▶ "), "\x1b[m") + "●★ provider"; !strings.Contains(modelOut, want) {
			t.Fatalf("Models selected row does not apply spinner style: want fragment %q in %q", want, modelOut)
		}

		agentsOut := renderAgentsOverlay(th, tabParallel, subagentState{}, parallelState{cursor: 0}, teamState{view: teamRoster}, nil, nil, boundedScenarioGroups(1), defaultHelpKeys(), 80, 24, 24)
		if want := strings.TrimSuffix(th.Style("spinner").Render("▶ "), "\x1b[m"); !strings.Contains(agentsOut, want) {
			t.Fatalf("Agents selected row does not apply spinner style: want fragment %q in %q", want, agentsOut)
		}
	})

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
			m.vp.SetContent(strings.Repeat("hidden conversation\n", 80))
			m.vp.SetYOffset(7)
			beforeCursor := []int{m.subagents.cursor, m.parallel.cursor, m.parallel.branchCursor, m.team.cursor}
			for range 200 {
				mm, _ := m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
				m = mm.(Model)
			}
			if got := agentsScenarioOffset(m, mode); got != 0 {
				t.Fatalf("wheel-up boundary offset = %d, want 0", got)
			}
			mm, _ := m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
			m = mm.(Model)
			if got := agentsScenarioOffset(m, mode); got != 1 {
				t.Fatalf("wheel down moved %s offset to %d, want one physical line", mode, got)
			}
			if m.vp.YOffset() != 7 {
				t.Fatalf("wheel over %s leaked to hidden conversation: %d", mode, m.vp.YOffset())
			}
			afterCursor := []int{m.subagents.cursor, m.parallel.cursor, m.parallel.branchCursor, m.team.cursor}
			for i := range beforeCursor {
				if afterCursor[i] != beforeCursor[i] {
					t.Fatalf("wheel moved logical cursor: before=%v after=%v", beforeCursor, afterCursor)
				}
			}
			mm, _ = m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
			m = mm.(Model)
			if got := agentsScenarioOffset(m, mode); got != 0 {
				t.Fatalf("wheel up did not return %s to top: %d", mode, got)
			}
			for range 200 {
				mm, _ = m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
				m = mm.(Model)
			}
			bottom := agentsScenarioOffset(m, mode)
			if bottom <= 0 {
				t.Fatalf("%s never reached a positive bottom offset", mode)
			}
			mm, _ = m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
			m = mm.(Model)
			if got := agentsScenarioOffset(m, mode); got != bottom {
				t.Fatalf("%s wheel-down bottom clamp = %d, want %d", mode, got, bottom)
			}
			mm, _ = m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
			m = mm.(Model)
			if got := agentsScenarioOffset(m, mode); got != bottom-1 {
				t.Fatalf("%s wheel-up from bottom = %d, want one line to %d", mode, got, bottom-1)
			}

			if strings.Contains(mode, "roster") || mode == "parallel-group" {
				mm, _, _ = m.onAgentsKey(tea.KeyPressMsg{Code: tea.KeyDown})
				m = mm.(Model)
				if !agentsScenarioSelectedVisible(m, mode) {
					t.Fatalf("keyboard movement after wheel did not reveal selected %s item", mode)
				}
				if plain := stripANSIstr(m.View().Content); !strings.Contains(plain, "▶ ") {
					t.Fatalf("keyboard movement after wheel did not reveal selected %s item:\n%s", mode, plain)
				}
			}
		})
	}
}

func agentsScenarioSelectedVisible(m Model, mode string) bool {
	var list *boundedList
	switch mode {
	case "subagent-roster":
		list = &m.subagents.roster
	case "parallel-roster":
		list = &m.parallel.roster
	case "parallel-group":
		list = &m.parallel.branches
	case "team-roster":
		list = &m.team.roster
	default:
		return true
	}
	for _, row := range list.view().rows {
		if row.selected {
			return true
		}
	}
	return false
}

func agentsScenarioOffset(m Model, mode string) int {
	switch mode {
	case "subagent-roster":
		return m.subagents.roster.viewport.offset
	case "subagent-focus":
		return m.subagents.detail.offset
	case "parallel-roster":
		return m.parallel.roster.viewport.offset
	case "parallel-group":
		return m.parallel.branches.viewport.offset
	case "team-roster":
		return m.team.roster.viewport.offset
	default:
		return m.team.detail.offset
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
	if m.vp.YOffset() != 5 || m.subagents.cursor != before.cursor || m.subagents.roster.viewport.offset != before.roster.viewport.offset {
		t.Fatalf("compact agents wheel leaked or navigated: vp=%d cursor=%d offset=%d", m.vp.YOffset(), m.subagents.cursor, m.subagents.roster.viewport.offset)
	}

	m = boundedScenarioAgentsModel(t, "team-tasks")
	m.height = 30
	m.vp.SetHeight(1)
	m.vp.SetContent(strings.Repeat("conversation\n", 40))
	m.vp.SetYOffset(5)
	mm, _ = m.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = mm.(Model)
	if m.vp.YOffset() != 5 || m.team.detail.offset != 0 {
		t.Fatalf("vp-short agents wheel leaked or navigated: vp=%d offset=%d", m.vp.YOffset(), m.team.detail.offset)
	}

	models := boundedScenarioModelsState(t, 20)
	root := resize(newMCPModel(t, aztec(), nil), 80, 30)
	root.modal = models
	root.vp.SetContent(strings.Repeat("conversation\n", 40))
	root.vp.SetYOffset(5)
	_, _ = models.Render(40, 12)
	cursor := models.cursor
	for range 200 {
		mm, _ = root.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		root = mm.(Model)
	}
	bottom := models.list.viewport.offset
	if bottom == 0 || models.cursor != cursor || root.vp.YOffset() != 5 {
		t.Fatalf("Models wheel-down boundary leaked or moved cursor: offset=%d cursor=%d vp=%d", bottom, models.cursor, root.vp.YOffset())
	}
	for range 200 {
		mm, _ = root.onMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
		root = mm.(Model)
	}
	if models.list.viewport.offset != 0 || models.cursor != cursor || root.vp.YOffset() != 5 {
		t.Fatalf("Models wheel-up boundary leaked or moved cursor: offset=%d cursor=%d vp=%d", models.list.viewport.offset, models.cursor, root.vp.YOffset())
	}
}

func TestMecatuiBoundedScrollCursor_Scenario3_ModelClickSelectsEnterActivates(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	m.deps.NoAltScreen = false
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	s := modelsSurface(t, m)
	s.filtered[1].DisplayName = strings.Repeat("wrapped model identity ", 5)
	m = resize(m, 40, 35)
	_ = m.View()
	s = modelsSurface(t, m)
	wheelCursor := s.cursor
	s.HandleWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if s.list.viewport.offset != 1 || s.cursor != wheelCursor {
		t.Fatalf("Models wheel down = offset/cursor %d/%d, want exact one line and cursor %d", s.list.viewport.offset, s.cursor, wheelCursor)
	}
	s.HandleWheel(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if s.list.viewport.offset != 0 || s.cursor != wheelCursor {
		t.Fatalf("Models wheel up = offset/cursor %d/%d, want 0/%d", s.list.viewport.offset, s.cursor, wheelCursor)
	}
	_ = m.View()
	s = modelsSurface(t, m)
	var first renderedHitRegion
	var second []renderedHitRegion
	for _, region := range m.hits.frame {
		switch s.hitItems[region.id] {
		case 0:
			if first.id == 0 {
				first = region
			}
		case 1:
			second = append(second, region)
		}
	}
	if first.id == 0 || len(second) < 2 {
		t.Fatalf("real Models frame hits: first=%d wrapped-second=%d, want marker and continuation regions", first.id, len(second))
	}
	globalX, globalY := m.metrics.localToGlobal(first.rect.x0, first.rect.y0)
	mm, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: globalX, Y: globalY})
	m = mm.(Model)
	if got := modelsSurface(t, m).cursor; got != 0 {
		t.Fatalf("marker-cell click selected %d, want first model", got)
	}
	continuation := second[len(second)-1]
	beforeClickOffset := s.list.viewport.offset
	globalX, globalY = m.metrics.localToGlobal(continuation.rect.x0+1, continuation.rect.y0)
	mm, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: globalX, Y: globalY})
	m = mm.(Model)
	s = modelsSurface(t, m)
	if s.cursor != 1 || s.intent != nil {
		t.Fatalf("click should move cursor only: cursor=%d intent=%T", s.cursor, s.intent)
	}
	if s.list.viewport.offset != beforeClickOffset {
		t.Fatalf("click on an already-visible model moved viewport from %d to %d", beforeClickOffset, s.list.viewport.offset)
	}
	visible := false
	for _, row := range s.list.view().rows {
		visible = visible || row.itemIndex == 1 && row.selected
	}
	if !visible {
		t.Fatal("clicked Model cursor was not revealed in its viewport")
	}
	mm, _, _ = m.onOverlayKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.modal != nil || m.phase != phaseConnecting {
		t.Fatalf("Enter did not retain activation: modal=%T phase=%v", m.modal, m.phase)
	}
	assertModelsOverflowIndicatorClickMisses(t)
}

func assertModelsOverflowIndicatorClickMisses(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	m.deps.NoAltScreen = false
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	m = resize(m, 40, 35)
	_ = m.View()

	s := modelsSurface(t, m)
	s.filtered[0].DisplayName = strings.Repeat("wrapped model identity ", 5)
	s.catalog.models = s.filtered
	_ = m.View()
	s.HandleWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if s.list.viewport.offset == 0 {
		t.Fatal("Models setup did not create an overflow indicator")
	}
	out := m.View().Content
	if !strings.Contains(stripANSIstr(out), "↑ ") {
		t.Fatalf("Models render did not show the expected overflow indicator:\n%s", stripANSIstr(out))
	}

	var first renderedHitRegion
	for _, region := range m.hits.frame {
		if _, ok := s.hitItems[region.id]; ok && (first.id == 0 || region.rect.y0 < first.rect.y0) {
			first = region
		}
	}
	if first.id == 0 || first.rect.y0 == 0 {
		t.Fatalf("cannot locate a rendered Models row below its overflow indicator: %#v", first)
	}
	before := s.cursor
	x, y := m.metrics.localToGlobal(first.rect.x0, first.rect.y0-1)
	mm, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	m = mm.(Model)
	s = modelsSurface(t, m)
	if s.cursor != before || s.intent != nil {
		t.Fatalf("overflow-indicator click must miss without moving or activating Models: cursor=%d intent=%T", s.cursor, s.intent)
	}
}

func TestMecatuiBoundedScrollCursor_Scenario3_StaleModelHitsIgnored(t *testing.T) {
	m := newModelsModel(t, sampleModels(), &fakeStore{}, modelsCaps(), client.ModelSelection{})
	m.deps.NoAltScreen = false
	mm, cmd := m.runModels()
	m = feedCmd(t, mm.(Model), cmd)
	_ = m.View()
	if len(m.hits.frame) == 0 {
		t.Fatal("Models frame rendered no row hits")
	}
	stale := m.hits.frame[0].id
	mm, _ = m.Update(tea.WindowSizeMsg{Width: m.width - 7, Height: m.height - 2})
	m = mm.(Model)
	_ = m.View()
	s := modelsSurface(t, m)
	s.cursor = 2
	s.list.setCursor(2)
	before := s.cursor
	mm, _ = m.Update(surfaceHitMsg{ID: stale})
	m = mm.(Model)
	if got := modelsSurface(t, m).cursor; got != before {
		t.Fatalf("old-frame hit changed cursor: got %d want %d", got, before)
	}

	// Chrome, trailing blank space, and out-of-bounds coordinates all miss through
	// the same frame registry and remain owned by the open Models surface.
	for _, point := range [][2]int{{0, 0}, {m.width - 1, m.metrics.contentOrigin.y}, {-1, -1}} {
		mm, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: point[0], Y: point[1]})
		m = mm.(Model)
		if got := modelsSurface(t, m).cursor; got != before {
			t.Fatalf("miss at %v changed cursor: got %d want %d", point, got, before)
		}
	}
	m.closeModal()
	mm, _ = m.Update(surfaceHitMsg{ID: stale})
	m = mm.(Model)
	if m.modal != nil {
		t.Fatal("closed-surface stale hit reopened Models")
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

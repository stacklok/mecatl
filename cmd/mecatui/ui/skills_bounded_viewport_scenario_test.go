package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

func assertSkillsOfferedFit(t *testing.T, rendered string, width, height int) {
	t.Helper()
	if got := lipgloss.Height(rendered); got > height {
		t.Fatalf("rendered height=%d, want <=%d\n%s", got, height, ansi.Strip(rendered))
	}
	for i, line := range strings.Split(rendered, "\n") {
		if got := ansi.StringWidth(ansi.Strip(line)); got > width {
			t.Fatalf("line %d width=%d, want <=%d: %q", i, got, width, ansi.Strip(line))
		}
	}
}

func skillsScenarioRows(n int) []client.Skill {
	out := make([]client.Skill, n)
	for i := range out {
		out[i] = client.Skill{Name: fmt.Sprintf("skill-%02d", i), Description: strings.Repeat(fmt.Sprintf("description-%02d ", i), 8)}
	}
	return out
}

func skillsScenarioState(th theme.Theme, external, learned int) *skillsState {
	st := &skillsState{view: skillsPanel, skills: skillsScenarioRows(external), deps: surfaceDeps{theme: th, caps: client.Capabilities{Skills: true}, marks: defaultHelpKeys(), keys: defaultKeys()}}
	st.filtered = st.skills
	for i := 0; i < learned; i++ {
		st.learned = append(st.learned, client.LearnedSkill{ID: fmt.Sprintf("learned-%02d", i), Name: fmt.Sprintf("learned-%02d", i), State: "staged", OwnerAgent: "agent"})
	}
	return st
}

func skillsRenderedCardWidth(rendered string) int {
	for _, line := range strings.Split(rendered, "\n") {
		plain := ansi.Strip(line)
		if strings.TrimSpace(plain) != "" {
			return ansi.StringWidth(strings.TrimLeft(plain, " "))
		}
	}
	return 0
}

func TestMecatuiSkillsInventoryBoundedViewport_Scenario1_WidthCapAndFrameAccounting(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	for _, width := range []int{24, 80, 180} {
		layout := newSkillsLayout(th, defaultHelpKeys(), width, 50, true)
		if !layout.normal {
			t.Fatalf("width %d unexpectedly compact", width)
		}
		if want := min(128, width); layout.outerWidth != want {
			t.Fatalf("outer width=%d, want %d", layout.outerWidth, want)
		}
		if want := layout.outerWidth - th.Style("askCard").GetHorizontalFrameSize(); layout.bodyWidth != want {
			t.Fatalf("body width=%d, want measured %d", layout.bodyWidth, want)
		}
		st := skillsScenarioState(th, 4, 2)
		out, _ := st.Render(width, 50)
		assertSkillsOfferedFit(t, out, width, 50)
		if got := skillsRenderedCardWidth(out); got != min(128, width) {
			t.Fatalf("rendered card width=%d, want %d", got, min(128, width))
		}
	}
}

func TestMecatuiSkillsInventoryBoundedViewport_Scenario1_UsesAvailableHeightWithOneGeometryPath(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	for _, height := range []int{11, 24, 60} {
		st := skillsScenarioState(th, 20, 20)
		out, _ := st.Render(100, height)
		assertSkillsOfferedFit(t, out, 100, height)
		if st.viewport == nil || st.externalRows < 1 || st.learnedRows < 1 {
			t.Fatalf("height %d did not allocate both regions: viewport=%v external=%d learned=%d", height, st.viewport, st.externalRows, st.learnedRows)
		}
		if st.externalRows < st.learnedRows || st.externalRows-st.learnedRows > 1 {
			t.Fatalf("uneven split external=%d learned=%d", st.externalRows, st.learnedRows)
		}
		if st.viewport.Height() > st.externalRows {
			t.Fatalf("viewport height=%d exceeds measured external region=%d", st.viewport.Height(), st.externalRows)
		}
	}
	sole := skillsScenarioState(th, 20, 0)
	_, _ = sole.Render(100, 24)
	if sole.externalRows != sole.bodyRows || sole.learnedRows != 0 {
		t.Fatalf("sole external region got %d/%d rows, learned=%d", sole.externalRows, sole.bodyRows, sole.learnedRows)
	}
}

func TestMecatuiSkillsInventoryBoundedViewport_Scenario1_AllStatesFitOfferedGeometry(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	states := []struct {
		name string
		st   *skillsState
		caps client.Capabilities
	}{
		{"loading", &skillsState{view: skillsPanel, loading: true, deps: surfaceDeps{theme: th, marks: defaultHelpKeys()}}, client.Capabilities{Skills: true}},
		{"error", &skillsState{view: skillsPanel, err: errors.New(strings.Repeat("long unsafe error ", 30) + "\x1b[31m"), deps: surfaceDeps{theme: th, marks: defaultHelpKeys()}}, client.Capabilities{Skills: true}},
		{"enabled-empty", &skillsState{view: skillsPanel, deps: surfaceDeps{theme: th, marks: defaultHelpKeys()}}, client.Capabilities{Skills: true}},
		{"disabled", &skillsState{view: skillsPanel, deps: surfaceDeps{theme: th, marks: defaultHelpKeys()}}, client.Capabilities{}},
		{"external", skillsScenarioState(th, 10, 0), client.Capabilities{Skills: true}},
		{"learned", skillsScenarioState(th, 0, 10), client.Capabilities{Skills: true}},
	}
	noMatch := skillsScenarioState(th, 3, 0)
	noMatch.filter.SetValue("missing")
	noMatch.filtered = nil
	states = append(states, struct {
		name string
		st   *skillsState
		caps client.Capabilities
	}{"no-match", noMatch, client.Capabilities{Skills: true}})
	for _, size := range [][2]int{{18, 5}, {42, 12}, {100, 30}} {
		for _, tc := range states {
			t.Run(fmt.Sprintf("%s-%dx%d", tc.name, size[0], size[1]), func(t *testing.T) {
				tc.st.deps.caps = tc.caps
				out, _ := tc.st.Render(size[0], size[1])
				assertSkillsOfferedFit(t, out, size[0], size[1])
			})
		}
	}
}

func TestMecatuiSkillsInventoryBoundedViewport_Scenario1_CompactFallbackAndNonpositiveGeometry(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	for _, size := range [][2]int{{1, 1}, {10, 3}, {40, 6}} {
		st := skillsScenarioState(th, 4, 4)
		_, _ = st.Render(100, 30)
		st.filter.Focus()
		st.cursor = 2
		st.detail = &st.learned[0]
		out, _ := st.Render(size[0], size[1])
		assertSkillsOfferedFit(t, out, size[0], size[1])
		if st.viewport != nil || st.filter.Focused() || st.cursor != 0 || st.detail != nil || strings.Contains(ansi.Strip(out), "Skills inventory") {
			t.Fatalf("compact %v retained interactive state: viewport=%v focus=%v cursor=%d detail=%v out=%q", size, st.viewport, st.filter.Focused(), st.cursor, st.detail, ansi.Strip(out))
		}
	}
	for _, size := range [][2]int{{0, 10}, {-1, 10}, {10, 0}, {10, -1}} {
		st := skillsScenarioState(th, 1, 0)
		if out, _ := st.Render(size[0], size[1]); out != "" {
			t.Fatalf("geometry %v rendered %q", size, ansi.Strip(out))
		}
	}
}

func TestMecatuiSkillsInventoryBoundedViewport_Scenario2_ClampsPhysicalBrowsing(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	st := skillsScenarioState(th, 12, 0)
	_, _ = st.Render(50, 15)
	rows := skillsRowLines(th, st.filtered, st.bodyWidth)
	seen := map[string]bool{}
	for i := 0; i <= len(rows); i++ {
		for _, row := range st.viewport.View(rows).Rows {
			seen[ansi.Strip(row)] = true
		}
		st.viewport.Move(bounded.LineDown, len(rows))
	}
	for i, row := range rows {
		if !seen[ansi.Strip(row)] {
			t.Fatalf("physical row %d unreachable", i)
		}
	}
	st.viewport.Move(bounded.End, len(rows))
	end := st.viewport.Offset()
	st.viewport.Move(bounded.LineDown, len(rows))
	if st.viewport.Offset() != end {
		t.Fatalf("end did not clamp: %d -> %d", end, st.viewport.Offset())
	}
	_, _ = st.Render(80, 30)
	if st.viewport.Offset() > max(0, len(skillsRowLines(th, st.filtered, st.bodyWidth))-st.viewport.Height()) {
		t.Fatal("resize left blank page")
	}
	st.filter.SetValue("skill-00")
	st.syncFilter()
	_, _ = st.Render(80, 30)
	if st.viewport.Offset() != 0 {
		t.Fatalf("query replacement left offset %d", st.viewport.Offset())
	}
}

func TestMecatuiSkillsInventoryBoundedViewport_Scenario2_CurrentOpenOwnsExternalResult(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	st := skillsScenarioState(th, 20, 0)
	st.requestID = 2
	_, _ = st.Render(60, 15)
	st.viewport.Move(bounded.End, len(skillsRowLines(th, st.filtered, st.bodyWidth)))
	_, _, _ = st.HandleMsg(skillsResultMsg{requestID: 2, result: client.SkillsMsg{Skills: []client.Skill{{Name: "current"}}}})
	if len(st.skills) != 1 || st.skills[0].Name != "current" || st.viewport == nil || st.viewport.Offset() != 0 {
		t.Fatalf("current result not installed/reset: %#v offset=%v", st.skills, st.viewport)
	}
	st.loading = true
	_, _, _ = st.HandleMsg(skillsResultMsg{requestID: 1, result: client.SkillsMsg{Err: errors.New("stale")}})
	if st.err != nil || !st.loading || st.skills[0].Name != "current" || st.viewport.Offset() != 0 {
		t.Fatal("older-open response changed current state")
	}
}

func TestMecatuiSkillsInventoryBoundedViewport_Scenario2_NonInventoryStatesClearBrowsing(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	for _, tc := range []struct {
		name        string
		loading     bool
		err         error
		skills      []client.Skill
		query, want string
		caps        client.Capabilities
	}{
		{"loading", true, nil, nil, "", "loading…", client.Capabilities{Skills: true}},
		{"error", false, errors.New("boom"), nil, "", "list skills: boom", client.Capabilities{Skills: true}},
		{"empty", false, nil, nil, "", "No skills are available", client.Capabilities{Skills: true}},
		{"disabled", false, nil, nil, "", "not enabled", client.Capabilities{}},
		{"no-match", false, nil, []client.Skill{{Name: "kept"}}, "missing", "no skills match", client.Capabilities{Skills: true}},
	} {
		st := &skillsState{view: skillsPanel, loading: tc.loading, err: tc.err, skills: tc.skills, deps: surfaceDeps{theme: th, caps: tc.caps, marks: defaultHelpKeys()}, viewport: &bounded.Viewport{}}
		st.filter.SetValue(tc.query)
		st.filtered = filterSkills(st.skills, tc.query)
		out, _ := st.Render(80, 20)
		if st.viewport != nil || !strings.Contains(ansi.Strip(out), tc.want) {
			t.Fatalf("%s retained browsing or copy missing: viewport=%v\n%s", tc.name, st.viewport, ansi.Strip(out))
		}
	}
}

func TestMecatuiSkillsInventoryBoundedViewport_Scenario2_PreservesSafeFilteredRows(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	st := skillsScenarioState(th, 0, 0)
	st.skills = []client.Skill{{Name: "first\x1b[31m", Description: "MATCH " + strings.Repeat("long ", 20)}, {Name: "second", Description: "match"}, {Name: "other"}}
	st.filter.SetValue("match")
	st.syncFilter()
	_, _ = st.Render(30, 12)
	if len(st.filtered) != 2 || st.filtered[0].Name[:5] != "first" || st.filtered[1].Name != "second" {
		t.Fatalf("filter order/case changed: %#v", st.filtered)
	}
	for _, row := range st.viewport.View(skillsRowLines(th, st.filtered, st.bodyWidth)).Rows {
		if strings.Contains(ansi.Strip(row), "\x1b") || !strings.HasSuffix(row, "\x1b[0m") {
			t.Fatalf("unsafe or unterminated row %q", row)
		}
	}
	_, _, _ = st.HandleKey(tea.KeyPressMsg{Code: 'j', Text: "j"})
	_, _, _ = st.HandleKey(tea.KeyPressMsg{Code: 'k', Text: "k"})
	if !strings.HasSuffix(st.filter.Value(), "jk") {
		t.Fatalf("j/k navigated instead of filtering: %q", st.filter.Value())
	}
}

func TestMecatuiSkillsInventoryBoundedViewport_Scenario2_PreservesLearnedLifecycleFencing(t *testing.T) {
	current := client.LearnedSkill{Project: "/p", ID: "id", OwnerAgent: "owner", Version: "v2", Revision: "r2", Generation: 8, PublicationStatus: "published"}
	st := &skillsState{view: skillsDetail, detail: &current, learned: []client.LearnedSkill{current}, project: "/p", requestID: 9, generations: map[string]uint64{"/p": 8}}
	stale := current
	stale.Revision = "stale"
	_, handled, _ := st.HandleMsg(client.LearnedSkillMsg{RequestID: 8, Project: "/p", Generation: 7, SelectedSkillID: "id", SelectedOwnerAgent: "owner", SelectedVersion: "v2", ExpectedRevision: "r2", Skill: &stale, PublicationStatus: "pending_reconciliation"})
	if !handled || st.detail.Revision != "r2" || st.detail.PublicationStatus != "published" || st.generations["/p"] != 8 {
		t.Fatalf("stale lifecycle response replaced current: %#v", st.detail)
	}
}

func TestMecatuiSkillsInventoryBoundedViewport_Scenario3_OpenCloseAndFilterLifecycle(t *testing.T) {
	m := newSkillsModel(t, sampleSkills(), client.Capabilities{Skills: true})
	m, st := openSkillsPanel(t, m)
	_, _ = st.Render(100, 30)
	if m.prompt.Focused() || !st.filter.Focused() {
		t.Fatal("normal open did not transfer focus")
	}
	st.filter.SetValue("x")
	st.syncFilter()
	if _, _, closed := st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc}); closed || st.filter.Value() != "" {
		t.Fatal("first escape did not clear")
	}
	if _, _, closed := st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc}); !closed {
		t.Fatal("second escape did not close")
	}
	m.phase = phaseRunning
	mm, cmd := m.runSkills()
	if mm.(Model).modal != m.modal || cmd != nil {
		t.Fatal("running open was not gated")
	}
}

func TestMecatuiSkillsInventoryBoundedViewport_Scenario3_RemappablePhysicalLineAndPagingNavigation(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	st := skillsScenarioState(th, 30, 20)
	_, _ = st.Render(70, 18)
	st.deps.keys.Up = key.NewBinding(key.WithKeys("u"))
	st.deps.keys.Down = key.NewBinding(key.WithKeys("d"))
	st.deps.keys.ScrollU = key.NewBinding(key.WithKeys("p"))
	st.deps.keys.ScrollD = key.NewBinding(key.WithKeys("n"))
	st.deps.keys.ScrollTop = key.NewBinding(key.WithKeys("t"))
	st.deps.keys.ScrollBottom = key.NewBinding(key.WithKeys("b"))
	press := func(s string) {
		_, handled, _ := st.HandleKey(tea.KeyPressMsg{Code: rune(s[0]), Text: s})
		if !handled {
			t.Fatalf("%q unhandled", s)
		}
	}
	press("d")
	if st.viewport.Offset() != 1 {
		t.Fatalf("external down offset=%d", st.viewport.Offset())
	}
	before := st.viewport.Offset()
	press("n")
	if st.viewport.Offset() != before+st.viewport.Height() {
		t.Fatalf("external page=%d want %d", st.viewport.Offset(), before+st.viewport.Height())
	}
	press("b")
	if st.viewport.Offset() == 0 {
		t.Fatal("external bottom did not move")
	}
	press("t")
	if st.viewport.Offset() != 0 {
		t.Fatal("external top failed")
	}
	_, _, _ = st.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if st.focus != skillsFocusLearned {
		t.Fatal("tab did not focus learned")
	}
	press("d")
	if st.cursor != 1 {
		t.Fatalf("learned down cursor=%d", st.cursor)
	}
	press("n")
	if st.cursor <= 1 {
		t.Fatalf("learned page cursor=%d", st.cursor)
	}
	press("b")
	if st.cursor != len(st.learned)-1 {
		t.Fatal("learned bottom failed")
	}
	press("t")
	if st.cursor != 0 {
		t.Fatal("learned top failed")
	}
	_, _, _ = st.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	press("j")
	press("k")
	if !strings.HasSuffix(st.filter.Value(), "jk") {
		t.Fatalf("typed j/k lost: %q", st.filter.Value())
	}
}

func TestMecatuiSkillsInventoryBoundedViewport_Scenario3_WheelOwnershipAndCompactIsolation(t *testing.T) {
	m := newSkillsModel(t, scrollSkills(40), client.Capabilities{Skills: true})
	m.conv.addUser("long")
	m.conv.appendAssistant(strings.Repeat("line\n", 100))
	m.refreshView()
	before := m.vp.YOffset()
	mm, cmd := m.runSkills()
	m = feedCmd(t, mm.(Model), cmd)
	st := skillsStateOf(t, m)
	st.learned = []client.LearnedSkill{{ID: "l1", Name: "learned-1"}, {ID: "l2", Name: "learned-2"}}
	_ = m.View()
	oldCursor := st.cursor
	mm, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = mm.(Model)
	if st.viewport == nil || st.viewport.Offset() != 1 || st.cursor != oldCursor || m.vp.YOffset() != before {
		t.Fatalf("external wheel ownership failed offset=%v cursor=%d conversation=%d/%d", st.viewport, st.cursor, m.vp.YOffset(), before)
	}
	_, _, _ = st.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	_, _ = st.HandleWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if st.cursor != oldCursor+1 || st.viewport.Offset() != 1 {
		t.Fatal("learned wheel changed wrong region")
	}
	_, _ = st.Render(10, 3)
	_, handled := st.HandleWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if !handled || st.viewport != nil || st.cursor != 0 {
		t.Fatal("compact wheel constructed or moved state")
	}
}

func TestMecatuiSkillsInventoryBoundedViewport_Scenario3_PreservesLearnedControlsWithoutExternalActivation(t *testing.T) {
	lifecycle := &getLifecycleClient{}
	th := theme.New("aztec", theme.AztecPalette())
	st := skillsScenarioState(th, 4, 2)
	st.learnedLifecycle = lifecycle
	st.nextEpoch = func() uint64 { return 10 }
	_, _ = st.Render(80, 20)
	cmd, _, _ := st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("enter under external focus activated/selected something")
	}
	_, _, _ = st.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	cmd, handled, _ := st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !handled || cmd == nil {
		t.Fatal("learned enter lost")
	}
	_ = cmd()
	if lifecycle.calls != 1 {
		t.Fatalf("learned get calls=%d", lifecycle.calls)
	}
	st.view = skillsDetail
	st.detail = &st.learned[0]
	if _, handled, _ := st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc}); !handled || st.view != skillsPanel {
		t.Fatal("detail escape lost")
	}
	_, _ = st.Render(10, 3)
	cmd, _, _ = st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("compact exposed learned action")
	}
}

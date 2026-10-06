package ui

import (
	"context"
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
			return ansi.StringWidth(strings.TrimSpace(plain))
		}
	}
	return 0
}

type recordingSkillsLifecycle struct {
	actions      []string
	diffs, rolls int
}

func (*recordingSkillsLifecycle) ListSkills(context.Context) ([]client.Skill, error) { return nil, nil }
func (*recordingSkillsLifecycle) ListLearnedSkills(context.Context, string) ([]client.LearnedSkill, error) {
	return nil, nil
}
func (*recordingSkillsLifecycle) GetLearnedSkill(context.Context, string, string, string, string) (client.LearnedSkill, error) {
	return client.LearnedSkill{}, nil
}
func (r *recordingSkillsLifecycle) MutateLearnedSkill(_ context.Context, action string, skill client.LearnedSkill) (client.LearnedSkill, error) {
	r.actions = append(r.actions, action)
	return skill, nil
}
func (r *recordingSkillsLifecycle) RollbackLearnedSkill(_ context.Context, skill client.LearnedSkill) (client.LearnedSkill, error) {
	r.rolls++
	return skill, nil
}
func (*recordingSkillsLifecycle) ListSkillChanges(context.Context, string) ([]client.SkillChange, error) {
	return nil, nil
}
func (r *recordingSkillsLifecycle) DiffLearnedSkill(context.Context, client.LearnedSkill) (string, error) {
	r.diffs++
	return "diff", nil
}

func TestMecatuiSkillsInventoryBoundedViewport_Scenario1_WidthCapAndFrameAccounting(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	for _, width := range []int{24, 80, 180} {
		m := modalPlacementTestModel()
		m.width = width
		m.vp.SetHeight(50)
		m.deps.Theme = th
		st := skillsScenarioState(th, 4, 2)
		m.modal = st
		out := m.renderModalSurface()
		assertSkillsOfferedFit(t, out, width, 50)
		if got := skillsRenderedCardWidth(out); got != min(128, width) {
			t.Fatalf("rendered card width=%d, want %d", got, min(128, width))
		}
		frame := th.Style("askCard").GetHorizontalFrameSize()
		if st.bodyWidth != min(128, width)-frame {
			t.Fatalf("body width=%d, want measured %d", st.bodyWidth, min(128, width)-frame)
		}
		body, _ := st.Render(st.bodyWidth, 50-th.Style("askCard").GetVerticalFrameSize())
		if strings.Contains(ansi.Strip(body), "╭") || lipgloss.Width(body) > st.bodyWidth {
			t.Fatalf("surface Render framed or exceeded its unframed offer: %q", ansi.Strip(body))
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
		if st.viewport.Height() != st.externalRows {
			t.Fatalf("viewport height=%d, want assigned external region=%d", st.viewport.Height(), st.externalRows)
		}
		plain := ansi.Strip(out)
		if !strings.Contains(plain, fmt.Sprintf("lines 1–%d of ", st.externalRows)) {
			t.Fatalf("overflow indicator was not rendered outside the %d-row viewport:\n%s", st.externalRows, plain)
		}
		if lipgloss.Height(out) != st.bodyRows+len(skillsTextLines(th.Style("askTitle"), "Skills inventory", 100))+1+len(skillsTextLines(th.Style("muted"), fmt.Sprintf(skillsFooter, defaultHelpKeys().scroll, defaultHelpKeys().closeOnly), 100))+1+1 {
			t.Fatalf("height does not account for viewport, learned label, and overflow chrome: body=%d rendered=%d", st.bodyRows, lipgloss.Height(out))
		}
	}
	sole := skillsScenarioState(th, 20, 0)
	_, _ = sole.Render(100, 24)
	if sole.externalRows != sole.bodyRows || sole.learnedRows != 0 {
		t.Fatalf("sole external region got %d/%d rows, learned=%d", sole.externalRows, sole.bodyRows, sole.learnedRows)
	}
	learnedOnly := skillsScenarioState(th, 0, 20)
	_, _ = learnedOnly.Render(100, 24)
	if learnedOnly.externalRows != 0 || learnedOnly.learnedRows != learnedOnly.bodyRows {
		t.Fatalf("sole learned region got external=%d learned=%d/%d rows", learnedOnly.externalRows, learnedOnly.learnedRows, learnedOnly.bodyRows)
	}
	for height := 1; height < 24; height++ {
		probe := skillsScenarioState(th, 0, 1)
		_, _ = probe.Render(100, height)
		if probe.normal {
			if probe.learnedRows != 1 && height == 1 {
				t.Fatalf("learned-only compact threshold did not require exactly one body row: %+v", probe)
			}
			break
		}
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
		m := modalPlacementTestModel()
		m.deps.Theme, m.width = th, size[0]
		m.vp.SetHeight(size[1])
		m.modal = st
		out := m.renderModalSurface()
		assertSkillsOfferedFit(t, out, size[0], size[1])
		plain := ansi.Strip(out)
		if st.viewport != nil || st.filter.Focused() || st.cursor != 0 || st.detail != nil || strings.Contains(plain, "Skills inventory") || strings.ContainsAny(plain, "┏┓┗┛") {
			t.Fatalf("compact %v retained interaction/frame: viewport=%v focus=%v cursor=%d detail=%v out=%q", size, st.viewport, st.filter.Focused(), st.cursor, st.detail, plain)
		}
	}
	learnedOnly := skillsScenarioState(th, 0, 1)
	const width = 80
	chrome := learnedOnly.externalStateLines(width)
	fixed := len(skillsTextLines(th.Style("askTitle"), "Skills inventory", width)) + 1 + len(skillsTextLines(th.Style("muted"), fmt.Sprintf(skillsFooter, defaultHelpKeys().scroll, defaultHelpKeys().closeOnly), width)) + len(chrome) + 1
	if out, _ := learnedOnly.Render(width, fixed); !learnedOnly.compact || learnedOnly.viewport != nil || strings.Contains(ansi.Strip(out), "Learned skills") {
		t.Fatalf("learned-only geometry without its one body row was not compact: %q", ansi.Strip(out))
	}
	learnedOnly = skillsScenarioState(th, 0, 1)
	if out, _ := learnedOnly.Render(width, fixed+1); !learnedOnly.normal || learnedOnly.externalRows != 0 || learnedOnly.learnedRows != 1 || !strings.Contains(ansi.Strip(out), "learned-00") {
		t.Fatalf("learned-only one-row threshold did not render normally: external=%d learned=%d out=%q", learnedOnly.externalRows, learnedOnly.learnedRows, ansi.Strip(out))
	}
	frame := th.Style("askCard").GetHorizontalFrameSize()
	if frame < 1 {
		t.Fatal("test theme has no askCard frame")
	}
	narrow := skillsScenarioState(th, 4, 4)
	m := modalPlacementTestModel()
	m.deps.Theme, m.width = th, frame-1
	m.vp.SetHeight(30)
	m.modal = narrow
	out := m.renderModalSurface()
	wantClose := ansi.Strip(ansi.Cut(th.Style("muted").Render(defaultHelpKeys().closeOnly+" close"), 0, frame-1))
	if !narrow.compact || strings.Join(strings.Fields(ansi.Strip(out)), " ") != strings.Join(strings.Fields(wantClose), " ") || strings.ContainsAny(ansi.Strip(out), "┏┓┗┛╭╮╰╯") {
		t.Fatalf("too-narrow parent geometry did not use unframed close-only fallback: %q", ansi.Strip(out))
	}

	for _, size := range [][2]int{{0, 10}, {-1, 10}, {10, 0}, {10, -1}} {
		st := skillsScenarioState(th, 1, 0)
		m := modalPlacementTestModel()
		m.deps.Theme, m.width = th, size[0]
		m.vp.SetHeight(size[1])
		m.modal = st
		*m.metrics = renderedSurfaceMetrics{outerBounds: cellRect{x0: 1, x1: 2, y0: 3, y1: 4}}
		if out := m.renderModalSurface(); out != "" || *m.metrics != (renderedSurfaceMetrics{}) {
			t.Fatalf("parent geometry %v rendered %q or retained metrics: %+v", size, ansi.Strip(out), *m.metrics)
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
	lifecycle := &mutateLifecycleClient{}
	m := modalPlacementTestModel()
	m.phase = phaseIdle
	m.deps.Theme, m.deps.Skills, m.deps.Ctx = th, lifecycle, context.Background()
	currentDetail := client.LearnedSkill{Project: "/p", ID: "learned", Version: "v1", Revision: "r1"}
	st := skillsScenarioState(th, 20, 0)
	st.externalRequestID, st.learnedRequestID = 2, 2
	_, _ = st.Render(60, 15)
	st.viewport.Move(bounded.End, len(skillsRowLines(th, st.filtered, st.bodyWidth)))
	st.detail, st.view, st.learnedLifecycle = &currentDetail, skillsDetail, lifecycle
	st.nextEpoch = func() uint64 { m.skillsEpoch++; return m.skillsEpoch + 2 }
	m.modal = st
	cmd, _, _ := st.HandleKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("precondition: learned action did not dispatch")
	}
	_ = cmd()
	mm, _ := m.Update(skillsResultMsg{requestID: 2, result: client.SkillsMsg{Skills: []client.Skill{{Name: "current"}}}})
	m = mm.(Model)
	st = skillsStateOf(t, m)
	if len(st.skills) != 1 || st.skills[0].Name != "current" || st.viewport == nil || st.viewport.Offset() != 0 {
		t.Fatalf("current result after learned traffic not installed/reset: %#v offset=%v", st.skills, st.viewport)
	}

	st.loading = true
	mm, _ = m.Update(skillsResultMsg{requestID: 1, result: client.SkillsMsg{Err: errors.New("older-open")}})
	m = mm.(Model)
	if st.err != nil || !st.loading || st.skills[0].Name != "current" || st.viewport.Offset() != 0 {
		t.Fatal("older-open response changed current state")
	}
	learnedEpoch := st.learnedRequestID
	m.closeModal()
	if m.skillsEpoch < learnedEpoch {
		t.Fatalf("close lost learned request epoch: model=%d learned=%d", m.skillsEpoch, learnedEpoch)
	}
	mm, _ = m.Update(skillsResultMsg{requestID: 2, result: client.SkillsMsg{Err: errors.New("after-close")}})
	m = mm.(Model)
	if m.modal != nil || st.err != nil || st.skills[0].Name != "current" {
		t.Fatal("after-close response changed closed inventory")
	}

	mmOpen, _ := m.runSkills()
	m = mmOpen.(Model)
	newer := skillsStateOf(t, m)
	if newer.externalRequestID <= learnedEpoch {
		t.Fatalf("reopen external epoch=%d did not advance past learned epoch=%d", newer.externalRequestID, learnedEpoch)
	}
	mm, _ = m.Update(skillsResultMsg{requestID: 2, result: client.SkillsMsg{Skills: []client.Skill{{Name: "old-open"}}}})
	m = mm.(Model)
	newer = skillsStateOf(t, m)
	if len(newer.skills) != 0 || !newer.loading {
		t.Fatal("older-open response replaced reopened inventory")
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
	st.skills = []client.Skill{{Name: "first\x1b[31m\a", Description: "MATCH " + strings.Repeat("long ", 20) + "\x1b]0;owned\a"}, {Name: "second", Description: "match"}, {Name: "other"}}
	st.filter.SetValue("match")
	st.syncFilter()
	_, _ = st.Render(30, 12)
	if len(st.filtered) != 2 || st.filtered[0].Name[:5] != "first" || st.filtered[1].Name != "second" {
		t.Fatalf("filter order/case changed: %#v", st.filtered)
	}
	rows := skillsRowLines(th, st.filtered, st.bodyWidth)
	for _, row := range rows {
		if strings.Contains(row, "\x1b]") || strings.Contains(row, "\x9d") || strings.ContainsRune(row, '\a') {
			t.Fatalf("raw rendered row retained OSC/control input %q", row)
		}
	}
	st.viewport.Move(bounded.LineDown, len(rows))
	boundary := st.viewport.View(rows).Rows
	if len(boundary) == 0 || !strings.HasSuffix(boundary[0], "\x1b[0m") || strings.Contains(boundary[0], "\x1b]") || strings.Contains(boundary[0], "\x9d") || strings.ContainsRune(boundary[0], '\a') {
		t.Fatalf("raw offset boundary leaked terminal state/control input: %#v", boundary)
	}
	_, _, _ = st.HandleKey(tea.KeyPressMsg{Code: 'j', Text: "j"})
	_, _, _ = st.HandleKey(tea.KeyPressMsg{Code: 'k', Text: "k"})
	if !strings.HasSuffix(st.filter.Value(), "jk") {
		t.Fatalf("j/k navigated instead of filtering: %q", st.filter.Value())
	}
}

func TestMecatuiSkillsInventoryBoundedViewport_Scenario2_PreservesLearnedLifecycleFencing(t *testing.T) {
	current := client.LearnedSkill{Project: "/p", ID: "id", OwnerAgent: "owner", Version: "v2", Revision: "r2", Generation: 8, PublicationStatus: "published"}
	newValue := current
	newValue.Revision, newValue.Generation = "r3", 9
	valid := client.LearnedSkillMsg{RequestID: 9, Project: "/p", Generation: 9, SelectedSkillID: "id", SelectedOwnerAgent: "owner", SelectedVersion: "v2", ExpectedRevision: "r2", Skill: &newValue, PublicationStatus: "published"}

	cases := []struct {
		name   string
		mutate func(*client.LearnedSkillMsg)
	}{
		{"request", func(m *client.LearnedSkillMsg) { m.RequestID-- }},
		{"generation", func(m *client.LearnedSkillMsg) { m.Generation = 7 }},
		{"partition", func(m *client.LearnedSkillMsg) { m.Project = "/other" }},
		{"selected-identity", func(m *client.LearnedSkillMsg) { m.SelectedSkillID = "other" }},
		{"selected-owner", func(m *client.LearnedSkillMsg) { m.SelectedOwnerAgent = "other" }},
		{"selected-version", func(m *client.LearnedSkillMsg) { m.SelectedVersion = "v1" }},
		{"expected-revision", func(m *client.LearnedSkillMsg) { m.ExpectedRevision = "r1" }},
		{"publication-status", func(m *client.LearnedSkillMsg) { m.PublicationStatus = "pending_reconciliation" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := current
			st := &skillsState{view: skillsDetail, detail: &value, learned: []client.LearnedSkill{value}, project: "/p", learnedRequestID: 9, generations: map[string]uint64{"/p": 8}}
			msg := valid
			replacement := newValue
			msg.Skill = &replacement
			tc.mutate(&msg)
			_, handled, _ := st.HandleMsg(msg)
			if !handled || st.detail.Revision != "r2" || st.detail.PublicationStatus != "published" || st.generations["/p"] != 8 {
				t.Fatalf("%s fence allowed stale lifecycle replacement: detail=%#v generations=%v", tc.name, st.detail, st.generations)
			}
		})
	}

	st := &skillsState{view: skillsDetail, detail: &current, learned: []client.LearnedSkill{current}, project: "/p", learnedRequestID: 9, generations: map[string]uint64{"": 4, "/p": 8}}
	_, _, _ = st.HandleMsg(client.LearnedSkillsMsg{RequestID: 9, Project: "/p", Generations: map[string]uint64{"": 3, "/p": 9}, Skills: []client.LearnedSkill{{ID: "stale-list"}}})
	if st.generations[""] != 4 || st.generations["/p"] != 8 || len(st.learned) != 1 || st.learned[0].ID != "id" {
		t.Fatalf("independent partition generation fence failed: learned=%#v generations=%v", st.learned, st.generations)
	}
	_, _, _ = st.HandleMsg(valid)
	if st.detail.Revision != "r3" || st.detail.PublicationStatus != "published" || st.generations["/p"] != 9 {
		t.Fatalf("valid lifecycle response did not land: detail=%#v generations=%v", st.detail, st.generations)
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
	press("u")
	if st.cursor != 0 {
		t.Fatalf("learned up cursor=%d", st.cursor)
	}
	press("n")
	if st.cursor <= 1 {
		t.Fatalf("learned page cursor=%d", st.cursor)
	}
	press("p")
	if st.cursor != 0 {
		t.Fatalf("learned reverse page cursor=%d", st.cursor)
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
	st.learned = []client.LearnedSkill{{ID: "l1", Name: "learned-1"}, {ID: "l2", Name: "learned-2"}, {ID: "l3", Name: "learned-3"}, {ID: "l4", Name: "learned-4"}}
	_ = m.View()
	oldCursor := st.cursor
	for i := 0; i < 2; i++ {
		mm, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		m = mm.(Model)
	}
	if st.viewport == nil || st.viewport.Offset() != 2 || st.cursor != oldCursor || m.vp.YOffset() != before {
		t.Fatalf("external downward setup changed wrong owner: offset=%v cursor=%d conversation=%d/%d", st.viewport, st.cursor, m.vp.YOffset(), before)
	}
	mm, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m = mm.(Model)
	if st.viewport.Offset() != 1 || st.cursor != oldCursor || m.vp.YOffset() != before {
		t.Fatalf("external wheel-up did not decrement exactly one row: offset=%d cursor=%d conversation=%d/%d", st.viewport.Offset(), st.cursor, m.vp.YOffset(), before)
	}
	st.viewport.Move(bounded.Top, len(st.externalPhysicalRows()))
	mm, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m = mm.(Model)
	if st.viewport.Offset() != 0 || m.vp.YOffset() != before {
		t.Fatal("external top endpoint was not consumed")
	}
	_, _, _ = st.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	for i := 0; i < 2; i++ {
		mm, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		m = mm.(Model)
	}
	if st.cursor != oldCursor+2 || st.viewport.Offset() != 0 || m.vp.YOffset() != before {
		t.Fatal("learned downward setup changed wrong region or hidden conversation")
	}
	mm, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m = mm.(Model)
	if st.cursor != oldCursor+1 || st.viewport.Offset() != 0 || m.vp.YOffset() != before {
		t.Fatal("learned wheel-up did not decrement exactly one row")
	}
	st.cursor = len(st.learned) - 1
	mm, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = mm.(Model)
	if st.cursor != len(st.learned)-1 || m.vp.YOffset() != before {
		t.Fatal("learned bottom endpoint was not consumed")
	}
	_, _ = st.Render(10, 3)
	mm, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = mm.(Model)
	if st.viewport != nil || st.cursor != 0 || m.vp.YOffset() != before {
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
	longDetail := st.learned[0]
	longDetail.Body = strings.Repeat("instruction ", 80) + "BODY-END"
	longDetail.Receipts = []client.SkillChange{{Operation: "activate", FromState: "staged", ToState: "active"}, {Operation: "archive", FromState: "active", ToState: "archived"}}
	longDetail.Supersedes = "v0"
	st.detail = &longDetail
	st.diff = strings.Repeat("diff-line\n", 20) + "DIFF-END"
	out, _ := st.Render(80, 14)
	plain := strings.Join(strings.Fields(ansi.Strip(out)), "")
	for _, want := range []string{"BODY-END", "activatestaged→active", "DIFF-END"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("pre-migration detail rendering lost %q:\n%s", want, ansi.Strip(out))
		}
	}
	beforeDetail := out
	_, handled = st.HandleWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if !handled {
		t.Fatal("detail wheel was not consumed")
	}
	_, handled, _ = st.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if !handled {
		t.Fatal("detail arrow was not consumed")
	}
	afterDetail, _ := st.Render(80, 14)
	if afterDetail != beforeDetail {
		t.Fatal("detail wheel/arrow introduced new browsing semantics")
	}
	if _, handled, _ := st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc}); !handled || st.view != skillsPanel {
		t.Fatal("detail escape lost")
	}

	recorder := &recordingSkillsLifecycle{}
	st.learnedLifecycle = recorder
	epoch := uint64(20)
	st.nextEpoch = func() uint64 { epoch++; return epoch }
	for _, action := range []string{"a", "x", "d", "v", "r"} {
		value := longDetail
		st.view, st.detail, st.compact = skillsDetail, &value, false
		cmd, handled, _ := st.HandleKey(tea.KeyPressMsg{Code: rune(action[0]), Text: action})
		if !handled || cmd == nil {
			t.Fatalf("learned action %q did not dispatch", action)
		}
		_ = cmd()
	}
	if got := strings.Join(recorder.actions, ","); got != "activate,reject,archive" || recorder.diffs != 1 || recorder.rolls != 1 {
		t.Fatalf("learned action dispatches actions=%q diffs=%d rollbacks=%d", got, recorder.diffs, recorder.rolls)
	}
	st.view, st.detail = skillsPanel, nil
	_, _ = st.Render(10, 3)
	cmd, _, _ = st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("compact exposed learned action")
	}
}

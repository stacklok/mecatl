package ui

import (
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

func TestBoundedNavigationReaderHistoricalPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name              string
		actions           []string
		help, soul, dream int
	}{
		{"lines", []string{"Up", "Down"}, 11, 11, 11},
		{"page before line only in help", []string{"Down", "ScrollD"}, 15, 11, 11},
		{"pages", []string{"ScrollU", "ScrollD"}, 15, 15, 15},
		{"ends", []string{"ScrollTop", "ScrollBottom"}, 95, 95, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			overrides := map[string][]string{}
			for _, action := range tc.actions {
				overrides[action] = []string{"z"}
			}
			deps := surfaceDeps{keys: applyKeyOverrides(defaultKeys(), overrides)}
			viewport := func() *bounded.Viewport {
				v := new(bounded.Viewport)
				v.SetGeometry(40, 5, 0, bounded.Clip)
				v.Move(bounded.PageDown, 100)
				v.Move(bounded.PageDown, 100)
				return v
			}
			h := &helpState{deps: deps, viewport: *viewport(), total: 100}
			s := &soulState{deps: deps, viewport: viewport(), total: 100}
			d := &dreamState{deps: deps, view: dreamReview, viewport: viewport(), readerRows: 100}
			for _, reader := range []struct {
				name   string
				handle func(tea.KeyPressMsg) (tea.Cmd, bool, bool)
				v      *bounded.Viewport
				want   int
			}{
				{"help", h.HandleKey, &h.viewport, tc.help},
				{"soul", s.HandleKey, s.viewport, tc.soul},
				{"dream", d.HandleKey, d.viewport, tc.dream},
			} {
				reader.handle(navigationTestKey("z"))
				if got := reader.v.Offset(); got != reader.want {
					t.Errorf("%s offset = %d, want %d", reader.name, got, reader.want)
				}
			}
		})
	}
}

func TestBoundedNavigationReaderCloseAndHelpPrecedeMovement(t *testing.T) {
	deps := surfaceDeps{keys: applyKeyOverrides(defaultKeys(), map[string][]string{"Close": {"q"}, "Down": {"q"}, "Help": {"z"}, "ScrollD": {"z"}})}
	v := new(bounded.Viewport)
	v.SetGeometry(40, 5, 0, bounded.Clip)
	h := &helpState{deps: deps, viewport: *v, total: 100}
	s := &soulState{deps: deps, viewport: v, total: 100}
	d := &dreamState{deps: deps, view: dreamReview, viewport: v, readerRows: 100}
	if _, _, closed := h.HandleKey(navigationTestKey("z")); !closed || h.viewport.Offset() != 0 {
		t.Fatal("Help must close before overlapping page movement")
	}
	if _, _, closed := h.HandleKey(navigationTestKey("q")); !closed || h.viewport.Offset() != 0 {
		t.Fatal("Help Close must precede Down")
	}
	if _, _, closed := s.HandleKey(navigationTestKey("q")); !closed || v.Offset() != 0 {
		t.Fatal("Soul Close must precede Down")
	}
	d.HandleKey(navigationTestKey("q"))
	if d.view != dreamTargets || v.Offset() != 0 {
		t.Fatal("Dream back action must precede Down")
	}
}

func TestBoundedNavigationToolcallsMovementBeforeChoose(t *testing.T) {
	m := addToolcallsForTest(t, newToolcallsInspectorModel(t), 30)
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	s.Render(80, 12)
	s.list.SetCursor(10)
	s.selected = 10
	s.deps.keys = applyKeyOverrides(s.deps.keys, map[string][]string{"Up": {"z"}, "Down": {"z"}, "Choose": {"z"}})
	s.HandleKey(navigationTestKey("z"))
	if s.selected != 11 || s.detail {
		t.Fatal("Toolcalls Down must precede Up and Choose")
	}
	s.deps.keys.Close = key.NewBinding(key.WithKeys("z"))
	if _, _, closed := s.HandleKey(navigationTestKey("z")); !closed || s.selected != 11 {
		t.Fatal("Toolcalls Close must precede navigation and Choose")
	}
}

func TestBoundedNavigationInventoryHistoricalPrecedence(t *testing.T) {
	m := newAgentsInvModel(t, scrollAgents(60), client.Capabilities{Agents: true})
	opened, cmd := m.openAgentsInv()
	m = feedCmd(t, opened.(Model), cmd)
	m.keys = applyKeyOverrides(m.keys, map[string][]string{"Up": {"z"}, "Down": {"z"}, "ScrollD": {"z"}})
	updated, _, _ := m.onAgentsInvKey(navigationTestKey("z"))
	m = updated.(Model)
	if m.agentsInv.viewport.Offset() != 1 {
		t.Fatal("inventory Down must precede Up and PageDown")
	}
	m.keys.Close = key.NewBinding(key.WithKeys("z"))
	updated, _, _ = m.onAgentsInvKey(navigationTestKey("z"))
	if updated.(Model).agentsInv.view != agentsInvNone {
		t.Fatal("inventory Close must precede movement")
	}
}

func TestBoundedReaderWheelClassification(t *testing.T) {
	h := &helpState{total: 100}
	s := &soulState{viewport: new(bounded.Viewport), total: 100}
	d := &dreamState{view: dreamReview, viewport: new(bounded.Viewport), readerRows: 100}
	u := &userModelState{view: userModelDetail, viewport: new(bounded.Viewport), detailLines: make([]string, 100)}
	for _, reader := range []struct {
		name   string
		handle func(tea.MouseWheelMsg) (tea.Cmd, bool)
		v      *bounded.Viewport
	}{
		{"help", h.HandleWheel, &h.viewport},
		{"soul", s.HandleWheel, s.viewport},
		{"dream", d.HandleWheel, d.viewport},
		{"memory", u.HandleWheel, u.viewport},
	} {
		t.Run(reader.name, func(t *testing.T) {
			reader.v.SetGeometry(40, 5, 0, bounded.Clip)
			for _, step := range []struct {
				button tea.MouseButton
				want   int
			}{
				{tea.MouseWheelDown, 1}, {tea.MouseWheelLeft, 1}, {tea.MouseWheelRight, 1}, {tea.MouseNone, 1}, {tea.MouseWheelUp, 0},
			} {
				reader.handle(tea.MouseWheelMsg{Button: step.button})
				if got := reader.v.Offset(); got != step.want {
					t.Fatalf("button %v offset = %d, want %d", step.button, got, step.want)
				}
			}
		})
	}
	d.compact, u.compact = true, true
	d.HandleWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	u.HandleWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if d.viewport.Offset() != 0 || u.viewport.Offset() != 0 {
		t.Fatal("compact readers accepted wheel movement")
	}
	d.compact, d.view = false, dreamConfirmApply
	d.HandleWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if d.viewport.Offset() != 0 {
		t.Fatal("Dream confirmation accepted reader movement")
	}
}

func TestToolcallsWheelRetainsLegacyNonUpMovement(t *testing.T) {
	m := addToolcallsForTest(t, newToolcallsInspectorModel(t), 30)
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	s.selected = 0
	s.Render(80, 12)
	selected := s.list.CursorID()
	for _, detail := range []bool{false, true} {
		s.detail = detail
		s.window = new(bounded.Viewport)
		s.window.SetGeometry(80, 5, 0, bounded.Clip)
		s.lines, s.width = 100, 0
		for _, step := range []struct {
			button tea.MouseButton
			want   int
		}{
			{tea.MouseWheelLeft, 1}, {tea.MouseWheelRight, 2}, {tea.MouseNone, 3}, {tea.MouseWheelDown, 4}, {tea.MouseWheelUp, 3},
		} {
			s.HandleWheel(tea.MouseWheelMsg{Button: step.button})
			got := s.list.Offset()
			if detail {
				got = s.window.Offset()
			}
			if got != step.want || s.list.CursorID() != selected {
				t.Fatalf("detail=%t button=%v offset=%d, want %d and unchanged selection", detail, step.button, got, step.want)
			}
		}
	}
}

func TestBoundedNavigationSessionsLocalActionsPrecedeMovement(t *testing.T) {
	m, _ := hitDispatchModel(t)
	m.phase = phaseIdle
	m.keys = applyKeyOverrides(m.keys, map[string][]string{"Close": {"q"}, "Up": {"q"}, "NextTab": {"z"}, "Down": {"z"}})
	s := sessionsScenarioState(sessionsScenarioRows(6))
	s.deps = (&m).surfaceDeps()
	m.modal = s
	_ = m.View()
	s.list.SetCursor(3)
	s.filter.SetValue("session")
	s.syncFilter()
	selected := s.list.CursorID()
	m = applyAll(m, navigationTestKey("q"))
	if m.modal != s || s.filter.Value() != "" || s.list.CursorID() != selected {
		t.Fatal("Close/filter-clear must precede overlapping Up")
	}
	m = applyAll(m, navigationTestKey("z"))
	if m.modal != s || s.tab != tabScheduledRuns {
		t.Fatal("NextTab must precede overlapping Down")
	}
	m = applyAll(m, navigationTestKey("q"))
	if m.modal != nil || !m.prompt.Focused() {
		t.Fatal("Close must dismiss after filter is clear")
	}
}

func TestBoundedNavigationMemoryCloseBackPrecedesMovement(t *testing.T) {
	entries := savedMemoryEntries(4)
	m := newUserModelModel(t, &fakeUserModel{model: client.UserModel{Entries: entries}}, client.Capabilities{UserModel: true})
	m.keys = applyKeyOverrides(m.keys, map[string][]string{"Close": {"q"}, "Down": {"q"}})
	m, s := openedUserModel(t, m)
	selected := s.list.CursorID()
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("inspect must request detail")
	}
	m = applyAll(m, client.UserModelDetailMsg{Generation: s.generation, RequestKey: selected,
		UserModel: client.UserModel{Entries: entries, Detail: &client.UserModelDetail{Current: client.UserModelRevision{Key: selected, Value: "saved value"}}}})
	if s.view != userModelDetail {
		t.Fatal("detail was not adopted")
	}
	m = applyAll(m, navigationTestKey("q"))
	if m.modal != s || s.view != userModelPanel || s.list.CursorID() != selected {
		t.Fatal("Close/back must precede overlapping Down")
	}
	m = applyAll(m, navigationTestKey("q"))
	if m.modal != nil || !m.prompt.Focused() {
		t.Fatal("Close must dismiss Memory panel")
	}
}

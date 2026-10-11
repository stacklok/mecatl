package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

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

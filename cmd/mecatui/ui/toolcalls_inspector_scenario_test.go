package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// These acceptance pins intentionally start at the UI reducer seam: the tool-call
// browser is a local, current-session projection and has no server dependency.
func TestMecatuiToolcallsInspector_Scenario1_OpenEmptyRunningAndResume(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	if _, ok := builtinByName(m.caps, m.wiredCollaborators(), "toolcalls"); !ok {
		t.Fatal("/toolcalls is absent from the local slash palette")
	}
	m = openToolcallsForTest(t, m)
	if !toolcallsForTest(t, m).open {
		t.Fatal("/toolcalls did not open for an empty conversation")
	}
	m.closeModal()
	m.phase = phaseRunning
	m = addToolcallsForTest(t, m, 2) // reconstructed scrollback is the resume projection
	m = openToolcallsForTest(t, m)
	if got, want := len(toolcallsForTest(t, m).entries), 2; got != want {
		t.Fatalf("resumed current-session calls = %d, want %d", got, want)
	}
}

func TestMecatuiToolcallsInspector_Scenario1_ChronologicalNavigation(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m = addToolcallsForTest(t, m, 3)
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	if got, want := s.selected, 2; got != want {
		t.Fatalf("newest call selection = %d, want %d", got, want)
	}
	_, _ = s.Render(80, 20)
	_, handled, closed := s.HandleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if !handled || closed || s.selected != 1 {
		t.Fatalf("up navigation = selected %d handled=%v closed=%v", s.selected, handled, closed)
	}
	_, _, _ = s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !s.detail {
		t.Fatal("enter did not open the detail placeholder")
	}
	_, _, closed = s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if s.detail || closed || s.selected != 1 {
		t.Fatal("escape did not return from detail to its selected list call")
	}
}

func TestMecatuiToolcallsInspector_Scenario1_StableLiveSelection(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m = addToolcallsForTest(t, m, 2)
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	s.selected = 0
	m = addToolcallsForTest(t, m, 1)
	if got, want := s.selected, 0; got != want {
		t.Fatalf("earlier selected call moved to %d, want %d", got, want)
	}
	m.conv.resolveTool("call-0", "done", false)
	m.syncToolcalls()
	if got, want := s.selected, 0; got != want || !s.entries[0].resolved {
		t.Fatalf("result changed selected row or did not update it: selected=%d entry=%#v", got, s.entries[0])
	}
	s.selected = len(s.entries) - 1
	m = addToolcallsForTest(t, m, 1)
	if got, want := s.selected, len(s.entries)-1; got != want {
		t.Fatalf("newest-follow selection = %d, want %d", got, want)
	}
}

func TestMecatuiToolcallsInspector_Scenario3_FullRegionAndCompactFallback(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m = addToolcallsForTest(t, m, 1)
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	if got, _ := s.Render(0, 1); got != "" {
		t.Fatalf("nonpositive geometry rendered %q", got)
	}
	compact, _ := s.Render(10, 3)
	if !s.compact || !strings.Contains(stripANSIstr(compact), "too small") {
		t.Fatalf("compact fallback = %q, compact=%v", compact, s.compact)
	}
	normal, _ := s.Render(80, 20)
	if s.compact || !strings.Contains(stripANSIstr(normal), "Tool calls") {
		t.Fatalf("normal geometry did not restore the list: %q", normal)
	}
}

func TestMecatuiToolcallsInspector_Scenario3_InputOwnershipAndSafety(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.conv.addTool("unsafe", "Read\x1b]8;;https://unsafe.example\a", "{\"path\":\"x\x1b[31m\"}")
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	if !s.open || m.prompt.Focused() {
		t.Fatal("open inspector did not own keyboard input")
	}
	body, _ := s.Render(80, 20)
	if strings.Contains(body, "\x1b]") || strings.Contains(body, "\x1b[31m") {
		t.Fatalf("untrusted terminal control reached inspector rendering: %q", body)
	}
	m.vp.SetContent(strings.Repeat("conversation\n", 100))
	before := m.vp.YOffset()
	updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = updated.(Model)
	if got := m.vp.YOffset(); got != before {
		t.Fatalf("inspector wheel changed hidden conversation offset from %d to %d", before, got)
	}
}

func TestMecatuiToolcallsInspector_Scenario3_SessionReplacementClosesInspector(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m = openToolcallsForTest(t, m)
	m = m.resetSession()
	if toolcallsForTest(t, m) != nil {
		t.Fatal("session replacement left the inspector open")
	}
}

func newToolcallsInspectorModel(t *testing.T) Model {
	t.Helper()
	m := newTestModelFromDeps(Deps{Theme: testTheme(), Ctx: t.Context()})
	m.phase = phaseIdle
	m.width, m.height = 100, 30
	m.relayout()
	return m
}

func openToolcallsForTest(t *testing.T, m Model) Model {
	t.Helper()
	opened, _ := m.runToolcalls()
	return opened.(Model)
}

func addToolcallsForTest(t *testing.T, m Model, count int) Model {
	t.Helper()
	start := m.conv.scrollback.Len()
	for i := 0; i < count; i++ {
		m.conv.addTool(fmt.Sprintf("call-%d", start+i), "Read", fmt.Sprintf(`{"path":"file-%d.go"}`, start+i))
	}
	m.syncToolcalls()
	return m
}

func toolcallsForTest(t *testing.T, m Model) *toolcallsState {
	t.Helper()
	if m.modal == nil {
		return nil
	}
	s, ok := m.modal.(*toolcallsState)
	if !ok {
		t.Fatalf("modal = %T, want *toolcallsState", m.modal)
	}
	return s
}

package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type bodyOwnerTestSurface struct{}

func (*bodyOwnerTestSurface) Render(int, int) (string, []ClickableRegion) { return "", nil }
func (*bodyOwnerTestSurface) HandleKey(tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	return nil, false, false
}
func (*bodyOwnerTestSurface) HandleMsg(tea.Msg) (tea.Cmd, bool, bool)       { return nil, false, false }
func (*bodyOwnerTestSurface) HandleWheel(tea.MouseWheelMsg) (tea.Cmd, bool) { return nil, false }
func (*bodyOwnerTestSurface) Close()                                        {}

func TestPromptSelectionMouseEditAndCopy(t *testing.T) {
	m, cb := selModel(t)
	m.prompt.Rewrite("hello world")
	rect, ok := inputRegionRect(m)
	if !ok {
		t.Fatal("input region unavailable")
	}

	m, _ = pressMouse(m, tea.MouseLeft, rect.x0, rect.y0)
	m, _ = motionMouse(m, rect.x0+5, rect.y0)
	m, cmd := releaseMouse(m, rect.x0+5, rect.y0)
	if cmd != nil || !m.prompt.HasSelection() || m.prompt.SelectedText() != "hello" {
		t.Fatalf("release must retain prompt selection without copying: selected=%q cmd=%v", m.prompt.SelectedText(), cmd)
	}
	if len(cb.wrote) != 0 {
		t.Fatal("release copied prompt selection")
	}

	mm, _ := m.Update(tea.KeyPressMsg{Code: 'X', Text: "X"})
	m = mm.(Model)
	if got := m.prompt.Value(); got != "X world" {
		t.Fatalf("typed replacement = %q, want %q", got, "X world")
	}

	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	m = mm.(Model)
	if got := m.prompt.Value(); got != "X\n world" {
		t.Fatalf("newline replacement = %q", got)
	}

	mm, _ = m.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	m = mm.(Model)
	if !m.prompt.HasSelection() || m.prompt.SelectedText() != "X\n world" {
		t.Fatalf("ctrl+g did not select prompt: %q", m.prompt.SelectedText())
	}
	mm, cmd = m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	m = mm.(Model)
	leaves := collectLeaves(cmd)
	payload, ok := osc52Payload(leaves)
	if !ok || payload != "X\n world" {
		t.Fatalf("copy payload = %q, ok=%v", payload, ok)
	}
	for _, msg := range leaves {
		mm, _ = m.Update(msg)
		m = mm.(Model)
	}
	if len(cb.wrote) != 1 || string(cb.wrote[0]) != "X\n world" {
		t.Fatalf("shell clipboard writes = %q", cb.wrote)
	}
}

func TestPromptSelectionKeyboardWorksWithoutMouseAndStagingReplaces(t *testing.T) {
	m, _ := selModel(t)
	m.deps.NoMouse = true
	m.prompt.Rewrite("before after")
	m.prompt.SelectAll()

	mm, _ := m.Update(pasteMsg(largePasteText()))
	m = mm.(Model)
	if !strings.Contains(m.prompt.Value(), "[Pasted text #1]") || strings.Contains(m.prompt.Value(), "before") {
		t.Fatalf("staged paste did not replace selection: %q", m.prompt.Value())
	}
	if m.prompt.HasSelection() {
		t.Fatal("paste left a stale selection")
	}
}

func TestKeyboardPromptSelectionOwnsSelectionWithoutMouse(t *testing.T) {
	m, _ := selModel(t)
	m.deps.NoMouse = true
	m.prompt.Rewrite("before after")
	m.sel = selection{active: true, anchorL: 0, anchorC: 0, headL: 0, headC: 4}

	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModShift})
	m = mm.(Model)
	if !m.prompt.HasSelection() || m.sel.active {
		t.Fatalf("shift+left owners: prompt=%q conversation=%+v", m.prompt.SelectedText(), m.sel)
	}
	mm, _ = m.Update(tea.PasteMsg{Content: "X"})
	m = mm.(Model)
	if got := m.prompt.Value(); got != "before afteX" {
		t.Fatalf("paste did not replace keyboard selection: %q", got)
	}

	m.sel = selection{active: true, anchorL: 0, anchorC: 0, headL: 0, headC: 4}
	mm, _ = m.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	m = mm.(Model)
	if !m.prompt.HasSelection() || m.sel.active || m.prompt.SelectedText() != "before afteX" {
		t.Fatalf("configured select-all owners: prompt=%q conversation=%+v", m.prompt.SelectedText(), m.sel)
	}
}

func TestPromptSelectionSurvivesOverlayAndStopsStaleDrag(t *testing.T) {
	m, _ := selModel(t)
	m.prompt.Rewrite("hello world")
	rect, ok := inputRegionRect(m)
	if !ok {
		t.Fatal("input region unavailable")
	}
	m, _ = pressMouse(m, tea.MouseLeft, rect.x0, rect.y0)
	m, _ = motionMouse(m, rect.x0+5, rect.y0)
	if m.prompt.SelectedText() != "hello" {
		t.Fatalf("drag selection = %q", m.prompt.SelectedText())
	}

	m.conv.fleetStart("child", "goal", "", "", "", "", false)
	mm, _ := m.Update(tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
	m = mm.(Model)
	if !m.prompt.HasSelection() || m.prompt.SelectedText() != "hello" {
		t.Fatalf("overlay cleared completed prompt selection: %q", m.prompt.SelectedText())
	}
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if !m.prompt.Focused() {
		t.Fatal("overlay close did not refocus prompt")
	}
	if m.prompt.SelectedText() != "hello" {
		t.Fatalf("overlay focus cycle changed selection: %q", m.prompt.SelectedText())
	}
	m, _ = motionMouse(m, rect.x0+10, rect.y0)
	m, _ = releaseMouse(m, rect.x0+10, rect.y0)
	if m.prompt.SelectedText() != "hello" {
		t.Fatalf("stale drag changed selection after overlay: %q", m.prompt.SelectedText())
	}
}

func TestPromptMousePressRespectsBodyOwners(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Model)
	}{
		{"schedule", func(m *Model) { m.schedule.view = schedulePanel }},
		{"session details", func(m *Model) { m.sessionDetailsOpen = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := selModel(t)
			m.prompt.Rewrite("hello world")
			m.prompt.SelectAll() // A completed selection persists while an overlay is open.
			tc.setup(&m)
			rect, ok := inputRegionRect(m)
			if !ok {
				t.Fatal("input region unavailable")
			}

			m, _ = pressMouse(m, tea.MouseLeft, rect.x0, rect.y0)
			m, _ = motionMouse(m, rect.x0+5, rect.y0)
			m, _ = releaseMouse(m, rect.x0+5, rect.y0)
			if got := m.prompt.SelectedText(); got != "hello world" {
				t.Fatalf("mouse selection began behind body owner: got %q, want existing selection", got)
			}
		})
	}
}

func TestSelectableMatchesAllBodyOwners(t *testing.T) {
	m, _ := selModel(t)
	for _, tc := range []struct {
		name  string
		setup func(*Model)
		want  bool
	}{
		{"idle", func(*Model) {}, true},
		{"running", func(m *Model) { m.phase = phaseRunning }, true},
		{"fatal", func(m *Model) { m.phase = phaseFatal }, false},
		{"awaiting approval", func(m *Model) { m.phase = phaseAwaitingApproval }, false},
		{"replay", func(m *Model) { m.phase = phaseReplay }, false},
		{"session details", func(m *Model) { m.sessionDetailsOpen = true }, false},
		{"help", func(m *Model) { m.showHelp = true }, false},
		{"team", func(m *Model) { m.team.view = teamRoster }, false},
		{"agents inventory", func(m *Model) { m.agentsInv.view = agentsInvPanel }, false},
		{"modal surface", func(m *Model) { m.modal = &bodyOwnerTestSurface{} }, false},
		{"user model", func(m *Model) { m.userModel.view = userModelPanel }, false},
		{"reflections", func(m *Model) { m.reflections.view = reflectionsList }, false},
		{"dream", func(m *Model) { m.dream.view = dreamTargets }, false},
		{"effort", func(m *Model) { m.effort.view = effortPanel }, false},
		{"worktrees", func(m *Model) { m.worktrees.view = worktreesPanel }, false},
		{"schedule", func(m *Model) { m.schedule.view = schedulePanel }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := m
			tc.setup(&got)
			if selectable(got) != tc.want {
				t.Fatalf("selectable = %v, want %v", selectable(got), tc.want)
			}
		})
	}
}

func TestPromptAndConversationSelectionsAreExclusive(t *testing.T) {
	m, _ := selModel(t)
	m.prompt.Rewrite("prompt")
	m.prompt.SelectAll()
	top := convTopRow(m)
	m, _ = pressMouse(m, tea.MouseLeft, 0, top)
	if m.prompt.HasSelection() {
		t.Fatal("conversation selection did not clear prompt selection")
	}

	rect, ok := inputRegionRect(m)
	if !ok {
		t.Fatal("input region unavailable")
	}
	m, _ = pressMouse(m, tea.MouseLeft, rect.x0, rect.y0)
	if m.sel.active {
		t.Fatal("prompt selection did not clear conversation selection")
	}
}

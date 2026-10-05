package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/keymap"
)

func TestMecatuiQuieterToolCalls_Scenario2_InspectorShortcutAndFocus(t *testing.T) {
	for _, tc := range []struct {
		name  string
		phase phase
	}{{"idle", phaseIdle}, {"running", phaseRunning}} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModelFromDeps(Deps{Theme: testTheme(), NoMouse: true})
			m.phase = tc.phase
			m = applyAll(m, tea.WindowSizeMsg{Width: 40, Height: 12}, client.ToolCallMsg{ID: "read", Name: "Read", Args: `{"path":"before"}`})
			m.prompt.Rewrite("draft survives")
			beforeOffset := m.vp.YOffset()

			m, _ = pressKey(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
			if got := toolcallsForTest(t, m); got == nil || len(got.entries) != 1 {
				t.Fatalf("Toolcalls shortcut inspector = %#v, want current-session entry", got)
			}
			if !m.conv.resolveTool("read", "arrived while inspecting", false) {
				t.Fatal("could not deliver streamed tool result")
			}
			m.syncToolcalls()
			if got := toolcallsForTest(t, m); len(got.entries) != 1 || got.entries[0].state != toolcallDone {
				t.Fatalf("stream result did not update open inspector: %#v", got.entries)
			}
			m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
			m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
			m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
			if m.modal != nil || m.prompt.Value() != "draft survives" || m.vp.YOffset() != beforeOffset {
				t.Fatalf("closing inspector lost context: modal=%T prompt=%q offset=%d want %d", m.modal, m.prompt.Value(), m.vp.YOffset(), beforeOffset)
			}
		})
	}
}

func TestMecatuiQuieterToolCalls_Scenario2_ContextualShortcutAndOverrides(t *testing.T) {
	t.Run("approval owns shortcuts", func(t *testing.T) {
		m := shellAskModel(t, longShellArgs)
		m.prompt.Rewrite("draft")
		m, _ = pressKey(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
		if !approvalSurfaceOf(t, m).argsViewOpen || m.prompt.Value() != "draft" {
			t.Fatal("Toolcalls must remain the approval full-args action")
		}

		plan := planAskModel(t, true)
		before := plan.expandTools
		plan, _ = pressKey(plan, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
		if plan.modal == nil || plan.expandTools != before {
			t.Fatal("plan approval Toolcalls must be a consumed no-op")
		}
		if _, ok := plan.modal.(*toolcallsState); ok {
			t.Fatal("plan approval Toolcalls must not open the inspector")
		}
		plan, _ = pressKey(plan, tea.KeyPressMsg{Code: tea.KeyF9})
		if plan.expandTools != before || plan.prompt.Value() != "" {
			t.Fatal("ExpandConversation must be inert while approval owns the keyboard")
		}
	})

	t.Run("rebind and verdict collision", func(t *testing.T) {
		if err := keymap.Validate(keymap.Resolved{ByAction: map[string][]string{"Toolcalls": {"a"}}}); err == nil {
			t.Fatal("Toolcalls overlap with Allow must be rejected")
		}
		m := newTestModelFromDeps(Deps{Theme: testTheme(), KeyOverrides: map[string][]string{"Toolcalls": {"ctrl+o"}}})
		m.phase = phaseIdle
		m, _ = pressKey(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
		if m.modal != nil {
			t.Fatal("old Toolcalls chord opened an overlay after rebind")
		}
		m, _ = pressKey(m, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
		if toolcallsForTest(t, m) == nil {
			t.Fatal("rebound Toolcalls chord did not open inspector")
		}
	})
}

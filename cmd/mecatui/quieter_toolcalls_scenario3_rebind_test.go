package main

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
)

func TestMecatuiQuieterToolCalls_Scenario3_RebindAndRejectsDeprecatedAlias(t *testing.T) {
	for _, tc := range []struct {
		name, settings       string
		flags                []string
		wantTool, wantDetail []string
		fail                 string
	}{
		{"settings canonical rebind", "keymap:\n  Toolcalls: ctrl+f10,ctrl+f11\n  ExpandConversation: ctrl+f12,ctrl+f13\n", nil, []string{"ctrl+f10", "ctrl+f11"}, []string{"ctrl+f12", "ctrl+f13"}, ""},
		{"CLI canonical rebind", "", []string{"--keymap", "Toolcalls=ctrl+f10,ctrl+f11", "--keymap", "ExpandConversation=ctrl+f12,ctrl+f13"}, []string{"ctrl+f10", "ctrl+f11"}, []string{"ctrl+f12", "ctrl+f13"}, ""},
		{"CLI canonical takes precedence", "keymap:\n  Toolcalls: ctrl+f10\n  ExpandConversation: ctrl+f12\n", []string{"--keymap", "Toolcalls=ctrl+f11"}, []string{"ctrl+f11"}, []string{"ctrl+f12"}, ""},
		{"deprecated settings alias", "keymap:\n  ExpandTools: ctrl+f10\n", nil, nil, nil, `unknown action "ExpandTools"`},
		{"deprecated CLI alias", "", []string{"--keymap", "ExpandTools=ctrl+f11"}, nil, nil, `unknown action "ExpandTools"`},
		{"settings verdict collision", "keymap:\n  Toolcalls: enter\n", nil, nil, nil, "collision"},
		{"settings detail collision", "keymap:\n  ExpandConversation: ctrl+t\n", nil, nil, nil, "collision"},
		{"CLI detail collision", "", []string{"--keymap", "ExpandConversation=ctrl+t"}, nil, nil, "collision"},
		{"settings default agent collision", "keymap:\n  Toolcalls: f6\n", nil, nil, nil, "collision"},
		{"CLI default panel collision", "", []string{"--keymap", "ExpandConversation=ctrl+o"}, nil, nil, "collision"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			if tc.settings != "" {
				writeSettings(t, "mecatui", tc.settings)
			}
			cfg, err := parseFlags(tc.flags)
			if err != nil {
				t.Fatal(err)
			}
			var deps ui.Deps
			err = applyKeyOverridesToDeps(cfg, mustReadClientSettings(t), &deps)
			if tc.fail != "" {
				if err == nil || !strings.Contains(err.Error(), tc.fail) {
					t.Fatalf("apply error = %v, want %s", err, tc.fail)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(deps.KeyOverrides["Toolcalls"], tc.wantTool) || !reflect.DeepEqual(deps.KeyOverrides["ExpandConversation"], tc.wantDetail) {
				t.Fatalf("effective rebinds = %#v, want Toolcalls %v, ExpandConversation %v", deps.KeyOverrides, tc.wantTool, tc.wantDetail)
			}
		})
	}
}

func TestMecatuiQuieterToolCalls_Scenario3_ConfiguredFooterAndShortcuts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeSettings(t, "mecatui", "keymap:\n  Toolcalls: ctrl+f10\n  ExpandConversation: ctrl+f12\n")
	cfg, err := parseFlags([]string{"--keymap", "Toolcalls=ctrl+f11"})
	if err != nil {
		t.Fatal(err)
	}
	deps := ui.Deps{Theme: theme.New("aztec", theme.AztecPalette()), Ctx: t.Context(), NoAltScreen: true}
	if err := applyKeyOverridesToDeps(cfg, mustReadClientSettings(t), &deps); err != nil {
		t.Fatal(err)
	}
	m := ui.New(deps)
	for _, msg := range []tea.Msg{
		tea.WindowSizeMsg{Width: 120, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-configured-keys"},
		client.TurnStartMsg{Turn: 1},
		client.AssistantDeltaMsg{Turn: 1, Text: "answer"},
	} {
		updated, _ := m.Update(msg)
		m = updated.(ui.Model)
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "ctrl+f11 tool calls · ctrl+f12 session details · ctrl+c quit") || strings.Contains(view, "ctrl+f10 tool calls") || strings.Contains(view, "ctrl+g select all") || strings.Contains(view, "ctrl+y copy") || strings.Contains(view, "ctrl+u clear") || strings.Contains(view, "● mecatl") {
		t.Fatalf("effective settings/CLI shortcuts missing from footer: %s", view)
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF12, Mod: tea.ModCtrl})
	m = updated.(ui.Model)
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "● mecatl") {
		t.Fatalf("configured ExpandConversation chord did not reveal speaker: %s", view)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyF11, Mod: tea.ModCtrl})
	m = updated.(ui.Model)
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Tool calls") {
		t.Fatalf("configured Toolcalls chord did not open inspector: %s", view)
	}
}

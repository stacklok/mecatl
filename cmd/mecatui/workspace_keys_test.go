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

func TestWorkspaceSettingsCLIRegistration(t *testing.T) {
	for _, tc := range []struct {
		name, settings string
		flags          []string
		want           []string
		fail           bool
	}{
		{"settings", "keymap:\n  Workspace: ctrl+f12,ctrl+f13\n", nil, []string{"ctrl+f12", "ctrl+f13"}, false},
		{"CLI", "", []string{"--keymap", "Workspace=ctrl+f12,ctrl+f13"}, []string{"ctrl+f12", "ctrl+f13"}, false},
		{"CLI precedence", "keymap:\n  Workspace: f6\n", []string{"--keymap", "Workspace=ctrl+f12,ctrl+f13"}, []string{"ctrl+f12", "ctrl+f13"}, false},
		{"settings takes default", "keymap:\n  Workspace: f6\n", nil, nil, true},
		{"CLI takes default", "", []string{"--keymap", "Workspace=f6"}, nil, true},
		{"settings shadows default", "keymap:\n  Agents: f10\n", nil, nil, true},
		{"CLI shadows default", "", []string{"--keymap", "Agents=f10"}, nil, true},
		{"safe swap", "keymap:\n  Agents: f10\n", []string{"--keymap", "Workspace=f6"}, []string{"f6"}, false},
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
			deps := ui.Deps{Ctx: t.Context(), Theme: theme.New("aztec", theme.AztecPalette()), NoAltScreen: true}
			err = applyKeyOverridesToDeps(cfg, mustReadClientSettings(t), &deps)
			if tc.fail {
				if err == nil || !strings.Contains(err.Error(), "collision") {
					t.Fatalf("startup error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(deps.KeyOverrides["Workspace"], tc.want) {
				t.Fatalf("effective Workspace = %v, want %v", deps.KeyOverrides["Workspace"], tc.want)
			}
			m := ui.New(deps)
			for _, msg := range []tea.Msg{tea.WindowSizeMsg{Width: 120, Height: 40}, client.SessionReadyMsg{SessionID: "workspace-config"}} {
				mm, _ := m.Update(msg)
				m = mm.(ui.Model)
			}
			key := tea.KeyPressMsg{Code: tea.KeyF13, Mod: tea.ModCtrl}
			if tc.name == "safe swap" {
				key = tea.KeyPressMsg{Code: tea.KeyF6}
			}
			mm, _ := m.Update(key)
			m = mm.(ui.Model)
			if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Workspace snapshot") || !strings.Contains(view, "workspace-config") {
				t.Fatalf("production configured key did not open Workspace: %s", view)
			}
		})
	}
}

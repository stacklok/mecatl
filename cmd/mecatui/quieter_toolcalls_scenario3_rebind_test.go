package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/ui"
)

func TestMecatuiQuieterToolCalls_Scenario3_RebindAndLegacyAlias(t *testing.T) {
	for _, tc := range []struct {
		name, settings       string
		flags                []string
		wantTool, wantDetail []string
		fail                 string
	}{
		{"settings rebind", "keymap:\n  Toolcalls: ctrl+f10,ctrl+f11\n  ExpandConversation: ctrl+f12,ctrl+f13\n", nil, []string{"ctrl+f10", "ctrl+f11"}, []string{"ctrl+f12", "ctrl+f13"}, ""},
		{"CLI rebind", "", []string{"--keymap", "Toolcalls=ctrl+f10,ctrl+f11", "--keymap", "ExpandConversation=ctrl+f12,ctrl+f13"}, []string{"ctrl+f10", "ctrl+f11"}, []string{"ctrl+f12", "ctrl+f13"}, ""},
		{"settings alias overridden by CLI", "keymap:\n  ExpandTools: ctrl+f10\n  ExpandConversation: ctrl+f12\n", []string{"--keymap", "Toolcalls=ctrl+f11"}, []string{"ctrl+f11"}, []string{"ctrl+f12"}, ""},
		{"CLI alias overrides settings", "keymap:\n  Toolcalls: ctrl+f10\n", []string{"--keymap", "ExpandTools=ctrl+f11"}, []string{"ctrl+f11"}, nil, ""},
		{"settings mixed names", "keymap:\n  ExpandTools: ctrl+f10\n  Toolcalls: ctrl+f11\n", nil, nil, nil, "ExpandTools and Toolcalls"},
		{"CLI mixed names", "", []string{"--keymap", "ExpandTools=ctrl+f10", "--keymap", "Toolcalls=ctrl+f11"}, nil, nil, "ExpandTools and Toolcalls"},
		{"settings verdict collision", "keymap:\n  Toolcalls: enter\n", nil, nil, nil, "collision"},
		{"CLI alias verdict collision", "", []string{"--keymap", "ExpandTools=esc"}, nil, nil, "collision"},
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
			if _, ok := deps.KeyOverrides["ExpandTools"]; ok {
				t.Fatal("alias survived normalization")
			}
		})
	}
}

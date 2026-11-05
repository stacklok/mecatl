package keymap

import (
	"strings"
	"testing"
)

func TestWorkspaceEffectiveDefaultCollisions(t *testing.T) {
	for _, action := range []string{"Submit", "Newline", "Cancel", "ClearPrompt", "EditBack", "Paste", "SelectAll", "CopySelection", "Quit", "QuitD", "Suspend", "ScrollU", "ScrollD", "ScrollTop", "ScrollBottom", "ModeSwitch", "MCPPanel", "Resources", "Prompts", "Agents", "Effort", "Toolcalls", "ExpandConversation", "Help"} {
		t.Run(action+" takes default Workspace", func(t *testing.T) {
			res, err := Parse(map[string][]string{action: {"f10"}})
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(res); err == nil || !strings.Contains(err.Error(), "Workspace") {
				t.Fatalf("collision with effective f10 default = %v", err)
			}
		})
	}
	for _, chord := range []string{"enter", "shift+enter", "ctrl+j", "ctrl+enter", "alt+enter", "esc", "ctrl+u", "up", "ctrl+v", "ctrl+g", "ctrl+y", "ctrl+c", "ctrl+d", "ctrl+z", "pgup", "pgdown", "home", "end", "shift+tab", "ctrl+o", "ctrl+r", "f8", "f6", "f7", "ctrl+t", "f9", "?"} {
		t.Run("Workspace takes "+chord, func(t *testing.T) {
			res, err := Parse(map[string][]string{"Workspace": {chord}})
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(res); err == nil {
				t.Fatal("accepted effective-default collision or bare rune")
			}
		})
	}
	for _, overrides := range []map[string][]string{
		{}, {"Workspace": {"ctrl+f12", "ctrl+f13"}},
		{"Workspace": {"f6"}, "Agents": {"f10"}},
		{"Workspace": {"f9"}, "ExpandConversation": {"f10"}},
		{"Workspace": {"ctrl+t"}, "Toolcalls": {"f10"}},
	} {
		res, err := Parse(overrides)
		if err != nil {
			t.Fatal(err)
		}
		if err := Validate(res); err != nil {
			t.Fatalf("safe bindings %v: %v", overrides, err)
		}
	}
}

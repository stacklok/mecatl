package ui

import (
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

func TestBoundedNavigationMove(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys keyMap
		msg  tea.KeyPressMsg
		want bounded.Move
		ok   bool
	}{
		{"default up", defaultKeys(), tea.KeyPressMsg{Code: tea.KeyUp}, bounded.LineUp, true},
		{"default down", defaultKeys(), tea.KeyPressMsg{Code: tea.KeyDown}, bounded.LineDown, true},
		{"default page up", defaultKeys(), tea.KeyPressMsg{Code: tea.KeyPgUp}, bounded.PageUp, true},
		{"default page down", defaultKeys(), tea.KeyPressMsg{Code: tea.KeyPgDown}, bounded.PageDown, true},
		{"default top", defaultKeys(), tea.KeyPressMsg{Code: tea.KeyHome}, bounded.Top, true},
		{"default bottom", defaultKeys(), tea.KeyPressMsg{Code: tea.KeyEnd}, bounded.End, true},
		{"remapped up", navigationTestKeys(), navigationTestKey("u"), bounded.LineUp, true},
		{"remapped down", navigationTestKeys(), navigationTestKey("d"), bounded.LineDown, true},
		{"remapped page up", navigationTestKeys(), navigationTestKey("p"), bounded.PageUp, true},
		{"remapped page down", navigationTestKeys(), navigationTestKey("n"), bounded.PageDown, true},
		{"remapped top", navigationTestKeys(), navigationTestKey("t"), bounded.Top, true},
		{"remapped bottom", navigationTestKeys(), navigationTestKey("b"), bounded.End, true},
		{"unmatched", defaultKeys(), navigationTestKey("x"), 0, false},
		{"overlap keeps declared precedence", navigationOverlapTestKeys(), navigationTestKey("x"), bounded.LineUp, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := boundedNavigationMove(tc.msg, tc.keys)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("boundedNavigationMove(%q) = (%v, %t), want (%v, %t)", tc.msg.String(), got, ok, tc.want, tc.ok)
			}
		})
	}
}

func navigationTestKeys() keyMap {
	return applyKeyOverrides(defaultKeys(), map[string][]string{
		"Up": {"u"}, "Down": {"d"}, "ScrollU": {"p"}, "ScrollD": {"n"}, "ScrollTop": {"t"}, "ScrollBottom": {"b"},
	})
}

func navigationOverlapTestKeys() keyMap {
	keys := navigationTestKeys()
	keys.Up = key.NewBinding(key.WithKeys("x"))
	keys.Down = key.NewBinding(key.WithKeys("x"))
	return keys
}

func navigationTestKey(text string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: rune(text[0]), Text: text}
}

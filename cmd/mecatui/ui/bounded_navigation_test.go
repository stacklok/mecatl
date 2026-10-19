package ui

import (
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

func TestBoundedNavigationMove(t *testing.T) {
	disabled := defaultKeys()
	disabled.Down.SetEnabled(false)
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
		{"disabled", disabled, tea.KeyPressMsg{Code: tea.KeyDown}, 0, false},
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

func TestBoundedNavigationOrderedSubset(t *testing.T) {
	keys := navigationOverlapTestKeys()
	keys.ScrollD = key.NewBinding(key.WithKeys("x"))
	keys.ScrollBottom = key.NewBinding(key.WithKeys("x"))
	for _, tc := range []struct {
		name  string
		order []bounded.Move
		msg   tea.KeyPressMsg
		want  bounded.Move
		ok    bool
	}{
		{"down first", []bounded.Move{bounded.LineDown, bounded.LineUp}, navigationTestKey("x"), bounded.LineDown, true},
		{"page first", []bounded.Move{bounded.PageDown, bounded.LineDown}, navigationTestKey("x"), bounded.PageDown, true},
		{"end only", []bounded.Move{bounded.End}, navigationTestKey("x"), bounded.End, true},
		{"excluded page", []bounded.Move{bounded.LineUp, bounded.LineDown}, navigationTestKey("p"), 0, false},
		{"excluded top", []bounded.Move{bounded.PageDown, bounded.PageUp}, navigationTestKey("t"), 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := boundedNavigationMove(tc.msg, keys, tc.order...); got != tc.want || ok != tc.ok {
				t.Fatalf("move = (%v, %t), want (%v, %t)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestBoundedFilterNavigationMove(t *testing.T) {
	keys := navigationTestKeys()
	keys.Up.SetEnabled(false)
	keys.Down.SetEnabled(false)
	keys.ScrollTop.SetEnabled(false)
	for _, tc := range []struct {
		name string
		msg  tea.KeyPressMsg
		want bounded.Move
		ok   bool
	}{
		{"fixed up", tea.KeyPressMsg{Code: tea.KeyUp}, bounded.LineUp, true},
		{"fixed down", tea.KeyPressMsg{Code: tea.KeyDown}, bounded.LineDown, true},
		{"remapped up is text", navigationTestKey("u"), 0, false},
		{"remapped down is text", navigationTestKey("d"), 0, false},
		{"j is text", navigationTestKey("j"), 0, false},
		{"k is text", navigationTestKey("k"), 0, false},
		{"page up", navigationTestKey("p"), bounded.PageUp, true},
		{"page down", navigationTestKey("n"), bounded.PageDown, true},
		{"disabled top", navigationTestKey("t"), 0, false},
		{"end", navigationTestKey("b"), bounded.End, true},
		{"old page", tea.KeyPressMsg{Code: tea.KeyPgDown}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := boundedFilterNavigationMove(tc.msg, keys); got != tc.want || ok != tc.ok {
				t.Fatalf("move = (%v, %t), want (%v, %t)", got, ok, tc.want, tc.ok)
			}
		})
	}
	if keys.Up.Enabled() || keys.Down.Enabled() || keys.Up.Keys()[0] != "u" || keys.Down.Keys()[0] != "d" {
		t.Fatal("filter navigation mutated shared bindings")
	}
}

func TestBoundedWheelMove(t *testing.T) {
	for _, tc := range []struct {
		button tea.MouseButton
		want   bounded.Move
		ok     bool
	}{
		{tea.MouseWheelUp, bounded.LineUp, true},
		{tea.MouseWheelDown, bounded.LineDown, true},
		{tea.MouseWheelLeft, 0, false},
		{tea.MouseWheelRight, 0, false},
		{tea.MouseNone, 0, false},
	} {
		if got, ok := boundedWheelMove(tea.MouseWheelMsg{Button: tc.button}); got != tc.want || ok != tc.ok {
			t.Errorf("button %v = (%v, %t), want (%v, %t)", tc.button, got, ok, tc.want, tc.ok)
		}
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

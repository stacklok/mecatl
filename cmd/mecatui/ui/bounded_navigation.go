package ui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

// The optional order preserves each caller's supported moves and binding precedence.
func boundedNavigationMove(msg tea.KeyPressMsg, keys keyMap, order ...bounded.Move) (bounded.Move, bool) {
	bindings := [...]key.Binding{
		bounded.LineUp:   keys.Up,
		bounded.LineDown: keys.Down,
		bounded.PageUp:   keys.ScrollU,
		bounded.PageDown: keys.ScrollD,
		bounded.Top:      keys.ScrollTop,
		bounded.End:      keys.ScrollBottom,
	}
	if len(order) == 0 {
		order = []bounded.Move{bounded.LineUp, bounded.LineDown, bounded.PageUp, bounded.PageDown, bounded.Top, bounded.End}
	}
	for _, move := range order {
		if key.Matches(msg, bindings[move]) {
			return move, true
		}
	}
	return 0, false
}

// Filterable pickers reserve printable Up/Down bindings for text input.
func boundedFilterNavigationMove(msg tea.KeyPressMsg, keys keyMap, order ...bounded.Move) (bounded.Move, bool) {
	keys.Up = key.NewBinding(key.WithKeys("up"))
	keys.Down = key.NewBinding(key.WithKeys("down"))
	return boundedNavigationMove(msg, keys, order...)
}

func boundedWheelMove(msg tea.MouseWheelMsg) (bounded.Move, bool) {
	switch msg.Button {
	case tea.MouseWheelUp:
		return bounded.LineUp, true
	case tea.MouseWheelDown:
		return bounded.LineDown, true
	default:
		return 0, false
	}
}

package ui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

func boundedNavigationMove(msg tea.KeyPressMsg, keys keyMap) (bounded.Move, bool) {
	switch {
	case key.Matches(msg, keys.Up):
		return bounded.LineUp, true
	case key.Matches(msg, keys.Down):
		return bounded.LineDown, true
	case key.Matches(msg, keys.ScrollU):
		return bounded.PageUp, true
	case key.Matches(msg, keys.ScrollD):
		return bounded.PageDown, true
	case key.Matches(msg, keys.ScrollTop):
		return bounded.Top, true
	case key.Matches(msg, keys.ScrollBottom):
		return bounded.End, true
	default:
		return 0, false
	}
}

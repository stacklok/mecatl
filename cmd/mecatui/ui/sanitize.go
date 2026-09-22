package ui

import "github.com/stacklok/mecatl/cmd/mecatui/ui/internal/terminaltext"

// sanitizeTerminal strips terminal control bytes from server-derived plain strings
// before they reach a lipgloss Render. Assistant markdown is rendered through
// glamour and must not be passed through here.
func sanitizeTerminal(s string) string {
	return terminaltext.Sanitize(s)
}

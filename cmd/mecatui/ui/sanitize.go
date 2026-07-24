package ui

import "strings"

// sanitizeTerminal strips terminal control bytes from server-derived strings
// before they reach a lipgloss Render (which passes raw bytes through to the
// terminal). Without this, a malicious tool result, tool args, or permission-ask
// field could embed ANSI/OSC escapes to redraw the screen or spoof the approval
// modal (CWE-150 terminal-escape injection).
//
// It removes all C0 control bytes (0x00–0x1F), ESC (0x1B), DEL (0x7F), and the
// C1 control range (0x80–0x9F — 8-bit CSI/OSC equivalents a terminal in 8-bit
// mode would interpret as escape-sequence openers), preserving only newline
// (\n) and tab (\t) for layout. Assistant markdown is rendered through glamour
// (which neutralises escapes itself) and must NOT be passed through here —
// only the plain-lipgloss server strings are.
func sanitizeTerminal(s string) string {
	if !strings.ContainsFunc(s, isControl) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isControl reports whether r is a control byte we strip (C0 / ESC / DEL / C1),
// except the layout-preserving \n and \t. The C1 range (0x80–0x9F) is stripped
// too: in a terminal with 8-bit controls enabled, 0x9B is CSI and 0x9D is OSC —
// single-byte escape-sequence openers (the sanitizeTrustEcho precedent,
// cmd/mecatui/trust.go). Common terminals default to 7-bit mode and render C1
// as glyphs, so this is defence-in-depth, not a live exploit — but a title
// reaching an OSC sequence must carry no escape opener of either width.
func isControl(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20 || r == 0x7f:
		return true // C0 + ESC + DEL
	case r >= 0x80 && r <= 0x9f:
		return true // C1 (8-bit CSI/OSC equivalents)
	default:
		return false
	}
}

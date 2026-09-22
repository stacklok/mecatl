package ui

import (
	"unicode"

	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/cards"
)

// sanitizeTerminal strips terminal control bytes from server-derived strings
// before they reach a lipgloss Render (which passes raw bytes through to the
// terminal). Without this, a malicious tool result, tool args, or permission-ask
// field could embed ANSI/OSC escapes to redraw the screen or spoof the approval
// modal (CWE-150 terminal-escape injection).
//
// It removes all C0 control bytes (0x00–0x1F), ESC (0x1B), DEL (0x7F), the
// C1 control range (0x80–0x9F — 8-bit CSI/OSC equivalents a terminal in 8-bit
// mode would interpret as escape-sequence openers), and the Unicode Cf format
// characters (zero-width, BOM, bidi override/isolate — see isControl), preserving
// only newline (\n) and tab (\t) for layout. Assistant markdown is rendered through glamour
// (which neutralises escapes itself) and must NOT be passed through here —
// only the plain-lipgloss server strings are.
func sanitizeTerminal(s string) string {
	return cards.SanitizePlain(s)
}

// isControl reports whether r is a control byte we strip (C0 / ESC / DEL / C1 / Cf),
// except the layout-preserving \n and \t. The C1 range (0x80–0x9F) is stripped
// too: in a terminal with 8-bit controls enabled, 0x9B is CSI and 0x9D is OSC —
// single-byte escape-sequence openers (the sanitizeTrustEcho precedent,
// cmd/mecatui/trust.go). Common terminals default to 7-bit mode and render C1
// as glyphs, so this is defence-in-depth, not a live exploit — but a title
// reaching an OSC sequence must carry no escape opener of either width.
//
// Unicode Cf (format) goes with them, for the display half of the same reason the engine's
// fence canonicalises it away before matching: U+202E (RTL override) and its bidi siblings
// REORDER a rendered line, and U+200B/U+FEFF are invisible — so a provider error or child
// summary carrying one can present a failure line that reads as something other than what
// it says (CWE-1007, spoofing a trusted UI). None of them is whitespace to unicode.IsSpace,
// so the strings.Fields collapse the callers apply does not touch them. Display-only: no
// capability is gained either way, which is why this is one predicate and not a policy.
func isControl(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20 || r == 0x7f:
		return true // C0 + ESC + DEL
	case r >= 0x80 && r <= 0x9f:
		return true // C1 (8-bit CSI/OSC equivalents)
	case unicode.Is(unicode.Cf, r):
		return true // zero-width, BOM, bidi overrides/isolates
	default:
		return false
	}
}

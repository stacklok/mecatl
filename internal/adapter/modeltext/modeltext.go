// Package modeltext contains protocol-neutral hygiene for model catalog text.
package modeltext

import (
	"strings"
	"unicode"
)

// StripControls removes terminal/log control characters while preserving all
// ordinary text. Callers decide whether modification means sanitize or reject.
func StripControls(value string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			return -1
		case r == 0x2028 || r == 0x2029 || unicode.Is(unicode.Bidi_Control, r):
			return -1
		default:
			return r
		}
	}, value)
}

// TruncateRunes caps value without splitting a multi-byte rune.
func TruncateRunes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

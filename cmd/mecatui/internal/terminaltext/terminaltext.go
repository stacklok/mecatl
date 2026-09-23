// Package terminaltext provides terminal-safe plain-text rendering helpers for
// mecatui UI and status-line renderers.
package terminaltext

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Sanitize removes terminal controls from plain text while preserving layout
// newlines and tabs. It removes C0 controls, ESC, DEL, C1 controls, and Unicode
// Cf format characters. Markdown must not pass through this function.
func Sanitize(s string) string {
	if !strings.ContainsFunc(s, isControl) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isControl(r) {
			return -1
		}
		return r
	}, s)
}

func isControl(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20 || r == 0x7f:
		return true
	case r >= 0x80 && r <= 0x9f:
		return true
	case unicode.Is(unicode.Cf, r):
		return true
	default:
		return false
	}
}

// SanitizeSingleLine removes terminal controls from a single-line display
// value. Unlike Sanitize, it removes layout newlines, tabs, U+2028, and U+2029.
func SanitizeSingleLine(s string) string {
	if !strings.ContainsFunc(s, isSingleLineControl) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isSingleLineControl(r) {
			return -1
		}
		return r
	}, s)
}

func isSingleLineControl(r rune) bool {
	return r == '\n' || r == '\t' || r == '\u2028' || r == '\u2029' || isControl(r)
}

// NormalizeWidth rewrites text so Lip Gloss's WcWidth and GraphemeWidth agree
// for every grapheme cluster. It preserves agreeing clusters, then tries
// removing VS16, keeping the first scalar, and finally U+FFFD.
func NormalizeWidth(src string) string {
	if !strings.ContainsFunc(src, func(r rune) bool { return r > 0x7f }) {
		return src
	}
	var out strings.Builder
	out.Grow(len(src))
	for rest := src; rest != ""; {
		cluster, _ := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		rest = rest[len(cluster):]
		if ansi.StringWidthWc(cluster) == ansi.StringWidth(cluster) {
			out.WriteString(cluster)
			continue
		}
		stripped := strings.ReplaceAll(cluster, "\ufe0f", "")
		if ansi.StringWidthWc(stripped) == ansi.StringWidth(stripped) {
			out.WriteString(stripped)
			continue
		}
		first, _ := utf8.DecodeRuneInString(stripped)
		candidate := string(first)
		if ansi.StringWidthWc(candidate) == ansi.StringWidth(candidate) {
			out.WriteString(candidate)
		} else {
			out.WriteRune('�')
		}
	}
	return out.String()
}

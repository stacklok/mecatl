package cards

import (
	"encoding/json"
	"strings"

	"charm.land/lipgloss/v2"
)

// ErrorInput is caller-owned transient-error content.
type ErrorInput struct{ Text string }

// ErrorSnapshot is an immutable transient-error snapshot.
type ErrorSnapshot struct{ text string }

// ErrorAppearance contains the resolved transient-error style.
type ErrorAppearance struct{ Error lipgloss.Style }

// SnapshotError owns a transient-error input.
func SnapshotError(in ErrorInput) ErrorSnapshot { return ErrorSnapshot{text: strings.Clone(in.Text)} }

// PrepareError prepares a transient error.
func PrepareError(in ErrorSnapshot, layout PlainLayout, a ErrorAppearance) Prepared {
	return preparePlain("error", layout, []string{wrapPrefixed("✗ ", SanitizePlain(in.text), a.Error, layout.width)}, []string{in.text}, 0)
}

// PermanentErrorInput is caller-owned permanent-error content.
type PermanentErrorInput struct{ Text string }

// PermanentErrorSnapshot is an immutable permanent-error snapshot.
type PermanentErrorSnapshot struct{ text string }

// PermanentErrorAppearance contains resolved permanent-error styles.
type PermanentErrorAppearance struct{ Error, Muted lipgloss.Style }

// SnapshotPermanentError owns a permanent-error input.
func SnapshotPermanentError(in PermanentErrorInput) PermanentErrorSnapshot {
	return PermanentErrorSnapshot{text: strings.Clone(in.Text)}
}

// PreparePermanentError prepares collapsed or expanded permanent-error content.
func PreparePermanentError(in PermanentErrorSnapshot, layout PlainLayout, a PermanentErrorAppearance) Prepared {
	summary := PermanentErrorSummary(in.text)
	lines := []string{wrapPrefixed("✗ ", summary, a.Error, layout.width)}
	if layout.expanded {
		lines = append(lines, a.Muted.Render("raw payload:"), wrapStyled(SanitizePlain(in.text), a.Muted, layout.width))
	} else {
		lines = append(lines, a.Muted.Render("  "+layout.expandMark+" shows details"))
	}
	return preparePlain("permanent-error", layout, lines, []string{in.text}, 0)
}

// PermanentErrorSummary preserves compatibility with legacy SDK-shaped errors.
func PermanentErrorSummary(raw string) string {
	first := firstLineCap(CollapseErrorSummary(raw), 120)
	if first == "" {
		return "permanent provider error — retrying won't help; the request is rejected. Start a new session."
	}
	return SanitizePlain(first) + " — retrying won't help; the request is rejected. Start a new session."
}

// CollapseErrorSummary rewrites a legacy SDK-shaped provider error.
func CollapseErrorSummary(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, `": `); i >= 0 && strings.HasPrefix(s, `POST "`) {
		s = strings.TrimSpace(s[i+3:])
	}
	if i := strings.IndexByte(s, '{'); i >= 0 {
		head, tail := strings.TrimRight(s[:i], " :"), s[i:]
		if msg := extractJSONMessage(tail); msg != "" {
			if head != "" {
				return head + ": " + msg
			}
			return msg
		}
		return head
	}
	return s
}

func extractJSONMessage(s string) string {
	var env map[string]json.RawMessage
	if json.Unmarshal([]byte(s), &env) != nil {
		return ""
	}
	if raw, ok := env["error"]; ok {
		if json.Unmarshal(raw, &env) != nil {
			return ""
		}
	}
	if raw, ok := env["message"]; ok {
		var msg string
		if json.Unmarshal(raw, &msg) == nil {
			return strings.TrimSpace(msg)
		}
	}
	return ""
}

func firstLineCap(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if first, _, ok := strings.Cut(s, "\n"); ok {
		s = first
	}
	if rs := []rune(s); len(rs) > maxRunes {
		s = string(rs[:maxRunes])
	}
	return s
}

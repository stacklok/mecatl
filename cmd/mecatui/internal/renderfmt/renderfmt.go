// Package renderfmt provides stateless formatting shared by mecatui renderers.
package renderfmt

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

const (
	ctxWarnFraction   = 0.60
	ctxDangerFraction = 0.85
	ctxBarWidth       = 8
	ctxGlyphOK        = "▒"
	ctxGlyphWarn      = "▓"
	ctxGlyphDanger    = "█"
	ctxGlyphEmpty     = "░"
	ctxDangerMark     = " ⚠"

	turnStatCacheFloor = 0.10
	trivialTurnTokens  = 50
	slotCtxWarn        = "ctxWarn"
	slotMuted          = "muted"
)

// HumanizeTokens renders a token count compactly.
func HumanizeTokens(n int64) string {
	if n < 0 {
		n = 0
	}
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return trimDecimal(float64(n)/1000.0) + "K"
	default:
		return trimDecimal(float64(n)/1_000_000.0) + "M"
	}
}

// trimDecimal formats v to one decimal place, dropping a redundant ".0".
func trimDecimal(v float64) string {
	s := fmt.Sprintf("%.1f", v)
	return strings.TrimSuffix(s, ".0")
}

// CacheHitRate returns the cache-read fraction of input tokens.
func CacheHitRate(u client.Usage) float64 {
	if u.InputTokens <= 0 {
		return 0
	}
	return float64(u.CacheReadTokens) / float64(u.InputTokens)
}

// PctString formats a fraction as a clamped integer percentage.
func PctString(frac float64) string {
	p := int(frac*100 + 0.5)
	if p < 0 {
		p = 0
	}
	if p > 100 {
		p = 100
	}
	return fmt.Sprintf("%d%%", p)
}

// contextFraction returns the clamped used/window fill fraction in [0,1].
func contextFraction(used, window int64) float64 {
	if window <= 0 {
		return 0
	}
	frac := float64(used) / float64(window)
	if frac < 0 {
		return 0
	}
	if frac > 1 {
		return 1
	}
	return frac
}

func contextPressureSlot(frac float64) string {
	switch {
	case frac >= ctxDangerFraction:
		return "ctxDanger"
	case frac >= ctxWarnFraction:
		return slotCtxWarn
	default:
		return "ctxOk"
	}
}

func contextGlyph(frac float64) string {
	switch {
	case frac >= ctxDangerFraction:
		return ctxGlyphDanger
	case frac >= ctxWarnFraction:
		return ctxGlyphWarn
	default:
		return ctxGlyphOK
	}
}

// contextLabel formats the percentage with the non-colour danger cue.
func contextLabel(frac float64) string {
	label := PctString(frac)
	if frac >= ctxDangerFraction {
		label += ctxDangerMark
	}
	return label
}

// RenderContextMeter formats context for a team roster subhead.
func RenderContextMeter(th theme.Theme, used, window int64) string {
	if used < 0 {
		used = 0
	}
	if window <= 0 {
		return "ctx " + HumanizeTokens(used)
	}
	frac := contextFraction(used, window)
	meter := th.Style(contextPressureSlot(frac)).Render(contextBar(frac) + " " + contextLabel(frac))
	return "ctx " + meter + " · " + HumanizeTokens(used) + "/" + HumanizeTokens(window)
}

// RenderContextMeterPlain is the ANSI-free representation required before generic
// roster wrapping; RenderContextMeter's styled bar must not be sanitized as raw text.
func RenderContextMeterPlain(used, window int64) string {
	if used < 0 {
		used = 0
	}
	if window <= 0 {
		return "ctx " + HumanizeTokens(used)
	}
	frac := contextFraction(used, window)
	return "ctx " + contextBar(frac) + " " + contextLabel(frac) + " · " + HumanizeTokens(used) + "/" + HumanizeTokens(window)
}

// contextBar builds a meter bar whose glyphs carry the pressure band.
func contextBar(frac float64) string {
	filled := int(frac*float64(ctxBarWidth) + 0.5)
	if filled > ctxBarWidth {
		filled = ctxBarWidth
	}
	return strings.Repeat(contextGlyph(frac), filled) + strings.Repeat(ctxGlyphEmpty, ctxBarWidth-filled)
}

// TurnStatLine formats the inline per-turn model-exchange statistics.
func TurnStatLine(msg client.TurnEndMsg) string {
	var b strings.Builder
	fmt.Fprintf(&b, "↑%s ↓%s", HumanizeTokens(msg.Usage.InputTokens), HumanizeTokens(msg.Usage.OutputTokens))
	if msg.DurationMs > 0 {
		b.WriteString(" · " + FormatDuration(msg.DurationMs))
	}
	if rate := CacheHitRate(msg.Usage); rate >= turnStatCacheFloor {
		b.WriteString(" · " + PctString(rate) + " cached")
	}
	return b.String()
}

// TrivialTurn reports whether a turn's statistics are too negligible for scrollback.
func TrivialTurn(msg client.TurnEndMsg) bool {
	return msg.Usage.InputTokens <= trivialTurnTokens && msg.Usage.OutputTokens <= trivialTurnTokens && msg.DurationMs < 1000
}

// FormatDuration renders an elapsed millisecond count compactly.
func FormatDuration(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return trimDecimal(float64(ms)/1000.0) + "s"
}

// HumanizeDuration renders a millisecond wall-clock duration compactly: sub-second
// as "Nms", under a minute as "N.Ns", else "Nm Ns". A non-positive duration (no
// clock) renders as "0ms".
func HumanizeDuration(ms int64) string {
	if ms <= 0 {
		return "0ms"
	}
	if ms < 1000 {
		return strconv.FormatInt(ms, 10) + "ms"
	}
	secs := float64(ms) / 1000.0
	if secs < 60 {
		return trimDecimal(secs) + "s"
	}
	m := int64(secs) / 60
	s := int64(secs) % 60
	return strconv.FormatInt(m, 10) + "m " + strconv.FormatInt(s, 10) + "s"
}

// HumanizeBytes renders a byte count compactly: bytes verbatim under 1 KB, then
// "N.N KB"/"N.N MB"/"N.N GB"/"N.N TB" with one decimal (trailing ".0" trimmed).
// The math is SI/decimal (1 KB = 1000 B, 1 MB = 1e6 B, …).
func HumanizeBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	const unit = 1000
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for value := n / unit; value >= unit && exp < 3; value /= unit {
		div *= unit
		exp++
	}
	return trimDecimal(float64(n)/float64(div)) + " " + [...]string{"KB", "MB", "GB", "TB"}[exp]
}

// StopReasonLabel maps a terminal stop reason to status text and theme style slot.
func StopReasonLabel(stop string) (text, slot string) {
	switch stop {
	case "end_turn", "":
		return "done", slotMuted
	case "max_turns":
		return "stopped · turn limit", slotCtxWarn
	case "max_tool_calls":
		return "stopped · tool-call limit", slotCtxWarn
	case "max_consecutive_failures":
		return "stopped · repeated failures", slotCtxWarn
	case "budget":
		return "stopped · token budget", slotCtxWarn
	case "cancelled":
		return "cancelled", slotMuted
	case "no_progress":
		return "stopped · no progress", slotCtxWarn
	case "structured_output":
		return "stopped · schema unmet", slotCtxWarn
	case "plan_approved":
		return "plan approved · executing", slotMuted
	case "plan_iterate":
		return "plan iterate · awaiting your feedback", slotMuted
	case "error":
		return "error", "errorText"
	default:
		return terminaltext.Sanitize(stop), slotMuted
	}
}

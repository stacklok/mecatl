package ui

import (
	"strings"
)

// windowTitleRunes caps the title SEGMENT of the terminal window/tab title (the
// "<title> — …" head). 40 runes is tab-sized: wide enough for a real prompt's
// first clause, narrow enough that the status word + "mecatui" survive a
// truncated tab bar (tab bars truncate from the right, so the title leads and
// the status word trails). Mirrors session.ClampTitle's tab-friendly contract
// at a tighter bound (session titles are 120 runes; a tab title is much less).
const windowTitleRunes = 40

// windowTitle computes the terminal window/tab title for the current phase and
// session. The format is title-first because terminal tab bars truncate from
// the RIGHT, so the title (the most identifying thing) leads and the status
// word + "mecatui" trail:
//
//	<title> — Working mecatui      (phaseRunning)
//	<title> — ⚠ mecatui            (phaseAwaitingApproval)
//	<title> — Connecting mecatui   (phaseConnecting)
//	<title> — ✗ mecatui            (phaseFatal)
//	<title> — mecatui              (phaseIdle / phaseReplay, title known)
//	mecatui                        (no title yet, or opt-out)
//
// The status is a STATIC WORD, never an animated spinner: per-frame title churn
// trips OS attention heuristics (the dock bounces / the taskbar flashes on every
// title change), so a word that changes only on a phase transition is the
// correct granularity. An empty/whitespace title falls back to the bare
// "mecatui" so a fresh session reads as just the app name. NoWindowTitle
// (composition: --terminal-title=off / MECATUI_NO_TERMINAL_TITLE=1) collapses
// the whole thing to "mecatui" — the escape hatch for terminals/multiplexers
// where a set title does more harm than good.
func (m Model) windowTitle() string {
	if m.deps.NoWindowTitle {
		return "mecatui"
	}
	if m.deps.DebugTarget != "" {
		return "DEBUG " + sessionDigest(m.deps.DebugTarget)[:8] + " — " + debugPhaseTitle(m.phase) + " mecatui"
	}
	title := clampWindowTitle(m.sessionTitle)
	status := phaseStatusWord(m.phase)
	switch {
	case title == "" && status == "":
		return "mecatui"
	case title == "":
		return status + " mecatui"
	case status == "":
		return title + " — mecatui"
	default:
		return title + " — " + status + " mecatui"
	}
}

func debugPhaseTitle(p phase) string {
	if status := phaseStatusWord(p); status != "" {
		return status
	}
	return "Ready"
}

// phaseStatusWord maps a phase to the status WORD it contributes to the window
// title. phaseIdle and phaseReplay contribute "" (the title alone reads as
// "just sitting there" — no status word needed); the others carry a single word
// or glyph that survives a right-truncated tab bar. The glyphs (⚠/✗) are plain
// Unicode, not ANSI, so they never risk a terminal-escape interpretation.
func phaseStatusWord(p phase) string {
	switch p {
	case phaseRunning:
		return "Working"
	case phaseAwaitingApproval:
		return "⚠"
	case phaseConnecting:
		return "Connecting"
	case phaseFatal:
		return "✗"
	default:
		// phaseIdle and phaseReplay: a title-known idle/replay session shows just
		// the title + "mecatui" — no status word.
		return ""
	}
}

// clampWindowTitle sanitizes a session title for a one-line terminal title:
// sanitizeTerminal strips C0/ESC/DEL (CWE-150), then strings.Fields collapses
// any surviving newlines/tabs (sanitizeTerminal preserves \n/\t for layout,
// but a window title is a single line) and trims surrounding whitespace. The
// result is clamped to windowTitleRunes via truncate (the shared rune-safe
// ellipsis clamp, view.go). An empty/whitespace-only input yields "" (the
// caller falls back to bare "mecatui"). It mirrors session.ClampTitle's
// contract at a tab-sized bound.
func clampWindowTitle(s string) string {
	s = sanitizeTerminal(s)
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if s == "" {
		return ""
	}
	return truncate(s, windowTitleRunes)
}

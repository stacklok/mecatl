package session

import (
	"strings"
	"unicode/utf8"
)

// This file is the domain home for the "genuine user prompt" predicate and the
// session-title helpers. It exists because engine/session is the domain LEAF and
// CANNOT import engine/agent (where isGenuineUserTurn lived) or engine/prompt
// (where IsInjectedTurn0Fragment lives) — the layering rule pins it to $gostd.
//
// Two read-time consumers of Title — the server layer's lazy ListSessions/
// GetSession fallback (internal/adapter/server) and the event-sourced Fold
// (engine/adapter/eventsource) — can import engine/session but NOT engine/agent,
// so the predicate they need (a genuine user prompt vs a synthesised compaction
// summary) must live HERE, exported. The loop's own isGenuineUserTurn (engine/
// agent/compaction.go) DELEGATES its synthesised-summary arm to
// session.IsSynthesisedSummary and keeps its prompt.IsInjectedTurn0Fragment arm
// (defense-in-depth for legacy/persisted turn-0 fragments; as of ADR 0043 those
// are ephemeral, never persisted, so that arm is not load-bearing for persisted
// history — which is exactly why session.IsGenuineUserPrompt does NOT need it).

// CompactionSummaryMarker prefixes the synthesised paths-summary message BOTH
// compactors emit (a RoleUser message). Tier4SummaryMarker prefixes the cascade's
// tier-4 LLM summary message (also a RoleUser message). BOTH are harness-authored
// context, not real user instructions, so a genuine-user-prompt test (and the
// compaction user-turn back-snap) SKIPS them: a re-compaction must not treat a
// prior compaction's summary as a user turn. These were promoted from the
// unexported engine/agent constants (compactionSummaryMarker / tier4SummaryMarker)
// so the domain leaf owns the genuine-vs-synthesised distinction it needs; the
// agent package keeps the emit sites and references these exported names. Keep
// them byte-for-byte in sync with the literals buildSummary (engine/agent/
// compaction.go) and CascadeCompactor.summarize (engine/agent/cascade.go) emit.
const (
	CompactionSummaryMarker = "[conversation compacted]"
	Tier4SummaryMarker      = "[earlier turns summarised]"
)

// IsSynthesisedSummary reports whether a message's text is a harness-synthesised
// compaction summary (the paths-summary OR the tier-4 LLM summary) rather than a
// genuine user instruction. The back-snap and the title fallback use it to avoid
// anchoring on a prior compaction's own output (the re-compaction footgun). It is
// the domain-leaf export of the predicate that lived unexported in engine/agent
// (isSynthesisedSummary) — promoted so the server/Fold consumers can reach it.
func IsSynthesisedSummary(text string) bool {
	return strings.HasPrefix(text, CompactionSummaryMarker) ||
		strings.HasPrefix(text, Tier4SummaryMarker)
}

// IsGenuineUserPrompt reports whether m is a GENUINE user instruction — the thing
// the title fallback and the event-sourced Fold anchor a session label on — as
// opposed to a harness-authored RoleUser synthesised compaction summary. It is
// m.Role == RoleUser && !IsSynthesisedSummary(m.Text).
//
// It deliberately does NOT check prompt.IsInjectedTurn0Fragment: the domain leaf
// cannot import engine/prompt, and as of ADR 0043 the turn-0 fragments are
// EPHEMERAL (prepended to the request per-run, never persisted into
// Conversation.Messages), so they do not appear in the persisted history the two
// read-time consumers (the lazy fallback + Fold) walk. The loop's OWN
// isGenuineUserTurn (engine/agent/compaction.go) keeps that arm as
// defense-in-depth for legacy history; this domain predicate is for the persisted
// read path and is correct without it.
func IsGenuineUserPrompt(m Message) bool {
	return m.Role == RoleUser && !IsSynthesisedSummary(m.Text)
}

// ClampTitle trims surrounding whitespace and, if the rune count exceeds
// maxTitleRunes, truncates to maxTitleRunes and appends a single "…" ellipsis.
// A short or empty input is returned trimmed (empty stays ""). It is the ONE
// clamp source of truth — SetTitle and the server-layer lazy fallback both call
// it, so a title is always clamped identically regardless of which path seeded
// it. The truncation is rune-safe (counts runes, not bytes).
func ClampTitle(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxTitleRunes {
		return s
	}
	rs := []rune(s)
	return string(rs[:maxTitleRunes]) + "…"
}

package session

import (
	"fmt"
	"slices"
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

// TitleProvenance records who last authored a session title. The zero value is
// legacy/unknown so snapshots written before provenance was introduced fail closed.
type TitleProvenance string

const (
	// TitleProvenanceUnknown means the title's author is unavailable.
	TitleProvenanceUnknown TitleProvenance = ""
	// TitleProvenanceFirstPrompt means the first genuine prompt supplied the title.
	TitleProvenanceFirstPrompt TitleProvenance = "first-prompt"
	// TitleProvenanceOperator means an operator explicitly renamed the session.
	TitleProvenanceOperator TitleProvenance = "operator"
	// TitleProvenanceGenerated means the title came from the configured title generator.
	TitleProvenanceGenerated TitleProvenance = "generated"
)

// TitleGenerationState is the durable lifecycle of automatic title generation.
type TitleGenerationState string

const (
	// TitleGenerationDisabled prevents automatic title generation.
	TitleGenerationDisabled TitleGenerationState = "disabled"
	// TitleGenerationPending awaits a generator attempt.
	TitleGenerationPending TitleGenerationState = "pending"
	// TitleGenerationGenerated records a successful generated title.
	TitleGenerationGenerated TitleGenerationState = "generated"
	// TitleGenerationExhausted records a terminal no-more-attempts state.
	TitleGenerationExhausted TitleGenerationState = "exhausted"
)

// TitleAttemptOutcome records a title-generation attempt's terminal result.
type TitleAttemptOutcome string

const (
	// TitleAttemptSucceeded records a valid generated title.
	TitleAttemptSucceeded TitleAttemptOutcome = "succeeded"
	// TitleAttemptDeferred records a generator decision to await another prompt.
	TitleAttemptDeferred TitleAttemptOutcome = "deferred"
	// TitleAttemptFailed records a known generator failure.
	TitleAttemptFailed TitleAttemptOutcome = "failed"
	// TitleAttemptInterrupted records a crash-interrupted unknown result.
	TitleAttemptInterrupted TitleAttemptOutcome = "interrupted"
)

// TitleAttempt is durable lifecycle metadata for one title-generation attempt.
type TitleAttempt struct {
	ID      string
	Outcome TitleAttemptOutcome
}

// TitlePayload is the bounded source-free projection emitted after a durable
// title lifecycle change. It intentionally contains neither title-source prompts
// nor provider error text.
type TitlePayload struct {
	Title           string
	Provenance      TitleProvenance
	GenerationState TitleGenerationState
	LatestAttempt   *TitleAttempt
	// Revision is the title-specific durable metadata revision. Zero is legacy.
	Revision uint64
}

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
// opposed to a harness-authored RoleUser compaction summary, no-progress nudge,
// or background notice. Empty-text and multimodal user messages remain genuine.
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
	if m.Role != RoleUser || IsSynthesisedSummary(m.Text) {
		return false
	}
	if m.Text == noProgressNudgeText || m.Text == noProgressExtractiveNudgeText {
		return false
	}
	return !isHarnessBackgroundNotice(m.Text)
}

const (
	noProgressNudgeText = "Please continue. Make concrete progress on the task using your tools, " +
		"or — if you are blocked or believe the task is complete — say so explicitly in a short message."
	noProgressExtractiveNudgeText = "Stop investigating now and do not run any " +
		"more commands or tools. Using only the information you have already gathered, " +
		"write your best final answer to the original task as a direct message now, even " +
		"if it is incomplete or uncertain — note any gaps briefly. Do not plan further " +
		"steps; deliver what you have."
)

func isHarnessBackgroundNotice(text string) bool {
	if !strings.HasPrefix(text, "[harness note: ") || !strings.HasSuffix(text, "]") {
		return false
	}
	return strings.Contains(text, " background subagent(s) finished: ") ||
		strings.Contains(text, " background command(s) finished: ") ||
		strings.Contains(text, " background subagent(s) still running: ") ||
		strings.Contains(text, " background command(s) still running: ")
}

// ActivityState is the content-free activity classification of persisted history.
type ActivityState string

const (
	// ActivityUnknown is reserved for unavailable or unproved discovery metadata.
	ActivityUnknown ActivityState = ""
	// ActivityDraft means the history has no genuine user prompt.
	ActivityDraft ActivityState = "draft"
	// ActivityActive means the history has at least one genuine user prompt.
	ActivityActive ActivityState = "active"
)

// ActivityOf classifies successfully decoded conversation history. It is pure and
// deliberately uses the persisted-history genuine-user predicate, so harness-authored
// compaction summaries do not activate an otherwise empty draft.
func ActivityOf(messages []Message) ActivityState {
	for _, message := range messages {
		if IsGenuineUserPrompt(message) {
			return ActivityActive
		}
	}
	return ActivityDraft
}

// ValidActivity clamps a decoded or wire-carried ActivityState to the closed
// {draft, active} set, treating anything else (corrupt, legacy, or a future
// value this build doesn't recognize) as Unknown. It is the ONE fail-closed
// validity rule for the type — storage/driver adapters call it instead of each
// re-deriving the same allow-list check independently.
func ValidActivity(a ActivityState) ActivityState {
	if a == ActivityDraft || a == ActivityActive {
		return a
	}
	return ActivityUnknown
}

// ClampTitle trims and normalizes whitespace, then, if the rune count exceeds
// maxTitleRunes, truncates to maxTitleRunes and appends a single "…" ellipsis.
// A short or empty input is returned trimmed (empty stays ""). It is the ONE
// clamp source of truth — SetTitle and the server-layer lazy fallback both call
// it, so a title is always clamped identically regardless of which path seeded
// it. The truncation is rune-safe (counts runes, not bytes).
func ClampTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxTitleRunes {
		return s
	}
	rs := []rune(s)
	return string(rs[:maxTitleRunes]) + "…"
}

const (
	maxTitleSourcePrompts  = 3
	maxTitleSourceRunes    = 2_000
	maxGeneratedTitleRunes = 80
)

// RecordTitleSourcePrompt captures a bounded principal text prompt at the
// original ingress seam while automatic title generation is pending. Empty
// prompts and prompts beyond the first three are ignored; this method never
// examines conversation history.
func (s *Session) RecordTitleSourcePrompt(text string) {
	if s.TitleGeneration != TitleGenerationPending || !s.recordTitleSourcePrompt(text) {
		return
	}
	s.bumpTitleRevision()
}

func (s *Session) recordTitleSourcePrompt(text string) bool {
	if strings.TrimSpace(text) == "" || len(s.titleSourcePrompts) == maxTitleSourcePrompts {
		return false
	}
	runes := []rune(text)
	if len(runes) > maxTitleSourceRunes {
		text = string(runes[:maxTitleSourceRunes])
	}
	s.titleSourcePrompts = append(s.titleSourcePrompts, text)
	return true
}

// TitleSourcePrompts returns an owned copy of the captured source prompts.
func (s *Session) TitleSourcePrompts() []string {
	return append([]string(nil), s.titleSourcePrompts...)
}

// SetTitleGeneration records durable automatic-title lifecycle intent.
func (s *Session) SetTitleGeneration(state TitleGenerationState) {
	if s.TitleGeneration == state {
		return
	}
	s.TitleGeneration = state
	s.bumpTitleRevision()
}

// ApplyTitleGeneration atomically replaces automatic-title lifecycle metadata.
// It advances the title revision once when either value changes.
func (s *Session) ApplyTitleGeneration(generation TitleGenerationState, attempts []TitleAttempt) {
	if generation == "" {
		generation = TitleGenerationDisabled
	}
	if s.TitleGeneration == generation && equalTitleAttempts(s.titleAttempts, attempts) {
		return
	}
	s.TitleGeneration = generation
	s.titleAttempts = append([]TitleAttempt(nil), attempts...)
	s.bumpTitleRevision()
}

func equalTitleAttempts(a, b []TitleAttempt) bool {
	return slices.Equal(a, b)
}

// RecordTitleAttempt appends durable lifecycle metadata for one attempt.
func (s *Session) RecordTitleAttempt(attempt TitleAttempt) {
	s.ApplyTitleGeneration(s.TitleGeneration, append(s.TitleAttempts(), attempt))
}

// TitleAttempts returns an owned copy of title-generation attempts.
func (s *Session) TitleAttempts() []TitleAttempt {
	return append([]TitleAttempt(nil), s.titleAttempts...)
}

// SetGeneratedTitle replaces the fallback title with a generated title. The
// generated-title limit is intentionally stricter than existing title limits.
func (s *Session) SetGeneratedTitle(text string) error {
	title := clampGeneratedTitle(text)
	if title == "" {
		return fmt.Errorf("session: generated title must not be blank")
	}
	if s.Title == title && s.TitleProvenance == TitleProvenanceGenerated && s.TitleGeneration == TitleGenerationGenerated {
		return nil
	}
	s.Title = title
	s.TitleProvenance = TitleProvenanceGenerated
	s.TitleGeneration = TitleGenerationGenerated
	s.bumpTitleRevision()
	return nil
}

func (s *Session) bumpTitleRevision() {
	if s.TitleRevision != ^uint64(0) {
		s.TitleRevision++
	}
}

func clampGeneratedTitle(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= maxGeneratedTitleRunes {
		return text
	}
	return string(runes[:maxGeneratedTitleRunes-1]) + "…"
}

// RestoreTitleMetadata atomically restores durable title-specific metadata from a
// trusted snapshot or event-source metadata projection. It deliberately does not
// advance TitleRevision: restoration is not a mutation.
func (s *Session) RestoreTitleMetadata(title string, provenance TitleProvenance, revision uint64, generation TitleGenerationState, sources []string, attempts []TitleAttempt) {
	if generation == "" {
		generation = TitleGenerationDisabled
	}
	s.Title = title
	s.TitleProvenance = provenance
	s.TitleRevision = revision
	s.TitleGeneration = generation
	s.titleSourcePrompts = nil
	for _, source := range sources {
		s.recordTitleSourcePrompt(source)
	}
	s.titleAttempts = append([]TitleAttempt(nil), attempts...)
}

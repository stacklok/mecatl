package ui

// This file holds the PURE footer-segment builders for mecatui: token
// humanisation, the cache-hit-rate computation, the context-meter string, and
// the session usage facets. These are deliberately layout-free — the actual
// footer assembly, right-alignment, and narrow-width tiering live in view.go's
// renderFooter. Keeping the segment builders here makes them unit-testable in
// isolation (no Model, no terminal) and keeps renderFooter readable.

import (
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// Context-meter pressure thresholds, as a fraction of the context window. The
// bands echo the corpus guidance that compaction triggers around 70–80% full:
// stay "ok" comfortably below it, "warn" approaching it, "danger" once over.
const (
	ctxWarnFraction   = 0.60
	ctxDangerFraction = 0.85
	ctxBarWidth       = 8 // glyph cells in the meter bar
)

// Per-band fill glyphs. Pressure is encoded in the GLYPH (not just colour) so it
// survives ANSI stripping and is legible to red/green-colourblind users: the
// filled cells grow heavier with pressure — medium shade when ok, dark shade
// when warning, full block in the danger band. The empty glyph is the light
// shade (distinct from every fill glyph, and from the footer's "·" separator).
const (
	ctxGlyphOk     = "▒"
	ctxGlyphWarn   = "▓"
	ctxGlyphDanger = "█"
	ctxGlyphEmpty  = "░" // unfilled cells
)

// ctxDangerMark is appended to the percentage in the danger band — a non-colour
// textual cue so "over budget" reads even with ANSI stripped.
const ctxDangerMark = " ⚠"

// teamLiveGlyph leads the footer team-summary segment when a team is LIVE. It is a
// STATIC literal (the issue mockup's "⟳"), deliberately NOT the animated m.sp
// spinner — the footer team segment is an advertisement, not a per-frame activity
// indicator, so it must not force the model to re-render every tick.
const teamLiveGlyph = "⟳"

// teamFooterIDLimit caps the rune-length of the team id shown in the full footer
// segment so a long id can't blow out the footer width before fitFooter even tiers
// it. Rune-safe via truncate.
const teamFooterIDLimit = 16

// humanizeTokens renders a token count compactly: < 1000 verbatim, thousands as
// "7.9K", millions as "1.2M". One decimal place, trailing ".0" trimmed
// (e.g. 2000 → "2K", 7903 → "7.9K", 1200000 → "1.2M"). Negatives are clamped to
// 0 (token counts are never negative on the wire).
func humanizeTokens(n int64) string {
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

// cacheHitRate computes the session cache-hit rate exactly as
// engine/session/usage.go does: CacheReadTokens / InputTokens, guarded against
// divide-by-zero (→ 0). The result is a fraction in [0,1]. The rate is
// meaningful because the provider adapters normalize CacheReadTokens ⊂
// InputTokens (Anthropic's raw input_tokens excludes cache tokens; its adapter
// folds them in), so the ratio can never exceed 1.
func cacheHitRate(u client.Usage) float64 {
	if u.InputTokens <= 0 {
		return 0
	}
	return float64(u.CacheReadTokens) / float64(u.InputTokens)
}

// pctString formats a fraction in [0,1] as an integer percentage (e.g. 0.881 →
// "88%"). Values are clamped to [0,100].
func pctString(frac float64) string {
	p := int(frac*100 + 0.5)
	if p < 0 {
		p = 0
	}
	if p > 100 {
		p = 100
	}
	return fmt.Sprintf("%d%%", p)
}

// ctxFraction returns the clamped used/window fill fraction in [0,1].
func ctxFraction(used, window int64) float64 {
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

// ctxPressureSlot maps a fill fraction to the themed pressure slot name.
func ctxPressureSlot(frac float64) string {
	switch {
	case frac >= ctxDangerFraction:
		return "ctxDanger"
	case frac >= ctxWarnFraction:
		return slotCtxWarn
	default:
		return "ctxOk"
	}
}

// ctxGlyph returns the per-band fill glyph for a fraction (non-colour pressure).
func ctxGlyph(frac float64) string {
	switch {
	case frac >= ctxDangerFraction:
		return ctxGlyphDanger
	case frac >= ctxWarnFraction:
		return ctxGlyphWarn
	default:
		return ctxGlyphOk
	}
}

// ctxLabel is the bar-less percentage label, with the danger ⚠ cue appended in
// the danger band. Used as the lowest-fidelity context tier ("ctx 92% ⚠").
func ctxLabel(frac float64) string {
	label := pctString(frac)
	if frac >= ctxDangerFraction {
		label += ctxDangerMark
	}
	return label
}

// renderContextMeter renders the FULL-fidelity context segment: a per-band bar +
// percentage (+ ⚠ in danger) + used/total, e.g.
// "ctx ▓▓▓▓▓▓░░ 70% · 140K/200K". With an unknown window (window<=0) it degrades
// to just the current size ("ctx 7.9K"). The bar/percentage carry the
// ctxOk/ctxWarn/ctxDanger colour AND a per-band glyph so pressure is legible
// without colour.
func renderContextMeter(th theme.Theme, used, window int64) string {
	if used < 0 {
		used = 0
	}
	if window <= 0 {
		return "ctx " + humanizeTokens(used)
	}
	frac := ctxFraction(used, window)
	style := th.Style(ctxPressureSlot(frac))
	meter := style.Render(ctxBar(frac) + " " + ctxLabel(frac))
	return "ctx " + meter + " · " + humanizeTokens(used) + "/" + humanizeTokens(window)
}

// renderContextMeterCompact is the mid-fidelity tier: the coloured per-band bar +
// label, WITHOUT the used/total suffix, e.g. "ctx ▓▓▓▓▓▓░░ 70%". Unknown window
// degrades to the bare size, identical to the full tier.
func renderContextMeterCompact(th theme.Theme, used, window int64) string {
	if used < 0 {
		used = 0
	}
	if window <= 0 {
		return "ctx " + humanizeTokens(used)
	}
	frac := ctxFraction(used, window)
	style := th.Style(ctxPressureSlot(frac))
	return "ctx " + style.Render(ctxBar(frac)+" "+ctxLabel(frac))
}

// renderContextMeterMinimal is the lowest-fidelity tier: no bar — just the
// coloured percentage (+ ⚠ in danger), e.g. "ctx 70%". Unknown window degrades
// to the bare size.
func renderContextMeterMinimal(th theme.Theme, used, window int64) string {
	if used < 0 {
		used = 0
	}
	if window <= 0 {
		return "ctx " + humanizeTokens(used)
	}
	frac := ctxFraction(used, window)
	return "ctx " + th.Style(ctxPressureSlot(frac)).Render(ctxLabel(frac))
}

// ctxBar builds the meter bar: filled cells use the per-band glyph, the rest the
// empty glyph. Glyph choice (not just colour) carries the pressure band.
func ctxBar(frac float64) string {
	filled := int(frac*float64(ctxBarWidth) + 0.5)
	if filled > ctxBarWidth {
		filled = ctxBarWidth
	}
	return strings.Repeat(ctxGlyph(frac), filled) + strings.Repeat(ctxGlyphEmpty, ctxBarWidth-filled)
}

// trivialTurnTokens is the per-direction token count below which a turn's
// input/output is considered negligible. A turn whose input AND output are both
// at or under this AND has no measurable duration (sub-second or no clock) is a
// near-empty turn whose stat line is pure noise, so it is suppressed entirely —
// keeping a long multi-turn run scannable.
const trivialTurnTokens = 50

// turnStatCacheFloor is the cache-hit-rate floor below which the per-turn stat
// line omits the "N% cached" facet: a negligible hit rate is noise, while a
// material one is the signal that prompt caching is actually paying off this turn.
const turnStatCacheFloor = 0.10

// turnStatLine formats the inline per-turn stat line shown after a turn's model
// exchange closes. It leads with cost — the turn's input/output tokens
// (humanised) — then the elapsed model-call time, e.g. "↑1.2K ↓340 · 4.1s". The
// duration segment is omitted when the server reported 0ms (no clock), giving
// just "↑1.2K ↓340". A material cache-hit rate (≥ turnStatCacheFloor) appends a
// "· N% cached" facet — the per-turn signal that prompt caching is paying off —
// and is omitted below the floor to keep the line scannable. Users think in cost,
// not turn numbers, so no index is shown.
func turnStatLine(msg client.TurnEndMsg) string {
	var b strings.Builder
	fmt.Fprintf(&b, "↑%s ↓%s",
		humanizeTokens(msg.Usage.InputTokens), humanizeTokens(msg.Usage.OutputTokens))
	if msg.DurationMs > 0 {
		b.WriteString(" · " + formatDuration(msg.DurationMs))
	}
	if rate := cacheHitRate(msg.Usage); rate >= turnStatCacheFloor {
		b.WriteString(" · " + pctString(rate) + " cached")
	}
	return b.String()
}

// trivialTurn reports whether a turn's stats are too negligible to be worth a
// scrollback line: both token directions at or below trivialTurnTokens AND no
// sub-second-or-better duration signal. Such lines are suppressed.
func trivialTurn(msg client.TurnEndMsg) bool {
	return msg.Usage.InputTokens <= trivialTurnTokens &&
		msg.Usage.OutputTokens <= trivialTurnTokens &&
		msg.DurationMs < 1000
}

// formatDuration renders an elapsed millisecond count compactly: under one
// second verbatim in ms ("840ms"), otherwise seconds to one decimal ("4.1s",
// trailing ".0" trimmed → "2s"). Negative values clamp to 0.
func formatDuration(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return trimDecimal(float64(ms)/1000.0) + "s"
}

// stopError is the "error" terminal stop reason (proto Result.stop /
// session.StopError). Named once so the few sites that branch on it — the footer
// label, the subagent label, and the ResultMsg/teardown paths — share one
// spelling rather than scattering the literal.
const stopError = "error"

// The closed team-member stop-reason vocabulary (mirrors the proto
// TeamMemberStopReason / client.reasonString output). Named once so the disposition
// label helper and any future site share one spelling.
const (
	teamStopReasonError     = "error"
	teamStopReasonCancelled = "cancelled"
	teamStopReasonBudget    = "budget"
)

// slotCtxWarn is the themed warning slot name shared by the context-pressure meter
// and the non-error LIMIT stop labels (turn/tool-call/repeated-failure/no-progress).
const slotCtxWarn = "ctxWarn"

// slotMuted is the themed muted slot name for the unobtrusive clean stops (end_turn,
// cancelled, plan_approved, plan_iterate) and the unknown-reason fallback. Named once
// so the literal does not trip goconst's min-occurrences across the switch arms.
const slotMuted = "muted"

// stopReasonLabel maps a run's terminal stop reason (client.ResultMsg.Stop, the
// proto Result.stop / session.StopReason vocabulary) to the human footer status
// text and the theme style slot it should carry. The non-error LIMIT stops
// (turn / tool-call / repeated-failure) are styled with the warning slot
// ("ctxWarn") — they aren't failures but are worth noticing; a clean end_turn is
// the unobtrusive muted "done"; cancelled is muted; error is the error slot (the
// error block already carries the detail). An unknown/empty reason passes through
// sanitized under the muted slot so a new server stop reason is never hidden.
//
// It is a pure function (no Model, no terminal) so it is unit-tested over every
// reason; renderFooter / Update apply the returned slot.
func stopReasonLabel(stop string) (text, slot string) {
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
		// The model went silent (no tool call, no text) across the nudge budget. Not a
		// failure, but worth noticing — styled like the limit stops.
		return "stopped · no progress", slotCtxWarn
	case "structured_output":
		// A subagent could not satisfy the requested output schema within the
		// retry budget. Subagent-only today (it does not reach the main footer),
		// but mapped so the raw token never leaks if it ever does.
		return "stopped · schema unmet", slotCtxWarn
	case "plan_approved":
		return "plan approved · executing", slotMuted
	case "plan_iterate":
		// The operator chose to iterate on the plan (a deny verdict). The run ends
		// cleanly so the operator's next typed prompt drives the revision — a
		// muted/transient "awaiting your feedback" cue, not a warning.
		return "plan iterate · awaiting your feedback", slotMuted
	case stopError:
		return "error", "errorText"
	default:
		return sanitizeTerminal(stop), slotMuted
	}
}

// renderUsageFacets renders the session-total facets surfaced from ALL usage
// fields: input/output token arrows, the previously-dropped cache-WRITE total
// (⊕), and the cache-hit rate. Format: "↑7.9K ↓345 ⊕1.2K cache 88%". The cache
// write is omitted when zero to keep the segment scannable. The ↑/↓/⊕ facets
// are SESSION-CUMULATIVE totals (m.usage); the ctx meter beside them is CURRENT
// occupancy (the latest turn's prompt size) — two different axes, deliberately.
func renderUsageFacets(u client.Usage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "↑%s ↓%s", humanizeTokens(u.InputTokens), humanizeTokens(u.OutputTokens))
	if u.CacheWriteTokens > 0 {
		fmt.Fprintf(&b, " ⊕%s", humanizeTokens(u.CacheWriteTokens))
	}
	fmt.Fprintf(&b, " cache %s", pctString(cacheHitRate(u)))
	return b.String()
}

// teamWorkingCounts classifies a team's member lanes into (working, total). total
// is the lane count; working is the number of lanes NOT idle (genuinely in a turn).
// It reuses the EXACT same !ln.idle predicate that teamLaneState uses for the
// non-terminal roster glyph (◆ working / ○ idle / ✓ done — the last being terminal,
// overlay-only), so the footer's "k/N working" can never disagree with the live
// glyphs in the f6 panel. The footer segment is only rendered for a LIVE team
// (liveTeamBlock returns nil once b.teamDone), so the terminal case never reaches
// here — excluding idle is sufficient.
func teamWorkingCounts(lanes []teamLane) (working, total int) {
	total = len(lanes)
	for i := range lanes {
		if !lanes[i].idle {
			working++
		}
	}
	return working, total
}

// teamFooterFull is the richest footer team-summary tier:
// "⟳ team-<id> · k/N working · <agents> agents". The id is sanitized and rune-safe
// truncated; when it is empty (team.start missed) the id-less medium form is used
// instead of showing a bare "team-". The segment carries the spinner (accent) slot
// so the live team reads as active without animation. agentsMark is the LIVE Agents
// chord (issue #457) so an override propagates to the footer affordance.
func teamFooterFull(th theme.Theme, teamID string, working, total int, agentsMark string) string {
	id := truncate(sanitizeTerminal(teamID), teamFooterIDLimit)
	if id == "" {
		return th.Style("spinner").Render(teamFooterMedium(teamID, working, total, agentsMark))
	}
	seg := fmt.Sprintf("%s %s · %d/%d working · %s agents", teamLiveGlyph, id, working, total, agentsMark)
	return th.Style("spinner").Render(seg)
}

// teamFooterMedium drops the id and the "agents" word: "⟳ k/N working · <agents>".
// It carries no theme styling itself so it composes when called from
// teamFooterFull (which styles the whole segment); fitFooter styles standalone uses.
// agentsMark is the LIVE Agents chord (issue #457).
func teamFooterMedium(_ string, working, total int, agentsMark string) string {
	return fmt.Sprintf("%s %d/%d working · %s", teamLiveGlyph, working, total, agentsMark)
}

// teamFooterCompact is the poorest team tier: "⟳ k/N" — just the glyph + counts.
func teamFooterCompact(working, total int) string {
	return fmt.Sprintf("%s %d/%d", teamLiveGlyph, working, total)
}

// subagentFleetGlyph leads the footer fleet-summary segment when ≥1 subagent has run
// this session. Like teamLiveGlyph it is a STATIC literal (the F2 mockup's gear),
// deliberately NOT the animated spinner — the segment is a peripheral discoverability
// cue, not a per-frame activity indicator, so it must not force a re-render every tick.
const subagentFleetGlyph = "⛭"

// subagentRunGlyph / subagentDoneGlyph are the running / done count markers on the
// fleet footer segment, matching the Subagents-tab roster vocabulary (◐ in flight,
// ✓ finished) so the footer and the overlay never use a different glyph for the same
// state. They are glyph-not-colour cues so they read with ANSI stripped.
const (
	subagentRunGlyph  = "◐"
	subagentDoneGlyph = "✓"
)

// subagentFooterFull is the richest fleet footer tier:
// "⛭ subagents 3◐ 1✓ · <agents>". It is shown whenever ≥1 subagent has STARTED this
// session (running+done > 0), so the parallel case is discoverable even before the
// overlay is opened — the missing "3/4 done" peripheral cue. It carries the spinner
// (accent) slot so the live fleet reads as active without animation. agentsMark is
// the LIVE Agents chord (issue #457) so an override propagates.
func subagentFooterFull(th theme.Theme, running, done int, agentsMark string) string {
	seg := fmt.Sprintf("%s subagents %d%s %d%s · %s", subagentFleetGlyph, running, subagentRunGlyph, done, subagentDoneGlyph, agentsMark)
	return th.Style("spinner").Render(seg)
}

// subagentFooterMedium drops the "subagents" word: "⛭ 3◐ 1✓ · <agents>". It carries no
// theme styling itself so it composes when styled by the caller (view.go).
// agentsMark is the LIVE Agents chord (issue #457).
func subagentFooterMedium(running, done int, agentsMark string) string {
	return fmt.Sprintf("%s %d%s %d%s · %s", subagentFleetGlyph, running, subagentRunGlyph, done, subagentDoneGlyph, agentsMark)
}

// subagentFooterCompact is the poorest fleet tier: "⛭ 3◐ 1✓" — glyph + counts only.
func subagentFooterCompact(running, done int) string {
	return fmt.Sprintf("%s %d%s %d%s", subagentFleetGlyph, running, subagentRunGlyph, done, subagentDoneGlyph)
}

// parallelFleetGlyph leads the footer Parallel-summary segment when ≥1 Parallel run has
// started this session. Like subagentFleetGlyph / teamLiveGlyph it is a STATIC literal (a
// fork glyph distinct from ⛭ subagents and ⟳ team), deliberately NOT the animated spinner.
const parallelFleetGlyph = "⑂"

// parallelFooterFull is the richest Parallel footer tier: "⑂ parallel 1◐ 2✓ · <agents>".
// Like the fleet segment it is shown whenever ≥1 Parallel run has STARTED this session
// (running+done > 0) and reuses the ◐/✓ count vocabulary so footer + overlay agree.
// agentsMark is the LIVE Agents chord (issue #457) so an override propagates.
func parallelFooterFull(th theme.Theme, running, done int, agentsMark string) string {
	seg := fmt.Sprintf("%s parallel %d%s %d%s · %s", parallelFleetGlyph, running, subagentRunGlyph, done, subagentDoneGlyph, agentsMark)
	return th.Style("spinner").Render(seg)
}

// parallelFooterMedium drops the "parallel" word: "⑂ 1◐ 2✓ · <agents>". It carries no theme
// styling itself so it composes when styled by the caller (view.go). agentsMark is the
// LIVE Agents chord (issue #457).
func parallelFooterMedium(running, done int, agentsMark string) string {
	return fmt.Sprintf("%s %d%s %d%s · %s", parallelFleetGlyph, running, subagentRunGlyph, done, subagentDoneGlyph, agentsMark)
}

// parallelFooterCompact is the poorest Parallel tier: "⑂ 1◐ 2✓" — glyph + counts only.
func parallelFooterCompact(running, done int) string {
	return fmt.Sprintf("%s %d%s %d%s", parallelFleetGlyph, running, subagentRunGlyph, done, subagentDoneGlyph)
}

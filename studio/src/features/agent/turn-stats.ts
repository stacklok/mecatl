/**
 * Pure per-turn cost helpers: how `turn.end` frames fold onto an assistant
 * message, the cache-hit rate, the trivial-turn suppression, the inline stat
 * line ("↑1.2k ↓340 · 4.1s · 35% cached"), and the context-pressure bands.
 * Mirrors the TUI footer (cmd/mecatui/ui/footer.go) so both clients read the
 * same numbers the same way.
 */

import { formatDurationMs, formatTokens } from "@/lib/formatters";
import type { TurnStats } from "./types";

/** Per-direction token count at or under which a turn's cost is negligible. */
export const TRIVIAL_TURN_TOKENS = 50;

/** Cache-hit rate under which the stat line omits the "N% cached" facet. */
export const TURN_STAT_CACHE_FLOOR = 0.1;

/** Context-pressure bands as a fraction of the window: ok < warn < danger. */
export const CONTEXT_WARN_FRACTION = 0.6;
export const CONTEXT_DANGER_FRACTION = 0.85;

/** The figures one `turn.end` frame contributes. */
export interface TurnEndFigures {
  durationMs: number;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
}

const nonNegative = (value: number | undefined): number =>
  value !== undefined && Number.isFinite(value) && value > 0 ? value : 0;

/**
 * Folds one turn onto a message's running stats: tokens and duration sum,
 * `turns` increments, and `lastInputTokens` is ASSIGNED (the latest turn's
 * input is the current context occupancy — never a sum).
 */
export function accumulateTurnStats(
  prev: TurnStats | undefined,
  turn: TurnEndFigures,
): TurnStats {
  const input = nonNegative(turn.inputTokens);
  return {
    turns: (prev?.turns ?? 0) + 1,
    inputTokens: (prev?.inputTokens ?? 0) + input,
    outputTokens: (prev?.outputTokens ?? 0) + nonNegative(turn.outputTokens),
    cacheReadTokens:
      (prev?.cacheReadTokens ?? 0) + nonNegative(turn.cacheReadTokens),
    cacheWriteTokens:
      (prev?.cacheWriteTokens ?? 0) + nonNegative(turn.cacheWriteTokens),
    durationMs: (prev?.durationMs ?? 0) + nonNegative(turn.durationMs),
    lastInputTokens: input,
  };
}

/**
 * Cache-hit rate exactly as the engine and the TUI compute it:
 * cacheReadTokens / inputTokens (the adapters normalise cache reads as a
 * SUBSET of input), guarded to 0 when nothing was input and clamped to 1.
 */
export function cacheHitRate(usage: {
  inputTokens: number;
  cacheReadTokens?: number;
}): number {
  const input = nonNegative(usage.inputTokens);
  if (input <= 0) return 0;
  return Math.min(1, nonNegative(usage.cacheReadTokens) / input);
}

/** A fraction in [0, 1] as a clamped integer percentage string ("35%"). */
export function formatPercent(fraction: number): string {
  const percent = Math.round(nonNegative(fraction) * 100);
  return `${Math.min(100, percent)}%`;
}

/**
 * Both token directions at or under the trivial floor AND no measurable
 * (sub-second) duration: a near-empty turn whose stat line is pure noise.
 */
export function isTrivialTurn(stats: TurnStats): boolean {
  return (
    stats.inputTokens <= TRIVIAL_TURN_TOKENS &&
    stats.outputTokens <= TRIVIAL_TURN_TOKENS &&
    stats.durationMs < 1000
  );
}

/**
 * The inline per-turn stat line: cost first (input/output tokens), then the
 * elapsed model-call time (omitted when the daemon reported no clock), then
 * a material cache-hit facet (omitted under the floor).
 */
export function formatTurnStat(stats: TurnStats): string {
  const parts = [
    `↑${formatTokens(stats.inputTokens)} ↓${formatTokens(stats.outputTokens)}`,
  ];
  if (stats.durationMs > 0) parts.push(formatDurationMs(stats.durationMs));
  const rate = cacheHitRate(stats);
  if (rate >= TURN_STAT_CACHE_FLOOR)
    parts.push(`${formatPercent(rate)} cached`);
  return parts.join(" · ");
}

export type ContextBand = "ok" | "warn" | "danger";

/** Maps a context-window fill fraction to its pressure band. */
export function contextBand(fraction: number): ContextBand {
  if (fraction >= CONTEXT_DANGER_FRACTION) return "danger";
  if (fraction >= CONTEXT_WARN_FRACTION) return "warn";
  return "ok";
}

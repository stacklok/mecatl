// SPDX-License-Identifier: Apache-2.0

import type { SessionUsageResponse } from "@mecatl-studio/contracts";

/** Per-direction token count at or under which a turn's cost is negligible. */
export const TRIVIAL_TURN_TOKENS = 50;

/** Cache-hit rate under which the stat line omits the "N% cached" facet. */
export const TURN_STAT_CACHE_FLOOR = 0.1;

/**
 * A model's token count as the compact live-usage label ("1.2k", "3.4M");
 * under 1,000 the exact integer.
 */
export function formatTokens(value: string): string {
  const n = Number(BigInt(value));
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return String(n);
}

/** A duration in milliseconds as "480ms" or "4.1s", dropping a redundant ".0". */
export function formatDurationMs(ms: number): string {
  const clamped = Number.isFinite(ms) && ms > 0 ? ms : 0;
  if (clamped < 1000) return `${Math.round(clamped)}ms`;
  const seconds = (clamped / 1000).toFixed(1).replace(/\.0$/, "");
  return `${seconds}s`;
}

/** cacheReadTokens / inputTokens, clamped to [0, 1]; 0 when there was no input. */
export function cacheHitRate(usage: SessionUsageResponse): number {
  const input = BigInt(usage.inputTokens);
  if (input <= 0n) return 0;
  const rate = Number(BigInt(usage.cacheReadTokens)) / Number(input);
  return Math.min(1, Math.max(0, rate));
}

/** A fraction in [0, 1] as a clamped integer percentage string ("35%"). */
export function formatPercent(fraction: number): string {
  return `${Math.min(100, Math.round(fraction * 100))}%`;
}

/**
 * Both token directions at or under the trivial floor and no measurable
 * duration: a near-empty turn whose stat line would be pure noise.
 */
export function isTrivialTurn(usage: SessionUsageResponse, durationMs: number): boolean {
  return (
    BigInt(usage.inputTokens) <= BigInt(TRIVIAL_TURN_TOKENS) &&
    BigInt(usage.outputTokens) <= BigInt(TRIVIAL_TURN_TOKENS) &&
    durationMs < 500
  );
}

/**
 * The inline per-turn stat line ("↑1.2k ↓340 · 4.1s · 35% cached"), or null
 * for a turn too small to be worth reporting. Mirrors Studio's
 * `formatTurnStat` so both clients read the same numbers the same way.
 */
export function formatTurnStat(usage: SessionUsageResponse, durationMs: number): string | null {
  if (isTrivialTurn(usage, durationMs)) return null;
  const parts = [`↑${formatTokens(usage.inputTokens)} ↓${formatTokens(usage.outputTokens)}`];
  if (durationMs > 0) parts.push(formatDurationMs(durationMs));
  const rate = cacheHitRate(usage);
  if (rate >= TURN_STAT_CACHE_FLOOR) parts.push(`${formatPercent(rate)} cached`);
  return parts.join(" · ");
}

/**
 * The chat ··· menu's "Token usage" rows — one line per non-zero facet, in
 * a fixed order, plus a cache-hit-rate line once anything was read from
 * cache. Mirrors Studio's `UsageMenuRow` facet list and ordering. Empty when
 * the session has spent no input or output tokens yet, so the caller can
 * hide the whole section rather than render a header with nothing under it.
 */
export function usageMenuLines(usage: SessionUsageResponse): string[] {
  if (BigInt(usage.inputTokens) + BigInt(usage.outputTokens) <= 0n) return [];
  const facets: Array<[string, string]> = [
    ["input", usage.inputTokens],
    ["output", usage.outputTokens],
    ["cache read", usage.cacheReadTokens],
    ["cache write", usage.cacheWriteTokens],
    ["reasoning", usage.reasoningTokens],
  ];
  const lines = facets
    .filter(([, count]) => BigInt(count) > 0n)
    .map(([label, count]) => `${formatTokens(count)} ${label}`);
  const rate = cacheHitRate(usage);
  if (rate > 0) lines.push(`${formatPercent(rate)} cache hit rate`);
  return lines;
}

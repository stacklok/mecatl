// SPDX-License-Identifier: Apache-2.0

import type { SessionSummaryResponse } from "@mecatl-studio/contracts";

export interface LatestChatSummary {
  id: string;
  title: string;
  updatedAt: string;
}

/**
 * The most recent chat worth offering a one-click "Continue" for on the
 * draft screen: not a thread-backing session (`excludeIds`, from
 * `useThreadSessionIds()`), and not currently `running`/`awaiting` — a chat
 * busy elsewhere isn't the one to land in by default. Newest `updatedAt`
 * wins; `updatedAt` is ISO-8601 (`Z`-suffixed, zero-padded), so lexical
 * comparison already gives chronological order.
 */
export function pickLatestEligibleChat(
  sessions: readonly SessionSummaryResponse[],
  excludeIds: ReadonlySet<string>,
): LatestChatSummary | undefined {
  let best: SessionSummaryResponse | undefined;
  for (const session of sessions) {
    if (excludeIds.has(session.id)) continue;
    if (session.state === "running" || session.state === "awaiting") continue;
    if (!best || session.updatedAt > best.updatedAt) best = session;
  }
  return best && { id: best.id, title: best.title, updatedAt: best.updatedAt };
}

/** A bare "5m"/"3h"/"2d" duration since `iso`; "" if unparseable. */
export function formatRelativeTime(iso: string): string {
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return "";
  const diffMin = Math.floor((Date.now() - then) / 60_000);
  if (diffMin < 1) return "<1m";
  if (diffMin < 60) return `${diffMin}m`;
  const diffHr = Math.floor(diffMin / 60);
  if (diffHr < 24) return `${diffHr}h`;
  return `${Math.floor(diffHr / 24)}d`;
}

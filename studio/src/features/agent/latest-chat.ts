import { isMockTourSession } from "@/features/agent/mock-tour";
import type { AgentSession } from "@/features/agent/types";

/**
 * The web analogue of mecatui's `--resume-latest`: which chat "the most
 * recent chat" means. Pure over the inventory the sidebar already holds.
 *
 * Eligibility mirrors the TUI's pick (cmd/mecatui: newest owned main-kind
 * chat that is not active/awaiting, else start fresh):
 * - not a thread-backing session (`exclude`, from `useThreadSessionIds()`);
 * - not the Labs mock tour (browser-local demo content, never a daemon row);
 * - not an inspect-only kind (already filtered upstream by `isChat` in
 *   `lib/protocol/sessions.ts`; kept here so the picker owns its own rule);
 * - not an AI-debug session (`debugTargetSessionId`; the TUI takes main-kind
 *   chats only);
 * - not `running` or `awaiting` (a chat busy elsewhere is not the one to
 *   land in by default);
 * - newest `updatedAt` wins.
 *
 * Deliberate deviations from the TUI, documented rather than mirrored: the
 * TUI also skips a row whose authoritative transcript is incomplete and
 * silently tries the next; Studio's open path (`useAgentChat`'s rehydrate)
 * surfaces a transcript failure inline with Retry instead. And a running or
 * awaiting chat stays openable BY HAND (it attaches the ADR 0250 watch) —
 * only the AUTOMATIC pick skips it.
 */
export function pickLatestEligibleChat(
  sessions: readonly AgentSession[],
  exclude: ReadonlySet<string>,
): AgentSession | null {
  let best: AgentSession | null = null;
  for (const session of sessions) {
    if (!isLatestChatEligible(session, exclude)) continue;
    if (!best || (session.updatedAt ?? 0) > (best.updatedAt ?? 0)) {
      best = session;
    }
  }
  return best;
}

/** One row's eligibility for the automatic pick (see `pickLatestEligibleChat`). */
export function isLatestChatEligible(
  session: AgentSession,
  exclude: ReadonlySet<string>,
): boolean {
  if (exclude.has(session.id)) return false;
  if (isMockTourSession(session.id)) return false;
  if (session.isChat === false) return false;
  if (session.debugTargetSessionId) return false;
  if (session.state === "running" || session.state === "awaiting") {
    return false;
  }
  return true;
}

/**
 * The "the user asked for a draft" mark. "New chat" from an open chat moves
 * the optional catch-all route from one segment to zero, which REMOUNTS the
 * chat workspace; the remounted launch effect would otherwise re-open the
 * most recent chat at once under the "Most recent chat" preference. The mark
 * rides sessionStorage (this tab only) so it survives the remount, and it
 * carries its write time so a stale mark — a "New chat" that did NOT remount
 * (the App Router syncs from native history without remounting on some
 * paths) — can never suppress a genuine landing minutes later.
 */
export const EXPLICIT_DRAFT_KEY = "mecatl-studio.chat.explicit-draft";

/** How long a "New chat" mark stays honoured (a remount is milliseconds). */
export const EXPLICIT_DRAFT_TTL_MS = 10_000;

export function markExplicitDraft(now: number = Date.now()): void {
  if (typeof window === "undefined") return;
  try {
    window.sessionStorage.setItem(EXPLICIT_DRAFT_KEY, String(now));
  } catch {
    // Storage disabled — the in-component guard still covers the same
    // mount; only a remount within the TTL loses the mark.
  }
}

/**
 * Reads AND clears the mark. True only for a fresh mark (within the TTL);
 * a stale or malformed one is discarded and reads as "no request". Called
 * unconditionally on mount — never only under one preference — so a mark
 * written under "New chat" (default preference) cannot linger and eat the
 * first landing after the user switches to "Most recent chat".
 */
export function consumeExplicitDraft(now: number = Date.now()): boolean {
  if (typeof window === "undefined") return false;
  try {
    const raw = window.sessionStorage.getItem(EXPLICIT_DRAFT_KEY);
    if (raw === null) return false;
    window.sessionStorage.removeItem(EXPLICIT_DRAFT_KEY);
    const at = Number(raw);
    return (
      Number.isFinite(at) && now - at >= 0 && now - at < EXPLICIT_DRAFT_TTL_MS
    );
  } catch {
    return false;
  }
}

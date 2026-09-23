// SPDX-License-Identifier: Apache-2.0

import { useEffect, useRef } from "react";
import type { StartOn } from "../../lib/profile-preferences";

/**
 * The launch half of the "Start on" preference (Settings → Appearance): on
 * a bare draft-route landing, which chat (if any) to open instead of the
 * draft greeting.
 *
 * - A chat is already selected (deep link, a click) — not a draft landing.
 * - The preference is "draft" — no auto-open.
 * - The session inventory hasn't loaded once — wait (undefined, not "no
 *   pick"; the caller must not settle on this answer).
 * - The draft already holds work (an arrival seed) — never yank it away.
 * - Otherwise, the eligible pick, or undefined for "start fresh".
 */
export function pickAutoOpenTarget({
  hold,
  latestChatId,
  loading,
  sessionId,
  startOn,
}: {
  hold: boolean;
  latestChatId: string | undefined;
  loading: boolean;
  sessionId: string | undefined;
  startOn: StartOn;
}): string | undefined {
  if (sessionId || startOn !== "latest" || loading) return undefined;
  return hold ? undefined : latestChatId;
}

/**
 * Applies {@link pickAutoOpenTarget} exactly once per mount, as a landing
 * decision rather than a standing rule — re-navigating every time the
 * inputs happen to satisfy it again would fight the user's own navigation.
 * `requestDraft` is for "New chat" in the same mount: it settles the
 * decision immediately so an explicit draft is never reopened onto the
 * latest chat right after the click.
 */
export function useLatestChatAutoOpen(input: {
  hold: boolean;
  latestChatId: string | undefined;
  loading: boolean;
  onOpen: (id: string) => void;
  sessionId: string | undefined;
  startOn: StartOn;
}): { requestDraft: () => void } {
  const settledRef = useRef(false);
  const { onOpen, sessionId, startOn, loading } = input;

  // biome-ignore lint/correctness/useExhaustiveDependencies: input's other fields (hold, latestChatId) are read once settled, not tracked as dependencies — re-running on them would re-evaluate a decision this hook deliberately makes only once
  useEffect(() => {
    if (settledRef.current || sessionId || startOn !== "latest" || loading) return;
    settledRef.current = true;
    const target = pickAutoOpenTarget(input);
    if (target) onOpen(target);
  }, [sessionId, startOn, loading, onOpen]);

  return {
    requestDraft: () => {
      settledRef.current = true;
    },
  };
}

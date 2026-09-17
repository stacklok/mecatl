"use client";

import { useCallback, useEffect, useRef } from "react";
import {
  consumeExplicitDraft,
  markExplicitDraft,
} from "@/features/agent/latest-chat";
import type { LaunchTarget } from "@/lib/profile-preferences";

/**
 * The launch half of mecatui's `--resume-latest`: when the user lands on the
 * bare draft route with the "Most recent chat" preference, open the newest
 * eligible chat instead — ONCE per mount, as a landing decision, never as a
 * standing rule. The caller owns what "open" does (a state update plus a
 * native `replaceState`, so Back returns to wherever the user came from and
 * never bounces to the draft).
 *
 * Decision order, evaluated on every input change until it settles:
 * 1. A chat is already selected (deep link, a minted draft, a click) — this
 *    mount is not a draft landing; nothing to do, ever.
 * 2. The preference is "draft" — nothing to do (and NOT settled: the
 *    preference hydrates from localStorage after the first frame).
 * 3. Not connected, or the inventory has not loaded once — wait. A fresh tab
 *    whose inventory lands before the 5 s daemon probe flips `connected`
 *    waits for the probe, so the auto-open can lag up to one probe interval.
 * 4. Settled. Stay on the draft when the user asked for one ("New chat"
 *    marked it — consumed on mount whatever the preference, see
 *    `consumeExplicitDraft`), when the draft already holds work (an arrival
 *    prompt, a picked starter, typed text — never yank the user off it), or
 *    when nothing is eligible (the TUI's "else start fresh"). Otherwise open.
 *
 * `requestDraft` is for "New chat" in the SAME mount: it settles the decision
 * here (the no-remount path) and writes the mark for a remount (the route
 * shape changes between zero and one segments, which remounts the page).
 */
export function useLatestChatAutoOpen({
  selectedId,
  launchTarget,
  connected,
  sessionsLoading,
  hold,
  latestChatId,
  onOpen,
}: {
  /** The current selection ("" on a draft). */
  selectedId: string;
  launchTarget: LaunchTarget;
  connected: boolean;
  /** True until the inventory has loaded once. */
  sessionsLoading: boolean;
  /** The draft already holds work: an arrival prompt, a seed, typed text. */
  hold: boolean;
  /** The eligible pick, or null for "start fresh". */
  latestChatId: string | null;
  onOpen: (id: string) => void;
}) {
  const settledRef = useRef(false);
  const explicitDraftRef = useRef(false);
  // Consume the mark on mount unconditionally — under EITHER preference — so
  // a "New chat" made under the default preference can never leave a mark
  // behind that eats the first landing after a switch to "Most recent chat".
  useEffect(() => {
    explicitDraftRef.current = consumeExplicitDraft();
  }, []);

  useEffect(() => {
    if (settledRef.current) return;
    if (selectedId) return;
    if (launchTarget !== "latest") return;
    if (!connected || sessionsLoading) return;
    settledRef.current = true;
    if (explicitDraftRef.current || hold || !latestChatId) return;
    onOpen(latestChatId);
  }, [
    selectedId,
    launchTarget,
    connected,
    sessionsLoading,
    hold,
    latestChatId,
    onOpen,
  ]);

  const requestDraft = useCallback(() => {
    settledRef.current = true;
    markExplicitDraft();
  }, []);

  return { requestDraft };
}

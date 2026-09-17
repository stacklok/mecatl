"use client";

import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import {
  AWAY_NOTICE_DURATION_MS,
  AWAY_NOTICE_TOAST_ID,
  composeAwayNotice,
  MIN_AWAY_MS,
} from "../away-notice";
import type { ChatPhase } from "../chat-phase";

export interface UseAwayNoticeInput {
  /** The open chat's phase (the same fold the tab title reads). */
  phase: ChatPhase;
  /** The open chat's title (or the draft's "New chat"). */
  chatTitle: string;
  /** The daemon is reachable. */
  connected: boolean;
  /**
   * Forces the daemon probe and the inventory refetch before the notice is
   * composed. A hidden tab's timers are throttled to about once a minute, so
   * on return the 5 s runtime probe and the 20 s inventory poll can both be
   * a minute stale — composed straight off them the line could say "kept
   * running and finished" about a run that is still going, or "went offline"
   * about a daemon that is back. Awaited, best-effort: a failure composes
   * from whatever state the refresh did reach.
   */
  refresh?: () => Promise<unknown>;
  /** Off switches the listeners off entirely. Defaults to on. */
  enabled?: boolean;
  /** Override for the absence cutoff (tests); defaults to MIN_AWAY_MS. */
  minAwayMs?: number;
}

/** What was true when the tab went hidden. */
interface AwaySnapshot {
  at: number;
  phase: ChatPhase;
  title: string;
  connected: boolean;
}

/** A return whose refresh has settled; the notice composes on this render. */
interface SettledReturn {
  snapshot: AwaySnapshot;
  returnedAt: number;
}

/**
 * Shows the "while you were away" resume notice — mecatui's `resumeNotice`
 * for the browser. The tab going hidden (`visibilitychange`, the device
 * sleeping, a bfcache freeze) records the chat's phase and title; the tab
 * coming back (`visibilitychange` to visible, or a bfcache `pageshow` with
 * `persisted`) after at least MIN_AWAY_MS refreshes the daemon state, then
 * composes ONE line from the phase at leaving and the phase now, as a
 * transient toast. The transcript needs no help: the SSE watch stays
 * attached in the background and has already caught up — the toast is the
 * explicit line saying so and naming what was in flight.
 *
 * The composition is deferred to a render AFTER the refresh settles (a
 * settled-return state feeds a second effect), never done synchronously in
 * the visibility handler: only then do `phase` and `connected` reflect the
 * refreshed daemon state. A tab hidden again before that settles drops the
 * pending return (a generation counter), so a stale line never lands on a
 * later visit.
 */
export function useAwayNotice(input: UseAwayNoticeInput): void {
  const latest = useRef(input);
  latest.current = input;
  const snapshotRef = useRef<AwaySnapshot | null>(null);
  const generationRef = useRef(0);
  const [settled, setSettled] = useState<SettledReturn | null>(null);
  const enabled = input.enabled ?? true;

  useEffect(() => {
    if (!enabled) return;
    let mounted = true;

    const onHidden = () => {
      generationRef.current += 1;
      const current = latest.current;
      snapshotRef.current = {
        at: Date.now(),
        phase: current.phase,
        title: current.chatTitle,
        connected: current.connected,
      };
    };

    const onVisible = () => {
      const snapshot = snapshotRef.current;
      snapshotRef.current = null;
      if (!snapshot) return;
      const returnedAt = Date.now();
      const minAwayMs = latest.current.minAwayMs ?? MIN_AWAY_MS;
      // A quick alt-tab: nothing to say, and no refresh worth forcing.
      if (returnedAt - snapshot.at < minAwayMs) return;
      const generation = generationRef.current;
      const settle = () => {
        if (!mounted || generationRef.current !== generation) return;
        setSettled({ snapshot, returnedAt });
      };
      const refresh = latest.current.refresh;
      if (!refresh) {
        settle();
        return;
      }
      let pending: Promise<unknown>;
      try {
        pending = Promise.resolve(refresh());
      } catch {
        settle();
        return;
      }
      void pending.then(settle, settle);
    };

    const onVisibilityChange = () => {
      if (document.visibilityState === "hidden") onHidden();
      else onVisible();
    };
    const onPageShow = (event: PageTransitionEvent) => {
      // A bfcache restore: the page was frozen, not re-created, so the
      // hidden snapshot is still here and this is the return.
      if (event.persisted) onVisible();
    };

    document.addEventListener("visibilitychange", onVisibilityChange);
    window.addEventListener("pageshow", onPageShow);
    return () => {
      mounted = false;
      generationRef.current += 1;
      snapshotRef.current = null;
      document.removeEventListener("visibilitychange", onVisibilityChange);
      window.removeEventListener("pageshow", onPageShow);
    };
  }, [enabled]);

  useEffect(() => {
    if (!settled) return;
    // This render carries the refreshed phase/connection: the refresh's own
    // state updates were scheduled before the settle, so they are in it.
    const current = latest.current;
    const message = composeAwayNotice({
      leftPhase: settled.snapshot.phase,
      returnedPhase: current.phase,
      chatTitle: settled.snapshot.title,
      awayMs: settled.returnedAt - settled.snapshot.at,
      connected: current.connected,
      leftConnected: settled.snapshot.connected,
      minAwayMs: current.minAwayMs,
    });
    setSettled(null);
    if (message !== null) {
      toast.info(message, {
        id: AWAY_NOTICE_TOAST_ID,
        duration: AWAY_NOTICE_DURATION_MS,
      });
    }
  }, [settled]);
}

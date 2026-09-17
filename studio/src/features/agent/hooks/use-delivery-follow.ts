"use client";

import { useEffect, useRef } from "react";
import { fetchSessionTranscriptMessages } from "@/lib/harness/client";
import { transcriptHasUnseenDelivery } from "../delivery-message";
import {
  newCompletedDeliveries,
  notifyDelivery,
} from "../delivery-notifications";
import { isMockTourSession } from "../mock-tour";
import type { AgentMessage } from "../types";

export interface DeliveryFollowInput {
  /** The open chat's daemon session id (null for a draft / the mock tour). */
  sessionId: string | null;
  /** The inventory row's last-write time (epoch millis) from the 20 s poll. */
  updatedAt: number | undefined;
  /** The inventory row's lifecycle state (running/awaiting attach the watch). */
  state: string | undefined;
  /** This tab is driving (or parked on) a run of its own. */
  isStreaming: boolean;
  /** The daemon is reachable. */
  connected: boolean;
  messages: AgentMessage[];
  /** `useAgentChat`'s authoritative transcript re-read. */
  refreshTranscript: () => Promise<void>;
}

/**
 * Picks up scheduled-task delivery notes that land while the chat is open.
 *
 * A fire's delivery drives a real run on the ORIGIN chat. A long one is
 * caught by the durable watch (the inventory poll reports running/awaiting
 * and `useAgentChat` attaches; the replayed `user_prompt` is the note). A
 * SHORT one that starts and ends between two polls only bumps the row's
 * `updatedAt` — so when that advances while nothing is running here, the
 * transcript is read and, ONLY if it carries a note this list lacks, the
 * chat re-reads it (`refreshTranscript`). The guard matters: a re-read
 * replaces this visit's rich live messages (reasoning, artifacts, the
 * failed-turn rendering) with the transcript projection, so it must never
 * follow this tab's own run — after a local run the transcript has no
 * unseen note and nothing is replaced.
 *
 * Out of scope here, deliberately: a persistent follow-watch on an idle chat
 * (that would restructure the chat's rehydrate/watch ownership), and the
 * `schedule.fired/skipped/failed` kinds, which are fire-session lifecycle
 * markers, not the note.
 *
 * The second half notifies a HIDDEN tab of each completed note that newly
 * appeared (the production trigger behind the appearance page's test
 * button). The first non-empty render of a chat seeds the baseline, so
 * opening a chat never re-notifies for its history.
 */
export function useDeliveryFollow(input: DeliveryFollowInput): void {
  const {
    sessionId,
    updatedAt,
    state,
    isStreaming,
    connected,
    messages,
    refreshTranscript,
  } = input;

  const messagesRef = useRef(messages);
  messagesRef.current = messages;
  const refreshRef = useRef(refreshTranscript);
  refreshRef.current = refreshTranscript;
  const lastSeenRef = useRef<{ id: string | null; updatedAt: number }>({
    id: null,
    updatedAt: 0,
  });

  useEffect(() => {
    const stamp = updatedAt ?? 0;
    const seen = lastSeenRef.current;
    if (seen.id !== sessionId) {
      // A switch: the row's current stamp is the baseline, never a trigger.
      lastSeenRef.current = { id: sessionId, updatedAt: stamp };
      return;
    }
    if (isStreaming || state === "running" || state === "awaiting") {
      // A run in flight (ours, or one the watch renders) writes the row on
      // every save; those advances are that run's, not a missed delivery.
      seen.updatedAt = Math.max(seen.updatedAt, stamp);
      return;
    }
    if (stamp <= seen.updatedAt) return;
    seen.updatedAt = stamp;
    if (!sessionId || !connected || isMockTourSession(sessionId)) return;
    // Nothing rendered yet: the open rehydrate (or the watch replay) owns
    // the first read; a second fetch here would only race it.
    if (messagesRef.current.length === 0) return;
    let cancelled = false;
    void (async () => {
      try {
        const transcript = await fetchSessionTranscriptMessages(sessionId);
        if (cancelled) return;
        if (!transcriptHasUnseenDelivery(transcript, messagesRef.current)) {
          return;
        }
        await refreshRef.current();
      } catch {
        // Best-effort: the note still appears on the next open.
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [sessionId, updatedAt, state, isStreaming, connected]);

  const previousRef = useRef<AgentMessage[]>([]);
  useEffect(() => {
    const previous = previousRef.current;
    previousRef.current = messages;
    // An empty baseline is a chat being (re)opened: its first render is
    // history, never news.
    if (previous.length === 0) return;
    for (const info of newCompletedDeliveries(previous, messages)) {
      notifyDelivery(info);
    }
  }, [messages]);
}

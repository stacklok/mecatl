"use client";

import { useCallback, useEffect, useRef, useState } from "react";

/**
 * The debug session's auto-submitted opening objective (ADR 0254 — the TUI
 * submits `defaultDebugPrompt` the moment its debug chat opens; the daemon's
 * create path starts no run, so the client owns this in both clients).
 *
 * `arm(sessionId, text)` parks ONE message for ONE session. The message is
 * sent exactly once, the first time the chat hook is keyed to that session,
 * the daemon is live and the hook reads idle — i.e. after the workspace has
 * selected the new debug chat and the hook has re-keyed onto it (its re-key
 * effect resets status to idle). Sending against any OTHER session is
 * impossible by construction: the id must match, and a null id (a draft) is
 * never a match, so the hook can never mint a session for it.
 *
 * Dropping is as important as firing: once the chat hook has been seen on
 * the armed session, moving to a different chat before the send fired
 * discards the message (a stale objective must not fire when the user comes
 * back later), and a fresh `arm` replaces whatever was pending. Nothing here
 * survives an unmount — the state dies with the workspace.
 */
export interface DebugOpeningPromptDeps {
  /** The chat hook's current session id (null for a draft / the mock tour). */
  sessionId: string | null;
  /** Whether the daemon is reachable (`harnessLive`). */
  live: boolean;
  /** The chat hook's run status; only "idle" admits the send. */
  status: string;
  /** The chat hook's send — `sendMessage(text)`. */
  sendMessage: (text: string) => Promise<void> | void;
}

export interface DebugOpeningPrompt {
  /** Parks `text` to be sent once into `sessionId`; replaces any pending one. */
  arm: (sessionId: string, text: string) => void;
  /** Drops the pending message, if any (a cancelled debug create). */
  cancel: () => void;
  /** A message is parked and has not been sent or dropped yet. */
  pending: boolean;
}

interface Pending {
  sessionId: string;
  text: string;
}

export function useDebugOpeningPrompt({
  sessionId,
  live,
  status,
  sendMessage,
}: DebugOpeningPromptDeps): DebugOpeningPrompt {
  const [pending, setPending] = useState<Pending | null>(null);
  // The hook has been observed keyed to the pending session at least once.
  const matchedRef = useRef(false);
  // The pending value already handed to sendMessage: a commit that still
  // carries it (the send's own status flip landing before the clear) must
  // not send it again.
  const firedRef = useRef<Pending | null>(null);
  // The latest send, read at fire time so the effect never re-runs (and
  // never re-fires) because a caller re-created its callback.
  const sendRef = useRef(sendMessage);
  sendRef.current = sendMessage;

  useEffect(() => {
    if (!pending || firedRef.current === pending) return;
    if (sessionId === pending.sessionId) {
      matchedRef.current = true;
      if (!live || status !== "idle") return;
      firedRef.current = pending;
      setPending(null);
      void sendRef.current(pending.text);
      return;
    }
    // Keyed to another chat. Before the selection landed that is the
    // in-flight target → debug switch and the message waits; after it has
    // landed once, it is the user leaving, and the objective is dropped.
    if (matchedRef.current && sessionId !== null) setPending(null);
  }, [pending, sessionId, live, status]);

  const arm = useCallback((id: string, text: string) => {
    matchedRef.current = false;
    setPending({ sessionId: id, text });
  }, []);
  const cancel = useCallback(() => {
    setPending(null);
  }, []);

  return { arm, cancel, pending: pending !== null };
}

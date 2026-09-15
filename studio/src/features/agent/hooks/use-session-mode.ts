"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  fetchHarnessSessionMode,
  setHarnessSessionMode,
} from "@/lib/harness/client";
import type { SessionPermissionMode } from "@/lib/protocol";
import { useRuntimeStatus } from "../runtime-status";

/**
 * The permission mode for one chat's composer Mode selector.
 *
 * Two shapes, one seam:
 * - A LIVE session (`sessionId` set): the mode is read from the session
 *   snapshot on open, and a change round-trips through POST /mode, adopting
 *   the daemon's echo (and rolling back when the daemon refuses — e.g. the
 *   aggregate rejects a mid-turn change).
 * - A DRAFT (`sessionId` null): the selection is held as pending local state;
 *   the caller reads it via `modeRef` at session-creation time and passes it
 *   into the create body.
 */
export function useSessionMode(sessionId: string | null) {
  const { connected } = useRuntimeStatus();
  const [mode, setMode] = useState<SessionPermissionMode>("default");
  // Mirror for callers that need the value at an async boundary (the chat
  // hook mints the draft's session mid-send, after this render's closure).
  const modeRef = useRef(mode);
  modeRef.current = mode;

  useEffect(() => {
    // Opening a chat (or going back to the draft) resets, then adopts the
    // snapshot's mode. A just-minted draft re-fetches its own creation mode —
    // momentarily "default", then self-consistent.
    setMode("default");
    if (!sessionId || !connected) return;
    const controller = new AbortController();
    void (async () => {
      try {
        const fetched = await fetchHarnessSessionMode(
          sessionId,
          controller.signal,
        );
        if (!controller.signal.aborted) setMode(fetched);
      } catch {
        // Unknown snapshot mode: keep the default label. Changing the mode
        // still round-trips normally.
      }
    })();
    return () => controller.abort();
  }, [sessionId, connected]);

  const changeMode = useCallback(
    (next: SessionPermissionMode) => {
      if (!sessionId) {
        // Draft: held pending; applied when the session is created.
        setMode(next);
        return;
      }
      const previous = modeRef.current;
      setMode(next); // optimistic; the echo or the rollback settles it
      void (async () => {
        try {
          const echoed = await setHarnessSessionMode(sessionId, next);
          setMode(echoed);
        } catch {
          // The daemon refused (mid-turn, or the session vanished): the
          // selector snaps back rather than lying about the posture.
          setMode(previous);
        }
      })();
    },
    [sessionId],
  );

  return { mode, modeRef, changeMode };
}

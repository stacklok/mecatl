"use client";

import { useCallback, useRef, useState } from "react";
import { toast } from "sonner";
import { HarnessApiError } from "@/lib/harness/errors";
import { isUnsupportedByDaemon } from "@/lib/harness/sdk";
import {
  clearHarnessSession,
  ThreadSourceBusyError,
} from "@/lib/harness/sessions";
import {
  CLEAR_SOURCE_BUSY,
  CLEAR_UNSUPPORTED,
  CLEARED_TOAST,
  NOTHING_TO_CLEAR,
} from "./clear-conversation";

/** The chat-workspace state the clear handoff acts on. */
export interface ClearConversationDeps {
  /** The selected daemon session id; null for a draft or the mock chat. */
  sessionId: string | null;
  /** Drops the composer's held queue: a cleared chat carries no backlog. */
  onClearQueue: () => void;
  /** Adopts the successor once the daemon answered: refresh the list, move
   *  the UI there. Runs BEFORE the composer is re-enabled. */
  onSessionCleared: (successorId: string) => void | Promise<void>;
}

/**
 * Clear conversation — the TUI's `/clear` handoff (cmd/mecatui/ui/update.go
 * `blockClearPendingInput` → `clearSessionReadyMsg`), for the browser.
 *
 * One call does the whole handoff: the composer is blocked (`clearing`)
 * while the ClearSession RPC is in flight, the daemon CANCELS a running or
 * awaiting source itself and waits for it to settle before minting the
 * empty-history successor (internal/adapter/server/placement_successor.go),
 * so no client-side cancel-then-poll is needed and this tab's own stream
 * simply ends on the cancelled result. The scrollback switches ONLY after
 * the daemon answered with the successor id; on a failure the source stays
 * selected and the toast offers Retry (the TUI's "retry /clear" footer).
 */
export function useClearConversation(deps: ClearConversationDeps): {
  /** True from the request until the successor is selected (or it failed). */
  clearing: boolean;
  /** Runs the handoff; a second call while one is pending is a no-op. */
  clearConversation: () => Promise<void>;
} {
  const depsRef = useRef(deps);
  depsRef.current = deps;
  const inFlightRef = useRef(false);
  const [clearing, setClearing] = useState(false);

  const clearConversation = useCallback(async (): Promise<void> => {
    const d = depsRef.current;
    if (inFlightRef.current) return;
    if (!d.sessionId) {
      toast.info(NOTHING_TO_CLEAR);
      return;
    }
    const sourceId = d.sessionId;
    inFlightRef.current = true;
    setClearing(true);
    try {
      const successorId = await clearHarnessSession(sourceId);
      d.onClearQueue();
      await d.onSessionCleared(successorId);
      toast.success(CLEARED_TOAST);
    } catch (caught) {
      // The source stays selected; every failure but "this daemon has no
      // clear" offers the TUI's retry as a toast action.
      if (isUnsupportedByDaemon(caught)) {
        toast.error(CLEAR_UNSUPPORTED);
        return;
      }
      const busy =
        caught instanceof ThreadSourceBusyError ||
        (caught instanceof HarnessApiError && caught.status === 412);
      toast.error(
        busy
          ? CLEAR_SOURCE_BUSY
          : caught instanceof Error
            ? caught.message
            : String(caught),
        { action: { label: "Retry", onClick: () => void clearConversation() } },
      );
    } finally {
      inFlightRef.current = false;
      setClearing(false);
    }
  }, []);

  return { clearing, clearConversation };
}

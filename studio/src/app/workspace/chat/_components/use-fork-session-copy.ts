"use client";

import { useCallback, useRef } from "react";
import { toast } from "sonner";
import {
  forkHarnessSessionCopy,
  ThreadSourceBusyError,
} from "@/lib/harness/client";

/** The success toast once the UI has moved to the copy. */
export const FORKED_TOAST = "Forked — continuing in a copy of this chat";
/** A mid-run source (412) is remedied by waiting, not retrying. */
export const FORK_SOURCE_BUSY =
  "Wait for the current response to finish, then fork.";

/**
 * Fork-as-is (the TUI's `f` on /sessions): mints a copy of a chat on the
 * same model, provider and placement — no model, effort or worktree change —
 * titled "<title> (copy)", then hands the new id to `onForked` (the caller
 * refreshes the inventory and moves the UI there) and toasts. The source
 * chat stays in the list; nothing is destroyed. A running/awaiting source
 * answers 412, which reads as "wait", while every other failure offers the
 * TUI's enter-to-retry as a toast action. Returns a stable callback.
 */
export function useForkSessionCopy({
  onForked,
}: {
  onForked: (newId: string) => Promise<void> | void;
}): (sourceId: string, title: string) => Promise<void> {
  const onForkedRef = useRef(onForked);
  onForkedRef.current = onForked;
  return useCallback(async (sourceId: string, title: string) => {
    const attempt = async (): Promise<void> => {
      try {
        const newId = await forkHarnessSessionCopy(sourceId, title);
        await onForkedRef.current(newId);
        toast.success(FORKED_TOAST);
      } catch (caught) {
        if (caught instanceof ThreadSourceBusyError) {
          toast.error(FORK_SOURCE_BUSY);
          return;
        }
        toast.error(caught instanceof Error ? caught.message : String(caught), {
          action: { label: "Retry", onClick: () => void attempt() },
        });
      }
    };
    await attempt();
  }, []);
}

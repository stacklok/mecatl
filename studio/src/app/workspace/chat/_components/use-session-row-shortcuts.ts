"use client";

import { toast } from "sonner";
import type { AgentSession } from "@/features/agent";
import { isMockTourSession } from "@/features/agent/mock-tour";
import { copyToClipboard } from "@/lib/clipboard";
import { capabilityReasonLabel } from "@/lib/session-kinds";
import { useShortcut } from "@/lib/shortcuts/use-shortcuts";
import { COPY_ID_NOT_OFFERED } from "./session-copy-menu-items";
import { forkGate, forkOffered } from "./session-row-action-items";

/** Toasted when the copy-ID key fires with no daemon chat open. */
export const NO_CHAT_TO_COPY = "Open a chat to copy its session ID";
/** Toasted when the fork key fires with no daemon chat open. */
export const NO_CHAT_TO_FORK = "Open a chat to fork it";
/** Toasted when the fork key fires on a chat Fork is not offered for. */
export const FORK_NOT_HERE = "This chat cannot be forked from here";

/** What a shortcut press does: act, or explain why not (a toast). */
export type ShortcutOutcome =
  | { kind: "act" }
  | { kind: "info"; message: string };

/**
 * The copy-ID key (`chat.copyId`, the TUI's `c` on /session): acts on a real
 * open chat the daemon lets copy; otherwise says why in the daemon's words,
 * the same verdict the row menu's disabled item shows.
 */
export function copyIdShortcutOutcome(
  session: AgentSession | undefined,
): ShortcutOutcome {
  if (!session || isMockTourSession(session.id)) {
    return { kind: "info", message: NO_CHAT_TO_COPY };
  }
  if (session.canCopyId !== true) {
    return {
      kind: "info",
      message:
        capabilityReasonLabel(session.copyIdReason) || COPY_ID_NOT_OFFERED,
    };
  }
  return { kind: "act" };
}

/**
 * The fork key (`chat.fork`, the TUI's `f` on /sessions): acts on a real open
 * chat Fork is offered for and the daemon would fork; otherwise the same
 * reason the menu item carries.
 */
export function forkShortcutOutcome(
  session: AgentSession | undefined,
): ShortcutOutcome {
  if (!session || isMockTourSession(session.id)) {
    return { kind: "info", message: NO_CHAT_TO_FORK };
  }
  if (!forkOffered(session)) return { kind: "info", message: FORK_NOT_HERE };
  const gate = forkGate(session);
  if (gate.disabled) return { kind: "info", message: gate.reason };
  return { kind: "act" };
}

/**
 * Registers the two per-chat keys for the OPEN chat. A refusal has no
 * composer to warn in, so it toasts (the `chat.clear` precedent).
 */
export function useSessionRowShortcuts({
  session,
  onFork,
}: {
  /** The selected daemon session (undefined on a draft). */
  session: AgentSession | undefined;
  onFork: (sessionId: string) => void;
}): void {
  useShortcut("chat.copyId", () => {
    const outcome = copyIdShortcutOutcome(session);
    if (outcome.kind === "info") {
      toast.info(outcome.message);
      return;
    }
    if (session) void copyToClipboard(session.id, "Session ID");
  });
  useShortcut("chat.fork", () => {
    const outcome = forkShortcutOutcome(session);
    if (outcome.kind === "info") {
      toast.info(outcome.message);
      return;
    }
    if (session) onFork(session.id);
  });
}

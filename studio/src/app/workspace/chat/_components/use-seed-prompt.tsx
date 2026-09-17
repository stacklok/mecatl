"use client";

import {
  type ReactNode,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import type { ChatSeed } from "@/lib/chat-seed";
import { SeedPromptDialog } from "./seed-prompt-dialog";

/**
 * Consumes the chat route's arrival prompt (`page.tsx` → `resolveChatSeed`;
 * the web analogue of `mecatui -p`) exactly once per mount:
 *
 * 1. the query is stripped from the address bar with a native replaceState
 *    (the path is kept, so `/workspace/chat/<id>?prompt=x` stays on its
 *    chat), so a reload, Back or a shared tab never re-seeds or re-sends;
 * 2. a prefill-only seed (`?prompt=` without `send=1`, a share, a slash
 *    command) goes straight into the composer via `onPrefill` — the user
 *    still presses Enter;
 * 3. a `send=1` seed is parked as a pending send and rendered as the
 *    `SeedPromptDialog` confirmation: Send hands the text to `onSend` (the
 *    same path the composer's Enter takes, so a draft mints its session and
 *    the workspace moves the URL); Edit first (or Esc) prefills instead.
 *
 * Nothing here survives an unmount, and a seed prop that changes after the
 * first consumption is ignored — the mount is the one arrival.
 */
export interface SeedPromptDeps {
  /** The route's resolved arrival prompt; null/undefined when none. */
  seed: ChatSeed | null | undefined;
  /** The route's session id at mount (undefined = the draft). */
  sessionId: string | undefined;
  /** Drops text into the composer (draft or open chat); nothing is sent. */
  onPrefill: (text: string) => void;
  /** The chat hook's send — `sendMessage(text)`. */
  onSend: (text: string) => Promise<void> | void;
  /** Why a send must wait right now (see `seedSendWaitReason`); null = go. */
  waitReason: string | null;
}

export interface SeedPrompt {
  /** The confirmation dialog while a `send=1` seed awaits its click. */
  seedPromptDialog: ReactNode;
  /** The parked seed, for tests and callers that gate on it. */
  pendingSeed: ChatSeed | null;
}

/** Strip the query (and hash) from the current URL, keeping the path. */
function stripQuery() {
  if (typeof window === "undefined") return;
  const { pathname, search, hash } = window.location;
  if (!search && !hash) return;
  window.history.replaceState(null, "", pathname);
}

export function useSeedPrompt({
  seed,
  sessionId,
  onPrefill,
  onSend,
  waitReason,
}: SeedPromptDeps): SeedPrompt {
  const [pending, setPending] = useState<ChatSeed | null>(null);
  // Mirror read by the click handlers, so a double click (or a click that
  // lands before the clearing render) fires the send exactly once — and no
  // side effect ever runs inside a state updater, which StrictMode doubles.
  const pendingRef = useRef<ChatSeed | null>(null);
  pendingRef.current = pending;
  // The one arrival has been consumed (also guards React's dev double-run).
  const consumedRef = useRef(false);
  // Latest callbacks, read at fire time so a caller re-creating them never
  // re-runs the consumption effect.
  const onPrefillRef = useRef(onPrefill);
  onPrefillRef.current = onPrefill;
  const onSendRef = useRef(onSend);
  onSendRef.current = onSend;

  useEffect(() => {
    if (consumedRef.current || !seed) return;
    consumedRef.current = true;
    stripQuery();
    if (seed.autoSend) {
      setPending(seed);
    } else {
      onPrefillRef.current(seed.prompt);
    }
  }, [seed]);

  const confirmSend = useCallback(() => {
    const current = pendingRef.current;
    if (!current) return;
    pendingRef.current = null;
    setPending(null);
    void onSendRef.current(current.prompt);
  }, []);

  const editInstead = useCallback(() => {
    const current = pendingRef.current;
    if (!current) return;
    pendingRef.current = null;
    setPending(null);
    onPrefillRef.current(current.prompt);
  }, []);

  return {
    pendingSeed: pending,
    seedPromptDialog: pending ? (
      <SeedPromptDialog
        prompt={pending.prompt}
        target={sessionId ? "current" : "new"}
        waitReason={waitReason}
        onSend={confirmSend}
        onEdit={editInstead}
      />
    ) : null,
  };
}

/** Send is disabled with this line while the mock tour is open. */
export const SEED_WAIT_MOCK =
  "The mock tour never talks to the daemon. Edit the prompt and send it from a real chat.";

/** Send is disabled with this line while the daemon is not reachable. */
export const SEED_WAIT_OFFLINE = "Waiting for the daemon to connect…";

/** Send is disabled with this line while the chat has a run in flight. */
export const SEED_WAIT_BUSY =
  "This chat has a run in progress. Send is available when it ends.";

/**
 * Why a `send=1` seed cannot go right now, or null when it can: the mock
 * tour never reaches the daemon; an unreachable daemon has nothing to send
 * to (the composer would only hand the text back); a chat that is streaming
 * or parked on an approval/sign-in/clarification takes no new prompt. An
 * idle or errored chat (a failed last turn) accepts one, as its composer does.
 */
export function seedSendWaitReason(state: {
  connected: boolean;
  isStreaming: boolean;
  status: string;
  isMock: boolean;
}): string | null {
  if (state.isMock) return SEED_WAIT_MOCK;
  if (!state.connected) return SEED_WAIT_OFFLINE;
  if (
    state.isStreaming ||
    (state.status !== "idle" && state.status !== "error")
  ) {
    return SEED_WAIT_BUSY;
  }
  return null;
}

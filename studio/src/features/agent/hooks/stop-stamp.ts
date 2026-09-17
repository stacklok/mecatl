import type { AgentMessage } from "../types";

/**
 * Stamps a run terminal the daemon never delivered to this tab onto the
 * transcript: the `cancelled` stop after a client-side Cancel (the tab
 * aborts its own stream before the daemon's terminal frame can arrive), or
 * the `cancelled` inventory state of a rehydrated session (the transcript
 * endpoint carries no stop reason).
 *
 * The trailing ASSISTANT turn gets `stopReason` when it has none yet and is
 * not a failed turn (the failed card owns that terminal). When the last
 * message is not an assistant turn — the run was cut before the model said
 * anything the daemon recorded — an empty assistant bubble is appended so
 * the stop is still visible, exactly as the watch reducer opens a bubble for
 * a limit stop with no assistant activity. Returns the SAME array when there
 * is nothing to do, so a repeat call is a no-op for React state.
 */
export function stampTrailingAssistantStop(
  messages: AgentMessage[],
  stop: string,
  nextId: () => string,
  timestamp: number = Date.now(),
): AgentMessage[] {
  const last = messages.at(-1);
  if (last?.role === "assistant") {
    if (last.failed || last.stopReason) return messages;
    return [...messages.slice(0, -1), { ...last, stopReason: stop }];
  }
  if (!last) return messages;
  return [
    ...messages,
    {
      id: nextId(),
      role: "assistant",
      content: "",
      timestamp,
      stopReason: stop,
    },
  ];
}

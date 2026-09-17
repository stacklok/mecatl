/**
 * A typed, page-local signal that a run this tab observed reached its
 * terminal `run_result` — the live prompt stream or the durable watch, never
 * a replayed history frame. Producers and consumers share no React ancestor
 * that owns run state (the chat hook fires it; the learned-skill receipt
 * notices and the Skills page's Learned view listen), so it rides `window`
 * like `sessions-changed.ts`; the helpers keep the event name and payload
 * shape in ONE place so neither side spells a string.
 *
 * Mirrors mecatui, where every `ResultMsg` re-lists lifecycle receipts.
 */

export const RUN_FINISHED_EVENT = "studio:run-finished";

export interface RunFinishedSignal {
  /** The daemon session whose run ended. */
  sessionId: string;
  /** The daemon's stop reason ("" when the frame carried none). */
  stop: string;
}

/** Announces one observed run terminal to every subscriber. */
export function emitRunFinished(signal: RunFinishedSignal): void {
  if (typeof window === "undefined") return;
  window.dispatchEvent(
    new CustomEvent<RunFinishedSignal>(RUN_FINISHED_EVENT, { detail: signal }),
  );
}

/** Subscribes to the signal; returns the unsubscribe for an effect cleanup. */
export function onRunFinished(
  listener: (signal: RunFinishedSignal) => void,
): () => void {
  if (typeof window === "undefined") return () => {};
  const handler = (event: Event) => {
    const detail = (event as CustomEvent<RunFinishedSignal>).detail;
    listener({
      sessionId: detail?.sessionId ?? "",
      stop: detail?.stop ?? "",
    });
  };
  window.addEventListener(RUN_FINISHED_EVENT, handler);
  return () => window.removeEventListener(RUN_FINISHED_EVENT, handler);
}

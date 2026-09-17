/**
 * What a bare Esc does in the chat view, in priority order (the TUI's esc
 * layering: a selection is dropped before anything else, a pending
 * permission ask is denied before anything is closed or stopped, a panel
 * closes before a run is interrupted). The dispatcher only delivers Esc once
 * every closer layer — a Radix dialog/menu, the composer's autocomplete — has
 * declined it, so this table is the fallback beneath them.
 *
 * `deny-ask` is the TUI's Esc-in-the-approval-modal: while an ask is parked
 * the run is `awaiting`, not streaming, and Esc must answer the ask (deny),
 * never cancel the whole run out from under it. It sits below the selection
 * arm because dropping a selection is harmless and a verdict is not — an Esc
 * pressed to clear a selection can never deny an ask by accident.
 *
 * `clear-draft` is the composer's double-Esc arm: with nothing else claiming
 * Esc and an unsent draft in the field, the press is forwarded to the
 * composer (`useComposerEscape` → `escapePress`), which runs
 * `pressEscapeToClear` below — the first press arms, the second clears.
 */
export type EscapeAction =
  | "clear-selection"
  | "deny-ask"
  | "close-panel"
  | "cancel-run"
  | "clear-draft"
  | "none";

export interface EscapeState {
  /** A non-empty text selection sits inside the transcript. */
  hasSelection: boolean;
  /** A permission ask is waiting for a verdict (the head of the queue). */
  pendingAsk?: boolean;
  /** The right-hand side panel (artifact, thread, tool call, agents…) is open. */
  panelOpen: boolean;
  /** A run is streaming and could be interrupted. */
  isStreaming: boolean;
  /** The composer holds an unsent draft the guard may want to clear first. */
  hasDraft: boolean;
}

/** Resolve one Esc press to exactly one action — never two. */
export function resolveEscapeAction(state: EscapeState): EscapeAction {
  if (state.hasSelection) return "clear-selection";
  if (state.pendingAsk) return "deny-ask";
  if (state.panelOpen) return "close-panel";
  if (state.isStreaming) return "cancel-run";
  if (state.hasDraft) return "clear-draft";
  return "none";
}

/**
 * The double-Esc clear (the TUI's "esc esc clears the prompt"): a first Esc
 * on an idle, non-empty composer ARMS the clear for `ESCAPE_ARM_MS`; a second
 * Esc inside that window empties the composer; an Esc after the window has
 * lapsed only re-arms. Pure — the caller owns the clock and the state, so the
 * machine is testable without a keyboard.
 */
export interface EscapeArm {
  /** Epoch ms until which a second Esc clears; 0 = disarmed. */
  armedUntil: number;
}

export const ESCAPE_ARM_MS = 1500;

export const DISARMED_ESCAPE: EscapeArm = { armedUntil: 0 };

export function pressEscapeToClear(
  state: EscapeArm,
  now: number,
): { state: EscapeArm; clear: boolean } {
  if (state.armedUntil > 0 && now < state.armedUntil) {
    return { state: DISARMED_ESCAPE, clear: true };
  }
  return { state: { armedUntil: now + ESCAPE_ARM_MS }, clear: false };
}

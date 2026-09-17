"use client";

import { type RefObject, useState } from "react";
import { useShortcut } from "@/lib/shortcuts/use-shortcuts";
import { resolveEscapeAction } from "./escape-layering";
import { selectionInside } from "./transcript-text";

export interface ComposerEscapeInput {
  /** The composer holds an unsent draft (the composer reports it via
      `onDraftChange`). */
  hasDraft: boolean;
  /** The right-hand side panel is open (the draft view has none). */
  panelOpen: boolean;
  /** A run is streaming and Esc should interrupt it. */
  isStreaming: boolean;
  /** A permission ask is waiting: Esc denies it instead of stopping the run
      (the TUI's Esc in the approval modal). Absent — no ask arm. */
  pendingAsk?: boolean;
  /** Denies the pending ask. */
  onDenyAsk?: () => void;
  /** The transcript container: a text selection inside it is dropped first.
      Absent (the draft view) — no selection arm. */
  hasSelectionIn?: RefObject<HTMLElement | null>;
  /** Drops the transcript selection; defaults to clearing the window
      selection. */
  onClearSelection?: () => void;
  /** Closes (or steps back inside) the side panel. */
  onClosePanel?: () => void;
  /** Interrupts the in-flight run. */
  onCancelRun?: () => void;
}

/**
 * Owns the `close.esc` registration for whichever chat surface is mounted
 * (ChatView or the DraftView — never both) and dispatches ONE arm per press
 * via `resolveEscapeAction`: selection, pending ask (deny), panel, run — and,
 * with nothing else claiming Esc, the composer's double-Esc clear. The
 * composer cannot see the global dispatcher, so the last arm is handed to it as a counter: every
 * forwarded press increments `escapePress`, and the composer runs the arm
 * machine (`pressEscapeToClear`) on each change. A press that another arm
 * claims never reaches the composer — an Esc meant to close a panel can
 * never start clearing a draft.
 */
export function useComposerEscape(input: ComposerEscapeInput): {
  escapePress: number;
} {
  const [escapePress, setEscapePress] = useState(0);

  useShortcut("close.esc", () => {
    switch (
      resolveEscapeAction({
        hasSelection: input.hasSelectionIn
          ? selectionInside(input.hasSelectionIn.current)
          : false,
        pendingAsk: input.pendingAsk === true,
        panelOpen: input.panelOpen,
        isStreaming: input.isStreaming,
        hasDraft: input.hasDraft,
      })
    ) {
      case "clear-selection":
        if (input.onClearSelection) input.onClearSelection();
        else window.getSelection()?.removeAllRanges();
        return;
      case "deny-ask":
        input.onDenyAsk?.();
        return;
      case "close-panel":
        input.onClosePanel?.();
        return;
      case "cancel-run":
        input.onCancelRun?.();
        return;
      case "clear-draft":
        setEscapePress((n) => n + 1);
        return;
    }
  });

  return { escapePress };
}

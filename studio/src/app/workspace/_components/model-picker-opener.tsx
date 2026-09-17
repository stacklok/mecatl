"use client";

import { useEffect, useRef } from "react";

/**
 * Outside open requests for the composer's model and effort picker — the
 * `/models` and `/effort` built-ins' way in (the TUI's F7 analogue).
 *
 * Mirrors `requestOpenMcpPicker` (mcp-composer-insert.tsx): the composer
 * registers its two pickers while mounted — the desktop dropdown and the
 * mobile sheet are BOTH always in the tree, one hidden by CSS — and a
 * request opens the one the viewport shows. False when no picker is
 * registered (a composer with the model locked, an AI-debug chat) so the
 * caller can say so instead of doing nothing.
 */

export type ModelPickerSurface = "desktop" | "mobile";

const openers: Partial<Record<ModelPickerSurface, () => void>> = {};

/** Registers one surface's opener; returns the unregister. */
export function registerModelPickerOpener(
  surface: ModelPickerSurface,
  open: () => void,
): () => void {
  openers[surface] = open;
  return () => {
    if (openers[surface] === open) delete openers[surface];
  };
}

/** The composer's own breakpoint (chat-input.tsx `mobileViewport`); false
 *  where `matchMedia` is missing (a server render, a bare jsdom). */
function mobileViewport(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(max-width: 499px)").matches
  );
}

/** Opens the picker the current viewport shows; false when none answers. */
export function requestOpenModelPicker(): boolean {
  const preferred: ModelPickerSurface = mobileViewport() ? "mobile" : "desktop";
  const open = openers[preferred] ?? openers.desktop ?? openers.mobile;
  if (!open) return false;
  open();
  return true;
}

/** Test seam: forgets every registered opener. */
export function resetModelPickerOpeners(): void {
  delete openers.desktop;
  delete openers.mobile;
}

/**
 * Renders nothing; registers `onOpen` for `surface` while mounted. Placed
 * next to the picker's open-state owner, like `ModelPickerShortcut`. The
 * latest `onOpen` is read through a ref, so an inline arrow never
 * re-registers the opener on every render.
 */
export function ModelPickerOpener({
  surface,
  onOpen,
}: {
  surface: ModelPickerSurface;
  onOpen: () => void;
}) {
  const ref = useRef(onOpen);
  ref.current = onOpen;
  useEffect(
    () => registerModelPickerOpener(surface, () => ref.current()),
    [surface],
  );
  return null;
}

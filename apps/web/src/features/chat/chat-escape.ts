// SPDX-License-Identifier: Apache-2.0

import { useCallback, useEffect, useRef } from "react";

/** One chat surface owns an Escape press. All verdict callbacks retain their captured exact target. */
export interface ChatEscapeOptions {
  active: boolean;
  draft: boolean;
  onClearDraft: () => void;
  onClosePanel: () => void;
  onDenyAsk: () => void;
  onHintChange: (visible: boolean) => void;
  onIteratePlan: () => void;
  onStopRun: () => void;
  onUnavailablePlan: () => void;
  overlayOpen?: boolean;
  navigationKey?: string;
  pendingAsk: "none" | "ordinary" | "plan";
  /** False while a verdict is in flight or its outcome is uncertain. */
  askAvailable?: boolean;
  panelOpen: boolean;
  planAvailable?: boolean;
  runActive: boolean;
  ownsFocus?: (target: EventTarget | null) => boolean;
}

const overlaySelector =
  '[role="dialog"][data-state="open"], [role="alertdialog"][data-state="open"], [role="menu"][data-state="open"]';

function hasOverlay(): boolean {
  return Boolean(document.querySelector(overlaySelector));
}

export function useChatEscape(options: ChatEscapeOptions): void {
  const latest = useRef(options);
  latest.current = options;
  const armedAt = useRef<number | undefined>(undefined);
  const expiry = useRef<number | undefined>(undefined);
  const released = useRef(false);
  const down = useRef(false);
  const overlayPress = useRef(new WeakSet<KeyboardEvent>());
  const previousNavigation = useRef(options.navigationKey);

  const disarm = useCallback(() => {
    if (expiry.current !== undefined) window.clearTimeout(expiry.current);
    expiry.current = undefined;
    if (armedAt.current === undefined) return;
    armedAt.current = undefined;
    released.current = false;
    latest.current.onHintChange(false);
  }, []);

  useEffect(() => {
    function capture(event: KeyboardEvent) {
      if (event.key === "Escape" && (latest.current.overlayOpen || hasOverlay())) {
        overlayPress.current.add(event);
        disarm();
      }
    }
    function keyup(event: KeyboardEvent) {
      if (event.key === "Escape") {
        down.current = false;
        if (armedAt.current !== undefined) released.current = true;
      }
    }
    function keydown(event: KeyboardEvent) {
      if (event.key !== "Escape" || !latest.current.active) return;
      if (overlayPress.current.has(event) || latest.current.overlayOpen || hasOverlay()) {
        disarm();
        return; // Radix owns the overlay close and trigger focus restoration.
      }
      if (latest.current.ownsFocus && !latest.current.ownsFocus(event.target)) return;
      if (event.isComposing) {
        disarm();
        return;
      }
      if (event.repeat || down.current) return;
      down.current = true;
      event.preventDefault();
      event.stopPropagation();
      const state = latest.current;
      const focused = document.activeElement;
      if (
        (focused instanceof HTMLInputElement || focused instanceof HTMLTextAreaElement) &&
        focused.selectionStart !== focused.selectionEnd &&
        focused.selectionEnd !== null
      ) {
        focused.setSelectionRange(focused.selectionEnd, focused.selectionEnd);
        disarm();
        return;
      }
      const selection = window.getSelection();
      if (selection && !selection.isCollapsed && selection.toString()) {
        selection.removeAllRanges();
        disarm();
        return;
      }
      if (state.pendingAsk !== "none") {
        disarm();
        if (state.pendingAsk === "plan" && !state.planAvailable) state.onUnavailablePlan();
        else if (state.askAvailable !== false) {
          if (state.pendingAsk === "plan") state.onIteratePlan();
          else state.onDenyAsk();
        }
        return;
      }
      if (state.panelOpen) {
        disarm();
        state.onClosePanel();
        return;
      }
      if (state.runActive) {
        disarm();
        state.onStopRun();
        return;
      }
      if (!state.draft) {
        disarm();
        return;
      }
      if (
        armedAt.current !== undefined &&
        released.current &&
        Date.now() - armedAt.current <= 500
      ) {
        disarm();
        state.onClearDraft();
      } else {
        armedAt.current = Date.now();
        released.current = false;
        state.onHintChange(true);
        expiry.current = window.setTimeout(disarm, 500);
      }
    }
    const changed = disarm;
    const observer = new MutationObserver(() => {
      if (hasOverlay()) disarm();
    });
    observer.observe(document.body, {
      attributes: true,
      attributeFilter: ["data-state"],
      childList: true,
      subtree: true,
    });
    document.addEventListener("keydown", capture, true);
    document.addEventListener("keydown", keydown);
    document.addEventListener("keyup", keyup);
    document.addEventListener("input", changed);
    document.addEventListener("change", changed);
    document.addEventListener("focusin", changed);
    document.addEventListener("compositionstart", changed);
    window.addEventListener("popstate", changed);
    window.addEventListener("hashchange", changed);
    return () => {
      if (expiry.current !== undefined) window.clearTimeout(expiry.current);
      observer.disconnect();
      document.removeEventListener("keydown", capture, true);
      document.removeEventListener("keydown", keydown);
      document.removeEventListener("keyup", keyup);
      document.removeEventListener("input", changed);
      document.removeEventListener("change", changed);
      document.removeEventListener("focusin", changed);
      document.removeEventListener("compositionstart", changed);
      window.removeEventListener("popstate", changed);
      window.removeEventListener("hashchange", changed);
    };
  }, [disarm]);

  // New higher layers, navigation to another surface, or draft edits disarm immediately.
  useEffect(() => {
    const navigated = previousNavigation.current !== options.navigationKey;
    previousNavigation.current = options.navigationKey;
    if (
      navigated ||
      !options.active ||
      options.overlayOpen ||
      options.pendingAsk !== "none" ||
      options.panelOpen ||
      options.runActive ||
      !options.draft
    ) {
      disarm();
    }
  }, [
    options.active,
    options.overlayOpen,
    options.pendingAsk,
    options.panelOpen,
    options.runActive,
    options.draft,
    options.navigationKey,
    disarm,
  ]);
}

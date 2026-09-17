"use client";

import { useEffect } from "react";

/**
 * Asks the browser to confirm before the tab closes, reloads, or navigates
 * away while `active` — the web analogue of the TUI's two-step quit. The
 * chat workspace arms it while the composer holds an unsent draft or while
 * THIS tab is driving a run (the `POST /prompt` stream cancels its run on
 * client disconnect — the CLAUDE.md residual; a run driven elsewhere and
 * merely watched survives the tab, so it never arms the guard).
 *
 * Browsers show their own generic prompt and ignore custom text, so the
 * handler only cancels the event (`preventDefault` plus the legacy
 * `returnValue` for older engines). Nothing is registered while inactive:
 * an idle tab closes silently.
 */
export function useBeforeUnloadGuard(active: boolean): void {
  useEffect(() => {
    if (!active) return;
    const handler = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      // Legacy engines need a set returnValue to show the prompt.
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", handler);
    return () => window.removeEventListener("beforeunload", handler);
  }, [active]);
}

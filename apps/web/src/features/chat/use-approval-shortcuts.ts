// SPDX-License-Identifier: Apache-2.0

import { type KeyboardEvent as ReactKeyboardEvent, type RefObject, useEffect, useRef } from "react";
import type { ApprovalVerdict } from "./approval-panel";

const overlaySelector =
  '[role="dialog"][data-state="open"], [role="alertdialog"][data-state="open"], [role="menu"][data-state="open"]';

function isTyping(element: Element): boolean {
  const node = element as HTMLElement;
  return (
    node.tagName === "INPUT" ||
    node.tagName === "TEXTAREA" ||
    node.tagName === "SELECT" ||
    node.isContentEditable
  );
}

function surfaceOf(element: Element): Element | null {
  return element.closest("[data-chat-surface]");
}

/**
 * Whether a key press belongs to the approval in `scope`. A press inside the
 * card always does. Otherwise it must come from the card's own chat surface,
 * and a press with nothing focused belongs to the outermost surface only, so a
 * side panel's ask never answers for the workspace or the other way round.
 */
export function approvalOwnsKey(scope: Element, target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false;
  if (scope.contains(target)) return true;
  const surface = surfaceOf(scope);
  if (target === target.ownerDocument.body || target === target.ownerDocument.documentElement) {
    return !surface?.parentElement?.closest("[data-chat-surface]");
  }
  return surface !== null && surfaceOf(target) === surface;
}

/**
 * Keyboard control for one verdict bar, ported from the prototype's
 * `use-approval-shortcuts.ts`: `y`/`a` allow once, `w` always allows, `n`/`d`
 * deny, and the arrow keys step focus between the verdict buttons
 * (`onBarKeyDown`, bound on the bar itself).
 *
 * Studio keeps three rules the prototype doesn't:
 * - Escape stays with `useChatEscape`, which already denies the surface's
 *   active ask (or iterates a plan) with its overlay, selection, and IME rules.
 * - The caller enables this only for the surface's active ask while the
 *   verdict ledger leaves it answerable: never in flight, acknowledged,
 *   uncertain, or stale. Each verdict still goes through the caller, which
 *   checks the ledger again.
 * - Letters never fire while typing, with a modifier, on a held key, under an
 *   open overlay, or from another surface. Arrows act only on a focused verdict
 *   button, so they never take over chat navigation elsewhere.
 */
export function useApprovalShortcuts({
  active,
  allowAvailable,
  onRespond,
  scope,
}: {
  active: boolean;
  /** False when the ask has no arguments; only Deny is offered then. */
  allowAvailable: boolean;
  onRespond: (verdict: ApprovalVerdict) => void;
  scope: RefObject<HTMLElement | null>;
}) {
  const allowOnceRef = useRef<HTMLButtonElement>(null);
  const allowAlwaysRef = useRef<HTMLButtonElement>(null);
  const denyRef = useRef<HTMLButtonElement>(null);
  const latestRespond = useRef(onRespond);
  latestRespond.current = onRespond;
  const focused = useRef(false);

  // Focus the first verdict once when the ask becomes answerable, but only
  // when nothing else holds focus: never take it from the composer.
  useEffect(() => {
    if (!active || focused.current) return;
    focused.current = true;
    const current = document.activeElement;
    if (current && current !== document.body) return;
    (allowAvailable ? allowOnceRef.current : denyRef.current)?.focus({ preventScroll: true });
  }, [active, allowAvailable]);

  useEffect(() => {
    if (!active) return;

    function onKeyDown(event: KeyboardEvent) {
      if (event.defaultPrevented || event.isComposing || event.repeat) return;
      if (event.altKey || event.ctrlKey || event.metaKey) return;
      const root = scope.current;
      const target = event.target;
      if (!root || !(target instanceof Element) || isTyping(target)) return;
      if (document.querySelector(overlaySelector)) return;

      if (!approvalOwnsKey(root, target)) return;
      let verdict: ApprovalVerdict | undefined;
      switch ((event.key ?? "").toLowerCase()) {
        case "y":
        case "a":
          if (allowAvailable) verdict = "allow_once";
          break;
        case "w":
          if (allowAvailable) verdict = "allow_always";
          break;
        case "n":
        case "d":
          verdict = "deny";
          break;
      }
      if (!verdict) return;
      event.preventDefault();
      latestRespond.current(verdict);
    }

    document.addEventListener("keydown", onKeyDown, { capture: true });
    return () => document.removeEventListener("keydown", onKeyDown, { capture: true });
  }, [active, allowAvailable, scope]);

  /** Arrow keys step focus between the enabled verdict buttons, wrapping. */
  function onBarKeyDown(event: ReactKeyboardEvent<HTMLElement>) {
    if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey) return;
    const delta =
      event.key === "ArrowRight" || event.key === "ArrowDown"
        ? 1
        : event.key === "ArrowLeft" || event.key === "ArrowUp"
          ? -1
          : 0;
    if (delta === 0) return;
    const sequence = [allowOnceRef.current, allowAlwaysRef.current, denyRef.current].filter(
      (button): button is HTMLButtonElement => Boolean(button && !button.disabled),
    );
    const index = sequence.indexOf(event.target as HTMLButtonElement);
    if (index === -1) return;
    event.preventDefault();
    sequence[(index + delta + sequence.length) % sequence.length]?.focus();
  }

  return { allowAlwaysRef, allowOnceRef, denyRef, onBarKeyDown };
}

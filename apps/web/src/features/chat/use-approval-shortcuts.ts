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

/**
 * The element whose focus answers one ask: each approval card marks itself
 * `data-approval-scope`, and the detail panel's whole frame is its ask's scope.
 */
const approvalScopeSelector = '[data-approval-scope], [data-chat-surface="approval"]';

/**
 * Whether a key press belongs to the approval in `scope`: only when focus is
 * inside that card or panel, and not inside another card nested in it. A press
 * from the page body (a click on plain text in Chromium, or on any button in
 * WebKit, leaves focus there), from elsewhere on the surface, or from another
 * card never answers this ask, so typing outside the composer can't grant one.
 */
export function approvalOwnsKey(scope: Element, target: EventTarget | null): boolean {
  return target instanceof Element && target.closest(approvalScopeSelector) === scope;
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
 * - Letters act only while focus is inside this card or panel
 *   (`approvalOwnsKey`), and never while typing, with a modifier, on a held
 *   key, or under an open overlay. Arrows act only on a focused verdict
 *   button, so they never take over chat navigation elsewhere.
 * - A new ask takes focus on Deny, never Allow once, so a stray Enter can only
 *   deny it.
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

  // Focus Deny once when the ask becomes answerable, but only when nothing
  // else holds focus: never take it from the composer. Never Allow once: a
  // stray Enter meant for something else must not approve an unseen ask.
  useEffect(() => {
    if (!active || focused.current) return;
    focused.current = true;
    const current = document.activeElement;
    if (current && current !== document.body) return;
    denyRef.current?.focus({ preventScroll: true });
  }, [active]);

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

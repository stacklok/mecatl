"use client";

import { useEffect, useRef } from "react";
import { Button } from "@/components/ui/button";
import { Kbd } from "@/components/ui/kbd";
import type { ApprovalChoice, ApprovalRequest } from "@/features/agent";
import {
  ariaKeyShortcuts,
  stepVerdictIndex,
  verdictCombos,
  verdictForKeyEvent,
  visibleApprovalVerdicts,
} from "@/features/agent/approval-verdicts";
import { useShortcutBindings } from "@/lib/shortcuts/keymap";
import { keycaps } from "@/lib/shortcuts/registry";
import { cn } from "@/lib/utils";

const LABELS: Record<ApprovalChoice, string> = {
  once: "Allow once",
  always: "Always allow",
  deny: "Deny",
  // Never rendered: the daemon's verdict is three-way. Kept so the record is
  // total over the ApprovalChoice union.
  session: "Allow for session",
};

/** True for a focused text field: a place the bar must never steal focus from. */
function isEditable(el: Element | null): boolean {
  const node = el as HTMLElement | null;
  return (
    !!node &&
    (node.tagName === "INPUT" ||
      node.tagName === "TEXTAREA" ||
      node.isContentEditable === true)
  );
}

/**
 * The verdict buttons of a permission ask (the TUI's approval modal foot):
 * Allow once, Always allow when the ask offers it, Deny — with the keyboard
 * half of the surface.
 *
 * - A NEW ask (a different `approvalId`) moves focus to Allow once, so the
 *   operator can answer by key at once; focus is never taken from a text
 *   field they are typing in.
 * - ← / → step between the visible buttons with wrap, Home / End jump to
 *   the ends (the TUI's left/right cycle). Tab keeps its native meaning so a
 *   keyboard user can leave the bar for the Expand or Raw controls.
 * - The registry's verdict keys (y/a allow once, w always, n/d deny — or
 *   whatever the user remapped them to) answer the ask while focus is inside
 *   the bar, and Esc denies; the press is marked consumed so the global
 *   dispatcher never fires a second verdict for it. This is what lets the
 *   thread panel's card — whose ask the global handlers do not target —
 *   answer to the same keys.
 * - Each button carries its primary key as a keycap hint and every effective
 *   key in `aria-keyshortcuts`; the hint is `aria-hidden`, so the button's
 *   accessible name stays its label.
 */
export function ApprovalVerdictBar({
  approval,
  onRespond,
  offerAlways,
  destructive = false,
  autoFocus = true,
  className,
}: {
  approval: Pick<ApprovalRequest, "approvalId">;
  onRespond: (choice: ApprovalChoice) => void;
  /** Whether "Always allow" is offered (main-agent, non-debugger asks). */
  offerAlways: boolean;
  /** Tint the primary button red (the tool name reads as destructive). */
  destructive?: boolean;
  /** Focus Allow once when a new ask lands (default) — false for a surface
   *  that must not move focus, e.g. a card rendered off-screen. */
  autoFocus?: boolean;
  className?: string;
}) {
  const verdicts = visibleApprovalVerdicts(offerAlways);
  const { bindings } = useShortcutBindings();
  const buttons = useRef(new Map<ApprovalChoice, HTMLButtonElement>());

  // biome-ignore lint/correctness/useExhaustiveDependencies: re-runs for every NEW ask (keyed on its id), not on every render
  useEffect(() => {
    if (!autoFocus) return;
    if (isEditable(document.activeElement)) return;
    buttons.current.get("once")?.focus();
  }, [approval.approvalId, autoFocus]);

  const handleKeyDown = (e: React.KeyboardEvent<HTMLFieldSetElement>) => {
    if (e.defaultPrevented || e.nativeEvent.isComposing) return;
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    const focused = verdicts.findIndex(
      (choice) => buttons.current.get(choice) === document.activeElement,
    );
    const next = stepVerdictIndex(focused, e.key, verdicts.length);
    if (next !== null) {
      e.preventDefault();
      buttons.current.get(verdicts[next])?.focus();
      return;
    }
    if (e.key === "Escape" && !e.shiftKey) {
      e.preventDefault();
      onRespond("deny");
      return;
    }
    const choice = verdictForKeyEvent(bindings, e);
    if (choice !== null && verdicts.includes(choice)) {
      e.preventDefault();
      onRespond(choice);
    }
  };

  return (
    // A fieldset is the semantic group; the keydown only re-targets presses
    // that already land on the focused buttons inside it.
    <fieldset
      aria-label="Permission verdict"
      className={cn("m-0 flex min-w-0 flex-wrap gap-2 border-0 p-0", className)}
      onKeyDown={handleKeyDown}
    >
      {verdicts.map((choice) => {
        const combos = verdictCombos(bindings, choice);
        const hint = combos[0] ? keycaps(combos[0]).join("") : null;
        return (
          <Button
            key={choice}
            ref={(el) => {
              if (el) buttons.current.set(choice, el);
              else buttons.current.delete(choice);
            }}
            size="sm"
            variant={
              choice === "once"
                ? "default"
                : choice === "always"
                  ? "outline"
                  : "ghost"
            }
            onClick={() => onRespond(choice)}
            aria-keyshortcuts={
              combos.length > 0 ? ariaKeyShortcuts(combos) : undefined
            }
            className={cn(
              choice === "once" &&
                cn(
                  "text-white",
                  destructive
                    ? "bg-destructive hover:bg-destructive-strong"
                    : "bg-warning hover:bg-warning/90",
                ),
              choice === "always" &&
                (destructive
                  ? "border-destructive/30 hover:bg-destructive/5"
                  : "border-warning/30 hover:bg-warning/5"),
              choice === "deny" &&
                "text-muted-foreground hover:text-foreground",
            )}
          >
            {LABELS[choice]}
            {hint !== null && (
              <Kbd size="sm" aria-hidden="true" data-testid="verdict-key">
                {hint}
              </Kbd>
            )}
          </Button>
        );
      })}
    </fieldset>
  );
}

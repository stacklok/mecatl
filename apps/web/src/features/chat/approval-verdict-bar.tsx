// SPDX-License-Identifier: Apache-2.0

import type { RefObject } from "react";
import { Button } from "@/components/ui/button";
import { Kbd } from "@/components/ui/kbd";
import { cn } from "@/lib/utils";
import type { ApprovalVerdict } from "./approval-panel";
import { useApprovalShortcuts } from "./use-approval-shortcuts";

/**
 * The approval card's three verdicts, ported from the prototype's
 * `approval-verdict-bar.tsx`. Studio keeps each button's own `disabled`
 * state rather than a `<fieldset disabled>`: Deny stays available when an ask
 * has no arguments to allow. The keycaps and `aria-keyshortcuts` appear only
 * while `shortcuts` is on, so a hint is never shown for a key that does
 * nothing.
 */
export function ApprovalVerdictBar({
  allowAvailable,
  destructive,
  disabled,
  onRespond,
  scope,
  shortcuts,
}: {
  allowAvailable: boolean;
  destructive: boolean;
  disabled: boolean;
  onRespond: (verdict: ApprovalVerdict) => void;
  /** The card or panel whose focus counts as answering this ask. */
  scope: RefObject<HTMLElement | null>;
  /** True only for the surface's active ask while the ledger leaves it answerable. */
  shortcuts: boolean;
}) {
  const { allowAlwaysRef, allowOnceRef, denyRef, onBarKeyDown } = useApprovalShortcuts({
    active: shortcuts && !disabled,
    allowAvailable,
    onRespond,
    scope,
  });
  const hints = shortcuts && !disabled;

  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: arrow keys only move focus between its buttons
    <div className="flex flex-wrap items-center gap-2" onKeyDown={onBarKeyDown}>
      <Button
        aria-keyshortcuts={hints && allowAvailable ? "y a" : undefined}
        className={cn(
          "gap-1.5",
          destructive
            ? "bg-destructive text-destructive-foreground hover:bg-destructive-strong"
            : "bg-warning text-warning-foreground hover:bg-warning/90",
        )}
        disabled={disabled || !allowAvailable}
        onClick={() => onRespond("allow_once")}
        ref={allowOnceRef}
        size="sm"
        type="button"
      >
        Allow once
        {hints && allowAvailable && (
          <Kbd aria-hidden="true" size="sm">
            Y
          </Kbd>
        )}
      </Button>
      <Button
        aria-keyshortcuts={hints && allowAvailable ? "w" : undefined}
        className="gap-1.5 border-warning/30 text-warning hover:bg-warning/5 hover:text-warning"
        disabled={disabled || !allowAvailable}
        onClick={() => onRespond("allow_always")}
        ref={allowAlwaysRef}
        size="sm"
        type="button"
        variant="outline"
      >
        Always allow
        {hints && allowAvailable && (
          <Kbd aria-hidden="true" size="sm">
            W
          </Kbd>
        )}
      </Button>
      <Button
        aria-keyshortcuts={hints ? "n d" : undefined}
        className="gap-1.5 text-muted-foreground"
        disabled={disabled}
        onClick={() => onRespond("deny")}
        ref={denyRef}
        size="sm"
        type="button"
        variant="ghost"
      >
        Deny
        {hints && (
          <Kbd aria-hidden="true" size="sm">
            N
          </Kbd>
        )}
      </Button>
    </div>
  );
}

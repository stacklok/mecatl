// SPDX-License-Identifier: Apache-2.0

import type * as React from "react";
import { cn } from "../../lib/utils";

/**
 * A single keycap. Render a chord as a row of these; a control that answers
 * to a key can show one as its hint (`aria-hidden`, so the accessible name
 * stays the control's own label — the key itself is announced through
 * `aria-keyshortcuts` on the control).
 *
 * `size="sm"` is the in-control hint: tighter, no shadow, and it inherits
 * the current text colour so it reads on a filled background too.
 */
function Kbd({
  className,
  size = "default",
  ...props
}: React.HTMLAttributes<HTMLElement> & { size?: "default" | "sm" }) {
  return (
    <kbd
      data-slot="kbd"
      className={cn(
        "inline-flex items-center justify-center rounded-md border font-mono font-medium",
        size === "default"
          ? "min-w-[1.75rem] border-border bg-muted px-2 py-1 text-xs text-foreground shadow-sm"
          : "min-w-[1.25rem] border-current/30 bg-current/10 px-1 text-[0.65rem] leading-4 text-current",
        className,
      )}
      {...props}
    />
  );
}

export { Kbd };

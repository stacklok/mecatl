import type * as React from "react";
import { cn } from "@/lib/utils";

/**
 * A single keycap. The help reference renders a chord as a row of these; a
 * button that answers to a key shows one as its hint (`aria-hidden`, so the
 * button's accessible name stays its label — the key itself is announced
 * through `aria-keyshortcuts` on the button).
 *
 * `size="sm"` is the in-button hint: tighter, no shadow, and it inherits the
 * button's text colour so it reads on a filled background too.
 */
export function Kbd({
  className,
  size = "default",
  ...props
}: React.HTMLAttributes<HTMLElement> & { size?: "default" | "sm" }) {
  return (
    <kbd
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

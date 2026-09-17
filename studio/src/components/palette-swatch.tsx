import type { ComponentType } from "react";
import { cn } from "@/lib/utils";

/**
 * A filled circle in a palette's accent, shaped like a lucide icon so the
 * settings `OptionField` can show it where it shows an icon. Built once per
 * palette at module load (see the Personalize page) so the component identity
 * is stable across renders.
 */
export function paletteSwatch(
  color: string,
): ComponentType<{ className?: string }> {
  function PaletteSwatch({ className }: { className?: string }) {
    return (
      <span
        aria-hidden="true"
        data-testid="palette-swatch"
        className={cn(
          "inline-block shrink-0 rounded-full ring-1 ring-border ring-inset",
          className,
        )}
        style={{ backgroundColor: color }}
      />
    );
  }
  return PaletteSwatch;
}

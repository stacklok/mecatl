// SPDX-License-Identifier: Apache-2.0

import type { ComponentType } from "react";

const swatches = new Map<string, ComponentType<{ className?: string }>>();

/** A decorative, stable icon for a named palette option. */
export function paletteSwatch(color: string): ComponentType<{ className?: string }> {
  const cached = swatches.get(color);
  if (cached) return cached;
  function PaletteSwatch({ className }: { className?: string }) {
    return (
      <span
        aria-hidden="true"
        className={`inline-block size-4 shrink-0 rounded-full border border-control-border ${className ?? ""}`}
        style={{ backgroundColor: color }}
      />
    );
  }
  swatches.set(color, PaletteSwatch);
  return PaletteSwatch;
}

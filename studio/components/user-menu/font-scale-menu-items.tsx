"use client";

import { Check, Minus, Plus, Type } from "lucide-react";

import { FONT_SCALE_LABELS, useFontScale } from "@/components/font-scale";
import { cn } from "@/lib/utils";

/**
 * A stepper rather than four menu rows: text size is a value an operator nudges
 * and immediately judges by eye, so it wants +/- in place, not a menu that
 * closes on every selection.
 */
export function FontScaleMenuItems() {
  const { scale, setScale, scales } = useFontScale();
  const index = scales.indexOf(scale);
  const atMin = index <= 0;
  const atMax = index >= scales.length - 1;

  return (
    <div className="py-1">
      <div className="flex items-center gap-4 px-5 py-3.5 font-medium text-foreground">
        <Type className="size-5 shrink-0 text-foreground/60" />
        <span className="text-sm">Text size</span>
        <div className="ml-auto flex items-center gap-1">
          <button
            type="button"
            aria-label="Decrease text size"
            disabled={atMin}
            onClick={() => setScale(scales[index - 1])}
            className="flex size-6 items-center justify-center rounded border border-border text-muted-foreground transition-colors hover:bg-accent hover:text-foreground disabled:pointer-events-none disabled:opacity-40"
          >
            <Minus className="size-3" />
          </button>
          <span className="w-14 text-center text-xs tabular-nums text-muted-foreground">
            {FONT_SCALE_LABELS[scale]}
          </span>
          <button
            type="button"
            aria-label="Increase text size"
            disabled={atMax}
            onClick={() => setScale(scales[index + 1])}
            className="flex size-6 items-center justify-center rounded border border-border text-muted-foreground transition-colors hover:bg-accent hover:text-foreground disabled:pointer-events-none disabled:opacity-40"
          >
            <Plus className="size-3" />
          </button>
        </div>
      </div>
      {scale !== 1 && (
        <button
          type="button"
          onClick={() => setScale(1)}
          className={cn(
            "flex w-full items-center gap-4 px-5 pb-3 text-left text-xs text-muted-foreground",
            "hover:text-foreground",
          )}
        >
          <span className="size-5 shrink-0" />
          Reset to default
          <Check className="ml-auto size-3.5 opacity-0" />
        </button>
      )}
    </div>
  );
}

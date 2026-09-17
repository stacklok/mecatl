"use client";

import { ChevronDown, ChevronRight } from "lucide-react";
import { useId } from "react";
import { sanitizeLine } from "@/features/agent/stop-reason";
import { useDetailsOpen } from "./use-details-open";

/**
 * The caveat over the expanded text: the daemon relays whatever reasoning
 * the provider chooses to summarise (`reasoning.delta`), which a model may
 * abbreviate or omit entirely — the reader must not take it for the model's
 * complete thinking.
 */
export const REASONING_CAVEAT =
  "Provider-summarised reasoning — may be incomplete or omitted by the model.";

/** Rune cap for the live line's one-line preview of the latest reasoning. */
const MAX_PREVIEW_RUNES = 160;

/** The non-empty lines of a reasoning blob (the header's `N lines` count). */
export function reasoningLines(reasoning: string): string[] {
  return reasoning
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line.length > 0);
}

/**
 * The collapsible reasoning summary on an assistant turn — the web analogue
 * of the TUI's reasoning line + collapsed "reasoning summary · N lines".
 *
 * While `live` (the run is streaming and this trailing turn has nothing
 * else to show yet) it is a pulsing "Reasoning…" line with the latest
 * reasoning line as a muted one-line preview. Otherwise it is a collapsed
 * header naming the line count; expanding it shows the text under the
 * caveat, capped in height and scrollable. The text is model-authored, so it
 * renders as plain text (no Markdown) and the preview is clamped to one
 * bounded line.
 *
 * Residual (daemon): the transcript endpoint carries no reasoning, so after
 * a reload only reasoning delivered live (or through the durable watch) is
 * shown — a turn rehydrated from the transcript has no disclosure.
 */
export function ReasoningDisclosure({
  reasoning,
  live = false,
  defaultOpen = false,
}: {
  reasoning: string;
  /** The trailing turn is still streaming with nothing else to show yet. */
  live?: boolean;
  /** Start expanded (the collapsed header is the default, unless the
   *  global Expand details preference is on). */
  defaultOpen?: boolean;
}) {
  const [open, toggleOpen] = useDetailsOpen(defaultOpen);
  const bodyId = useId();
  const lines = reasoningLines(reasoning);
  if (lines.length === 0) return null;

  if (live) {
    const preview = sanitizeLine(lines[lines.length - 1], MAX_PREVIEW_RUNES);
    return (
      <div
        data-testid="reasoning-live"
        role="status"
        aria-live="polite"
        className="mt-1 flex min-w-0 items-baseline gap-2 text-xs text-muted-foreground"
      >
        <span className="shrink-0 animate-pulse font-medium">Reasoning…</span>
        <span
          className="min-w-0 truncate text-muted-foreground/70"
          title={preview}
        >
          {preview}
        </span>
      </div>
    );
  }

  const count = `${lines.length} ${lines.length === 1 ? "line" : "lines"}`;
  return (
    <div data-testid="reasoning-disclosure" className="mt-1">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={bodyId}
        onClick={toggleOpen}
        className="inline-flex items-center gap-1 rounded text-xs text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        {open ? (
          <ChevronDown className="size-3" aria-hidden="true" />
        ) : (
          <ChevronRight className="size-3" aria-hidden="true" />
        )}
        Reasoning summary · {count}
      </button>
      {open && (
        <div id={bodyId} className="mt-1">
          <p className="text-[11px] italic text-muted-foreground/70">
            {REASONING_CAVEAT}
          </p>
          <div className="mt-1 max-h-64 overflow-y-auto whitespace-pre-wrap break-words rounded border border-border/60 bg-muted/30 px-2.5 py-2 text-xs leading-relaxed text-muted-foreground">
            {reasoning.trim()}
          </div>
        </div>
      )}
    </div>
  );
}

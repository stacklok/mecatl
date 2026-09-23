// SPDX-License-Identifier: Apache-2.0

import { ChevronDown, ChevronRight } from "lucide-react";
import { useId } from "react";
import { useDetailsOpen } from "./use-details-open";

/**
 * The caveat shown under an expanded reasoning summary: the provider may
 * abbreviate or omit its own reasoning, so this must never read as the
 * model's complete thinking.
 */
const REASONING_CAVEAT = "Provider-summarised reasoning — may be incomplete or omitted.";

/** Character cap for the live line's one-line preview of the latest reasoning. */
const PREVIEW_MAX_CHARS = 160;

function nonEmptyLines(text: string): string[] {
  return text
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line.length > 0);
}

function clampLine(text: string, max: number): string {
  return text.length > max ? `${text.slice(0, max - 1)}…` : text;
}

/**
 * The collapsible reasoning summary under an assistant turn. While
 * `streaming`, it renders a pulsing "Reasoning…" label with the latest
 * reasoning line as a muted one-line preview. Once settled, it collapses to
 * a header naming the line count; expanding it reveals the full text under
 * the provider caveat, capped in height and scrollable. Renders nothing when
 * there is no reasoning text yet.
 */
export function ReasoningDisclosure({ streaming, text }: { text: string; streaming: boolean }) {
  const [open, toggleOpen] = useDetailsOpen();
  const bodyId = useId();
  const lines = nonEmptyLines(text);
  if (lines.length === 0) return null;

  if (streaming) {
    const preview = clampLine(lines[lines.length - 1] ?? "", PREVIEW_MAX_CHARS);
    return (
      <div className="mt-1 flex min-w-0 items-baseline gap-2 text-xs text-muted-foreground">
        <span className="shrink-0 animate-pulse font-medium">Reasoning…</span>
        <span className="min-w-0 truncate text-muted-foreground/70" title={preview}>
          {preview}
        </span>
      </div>
    );
  }

  const count = `${lines.length} ${lines.length === 1 ? "line" : "lines"}`;
  return (
    <div className="mt-1">
      <button
        aria-controls={bodyId}
        aria-expanded={open}
        className="inline-flex items-center gap-1 rounded text-xs text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        onClick={toggleOpen}
        type="button"
      >
        {open ? (
          <ChevronDown aria-hidden="true" className="size-3" />
        ) : (
          <ChevronRight aria-hidden="true" className="size-3" />
        )}
        Reasoning summary · {count}
      </button>
      {open && (
        <div className="mt-1" id={bodyId}>
          <p className="text-[11px] italic text-muted-foreground/70">{REASONING_CAVEAT}</p>
          <div className="mt-1 max-h-64 overflow-y-auto whitespace-pre-wrap break-words rounded border border-border/60 bg-muted/30 px-2.5 py-2 text-xs leading-relaxed text-muted-foreground">
            {text.trim()}
          </div>
        </div>
      )}
    </div>
  );
}

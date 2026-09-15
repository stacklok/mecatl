"use client";

/**
 * The slim context strip near the composer (B1.1): the session's effective
 * model and an APPROXIMATE context-utilisation meter — cumulative input+output
 * tokens this visit counted (summed from the runs' terminal usage frames) vs
 * the model's `resolved_model.context_window`. Deliberately labeled
 * approximate: the daemon's true compaction trigger also counts the system
 * prompt and tool schemas, which the client never sees, and a page reload
 * loses the visit's running total.
 */

import { cn } from "@/lib/utils";

/**
 * Fraction of the context window the counted tokens occupy, clamped to
 * [0, 1]. Null when the window is unknown (<= 0) — the meter must not render
 * against a made-up denominator.
 */
export function contextUtilisation(
  inputTokens: number,
  outputTokens: number,
  contextWindow: number,
): number | null {
  if (!Number.isFinite(contextWindow) || contextWindow <= 0) return null;
  const used = Math.max(0, inputTokens) + Math.max(0, outputTokens);
  return Math.min(1, used / contextWindow);
}

export function ContextMeter({
  modelLabel,
  contextWindow,
  inputTokens,
  outputTokens,
}: {
  /** The effective model (resolved_model.model_id). */
  modelLabel: string;
  contextWindow: number;
  inputTokens: number;
  outputTokens: number;
}) {
  const fraction = contextUtilisation(inputTokens, outputTokens, contextWindow);
  // No window, or nothing counted yet this visit: showing "0%" on a chat
  // whose history the daemon still carries would be a lie, so stay quiet.
  if (fraction === null || inputTokens + outputTokens <= 0) return null;
  const percent = Math.round(fraction * 100);
  return (
    <div className="flex items-center gap-2 px-2 text-[11px] text-muted-foreground/80">
      {modelLabel && <span className="truncate font-medium">{modelLabel}</span>}
      <span
        className="h-1 w-16 shrink-0 overflow-hidden rounded-full bg-border"
        aria-hidden="true"
      >
        <span
          className={cn(
            "block h-full rounded-full",
            fraction >= 0.85 ? "bg-warning" : "bg-brand/60",
          )}
          style={{ width: `${Math.max(2, percent)}%` }}
        />
      </span>
      <span className="whitespace-nowrap tabular-nums">
        ~{percent}% of context
      </span>
      <span className="whitespace-nowrap text-muted-foreground/60">
        approximate
      </span>
    </div>
  );
}

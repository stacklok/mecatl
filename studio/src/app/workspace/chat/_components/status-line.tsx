"use client";

import { AlertTriangle, Info } from "lucide-react";
import {
  MAX_STATUS_TEXT_RUNES,
  type StatusMessage,
  sanitizeLine,
} from "@/features/agent/stop-reason";
import { cn } from "@/lib/utils";

/**
 * The transient status line at the bottom of the transcript — the web
 * analogue of the TUI footer's status slot. It takes over the activity
 * line's row once a run is no longer streaming: a no-progress nudge or the
 * pre-flight recover notice (warning-coloured advisories), or how the last
 * run stopped ("stopped · turn limit", "cancelled"). Transient by design: the
 * next run clears it, and a durable-log replay never resurrects it.
 *
 * Same row geometry as StreamingIndicator (avatar gutter, then text) so the
 * line sits where the activity label was. The text is daemon-influenced, so
 * it is clamped to one bounded line before rendering.
 */
export function StatusLine({ status }: { status: StatusMessage }) {
  const warn = status.tone === "warn";
  const text = sanitizeLine(status.text, MAX_STATUS_TEXT_RUNES);
  if (!text) return null;
  const Icon = warn ? AlertTriangle : Info;
  return (
    <div
      role="status"
      aria-live="polite"
      data-status-kind={status.kind}
      data-status-tone={status.tone}
      className="flex items-center gap-2 py-2 lg:gap-3"
    >
      <span
        className="flex w-7 shrink-0 items-center justify-center lg:w-9"
        aria-hidden="true"
      >
        <Icon
          className={cn(
            "size-3.5",
            warn ? "text-warning" : "text-muted-foreground/70",
          )}
        />
      </span>
      <span
        className={cn(
          "min-w-0 truncate text-sm lg:text-[15px]",
          warn ? "text-warning" : "text-muted-foreground",
        )}
        title={text}
      >
        {text}
      </span>
    </div>
  );
}

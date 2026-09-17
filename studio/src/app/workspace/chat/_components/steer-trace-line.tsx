"use client";

import {
  formatSteerTraceLine,
  type SteerTraceEntry,
} from "@/features/agent/steer-trace";

/**
 * The developer-tools steer trace under the queue strip — the web analogue
 * of the TUI's DebugSteer status line: one muted mono line per steer with
 * its id and decision (`[steer] <id> accepted`, `… drained (watermark <id>)`,
 * `… too_late`, `… retracted`, `… held`), oldest first, so a stuck or
 * mis-correlated steer lifecycle is visible instead of opaque. Renders
 * nothing while the trace is empty; the caller passes `entries` only while
 * Settings → Labs "Developer tools" is on.
 */
export function SteerTraceLine({
  entries,
}: {
  entries?: readonly SteerTraceEntry[];
}) {
  if (!entries?.length) return null;
  return (
    <div className="max-[499px]:mx-3">
      <ol
        aria-label="Steer trace (developer tools)"
        className="max-h-24 space-y-0.5 overflow-y-auto rounded-lg border border-dashed bg-background px-3 py-1.5 font-mono text-[11px] leading-4 text-muted-foreground"
      >
        {entries.map((entry) => {
          const line = formatSteerTraceLine(entry);
          return (
            <li
              key={`${entry.id}-${entry.decision}-${entry.at}`}
              className="truncate"
              title={entry.text ? `${line} — ${entry.text}` : line}
            >
              {line}
            </li>
          );
        })}
      </ol>
    </div>
  );
}

// SPDX-License-Identifier: Apache-2.0

import type { RunStreamEvent } from "@mecatl-studio/contracts";

/** Only this projection may enter the developer trace; event payloads are never retained. */
export interface SteerTraceEntry {
  kind: "steer" | "steer.outcome";
  messageId: string;
  outcome?: "accepted" | "appended" | "retracted" | "none pending" | "too late";
  promoted?: boolean;
  runId: string;
}

const outcomeNames = {
  1: "accepted",
  2: "appended",
  3: "retracted",
  4: "none pending",
  5: "too late",
} as const;

// Opaque IDs still must not turn the metadata view into a URL or free-text surface.
function safeId(value: unknown): value is string {
  return typeof value === "string" && /^[A-Za-z0-9._-]{1,64}$/u.test(value);
}

export function observedSteerTrace(delivery: RunStreamEvent): SteerTraceEntry | undefined {
  if (delivery.type !== "run.event") return undefined;
  const { event } = delivery;
  if (event.unknown || (event.kind !== "steer" && event.kind !== "steer.outcome")) return undefined;
  if (typeof event.payload !== "object" || event.payload === null || Array.isArray(event.payload))
    return undefined;
  const payload = event.payload as Record<string, unknown>;
  if (!safeId(event.runId) || !safeId(payload.messageId)) return undefined;

  if (event.kind === "steer") {
    return { kind: "steer", messageId: payload.messageId, runId: event.runId };
  }
  const outcome =
    typeof payload.outcome === "number" && payload.outcome in outcomeNames
      ? outcomeNames[payload.outcome as keyof typeof outcomeNames]
      : undefined;
  return {
    kind: "steer.outcome",
    messageId: payload.messageId,
    ...(outcome ? { outcome } : {}),
    ...(typeof payload.promoted === "boolean" ? { promoted: payload.promoted } : {}),
    runId: event.runId,
  };
}

export function appendSteerTrace(
  entries: SteerTraceEntry[],
  entry: SteerTraceEntry,
): SteerTraceEntry[] {
  if (
    entries.some(
      (current) =>
        current.runId === entry.runId &&
        current.messageId === entry.messageId &&
        current.kind === entry.kind &&
        current.outcome === entry.outcome &&
        current.promoted === entry.promoted,
    )
  )
    return entries;
  return [...entries, entry].slice(-100);
}

/**
 * The developer steer trace, in the prototype's steer-trace line: a dashed,
 * muted mono list above the queued messages, one line per observed steer
 * event, newest last. It shows only the projection above, and says so when
 * nothing has been observed yet.
 */
export function SteerTrace({ entries }: { entries: SteerTraceEntry[] }) {
  return (
    <section
      aria-label="Developer steer trace"
      className="mx-auto mb-1.5 w-full max-w-3xl px-4 max-[499px]:px-3 min-[500px]:px-6"
    >
      <h2 className="sr-only">Developer steer trace</h2>
      <div className="rounded-lg border border-dashed bg-background px-3 py-1.5 font-mono text-[11px] leading-4 text-muted-foreground">
        {entries.length === 0 ? (
          <p>No observed steer activity. Outcome unknown.</p>
        ) : (
          <ol className="max-h-24 space-y-0.5 overflow-y-auto">
            {entries.map((entry) => {
              const line = `Run ${entry.runId} · Message ${entry.messageId} · Event ${entry.kind} · Outcome ${entry.outcome ?? "unknown"}${entry.promoted !== undefined ? ` · Promoted ${entry.promoted ? "yes" : "no"}` : ""}`;
              return (
                <li
                  className="truncate"
                  key={`${entry.runId}:${entry.messageId}:${entry.kind}:${entry.outcome}:${entry.promoted}`}
                  title={line}
                >
                  {line}
                </li>
              );
            })}
          </ol>
        )}
      </div>
    </section>
  );
}

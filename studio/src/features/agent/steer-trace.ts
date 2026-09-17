/**
 * The steer correlation trace — the web analogue of the TUI's DebugSteer
 * status-line trace (cmd/mecatui/ui/update.go steerTrace). Every steer the
 * chat sends gets a line with its id and what became of it: accepted by the
 * daemon, held (queued instead), too late (the run had ended), drained into
 * the run at a watermark, or retracted. It makes a stuck or mis-correlated
 * steer lifecycle visible instead of opaque.
 *
 * Pure: the chat hook records entries through `appendSteerTrace`; the strip
 * renders them only while Settings → Labs "Developer tools" is on. Bounded so
 * a long session never grows it without limit.
 */

export type SteerTraceDecision =
  /** The daemon did not take the steer; the text was queued instead. */
  | "held"
  /** The daemon accepted it into the run's pending bundle. */
  | "accepted"
  /** The run drained the bundle; `watermark` is the echo's message id. */
  | "drained"
  /** The pending bundle was retracted before it drained. */
  | "retracted"
  /** The named run had already ended (`stale_run_control`); requeued. */
  | "too_late";

export interface SteerTraceEntry {
  /** The steer's client-minted id (the daemon's correlation key). */
  id: string;
  /** The steer text, for reading the trace without the transcript. */
  text: string;
  decision: SteerTraceDecision;
  /** The drain echo's message id — the watermark the pending list split on. */
  watermark?: string;
  /** Epoch ms. */
  at: number;
}

/** How many entries the trace keeps (the oldest fall off). */
export const STEER_TRACE_LIMIT = 20;

/** The trace with `entry` appended, oldest entries dropped past the bound. */
export function appendSteerTrace(
  trace: readonly SteerTraceEntry[],
  entry: SteerTraceEntry,
): SteerTraceEntry[] {
  const next = [...trace, entry];
  return next.length > STEER_TRACE_LIMIT
    ? next.slice(next.length - STEER_TRACE_LIMIT)
    : next;
}

/**
 * Records a drain echo: one `drained` line per steer the watermark split off
 * the pending list. An echo that matched nothing locally (a steer accepted
 * before a reload) still leaves one line under the watermark itself, so the
 * echo is never invisible.
 */
export function traceDrainedSteers(
  trace: readonly SteerTraceEntry[],
  drained: readonly { id: string; text: string }[],
  watermark: string,
  at: number,
): SteerTraceEntry[] {
  if (drained.length === 0) {
    return appendSteerTrace(trace, {
      id: watermark || "-",
      text: "",
      decision: "drained",
      watermark,
      at,
    });
  }
  return drained.reduce<SteerTraceEntry[]>(
    (acc, steer) =>
      appendSteerTrace(acc, {
        id: steer.id,
        text: steer.text,
        decision: "drained",
        watermark,
        at,
      }),
    [...trace],
  );
}

/** One trace line: `[steer] <id> <decision> (watermark <id>)`. */
export function formatSteerTraceLine(entry: SteerTraceEntry): string {
  const base = `[steer] ${entry.id} ${entry.decision}`;
  return entry.watermark ? `${base} (watermark ${entry.watermark})` : base;
}

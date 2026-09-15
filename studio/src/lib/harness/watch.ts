/**
 * Browser-side client for the durable session watch (ADR 0250), over the
 * SDK's `session.activity()`: replay of what is already durable, one
 * event-less boundary delivery with phase "live", then live follow.
 *
 * The SDK owns the transport — SSE framing, the resumable reconnect from the
 * last cursor (`watch_lagging`, a dropped connection), and cursor scoping.
 * This module owns Studio's delivery shape and its typed terminal faults.
 */

import {
  ActivityGapError,
  CursorExpiredError,
  CursorMalformedError,
  CursorScopeError,
} from "@stacklok-oss/mecatl-sdk";
import type { StreamEvent } from "@/features/agent/types";
import { translateEvent } from "@/lib/protocol";
import { toHarnessError } from "./sdk";
import { harnessSession } from "./sessions";

/**
 * A watch that ended on a stream fault, typed on a stable machine code. The
 * codes a caller branches on:
 * - `activity_gap`: recorded events are missing; refetch the transcript.
 * - `cursor_expired`: the cursor is from a superseded log generation; refetch
 *   the transcript and restart from the beginning.
 * - `cursor_malformed`: the resume cursor is not one this SDK issued (or was
 *   issued for another run/filter); restart from the beginning.
 */
export class WatchStreamError extends Error {
  readonly code: string;
  constructor(code: string, message: string) {
    super(message);
    this.name = "WatchStreamError";
    this.code = code;
  }
}

/** One delivery to the watch consumer. */
export interface WatchDelivery {
  /** Open string: "replay" | "live" | "gap" — tolerate unknown values. */
  phase: string;
  /** Opaque resume cursor positioned AFTER this envelope ("" on a gap). */
  cursor: string;
  /** Null on event-less frames — the replay→live boundary (phase "live") and
   *  gap markers (phase "gap") — and on envelopes whose event kinds have no
   *  visual surface (the cursor still advances). */
  event: StreamEvent | null;
}

export interface WatchOptions {
  /** Resume cursor (one this module previously delivered); empty/absent =
   *  from the beginning (the normal first attachment). */
  cursor?: string;
  signal?: AbortSignal;
}

/** Narrows an SDK watch fault to Studio's stable code; undefined otherwise. */
function watchFaultCode(error: unknown): string | undefined {
  if (error instanceof ActivityGapError) return "activity_gap";
  if (error instanceof CursorExpiredError) return "cursor_expired";
  if (
    error instanceof CursorMalformedError ||
    error instanceof CursorScopeError
  )
    return "cursor_malformed";
  return undefined;
}

/**
 * Attaches a durable watch and delivers every envelope to `onDelivery` until
 * the caller aborts (resolves quietly) or the watch faults (throws).
 *
 * Resumable faults are the SDK's job — it reconnects from the last cursor.
 * Non-resumable faults throw: `activity_gap` / `cursor_expired` /
 * `cursor_malformed` as WatchStreamError (the caller falls back to a
 * transcript refetch), pre-stream refusals as HarnessApiError
 * (`no_event_log` / `watch_unsupported` / 404 / 400).
 */
export async function watchSessionEvents(
  sessionId: string,
  onDelivery: (delivery: WatchDelivery) => void,
  options?: WatchOptions,
): Promise<void> {
  const signal = options?.signal;
  if (signal?.aborted) return;
  try {
    const session = await harnessSession(sessionId, signal);
    const activity = await session.activity({
      from: options?.cursor || "start",
      includeLogOnly: true,
      signal,
    });
    try {
      for await (const envelope of activity) {
        if (signal?.aborted) return;
        switch (envelope.kind) {
          case "boundary":
            onDelivery({ phase: "live", cursor: envelope.cursor, event: null });
            break;
          case "gap":
            // The SDK throws ActivityGapError right after yielding this.
            onDelivery({ phase: "gap", cursor: "", event: null });
            break;
          default: {
            const events = envelope.event
              ? translateEvent(envelope.event, sessionId)
              : [];
            if (events.length === 0) {
              // Envelopes whose kinds render nothing still advance the
              // caller's cursor and carry the phase.
              onDelivery({
                phase: envelope.phase,
                cursor: envelope.cursor,
                event: null,
              });
              break;
            }
            for (const event of events) {
              onDelivery({
                phase: envelope.phase,
                cursor: envelope.cursor,
                event,
              });
            }
          }
        }
      }
    } finally {
      await activity.close().catch(() => undefined);
    }
  } catch (error) {
    // A caller-initiated abort resolves quietly.
    if (signal?.aborted) return;
    const code = watchFaultCode(error);
    if (code) {
      throw new WatchStreamError(
        code,
        error instanceof Error ? error.message : String(error),
      );
    }
    throw toHarnessError(error);
  }
}

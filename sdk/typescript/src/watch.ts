import { ProtocolError, type TransportKind } from "./errors.js";
import { decodeEvent, type Event } from "./events.js";
import type { WatchSessionEventsResponse } from "./gen/mecatl/v1/harness_pb.js";

// BEGIN MECATL_WATCH_PHASES
/** Watch phases this SDK understands. @public */
// biome-ignore format: the Go parity parser requires one string literal per line.
export const MECATL_WATCH_PHASES = [
  "gap",
  "live",
  "replay",
] as const;
// END MECATL_WATCH_PHASES

// BEGIN MECATL_ATTACH_FILTERED_KINDS
/** Event kinds omitted by ergonomic attachment views unless requested. @public */
export const MECATL_ATTACH_FILTERED_KINDS = [
  "approval",
  "compaction.archive",
  "network.attempt",
  "request.manifest",
  "user_prompt",
] as const;
// END MECATL_ATTACH_FILTERED_KINDS

/** The compatibility feature required by every durable watch. */
export const WATCH_SESSION_EVENTS_FEATURE = "watch_session_events";

/** A serializable cursor issued by an ergonomic SDK attachment. @public */
export type SdkCursor = string;

/** A replayed or live durable event. @public */
export interface WatchEventEnvelope {
  readonly cursor: SdkCursor;
  readonly event: Event;
  readonly kind: "event";
  readonly phase: "live" | "replay";
}

/** The single replay-to-live transition marker. @public */
export interface WatchBoundaryEnvelope {
  readonly cursor: SdkCursor;
  readonly kind: "boundary";
  readonly phase: "live";
}

/** A known hole in durable delivery. It deliberately exposes no cursor. @public */
export interface WatchGapEnvelope {
  readonly kind: "gap";
  readonly phase: "gap";
}

/** A future watch phase preserved for forward compatibility. @public */
export interface UnknownWatchEnvelope {
  readonly cursor: SdkCursor;
  readonly event?: Event;
  readonly kind: "unknown";
  readonly phase: string;
}

/** One decoded durable-watch delivery envelope. @public */
export type WatchEnvelope =
  | WatchEventEnvelope
  | WatchBoundaryEnvelope
  | WatchGapEnvelope
  | UnknownWatchEnvelope;

/** Internal decoder shared by raw-watch consumers and ergonomic attachments. */
export function decodeWatchEnvelope(
  envelope: WatchSessionEventsResponse,
  transport: TransportKind,
): WatchEnvelope {
  const event = envelope.event;
  switch (envelope.phase) {
    case "replay":
      if (event === undefined) {
        throw new ProtocolError("A replay watch frame must carry an event", { transport });
      }
      return {
        cursor: envelope.cursor,
        event: decodeEvent(event, transport),
        kind: "event",
        phase: "replay",
      };
    case "live":
      if (event === undefined) {
        return { cursor: envelope.cursor, kind: "boundary", phase: "live" };
      }
      return {
        cursor: envelope.cursor,
        event: decodeEvent(event, transport),
        kind: "event",
        phase: "live",
      };
    case "gap":
      if (event !== undefined) {
        throw new ProtocolError("A gap watch frame cannot carry an event", { transport });
      }
      return { kind: "gap", phase: "gap" };
    default:
      return {
        cursor: envelope.cursor,
        ...(event === undefined ? {} : { event: decodeEvent(event, transport) }),
        kind: "unknown",
        phase: envelope.phase,
      };
  }
}

import {
  InvalidStateError,
  NoRunsError,
  ProtocolError,
  type TransportKind,
  UnsupportedFeatureError,
} from "./errors.js";
import { decodeEvent, type Event } from "./events.js";
import type { WatchSessionEventsResponse } from "./gen/mecatl/v1/harness_pb.js";
import type { PermissionVerdict } from "./run.js";

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

/** A durable, cross-run session activity stream. @public */
export interface SessionActivity extends AsyncIterable<WatchEnvelope>, AsyncDisposable {
  readonly cursor: SdkCursor;
  /** Detaches from the watch without cancelling a run. */
  close(): Promise<void>;
}

/** A durable activity stream bound to one run. @public */
export interface AttachedRun extends SessionActivity {
  readonly runId: string;
  /** True until this attachment observes its run's terminal result. */
  readonly live: boolean;
  cancel(): Promise<void>;
  approve(askId: string, allow: boolean): Promise<never>;
  resolveAsk(askId: string, verdict: PermissionVerdict): Promise<never>;
  steer(text: string): Promise<never>;
}

interface AttachmentOperations {
  features(): Promise<ReadonlySet<string>>;
  readonly transportKind: TransportKind;
  watch(
    sessionId: string,
    runId: string,
    signal: AbortSignal,
  ): AsyncIterable<WatchSessionEventsResponse>;
}

const filteredKinds = new Set<string>(MECATL_ATTACH_FILTERED_KINDS);

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

function envelopeEvent(envelope: WatchEnvelope): Event | undefined {
  return envelope.kind === "event" || envelope.kind === "unknown" ? envelope.event : undefined;
}

async function requireWatchFeature(operations: AttachmentOperations): Promise<void> {
  const features = await operations.features();
  if (!features.has(WATCH_SESSION_EVENTS_FEATURE)) {
    throw new UnsupportedFeatureError(WATCH_SESSION_EVENTS_FEATURE, {
      transport: operations.transportKind,
    });
  }
}

class SessionActivityImpl implements SessionActivity {
  readonly #abort: AbortController;
  readonly #buffer: WatchEnvelope[];
  readonly #source: AsyncIterator<WatchSessionEventsResponse>;
  readonly #transport: TransportKind;
  readonly #runId: string | undefined;
  readonly #observe: (envelope: WatchEnvelope) => void;
  #closed = false;
  #closePromise: Promise<void> | undefined;
  #consumed = false;
  #cursor: SdkCursor = "";

  constructor(
    source: AsyncIterator<WatchSessionEventsResponse>,
    transport: TransportKind,
    abort: AbortController,
    runId: string | undefined,
    buffer: WatchEnvelope[] = [],
    observe: (envelope: WatchEnvelope) => void = () => undefined,
  ) {
    this.#source = source;
    this.#transport = transport;
    this.#abort = abort;
    this.#runId = runId;
    this.#buffer = buffer;
    this.#observe = observe;
  }

  get cursor(): SdkCursor {
    return this.#cursor;
  }

  async close(): Promise<void> {
    this.#closePromise ??= this.#close();
    return this.#closePromise;
  }

  async [Symbol.asyncDispose](): Promise<void> {
    await this.close();
  }

  [Symbol.asyncIterator](): AsyncIterator<WatchEnvelope> {
    if (this.#consumed) {
      throw new InvalidStateError("An attachment can only be consumed once", {
        transport: this.#transport,
      });
    }
    this.#consumed = true;
    return this.#iterate();
  }

  async *#iterate(): AsyncGenerator<WatchEnvelope> {
    try {
      for (;;) {
        const envelope = await this.#nextEnvelope();
        if (envelope === undefined) return;
        this.#observe(envelope);
        if ("cursor" in envelope) this.#cursor = envelope.cursor;

        const event = envelopeEvent(envelope);
        if (this.#runId !== undefined && event !== undefined && event.runId !== this.#runId) {
          continue;
        }
        if (event !== undefined && event.kind !== "unknown" && filteredKinds.has(event.kind)) {
          continue;
        }

        yield envelope;
        if (this.#runId !== undefined && event?.runId === this.#runId && event.kind === "result") {
          return;
        }
      }
    } finally {
      await this.close();
    }
  }

  async #nextEnvelope(): Promise<WatchEnvelope | undefined> {
    const buffered = this.#buffer.shift();
    if (buffered !== undefined) return buffered;
    if (this.#closed) return undefined;
    const next = await this.#source.next();
    return next.done ? undefined : decodeWatchEnvelope(next.value, this.#transport);
  }

  async #close(): Promise<void> {
    if (this.#closed) return;
    this.#closed = true;
    this.#abort.abort();
    try {
      await this.#source.return?.();
    } catch {
      // Detaching is best-effort: aborting the watch can reject its pending read.
    }
  }
}

class AttachedRunImpl extends SessionActivityImpl implements AttachedRun {
  readonly runId: string;
  readonly #transport: TransportKind;
  readonly #liveState: { value: boolean };

  constructor(
    runId: string,
    source: AsyncIterator<WatchSessionEventsResponse>,
    transport: TransportKind,
    abort: AbortController,
    buffer: WatchEnvelope[] = [],
  ) {
    const liveState = { value: true };
    super(source, transport, abort, runId, buffer, (envelope) => {
      const event = envelopeEvent(envelope);
      if (event?.runId === runId && event.kind === "result") liveState.value = false;
    });
    this.runId = runId;
    this.#transport = transport;
    this.#liveState = liveState;
  }

  get live(): boolean {
    return this.#liveState.value;
  }

  async cancel(): Promise<void> {
    throw new UnsupportedFeatureError(
      this.#transport === "grpc" ? "prompt_free_controls" : "attached_cancel",
      { transport: this.#transport },
    );
  }

  async approve(_askId: string, _allow: boolean): Promise<never> {
    throw this.#approvalUnsupported();
  }

  async resolveAsk(_askId: string, _verdict: PermissionVerdict): Promise<never> {
    throw this.#approvalUnsupported();
  }

  async steer(_text: string): Promise<never> {
    throw new UnsupportedFeatureError(
      this.#transport === "grpc" ? "prompt_free_controls" : "http_steer",
      { transport: this.#transport },
    );
  }

  #approvalUnsupported(): UnsupportedFeatureError {
    return new UnsupportedFeatureError(
      this.#transport === "grpc" ? "prompt_free_controls" : "approve_ack_only",
      { transport: this.#transport },
    );
  }
}

function watch(
  sessionId: string,
  runId: string,
  operations: AttachmentOperations,
): {
  abort: AbortController;
  source: AsyncIterator<WatchSessionEventsResponse>;
} {
  const abort = new AbortController();
  const source = operations.watch(sessionId, runId, abort.signal)[Symbol.asyncIterator]();
  return { abort, source };
}

/** Creates the durable cross-run activity view for one session. */
export async function createSessionActivity(
  sessionId: string,
  operations: AttachmentOperations,
): Promise<SessionActivity> {
  await requireWatchFeature(operations);
  const opened = watch(sessionId, "", operations);
  return new SessionActivityImpl(opened.source, operations.transportKind, opened.abort, undefined);
}

/** Selects one run and creates its durable attachment view. */
export async function createAttachedRun(
  sessionId: string,
  runId: string | undefined,
  operations: AttachmentOperations,
): Promise<AttachedRun> {
  await requireWatchFeature(operations);
  const opened = watch(sessionId, runId ?? "", operations);
  if (runId !== undefined) {
    return new AttachedRunImpl(runId, opened.source, operations.transportKind, opened.abort);
  }

  const replay: WatchEnvelope[] = [];
  let newestRunId = "";
  try {
    for (;;) {
      const next = await opened.source.next();
      if (next.done) break;
      const envelope = decodeWatchEnvelope(next.value, operations.transportKind);
      replay.push(envelope);
      const event = envelopeEvent(envelope);
      if (event?.runId !== undefined && event.runId !== "") newestRunId = event.runId;
      if (envelope.kind === "boundary") break;
    }
  } catch (error) {
    opened.abort.abort();
    await opened.source.return?.().catch(() => undefined);
    throw error;
  }

  if (newestRunId === "") {
    opened.abort.abort();
    await opened.source.return?.().catch(() => undefined);
    throw new NoRunsError();
  }

  return new AttachedRunImpl(
    newestRunId,
    opened.source,
    operations.transportKind,
    opened.abort,
    replay,
  );
}

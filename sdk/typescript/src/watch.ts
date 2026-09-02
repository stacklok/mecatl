import {
  ActivityGapError,
  CursorMalformedError,
  CursorScopeError,
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

/** Where an attached run begins reading its durable activity. @public */
export interface AttachOptions {
  /** `now` still reads the durable replay over the wire, but discards it locally. */
  from?: "now" | "start" | SdkCursor;
}

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
    cursor: string,
    signal: AbortSignal,
  ): AsyncIterable<WatchSessionEventsResponse>;
}

interface CursorEnvelope {
  readonly filter: string;
  readonly run: string;
  readonly token: string;
  readonly v: "sdkcur/1";
}

function encodeCursor(token: string, filter: string, run: string): SdkCursor {
  const bytes = new TextEncoder().encode(JSON.stringify({ v: "sdkcur/1", token, filter, run }));
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/u, "");
}

function decodeCursor(cursor: SdkCursor): CursorEnvelope {
  try {
    if (cursor === "" || !/^[A-Za-z0-9_-]+$/u.test(cursor) || cursor.length % 4 === 1) {
      throw new Error("invalid base64url");
    }
    const base64 = cursor.replaceAll("-", "+").replaceAll("_", "/");
    const padded = base64.padEnd(base64.length + ((4 - (base64.length % 4)) % 4), "=");
    const binary = atob(padded);
    const bytes = Uint8Array.from(binary, (character) => character.charCodeAt(0));
    const value: unknown = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes));
    if (typeof value !== "object" || value === null || Array.isArray(value)) {
      throw new Error("cursor payload is not an object");
    }
    const record = value as Record<string, unknown>;
    if (
      record.v !== "sdkcur/1" ||
      typeof record.token !== "string" ||
      typeof record.filter !== "string" ||
      typeof record.run !== "string"
    ) {
      throw new Error("cursor payload has invalid fields");
    }
    return {
      filter: record.filter,
      run: record.run,
      token: record.token,
      v: "sdkcur/1",
    };
  } catch (error) {
    if (error instanceof CursorMalformedError) throw error;
    throw new CursorMalformedError("The attachment cursor is not a valid sdkcur/1 value", {
      cause: error,
      transport: "local",
    });
  }
}

function cursorFrom(options: AttachOptions): CursorEnvelope | undefined {
  const from = options.from;
  return from === undefined || from === "start" || from === "now" ? undefined : decodeCursor(from);
}

function requireCursorScope(cursor: CursorEnvelope, targetRun: string, targetFilter: string): void {
  if (cursor.run !== "" && cursor.run !== targetRun) {
    throw new CursorScopeError("The attachment cursor is bound to a different run");
  }
  if (cursor.filter !== "" && cursor.filter !== targetFilter) {
    throw new CursorScopeError("The attachment cursor was issued under a narrower server filter");
  }
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
  readonly #serverFilter: string;
  readonly #observe: (envelope: WatchEnvelope) => void;
  readonly #discardReplay: boolean;
  #closed = false;
  #closePromise: Promise<void> | undefined;
  #consumed = false;
  #cursor: SdkCursor;

  constructor(
    source: AsyncIterator<WatchSessionEventsResponse>,
    transport: TransportKind,
    abort: AbortController,
    runId: string | undefined,
    serverFilter: string,
    initialToken: string,
    buffer: WatchEnvelope[] = [],
    observe: (envelope: WatchEnvelope) => void = () => undefined,
    discardReplay = false,
  ) {
    this.#source = source;
    this.#transport = transport;
    this.#abort = abort;
    this.#runId = runId;
    this.#serverFilter = serverFilter;
    this.#cursor = encodeCursor(initialToken, serverFilter, runId ?? "");
    this.#buffer = buffer;
    this.#observe = observe;
    this.#discardReplay = discardReplay;
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
        if (envelope.kind === "gap") throw new ActivityGapError();
        this.#observe(envelope);

        const event = envelopeEvent(envelope);
        if (this.#discardReplay && envelope.phase === "replay") {
          this.#checkpoint(envelope);
          continue;
        }
        if (this.#runId !== undefined && event !== undefined && event.runId !== this.#runId) {
          this.#checkpoint(envelope);
          continue;
        }
        if (event !== undefined && event.kind !== "unknown" && filteredKinds.has(event.kind)) {
          this.#checkpoint(envelope);
          continue;
        }

        yield envelope;
        this.#checkpoint(envelope);
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
    if (buffered !== undefined) return this.#brandCursor(buffered);
    if (this.#closed) return undefined;
    const next = await this.#source.next();
    return next.done
      ? undefined
      : this.#brandCursor(decodeWatchEnvelope(next.value, this.#transport));
  }

  #brandCursor(envelope: WatchEnvelope): WatchEnvelope {
    if (!("cursor" in envelope)) return envelope;
    return {
      ...envelope,
      cursor: encodeCursor(envelope.cursor, this.#serverFilter, this.#runId ?? ""),
    };
  }

  #checkpoint(envelope: WatchEnvelope): void {
    if ("cursor" in envelope) this.#cursor = envelope.cursor;
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
    serverFilter: string,
    initialToken: string,
    buffer: WatchEnvelope[] = [],
    discardReplay = false,
  ) {
    const liveState = { value: true, pendingAsks: new Set<string>() };
    super(
      source,
      transport,
      abort,
      runId,
      serverFilter,
      initialToken,
      buffer,
      (envelope) => {
        const event = envelopeEvent(envelope);
        if (event?.runId !== runId) return;
        if (event.kind === "permission.ask") liveState.pendingAsks.add(event.payload.askId);
        if (event.kind === "permission.retract" || event.kind === "approval") {
          liveState.pendingAsks.delete(event.payload.askId);
        }
        if (event.kind === "result") {
          liveState.value = false;
          liveState.pendingAsks.clear();
        }
      },
      discardReplay,
    );
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
  cursor: string,
  operations: AttachmentOperations,
): {
  abort: AbortController;
  source: AsyncIterator<WatchSessionEventsResponse>;
} {
  const abort = new AbortController();
  const source = operations.watch(sessionId, runId, cursor, abort.signal)[Symbol.asyncIterator]();
  return { abort, source };
}

/** Creates the durable cross-run activity view for one session. */
export async function createSessionActivity(
  sessionId: string,
  operations: AttachmentOperations,
  options: AttachOptions = {},
): Promise<SessionActivity> {
  if (options.from === "now") {
    throw new InvalidStateError('Opening activity from "now" requires an explicit run id', {
      transport: "local",
    });
  }
  const resume = cursorFrom(options);
  if (resume !== undefined) requireCursorScope(resume, "", "");
  await requireWatchFeature(operations);
  const token = resume?.token ?? "";
  const opened = watch(sessionId, "", token, operations);
  return new SessionActivityImpl(
    opened.source,
    operations.transportKind,
    opened.abort,
    undefined,
    "",
    token,
  );
}

/** Selects one run and creates its durable attachment view. */
export async function createAttachedRun(
  sessionId: string,
  runId: string | undefined,
  operations: AttachmentOperations,
  options: AttachOptions = {},
): Promise<AttachedRun> {
  if (options.from === "now" && (runId === undefined || runId === "")) {
    throw new InvalidStateError('Attaching from "now" requires an explicit run id', {
      transport: "local",
    });
  }
  const resume = cursorFrom(options);
  if (runId !== undefined && resume !== undefined) requireCursorScope(resume, runId, runId);

  if (runId === undefined && resume?.run !== undefined && resume.run !== "") {
    const restoredRun = resume.run;
    const serverFilter = resume.filter;
    requireCursorScope(resume, restoredRun, serverFilter);
    await requireWatchFeature(operations);
    const opened = watch(sessionId, serverFilter, resume.token, operations);
    return new AttachedRunImpl(
      restoredRun,
      opened.source,
      operations.transportKind,
      opened.abort,
      serverFilter,
      resume.token,
    );
  }

  await requireWatchFeature(operations);
  const serverFilter = runId ?? resume?.filter ?? "";
  const token = resume?.token ?? "";
  const opened = watch(sessionId, serverFilter, token, operations);
  if (runId !== undefined) {
    return new AttachedRunImpl(
      runId,
      opened.source,
      operations.transportKind,
      opened.abort,
      serverFilter,
      token,
      [],
      options.from === "now",
    );
  }

  const replay: WatchEnvelope[] = [];
  let newestRunId = "";
  try {
    for (;;) {
      const next = await opened.source.next();
      if (next.done) break;
      const envelope = decodeWatchEnvelope(next.value, operations.transportKind);
      if (envelope.kind === "gap") throw new ActivityGapError();
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
    serverFilter,
    token,
    replay,
  );
}

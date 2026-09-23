import { Code } from "@connectrpc/connect";
import {
  ActivityGapError,
  AuthenticationError,
  CursorMalformedError,
  CursorScopeError,
  IncompatibleServerError,
  InvalidStateError,
  MecatlError,
  NoRunsError,
  ProtocolError,
  TransportError,
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
/** Event kinds omitted by high-level attachment views unless requested. @public */
export const MECATL_ATTACH_FILTERED_KINDS = [
  "approval",
  "compaction.archive",
  "network.attempt",
  "request.manifest",
  "user_prompt",
] as const;
// END MECATL_ATTACH_FILTERED_KINDS

const watchSessionEventsFeature = "watch_session_events";

/** A serializable cursor issued by a durable SDK attachment. @public */
export type SdkCursor = string;

/** Where an attached run begins reading its durable activity. @public */
export interface AttachOptions {
  /** Starts with events received after attachment, discarding the existing replay locally. */
  from?: "now" | "start" | SdkCursor;
  /** Includes durable records omitted by the high-level view by default. */
  includeLogOnly?: boolean;
  /** Detaches this view when aborted; it never cancels a run. */
  signal?: AbortSignal;
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
  /** True until this attachment observes its run's terminal result or valid authorization park. */
  readonly live: boolean;
  /**
   * Cancels the attached run using its exact run ID.
   *
   * @returns A promise that resolves after the cancellation request is accepted.
   */
  cancel(): Promise<void>;
  /**
   * Reports that ask resolution is unavailable on durable attachments.
   *
   * @param askId - Permission-ask ID, retained for parity with a live run.
   * @param verdict - Permission verdict, retained for parity with a live run.
   * @returns A rejected promise.
   * @throws `UnsupportedFeatureError` for every call.
   */
  resolveAsk(askId: string, verdict: PermissionVerdict): Promise<never>;
  /**
   * Reports that steering is unavailable on durable attachments.
   *
   * @param text - Steering text, retained for parity with a live run.
   * @returns A rejected promise.
   * @throws `UnsupportedFeatureError` for every call.
   */
  steer(text: string): Promise<never>;
}

interface AttachmentOperations {
  attachmentStatus?(): AttachmentStatusWriter;
  cancelRun(sessionId: string, runId: string): Promise<void>;
  readonly clientSignal: AbortSignal;
  features(): Promise<ReadonlySet<string>>;
  invalidateCompatibility(): void;
  readonly transportKind: TransportKind;
  watch(
    sessionId: string,
    runId: string,
    cursor: string,
    signal: AbortSignal,
  ): AsyncIterable<WatchSessionEventsResponse>;
}

type AttachmentConnectionStatus = "online" | "reconnecting" | "unauthorized" | "incompatible";

interface AttachmentStatusWriter {
  close(): void;
  set(status: AttachmentConnectionStatus): void;
}

interface AttachmentScheduler {
  delayFor(attempt: number): number;
  sleep(ms: number, signal: AbortSignal): Promise<void>;
}

interface AttachmentInternalOptions {
  onClose?: () => void;
  scheduler?: Partial<AttachmentScheduler>;
}

const RECONNECT_BASE_MS = 100;
const RECONNECT_CAP_MS = 5_000;
const NOOP_ATTACHMENT_STATUS: AttachmentStatusWriter = {
  close: () => undefined,
  set: () => undefined,
};

function delayFor(attempt: number): number {
  const exponent = Math.min(Math.max(0, attempt), 30);
  const exponential = RECONNECT_BASE_MS * 2 ** exponent;
  const jitter = 0.75 + Math.random() * 0.25;
  return Math.min(RECONNECT_CAP_MS, Math.floor(exponential * jitter));
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(signal.reason);
      return;
    }
    let timer: ReturnType<typeof setTimeout> | undefined = setTimeout(() => {
      timer = undefined;
      signal.removeEventListener("abort", aborted);
      resolve();
    }, ms);
    const aborted = () => {
      if (timer !== undefined) clearTimeout(timer);
      timer = undefined;
      reject(signal.reason);
    };
    signal.addEventListener("abort", aborted, { once: true });
  });
}

const terminalWatchCodes = new Set([
  "activity_gap",
  "cursor_expired",
  "cursor_malformed",
  "incompatible_server",
  "invalid_argument",
  "management_unauthorized",
  "no_event_log",
  "session_not_found",
  "watch_unsupported",
]);

type WatchFailureDisposition = "propagate" | "resume" | "terminate";

function watchFailureDisposition(error: unknown): WatchFailureDisposition {
  if (
    error instanceof AuthenticationError ||
    error instanceof TransportError ||
    // A replacement daemon closes connect-node's in-flight HTTP/2 watch with
    // CANCEL. Caller-driven cancellation is handled by WatchConnection's
    // aborted-signal checks before classification, so this remaining shape is a
    // transport drop of the one idempotent operation the SDK may reconnect.
    (error instanceof MecatlError && error.status === Code.Canceled) ||
    (error instanceof MecatlError &&
      (error.code === "watch_capacity" || error.code === "watch_lagging"))
  ) {
    return "resume";
  }
  if (error instanceof MecatlError && terminalWatchCodes.has(error.code)) return "terminate";
  return "propagate";
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

function isAuthorizationPark(event: Event | undefined, runId: string): boolean {
  return (
    event?.kind === "authorization.required" &&
    event.runId === runId &&
    event.payload.status === "pending" &&
    event.payload.authorizationId !== "" &&
    event.payload.callId !== ""
  );
}

function isAttachedRunTerminal(event: Event | undefined, runId: string): boolean {
  return event?.runId === runId && (event.kind === "result" || isAuthorizationPark(event, runId));
}

async function requireWatchFeature(operations: AttachmentOperations): Promise<void> {
  const features = await operations.features();
  if (!features.has(watchSessionEventsFeature)) {
    throw new UnsupportedFeatureError(watchSessionEventsFeature, {
      transport: operations.transportKind,
    });
  }
}

class WatchConnection {
  readonly #abort = new AbortController();
  readonly #operations: AttachmentOperations;
  readonly #runId: string;
  readonly #scheduler: AttachmentScheduler;
  readonly #sessionId: string;
  readonly #signal: AbortSignal;
  readonly #status: AttachmentStatusWriter;
  #attempt = 0;
  #closed = false;
  #source: AsyncIterator<WatchSessionEventsResponse> | undefined;

  constructor(
    sessionId: string,
    runId: string,
    operations: AttachmentOperations,
    externalSignal: AbortSignal | undefined,
    internal: AttachmentInternalOptions,
  ) {
    this.#sessionId = sessionId;
    this.#runId = runId;
    this.#operations = operations;
    this.#signal = AbortSignal.any([
      this.#abort.signal,
      operations.clientSignal,
      ...(externalSignal === undefined ? [] : [externalSignal]),
    ]);
    this.#scheduler = {
      delayFor: internal.scheduler?.delayFor ?? delayFor,
      sleep: internal.scheduler?.sleep ?? sleep,
    };
    this.#status = operations.attachmentStatus?.() ?? NOOP_ATTACHMENT_STATUS;
  }

  async next(cursor: string): Promise<IteratorResult<WatchSessionEventsResponse>> {
    for (;;) {
      if (this.#closed || this.#signal.aborted) return { done: true, value: undefined };
      try {
        this.#source ??= this.#open(cursor);
        const next = await this.#source.next();
        if (!next.done) {
          // The server emits a phase-only boundary before attempting follow
          // admission. A saturated follow can therefore return a boundary and
          // then watch_capacity on every reconnect. Reset backoff only when the
          // stream advances, otherwise persistent saturation retries forever at
          // the minimum delay.
          if (next.value.event !== undefined || next.value.cursor !== cursor) {
            this.#attempt = 0;
          }
          this.#status.set("online");
          return next;
        }
      } catch (error) {
        if (this.#closed || this.#signal.aborted) return { done: true, value: undefined };
        const disposition = watchFailureDisposition(error);
        this.#observeFailure(error, disposition);
        if (disposition !== "resume") throw error;
        if (!(await this.#reconnect(cursor))) return { done: true, value: undefined };
        continue;
      }
      this.#status.set("reconnecting");
      if (!(await this.#reconnect(cursor))) return { done: true, value: undefined };
    }
  }

  async close(): Promise<void> {
    if (this.#closed) return;
    this.#closed = true;
    this.#abort.abort();
    await this.#releaseSource();
    this.#status.close();
  }

  #open(cursor: string): AsyncIterator<WatchSessionEventsResponse> {
    return this.#operations
      .watch(this.#sessionId, this.#runId, cursor, this.#signal)
      [Symbol.asyncIterator]();
  }

  async #reconnect(cursor: string): Promise<boolean> {
    this.#operations.invalidateCompatibility();
    await this.#releaseSource();
    while (!this.#closed && !this.#signal.aborted) {
      const delay = this.#scheduler.delayFor(this.#attempt);
      this.#attempt += 1;
      try {
        await this.#scheduler.sleep(delay, this.#signal);
      } catch (error) {
        if (this.#closed || this.#signal.aborted) return false;
        throw error;
      }
      if (this.#closed || this.#signal.aborted) return false;
      try {
        await requireWatchFeature(this.#operations);
        if (this.#closed || this.#signal.aborted) return false;
        this.#source = this.#open(cursor);
        return true;
      } catch (error) {
        if (this.#closed || this.#signal.aborted) return false;
        const disposition = watchFailureDisposition(error);
        this.#observeFailure(error, disposition);
        if (disposition !== "resume") throw error;
        this.#operations.invalidateCompatibility();
      }
    }
    return false;
  }

  async #releaseSource(): Promise<void> {
    const source = this.#source;
    this.#source = undefined;
    try {
      await source?.return?.();
    } catch {
      // Releasing a failed or aborted transport stream is best-effort.
    }
  }

  #observeFailure(error: unknown, disposition: WatchFailureDisposition): void {
    if (
      error instanceof IncompatibleServerError ||
      (error instanceof MecatlError && error.code === "incompatible_server")
    ) {
      this.#status.set("incompatible");
    } else if (error instanceof AuthenticationError) {
      this.#status.set("unauthorized");
    } else if (disposition === "resume") {
      this.#status.set("reconnecting");
    }
  }
}

class SessionActivityImpl implements SessionActivity {
  readonly #buffer: WatchEnvelope[];
  readonly #connection: WatchConnection;
  readonly #transport: TransportKind;
  readonly #runId: string | undefined;
  readonly #serverFilter: string;
  readonly #observe: (envelope: WatchEnvelope) => void;
  readonly #onClose: () => void;
  readonly #discardReplay: boolean;
  readonly #includeLogOnly: boolean;
  #boundaryAnnounced = false;
  #closed = false;
  #closePromise: Promise<void> | undefined;
  #consumed = false;
  #cursor: SdkCursor;
  #token: string;

  constructor(
    connection: WatchConnection,
    transport: TransportKind,
    runId: string | undefined,
    serverFilter: string,
    initialToken: string,
    includeLogOnly: boolean,
    buffer: WatchEnvelope[] = [],
    observe: (envelope: WatchEnvelope) => void = () => undefined,
    discardReplay = false,
    onClose: () => void = () => undefined,
  ) {
    this.#connection = connection;
    this.#transport = transport;
    this.#runId = runId;
    this.#serverFilter = serverFilter;
    this.#cursor = encodeCursor(initialToken, serverFilter, runId ?? "");
    this.#token = initialToken;
    this.#buffer = buffer;
    this.#observe = observe;
    this.#onClose = onClose;
    this.#discardReplay = discardReplay;
    this.#includeLogOnly = includeLogOnly;
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
        if (envelope.kind === "gap" && this.#runId !== undefined) throw new ActivityGapError();
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
        if (envelope.kind === "boundary" && this.#boundaryAnnounced) {
          this.#checkpoint(envelope);
          continue;
        }
        if (
          !this.#includeLogOnly &&
          event !== undefined &&
          event.kind !== "unknown" &&
          filteredKinds.has(event.kind)
        ) {
          this.#checkpoint(envelope);
          continue;
        }

        if (envelope.kind === "boundary") this.#boundaryAnnounced = true;
        yield envelope;
        if (envelope.kind === "gap") throw new ActivityGapError();
        this.#checkpoint(envelope);
        if (this.#runId !== undefined && isAttachedRunTerminal(event, this.#runId)) {
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
    const next = await this.#connection.next(this.#token);
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
    if ("cursor" in envelope) {
      this.#cursor = envelope.cursor;
      this.#token = decodeCursor(envelope.cursor).token;
    }
  }

  async #close(): Promise<void> {
    if (this.#closed) return;
    this.#closed = true;
    try {
      await this.#connection.close();
    } finally {
      this.#onClose();
    }
  }
}

class AttachedRunImpl extends SessionActivityImpl implements AttachedRun {
  readonly runId: string;
  readonly #operations: AttachmentOperations;
  readonly #sessionId: string;
  readonly #transport: TransportKind;
  readonly #liveState: { value: boolean };

  constructor(
    sessionId: string,
    runId: string,
    connection: WatchConnection,
    operations: AttachmentOperations,
    serverFilter: string,
    initialToken: string,
    includeLogOnly: boolean,
    buffer: WatchEnvelope[] = [],
    discardReplay = false,
    onClose: () => void = () => undefined,
  ) {
    const liveState = { value: true, pendingAsks: new Set<string>() };
    super(
      connection,
      operations.transportKind,
      runId,
      serverFilter,
      initialToken,
      includeLogOnly,
      buffer,
      (envelope) => {
        const event = envelopeEvent(envelope);
        if (event?.runId !== runId) return;
        if (event.kind === "permission.ask") liveState.pendingAsks.add(event.payload.askId);
        if (event.kind === "permission.retract" || event.kind === "approval") {
          liveState.pendingAsks.delete(event.payload.askId);
        }
        if (isAttachedRunTerminal(event, runId)) {
          liveState.value = false;
          liveState.pendingAsks.clear();
        }
      },
      discardReplay,
      onClose,
    );
    this.runId = runId;
    this.#operations = operations;
    this.#sessionId = sessionId;
    this.#transport = operations.transportKind;
    this.#liveState = liveState;
  }

  get live(): boolean {
    return this.#liveState.value;
  }

  async cancel(): Promise<void> {
    await this.#operations.cancelRun(this.#sessionId, this.runId);
  }

  async resolveAsk(_askId: string, _verdict: PermissionVerdict): Promise<never> {
    throw this.#controlsUnsupported();
  }

  async steer(_text: string): Promise<never> {
    throw this.#controlsUnsupported();
  }

  #controlsUnsupported(): UnsupportedFeatureError {
    const error = new UnsupportedFeatureError("attached_run_controls", {
      transport: this.#transport,
    });
    error.message += "; use session.controls(attached.runId)";
    return error;
  }
}

/** Creates the durable cross-run activity view for one session. */
export async function createSessionActivity(
  sessionId: string,
  operations: AttachmentOperations,
  options: AttachOptions = {},
  internal: AttachmentInternalOptions = {},
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
  const connection = new WatchConnection(sessionId, "", operations, options.signal, internal);
  return new SessionActivityImpl(
    connection,
    operations.transportKind,
    undefined,
    "",
    token,
    options.includeLogOnly ?? false,
    [],
    () => undefined,
    false,
    internal.onClose,
  );
}

/** Selects one run and creates its durable attachment view. */
export async function createAttachedRun(
  sessionId: string,
  runId: string | undefined,
  operations: AttachmentOperations,
  options: AttachOptions = {},
  internal: AttachmentInternalOptions = {},
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
    const connection = new WatchConnection(
      sessionId,
      serverFilter,
      operations,
      options.signal,
      internal,
    );
    return new AttachedRunImpl(
      sessionId,
      restoredRun,
      connection,
      operations,
      serverFilter,
      resume.token,
      options.includeLogOnly ?? false,
      [],
      false,
      internal.onClose,
    );
  }

  await requireWatchFeature(operations);
  const serverFilter = runId ?? resume?.filter ?? "";
  const token = resume?.token ?? "";
  const connection = new WatchConnection(
    sessionId,
    serverFilter,
    operations,
    options.signal,
    internal,
  );
  if (runId !== undefined) {
    return new AttachedRunImpl(
      sessionId,
      runId,
      connection,
      operations,
      serverFilter,
      token,
      options.includeLogOnly ?? false,
      [],
      options.from === "now",
      internal.onClose,
    );
  }

  const replay: WatchEnvelope[] = [];
  let newestRunId = "";
  let discoveryToken = token;
  try {
    for (;;) {
      const next = await connection.next(discoveryToken);
      if (next.done) break;
      const envelope = decodeWatchEnvelope(next.value, operations.transportKind);
      if (envelope.kind === "gap") throw new ActivityGapError();
      replay.push(envelope);
      if ("cursor" in envelope) discoveryToken = envelope.cursor;
      const event = envelopeEvent(envelope);
      if (event?.runId !== undefined && event.runId !== "") newestRunId = event.runId;
      if (envelope.kind === "boundary") break;
    }
  } catch (error) {
    await connection.close();
    throw error;
  }

  if (newestRunId === "") {
    await connection.close();
    throw new NoRunsError();
  }

  return new AttachedRunImpl(
    sessionId,
    newestRunId,
    connection,
    operations,
    serverFilter,
    token,
    options.includeLogOnly ?? false,
    replay,
    false,
    internal.onClose,
  );
}

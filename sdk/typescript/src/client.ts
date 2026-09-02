import type {
  DescMessage,
  DescMethodStreaming,
  DescMethodUnary,
  MessageInitShape,
  MessageShape,
} from "@bufbuild/protobuf";
import { create } from "@bufbuild/protobuf";
import type { Transport } from "@connectrpc/connect";

import {
  AuthenticationError,
  IncompatibleServerError,
  InvalidStateError,
  ProtocolError,
  ServerError,
  SessionBusyError,
  TransportError,
  type TransportKind,
} from "./errors.js";
import {
  ContentSchema,
  type ConverseResponse,
  type Event,
  HarnessService,
} from "./gen/mecatl/v1/harness_pb.js";
import { createHttpTransport, type HttpTransportOptions } from "./http.js";
import { encodePrompt, type PromptCapabilities, type PromptInput } from "./media.js";
import { createRawClient, type RawClient } from "./raw.js";
import { type ConverseFrame, type Run, RunImpl, type RunOptions } from "./run.js";

/** The complete connection-state vocabulary exposed by the SDK. @public */
export type ConnectionStatus =
  | "connecting"
  | "online"
  | "reconnecting"
  | "offline"
  | "unauthorized"
  | "incompatible";

/** A callback notified whenever connection status changes. @public */
export type ConnectionStatusListener = (status: ConnectionStatus) => void;

/** A multicast view of the client's latest connection status. @public */
export interface ConnectionStatusStore {
  getSnapshot(): ConnectionStatus;
  subscribe(listener: ConnectionStatusListener): () => void;
}

/** Options accepted by the isomorphic entry point when injecting a transport. @public */
export interface InjectedTransportOptions {
  /** A caller-owned Connect-ES transport, including createRouterTransport() in tests. */
  transport: Transport;
  /** Required only when an unregistered transport speaks the HTTP/JSON/SSE protocol. */
  transportKind?: TransportKind;
}

/** Options accepted by the isomorphic connect() entry point. @public */
export type ConnectOptions = HttpTransportOptions | InjectedTransportOptions;

/** Optional stop conditions for a newly created session. @public */
export interface SessionLimits {
  maxConsecutiveFailures?: number;
  maxToolCalls?: number;
  maxTurns?: number;
}

/** A client-provided streaming-HTTP MCP server. @public */
export interface SessionMcpServer {
  command?: string;
  headers?: Record<string, string>;
  name?: string;
  type?: string;
  url?: string;
}

/** Session-creation fields map directly onto CreateSessionRequest. @public */
export interface CreateSessionOptions {
  debugMcpServers?: string[];
  debugTargetSessionId?: string;
  limits?: SessionLimits;
  mcpServers?: SessionMcpServer[];
  /** PermissionMode enum value from the generated `./gen` entry point. */
  mode?: 0 | 1 | 2 | 3;
  modelId?: string;
  profile?: string;
  providerId?: string;
  reasoningEffort?: string;
  sourceSessionId?: string;
  workspace?: string;
}

/** Optional overrides accepted when forking a session. @public */
export interface ForkSessionOptions {
  reasoningEffort?: string;
  title?: string;
}

/** A durable mecatl session handle. @public */
export interface Session {
  readonly id: string;
  /** Starts a run and resolves once its first run-ID-bearing event arrives. */
  run(prompt: PromptInput, options?: RunOptions): Promise<Run>;
  /** Releases runtime resources without removing the durable session. */
  close(): Promise<void>;
  /** Permanently removes the durable session and its sidecars. */
  delete(): Promise<void>;
}

/** Session lifecycle operations exposed by a Client. @public */
export interface Sessions {
  create(options: CreateSessionOptions): Promise<Session>;
  get(sessionId: string): Promise<Session>;
  fork(sourceSessionId: string, options?: ForkSessionOptions): Promise<Session>;
}

/** The ergonomic mecatl client. @public */
export interface Client {
  readonly sessions: Sessions;
  readonly status: ConnectionStatusStore;
  close(): Promise<void>;
  [Symbol.asyncDispose](): Promise<void>;
}

interface ClientCoreOptions {
  owned: boolean;
  transport: Transport;
  transportKind: TransportKind;
  visibility: boolean;
}

interface SessionOperations {
  assertOpen(): void;
  features(): Promise<ReadonlySet<string>>;
  readonly transportKind: TransportKind;
  stream<I extends DescMessage, O extends DescMessage>(
    method: DescMethodStreaming<I, O>,
    input: AsyncIterable<MessageInitShape<I>>,
  ): AsyncIterable<MessageShape<O>>;
  unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    input: MessageInitShape<I>,
  ): Promise<MessageShape<O>>;
}

type DisposableTransport = Transport & {
  close?: () => Promise<void> | void;
  [Symbol.asyncDispose]?: () => Promise<void>;
  [Symbol.dispose]?: () => void;
};

const HEARTBEAT_INTERVAL_MS = 30_000;

class SessionImpl implements Session {
  readonly id: string;
  readonly #operations: SessionOperations;
  readonly #promptCapabilities: PromptCapabilities | undefined;
  #busy = false;

  constructor(
    id: string,
    operations: SessionOperations,
    promptCapabilities: PromptCapabilities | undefined,
  ) {
    this.id = id;
    this.#operations = operations;
    this.#promptCapabilities = promptCapabilities;
  }

  async run(prompt: PromptInput, options: RunOptions = {}): Promise<Run> {
    this.#operations.assertOpen();
    if (this.#busy) {
      throw new SessionBusyError("A run is already active on this Session", {
        transport: this.#operations.transportKind,
      });
    }
    const encoded = encodePrompt(prompt, this.#promptCapabilities);
    this.#busy = true;
    const input = new ConverseInput(
      {
        kind: {
          case: "prompt",
          value: {
            parts: encoded.media.map((part) =>
              create(ContentSchema, {
                ...(part.bytes === undefined ? {} : { data: part.bytes }),
                kind: part.kind === "image" ? 1 : 2,
                mimeType: part.mimeType,
                ...(part.url === undefined ? {} : { url: part.url }),
              }),
            ),
            sessionId: this.id,
            text: encoded.text,
          },
        },
      },
      this.#operations.transportKind,
    );
    const responses = this.#operations
      .stream(HarnessService.method.converse, input)
      [Symbol.asyncIterator]();
    try {
      let first: Event;
      for (;;) {
        const next = await responses.next();
        if (next.done) {
          throw new ProtocolError("The Converse stream ended before reporting a run id", {
            transport: this.#operations.transportKind,
          });
        }
        if (next.value.event === undefined) {
          throw new ProtocolError("The Converse stream returned a frame without an event", {
            transport: this.#operations.transportKind,
          });
        }
        if (next.value.event.runId !== "") {
          first = next.value.event;
          break;
        }
      }
      const events = unwrapEvents(responses, first.runId, this.#operations.transportKind, () => {
        this.#busy = false;
        input.close();
      });
      if (first.type === "result") {
        this.#busy = false;
        input.close();
      }
      return new RunImpl(
        this.id,
        first.runId,
        first,
        events,
        {
          send: (frame) => input.send(frame),
          transportKind: this.#operations.transportKind,
        },
        options,
      );
    } catch (error) {
      this.#busy = false;
      input.close();
      throw error;
    }
  }

  async close(): Promise<void> {
    this.#operations.assertOpen();
    await this.#operations.unary(HarnessService.method.closeSession, { sessionId: this.id });
  }

  async delete(): Promise<void> {
    this.#operations.assertOpen();
    await this.#operations.unary(HarnessService.method.deleteSession, { sessionId: this.id });
  }
}

class ClientImpl implements Client {
  readonly sessions: Sessions;
  readonly status: ConnectionStatusStore;

  readonly #abort = new AbortController();
  readonly #listeners = new Set<ConnectionStatusListener>();
  readonly #operations: SessionOperations;
  readonly #owned: boolean;
  readonly #raw: RawClient;
  readonly #transport: Transport;
  readonly #transportKind: TransportKind;
  readonly #watchVisibility: boolean;
  #closed = false;
  #closePromise: Promise<void> | undefined;
  #heartbeat: ReturnType<typeof setTimeout> | undefined;
  #heartbeatAbort: AbortController | undefined;
  #snapshot: ConnectionStatus = "connecting";
  #visibilityTarget: Document | undefined;

  constructor(options: ClientCoreOptions) {
    this.#owned = options.owned;
    this.#transport = options.transport;
    this.#transportKind = options.transportKind;
    this.#watchVisibility = options.visibility;
    this.#raw = createRawClient({
      transport: options.transport,
      transportKind: options.transportKind,
    });
    this.#operations = {
      assertOpen: () => this.#assertOpen(),
      features: () => this.#features(),
      stream: (method, input) => this.#stream(method, input),
      transportKind: this.#transportKind,
      unary: (method, input) => this.#unary(method, input),
    };
    this.sessions = {
      create: async (input) => {
        const response = await this.#unary(HarnessService.method.createSession, input);
        return this.#session(response.sessionId, "CreateSession", response.sessionCapabilities);
      },
      fork: async (sourceSessionId, input = {}) => {
        const response = await this.#unary(HarnessService.method.forkSession, {
          ...input,
          sourceSessionId,
        });
        return this.#session(response.sessionId, "ForkSession", undefined);
      },
      get: async (sessionId) => {
        const response = await this.#unary(HarnessService.method.getSession, { sessionId });
        if (response.session === undefined || response.session.sessionId === "") {
          throw new ProtocolError("GetSession returned no session", {
            transport: this.#transportKind,
          });
        }
        return new SessionImpl(response.session.sessionId, this.#operations, undefined);
      },
    };
    this.status = {
      getSnapshot: () => this.#snapshot,
      subscribe: (listener) => this.#subscribe(listener),
    };

    void this.#probe(this.#raw).catch(() => undefined);
  }

  async close(): Promise<void> {
    this.#closePromise ??= this.#close();
    return this.#closePromise;
  }

  async [Symbol.asyncDispose](): Promise<void> {
    await this.close();
  }

  #assertOpen(): void {
    if (this.#closed) {
      throw new InvalidStateError("The client is closed", { transport: this.#transportKind });
    }
  }

  async #close(): Promise<void> {
    if (this.#closed) return;
    this.#closed = true;
    this.#abort.abort(
      new InvalidStateError("The client is closed", {
        transport: this.#transportKind,
      }),
    );
    this.#stopHeartbeat();
    this.#detachVisibility();
    this.#listeners.clear();
    if (!this.#owned) return;

    const transport = this.#transport as DisposableTransport;
    const asyncDispose = transport[Symbol.asyncDispose];
    const dispose = transport[Symbol.dispose];
    if (asyncDispose !== undefined) {
      await asyncDispose.call(transport);
    } else if (dispose !== undefined) {
      dispose.call(transport);
    } else {
      await transport.close?.();
    }
  }

  #session(
    sessionId: string,
    operation: string,
    promptCapabilities: PromptCapabilities | undefined,
  ): Session {
    if (sessionId === "") {
      throw new ProtocolError(`${operation} returned no session id`, {
        transport: this.#transportKind,
      });
    }
    return new SessionImpl(sessionId, this.#operations, promptCapabilities);
  }

  async #unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    input: MessageInitShape<I>,
  ): Promise<MessageShape<O>> {
    this.#assertOpen();
    if (this.#snapshot === "offline") this.#publish("reconnecting");
    try {
      const response = await this.#raw.unary(method, input, { signal: this.#abort.signal });
      this.#publish("online");
      return response;
    } catch (error) {
      this.#observeError(error);
      throw error;
    }
  }

  async #features(): Promise<ReadonlySet<string>> {
    this.#assertOpen();
    if (this.#snapshot === "offline") this.#publish("reconnecting");
    try {
      const features = await this.#raw.features({ signal: this.#abort.signal });
      this.#publish("online");
      return features;
    } catch (error) {
      this.#observeError(error);
      throw error;
    }
  }

  #stream<I extends DescMessage, O extends DescMessage>(
    method: DescMethodStreaming<I, O>,
    input: AsyncIterable<MessageInitShape<I>>,
  ): AsyncIterable<MessageShape<O>> {
    this.#assertOpen();
    const raw = this.#raw.stream(method, input, { signal: this.#abort.signal });
    const observeError = (error: unknown) => this.#observeError(error);
    const publishOnline = () => this.#publish("online");
    return (async function* () {
      try {
        for await (const message of raw) {
          publishOnline();
          yield message;
        }
      } catch (error) {
        observeError(error);
        throw error;
      }
    })();
  }

  async #probe(raw: RawClient, signal: AbortSignal = this.#abort.signal): Promise<void> {
    this.#assertOpen();
    if (this.#snapshot === "offline") this.#publish("reconnecting");
    try {
      await raw.unary(
        HarnessService.method.getCompatibilityInfo,
        {},
        {
          signal,
        },
      );
      this.#publish("online");
    } catch (error) {
      this.#observeError(error);
      throw error;
    }
  }

  #observeError(error: unknown): void {
    if (this.#closed) return;
    if (error instanceof AuthenticationError) {
      this.#publish("unauthorized");
      return;
    }
    if (error instanceof IncompatibleServerError) {
      this.#publish("incompatible");
      return;
    }
    if (error instanceof TransportError) {
      this.#publish("reconnecting");
      this.#publish("offline");
      return;
    }
    if (error instanceof ServerError) this.#publish("online");
  }

  #publish(status: ConnectionStatus): void {
    if (this.#closed || status === this.#snapshot) return;
    this.#snapshot = status;
    for (const listener of [...this.#listeners]) {
      try {
        listener(status);
      } catch {
        // A status observer cannot alter request or heartbeat behavior.
      }
    }
  }

  #subscribe(listener: ConnectionStatusListener): () => void {
    this.#assertOpen();
    this.#listeners.add(listener);
    try {
      listener(this.#snapshot);
    } catch {
      // A status observer cannot alter request or heartbeat behavior.
    }
    if (this.#listeners.size === 1) {
      this.#attachVisibility();
      this.#scheduleHeartbeat();
    }
    let subscribed = true;
    return () => {
      if (!subscribed) return;
      subscribed = false;
      this.#listeners.delete(listener);
      if (this.#listeners.size === 0) {
        this.#stopHeartbeat();
        this.#detachVisibility();
      }
    };
  }

  #scheduleHeartbeat(): void {
    if (
      this.#closed ||
      this.#heartbeat !== undefined ||
      this.#listeners.size === 0 ||
      this.#visibilityTarget?.visibilityState === "hidden"
    ) {
      return;
    }
    this.#heartbeat = setTimeout(() => {
      this.#heartbeat = undefined;
      const controller = new AbortController();
      this.#heartbeatAbort = controller;
      const heartbeatRaw = createRawClient({
        transport: this.#transport,
        transportKind: this.#transportKind,
      });
      void this.#probe(heartbeatRaw, controller.signal)
        .catch(() => undefined)
        .finally(() => {
          if (this.#heartbeatAbort === controller) this.#heartbeatAbort = undefined;
          this.#scheduleHeartbeat();
        });
    }, HEARTBEAT_INTERVAL_MS);
  }

  #stopHeartbeat(): void {
    if (this.#heartbeat !== undefined) {
      clearTimeout(this.#heartbeat);
      this.#heartbeat = undefined;
    }
    this.#heartbeatAbort?.abort();
    this.#heartbeatAbort = undefined;
  }

  readonly #visibilityChanged = (): void => {
    if (this.#visibilityTarget?.visibilityState === "hidden") {
      this.#stopHeartbeat();
    } else {
      this.#scheduleHeartbeat();
    }
  };

  #attachVisibility(): void {
    if (!this.#watchVisibility) return;
    const candidate = globalThis.document;
    if (
      candidate === undefined ||
      typeof candidate.addEventListener !== "function" ||
      typeof candidate.removeEventListener !== "function"
    ) {
      return;
    }
    this.#visibilityTarget = candidate;
    candidate.addEventListener("visibilitychange", this.#visibilityChanged);
  }

  #detachVisibility(): void {
    this.#visibilityTarget?.removeEventListener("visibilitychange", this.#visibilityChanged);
    this.#visibilityTarget = undefined;
  }
}

class ConverseInput implements AsyncIterable<ConverseFrame> {
  readonly #values: ConverseFrame[];
  readonly #transport: TransportKind;
  #closed = false;
  #waiting: ((value: IteratorResult<ConverseFrame>) => void) | undefined;

  constructor(first: ConverseFrame, transport: TransportKind) {
    this.#values = [first];
    this.#transport = transport;
  }

  send(frame: ConverseFrame): void {
    if (this.#closed) {
      throw new InvalidStateError("The run control stream is closed", {
        transport: this.#transport,
      });
    }
    const waiting = this.#waiting;
    if (waiting === undefined) this.#values.push(frame);
    else {
      this.#waiting = undefined;
      waiting({ done: false, value: frame });
    }
  }

  close(): void {
    if (this.#closed) return;
    this.#closed = true;
    this.#waiting?.({ done: true, value: undefined });
    this.#waiting = undefined;
  }

  [Symbol.asyncIterator](): AsyncIterator<ConverseFrame> {
    return {
      next: async () => {
        const value = this.#values.shift();
        if (value !== undefined) return { done: false, value };
        if (this.#closed) return { done: true, value: undefined };
        return new Promise<IteratorResult<ConverseFrame>>((resolve) => {
          this.#waiting = resolve;
        });
      },
      return: async () => {
        this.close();
        return { done: true, value: undefined };
      },
      throw: async (error?: unknown) => {
        this.close();
        throw error;
      },
    };
  }
}

function unwrapEvents(
  responses: AsyncIterator<ConverseResponse>,
  runId: string,
  transport: TransportKind,
  release: () => void,
): AsyncIterator<Event> {
  return {
    next: async () => {
      try {
        const next = await responses.next();
        if (next.done) {
          release();
          return { done: true, value: undefined };
        }
        const event = next.value.event;
        if (event === undefined) {
          throw new ProtocolError("The Converse stream returned a frame without an event", {
            transport,
          });
        }
        if (event.runId !== runId) {
          throw new ProtocolError("The Converse stream changed run id", { transport });
        }
        if (event.type === "result") release();
        return { done: false, value: event };
      } catch (error) {
        release();
        throw error;
      }
    },
  };
}

/** Internal construction seam shared with the Node entry point. */
export function connectTransport(options: ClientCoreOptions): Client {
  return new ClientImpl(options);
}

/** Creates an isomorphic Client over HTTP or a caller-injected transport. @public */
export function connect(options: ConnectOptions): Client {
  if ("transport" in options) {
    return connectTransport({
      owned: false,
      transport: options.transport,
      transportKind: options.transportKind ?? "grpc",
      visibility: true,
    });
  }
  return connectTransport({
    owned: true,
    transport: createHttpTransport(options),
    transportKind: "http",
    visibility: true,
  });
}

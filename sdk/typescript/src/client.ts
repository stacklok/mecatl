import type {
  DescMessage,
  DescMethodStreaming,
  DescMethodUnary,
  MessageInitShape,
  MessageShape,
} from "@bufbuild/protobuf";
import { create } from "@bufbuild/protobuf";
import type { CallOptions, Transport } from "@connectrpc/connect";

import {
  AuthenticationError,
  type DiagnosticsSink,
  IncompatibleServerError,
  InvalidStateError,
  MecatlError,
  ProtocolError,
  ServerError,
  SessionBusyError,
  TransportError,
  type TransportKind,
  UnsupportedFeatureError,
} from "./errors.js";
import {
  ContentSchema,
  type ConverseResponse,
  type Event,
  HarnessService,
  type ListSessionsRequest,
  type ListSessionsResponse,
  type WatchSessionEventsResponse,
} from "./gen/mecatl/v1/harness_pb.js";
import { createHttpTransport, type HttpTransportOptions } from "./http.js";
import { encodePrompt, type PromptCapabilities, type PromptInput } from "./media.js";
import {
  type Agents,
  type Commands,
  createCoreNamespaces,
  type McpInventory,
  type Models,
  type RequestOptions,
  type Worktrees,
} from "./namespaces-core.js";
import {
  createOperationalNamespaces,
  type DreamPlans,
  type LearnedSkills,
  type LearningAttempts,
  type LearningProposals,
  type Reflection,
  type Schedules,
  type Skills,
  type Soul,
  type Storage,
  type UserModel,
} from "./namespaces-ops.js";
import {
  createRawClient,
  invalidateRawCompatibility,
  type RawClient,
  registeredTransportOperations,
  sessionAffinityIfRepresentable,
} from "./raw.js";
import { type ConverseFrame, type Run, RunImpl, type RunOptions } from "./run.js";
import { createTeams, type Teams } from "./team.js";
import {
  type AttachedRun,
  type AttachOptions,
  createAttachedRun,
  createSessionActivity,
  type SessionActivity,
} from "./watch.js";

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
}

/** Optional overrides accepted when forking a session. @public */
export interface ForkSessionOptions {
  reasoningEffort?: string;
  title?: string;
}

/** A durable mecatl session handle. @public */
export interface Session {
  readonly id: string;
  /** Attaches to an explicit run, or selects the newest run in the durable log. */
  attach(runId?: string, options?: AttachOptions): Promise<AttachedRun>;
  /** Opens the durable cross-run activity stream for this session. */
  activity(options?: AttachOptions): Promise<SessionActivity>;
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
  list(request: ListSessionsRequest, options?: RequestOptions): Promise<ListSessionsResponse>;
}

/** The ergonomic mecatl client. @public */
export interface Client {
  readonly agents: Agents;
  readonly commands: Commands;
  readonly dreamPlans: DreamPlans;
  readonly learnedSkills: LearnedSkills;
  readonly learningAttempts: LearningAttempts;
  readonly learningProposals: LearningProposals;
  readonly mcp: McpInventory;
  readonly models: Models;
  readonly reflection: Reflection;
  readonly schedules: Schedules;
  readonly sessions: Sessions;
  readonly skills: Skills;
  readonly soul: Soul;
  readonly status: ConnectionStatusStore;
  readonly storage: Storage;
  readonly teams: Teams;
  readonly userModel: UserModel;
  readonly worktrees: Worktrees;
  close(): Promise<void>;
  [Symbol.asyncDispose](): Promise<void>;
}

const diagnosticSinks = new WeakMap<Client, DiagnosticsSink>();

/** Returns the diagnostic sink installed on one client, when present. */
export function clientDiagnostics(client: Client): DiagnosticsSink | undefined {
  return diagnosticSinks.get(client);
}

interface ClientCoreOptions {
  diagnostics?: DiagnosticsSink;
  internal?: ClientInternalOptions;
  owned: boolean;
  transport: Transport;
  transportKind: TransportKind;
  visibility: boolean;
}

interface ClientDaemonExit {
  readonly code: number | null;
  readonly signal: string | null;
}

interface ClientDaemonLifecycle {
  readonly exit: Promise<ClientDaemonExit>;
  removeRuntime(): Promise<void>;
  stop(): Promise<void>;
}

interface ClientToolHostLifecycle {
  abort(reason: unknown): void;
  beginSessionCreate?(): { finish(created: boolean): void };
  hasTools?(): boolean;
  mcpServer?(): SessionMcpServer;
  readonly serverName?: string;
  start(): Promise<void> | void;
  stop(): Promise<void>;
}

interface ClientInternalOptions {
  daemon?: ClientDaemonLifecycle;
  onTeardownStep?: (step: string) => void;
  toolHost?: ClientToolHostLifecycle;
}

type AttachmentConnectionStatus = "online" | "reconnecting" | "unauthorized" | "incompatible";

interface AttachmentStatusWriter {
  close(): void;
  set(status: AttachmentConnectionStatus): void;
}

interface SessionOperations {
  assertOpen(): void;
  attachmentStatus(): AttachmentStatusWriter;
  cancelRun(sessionId: string, runId: string): Promise<void>;
  readonly clientSignal: AbortSignal;
  features(): Promise<ReadonlySet<string>>;
  invalidateCompatibility(): void;
  registerAttachment(close: () => Promise<void>): () => void;
  registerRun(cancel: () => Promise<void>): () => void;
  readonly transportKind: TransportKind;
  stream<I extends DescMessage, O extends DescMessage>(
    method: DescMethodStreaming<I, O>,
    input: AsyncIterable<MessageInitShape<I>>,
    options?: CallOptions,
  ): AsyncIterable<MessageShape<O>>;
  unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    input: MessageInitShape<I>,
    options?: CallOptions,
  ): Promise<MessageShape<O>>;
  watch(
    sessionId: string,
    runId: string,
    cursor: string,
    signal: AbortSignal,
  ): AsyncIterable<WatchSessionEventsResponse>;
}

function createSessionAffinity(options: CreateSessionOptions): CallOptions | undefined {
  const reference = options.debugTargetSessionId;
  return reference === undefined || reference === ""
    ? undefined
    : sessionAffinityIfRepresentable(reference);
}

function sessionAffinityOperations(
  sessionId: string,
  operations: SessionOperations,
): SessionOperations {
  return {
    ...operations,
    stream: (method, input, options) =>
      operations.stream(method, input, sessionAffinityIfRepresentable(sessionId, options)),
    unary: (method, input, options) =>
      operations.unary(method, input, sessionAffinityIfRepresentable(sessionId, options)),
  };
}

type DisposableTransport = Transport & {
  close?: () => Promise<void> | void;
  [Symbol.asyncDispose]?: () => Promise<void>;
  [Symbol.dispose]?: () => void;
};

/** Internal transport-disposal seam shared with local daemon startup. */
export async function disposeTransport(transport: Transport): Promise<void> {
  const disposable = transport as DisposableTransport;
  const asyncDispose = disposable[Symbol.asyncDispose];
  const dispose = disposable[Symbol.dispose];
  if (asyncDispose !== undefined) {
    await asyncDispose.call(disposable);
  } else if (dispose !== undefined) {
    dispose.call(disposable);
  } else {
    await disposable.close?.();
  }
}

const HEARTBEAT_INTERVAL_MS = 30_000;
const CONNECTION_STATUS_PRECEDENCE: readonly ConnectionStatus[] = [
  "incompatible",
  "unauthorized",
  "reconnecting",
  "connecting",
  "offline",
  "online",
];

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
    this.#operations = sessionAffinityOperations(this.id, operations);
    this.#promptCapabilities = promptCapabilities;
  }

  async attach(runId?: string, options: AttachOptions = {}): Promise<AttachedRun> {
    this.#operations.assertOpen();
    let unregister: () => void = () => undefined;
    const attached = await createAttachedRun(this.id, runId, this.#operations, options, {
      onClose: () => unregister(),
    });
    unregister = this.#operations.registerAttachment(() => attached.close());
    return attached;
  }

  async activity(options: AttachOptions = {}): Promise<SessionActivity> {
    this.#operations.assertOpen();
    let unregister: () => void = () => undefined;
    const activity = await createSessionActivity(this.id, this.#operations, options, {
      onClose: () => unregister(),
    });
    unregister = this.#operations.registerAttachment(() => activity.close());
    return activity;
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
    const runAbort = new AbortController();
    const responses = this.#operations
      .stream(HarnessService.method.converse, input, { signal: runAbort.signal })
      [Symbol.asyncIterator]();
    let acceptedRunId = "";
    let released = false;
    let unregister: () => void = () => undefined;
    const release = () => {
      if (released) return;
      released = true;
      this.#busy = false;
      input.close();
      unregister();
    };
    unregister = this.#operations.registerRun(async () => {
      try {
        if (acceptedRunId !== "") {
          input.send({
            kind: { case: "cancel", value: { expectedRunId: acceptedRunId } },
          });
        }
      } finally {
        release();
        runAbort.abort();
        await responses.return?.();
      }
    });
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
          acceptedRunId = first.runId;
          break;
        }
      }
      const events = unwrapEvents(responses, first.runId, this.#operations.transportKind, release);
      if (first.type === "result") {
        release();
      }
      return new RunImpl(
        this.id,
        first.runId,
        first,
        events,
        {
          assertOpen: () => this.#operations.assertOpen(),
          send: (frame) => input.send(frame),
          transportKind: this.#operations.transportKind,
        },
        options,
      );
    } catch (error) {
      release();
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
  readonly agents: Agents;
  readonly commands: Commands;
  readonly dreamPlans: DreamPlans;
  readonly learnedSkills: LearnedSkills;
  readonly learningAttempts: LearningAttempts;
  readonly learningProposals: LearningProposals;
  readonly mcp: McpInventory;
  readonly models: Models;
  readonly reflection: Reflection;
  readonly schedules: Schedules;
  readonly sessions: Sessions;
  readonly skills: Skills;
  readonly soul: Soul;
  readonly status: ConnectionStatusStore;
  readonly storage: Storage;
  readonly teams: Teams;
  readonly userModel: UserModel;
  readonly worktrees: Worktrees;

  readonly #abort = new AbortController();
  readonly #attachments = new Map<symbol, () => Promise<void>>();
  readonly #attachmentStatuses = new Map<symbol, AttachmentConnectionStatus>();
  readonly #daemon: ClientDaemonLifecycle | undefined;
  readonly #diagnostics: DiagnosticsSink | undefined;
  readonly #listeners = new Set<ConnectionStatusListener>();
  readonly #operations: SessionOperations;
  readonly #owned: boolean;
  readonly #onTeardownStep: ((step: string) => void) | undefined;
  readonly #raw: RawClient;
  readonly #runs = new Map<symbol, () => Promise<void>>();
  readonly #toolHost: ClientToolHostLifecycle | undefined;
  readonly #toolHostStarted: Promise<void> | undefined;
  readonly #transport: Transport;
  readonly #transportKind: TransportKind;
  readonly #watchVisibility: boolean;
  #closed = false;
  #closePromise: Promise<void> | undefined;
  #heartbeat: ReturnType<typeof setTimeout> | undefined;
  #heartbeatAbort: AbortController | undefined;
  #requestStatus: ConnectionStatus = "connecting";
  #snapshot: ConnectionStatus = "connecting";
  #terminalError: InvalidStateError | undefined;
  #visibilityTarget: Document | undefined;

  constructor(options: ClientCoreOptions) {
    if (options.diagnostics !== undefined) diagnosticSinks.set(this, options.diagnostics);
    this.#daemon = options.internal?.daemon;
    this.#diagnostics = options.diagnostics;
    this.#owned = options.owned;
    this.#onTeardownStep = options.internal?.onTeardownStep;
    this.#toolHost = options.internal?.toolHost;
    this.#transport = options.transport;
    this.#transportKind = options.transportKind;
    this.#watchVisibility = options.visibility;
    this.#raw = createRawClient({
      transport: options.transport,
      transportKind: options.transportKind,
    });
    this.#operations = {
      assertOpen: () => this.#assertOpen(),
      attachmentStatus: () => this.#createAttachmentStatus(),
      cancelRun: (sessionId, runId) => this.#cancelRun(sessionId, runId),
      clientSignal: this.#abort.signal,
      features: () => this.#features(),
      invalidateCompatibility: () => invalidateRawCompatibility(this.#raw),
      registerAttachment: (close) => this.#register(this.#attachments, close),
      registerRun: (cancel) => this.#register(this.#runs, cancel),
      stream: (method, input, options) => this.#stream(method, input, options),
      transportKind: this.#transportKind,
      unary: (method, input, options) => this.#unary(method, input, options),
      watch: (sessionId, runId, cursor, signal) =>
        this.#watch(
          HarnessService.method.watchSessionEvents,
          singleValue({ cursor, runId, sessionId }),
          signal,
        ),
    };
    const namespaceOperations = {
      unary: (method, input, requestOptions) => this.#unary(method, input, requestOptions),
    } satisfies Pick<RawClient, "unary">;
    const namespaces = createCoreNamespaces(namespaceOperations);
    const operational = createOperationalNamespaces(namespaceOperations);
    this.agents = namespaces.agents;
    this.commands = namespaces.commands;
    this.mcp = namespaces.mcp;
    this.models = namespaces.models;
    this.worktrees = namespaces.worktrees;
    this.dreamPlans = operational.dreamPlans;
    this.learnedSkills = operational.learnedSkills;
    this.learningAttempts = operational.learningAttempts;
    this.learningProposals = operational.learningProposals;
    this.reflection = operational.reflection;
    this.schedules = operational.schedules;
    this.skills = operational.skills;
    this.soul = operational.soul;
    this.storage = operational.storage;
    this.teams = createTeams({
      assertOpen: () => this.#assertOpen(),
      stream: (method, input, requestOptions) => this.#stream(method, input, requestOptions),
      transportKind: this.#transportKind,
      unary: (method, input, requestOptions) => this.#unary(method, input, requestOptions),
    });
    this.userModel = operational.userModel;
    this.sessions = {
      create: async (input) => {
        const lease = this.#toolHost?.beginSessionCreate?.();
        try {
          let request = input;
          if (this.#toolHost?.hasTools?.() === true) {
            await this.#toolHostStarted;
            const serverName = this.#toolHost.serverName;
            const mcpServer = this.#toolHost.mcpServer?.();
            if (serverName === undefined || mcpServer === undefined) {
              throw new InvalidStateError("The callback tool host is not ready", {
                transport: "local",
              });
            }
            const inventory = await this.#unary(HarnessService.method.listMcpSources, {});
            if (
              inventory.sources.some((source) =>
                source.servers.some((server) => server.name === serverName),
              )
            ) {
              throw new MecatlError(
                `Callback tool server name ${JSON.stringify(serverName)} collides with a resolved server-global MCP server`,
                { code: "tool_registration", transport: "local" },
              );
            }
            request = {
              ...input,
              mcpServers: [...(input.mcpServers ?? []), mcpServer],
            };
          }
          const response = await this.#unary(
            HarnessService.method.createSession,
            request,
            createSessionAffinity(input),
          );
          lease?.finish(true);
          return this.#session(response.sessionId, "CreateSession", response.sessionCapabilities);
        } catch (error) {
          lease?.finish(false);
          throw error;
        }
      },
      fork: async (sourceSessionId, input = {}) => {
        const response = await this.#unary(
          HarnessService.method.forkSession,
          {
            ...input,
            sourceSessionId,
          },
          sessionAffinityIfRepresentable(sourceSessionId),
        );
        return this.#session(response.sessionId, "ForkSession", undefined);
      },
      get: async (sessionId) => {
        const response = await this.#unary(
          HarnessService.method.getSession,
          { sessionId },
          sessionAffinityIfRepresentable(sessionId),
        );
        if (response.session === undefined || response.session.sessionId === "") {
          throw new ProtocolError("GetSession returned no session", {
            transport: this.#transportKind,
          });
        }
        return new SessionImpl(response.session.sessionId, this.#operations, undefined);
      },
      list: operational.sessionInventory.list,
    };
    this.status = {
      getSnapshot: () => this.#snapshot,
      subscribe: (listener) => this.#subscribe(listener),
    };

    if (this.#toolHost !== undefined) {
      try {
        this.#toolHostStarted = Promise.resolve(this.#toolHost.start());
      } catch (error) {
        this.#toolHostStarted = Promise.reject(error);
      }
      void this.#toolHostStarted.catch(() => undefined);
    }
    if (this.#daemon !== undefined) {
      void this.#daemon.exit.then(
        (status) => this.#daemonExited(status),
        () => this.#daemonExited({ code: null, signal: null }),
      );
    }

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
    if (this.#terminalError !== undefined) throw this.#terminalError;
    if (this.#closed) {
      throw new InvalidStateError("The client is closed", { transport: this.#transportKind });
    }
  }

  async #close(): Promise<void> {
    if (this.#closed) return;
    this.#closed = true;
    const closed = new InvalidStateError("The client is closed", {
      transport: this.#transportKind,
    });

    await this.#closeRegistered("cancel_owned_run", this.#runs);
    await this.#closeRegistered("release_attachment", this.#attachments);
    this.#abort.abort(closed);
    this.#attachmentStatuses.clear();
    await this.#teardown("status_monitor", () => {
      this.#stopHeartbeat();
      this.#detachVisibility();
      this.#listeners.clear();
    });

    if (this.#toolHost !== undefined) {
      await this.#teardown("tool_host_abort", () => this.#toolHost?.abort(closed));
      await this.#teardown("tool_host_stop", async () => this.#toolHost?.stop());
    }

    if (this.#owned) {
      await this.#teardown("transport", () => disposeTransport(this.#transport));
    }
    if (this.#daemon !== undefined) {
      await this.#teardown("daemon_stop", () => this.#daemon?.stop());
      await this.#teardown("runtime_directory", () => this.#daemon?.removeRuntime());
    }
  }

  async #closeRegistered(step: string, resources: Map<symbol, () => Promise<void>>): Promise<void> {
    const closers = [...resources.values()];
    resources.clear();
    for (const close of closers) {
      await this.#teardown(step, close);
    }
  }

  #daemonExited(status: ClientDaemonExit): void {
    if (this.#closed || this.#terminalError !== undefined) return;
    this.#terminalError = new InvalidStateError(
      `The spawned mecated daemon exited (code=${String(status.code)}, signal=${String(status.signal)})`,
      { transport: "local" },
    );
    this.#requestStatus = "offline";
    this.#attachmentStatuses.clear();
    this.#publishResolvedStatus();
    this.#abort.abort(this.#terminalError);
    this.#stopHeartbeat();
    this.#detachVisibility();
    this.#emitDiagnostic({
      code: "daemon_exited",
      fields: Object.freeze({ exitCode: status.code, signal: status.signal }),
      level: "error",
      message: "The spawned mecated daemon exited unexpectedly",
    });
  }

  #emitDiagnostic(record: Parameters<DiagnosticsSink>[0]): void {
    try {
      this.#diagnostics?.(Object.freeze(record));
    } catch {
      // Diagnostics observers never alter client lifecycle behavior.
    }
  }

  #register(resources: Map<symbol, () => Promise<void>>, close: () => Promise<void>): () => void {
    this.#assertOpen();
    const id = Symbol("client-resource");
    resources.set(id, close);
    return () => resources.delete(id);
  }

  async #teardown(step: string, action: () => Promise<unknown> | unknown): Promise<void> {
    try {
      this.#onTeardownStep?.(step);
    } catch {
      // The internal lifecycle observer cannot alter teardown.
    }
    try {
      await action();
    } catch (error) {
      this.#emitDiagnostic({
        code: "client_disposal_failed",
        fields: Object.freeze({
          errorName: error instanceof Error ? error.name : typeof error,
          step,
        }),
        level: "error",
        message: `Client disposal could not complete the ${step} step`,
      });
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
    options?: CallOptions,
  ): Promise<MessageShape<O>> {
    this.#assertOpen();
    if (this.#requestStatus === "offline") this.#setRequestStatus("reconnecting");
    try {
      const response = await this.#raw.unary(method, input, {
        ...options,
        signal:
          options?.signal === undefined
            ? this.#abort.signal
            : AbortSignal.any([this.#abort.signal, options.signal]),
      });
      this.#setRequestStatus("online");
      return response;
    } catch (error) {
      this.#observeError(error);
      throw error;
    }
  }

  async #cancelRun(sessionId: string, runId: string): Promise<void> {
    this.#assertOpen();
    if (this.#transportKind === "grpc") {
      throw new UnsupportedFeatureError("prompt_free_controls", { transport: "grpc" });
    }
    const cancel = registeredTransportOperations(this.#transport)?.cancelRun;
    if (cancel === undefined) {
      throw new UnsupportedFeatureError("attached_cancel", { transport: "http" });
    }
    if (this.#requestStatus === "offline") this.#setRequestStatus("reconnecting");
    try {
      await cancel(sessionId, runId, this.#abort.signal);
      this.#setRequestStatus("online");
    } catch (error) {
      this.#observeError(error);
      throw error;
    }
  }

  async #features(): Promise<ReadonlySet<string>> {
    this.#assertOpen();
    if (this.#requestStatus === "offline") this.#setRequestStatus("reconnecting");
    try {
      const features = await this.#raw.features({ signal: this.#abort.signal });
      this.#setRequestStatus("online");
      return features;
    } catch (error) {
      this.#observeError(error);
      throw error;
    }
  }

  #stream<I extends DescMessage, O extends DescMessage>(
    method: DescMethodStreaming<I, O>,
    input: AsyncIterable<MessageInitShape<I>>,
    options?: CallOptions,
  ): AsyncIterable<MessageShape<O>> {
    this.#assertOpen();
    const raw = this.#raw.stream(method, input, {
      ...options,
      signal:
        options?.signal === undefined
          ? this.#abort.signal
          : AbortSignal.any([this.#abort.signal, options.signal]),
    });
    const observeError = (error: unknown) => this.#observeError(error);
    const publishOnline = () => this.#setRequestStatus("online");
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

  #watch<I extends DescMessage, O extends DescMessage>(
    method: DescMethodStreaming<I, O>,
    input: AsyncIterable<MessageInitShape<I>>,
    signal: AbortSignal,
  ): AsyncIterable<MessageShape<O>> {
    this.#assertOpen();
    return this.#raw.stream(method, input, {
      signal: AbortSignal.any([this.#abort.signal, signal]),
    });
  }

  async #probe(raw: RawClient, signal: AbortSignal = this.#abort.signal): Promise<void> {
    this.#assertOpen();
    if (this.#requestStatus === "offline") this.#setRequestStatus("reconnecting");
    try {
      await raw.unary(
        HarnessService.method.getCompatibilityInfo,
        {},
        {
          signal,
        },
      );
      this.#setRequestStatus("online");
    } catch (error) {
      this.#observeError(error);
      throw error;
    }
  }

  #observeError(error: unknown): void {
    if (this.#closed) return;
    if (error instanceof AuthenticationError) {
      this.#setRequestStatus("unauthorized");
      return;
    }
    if (error instanceof IncompatibleServerError) {
      this.#setRequestStatus("incompatible");
      return;
    }
    if (error instanceof TransportError) {
      this.#setRequestStatus("reconnecting");
      this.#setRequestStatus("offline");
      return;
    }
    if (error instanceof ServerError) this.#setRequestStatus("online");
  }

  #createAttachmentStatus(): AttachmentStatusWriter {
    const id = Symbol("attachment-status");
    let open = true;
    this.#attachmentStatuses.set(id, "online");
    this.#publishResolvedStatus();
    return {
      close: () => {
        if (!open) return;
        open = false;
        this.#attachmentStatuses.delete(id);
        this.#publishResolvedStatus();
      },
      set: (status) => {
        if (!open || this.#closed || this.#terminalError !== undefined) return;
        this.#attachmentStatuses.set(id, status);
        // A terminal floor failure remains useful after its attachment closes;
        // the next successful ordinary exchange clears the deployment fact.
        if (status === "incompatible") this.#requestStatus = status;
        this.#publishResolvedStatus();
      },
    };
  }

  #setRequestStatus(status: ConnectionStatus): void {
    if (this.#closed || this.#terminalError !== undefined) return;
    this.#requestStatus = status;
    this.#publishResolvedStatus();
  }

  #publishResolvedStatus(): void {
    const inputs = new Set<ConnectionStatus>([
      this.#requestStatus,
      ...this.#attachmentStatuses.values(),
    ]);
    const status = CONNECTION_STATUS_PRECEDENCE.find((candidate) => inputs.has(candidate));
    if (status === undefined) return;
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

async function* singleValue<T>(value: T): AsyncGenerator<T> {
  yield value;
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

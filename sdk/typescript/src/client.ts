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
  normalizeError,
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
  type GetGuardrailReviewDetailResponse,
  HarnessService,
  type ListGuardrailCoverageResponse,
  type ListSessionsRequest,
  type ListSessionsResponse,
  type Session as ProtoSession,
  type WatchSessionEventsResponse,
} from "./gen/mecatl/v1/harness_pb.js";
import { createHttpTransport, type HttpTransportOptions } from "./http.js";
import { createMcpAuthorization, type McpAuthorization } from "./mcp-authorization.js";
import {
  type McpConnectorInventory,
  projectMcpConnectorInventory,
  projectWorkspaceEnrollment,
  type WorkspaceEnrollment,
} from "./mcp-workspace-enrollment.js";
import {
  assertArtifactCapability,
  encodePrompt,
  type PromptCapabilities,
  type PromptInput,
  validateArtifactId,
} from "./media.js";
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
  PDF_CHUNK_BYTES,
  PDF_MAX_BYTES,
  type PdfSource,
  pdfUploadFrames,
  validatePdfUpload,
} from "./pdf-upload.js";
import { createPlanResolution, type PlanApprovalVerdict, type PlanResolution } from "./plan.js";
import {
  createRawClient,
  invalidateRawCompatibility,
  invalidateRawCompatibilityGeneration,
  type RawClient,
  readRawCompatibility,
  refreshRawCompatibility,
  registeredTransportOperations,
  sessionAffinityIfRepresentable,
} from "./raw.js";
import { type ConverseFrame, type Run, RunImpl, type RunOptions } from "./run.js";
import { createRunControls, type RunControls } from "./run-controls.js";
import {
  createServer,
  projectServerCompatibility,
  type Server,
  type ServerCompatibility,
  ServerFeature,
} from "./server.js";
import {
  projectSessionSnapshot,
  projectSessionTranscript,
  type SessionCapabilities,
  type SessionMode,
  type SessionSnapshot,
  type SessionTranscript,
} from "./session-projections.js";
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
  /** Returns the client's current connection status. */
  getSnapshot(): ConnectionStatus;
  /** Registers a listener and returns a function that removes it. */
  subscribe(listener: ConnectionStatusListener): () => void;
}

/** Options accepted by the isomorphic entry point when injecting a transport. @public */
export interface InjectedTransportOptions {
  /** A caller-owned Connect-ES transport. */
  transport: Transport;
  /** Required only when an unregistered transport speaks the HTTP/JSON/SSE protocol. */
  transportKind?: TransportKind;
}

/** Options accepted by the isomorphic connect() entry point. @public */
export type ConnectOptions = HttpTransportOptions | InjectedTransportOptions;

/** Optional stop conditions for a newly created session. @public */
export interface SessionLimits {
  /** Maximum consecutive tool failures; zero disables this limit. */
  maxConsecutiveFailures?: number;
  /** Maximum tool calls; zero disables this limit. */
  maxToolCalls?: number;
  /** Maximum model turns; zero disables this limit. */
  maxTurns?: number;
}

/** A client-provided streaming-HTTP MCP server. @public */
export interface SessionMcpServer {
  /** Command-shaped value used only to reject unsupported stdio configurations. */
  command?: string;
  /** HTTP headers sent to the MCP server. Treat their values as secrets. */
  headers?: Record<string, string>;
  /** Stable server name used in namespaced MCP tool names. */
  name?: string;
  /** Transport type. The server accepts `http` or an empty value with a URL. */
  type?: string;
  /** Absolute HTTPS endpoint, or an HTTP endpoint on an explicit loopback host. */
  url?: string;
}

/** Session-creation fields map directly onto CreateSessionRequest. @public */
export interface CreateSessionOptions {
  /** Configured server-global MCP servers selected for a diagnostic session. */
  debugMcpServers?: string[];
  /** Existing session ID used to create a separate diagnostic session. */
  debugTargetSessionId?: string;
  /** Stop conditions for the new session. */
  limits?: SessionLimits;
  /** Client-provided streaming-HTTP MCP servers mounted for this session. */
  mcpServers?: SessionMcpServer[];
  /** Permission posture for the new session. */
  mode?: SessionMode;
  /** Model selector within `providerId`. */
  modelId?: string;
  /** Tool-surface profile, or the deployment default when omitted. */
  profile?: string;
  /** Configured model-provider ID, or the deployment default when omitted. */
  providerId?: string;
  /** Requested reasoning-effort tier. The server reports the effective value. */
  reasoningEffort?: string;
}

/** Optional overrides accepted when forking a session. @public */
export interface ForkSessionOptions {
  /** Model selector within `providerId`. */
  modelId?: string;
  /** Configured model-provider ID. */
  providerId?: string;
  /** Requested reasoning-effort tier for the forked session. */
  reasoningEffort?: string;
  /** Human-readable title for the forked session. */
  title?: string;
  /** Opaque source-scoped selector for an existing worktree. */
  worktreeSelector?: string;
}

/** Optional overrides accepted when clearing a session. @public */
export interface ClearSessionOptions {
  /** Opaque source-scoped selector for an existing worktree. */
  worktreeSelector?: string;
}

/** Bounded metadata for one session-owned uploaded artifact. @public */
export interface UploadedArtifact {
  readonly artifactId: string;
  readonly name: string;
  readonly mimeType: string;
  readonly size: bigint;
  readonly sha256: string;
}

/** A durable Mecatl session handle. @public */
export interface Session {
  readonly id: string;
  /**
   * Binds one external authorization ID to this session without performing I/O.
   *
   * This handle consumes session-scoped ToolHive broker authorization handoffs.
   * It does not administer direct or global MCP profiles or their credentials.
   *
   * @param authorizationId - Exact non-empty ID from an authorization event.
   * @returns A reusable correlation handle that makes no authorization-state assertion.
   */
  mcpAuthorization(authorizationId: string): McpAuthorization;
  /**
   * Reads the current broker connector inventory for this session.
   *
   * The inventory describes broker-local publication rather than connector
   * health or enrollment-attempt history. This method performs one target
   * request and never starts enrollment or a direct MCP operation.
   *
   * @param options - Request headers, cancellation signal, and deadline.
   * @returns A detached SDK-owned connector inventory projection.
   */
  listMcpConnectors(options?: RequestOptions): Promise<McpConnectorInventory>;
  /**
   * Reads the effective guardrail coverage for this session.
   *
   * The server authorizes this diagnostic for the session owner. This RPC is
   * available over gRPC; HTTP transport reports `UnsupportedFeatureError`.
   *
   * @param options - Request headers, cancellation signal, and deadline.
   * @returns The effective checker configuration and rule coverage.
   */
  guardrailCoverage(options?: RequestOptions): Promise<ListGuardrailCoverageResponse>;
  /**
   * Reads bounded live detail for one guardrail review in this session.
   *
   * The server authorizes this diagnostic for the session owner. This RPC is
   * available over gRPC; HTTP transport reports `UnsupportedFeatureError`.
   *
   * @param reviewId - Review ID from the session's guardrail event.
   * @param options - Request headers, cancellation signal, and deadline.
   * @returns The review concern, source display, and next action.
   */
  guardrailReviewDetail(
    reviewId: string,
    options?: RequestOptions,
  ): Promise<GetGuardrailReviewDetailResponse>;
  /**
   * Starts or observes this session's whole-bundle workspace enrollment.
   *
   * Each invocation performs one target request. The SDK does not poll, retry,
   * open a browser, or retain the returned presentation URL.
   *
   * @param options - Request headers, cancellation signal, and deadline.
   * @returns The immediate enrollment state and an ephemeral URL while pending.
   * @throws `ProtocolError` when the successful response is structurally malformed.
   */
  connectWorkspaceServices(options?: RequestOptions): Promise<WorkspaceEnrollment>;
  /**
   * Replaces one exact pending workspace-enrollment correlation.
   *
   * Use this explicit operation when the application retained a pending
   * correlation but lost its presentation URL. The SDK performs no automatic
   * recovery after an ambiguous unary result.
   *
   * @param enrollmentId - Exact prior enrollment correlation to replace.
   * @param options - Request headers, cancellation signal, and deadline.
   * @returns A replacement enrollment with a distinct correlation.
   * @throws `ProtocolError` when the replacement correlation is missing or unchanged.
   */
  retryWorkspaceEnrollment(
    enrollmentId: string,
    options?: RequestOptions,
  ): Promise<WorkspaceEnrollment>;
  /**
   * Cancels one exact workspace-enrollment correlation.
   *
   * The SDK accepts terminal server outcomes and leaves a future `unknown`
   * value uninterpreted. It sends no follow-up request.
   *
   * @param enrollmentId - Exact enrollment correlation to cancel.
   * @param options - Request headers, cancellation signal, and deadline.
   * @returns The terminal server result, or `unknown` for a future result state.
   * @throws `ProtocolError` when the returned correlation differs or a known result is pending.
   */
  cancelWorkspaceEnrollment(
    enrollmentId: string,
    options?: RequestOptions,
  ): Promise<WorkspaceEnrollment>;
  /**
   * Creates prompt-free controls bound to one exact run without opening a watch.
   *
   * @param runId - Exact durable run ID to address.
   * @returns A synchronous lightweight control resource.
   */
  controls(runId: string): RunControls;
  /**
   * Attaches to an explicit run, or selects the newest run in the durable log.
   *
   * @param runId - Run ID to follow. Omit it to select the newest run.
   * @param options - Replay position, event filtering, and cancellation options.
   * @returns A single-consumption durable stream bound to the selected run.
   * @throws `NoRunsError` when no run can be selected.
   * @throws `CursorScopeError` when a cursor would widen its original filter.
   */
  attach(runId?: string, options?: AttachOptions): Promise<AttachedRun>;
  /**
   * Opens the durable cross-run activity stream for this session.
   *
   * @param options - Replay position, event filtering, and cancellation options.
   * @returns A single-consumption stream of session activity.
   * @throws `CursorScopeError` when a cursor would widen its original filter.
   */
  activity(options?: AttachOptions): Promise<SessionActivity>;
  /**
   * Reads the authoritative current session snapshot.
   *
   * @param options - Request headers, cancellation signal, and deadline.
   * @returns A detached SDK-owned projection of the session aggregate.
   * @throws `ProtocolError` when the server response is missing or mismatched.
   */
  snapshot(options?: RequestOptions): Promise<SessionSnapshot>;
  /**
   * Reads the authoritative model-visible conversation.
   *
   * @param options - Request headers, cancellation signal, and deadline.
   * @returns The ordered transcript without provider-private replay fields.
   * @throws `ProtocolError` when the server response is missing or mismatched.
   */
  transcript(options?: RequestOptions): Promise<SessionTranscript>;
  /**
   * Replaces the title of an eligible session.
   *
   * @param title - New human-readable title.
   * @param options - Request headers, cancellation signal, and deadline.
   * @returns The resulting authoritative snapshot.
   */
  rename(title: string, options?: RequestOptions): Promise<SessionSnapshot>;
  /**
   * Changes the permission posture of an eligible session.
   *
   * @param mode - New SDK permission mode.
   * @param options - Request headers, cancellation signal, and deadline.
   * @returns The resulting authoritative snapshot.
   */
  setMode(mode: SessionMode, options?: RequestOptions): Promise<SessionSnapshot>;
  /**
   * Requests one out-of-band compaction pass.
   *
   * @param options - Request headers, cancellation signal, and deadline.
   * @returns Whether the server reduced the model-visible history.
   */
  compact(options?: RequestOptions): Promise<boolean>;
  /**
   * Creates an empty-history successor without changing this handle.
   *
   * @param options - Optional opaque worktree selector.
   * @param requestOptions - Request headers, cancellation signal, and deadline.
   * @returns A distinct session handle for the successor.
   */
  clear(options?: ClearSessionOptions, requestOptions?: RequestOptions): Promise<Session>;
  /**
   * Streams one PDF into a session-owned artifact for later prompts or steers.
   *
   * @param source - Browser Blob or async source of PDF bytes.
   * @param options - A safe basename and the PDF MIME type.
   * @param requestOptions - Request headers, cancellation signal, and deadline.
   * @returns An opaque artifact ID and validated metadata for a later prompt.
   * @throws `UnsupportedFeatureError` when artifact storage is unavailable.
   * @throws `PromptValidationError` for invalid upload metadata or source bytes.
   */
  uploadArtifact(
    source: Blob | AsyncIterable<Uint8Array>,
    options: { name: string; mimeType: "application/pdf" },
    requestOptions?: RequestOptions,
  ): Promise<UploadedArtifact>;
  /**
   * Streams a session-owned PDF artifact in ordered byte chunks.
   *
   * The stream starts on first consumption. Returning from its iterator closes
   * the transport response and cancels any unfinished download.
   *
   * @param artifactId - Opaque ID from a PDF tool-result block.
   * @param requestOptions - Request headers, cancellation signal, and deadline.
   * @returns An async byte stream with no whole-file buffer.
   * @throws `UnsupportedFeatureError` when artifact storage is unavailable.
   */
  downloadArtifact(artifactId: string, requestOptions?: RequestOptions): AsyncIterable<Uint8Array>;
  /**
   * Starts a run and resolves once its first run-ID-bearing event arrives.
   *
   * @param prompt - Text or ordered text, image, audio, and PDF parts for the run.
   * @param options - Automatic permission and plan-approval responders.
   * @param requestOptions - Request headers, cancellation signal, and deadline.
   * @returns A single-consumption handle for the accepted run.
   * @throws `PromptValidationError` when the prompt is invalid or unsupported.
   * @throws `SessionBusyError` when the session already has an active run.
   */
  run(prompt: PromptInput, options?: RunOptions, requestOptions?: RequestOptions): Promise<Run>;
  /**
   * Retries the server-selected eligible failed model step.
   *
   * @param options - Automatic permission and plan-approval responders.
   * @param requestOptions - Request headers, cancellation signal, and deadline.
   * @returns The same single-consumption run lifecycle returned by run().
   * @throws `SessionBusyError` when the session already has an active run.
   */
  retry(options?: RunOptions, requestOptions?: RequestOptions): Promise<Run>;
  /**
   * Atomically resolves a durably parked plan and streams its resumed and continuation runs.
   *
   * @param verdict - Plan decision. Defaults to `approve`.
   * @returns A single-consumption plan-resolution stream.
   * @throws `ServerError` when the session has no parked plan awaiting approval.
   */
  resolvePlan(verdict?: PlanApprovalVerdict): PlanResolution;
  /**
   * Releases runtime resources without removing the durable session.
   *
   * @param options - Request headers, cancellation signal, and deadline.
   * @returns A promise that resolves after local session resources are released.
   */
  close(options?: RequestOptions): Promise<void>;
  /**
   * Permanently removes the durable session and its sidecars.
   *
   * @param options - Request headers, cancellation signal, and deadline.
   * @returns A promise that resolves after the server removes the session.
   */
  delete(options?: RequestOptions): Promise<void>;
}

/** Session lifecycle operations exposed by a Client. @public */
export interface Sessions {
  /**
   * Creates a session and returns its handle.
   *
   * @param options - Session configuration fields.
   * @param requestOptions - Request headers, cancellation signal, and deadline.
   * @returns A handle for the newly created session.
   */
  create(options: CreateSessionOptions, requestOptions?: RequestOptions): Promise<Session>;
  /**
   * Loads an existing session by ID.
   *
   * @param sessionId - Durable session ID to load.
   * @param options - Request headers, cancellation signal, and deadline.
   * @returns A handle bound to the requested session.
   */
  get(sessionId: string, options?: RequestOptions): Promise<Session>;
  /**
   * Forks an existing session into a distinct successor.
   *
   * @param sourceSessionId - Session whose conversation will be copied.
   * @param options - Optional title, model, reasoning, and worktree overrides.
   * @param requestOptions - Request headers, cancellation signal, and deadline.
   * @returns A handle for the forked successor session.
   */
  fork(
    sourceSessionId: string,
    options?: ForkSessionOptions,
    requestOptions?: RequestOptions,
  ): Promise<Session>;
  /** Lists the sessions visible to the authenticated caller. */
  list(request: ListSessionsRequest, options?: RequestOptions): Promise<ListSessionsResponse>;
}

/** The high-level Mecatl client. @public */
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
  readonly server: Server;
  readonly sessions: Sessions;
  readonly skills: Skills;
  readonly soul: Soul;
  readonly status: ConnectionStatusStore;
  readonly storage: Storage;
  readonly teams: Teams;
  readonly userModel: UserModel;
  readonly worktrees: Worktrees;
  /** Releases activity, transports, and resources owned by this client. */
  close(): Promise<void>;
  /** Releases the same resources as close() when used with await using. */
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
  features(options?: RequestOptions): Promise<ReadonlySet<string>>;
  getSessionHandle(sessionId: string, options?: RequestOptions): Promise<Session>;
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

function createSessionAffinity(
  options: CreateSessionOptions,
  requestOptions?: RequestOptions,
): CallOptions | undefined {
  const reference = options.debugTargetSessionId;
  return reference === undefined || reference === ""
    ? requestOptions
    : sessionAffinityIfRepresentable(reference, requestOptions);
}

function promptCapabilities(
  sessionCapabilities: SessionCapabilities | undefined,
  serverCapabilities?: {
    readonly audio: boolean;
    readonly image: boolean;
    readonly pdf?: boolean;
    readonly artifacts?: boolean;
  },
): PromptCapabilities | undefined {
  const value = sessionCapabilities ?? serverCapabilities;
  return value === undefined
    ? undefined
    : {
        audio: value.audio,
        image: value.image,
        pdf: sessionCapabilities?.pdf ?? serverCapabilities?.pdf === true,
        artifacts: serverCapabilities?.artifacts === true,
      };
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

function assertRequestNotAborted(
  options: RequestOptions | undefined,
  transport: TransportKind,
): void {
  if (options?.signal?.aborted === true) throw normalizeError(options.signal.reason, transport);
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
  readonly #baseOperations: SessionOperations;
  readonly #operations: SessionOperations;
  #promptCapabilities: PromptCapabilities | undefined;
  #busy = false;

  constructor(
    id: string,
    operations: SessionOperations,
    promptCapabilities: PromptCapabilities | undefined,
  ) {
    this.id = id;
    this.#baseOperations = operations;
    this.#operations = sessionAffinityOperations(this.id, operations);
    this.#promptCapabilities = promptCapabilities;
  }

  controls(runId: string): RunControls {
    return createRunControls(this.id, runId, {
      assertOpen: () => this.#operations.assertOpen(),
      features: (options) => this.#operations.features(options),
      promptCapabilities: () => this.#promptCapabilities,
      transportKind: this.#operations.transportKind,
      unary: (method, input, options) => this.#operations.unary(method, input, options),
    });
  }

  async listMcpConnectors(options?: RequestOptions): Promise<McpConnectorInventory> {
    this.#operations.assertOpen();
    assertRequestNotAborted(options, this.#operations.transportKind);
    const response = await this.#operations.unary(
      HarnessService.method.listSessionMcpConnectors,
      { sessionId: this.id },
      options,
    );
    return projectMcpConnectorInventory(response);
  }

  async guardrailCoverage(options?: RequestOptions): Promise<ListGuardrailCoverageResponse> {
    this.#operations.assertOpen();
    return this.#operations.unary(
      HarnessService.method.listGuardrailCoverage,
      { sessionId: this.id },
      options,
    );
  }

  async guardrailReviewDetail(
    reviewId: string,
    options?: RequestOptions,
  ): Promise<GetGuardrailReviewDetailResponse> {
    this.#operations.assertOpen();
    return this.#operations.unary(
      HarnessService.method.getGuardrailReviewDetail,
      { reviewId, sessionId: this.id },
      options,
    );
  }

  async connectWorkspaceServices(options?: RequestOptions): Promise<WorkspaceEnrollment> {
    this.#operations.assertOpen();
    assertRequestNotAborted(options, this.#operations.transportKind);
    const response = await this.#operations.unary(
      HarnessService.method.connectWorkspaceServices,
      { sessionId: this.id },
      options,
    );
    return projectWorkspaceEnrollment(
      response,
      { kind: "connect" },
      this.#operations.transportKind,
    );
  }

  async retryWorkspaceEnrollment(
    enrollmentId: string,
    options?: RequestOptions,
  ): Promise<WorkspaceEnrollment> {
    this.#operations.assertOpen();
    assertRequestNotAborted(options, this.#operations.transportKind);
    const response = await this.#operations.unary(
      HarnessService.method.retryWorkspaceEnrollment,
      { enrollmentId, sessionId: this.id },
      options,
    );
    return projectWorkspaceEnrollment(
      response,
      { enrollmentId, kind: "retry" },
      this.#operations.transportKind,
    );
  }

  async cancelWorkspaceEnrollment(
    enrollmentId: string,
    options?: RequestOptions,
  ): Promise<WorkspaceEnrollment> {
    this.#operations.assertOpen();
    assertRequestNotAborted(options, this.#operations.transportKind);
    const response = await this.#operations.unary(
      HarnessService.method.cancelWorkspaceEnrollment,
      { enrollmentId, sessionId: this.id },
      options,
    );
    return projectWorkspaceEnrollment(
      response,
      { enrollmentId, kind: "cancel" },
      this.#operations.transportKind,
    );
  }

  mcpAuthorization(authorizationId: string): McpAuthorization {
    return createMcpAuthorization(this.id, authorizationId, {
      ...this.#operations,
      promptCapabilities: () => this.#promptCapabilities,
    });
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

  async snapshot(options?: RequestOptions): Promise<SessionSnapshot> {
    this.#operations.assertOpen();
    const response = await this.#operations.unary(
      HarnessService.method.getSession,
      { sessionId: this.id },
      options,
    );
    return this.#snapshot(response.session, "GetSession");
  }

  async transcript(options?: RequestOptions): Promise<SessionTranscript> {
    this.#operations.assertOpen();
    const response = await this.#operations.unary(
      HarnessService.method.getSessionTranscript,
      { sessionId: this.id },
      options,
    );
    const transcript = projectSessionTranscript(response, this.id);
    if (transcript === undefined) {
      throw new ProtocolError("GetSessionTranscript returned a missing or mismatched session", {
        transport: this.#operations.transportKind,
      });
    }
    return transcript;
  }

  async rename(title: string, options?: RequestOptions): Promise<SessionSnapshot> {
    this.#operations.assertOpen();
    const response = await this.#operations.unary(
      HarnessService.method.renameSession,
      { sessionId: this.id, title },
      options,
    );
    return this.#snapshot(response.session, "RenameSession");
  }

  async setMode(mode: SessionMode, options?: RequestOptions): Promise<SessionSnapshot> {
    this.#operations.assertOpen();
    const response = await this.#operations.unary(
      HarnessService.method.setMode,
      { mode, sessionId: this.id },
      options,
    );
    return this.#snapshot(response.session, "SetMode");
  }

  async compact(options?: RequestOptions): Promise<boolean> {
    this.#operations.assertOpen();
    const response = await this.#operations.unary(
      HarnessService.method.compactSession,
      { sessionId: this.id },
      options,
    );
    return response.compacted;
  }

  async clear(
    options: ClearSessionOptions = {},
    requestOptions?: RequestOptions,
  ): Promise<Session> {
    this.#operations.assertOpen();
    const response = await this.#operations.unary(
      HarnessService.method.clearSession,
      { ...options, sourceSessionId: this.id },
      requestOptions,
    );
    if (response.sessionId === "") {
      throw new ProtocolError("ClearSession returned no session id", {
        transport: this.#operations.transportKind,
      });
    }
    return this.#baseOperations.getSessionHandle(response.sessionId, requestOptions);
  }

  async uploadArtifact(
    source: PdfSource,
    options: { name: string; mimeType: "application/pdf" },
    requestOptions?: RequestOptions,
  ): Promise<UploadedArtifact> {
    this.#operations.assertOpen();
    assertRequestNotAborted(requestOptions, this.#operations.transportKind);
    validatePdfUpload(source, options?.name, options?.mimeType);
    assertArtifactCapability(this.#promptCapabilities);
    const signals = [this.#operations.clientSignal];
    if (requestOptions?.signal !== undefined) signals.push(requestOptions.signal);
    if (requestOptions?.timeoutMs !== undefined && requestOptions.timeoutMs > 0) {
      signals.push(AbortSignal.timeout(requestOptions.timeoutMs));
    }
    const signal = AbortSignal.any(signals);
    let localFailure: unknown;
    let failedLocally = false;
    let response: UploadedArtifact | undefined;
    try {
      for await (const frame of this.#operations.stream(
        HarnessService.method.uploadArtifact,
        pdfUploadFrames(
          source,
          this.id,
          options.name,
          signal,
          this.#operations.transportKind,
          (cause) => {
            localFailure = cause;
            failedLocally = true;
          },
        ),
        { ...requestOptions, signal },
      )) {
        if (response !== undefined) {
          throw new ProtocolError("UploadArtifact returned more than one response", {
            transport: this.#operations.transportKind,
          });
        }
        response = frame;
      }
    } catch (cause) {
      if (failedLocally) throw normalizeError(localFailure, this.#operations.transportKind);
      throw cause;
    }
    if (
      response === undefined ||
      typeof response.artifactId !== "string" ||
      response.artifactId.trim() === "" ||
      response.artifactId !== response.artifactId.trim() ||
      /[\p{Cc}\p{Cf}]/u.test(response.artifactId) ||
      response.name !== options.name ||
      response.mimeType !== options.mimeType ||
      typeof response.size !== "bigint" ||
      response.size < 1n ||
      response.size > BigInt(PDF_MAX_BYTES) ||
      typeof response.sha256 !== "string" ||
      !/^[a-f0-9]{64}$/u.test(response.sha256)
    ) {
      throw new ProtocolError("UploadArtifact returned invalid artifact metadata", {
        transport: this.#operations.transportKind,
      });
    }
    return {
      artifactId: response.artifactId,
      name: response.name,
      mimeType: response.mimeType,
      size: response.size,
      sha256: response.sha256,
    };
  }

  async *downloadArtifact(
    artifactId: string,
    requestOptions?: RequestOptions,
  ): AsyncIterable<Uint8Array> {
    this.#operations.assertOpen();
    assertRequestNotAborted(requestOptions, this.#operations.transportKind);
    validateArtifactId(artifactId);
    assertArtifactCapability(this.#promptCapabilities);
    const cancel = new AbortController();
    const signals = [this.#operations.clientSignal, cancel.signal];
    if (requestOptions?.signal !== undefined) signals.push(requestOptions.signal);
    const signal = AbortSignal.any(signals);
    const sessionId = this.id;
    let total = 0;
    const frames = this.#operations
      .stream(
        HarnessService.method.downloadArtifact,
        (async function* () {
          yield { sessionId, artifactId };
        })(),
        { ...requestOptions, signal },
      )
      [Symbol.asyncIterator]();
    try {
      for (;;) {
        const next = await frames.next();
        if (next.done) break;
        const frame = next.value;
        const chunk = frame.chunk;
        if (
          !(chunk instanceof Uint8Array) ||
          chunk.byteLength === 0 ||
          chunk.byteLength > PDF_CHUNK_BYTES
        ) {
          throw new ProtocolError("DownloadArtifact returned an invalid chunk", {
            transport: this.#operations.transportKind,
          });
        }
        total += chunk.byteLength;
        if (total > PDF_MAX_BYTES) {
          throw new ProtocolError("DownloadArtifact exceeds the PDF size limit", {
            transport: this.#operations.transportKind,
          });
        }
        yield chunk;
      }
    } finally {
      cancel.abort();
      await frames.return?.();
    }
  }

  async run(
    prompt: PromptInput,
    options: RunOptions = {},
    requestOptions?: RequestOptions,
  ): Promise<Run> {
    this.#assertRunAvailable();
    const encoded = encodePrompt(prompt, this.#promptCapabilities);
    if (options.serverOwnedPlanContinuation === true) {
      if (options.onPlanApproval !== undefined) {
        throw new InvalidStateError("Server-owned plan continuation cannot use onPlanApproval", {
          transport: this.#operations.transportKind,
        });
      }
      // Reserve admission before awaiting compatibility; another run must not
      // pass the local busy check while this one is still preflighting.
      this.#busy = true;
      try {
        const features = await this.#operations.features(requestOptions);
        if (!features.has(ServerFeature.ExactPlanAskControl)) {
          throw new UnsupportedFeatureError(ServerFeature.ExactPlanAskControl, {
            transport: this.#operations.transportKind,
          });
        }
      } catch (error) {
        this.#busy = false;
        throw error;
      }
    }
    return this.#startRun(
      {
        kind: {
          case: "prompt",
          value: {
            parts: encoded.media.map((part) =>
              create(ContentSchema, {
                ...(part.kind === "pdf"
                  ? { artifactId: part.artifactId }
                  : {
                      ...(part.bytes === undefined ? {} : { data: part.bytes }),
                      ...(part.url === undefined ? {} : { url: part.url }),
                    }),
                kind: part.kind === "image" ? 1 : part.kind === "audio" ? 2 : 3,
                mimeType: part.kind === "pdf" ? "application/pdf" : part.mimeType,
              }),
            ),
            sessionId: this.id,
            text: encoded.text,
            ...(options.serverOwnedPlanContinuation === true
              ? { serverOwnedPlanContinuation: true }
              : {}),
          },
        },
      },
      options,
      requestOptions,
    );
  }

  async retry(options: RunOptions = {}, requestOptions?: RequestOptions): Promise<Run> {
    this.#assertRunAvailable();
    if (options.serverOwnedPlanContinuation === true) {
      throw new InvalidStateError("retry() cannot opt into server-owned plan continuation", {
        transport: this.#operations.transportKind,
      });
    }
    return this.#startRun(
      { kind: { case: "retry", value: { sessionId: this.id } } },
      options,
      requestOptions,
    );
  }

  #assertRunAvailable(): void {
    this.#operations.assertOpen();
    if (this.#busy) {
      throw new SessionBusyError("A run is already active on this Session", {
        transport: this.#operations.transportKind,
      });
    }
  }

  async #startRun(
    start: ConverseFrame,
    options: RunOptions,
    requestOptions?: RequestOptions,
  ): Promise<Run> {
    this.#busy = true;
    const input = new ConverseInput(start, this.#operations.transportKind);
    const runAbort = new AbortController();
    const signal =
      requestOptions?.signal === undefined
        ? runAbort.signal
        : AbortSignal.any([requestOptions.signal, runAbort.signal]);
    const responses = this.#operations
      .stream(HarnessService.method.converse, input, { ...requestOptions, signal })
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

  #snapshot(value: ProtoSession | undefined, operation: string): SessionSnapshot {
    const snapshot = value === undefined ? undefined : projectSessionSnapshot(value, this.id);
    if (snapshot === undefined) {
      throw new ProtocolError(`${operation} returned a missing or mismatched session`, {
        transport: this.#operations.transportKind,
      });
    }
    const refreshed = promptCapabilities(snapshot.sessionCapabilities, this.#promptCapabilities);
    if (refreshed !== undefined) this.#promptCapabilities = refreshed;
    return snapshot;
  }

  resolvePlan(verdict: PlanApprovalVerdict = "approve"): PlanResolution {
    this.#operations.assertOpen();
    if (this.#busy) {
      throw new SessionBusyError("A run is already active on this Session", {
        transport: this.#operations.transportKind,
      });
    }
    this.#busy = true;
    try {
      return createPlanResolution(this.id, verdict, this.#operations, () => {
        this.#busy = false;
      });
    } catch (error) {
      this.#busy = false;
      throw error;
    }
  }

  async close(options?: RequestOptions): Promise<void> {
    this.#operations.assertOpen();
    await this.#operations.unary(
      HarnessService.method.closeSession,
      { sessionId: this.id },
      options,
    );
  }

  async delete(options?: RequestOptions): Promise<void> {
    this.#operations.assertOpen();
    await this.#operations.unary(
      HarnessService.method.deleteSession,
      { sessionId: this.id },
      options,
    );
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
  readonly server: Server;
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
      features: (options) => this.#features(options),
      getSessionHandle: (sessionId, options) => this.sessions.get(sessionId, options),
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
    this.server = createServer({
      compatibility: (requestOptions, refresh) => this.#compatibility(requestOptions, refresh),
      transportKind: this.#transportKind,
      unary: (method, input, requestOptions) => this.#unary(method, input, requestOptions),
    });
    this.sessions = {
      create: async (input, requestOptions) => {
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
            const inventory = await this.#unary(
              HarnessService.method.listMcpSources,
              {},
              requestOptions,
            );
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
          const compatibility = await this.#compatibility(requestOptions, false);
          const response = await this.#unary(
            HarnessService.method.createSession,
            request,
            createSessionAffinity(input, requestOptions),
          );
          lease?.finish(true);
          return this.#session(
            response.sessionId,
            "CreateSession",
            promptCapabilities(response.sessionCapabilities, compatibility.capabilities),
          );
        } catch (error) {
          lease?.finish(false);
          throw error;
        }
      },
      fork: async (sourceSessionId, input = {}, requestOptions) => {
        const response = await this.#unary(
          HarnessService.method.forkSession,
          {
            ...input,
            sourceSessionId,
          },
          sessionAffinityIfRepresentable(sourceSessionId, requestOptions),
        );
        if (response.sessionId === "") {
          throw new ProtocolError("ForkSession returned no session id", {
            transport: this.#transportKind,
          });
        }
        return this.sessions.get(response.sessionId, requestOptions);
      },
      get: async (sessionId, options) => {
        const compatibility = await this.#compatibility(options, false);
        const response = await this.#unary(
          HarnessService.method.getSession,
          { sessionId },
          sessionAffinityIfRepresentable(sessionId, options),
        );
        const snapshot =
          response.session === undefined
            ? undefined
            : projectSessionSnapshot(response.session, sessionId);
        if (snapshot === undefined) {
          throw new ProtocolError("GetSession returned a missing or mismatched session", {
            transport: this.#transportKind,
          });
        }
        return new SessionImpl(
          snapshot.sessionId,
          this.#operations,
          promptCapabilities(snapshot.sessionCapabilities, compatibility.capabilities),
        );
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

  #withClientSignal(options?: CallOptions): CallOptions {
    return {
      ...options,
      signal:
        options?.signal === undefined
          ? this.#abort.signal
          : AbortSignal.any([this.#abort.signal, options.signal]),
    };
  }

  async #observeRequest<T>(request: () => Promise<T>): Promise<T> {
    if (this.#requestStatus === "offline") this.#setRequestStatus("reconnecting");
    try {
      const result = await request();
      this.#setRequestStatus("online");
      return result;
    } catch (error) {
      this.#observeError(error);
      throw error;
    }
  }

  async #unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    input: MessageInitShape<I>,
    options?: CallOptions,
  ): Promise<MessageShape<O>> {
    this.#assertOpen();
    return this.#observeRequest(() =>
      this.#raw.unary(method, input, this.#withClientSignal(options)),
    );
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
    await this.#observeRequest(() => cancel(sessionId, runId, this.#abort.signal));
  }

  async #features(options?: RequestOptions): Promise<ReadonlySet<string>> {
    this.#assertOpen();
    return this.#observeRequest(() => this.#raw.features(this.#withClientSignal(options)));
  }

  async #compatibility(
    options: RequestOptions | undefined,
    refresh: boolean,
  ): Promise<ServerCompatibility> {
    this.#assertOpen();
    const requestOptions = this.#withClientSignal(options);
    return this.#observeRequest(async () => {
      const result = await (refresh
        ? refreshRawCompatibility(this.#raw, requestOptions)
        : readRawCompatibility(this.#raw, requestOptions));
      let projection: ServerCompatibility;
      try {
        projection = projectServerCompatibility(result.message, this.#transportKind);
      } catch (error) {
        invalidateRawCompatibilityGeneration(this.#raw, result.generation);
        throw error;
      }
      requestOptions.onHeader?.(result.header);
      requestOptions.onTrailer?.(result.trailer);
      return projection;
    });
  }

  #stream<I extends DescMessage, O extends DescMessage>(
    method: DescMethodStreaming<I, O>,
    input: AsyncIterable<MessageInitShape<I>>,
    options?: CallOptions,
  ): AsyncIterable<MessageShape<O>> {
    this.#assertOpen();
    const raw = this.#raw.stream(method, input, this.#withClientSignal(options));
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
    await this.#observeRequest(() =>
      raw.unary(HarnessService.method.getCompatibilityInfo, {}, { signal }),
    );
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
  let returned = false;
  const close = async () => {
    release();
    if (returned) return;
    returned = true;
    await responses.return?.();
  };
  return {
    next: async () => {
      try {
        const next = await responses.next();
        if (next.done) {
          await close();
          return { done: true, value: undefined };
        }
        const event = next.value.event;
        if (event === undefined) {
          throw new ProtocolError("The Converse stream returned a frame without an event", {
            transport,
          });
        }
        const correlatedControlEvent =
          event.type === "permission.ask" || event.type === "control.refused";
        if (event.runId !== runId && !correlatedControlEvent) {
          throw new ProtocolError("The Converse stream changed run id", { transport });
        }
        if (event.type === "result") release();
        return { done: false, value: event };
      } catch (error) {
        release();
        throw error;
      }
    },
    return: async () => {
      await close();
      return { done: true, value: undefined };
    },
  };
}

/** Internal construction seam shared with the Node entry point. */
export function connectTransport(options: ClientCoreOptions): Client {
  return new ClientImpl(options);
}

/**
 * Creates an isomorphic Client over HTTP or a caller-injected transport.
 *
 * @param options - HTTP transport settings or a caller-owned transport.
 * @returns A high-level Mecatl client.
 * @public
 */
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

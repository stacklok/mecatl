import type {
  DescMessage,
  DescMethodStreaming,
  DescMethodUnary,
  MessageInitShape,
  MessageShape,
} from "@bufbuild/protobuf";
import type { CallOptions } from "@connectrpc/connect";

import { InvalidStateError, ProtocolError, type TransportKind } from "./errors.js";
import { decodeEvent, type Event, type EventOf } from "./events.js";
import {
  HarnessService,
  type ListSessionMcpConnectorsResponse,
  type Event as ProtoEvent,
  type WorkspaceEnrollment as ProtoWorkspaceEnrollment,
} from "./gen/mecatl/v1/harness_pb.js";
import type { RequestOptions } from "./namespaces-core.js";
import type { RunResult } from "./run.js";

/** The live browser URL for one pending MCP authorization. @public */
export interface McpAuthorizationPresentation {
  /** The URL to open in a browser. It is returned only by this live control and never enters an event or snapshot. */
  readonly url: string;
}

/** The terminal outcome of an authorization recheck or cancellation stream. @public */
export interface McpAuthorizationOutcome {
  /** The terminal result of the continuation run, absent when the stream ended without one. */
  readonly result?: RunResult;
}

/**
 * The event stream an authorization control returns: the daemon's status
 * events followed by the continuation run when the tool call resumes.
 * Single-consumption: iterate it, or call result().
 * @public
 */
export interface McpAuthorizationStream extends AsyncIterable<Event> {
  /** Drains the stream and returns the continuation's terminal, when one arrived. */
  result(): Promise<McpAuthorizationOutcome>;
  /** Stops consuming the stream without cancelling the authorization. */
  close(): Promise<void>;
}

/**
 * Controls for one pending per-tool MCP authorization (`authorization.required`).
 *
 * `presentation()` fetches the live browser URL; `recheck()` asks the daemon to
 * re-inspect the authorization and streams the outcome (and the resumed run when
 * it succeeds); `cancel()` abandons the authorization with the same stream shape.
 * @public
 */
export interface McpAuthorization {
  readonly authorizationId: string;
  readonly sessionId: string;
  /**
   * Reads the live browser URL for this pending authorization.
   *
   * @param options - Request headers, cancellation signal, and deadline.
   * @returns The URL the operator opens to authorize the tool.
   */
  presentation(options?: RequestOptions): Promise<McpAuthorizationPresentation>;
  /**
   * Re-checks the authorization and streams status plus any continuation run.
   *
   * @param options - Request headers, cancellation signal, and deadline.
   * @returns A single-consumption event stream.
   */
  recheck(options?: RequestOptions): McpAuthorizationStream;
  /**
   * Cancels the authorization and streams the cancellation outcome plus any continuation run.
   *
   * @param options - Request headers, cancellation signal, and deadline.
   * @returns A single-consumption event stream.
   */
  cancel(options?: RequestOptions): McpAuthorizationStream;
}

/** One broker-local MCP connector as the session sees it. @public */
export interface McpConnectorStatus {
  readonly name: string;
  readonly catalogueState: string;
  readonly toolCount: number;
}

/** The session's broker-local MCP connector catalogue. @public */
export interface SessionMcpConnectors {
  readonly availability: string;
  readonly enrollmentState: string;
  readonly connectors: readonly McpConnectorStatus[];
  readonly totalConnectors: number;
  readonly truncated: boolean;
}

/** The safe projection of one workspace-services enrollment. @public */
export interface WorkspaceEnrollmentState {
  readonly enrollmentId: string;
  readonly status: string;
  readonly requiredServices: number;
  /** The browser URL to complete the enrollment, "" when none is pending. */
  readonly presentationUrl: string;
}

/**
 * The pre-prompt workspace-services enrollment controls for one session.
 * `connect()` begins an eligible enrollment or observes the pending one; `retry()`
 * and `cancel()` act on one exact enrollment id and fail stale with a 412 problem.
 * @public
 */
export interface WorkspaceEnrollmentControls {
  /**
   * Begins an eligible pre-prompt enrollment, or observes the exact pending one.
   *
   * @param options - Request headers, cancellation signal, and deadline.
   * @returns The safe enrollment projection, including the browser URL while pending.
   */
  connect(options?: RequestOptions): Promise<WorkspaceEnrollmentState>;
  /**
   * Cancels the exact pending enrollment and begins its replacement.
   *
   * @param enrollmentId - The enrollment to replace.
   * @param options - Request headers, cancellation signal, and deadline.
   * @returns The replacement enrollment's projection.
   * @throws `ServerError` with status 412 when the id is stale.
   */
  retry(enrollmentId: string, options?: RequestOptions): Promise<WorkspaceEnrollmentState>;
  /**
   * Cancels only the exact pending enrollment and clears its prompt gate.
   *
   * @param enrollmentId - The enrollment to cancel.
   * @param options - Request headers, cancellation signal, and deadline.
   * @returns The cancelled enrollment's projection.
   * @throws `ServerError` with status 412 when the id is stale.
   */
  cancel(enrollmentId: string, options?: RequestOptions): Promise<WorkspaceEnrollmentState>;
}

/** Internal seam shared with the client. */
export interface AuthorizationOperations {
  assertOpen(): void;
  registerRun(cancel: () => Promise<void>): () => void;
  stream<I extends DescMessage, O extends DescMessage>(
    method: DescMethodStreaming<I, O>,
    input: AsyncIterable<MessageInitShape<I>>,
    options?: CallOptions,
  ): AsyncIterable<MessageShape<O>>;
  readonly transportKind: TransportKind;
  unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    input: MessageInitShape<I>,
    options?: CallOptions,
  ): Promise<MessageShape<O>>;
}

export function projectMcpConnectors(
  value: ListSessionMcpConnectorsResponse,
): SessionMcpConnectors {
  return {
    availability: value.availability,
    connectors: value.connectors.map((connector) => ({
      catalogueState: connector.catalogueState,
      name: connector.name,
      toolCount: connector.toolCount,
    })),
    enrollmentState: value.enrollmentState,
    totalConnectors: value.totalConnectors,
    truncated: value.truncated,
  };
}

export function projectWorkspaceEnrollment(
  value: ProtoWorkspaceEnrollment,
): WorkspaceEnrollmentState {
  return {
    enrollmentId: value.enrollmentId,
    presentationUrl: value.presentationUrl,
    requiredServices: value.requiredServices,
    status: value.status,
  };
}

async function* singleValue<T>(value: T): AsyncGenerator<T> {
  yield value;
}

type EventCarrier = { readonly event?: ProtoEvent | undefined };

class AuthorizationStreamImpl implements McpAuthorizationStream {
  readonly #abort: AbortController;
  readonly #frames: AsyncIterator<EventCarrier>;
  readonly #operations: AuthorizationOperations;
  readonly #release: () => void;
  readonly #sessionId: string;
  #claimed = false;
  #ended = false;
  #lastRunId = "";
  #terminal: EventOf<"result"> | undefined;

  constructor(
    sessionId: string,
    frames: AsyncIterator<EventCarrier>,
    operations: AuthorizationOperations,
    abort: AbortController,
    release: () => void,
  ) {
    this.#sessionId = sessionId;
    this.#frames = frames;
    this.#operations = operations;
    this.#abort = abort;
    this.#release = release;
  }

  [Symbol.asyncIterator](): AsyncIterator<Event> {
    this.#claim();
    return {
      next: () => this.#next(),
      return: async () => {
        await this.close();
        return { done: true, value: undefined };
      },
    };
  }

  async result(): Promise<McpAuthorizationOutcome> {
    this.#claim();
    for (;;) {
      const next = await this.#next();
      if (next.done) break;
    }
    const terminal = this.#terminal;
    if (terminal === undefined) return {};
    return {
      result: {
        content: terminal.payload.text,
        rawEvent: terminal,
        runId: this.#lastRunId,
        sessionId: this.#sessionId,
        stopReason: terminal.payload.stop,
        text: terminal.payload.text,
        usage: terminal.payload.usage ?? terminal.usage,
      },
    };
  }

  async close(): Promise<void> {
    if (this.#ended) return;
    this.#ended = true;
    this.#abort.abort();
    try {
      await this.#frames.return?.();
    } catch {
      // Releasing an aborted transport stream is best-effort.
    } finally {
      this.#release();
    }
  }

  #claim(): void {
    this.#operations.assertOpen();
    if (this.#claimed) {
      throw new InvalidStateError("The authorization stream is already being consumed", {
        transport: this.#operations.transportKind,
      });
    }
    this.#claimed = true;
  }

  async #next(): Promise<IteratorResult<Event>> {
    this.#operations.assertOpen();
    if (this.#ended) return { done: true, value: undefined };
    let next: IteratorResult<EventCarrier>;
    try {
      next = await this.#frames.next();
    } catch (error) {
      await this.close();
      throw error;
    }
    if (next.done) {
      await this.close();
      return { done: true, value: undefined };
    }
    const raw = next.value.event;
    if (raw === undefined) {
      await this.close();
      throw new ProtocolError("The authorization stream returned a frame without an event", {
        transport: this.#operations.transportKind,
      });
    }
    const event = decodeEvent(raw, this.#operations.transportKind);
    if (event.runId !== "") this.#lastRunId = event.runId;
    if (event.kind === "result") this.#terminal = event;
    return { done: false, value: event };
  }
}

function openStream(
  sessionId: string,
  authorizationId: string,
  action: "recheck" | "cancel",
  operations: AuthorizationOperations,
  options?: RequestOptions,
): McpAuthorizationStream {
  operations.assertOpen();
  const abort = new AbortController();
  const signal =
    options?.signal === undefined ? abort.signal : AbortSignal.any([abort.signal, options.signal]);
  const callOptions = { ...options, signal };
  const input = singleValue({ authorizationId, sessionId });
  const iterable: AsyncIterable<EventCarrier> =
    action === "recheck"
      ? operations.stream(HarnessService.method.recheckMcpAuthorization, input, callOptions)
      : operations.stream(HarnessService.method.cancelMcpAuthorization, input, callOptions);
  const frames = iterable[Symbol.asyncIterator]();
  let released = false;
  let unregister: () => void = () => undefined;
  const release = () => {
    if (released) return;
    released = true;
    unregister();
  };
  const stream = new AuthorizationStreamImpl(sessionId, frames, operations, abort, release);
  unregister = operations.registerRun(() => stream.close());
  return stream;
}

/** Creates the control handle for one pending MCP authorization. */
export function createMcpAuthorization(
  sessionId: string,
  authorizationId: string,
  operations: AuthorizationOperations,
): McpAuthorization {
  if (authorizationId === "") {
    throw new InvalidStateError("An MCP authorization handle requires an authorization id", {
      transport: operations.transportKind,
    });
  }
  return {
    authorizationId,
    sessionId,
    async presentation(options) {
      operations.assertOpen();
      const response = await operations.unary(
        HarnessService.method.getMcpAuthorizationPresentation,
        { authorizationId, sessionId },
        options,
      );
      return { url: response.url };
    },
    recheck: (options) => openStream(sessionId, authorizationId, "recheck", operations, options),
    cancel: (options) => openStream(sessionId, authorizationId, "cancel", operations, options),
  };
}

/** Creates the workspace-enrollment controls for one session. */
export function createWorkspaceEnrollmentControls(
  sessionId: string,
  operations: AuthorizationOperations,
): WorkspaceEnrollmentControls {
  const guard = (enrollmentId: string) => {
    operations.assertOpen();
    if (enrollmentId === "") {
      throw new InvalidStateError("Workspace enrollment controls require an enrollment id", {
        transport: operations.transportKind,
      });
    }
  };
  return {
    async connect(options) {
      operations.assertOpen();
      const response = await operations.unary(
        HarnessService.method.connectWorkspaceServices,
        { sessionId },
        options,
      );
      return projectWorkspaceEnrollment(response);
    },
    async retry(enrollmentId, options) {
      guard(enrollmentId);
      const response = await operations.unary(
        HarnessService.method.retryWorkspaceEnrollment,
        { enrollmentId, sessionId },
        options,
      );
      return projectWorkspaceEnrollment(response);
    },
    async cancel(enrollmentId, options) {
      guard(enrollmentId);
      const response = await operations.unary(
        HarnessService.method.cancelWorkspaceEnrollment,
        { enrollmentId, sessionId },
        options,
      );
      return projectWorkspaceEnrollment(response);
    },
  };
}

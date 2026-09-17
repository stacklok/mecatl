import type {
  DescMessage,
  DescMethodStreaming,
  DescMethodUnary,
  MessageInitShape,
  MessageShape,
} from "@bufbuild/protobuf";
import type { CallOptions } from "@connectrpc/connect";

import { InvalidStateError, normalizeError, ProtocolError, type TransportKind } from "./errors.js";
import { decodeEvent, type Event, type EventOf } from "./events.js";
import {
  HarnessService,
  type RecheckMcpAuthorizationRequestSchema,
  type RecheckMcpAuthorizationResponse,
} from "./gen/mecatl/v1/harness_pb.js";
import type { RequestOptions } from "./namespaces-core.js";
import type { PermissionAskResponder, PermissionVerdict, RunResult } from "./run.js";

/** The closed authorization status vocabulary interpreted by the lifecycle helper. @public */
export type McpAuthorizationStatus =
  | "pending"
  | "granted"
  | "denied"
  | "cancelled"
  | "expired"
  | "interrupted"
  | "failed"
  | "closed";

/** The server transition requested by one authorization flow. @public */
export type McpAuthorizationOperation = "recheck" | "cancel";

/** Application-owned behavior for one authorization continuation. @public */
export interface McpAuthorizationFlowOptions {
  /** Automatically answers ordinary permission asks observed on the continuation. */
  onPermissionAsk?: PermissionAskResponder;
  /** Request options used only for automatic permission replies. */
  permissionRequestOptions?: RequestOptions;
}

/** The authoritative result of one authorization recheck or cancellation. @public */
export type McpAuthorizationResult =
  | {
      readonly outcome: "pending";
      readonly status: "pending";
      readonly authorization: EventOf<"authorization.required">;
    }
  | {
      readonly outcome: "settled";
      readonly status: Exclude<McpAuthorizationStatus, "pending">;
      readonly authorization: EventOf<"authorization.resolved">;
    }
  | {
      readonly outcome: "completed";
      readonly status: Exclude<McpAuthorizationStatus, "pending">;
      readonly authorization: EventOf<"authorization.resolved">;
      readonly continuationRunId: string;
      readonly continuation: RunResult;
    }
  | {
      readonly outcome: "authorization_required";
      readonly status: Exclude<McpAuthorizationStatus, "pending">;
      readonly authorization: EventOf<"authorization.resolved">;
      readonly continuationRunId: string;
      readonly nextAuthorization: EventOf<"authorization.required">;
    };

/** One lazy, single-consumption authorization control and optional continuation. @public */
export interface McpAuthorizationFlow extends AsyncIterable<Event> {
  readonly sessionId: string;
  readonly authorizationId: string;
  readonly operation: McpAuthorizationOperation;
  readonly continuationRunId: string | undefined;
  resolveAsk(
    askId: string,
    verdict: PermissionVerdict,
    requestOptions?: RequestOptions,
  ): Promise<void>;
  cancelContinuation(requestOptions?: RequestOptions): Promise<void>;
  result(): Promise<McpAuthorizationResult>;
}

/** A reusable session-bound correlation handle for one server-owned authorization. @public */
export interface McpAuthorization {
  readonly sessionId: string;
  readonly authorizationId: string;
  presentation(requestOptions?: RequestOptions): Promise<string>;
  recheck(
    options?: McpAuthorizationFlowOptions,
    requestOptions?: RequestOptions,
  ): McpAuthorizationFlow;
  cancel(
    options?: McpAuthorizationFlowOptions,
    requestOptions?: RequestOptions,
  ): McpAuthorizationFlow;
}

export interface McpAuthorizationOperations {
  assertOpen(): void;
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
}

type ConsumptionMode = "events" | "result";
type TerminalStatus = Exclude<McpAuthorizationStatus, "pending">;

const authorizationStatuses = new Set<McpAuthorizationStatus>([
  "pending",
  "granted",
  "denied",
  "cancelled",
  "expired",
  "interrupted",
  "failed",
  "closed",
]);

type AuthorizationRequest = MessageInitShape<typeof RecheckMcpAuthorizationRequestSchema>;

class AuthorizationInput implements AsyncIterable<AuthorizationRequest> {
  readonly #first: AuthorizationRequest;
  #closed = false;
  #firstPending = true;
  #waiting: ((result: IteratorResult<AuthorizationRequest>) => void) | undefined;

  constructor(first: AuthorizationRequest) {
    this.#first = first;
  }

  close(): void {
    if (this.#closed) return;
    this.#closed = true;
    this.#waiting?.({ done: true, value: undefined });
    this.#waiting = undefined;
  }

  [Symbol.asyncIterator](): AsyncIterator<AuthorizationRequest> {
    return {
      next: async () => {
        if (this.#firstPending) {
          this.#firstPending = false;
          return { done: false, value: this.#first };
        }
        if (this.#closed) return { done: true, value: undefined };
        return new Promise<IteratorResult<AuthorizationRequest>>((resolve) => {
          this.#waiting = resolve;
        });
      },
      return: async () => {
        this.close();
        return { done: true, value: undefined };
      },
    };
  }
}

class McpAuthorizationFlowImpl implements McpAuthorizationFlow {
  readonly authorizationId: string;
  readonly operation: McpAuthorizationOperation;
  readonly sessionId: string;
  readonly #operations: McpAuthorizationOperations;
  readonly #requestOptions: RequestOptions | undefined;
  #abort: AbortController | undefined;
  #authorization: EventOf<"authorization.required"> | EventOf<"authorization.resolved"> | undefined;
  #consumption: ConsumptionMode | undefined;
  #continuationRunId: string | undefined;
  #continuationTerminal: EventOf<"result"> | undefined;
  #ended = false;
  #events: AsyncIterator<RecheckMcpAuthorizationResponse> | undefined;
  #input: AuthorizationInput | undefined;
  #nextAuthorization: EventOf<"authorization.required"> | undefined;
  #release: (() => void) | undefined;
  #repeatSeen = false;
  #result: McpAuthorizationResult | undefined;

  constructor(
    sessionId: string,
    authorizationId: string,
    operation: McpAuthorizationOperation,
    operations: McpAuthorizationOperations,
    flowOptions: McpAuthorizationFlowOptions,
    requestOptions: RequestOptions | undefined,
  ) {
    this.sessionId = sessionId;
    this.authorizationId = authorizationId;
    this.operation = operation;
    this.#operations = operations;
    void flowOptions;
    this.#requestOptions = requestOptions;
  }

  get continuationRunId(): string | undefined {
    return this.#continuationRunId;
  }

  [Symbol.asyncIterator](): AsyncIterator<Event> {
    this.#claim("events");
    return {
      next: () => this.#next(),
      return: async () => {
        await this.#close();
        return { done: true, value: undefined };
      },
    };
  }

  async result(): Promise<McpAuthorizationResult> {
    this.#claim("result");
    for (;;) {
      const next = await this.#next();
      if (next.done) break;
    }
    if (this.#result === undefined)
      throw this.#protocol("The authorization flow produced no result");
    return this.#result;
  }

  async resolveAsk(
    askId: string,
    verdict: PermissionVerdict,
    requestOptions?: RequestOptions,
  ): Promise<void> {
    this.#operations.assertOpen();
    void askId;
    void verdict;
    void requestOptions;
    throw new InvalidStateError("The authorization continuation has no observed permission ask", {
      transport: this.#operations.transportKind,
    });
  }

  async cancelContinuation(requestOptions?: RequestOptions): Promise<void> {
    this.#operations.assertOpen();
    void requestOptions;
    throw new InvalidStateError("The authorization continuation run has not been observed", {
      transport: this.#operations.transportKind,
    });
  }

  #claim(mode: ConsumptionMode): void {
    this.#operations.assertOpen();
    if (this.#consumption !== undefined) {
      throw new InvalidStateError(
        `MCP authorization events are already being consumed through ${this.#consumption}`,
        { transport: this.#operations.transportKind },
      );
    }
    this.#consumption = mode;
  }

  async #start(): Promise<void> {
    if (this.#events !== undefined) return;
    this.#operations.assertOpen();
    const callerSignal = this.#requestOptions?.signal;
    if (callerSignal?.aborted === true) {
      throw normalizeError(callerSignal.reason, this.#operations.transportKind);
    }

    const abort = new AbortController();
    const signal =
      callerSignal === undefined ? abort.signal : AbortSignal.any([callerSignal, abort.signal]);
    const input = new AuthorizationInput({
      authorizationId: this.authorizationId,
      sessionId: this.sessionId,
    });
    const method =
      this.operation === "recheck"
        ? HarnessService.method.recheckMcpAuthorization
        : HarnessService.method.cancelMcpAuthorization;
    const stream = this.#operations.stream(
      method as typeof HarnessService.method.recheckMcpAuthorization,
      input,
      { ...this.#requestOptions, signal },
    );
    this.#abort = abort;
    this.#input = input;
    this.#events = stream[Symbol.asyncIterator]() as AsyncIterator<RecheckMcpAuthorizationResponse>;
    let released = false;
    let unregister: () => void = () => undefined;
    this.#release = () => {
      if (released) return;
      released = true;
      unregister();
    };
    unregister = this.#operations.registerRun(() => this.#close());
  }

  async #next(): Promise<IteratorResult<Event>> {
    this.#operations.assertOpen();
    if (this.#ended) return { done: true, value: undefined };
    try {
      await this.#start();
      const next = await this.#events?.next();
      if (next === undefined || next.done) return await this.#finishEOF();
      const raw = next.value.event;
      if (raw === undefined)
        throw this.#protocol("The authorization stream returned a frame without an event");
      const event = decodeEvent(raw, this.#operations.transportKind);
      this.#observe(event);
      return { done: false, value: event };
    } catch (error) {
      await this.#close();
      throw error;
    }
  }

  #observe(event: Event): void {
    if (this.#authorization === undefined) {
      this.#authorization = this.#validateAuthoritative(event);
      return;
    }
    if (this.#authorization.kind === "authorization.required") {
      throw this.#protocol("A pending authorization result cannot have a continuation");
    }
    if (event.runId === "") {
      throw this.#protocol("An authorization continuation event has no run id");
    }
    this.#continuationRunId ??= event.runId;
    if (event.runId !== this.#continuationRunId) {
      throw this.#protocol("The authorization continuation changed run id");
    }
    if (this.#continuationTerminal !== undefined || this.#nextAuthorization !== undefined) {
      throw this.#protocol("The authorization continuation returned an event after its terminal");
    }

    if (event.kind === "authorization.resolved") {
      if (!sameAuthorization(event, this.#authorization)) {
        throw this.#protocol("The authorization continuation changed the original resolution");
      }
      if (this.#repeatSeen) {
        throw this.#protocol(
          "The authorization continuation repeated the original resolution twice",
        );
      }
      this.#repeatSeen = true;
      return;
    }
    if (event.kind === "authorization.required") {
      if (
        event.payload.status !== "pending" ||
        event.payload.authorizationId === this.authorizationId ||
        event.payload.authorizationId === "" ||
        event.payload.callId === ""
      ) {
        throw this.#protocol(
          "The authorization continuation returned a malformed chained authorization",
        );
      }
      this.#nextAuthorization = event;
      return;
    }
    if (event.kind === "result") this.#continuationTerminal = event;
  }

  #validateAuthoritative(
    event: Event,
  ): EventOf<"authorization.required"> | EventOf<"authorization.resolved"> {
    if (event.runId !== "") {
      throw this.#protocol("The authoritative authorization event unexpectedly carried a run id");
    }
    if (event.kind !== "authorization.required" && event.kind !== "authorization.resolved") {
      throw this.#protocol("The authorization stream did not begin with an authorization event");
    }
    const status = event.payload.status;
    if (
      event.payload.authorizationId !== this.authorizationId ||
      event.payload.callId === "" ||
      !authorizationStatuses.has(status as McpAuthorizationStatus)
    ) {
      throw this.#protocol(
        "The authoritative authorization event has invalid correlation or status",
      );
    }
    if (
      (status === "pending" && event.kind !== "authorization.required") ||
      (status !== "pending" && event.kind !== "authorization.resolved")
    ) {
      throw this.#protocol(
        "The authoritative authorization event has an invalid kind and status pairing",
      );
    }
    return event;
  }

  async #finishEOF(): Promise<IteratorResult<Event>> {
    const authorization = this.#authorization;
    if (authorization === undefined) {
      throw this.#protocol("The authorization stream ended without an authoritative result");
    }

    if (this.#continuationRunId === undefined) {
      this.#result =
        authorization.kind === "authorization.required"
          ? { authorization, outcome: "pending", status: "pending" }
          : {
              authorization,
              outcome: "settled",
              status: authorization.payload.status as TerminalStatus,
            };
    } else {
      if (authorization.kind !== "authorization.resolved") {
        throw this.#protocol("A pending authorization result cannot have a continuation");
      }
      if (!this.#repeatSeen) {
        throw this.#protocol("The authorization continuation omitted the original resolution");
      }
      const status = authorization.payload.status as TerminalStatus;
      if (this.#continuationTerminal !== undefined) {
        this.#result = {
          authorization,
          continuation: runResult(
            this.sessionId,
            this.#continuationRunId,
            this.#continuationTerminal,
          ),
          continuationRunId: this.#continuationRunId,
          outcome: "completed",
          status,
        };
      } else if (this.#nextAuthorization !== undefined) {
        this.#result = {
          authorization,
          continuationRunId: this.#continuationRunId,
          nextAuthorization: this.#nextAuthorization,
          outcome: "authorization_required",
          status,
        };
      } else {
        throw this.#protocol("The authorization continuation ended without a terminal outcome");
      }
    }
    await this.#close();
    return { done: true, value: undefined };
  }

  async #close(): Promise<void> {
    if (this.#ended) return;
    this.#ended = true;
    this.#abort?.abort();
    this.#input?.close();
    try {
      await this.#events?.return?.();
    } catch {
      // Releasing an aborted transport stream is best-effort.
    } finally {
      this.#release?.();
    }
  }

  #protocol(message: string): ProtocolError {
    return new ProtocolError(message, { transport: this.#operations.transportKind });
  }
}

class McpAuthorizationImpl implements McpAuthorization {
  readonly authorizationId: string;
  readonly sessionId: string;
  readonly #operations: McpAuthorizationOperations;

  constructor(sessionId: string, authorizationId: string, operations: McpAuthorizationOperations) {
    this.sessionId = sessionId;
    this.authorizationId = authorizationId;
    this.#operations = operations;
  }

  async presentation(requestOptions?: RequestOptions): Promise<string> {
    this.#operations.assertOpen();
    const response = await this.#operations.unary(
      HarnessService.method.getMcpAuthorizationPresentation,
      { authorizationId: this.authorizationId, sessionId: this.sessionId },
      requestOptions,
    );
    const value = response.url;
    let parsed: URL;
    try {
      parsed = new URL(value);
    } catch (cause) {
      throw new ProtocolError("The MCP authorization presentation URL is malformed", {
        cause,
        transport: this.#operations.transportKind,
      });
    }
    if ((parsed.protocol !== "http:" && parsed.protocol !== "https:") || parsed.hostname === "") {
      throw new ProtocolError("The MCP authorization presentation URL must be absolute HTTP(S)", {
        transport: this.#operations.transportKind,
      });
    }
    return value;
  }

  recheck(
    options: McpAuthorizationFlowOptions = {},
    requestOptions?: RequestOptions,
  ): McpAuthorizationFlow {
    return this.#flow("recheck", options, requestOptions);
  }

  cancel(
    options: McpAuthorizationFlowOptions = {},
    requestOptions?: RequestOptions,
  ): McpAuthorizationFlow {
    return this.#flow("cancel", options, requestOptions);
  }

  #flow(
    operation: McpAuthorizationOperation,
    options: McpAuthorizationFlowOptions,
    requestOptions: RequestOptions | undefined,
  ): McpAuthorizationFlow {
    return new McpAuthorizationFlowImpl(
      this.sessionId,
      this.authorizationId,
      operation,
      this.#operations,
      options,
      requestOptions,
    );
  }
}

function sameAuthorization(
  event: EventOf<"authorization.resolved">,
  original: EventOf<"authorization.resolved">,
): boolean {
  const left = event.payload;
  const right = original.payload;
  return (
    left.authorizationId === right.authorizationId &&
    left.callId === right.callId &&
    left.displayName === right.displayName &&
    left.status === right.status &&
    left.expiresAt?.seconds === right.expiresAt?.seconds &&
    left.expiresAt?.nanos === right.expiresAt?.nanos
  );
}

function runResult(sessionId: string, runId: string, event: EventOf<"result">): RunResult {
  return {
    content: event.payload.text,
    rawEvent: event,
    runId,
    sessionId,
    stopReason: event.payload.stop,
    text: event.payload.text,
    usage: event.payload.usage ?? event.usage,
  };
}

export function createMcpAuthorization(
  sessionId: string,
  authorizationId: string,
  operations: McpAuthorizationOperations,
): McpAuthorization {
  return new McpAuthorizationImpl(sessionId, authorizationId, operations);
}

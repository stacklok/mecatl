import type {
  DescMessage,
  DescMethodStreaming,
  DescMethodUnary,
  MessageInitShape,
  MessageShape,
} from "@bufbuild/protobuf";
import { type CallOptions, Code, ConnectError } from "@connectrpc/connect";

import {
  InvalidStateError,
  type MecatlError,
  normalizeError,
  ProtocolError,
  type TransportKind,
} from "./errors.js";
import { decodeEvent, type Event, type EventOf } from "./events.js";
import {
  HarnessService,
  type RecheckMcpAuthorizationRequestSchema,
  type RecheckMcpAuthorizationResponse,
} from "./gen/mecatl/v1/harness_pb.js";
import type { PromptCapabilities } from "./media.js";
import type { RequestOptions } from "./namespaces-core.js";
import { PLAN_APPROVAL_TOOL } from "./plan.js";
import type { PermissionAskResponder, PermissionVerdict, RunResult } from "./run.js";
import { createRunControls, type RunControls } from "./run-controls.js";

/**
 * The closed authorization status vocabulary interpreted by the lifecycle helper.
 *
 * The server remains authoritative for every status. An unknown value is a protocol error in
 * this lifecycle even though the general event union keeps raw status strings open.
 * @public
 */
export type McpAuthorizationStatus =
  | "pending"
  | "granted"
  | "denied"
  | "cancelled"
  | "expired"
  | "interrupted"
  | "failed"
  | "closed";

/** The one-shot server transition requested by an authorization flow. @public */
export type McpAuthorizationOperation = "recheck" | "cancel";

/**
 * Application-owned permission behavior for one authorization continuation.
 *
 * These options never choose an authorization status or browser policy. Automatic permission
 * replies use only `permissionRequestOptions`, independently of the flow request options.
 * @public
 */
export interface McpAuthorizationFlowOptions {
  /** Automatically answers only ordinary permission asks observed on the continuation. */
  onPermissionAsk?: PermissionAskResponder;
  /** Request options used only for automatic permission replies. */
  permissionRequestOptions?: RequestOptions;
}

/**
 * The authoritative result of one authorization recheck or cancellation.
 *
 * `pending` and `settled` have no continuation. `completed` carries one ordinary run result.
 * `authorization_required` hands off a different authorization parked by the continuation.
 * @public
 */
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

/**
 * One lazy, single-consumption authorization control and optional continuation.
 *
 * Calling `recheck()` or `cancel()` creates this flow without I/O. The first iterator `next()` or
 * `result()` performs the one control request with exact session affinity. Iteration and
 * `result()` are mutually exclusive. Request cancellation releases SDK-owned resources but does
 * not determine whether the server committed the control. The SDK does not poll, retry, reconnect,
 * or scan durable activity automatically.
 * @public
 */
export interface McpAuthorizationFlow extends AsyncIterable<Event> {
  readonly sessionId: string;
  readonly authorizationId: string;
  readonly operation: McpAuthorizationOperation;
  readonly continuationRunId: string | undefined;
  /**
   * Resolves one observed ordinary permission ask on the exact continuation run.
   *
   * @param askId - ID of a pending ask already observed on this flow.
   * @param verdict - Application-owned permission decision to send unchanged.
   * @param requestOptions - Options used only for this permission mutation.
   * @returns A promise that resolves after the server accepts the decision.
   * @throws `InvalidStateError` when the ask is unknown, no longer pending, or plan-originated.
   * @throws `UnsupportedFeatureError` when the server lacks `prompt_free_controls`.
   */
  resolveAsk(
    askId: string,
    verdict: PermissionVerdict,
    requestOptions?: RequestOptions,
  ): Promise<void>;
  /**
   * Requests cancellation of the exact continuation run already observed by this flow.
   *
   * @param requestOptions - Options used only for this cancellation mutation.
   * @returns A promise that resolves after the server accepts the request.
   * @throws `InvalidStateError` before a continuation run is observed.
   * @throws `UnsupportedFeatureError` when the server lacks `prompt_free_controls`.
   */
  cancelContinuation(requestOptions?: RequestOptions): Promise<void>;
  /**
   * Starts and drains this flow as its single consumption mode.
   *
   * @returns A pending, settled, completed, or chained-authorization result.
   * @throws `InvalidStateError` when the flow is already being consumed.
   * @throws `ProtocolError` when the server stream violates lifecycle correlation or grammar.
   */
  result(): Promise<McpAuthorizationResult>;
}

/**
 * A reusable session-bound correlation handle for one server-owned authorization.
 *
 * Construction stores exact correlation only. It performs no I/O and makes no state or authority
 * claim. The handle does not persist credentials or lifecycle truth. Every presentation lookup and
 * control request receives automatic session affinity.
 * @public
 */
export interface McpAuthorization {
  readonly sessionId: string;
  readonly authorizationId: string;
  /**
   * Reads the live presentation URL for this authorization.
   *
   * The application owns display and browser policy. The SDK validates and returns the HTTP(S)
   * string without opening, copying, caching, rendering, or persisting it.
   *
   * @param requestOptions - Request headers, callbacks, cancellation signal, and deadline.
   * @returns The server's current absolute HTTP(S) presentation URL.
   * @throws `ProtocolError` when the response has no valid absolute HTTP(S) URL.
   */
  presentation(requestOptions?: RequestOptions): Promise<string>;
  /**
   * Creates one lazy authorization recheck flow.
   *
   * @param options - Permission handling for a possible continuation.
   * @param requestOptions - Options used only when this flow starts.
   * @returns A distinct, transport-lazy, single-consumption flow.
   */
  recheck(
    options?: McpAuthorizationFlowOptions,
    requestOptions?: RequestOptions,
  ): McpAuthorizationFlow;
  /**
   * Creates one lazy authorization cancellation flow.
   *
   * @param options - Permission handling for a possible continuation.
   * @param requestOptions - Options used only when this flow starts.
   * @returns A distinct, transport-lazy, single-consumption flow.
   */
  cancel(
    options?: McpAuthorizationFlowOptions,
    requestOptions?: RequestOptions,
  ): McpAuthorizationFlow;
}

export interface McpAuthorizationOperations {
  assertOpen(): void;
  features(options?: RequestOptions): Promise<ReadonlySet<string>>;
  promptCapabilities(): PromptCapabilities | undefined;
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
type PendingAsk = { readonly controller: AbortController; readonly plan: boolean };

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
      throw: async (error?: unknown) => {
        this.close();
        throw error;
      },
    };
  }
}

class McpAuthorizationFlowImpl implements McpAuthorizationFlow {
  readonly authorizationId: string;
  readonly operation: McpAuthorizationOperation;
  readonly sessionId: string;
  readonly #operations: McpAuthorizationOperations;
  readonly #flowOptions: McpAuthorizationFlowOptions;
  readonly #requestOptions: RequestOptions | undefined;
  readonly #controlFailure: Promise<never>;
  readonly #rejectControlFailure: (error: unknown) => void;
  #abort: AbortController | undefined;
  #authorization: EventOf<"authorization.required"> | EventOf<"authorization.resolved"> | undefined;
  #consumption: ConsumptionMode | undefined;
  #continuationRunId: string | undefined;
  #continuationTerminal: EventOf<"result"> | undefined;
  #ended = false;
  #events: AsyncIterator<RecheckMcpAuthorizationResponse> | undefined;
  #input: AuthorizationInput | undefined;
  #releaseRequestLifetime: (() => void) | undefined;
  #nextAuthorization: EventOf<"authorization.required"> | undefined;
  readonly #knownAsks = new Set<string>();
  readonly #pendingAsks = new Map<string, PendingAsk>();
  #release: (() => void) | undefined;
  #repeatSeen = false;
  #result: McpAuthorizationResult | undefined;
  #runControls: RunControls | undefined;
  #terminalError: MecatlError | undefined;

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
    this.#flowOptions = flowOptions;
    this.#requestOptions = requestOptions;
    let rejectControlFailure: (error: unknown) => void = () => undefined;
    this.#controlFailure = new Promise<never>((_resolve, reject) => {
      rejectControlFailure = reject;
    });
    void this.#controlFailure.catch(() => undefined);
    this.#rejectControlFailure = rejectControlFailure;
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
    const pending = this.#pendingAsks.get(askId);
    if (pending === undefined) {
      throw new InvalidStateError(
        `The authorization continuation has no pending permission ask ${askId}`,
        { transport: this.#operations.transportKind },
      );
    }
    if (pending.plan) {
      throw new InvalidStateError(
        `Plan approval ask ${askId} must be resolved through the plan workflow`,
        { transport: this.#operations.transportKind },
      );
    }
    await this.#resolvePendingAsk(askId, verdict, pending, requestOptions);
  }

  async cancelContinuation(requestOptions?: RequestOptions): Promise<void> {
    this.#operations.assertOpen();
    const controls = this.#runControls;
    if (controls === undefined) {
      throw new InvalidStateError("The authorization continuation run has not been observed", {
        transport: this.#operations.transportKind,
      });
    }
    await controls.cancel(requestOptions);
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
    this.#abort = abort;
    this.#bindRequestLifetime(abort, callerSignal, this.#requestOptions?.timeoutMs);
    if (abort.signal.aborted) {
      throw normalizeError(abort.signal.reason, this.#operations.transportKind);
    }
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
      { ...this.#requestOptions, signal: abort.signal },
    );
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

  #bindRequestLifetime(
    abort: AbortController,
    callerSignal: AbortSignal | undefined,
    timeoutMs: number | undefined,
  ): void {
    let deadline: ReturnType<typeof setTimeout> | undefined;
    const expire = (reason: unknown) => {
      if (abort.signal.aborted) return;
      this.#terminalError = normalizeError(reason, this.#operations.transportKind);
      abort.abort(reason);
      void this.#close();
    };
    const callerAbort = () => expire(callerSignal?.reason);
    callerSignal?.addEventListener("abort", callerAbort, { once: true });
    this.#releaseRequestLifetime = () => {
      callerSignal?.removeEventListener("abort", callerAbort);
      if (deadline !== undefined) clearTimeout(deadline);
      this.#releaseRequestLifetime = undefined;
    };
    if (timeoutMs === undefined) return;
    const deadlineExceeded = () =>
      expire(new ConnectError("the operation timed out", Code.DeadlineExceeded));
    if (timeoutMs <= 0) deadlineExceeded();
    else deadline = setTimeout(deadlineExceeded, timeoutMs);
  }

  async #next(): Promise<IteratorResult<Event>> {
    this.#operations.assertOpen();
    const terminalError = this.#takeTerminalError();
    if (terminalError !== undefined) throw terminalError;
    if (this.#ended) return { done: true, value: undefined };
    try {
      await this.#start();
      const next = await Promise.race([this.#events?.next(), this.#controlFailure]);
      if (next === undefined || next.done) return await this.#finishEOF();
      const raw = next.value.event;
      if (raw === undefined)
        throw this.#protocol("The authorization stream returned a frame without an event");
      const event = decodeEvent(raw, this.#operations.transportKind);
      this.#observe(event);
      return { done: false, value: event };
    } catch (error) {
      const retainedError = this.#takeTerminalError();
      await this.#close();
      throw retainedError ?? error;
    }
  }

  #takeTerminalError(): MecatlError | undefined {
    const error = this.#terminalError;
    this.#terminalError = undefined;
    return error;
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
    if (this.#continuationRunId === undefined) {
      this.#continuationRunId = event.runId;
      this.#runControls = createRunControls(this.sessionId, event.runId, {
        assertOpen: () => this.#operations.assertOpen(),
        features: (options) => this.#operations.features(options),
        promptCapabilities: () => this.#operations.promptCapabilities(),
        transportKind: this.#operations.transportKind,
        unary: (method, input, options) => this.#operations.unary(method, input, options),
      });
    }
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
      this.#retireAllAsks();
      return;
    }
    if (event.kind === "permission.ask") {
      this.#startAsk(event);
      return;
    }
    if (event.kind === "permission.retract" || event.kind === "approval") {
      this.#retireAsk(event.payload.askId);
      return;
    }
    if (event.kind === "result") {
      this.#continuationTerminal = event;
      this.#retireAllAsks();
    }
  }

  #startAsk(event: EventOf<"permission.ask">): void {
    const askId = event.payload.askId;
    if (askId === "" || this.#knownAsks.has(askId)) {
      throw this.#protocol("The authorization continuation returned an invalid permission ask");
    }
    this.#knownAsks.add(askId);
    const pending = {
      controller: new AbortController(),
      plan: event.payload.tool === PLAN_APPROVAL_TOOL,
    };
    this.#pendingAsks.set(askId, pending);
    if (pending.plan) return;
    const responder = this.#flowOptions.onPermissionAsk;
    if (responder === undefined) return;

    void (async () => {
      let verdict: PermissionVerdict | undefined;
      try {
        verdict = await responder(event.payload, pending.controller.signal);
      } catch {
        return;
      }
      if (verdict === undefined || pending.controller.signal.aborted) return;
      try {
        await this.#resolvePendingAsk(
          askId,
          verdict,
          pending,
          this.#automaticControlOptions(this.#flowOptions.permissionRequestOptions),
        );
      } catch (error) {
        if (!this.#ended) this.#rejectControlFailure(error);
      }
    })();
  }

  async #resolvePendingAsk(
    askId: string,
    verdict: PermissionVerdict,
    pending: PendingAsk,
    requestOptions: RequestOptions | undefined,
  ): Promise<void> {
    if (this.#pendingAsks.get(askId) !== pending || pending.plan) {
      throw new InvalidStateError(`Permission ask ${askId} is no longer pending`, {
        transport: this.#operations.transportKind,
      });
    }
    const controls = this.#runControls;
    if (controls === undefined) {
      throw new InvalidStateError("The authorization continuation run has not been observed", {
        transport: this.#operations.transportKind,
      });
    }
    this.#pendingAsks.delete(askId);
    pending.controller.abort();
    await controls.resolveAsk(askId, verdict, requestOptions);
  }

  #automaticControlOptions(requestOptions: RequestOptions | undefined): RequestOptions {
    const lifetimeSignal = this.#abort?.signal;
    if (lifetimeSignal === undefined) {
      throw this.#protocol("The authorization flow has no active request lifetime");
    }
    return {
      ...requestOptions,
      signal:
        requestOptions?.signal === undefined
          ? lifetimeSignal
          : AbortSignal.any([requestOptions.signal, lifetimeSignal]),
    };
  }

  #retireAsk(askId: string): void {
    const pending = this.#pendingAsks.get(askId);
    if (pending === undefined) return;
    this.#pendingAsks.delete(askId);
    pending.controller.abort();
  }

  #retireAllAsks(): void {
    for (const pending of this.#pendingAsks.values()) pending.controller.abort();
    this.#pendingAsks.clear();
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
      event.payload.authorizationId === "" ||
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
    this.#releaseRequestLifetime?.();
    this.#retireAllAsks();
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
  if (authorizationId === "") {
    throw new InvalidStateError("The MCP authorization ID must be non-empty", {
      transport: "local",
    });
  }
  return new McpAuthorizationImpl(sessionId, authorizationId, operations);
}

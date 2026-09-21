import type { MessageInitShape } from "@bufbuild/protobuf";

import {
  InvalidStateError,
  type MecatlErrorOptions,
  PermissionAskAlreadyResolvedError,
  ProtocolError,
  type TransportKind,
} from "./errors.js";
import {
  decodeEvent,
  type Event,
  type EventOf,
  type EventUsage,
  type PermissionAskEventPayload,
} from "./events.js";
import {
  ApprovalVerdict,
  type ConverseRequestSchema,
  type Event as ProtoEvent,
} from "./gen/mecatl/v1/harness_pb.js";
import {
  PLAN_APPROVAL_TOOL,
  type PlanApprovalResponder,
  type PlanApprovalVerdict,
  planPermissionVerdict,
} from "./plan.js";

/** Canonical server permission verdicts. @public */
export const PermissionVerdict = {
  AllowOnce: "allow_once",
  AllowAlways: "allow_always",
  Deny: "deny",
} as const;

/** One server permission verdict accepted by run.resolveAsk(). @public */
export type PermissionVerdict = (typeof PermissionVerdict)[keyof typeof PermissionVerdict];

/** An optional automatic responder invoked for each permission ask on a run. @public */
export type PermissionAskResponder = (
  ask: PermissionAskEventPayload,
  signal: AbortSignal,
) => PermissionVerdict | undefined | Promise<PermissionVerdict | undefined>;

/** Options applied to one run. @public */
export interface RunOptions {
  /** Automatically answers ordinary permission asks. */
  onPermissionAsk?: PermissionAskResponder;
  /** Automatically answers only plan-originated PresentPlan asks. */
  onPlanApproval?: PlanApprovalResponder;
}

/** The terminal outcome of a consumed run. Server-declared stops are values, not errors. @public */
export interface RunResult {
  /** Final text, mirrored as `content` for content-oriented consumers. */
  readonly text: string;
  readonly content: string;
  readonly stopReason: string;
  readonly usage: EventUsage | undefined;
  readonly sessionId: string;
  readonly runId: string;
  /** The terminal event from the same discriminated union exposed by iteration. */
  readonly rawEvent: EventOf<"result">;
}

/** A normally completed run outcome returned by `Run.outcome()`. @public */
export interface RunCompletedOutcome {
  readonly outcome: "completed";
  readonly result: RunResult;
}

/**
 * A run that handed off one pending external authorization.
 *
 * This detached value carries correlation only. The server retains lifecycle ownership.
 * @public
 */
export interface RunAuthorizationRequiredOutcome {
  readonly outcome: "authorization_required";
  readonly sessionId: string;
  readonly runId: string;
  readonly authorization: EventOf<"authorization.required">;
}

/** The closed set of completion and authorization-park outcomes from `Run.outcome()`. @public */
export type RunOutcome = RunCompletedOutcome | RunAuthorizationRequiredOutcome;

/**
 * `Run.result()` consumed a valid authorization handoff instead of a completed result.
 *
 * Read `outcome` to create `Session.mcpAuthorization()` with the exact authorization ID.
 * @public
 */
export class RunAuthorizationRequiredError extends InvalidStateError {
  readonly outcome: RunAuthorizationRequiredOutcome;

  constructor(outcome: RunAuthorizationRequiredOutcome, options: Omit<MecatlErrorOptions, "code">) {
    super(`Run ${outcome.runId} requires external authorization`, options);
    this.outcome = outcome;
  }
}

/** One accepted server run and its single-consumption event stream. @public */
export interface Run extends AsyncIterable<Event> {
  readonly id: string;
  readonly sessionId: string;
  /**
   * Resolves one pending ask on this run with the server's string verdict vocabulary.
   *
   * @param askId - ID carried by the permission ask.
   * @param verdict - Decision to apply to the pending ask.
   * @returns A promise that resolves after the verdict frame is handed to the
   * active stream transport. This send-only API does not acknowledge server
   * acceptance; use `Session.controls(runId).resolveAsk()` when an acknowledged
   * control operation is required.
   * @throws `PermissionAskAlreadyResolvedError` when the ask is no longer pending.
   * @throws `InvalidStateError` when used for a plan-approval ask.
   */
  resolveAsk(askId: string, verdict: PermissionVerdict): Promise<void>;
  /**
   * Requests cancellation; consume the run normally to receive the cancelled outcome.
   *
   * @returns A promise that resolves after the cancellation request is sent.
   */
  cancel(): Promise<void>;
  /**
   * Strictly steers this run. A late steer is refused and is never promoted.
   *
   * @param text - Instruction to apply to the active run.
   * @returns A promise that resolves after the steering request is sent.
   */
  steer(text: string): Promise<void>;
  /**
   * Drains all remaining events and returns either completion or an authorization handoff.
   *
   * @returns The normal terminal outcome for this run.
   * @throws `InvalidStateError` when the run is already being consumed.
   */
  outcome(): Promise<RunOutcome>;
  /**
   * Drains all remaining events and returns the completed terminal result.
   *
   * @returns The terminal result for this run.
   * @throws `InvalidStateError` when the run is already being consumed.
   * @throws `RunAuthorizationRequiredError` when the run parks on external authorization.
   */
  result(): Promise<RunResult>;
}

export interface RunOperations {
  assertOpen(): void;
  readonly transportKind: TransportKind;
  send(frame: MessageInitShape<typeof ConverseRequestSchema>): void;
}

type ConsumptionMode = "events" | "outcome" | "result";
type PendingAsk = { readonly controller: AbortController; readonly plan: boolean };

export class RunImpl implements Run {
  readonly id: string;
  readonly sessionId: string;

  readonly #events: AsyncIterator<ProtoEvent>;
  readonly #first: Event;
  readonly #knownAsks = new Set<string>();
  readonly #onPermissionAsk: PermissionAskResponder | undefined;
  readonly #onPlanApproval: PlanApprovalResponder | undefined;
  readonly #operations: RunOperations;
  readonly #pendingAsks = new Map<string, PendingAsk>();
  #consumption: ConsumptionMode | undefined;
  #ended = false;
  #firstPending = true;
  #authorization: EventOf<"authorization.required"> | undefined;
  #streamEnded = false;
  #terminal: EventOf<"result"> | undefined;
  #steerSequence = 0;

  constructor(
    sessionId: string,
    runId: string,
    first: ProtoEvent,
    events: AsyncIterator<ProtoEvent>,
    operations: RunOperations,
    options: RunOptions = {},
  ) {
    this.id = runId;
    this.sessionId = sessionId;
    this.#first = decodeEvent(first, operations.transportKind);
    this.#events = events;
    this.#operations = operations;
    this.#onPermissionAsk = options.onPermissionAsk;
    this.#onPlanApproval = options.onPlanApproval;
    this.#observe(this.#first);
  }

  async resolveAsk(askId: string, verdict: PermissionVerdict): Promise<void> {
    this.#operations.assertOpen();
    const pending = this.#pendingAsks.get(askId);
    if (pending === undefined) {
      throw new PermissionAskAlreadyResolvedError(askId, {
        transport: this.#operations.transportKind,
      });
    }
    if (pending.plan) {
      throw new InvalidStateError(
        `Plan approval ask ${askId} must be resolved through onPlanApproval`,
        { transport: this.#operations.transportKind },
      );
    }

    await this.#resolvePendingAsk(askId, verdict, pending);
  }

  async #resolvePlanAsk(askId: string, verdict: PlanApprovalVerdict): Promise<void> {
    this.#operations.assertOpen();
    const pending = this.#pendingAsks.get(askId);
    if (pending === undefined) {
      throw new PermissionAskAlreadyResolvedError(askId, {
        transport: this.#operations.transportKind,
      });
    }
    if (!pending.plan) {
      throw new InvalidStateError(`Permission ask ${askId} is not a plan approval`, {
        transport: this.#operations.transportKind,
      });
    }
    await this.#resolvePendingAsk(askId, planPermissionVerdict(verdict), pending);
  }

  async #resolvePendingAsk(
    askId: string,
    verdict: PermissionVerdict,
    pending: PendingAsk,
  ): Promise<void> {
    const wireVerdict = approvalVerdict(verdict, this.#operations.transportKind);
    this.#pendingAsks.delete(askId);
    pending.controller.abort();
    this.#send({
      kind: {
        case: "resumeApproval",
        value: {
          askId,
          expectedRunId: this.id,
          verdict: wireVerdict,
        },
      },
    });
  }

  async cancel(): Promise<void> {
    this.#send({
      kind: { case: "cancel", value: { expectedRunId: this.id } },
    });
    this.#end();
  }

  async steer(text: string): Promise<void> {
    this.#steerSequence += 1;
    this.#send({
      kind: {
        case: "steer",
        value: {
          expectedRunId: this.id,
          messageId: `sdk-steer-${this.#steerSequence}`,
          text,
        },
      },
    });
  }

  [Symbol.asyncIterator](): AsyncIterator<Event> {
    this.#claim("events");
    return this.#consumer();
  }

  async outcome(): Promise<RunOutcome> {
    this.#claim("outcome");
    return this.#drainOutcome();
  }

  async result(): Promise<RunResult> {
    this.#claim("result");
    const outcome = await this.#drainOutcome();
    if (outcome.outcome === "authorization_required") {
      throw new RunAuthorizationRequiredError(outcome, {
        transport: this.#operations.transportKind,
      });
    }
    return outcome.result;
  }

  async #drainOutcome(): Promise<RunOutcome> {
    for (;;) {
      const next = await this.#next();
      if (next.done) break;
    }
    const event = this.#terminal;
    if (event !== undefined) {
      return { outcome: "completed", result: runResult(this.sessionId, this.id, event) };
    }

    const authorization = this.#authorization;
    if (authorization !== undefined) return this.#authorizationOutcome(authorization);

    throw this.#protocol("The Converse stream ended without a terminal outcome");
  }

  #authorizationOutcome(
    authorization: EventOf<"authorization.required">,
  ): RunAuthorizationRequiredOutcome {
    return {
      authorization,
      outcome: "authorization_required",
      runId: this.id,
      sessionId: this.sessionId,
    };
  }

  #claim(mode: ConsumptionMode): void {
    this.#operations.assertOpen();
    if (this.#consumption !== undefined) {
      throw new InvalidStateError(
        `Run events are already being consumed through ${this.#consumption}`,
        { transport: this.#operations.transportKind },
      );
    }
    this.#consumption = mode;
  }

  #consumer(): AsyncIterator<Event> {
    return {
      next: () => this.#next(),
      return: async () => {
        if (this.#authorization !== undefined || this.#terminal !== undefined) {
          await this.#closeStream();
        }
        return { done: true, value: undefined };
      },
    };
  }

  async #next(): Promise<IteratorResult<Event>> {
    this.#operations.assertOpen();
    if (this.#firstPending) {
      this.#firstPending = false;
      return { done: false, value: this.#first };
    }
    if (this.#streamEnded) return { done: true, value: undefined };
    let next: IteratorResult<ProtoEvent>;
    try {
      next = await this.#events.next();
    } catch (error) {
      this.#end();
      throw error;
    }
    if (next.done) {
      this.#streamEnded = true;
      this.#end();
      if (this.#terminal !== undefined || this.#authorization !== undefined) {
        return { done: true, value: undefined };
      }
      throw this.#protocol("The Converse stream ended without a terminal outcome");
    }
    if (this.#terminal !== undefined) {
      await this.#closeStream();
      throw this.#protocol("The Converse stream returned an event after its terminal result");
    }
    if (this.#authorization !== undefined) {
      await this.#closeStream();
      throw this.#protocol("The Converse stream returned an event after authorization parking");
    }
    if (next.value.runId !== this.id) {
      await this.#closeStream();
      throw this.#protocol("The Converse stream changed run id");
    }
    try {
      const event = decodeEvent(next.value, this.#operations.transportKind);
      this.#observe(event);
      return { done: false, value: event };
    } catch (error) {
      await this.#closeStream();
      throw error;
    }
  }

  #observe(event: Event): void {
    if (event.kind === "authorization.required") {
      if (
        this.sessionId === "" ||
        this.id === "" ||
        event.runId !== this.id ||
        event.payload.status !== "pending" ||
        event.payload.authorizationId === "" ||
        event.payload.callId === ""
      ) {
        throw this.#protocol("The Converse stream returned a malformed authorization requirement");
      }
      this.#authorization = event;
      this.#end();
      return;
    }
    if (event.kind === "permission.ask") {
      this.#startAsk(event);
      return;
    }
    if (event.kind === "permission.retract") {
      this.#retireAsk(event.payload.askId);
      return;
    }
    if (event.kind === "approval") {
      this.#retireAsk(event.payload.askId);
      return;
    }
    if (terminal(event)) {
      this.#terminal = event;
      this.#end();
    }
  }

  #startAsk(event: EventOf<"permission.ask">): void {
    const askId = event.payload.askId;
    if (this.#ended || this.#knownAsks.has(askId)) return;
    this.#knownAsks.add(askId);
    const plan = event.payload.tool === PLAN_APPROVAL_TOOL;
    const pending = { controller: new AbortController(), plan };
    this.#pendingAsks.set(askId, pending);
    if (plan) {
      this.#startPlanResponder(event, pending);
      return;
    }
    this.#startPermissionResponder(event, pending);
  }

  #startPermissionResponder(event: EventOf<"permission.ask">, pending: PendingAsk): void {
    const responder = this.#onPermissionAsk;
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
        await this.resolveAsk(event.payload.askId, verdict);
      } catch (error) {
        if (!(error instanceof PermissionAskAlreadyResolvedError)) throw error;
      }
    })().catch(() => {
      // Automatic responder failures never alter raw event consumption.
    });
  }

  #startPlanResponder(event: EventOf<"permission.ask">, pending: PendingAsk): void {
    const responder = this.#onPlanApproval;
    if (responder === undefined) return;

    void (async () => {
      let verdict: PlanApprovalVerdict | undefined;
      try {
        verdict = await responder(event.payload, pending.controller.signal);
      } catch {
        return;
      }
      if (verdict === undefined || pending.controller.signal.aborted) return;
      try {
        await this.#resolvePlanAsk(event.payload.askId, verdict);
      } catch (error) {
        if (!(error instanceof PermissionAskAlreadyResolvedError)) throw error;
      }
    })().catch(() => {
      // Automatic plan-responder failures never alter raw event consumption.
    });
  }

  #retireAsk(askId: string): void {
    const pending = this.#pendingAsks.get(askId);
    if (pending === undefined) return;
    this.#pendingAsks.delete(askId);
    pending.controller.abort();
  }

  #end(): void {
    if (this.#ended) return;
    this.#ended = true;
    for (const pending of this.#pendingAsks.values()) pending.controller.abort();
    this.#pendingAsks.clear();
  }

  async #closeStream(): Promise<void> {
    if (this.#streamEnded) return;
    this.#streamEnded = true;
    this.#end();
    await this.#events.return?.();
  }

  #protocol(message: string): ProtocolError {
    return new ProtocolError(message, { transport: this.#operations.transportKind });
  }

  #send(frame: MessageInitShape<typeof ConverseRequestSchema>): void {
    this.#operations.assertOpen();
    this.#operations.send(frame);
  }
}

function approvalVerdict(verdict: PermissionVerdict, transport: TransportKind): ApprovalVerdict {
  switch (verdict) {
    case "allow_once":
      return ApprovalVerdict.ALLOW_ONCE;
    case "allow_always":
      return ApprovalVerdict.ALLOW_ALWAYS;
    case "deny":
      return ApprovalVerdict.DENY;
    default:
      throw new InvalidStateError(`Unknown permission verdict: ${String(verdict)}`, { transport });
  }
}

function terminal(event: Event): event is EventOf<"result"> {
  return event.kind === "result";
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

export type ConverseFrame = MessageInitShape<typeof ConverseRequestSchema>;

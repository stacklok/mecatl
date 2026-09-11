import type { MessageInitShape } from "@bufbuild/protobuf";

import {
  InvalidStateError,
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

/** A server permission verdict accepted by run.resolveAsk(). @public */
export type PermissionVerdict = "allow_once" | "allow_always" | "deny";

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

/** One accepted server run and its single-consumption event stream. @public */
export interface Run extends AsyncIterable<Event> {
  readonly id: string;
  readonly sessionId: string;
  /** Sends a permission verdict for a `permission.ask` event. */
  approve(askId: string, allow: boolean): Promise<void>;
  /** Resolves one pending ask on this run with the server's string verdict vocabulary. */
  resolveAsk(askId: string, verdict: PermissionVerdict): Promise<void>;
  /** Requests cancellation; consume the run normally to receive the cancelled outcome. */
  cancel(): Promise<void>;
  /** Strictly steers this run. A late steer is refused and is never promoted. */
  steer(text: string): Promise<void>;
  /** Drains all remaining events and returns the typed terminal outcome. */
  result(): Promise<RunResult>;
}

export interface RunOperations {
  assertOpen(): void;
  readonly transportKind: TransportKind;
  send(frame: MessageInitShape<typeof ConverseRequestSchema>): void;
}

type ConsumptionMode = "events" | "result";
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

  async approve(askId: string, allow: boolean): Promise<void> {
    this.#operations.assertOpen();
    if (this.#knownAsks.has(askId)) {
      await this.resolveAsk(askId, allow ? "allow_once" : "deny");
      return;
    }
    this.#send({
      kind: {
        case: "resumeApproval",
        value: { allow, askId, expectedRunId: this.id },
      },
    });
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
          allow: verdict !== "deny",
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

  async result(): Promise<RunResult> {
    this.#claim("result");
    for (;;) {
      const next = await this.#next();
      if (next.done) break;
    }
    const event = this.#terminal;
    if (event === undefined) {
      throw new ProtocolError("The Converse stream ended without a terminal result", {
        transport: this.#operations.transportKind,
      });
    }
    return {
      content: event.payload.text,
      rawEvent: event,
      runId: this.id,
      sessionId: this.sessionId,
      stopReason: event.payload.stop,
      text: event.payload.text,
      usage: event.payload.usage ?? event.usage,
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
      return: async () => ({ done: true, value: undefined }),
    };
  }

  async #next(): Promise<IteratorResult<Event>> {
    this.#operations.assertOpen();
    if (this.#firstPending) {
      this.#firstPending = false;
      return { done: false, value: this.#first };
    }
    if (this.#terminal !== undefined) return { done: true, value: undefined };
    let next: IteratorResult<ProtoEvent>;
    try {
      next = await this.#events.next();
    } catch (error) {
      this.#end();
      throw error;
    }
    if (next.done) {
      this.#end();
      throw new ProtocolError("The Converse stream ended without a terminal result", {
        transport: this.#operations.transportKind,
      });
    }
    if (next.value.runId !== this.id) {
      throw new ProtocolError("The Converse stream changed run id", {
        transport: this.#operations.transportKind,
      });
    }
    const event = decodeEvent(next.value, this.#operations.transportKind);
    this.#observe(event);
    return { done: false, value: event };
  }

  #observe(event: Event): void {
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

export type ConverseFrame = MessageInitShape<typeof ConverseRequestSchema>;

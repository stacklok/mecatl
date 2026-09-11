import type {
  DescMessage,
  DescMethodStreaming,
  MessageInitShape,
  MessageShape,
} from "@bufbuild/protobuf";
import type { CallOptions } from "@connectrpc/connect";

import {
  InvalidStateError,
  PlanContinuationStartError,
  ProtocolError,
  type TransportKind,
} from "./errors.js";
import { decodeEvent, type Event, type EventOf, type PermissionAskEventPayload } from "./events.js";
import {
  HarnessService,
  PermissionMode,
  type Event as ProtoEvent,
} from "./gen/mecatl/v1/harness_pb.js";
import type { PermissionVerdict, RunResult } from "./run.js";

/** The plan-specific decisions accepted by session.resolvePlan() and onPlanApproval. @public */
export type PlanApprovalVerdict = "approve" | "accept_edits" | "iterate";

/** An automatic responder invoked only for a PresentPlan approval ask. @public */
export type PlanApprovalResponder = (
  ask: PermissionAskEventPayload,
  signal: AbortSignal,
) => PlanApprovalVerdict | undefined | Promise<PlanApprovalVerdict | undefined>;

/** The two ordered outcomes carried by one atomic plan-resolution stream. @public */
export interface PlanResolutionResult {
  readonly resumed: RunResult;
  readonly continuation?: RunResult;
}

/** One atomic, single-consumption resolution of a durably parked plan. @public */
export interface PlanResolution extends AsyncIterable<Event> {
  /**
   * Drains the merged stream and returns the resumed and optional continuation outcomes.
   *
   * @returns The resumed run and any continuation run started by approval.
   * @throws `InvalidStateError` when the resolution is already being consumed.
   * @throws `PlanContinuationStartError` when an approved continuation cannot start.
   */
  result(): Promise<PlanResolutionResult>;
}

export interface PlanResolutionOperations {
  assertOpen(): void;
  registerRun(cancel: () => Promise<void>): () => void;
  stream<I extends DescMessage, O extends DescMessage>(
    method: DescMethodStreaming<I, O>,
    input: AsyncIterable<MessageInitShape<I>>,
    options?: CallOptions,
  ): AsyncIterable<MessageShape<O>>;
  readonly transportKind: TransportKind;
}

type ConsumptionMode = "events" | "result";
type ResolutionPhase = "resumed" | "after-resumed" | "continuation" | "after-continuation";

export const PLAN_APPROVAL_TOOL = "PresentPlan";

// BEGIN MECATL_PLAN_APPROVED_PROCEED_TEXT
export const PLAN_APPROVED_PROCEED_TEXT = "Plan approved by operator. Proceed with execution.";
// END MECATL_PLAN_APPROVED_PROCEED_TEXT

export function planPermissionVerdict(verdict: PlanApprovalVerdict): PermissionVerdict {
  switch (verdict) {
    case "approve":
      return "allow_once";
    case "accept_edits":
      return "allow_always";
    case "iterate":
      return "deny";
  }
}

function planTargetMode(verdict: PlanApprovalVerdict, transport: TransportKind): PermissionMode {
  switch (verdict) {
    case "approve":
      return PermissionMode.DEFAULT;
    case "accept_edits":
      return PermissionMode.ACCEPT_EDITS;
    case "iterate":
      return PermissionMode.PLAN;
    default:
      throw new InvalidStateError(`Unknown plan approval verdict: ${String(verdict)}`, {
        transport,
      });
  }
}

class PlanResolutionImpl implements PlanResolution {
  readonly #abort: AbortController;
  readonly #events: AsyncIterator<ProtoEvent>;
  readonly #operations: PlanResolutionOperations;
  readonly #release: () => void;
  readonly #sessionId: string;
  #consumption: ConsumptionMode | undefined;
  #continuationId: string | undefined;
  #continuationTerminal: EventOf<"result"> | undefined;
  #ended = false;
  #phase: ResolutionPhase = "resumed";
  #resumedId: string | undefined;
  #resumedTerminal: EventOf<"result"> | undefined;

  constructor(
    sessionId: string,
    events: AsyncIterator<ProtoEvent>,
    operations: PlanResolutionOperations,
    abort: AbortController,
    release: () => void,
  ) {
    this.#sessionId = sessionId;
    this.#events = events;
    this.#operations = operations;
    this.#abort = abort;
    this.#release = release;
  }

  [Symbol.asyncIterator](): AsyncIterator<Event> {
    this.#claim("events");
    return {
      next: () => this.#next(),
      return: async () => {
        await this.close();
        return { done: true, value: undefined };
      },
    };
  }

  async result(): Promise<PlanResolutionResult> {
    this.#claim("result");
    for (;;) {
      const next = await this.#next();
      if (next.done) break;
    }
    const resumed = this.#resumedTerminal;
    const resumedId = this.#resumedId;
    if (resumed === undefined || resumedId === undefined) {
      throw this.#protocol("The ApprovePlan stream ended without the resumed run terminal");
    }
    const continuation = this.#continuationTerminal;
    const continuationId = this.#continuationId;
    return {
      resumed: runResult(this.#sessionId, resumedId, resumed),
      ...(continuation === undefined || continuationId === undefined
        ? {}
        : { continuation: runResult(this.#sessionId, continuationId, continuation) }),
    };
  }

  async close(): Promise<void> {
    if (this.#ended) return;
    this.#ended = true;
    this.#abort.abort();
    try {
      await this.#events.return?.();
    } catch {
      // Releasing an aborted transport stream is best-effort.
    } finally {
      this.#release();
    }
  }

  #claim(mode: ConsumptionMode): void {
    this.#operations.assertOpen();
    if (this.#consumption !== undefined) {
      throw new InvalidStateError(
        `Plan resolution events are already being consumed through ${this.#consumption}`,
        { transport: this.#operations.transportKind },
      );
    }
    this.#consumption = mode;
  }

  async #next(): Promise<IteratorResult<Event>> {
    this.#operations.assertOpen();
    if (this.#ended) return { done: true, value: undefined };

    let next: IteratorResult<ProtoEvent>;
    try {
      next = await this.#events.next();
      if (next.done) return this.#finishEOF();
      return { done: false, value: this.#observe(next.value) };
    } catch (error) {
      await this.close();
      throw error;
    }
  }

  #observe(raw: ProtoEvent): Event {
    switch (this.#phase) {
      case "resumed":
        return this.#observeResumed(raw);
      case "after-resumed":
        return this.#observeAfterResumed(raw);
      case "continuation":
        return this.#observeContinuation(raw);
      case "after-continuation":
        throw this.#protocol(
          "The ApprovePlan stream returned an event after the continuation terminal",
        );
    }
  }

  #observeResumed(raw: ProtoEvent): Event {
    if (raw.runId === "") {
      throw this.#protocol("The ApprovePlan stream began with an event that had no run id");
    }
    this.#resumedId ??= raw.runId;
    if (raw.runId !== this.#resumedId) {
      throw this.#protocol("The ApprovePlan continuation began before the resumed run terminal");
    }
    const event = decodeEvent(raw, this.#operations.transportKind);
    if (event.kind === "result") {
      this.#resumedTerminal = event;
      this.#phase = "after-resumed";
    }
    return event;
  }

  #observeAfterResumed(raw: ProtoEvent): Event {
    const resumed = this.#resumedTerminal;
    if (resumed === undefined) {
      throw this.#protocol("The ApprovePlan stream lost the resumed run terminal");
    }
    if (resumed.payload.stop !== "plan_approved") {
      throw this.#protocol("The ApprovePlan stream continued after a non-approving terminal");
    }
    if (raw.runId === "") {
      if (raw.type === "result" && raw.result?.stop === "error") {
        throw new PlanContinuationStartError(raw.result.error || raw.result.text, {
          transport: this.#operations.transportKind,
        });
      }
      throw this.#protocol("The ApprovePlan continuation event had no run id");
    }
    if (raw.runId === this.#resumedId) {
      throw this.#protocol(
        "The ApprovePlan stream returned another resumed-run event after its terminal",
      );
    }
    this.#continuationId = raw.runId;
    this.#phase = "continuation";
    return this.#observeContinuation(raw);
  }

  #observeContinuation(raw: ProtoEvent): Event {
    if (raw.runId !== this.#continuationId) {
      throw this.#protocol("The ApprovePlan stream changed to a third run id");
    }
    const event = decodeEvent(raw, this.#operations.transportKind);
    if (event.kind === "result") {
      this.#continuationTerminal = event;
      this.#phase = "after-continuation";
    }
    return event;
  }

  #finishEOF(): IteratorResult<Event> {
    const resumed = this.#resumedTerminal;
    if (resumed === undefined) {
      throw this.#protocol("The ApprovePlan stream ended without the resumed run terminal");
    }
    if (resumed.payload.stop === "plan_approved" && this.#continuationTerminal === undefined) {
      throw this.#protocol("The ApprovePlan stream ended without the continuation run terminal");
    }
    this.#ended = true;
    this.#release();
    return { done: true, value: undefined };
  }

  #protocol(message: string): ProtocolError {
    return new ProtocolError(message, { transport: this.#operations.transportKind });
  }
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

async function* singleValue<T>(value: T): AsyncGenerator<T> {
  yield value;
}

export function createPlanResolution(
  sessionId: string,
  verdict: PlanApprovalVerdict,
  operations: PlanResolutionOperations,
  onRelease: () => void,
): PlanResolution {
  const targetMode = planTargetMode(verdict, operations.transportKind);
  const abort = new AbortController();
  const events = operations
    .stream(HarnessService.method.approvePlan, singleValue({ sessionId, targetMode }), {
      signal: abort.signal,
    })
    [Symbol.asyncIterator]();
  let released = false;
  let unregister: () => void = () => undefined;
  const release = () => {
    if (released) return;
    released = true;
    unregister();
    onRelease();
  };
  const resolution = new PlanResolutionImpl(sessionId, events, operations, abort, release);
  unregister = operations.registerRun(() => resolution.close());
  return resolution;
}

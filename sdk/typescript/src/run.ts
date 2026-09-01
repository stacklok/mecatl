import type { MessageInitShape } from "@bufbuild/protobuf";

import { InvalidStateError, ProtocolError, type TransportKind } from "./errors.js";
import { decodeEvent, type Event, type EventOf, type EventUsage } from "./events.js";
import type { ConverseRequestSchema, Event as ProtoEvent } from "./gen/mecatl/v1/harness_pb.js";

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
  /** Sends a permission verdict for a raw permission.ask event. Scenario 7 adds responders. */
  approve(askId: string, allow: boolean): Promise<void>;
  /** Requests cancellation; consume the run normally to receive the cancelled outcome. */
  cancel(): Promise<void>;
  /** Strictly steers this run. A late steer is refused and is never promoted. */
  steer(text: string): Promise<void>;
  /** Drains all remaining events and returns the typed terminal outcome. */
  result(): Promise<RunResult>;
}

export interface RunOperations {
  readonly transportKind: TransportKind;
  send(frame: MessageInitShape<typeof ConverseRequestSchema>): void;
}

type ConsumptionMode = "events" | "result";
export class RunImpl implements Run {
  readonly id: string;
  readonly sessionId: string;

  readonly #events: AsyncIterator<ProtoEvent>;
  readonly #first: Event;
  readonly #operations: RunOperations;
  #consumption: ConsumptionMode | undefined;
  #firstPending = true;
  #terminal: EventOf<"result"> | undefined;
  #steerSequence = 0;

  constructor(
    sessionId: string,
    runId: string,
    first: ProtoEvent,
    events: AsyncIterator<ProtoEvent>,
    operations: RunOperations,
  ) {
    this.id = runId;
    this.sessionId = sessionId;
    this.#first = decodeEvent(first, operations.transportKind);
    this.#events = events;
    this.#operations = operations;
    if (terminal(this.#first)) this.#terminal = this.#first;
  }

  async approve(askId: string, allow: boolean): Promise<void> {
    this.#send({
      kind: {
        case: "resumeApproval",
        value: { allow, askId, expectedRunId: this.id },
      },
    });
  }

  async cancel(): Promise<void> {
    this.#send({
      kind: { case: "cancel", value: { expectedRunId: this.id } },
    });
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
    if (this.#firstPending) {
      this.#firstPending = false;
      return { done: false, value: this.#first };
    }
    if (this.#terminal !== undefined) return { done: true, value: undefined };
    const next = await this.#events.next();
    if (next.done) {
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
    if (terminal(event)) this.#terminal = event;
    return { done: false, value: event };
  }

  #send(frame: MessageInitShape<typeof ConverseRequestSchema>): void {
    this.#operations.send(frame);
  }
}

function terminal(event: Event): event is EventOf<"result"> {
  return event.kind === "result";
}

export type ConverseFrame = MessageInitShape<typeof ConverseRequestSchema>;
